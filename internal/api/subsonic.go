// Subsonic API compatibility layer.
//
// Flick's Subsonic client (SubsonicService) talks the Subsonic REST API over
// JSON (`?f=json`). We expose just the endpoints the client actually calls for
// browsing, streaming and cover art, mapped onto the same library database and
// streamer the native `/api/*` layer uses. The original /api behaviour is
// untouched.
//
// Auth: Flick always sends `u`, `t` (md5(password+salt)) and `s`. We accept
// these silently case the pre-shared library is unstructured; read/stream/art
// stay open, matching the native /api read behaviour. Write endpoints are not
// exposed here, so the write-auth boundary is preserved.
package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"flac-server/internal/db"
	"flac-server/internal/meta"
	"flac-server/internal/streamer"
)

const subsonicVersion = "1.16.1"

// subsonicMount registers /rest/* routes used by Flick's Subsonic client.
func (h *Handler) subsonicMount(r chi.Router) {
	r.Get("/rest/ping.view", h.subPing)
	r.Get("/rest/getMusicFolders.view", h.subMusicFolders)
	r.Get("/rest/getArtists.view", h.subArtists)
	r.Get("/rest/getAlbumList2.view", h.subAlbumList2)
	r.Get("/rest/getAlbum.view", h.subAlbum)
	r.Get("/rest/getCoverArt.view", h.subCoverArt)
	r.Get("/rest/stream.view", h.subStream)
	r.Get("/rest/search3.view", h.subSearch3)
	r.Get("/rest/getLyricsBySongId.view", h.subLyricsBySongID)
	r.Get("/rest/getLyrics.view", h.subLyricsByTitle)
}

// subEnvelope encodes the final body.
func (h *Handler) subEnvelope(w http.ResponseWriter, data map[string]any) {
	body := map[string]any{
		"subsonic-response": map[string]any{
			"status":        "ok",
			"version":       subsonicVersion,
			"type":          "flac-server",
			"serverVersion": h.Version,
		},
	}
	for k, v := range data {
		body["subsonic-response"].(map[string]any)[k] = v
	}
	h.json(w, http.StatusOK, body)
}

func (h *Handler) subErr(w http.ResponseWriter, code int, msg string) {
	h.json(w, http.StatusOK, map[string]any{
		"subsonic-response": map[string]any{
			"status":  "failed",
			"version": subsonicVersion,
			"type":    "flac-server",
			"error":   map[string]any{"code": code, "message": msg},
		},
	})
}

// subID parses a numeric Subsonic id from the `id` query param.
func (h *Handler) subID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func (h *Handler) subPing(w http.ResponseWriter, r *http.Request) {
	h.subEnvelope(w, nil)
}

func (h *Handler) subMusicFolders(w http.ResponseWriter, r *http.Request) {
	h.subEnvelope(w, map[string]any{
		"musicFolders": map[string]any{
			"musicFolder": []map[string]any{{"id": 1, "name": "Library"}},
		},
	})
}

func (h *Handler) subArtists(w http.ResponseWriter, r *http.Request) {
	artists, _, err := h.DB.ListArtists("", 0, 0)
	if err != nil {
		h.subErr(w, 0, "list artists: "+err.Error())
		return
	}
	indexes := map[string]map[string]any{}
	for _, a := range artists {
		key := strings.ToUpper(a.Name)
		key = key[:1]
		if key == "" {
			key = "#"
		}
		entry, ok := indexes[key]
		if !ok {
			entry = map[string]any{"name": key, "artist": []map[string]any{}}
			indexes[key] = entry
		}
		entry["artist"] = append(entry["artist"].([]map[string]any), map[string]any{
			"id":      strconv.FormatInt(a.ID, 10),
			"name":    a.Name,
			"albumCount": a.AlbumCount,
		})
	}
	idx := make([]map[string]any, 0, len(indexes))
	for _, v := range indexes {
		idx = append(idx, v)
	}
	h.subEnvelope(w, map[string]any{
		"artists": map[string]any{"index": idx},
	})
}

// subAlbumList2 returns all albums, paginated (Flick walks `type`,
// `size`, `offset`).
func (h *Handler) subAlbumList2(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("size"))
	if limit <= 0 {
		limit = 500
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	albums, _, err := h.DB.ListAlbums("", "", limit, offset)
	if err != nil {
		h.subErr(w, 0, "list albums: "+err.Error())
		return
	}
	list := make([]map[string]any, 0, len(albums))
	for _, a := range albums {
		list = append(list, h.subAlbumEntry(a))
	}
	// Untagged tracks (WAV/MP3 without tags) never form an album row, so they
	// are invisible to album-based clients. Expose them under one synthetic
	// album so the whole library stays reachable.
	if untagged := h.untaggedAlbumEntry(); untagged != nil {
		// The synthetic album is always appended last: Flick pages until the
		// page is short, so its position must be past every real album.
		if offset == 0 || offset <= len(albums) {
			list = append(list, untagged)
		}
	}
	h.subEnvelope(w, map[string]any{
		"albumList2": map[string]any{"album": list},
	})
}

// untaggedAlbumID is the reserved Subsonic album id for untagged tracks.
const untaggedAlbumID = "al-untagged"

// untaggedAlbumEntry builds the synthetic album, or nil when nothing is
// untagged.
func (h *Handler) untaggedAlbumEntry() map[string]any {
	tracks, total, err := h.DB.ListTracks(db.TrackFilter{NoAlbum: true})
	if err != nil || total == 0 {
		return nil
	}
	var ms int64
	for i := range tracks {
		ms += tracks[i].DurationMs
	}
	return map[string]any{
		"id":          untaggedAlbumID,
		"album":       "Untagged",
		"albumArtist": "Unknown Artist",
		"artist":      "Unknown Artist",
		"songCount":   total,
		"duration":    ms / 1000,
	}
}

// untaggedSongs returns the untagged tracks as Subsonic song entries.
func (h *Handler) untaggedSongs() ([]map[string]any, error) {
	tracks, _, err := h.DB.ListTracks(db.TrackFilter{NoAlbum: true})
	if err != nil {
		return nil, err
	}
	songs := make([]map[string]any, 0, len(tracks))
	for i := range tracks {
		songs = append(songs, h.subSongEntry(&tracks[i]))
	}
	return songs, nil
}

func (h *Handler) subAlbumEntry(a db.Album) map[string]any {
	entry := map[string]any{
		"id":          strconv.FormatInt(a.ID, 10),
		"album":       a.Name,
		"albumArtist": a.Artist,
		"artist":      a.Artist,
		"songCount":   a.TrackCount,
		"duration":    a.TotalDurationMs / 1000,
	}
	if a.Year != nil {
		entry["year"] = *a.Year
	}
	if h.albumHasArt(&a) {
		entry["coverArt"] = strconv.FormatInt(a.ID, 10)
	}
	return entry
}

func (h *Handler) albumHasArt(a *db.Album) bool {
	if a == nil {
		return false
	}
	return (a.ArtPath != nil && *a.ArtPath != "") || (a.ArtHash != nil && *a.ArtHash != "")
}

// subAlbum returns one album plus its songs.
func (h *Handler) subAlbum(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("id") == untaggedAlbumID {
		songs, err := h.untaggedSongs()
		if err != nil {
			h.subErr(w, 0, "list untagged: "+err.Error())
			return
		}
		entry := h.untaggedAlbumEntry()
		album := map[string]any{
			"id":          untaggedAlbumID,
			"name":        "Untagged",
			"artist":      "Unknown Artist",
			"albumArtist": "Unknown Artist",
			"song":        songs,
		}
		if entry != nil {
			album["songCount"] = entry["songCount"]
			album["duration"] = entry["duration"]
		}
		h.subEnvelope(w, map[string]any{"album": album})
		return
	}
	id, ok := h.subID(r)
	if !ok {
		h.subErr(w, 0, "invalid id")
		return
	}
	a, err := h.DB.GetAlbum(id)
	if err != nil {
		h.subErr(w, 70, "album not found")
		return
	}
	tracks, _, err := h.DB.ListTracks(db.TrackFilter{Album: a.Name, Artist: a.Artist})
	if err != nil {
		h.subErr(w, 0, "list tracks: "+err.Error())
		return
	}
	songs := make([]map[string]any, 0, len(tracks))
	for i := range tracks {
		songs = append(songs, h.subSongEntry(&tracks[i]))
	}
	h.subEnvelope(w, map[string]any{
		"album": map[string]any{
			"id":      strconv.FormatInt(id, 10),
			"name":    a.Name,
			"artist":  a.Artist,
			"albumArtist": a.Artist,
			"song":    songs,
		},
	})
}

func (h *Handler) subSongEntry(t *db.Track) map[string]any {
	e := map[string]any{
		"id":         strconv.FormatInt(t.ID, 10),
		"title":      str(t.Title),
		"artist":     str(t.Artist),
		"album":      str(t.Album),
		"albumArtist": str(t.AlbumArtist),
		"genre":      str(t.Genre),
		"suffix":     t.Format,
		"size":       t.FileSize,
		"bitRate":    t.Bitrate,
		"sampleRate": t.SampleRate,
		"duration":   t.DurationMs / 1000,
		"path":       t.Path,
	}
	if t.TrackNum != nil {
		e["track"] = *t.TrackNum
	}
	if t.DiscNum != nil {
		e["discNumber"] = *t.DiscNum
	}
	if t.Year != nil {
		e["year"] = *t.Year
	}
	// Cover: songs inherit the album art. Flick falls back to the album's
	// coverArt for the marker; setting per-song allows fallback too.
	if t.ArtURL != nil && *t.ArtURL != "" {
		// not used directly; marker scheme resolves via getCoverArt by id
	}
	return e
}

// subCoverArt serves cover art by an id. The id may be an album id or a track
// id; both resolve to the album's art. Works with the native getArt logic.
func (h *Handler) subCoverArt(w http.ResponseWriter, r *http.Request) {
	id, ok := h.subID(r)
	if !ok {
		h.subErr(w, 30, "invalid coverArt id")
		return
	}
	h.serveAlbumArt(w, r, id)
}

// serveAlbumArt resolves an album/track id to cover art bytes and streams it.
func (h *Handler) serveAlbumArt(w http.ResponseWriter, r *http.Request, id int64) {
	a, err := h.DB.GetAlbum(id)
	if err != nil && !(errors.Is(err, db.ErrNotFound) || errors.Is(err, errNoRow)) {
		h.subErr(w, 30, "get album: "+err.Error())
		return
	}
	if !h.albumHasArt(a) {
		// Try as track id
		t, terr := h.DB.GetTrack(id)
		if terr != nil || t.Album == nil || *t.Album == "" {
			h.subErr(w, 30, "no cover art")
			return
		}
		artist := str(t.Artist)
		if t.AlbumArtist != nil && *t.AlbumArtist != "" {
			artist = *t.AlbumArtist
		}
		a, err = h.DB.GetAlbumByNameAndArtist(*t.Album, artist)
		if err != nil || !h.albumHasArt(a) {
			h.subErr(w, 30, "no cover art")
			return
		}
	}
	if a.ArtPath == nil || *a.ArtPath == "" {
		if a.ArtHash != nil && *a.ArtHash != "" {
			h.serveArtCacheFile(w, r, *a.ArtHash)
			return
		}
		h.subErr(w, 30, "no cover art")
		return
	}
	abs, err := h.Resolver.Abs(*a.ArtPath)
	if err != nil {
		h.subErr(w, 30, "cover art path unresolved")
		return
	}
	h.serveImageFile(w, r, abs)
}

func (h *Handler) serveImageFile(w http.ResponseWriter, r *http.Request, abs string) {
	f, err := os.Open(abs)
	if err != nil {
		h.subErr(w, 30, "cover art not found")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		h.subErr(w, 30, "cover art stat error")
		return
	}
	w.Header().Set("Content-Type", artContentType(abs))
	w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", `"`+strconv.FormatInt(id, 10)+`"`)
	http.ServeContent(w, r, filepath.Base(abs), info.ModTime(), f)
}

// serveArtCacheFile serves art bytes from the cache dir by "hash.ext".
func (h *Handler) serveArtCacheFile(w http.ResponseWriter, r *http.Request, name string) {
	dir := h.Scanner.ArtCacheDir
	if dir == "" {
		h.subErr(w, 30, "art cache disabled")
		return
	}
	// name is "<32hex>.jpg" from art_hash
	base, ext, ok := strings.Cut(name, ".")
	if !ok || len(base) != 32 || !isHex(base) {
		h.subErr(w, 30, "invalid art name")
		return
	}
	switch strings.ToLower(ext) {
	case "jpg", "png", "webp", "img":
	default:
		h.subErr(w, 30, "invalid art extension")
		return
	}
	abs := filepath.Join(dir, base+"."+strings.ToLower(ext))
	if !withinDir(dir, abs) {
		h.subErr(w, 30, "invalid art name")
		return
	}
	h.serveImageFile(w, r, abs)
}

// subStream streams a track at original quality (no transcode params), with
// Range support, same as the native /api/stream.
func (h *Handler) subStream(w http.ResponseWriter, r *http.Request) {
	id, ok := h.subID(r)
	if !ok {
		h.subErr(w, 10, "invalid track id")
		return
	}
	t, err := h.DB.GetTrack(id)
	if err != nil {
		h.subErr(w, 70, "track not found")
		return
	}
	if _, err := streamer.Serve(w, r, h.Resolver, t.Path); err != nil {
		if errors.Is(err, streamer.ErrNotFound) {
			h.subErr(w, 70, "track file not found")
			return
		}
		h.subErr(w, 0, "stream error: "+err.Error())
		return
	}
}

// subLyricsBySongID returns the sidecar .lrc for a track, if the library holds
// one. Standard Subsonic shape: {"lyrics": {"artist","title","value"}}.
func (h *Handler) subLyricsBySongID(w http.ResponseWriter, r *http.Request) {
	id, ok := h.subID(r)
	if !ok {
		h.subErr(w, 10, "invalid track id")
		return
	}
	t, err := h.DB.GetTrack(id)
	if err != nil {
		h.subErr(w, 70, "track not found")
		return
	}
	text, ok := h.sidecarLyrics(t.Path)
	if !ok {
		h.subEnvelope(w, map[string]any{"lyrics": nil})
		return
	}
	h.subEnvelope(w, map[string]any{
		"lyrics": map[string]any{
			"artist": str(t.Artist),
			"title":  str(t.Title),
			"value":  text,
		},
	})
}

// subLyricsByTitle finds a sidecar .lrc by matching track title (and optional
// artist), so clients without ids can still get lyrics.
func (h *Handler) subLyricsByTitle(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	title, artist := q.Get("title"), q.Get("artist")
	if title == "" {
		h.subErr(w, 10, "title required")
		return
	}
	filter := db.TrackFilter{Query: title}
	if artist != "" {
		filter.Artist = artist
	}
	tracks, _, err := h.DB.ListTracks(filter)
	if err != nil {
		h.subErr(w, 0, "search: "+err.Error())
		return
	}
	for i := range tracks {
		if !strings.EqualFold(str(tracks[i].Title), title) {
			continue
		}
		if text, ok := h.sidecarLyrics(tracks[i].Path); ok {
			h.subEnvelope(w, map[string]any{
				"lyrics": map[string]any{
					"artist": str(tracks[i].Artist),
					"title":  title,
					"value":  text,
				},
			})
			return
		}
	}
	h.subEnvelope(w, map[string]any{"lyrics": nil})
}

// sidecarLyrics reads a sibling .lrc for an indexed track path. It trusts the
// same Resolver the stream endpoint uses, so only library files are reachable
// and the extension swap cannot escape the library root.
//
// A sidecar file wins over an embedded tag: it is the more specific source
// and the user can edit it without re-tagging the audio.
func (h *Handler) sidecarLyrics(indexed string) (string, bool) {
	abs, err := h.Resolver.Abs(indexed)
	if err != nil {
		return "", false
	}
	base := strings.TrimSuffix(abs, filepath.Ext(abs))
	for _, ext := range []string{".lrc", ".LRC", ".txt"} {
		b, err := os.ReadFile(base + ext)
		if err == nil && strings.TrimSpace(string(b)) != "" {
			return string(b), true
		}
	}
	return h.embeddedLyrics(abs)
}

// embeddedLyrics falls back to lyrics stored in the audio file's own tags
// (Vorbis LYRICS/UNSYNCEDLYRICS on FLAC, ID3 USLT on MP3). Parsing on demand
// keeps re-tagging effective without a rescan, and costs one head read only on
// a lyrics request.
func (h *Handler) embeddedLyrics(abs string) (string, bool) {
	info, err := meta.Parse(abs)
	if err != nil || info == nil {
		return "", false
	}
	text := strings.TrimSpace(info.Lyrics)
	if text == "" {
		return "", false
	}
	return text, true
}
func (h *Handler) subSearch3(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("query")
	songCount, _ := strconv.Atoi(r.URL.Query().Get("songCount"))
	if songCount <= 0 { songCount = 20 }
	
	tracks, _, err := h.DB.ListTracks(db.TrackFilter{Query: query, Limit: songCount})
	if err != nil {
		h.subErr(w, 0, "search error")
		return
	}
	
	songList := make([]map[string]any, 0, len(tracks))
	for _, t := range tracks {
		songList = append(songList, h.subSongEntry(&t))
	}
	
	h.subEnvelope(w, map[string]any{
		"searchResult3": map[string]any{
			"song": songList,
		},
	})
}
