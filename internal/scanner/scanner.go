// Package scanner walks the music root and keeps the database in sync.
package scanner

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"flac-server/internal/db"
	"flac-server/internal/meta"
)

// Scanner indexes one or more music roots into SQLite.
//
// Indexed paths are root-relative and carry the root name as first segment
// ("library/Artist/Album/01.flac") so two roots can hold identical layouts
// without colliding on the UNIQUE path column.
type Scanner struct {
	DB         *db.DB
	Roots      map[string]string // root name -> absolute path
	Extensions []string
	MaxMtime   map[string]int64 // path -> indexed mtime, filled at scan start
	ArtCacheDir string         // where embedded cover art is extracted to
	Log        *slog.Logger

	// pendingArt holds "album name\x00album artist" -> "hash.ext" for files
	// whose embedded art was written during the walk. Album rows do not exist
	// until RebuildAggregates, so the DB write is deferred to scanEmbeddedArt.
	pendingArt map[string]string
}

// artExt maps a PICTURE block's MIME type to a file extension. Unknown types
// get .img; artContentType serves anything unknown as a JPEG.
var artExt = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

// Result summarizes one scan.
type Result struct {
	FilesScanned int
	Added        int
	Updated      int
	Removed      int
}

// New builds a Scanner. Roots that do not exist are logged and skipped later.
// artCacheDir may be empty, which disables embedded art extraction entirely.
func New(log *slog.Logger, d *db.DB, roots map[string]string, exts []string, artCacheDir string) *Scanner {
	if len(exts) == 0 {
		exts = []string{".flac", ".mp3"}
	}
	if len(roots) == 0 {
		roots = map[string]string{"library": "/music"}
	}
	return &Scanner{
		DB:          d,
		Roots:       roots,
		Extensions:  normalizeExt(exts),
		MaxMtime:    map[string]int64{},
		ArtCacheDir: artCacheDir,
		Log:         log,
		pendingArt:  map[string]string{},
	}
}

// storeEmbeddedArt writes a file's embedded PICTURE block into the art cache
// and records the album it belongs to for scanEmbeddedArt to persist.
//
// The name is a hash of the album identity plus the picture length, so all
// tracks of one album share a single file instead of writing N identical
// copies; only the first track of an album actually writes. Every failure is
// logged and dropped: art is decoration, and a missing cover must never cost
// us the track it came with.
func (s *Scanner) storeEmbeddedArt(t *db.Track, m *meta.Info) {
	if s.ArtCacheDir == "" || len(m.Picture) == 0 {
		return
	}
	album, artist := deref(t.Album), deref(t.AlbumArtist)
	if artist == "" {
		artist = deref(t.Artist)
	}
	if album == "" {
		return // no album: there is no row to attach the art to
	}
	key := album + "\x00" + artist
	if _, done := s.pendingArt[key]; done {
		return // this album's art is already written this scan
	}
	s.pendingArt[key] = "" // claim it so a later track cannot rewrite it

	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", album, artist, len(m.Picture))))
	ext, ok := artExt[strings.ToLower(m.PictureMIME)]
	if !ok {
		ext = ".img"
	}
	name := hex.EncodeToString(sum[:16]) + ext
	dst := filepath.Join(s.ArtCacheDir, name)
	if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
		s.pendingArt[key] = name // cached from an earlier scan
		return
	}
	if err := os.MkdirAll(s.ArtCacheDir, 0o755); err != nil {
		s.Log.Warn("art cache dir unavailable", "dir", s.ArtCacheDir, "err", err)
		return
	}
	// Write to a temp name and rename: a reader must never see a half-written
	// image, and a crash leaves no truncated cache file.
	tmp := dst + ".part"
	if err := os.WriteFile(tmp, m.Picture, 0o644); err != nil {
		s.Log.Warn("write cover art failed", "path", t.Path, "err", err)
		os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, dst); err != nil {
		s.Log.Warn("commit cover art failed", "path", t.Path, "err", err)
		os.Remove(tmp)
		return
	}
	s.pendingArt[key] = name
}

// scanEmbeddedArt persists the art hashes collected during the walk. It runs
// after RebuildAggregates so the album rows exist, and before scanArt so a
// folder image still wins: art_path is preferred by the art_url CASE.
func (s *Scanner) scanEmbeddedArt() {
	for key, name := range s.pendingArt {
		if name == "" {
			continue
		}
		album, artist, _ := strings.Cut(key, "\x00") // NUL separator, see storeEmbeddedArt
		if err := s.DB.SetAlbumArtHash(album, artist, name); err != nil {
			s.Log.Warn("set album art hash failed", "album", album, "err", err)
		}
	}
	s.pendingArt = map[string]string{}
}

// deref returns the value behind a tag pointer, or "" when the tag is absent.
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func normalizeExt(in []string) []string {
	out := make([]string, 0, len(in))
	for _, e := range in {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		out = append(out, e)
	}
	return out
}

func (s *Scanner) wants(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, e := range s.Extensions {
		if ext == e {
			return true
		}
	}
	return false
}

// Scan walks every root, upserts new or changed files, drops vanished rows and
// rebuilds artist/album/genre aggregates. One scan runs at a time.
func (s *Scanner) Scan() (*Result, error) {
	known, err := s.DB.AllTrackPaths()
	if err != nil {
		return nil, err
	}
	res := &Result{}
	seen := make(map[string]bool, len(known))
	// Seed mtimes so unchanged files skip re-parsing on periodic rescans.
	if mtimes, err := s.DB.AllTrackMtimes(); err == nil {
		s.MaxMtime = mtimes
	}

	for rootName, root := range s.Roots {
		if _, err := os.Stat(root); err != nil {
			s.Log.Warn("music root unavailable", "root", rootName, "path", root, "err", err)
			continue
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				// Unreadable dir or file: log once, keep walking.
				s.Log.Warn("walk error", "path", path, "err", err)
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				// Skip hidden dirs and @eaDir / .thumbnails noise.
				name := d.Name()
				if path != root && (strings.HasPrefix(name, ".") || name == "@eaDir") {
					return fs.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || !s.wants(d.Name()) {
				return nil
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				rel = path
			}
			rel = rootName + "/" + filepath.ToSlash(rel)
			seen[rel] = true
			res.FilesScanned++
			s.indexOne(path, rel, d, known, res)
			return nil
		})
		if err != nil {
			return res, err
		}
	}

	// Rows whose file vanished. Only paths under our roots are considered.
	var missing []string
	for p := range known {
		if !seen[p] {
			missing = append(missing, p)
		}
	}
	if n, err := s.DB.RemoveMissingPaths(missing); err != nil {
		s.Log.Error("remove missing failed", "err", err)
	} else {
		res.Removed = n
	}

	if err := s.DB.RebuildAggregates(); err != nil {
		return res, err
	}
	s.scanEmbeddedArt()
	s.scanArt()
	return res, nil
}

// coverNames are the album image filenames we accept, in priority order.
var coverNames = []string{
	"cover.jpg", "cover.jpeg", "cover.png", "folder.jpg", "folder.png",
	"front.jpg", "album.jpg", "albumart.jpg", "thumb.jpg",
}

// coverPriority maps a lowercased filename to its rank in coverNames so a
// folder holding both front.jpg and cover.jpg picks cover.jpg.
var coverPriority = func() map[string]int {
	m := make(map[string]int, len(coverNames))
	for i, n := range coverNames {
		m[n] = i
	}
	return m
}()

// scanArt sets or clears albums.art_path. Covers are best-effort: a failure
// here is logged and never fails the scan, and a library with no images at all
// costs one query and no writes.
func (s *Scanner) scanArt() {
	albums, err := s.DB.ListAlbumArt()
	if err != nil {
		s.Log.Error("album art list failed", "err", err)
		return
	}
	for _, a := range albums {
		rel := s.findCover(a.Sample)
		if rel == a.ArtPath {
			continue // unchanged: no write, and no rewrite of a known cover
		}
		if err := s.DB.SetAlbumArt(a.ID, rel); err != nil {
			s.Log.Error("set album art failed", "album", a.ID, "err", err)
		}
	}
}

// findCover returns the root-relative path of the first cover image found in
// the album's own directory or any parent below the root, or "" when none
// exists. Walking up lets Artist/Album/cover.jpg cover a sibling CD1 folder.
func (s *Scanner) findCover(sample string) string {
	if sample == "" {
		return ""
	}
	name, rest, ok := strings.Cut(sample, "/")
	if !ok {
		return ""
	}
	root, okRoot := s.Roots[name]
	if !okRoot {
		return ""
	}
	// Indexed paths are slash-separated; the walk needs the native separators.
	rel := filepath.FromSlash(rest)
	dir := filepath.Dir(rel)
	for {
		if hit := s.coverInDir(filepath.Join(root, dir)); hit != "" {
			if r, err := filepath.Rel(root, hit); err == nil {
				return name + "/" + filepath.ToSlash(r)
			}
			return ""
		}
		if dir == "." || dir == string(filepath.Separator) {
			return "" // walked past the root
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// coverInDir returns the first cover image in dir, matched case-insensitively
// so Cover.JPG and cover.jpg both work. The dir listing is one ReadDir per
// directory rather than one stat per candidate name.
func (s *Scanner) coverInDir(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "" // no dir or unreadable: keep scanning, covers are optional
	}
	best := len(coverNames)
	hit := ""
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if rank, ok := coverPriority[strings.ToLower(e.Name())]; ok && rank < best {
			best, hit = rank, e.Name()
		}
	}
	if hit == "" {
		return ""
	}
	return filepath.Join(dir, hit) // keep on-disk case so the stored path resolves
}

func (s *Scanner) indexOne(abs, rel string, d fs.DirEntry, known map[string]bool, res *Result) {
	info, err := d.Info()
	if err != nil {
		s.Log.Warn("stat failed", "path", abs, "err", err)
		return
	}
	_, existed := known[rel]

	// Skip unchanged files on a rescan (mtime seconds granularity).
	if existed && s.MaxMtime[rel] == info.ModTime().Unix() && s.MaxMtime[rel] != 0 {
		return
	}

	m, err := meta.Parse(abs)
	if err != nil {
		if !errors.Is(err, meta.ErrUnsupported) {
			s.Log.Warn("parse failed", "path", rel, "err", err)
		}
		return
	}

	t := &db.Track{
		Path:          rel,
		Filename:      d.Name(),
		Title:         m.Title,
		Artist:        m.Artist,
		Album:         m.Album,
		AlbumArtist:   m.AlbumArtist,
		Composer:      m.Composer,
		Genre:         m.Genre,
		Year:          m.Year,
		TrackNum:      m.TrackNum,
		DiscNum:       m.DiscNum,
		DurationMs:    m.DurationMs,
		SampleRate:    m.SampleRate,
		BitsPerSample: m.BitsPerSample,
		Channels:      m.Channels,
		Bitrate:       m.Bitrate,
		Format:        m.Format,
		FileSize:      info.Size(),
		Mtime:         info.ModTime().Unix(),
	}
	fillFromDir(t)
	s.storeEmbeddedArt(t, m)
	inserted, err := s.DB.UpsertTrack(t)
	if err != nil {
		s.Log.Error("upsert failed", "path", rel, "err", err)
		return
	}
	s.MaxMtime[rel] = t.Mtime
	if inserted {
		res.Added++
	} else {
		res.Updated++
	}
}

// fillFromDir fills the album and artist fields a WAV or an untagged FLAC left
// blank from the folder layout. Without this an untagged library has no albums
// at all, so album cover art can never be found or shown. Real tags always win:
// only empty fields are filled, and only when the file sits at least two
// levels deep (root/artist/album/file) so a loose file is not made an "album"
// of its own.
func fillFromDir(t *db.Track) {
	_, rest, ok := strings.Cut(t.Path, "/")
	if !ok {
		return
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 3 {
		return // root/file or root/subdir/file: no artist/album to infer
	}
	dir := parts[:len(parts)-1]
	albumDir, artistDir := dir[len(dir)-1], dir[len(dir)-2]
	if t.Album == nil || *t.Album == "" {
		t.Album = &albumDir
	}
	if t.AlbumArtist == nil || *t.AlbumArtist == "" {
		if len(dir) >= 3 {
			t.AlbumArtist = &artistDir
		}
	}
	if t.Artist == nil || *t.Artist == "" {
		if t.AlbumArtist != nil {
			t.Artist = t.AlbumArtist
		} else {
			t.Artist = &artistDir
		}
	}
}

// Periodic runs Scan every interval until ctx-like stop channel closes.
func (s *Scanner) Periodic(interval int, stop <-chan struct{}, onDone func(*Result, error)) {
	if interval <= 0 {
		interval = 300
	}
	t := time.NewTicker(time.Duration(interval) * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			start := time.Now()
			res, err := s.Scan()
			if err != nil {
				s.Log.Error("scan failed", "err", err)
			} else {
				s.Log.Info("scan done", "scanned", res.FilesScanned, "added", res.Added,
					"updated", res.Updated, "removed", res.Removed,
					"dur", time.Since(start).String())
			}
			if onDone != nil {
				onDone(res, err)
			}
		}
	}
}
