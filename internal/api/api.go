// Package api contains the HTTP layer: routing, handlers and middleware.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"flac-server/internal/config"
	"flac-server/internal/db"
	"flac-server/internal/scanner"
	"flac-server/internal/streamer"
	"flac-server/internal/yt"
)

// Options carries runtime settings shared by all handlers.
type Options struct {
	MaxFileSize    int64
	AllowedFormats map[string]bool
	TempDir        string
	WritableRoot   string
	WritableName   string
	AuthEnabled    bool
	TrustedDomain  string
	SessionTTL     time.Duration
	LocalUsers     []config.User
}

type SessionInfo struct {
	Username string
	Role     string // "admin" or "guest"
	Expiry   time.Time
}

// Handler holds dependencies for every endpoint.
type Handler struct {
	DB        *db.DB
	Scanner   *scanner.Scanner
	Resolver  streamer.Resolver
	Opt       Options
	Log       *slog.Logger
	Version   string
	BuildTime string
	Started   time.Time
	YT        *yt.Service

	// mu guards sessions and loginFails.
	mu         sync.Mutex
	sessions   map[string]SessionInfo // cookie value -> session info
	loginFails map[string]*failWindow

	scan scanState
}

// failWindow tracks consecutive failed logins from one IP.
type failWindow struct {
	count int
	start time.Time
}

// maxLoginFails is how many wrong passwords one IP may submit per window.
const maxLoginFails = 5

// loginWindow is how long the failed-attempt counter lives.
const loginWindow = 5 * time.Minute

// New builds a Handler.
func New(d *db.DB, sc *scanner.Scanner, res streamer.Resolver, opt Options, log *slog.Logger, ver, bt string) *Handler {
	if opt.SessionTTL <= 0 {
		opt.SessionTTL = 24 * time.Hour
	}
	return &Handler{
		DB: d, Scanner: sc, Resolver: res, Opt: opt, Log: log,
		Version: ver, BuildTime: bt, Started: time.Now(),
		YT:         yt.NewService(),
		sessions:   make(map[string]SessionInfo),
		loginFails: make(map[string]*failWindow),
	}
}

// Mount registers every route on r.
func (h *Handler) Mount(r chi.Router) {
	// CORS for external domain access
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"https://music.alphared26.web.id", "https://*.alphared26.web.id", "http://localhost:8082"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-API-Key", "Range", "If-Range", "X-Requested-With"},
		ExposedHeaders:   []string{"Content-Range", "Accept-Ranges", "Content-Length", "Content-Type"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// Compress responses
	r.Use(middleware.Compress(5))

	r.Get("/api/health", h.health)
	r.Get("/api/library/stats", h.stats)

	r.Get("/api/tracks", h.listTracks)
	r.Get("/api/tracks/{id}", h.getTrack)
	r.Get("/api/stream/{id}", h.streamTrack)

	r.Get("/api/albums", h.listAlbums)
	r.Get("/api/albums/{id}", h.getAlbum)
	r.Get("/api/art/{id}", h.getArt)
	r.Get("/api/artfile/{name}", h.getArtFile)
	r.Get("/api/artists", h.listArtists)
	r.Get("/api/artists/{id}", h.getArtist)
	r.Get("/api/genres", h.listGenres)

	r.Get("/api/scan/{id}", h.scanStatus)

	// YouTube Music streaming & search (open/guest/admin)
	r.Get("/api/yt/search", h.ytSearch)
	r.Get("/api/yt/stream/{id}", h.ytStream)

	// Admin-only mutating operations (guest cannot upload, delete, edit, or scan)
	r.Group(func(r chi.Router) {
		r.Use(h.requireAdmin)
		r.Post("/api/scan", h.triggerScan)
		r.Post("/api/yt/download/{id}", h.ytDownload)
		r.Post("/api/upload", h.upload)
		r.Post("/api/upload/chunk", h.uploadChunk)
		r.Delete("/api/upload/chunk", h.abortChunk)
		r.Post("/api/upload/commit", h.commitChunk)
		r.Delete("/api/tracks/{id}/file", h.deleteTrack)
		r.Get("/api/folders", h.listFolders)
		r.Post("/api/tracks/{id}/lrc", h.saveLRC)
		r.Delete("/api/tracks/{id}/lrc", h.deleteLRC)
		r.Patch("/api/tracks/{id}", h.updateTrack)
		r.Post("/api/tracks/{id}/art", h.updateTrackArt)
	})

	// Authenticated actions (both admin and guest)
	r.Group(func(r chi.Router) {
		r.Use(h.requireAuth)
		r.Get("/api/favorites", h.listFavorites)
		r.Post("/api/tracks/{id}/favorite", h.addFavorite)
		r.Delete("/api/tracks/{id}/favorite", h.deleteFavorite)
	})

	r.Post("/api/auth/login", h.login)
	r.Post("/api/auth/logout", h.logout)
	r.Get("/api/auth/me", h.authMe)

	// Subsonic API compatibility layer (for Flick's Subsonic client).
	h.subsonicMount(r)

	// Embedded web UI — all /web* paths handled by webUI. Deliberately public:
	// this shell IS the login page, so gating it would lock out every user
	// before they could authenticate. Every data and mutating endpoint carries
	// its own check, so an anonymous visitor gets nothing but the login form.
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/web", http.StatusFound)
	})
	r.Get("/web/*", h.webUI)
	r.Get("/web", h.webUI)
}

// ---------------------------------------------------------------- helpers --

func (h *Handler) json(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.Log.Error("json encode", "err", err)
	}
}

func (h *Handler) fail(w http.ResponseWriter, status int, format string, a ...any) {
	h.json(w, status, ErrorResponse{Error: fmt.Sprintf(format, a...)})
}

func idParam(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func page(r *http.Request) (limit, offset, num int) {
	q := r.URL.Query()
	num, _ = strconv.Atoi(q.Get("page"))
	limit, _ = strconv.Atoi(q.Get("limit"))
	if num <= 0 {
		num = 1
	}
	if limit <= 0 {
		limit = 50
	} else if limit > 2000 {
		limit = 2000
	}
	return limit, (num - 1) * limit, num
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func num(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func toDTO(t db.Track) Track {
	return Track{
		ID: t.ID, Path: t.Path, Filename: t.Filename,
		Title: str(t.Title), Artist: str(t.Artist), Album: str(t.Album),
		AlbumArtist: str(t.AlbumArtist), Composer: str(t.Composer), Genre: str(t.Genre),
		Year: num(t.Year), TrackNum: num(t.TrackNum), DiscNum: num(t.DiscNum),
		DurationMs: t.DurationMs, SampleRate: t.SampleRate,
		BitsPerSample: t.BitsPerSample, Channels: t.Channels, Bitrate: t.Bitrate,
		Format: t.Format, FileSize: t.FileSize,
		StreamURL:     fmt.Sprintf("/api/stream/%d", t.ID),
		ArtURL:        str(t.ArtURL),
		AddedAt:       t.AddedAt,
		SpotifyAt:     t.SpotifyAt,
		SpotifyGenres: t.SpotifyGenres,
		Bpm:           t.Bpm,
		Energy:        t.Energy,
		Danceability:  t.Danceability,
	}
}

func totalPages(total, limit int) int {
	if limit <= 0 {
		return 0
	}
	return (total + limit - 1) / limit
}

var errNoRow = errors.New("sql: no rows in result set")

// ------------------------------------------------------------------ health --

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	h.json(w, http.StatusOK, Health{
		Status:    "ok",
		Version:   h.Version,
		BuildTime: h.BuildTime,
		UptimeSec: int64(time.Since(h.Started).Seconds()),
	})
}

func (h *Handler) stats(w http.ResponseWriter, r *http.Request) {
	s, err := h.DB.GetStats()
	if err != nil {
		h.fail(w, http.StatusInternalServerError, "stats: %v", err)
		return
	}
	h.json(w, http.StatusOK, s)
}

// ------------------------------------------------------------------ tracks --

func (h *Handler) listTracks(w http.ResponseWriter, r *http.Request) {
	limit, offset, num := page(r)
	q := r.URL.Query()
	sort := q.Get("sort")
	desc := q.Get("order") == "desc"
	if sort == "added" && q.Get("order") == "" {
		desc = true // newest first by default
	}
	tracks, total, err := h.DB.ListTracks(db.TrackFilter{
		Query:         q.Get("q"),
		Artist:        q.Get("artist"),
		Album:         q.Get("album"),
		Genre:         q.Get("genre"),
		Format:        q.Get("format"),
		FavoritesOnly: q.Get("favorite") == "true" || q.Get("favorites") == "true",
		Sort:          sort,
		Desc:          desc,
		Limit:         limit,
		Offset:        offset,
	})
	if err != nil {
		h.fail(w, http.StatusInternalServerError, "list tracks: %v", err)
		return
	}
	out := make([]Track, len(tracks))
	for i, t := range tracks {
		out[i] = toDTO(t)
	}
	h.json(w, http.StatusOK, PaginatedTracks{
		Data: out, Page: num, Limit: limit, Total: total, TotalPages: totalPages(total, limit),
	})
}

func (h *Handler) getTrack(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r)
	if !ok {
		h.fail(w, http.StatusBadRequest, "invalid id")
		return
	}
	t, err := h.DB.GetTrack(id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) || errors.Is(err, errNoRow) {
			h.fail(w, http.StatusNotFound, "track not found")
			return
		}
		h.fail(w, http.StatusInternalServerError, "get track: %v", err)
		return
	}
	h.json(w, http.StatusOK, toDTO(*t))
}

func (h *Handler) streamTrack(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r)
	if !ok {
		h.fail(w, http.StatusBadRequest, "invalid id")
		return
	}
	t, err := h.DB.GetTrack(id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) || errors.Is(err, errNoRow) {
			h.fail(w, http.StatusNotFound, "track not found")
			return
		}
		h.fail(w, http.StatusInternalServerError, "get track: %v", err)
		return
	}
	abs, err := streamer.Serve(w, r, h.Resolver, t.Path)
	if err != nil {
		if errors.Is(err, streamer.ErrNotFound) {
			h.Log.Warn("indexed file missing", "track", t.ID, "path", t.Path)
			h.fail(w, http.StatusNotFound, "file missing on disk")
			return
		}
		h.Log.Error("stream", "path", t.Path, "err", err)
	}
	_ = abs
}

// ------------------------------------------------------------------ albums --

func toAlbumDTO(a db.Album) Album {
	return Album{
		ID: a.ID, Name: a.Name, Artist: a.Artist, Year: num(a.Year),
		TrackCount: a.TrackCount, TotalDurationMs: a.TotalDurationMs,
		ArtURL: str(a.ArtURL),
	}
}

func (h *Handler) listAlbums(w http.ResponseWriter, r *http.Request) {
	limit, offset, num := page(r)
	q := r.URL.Query()
	albums, total, err := h.DB.ListAlbums(q.Get("artist"), q.Get("q"), limit, offset)
	if err != nil {
		h.fail(w, http.StatusInternalServerError, "list albums: %v", err)
		return
	}
	out := make([]Album, len(albums))
	for i, a := range albums {
		out[i] = toAlbumDTO(a)
	}
	h.json(w, http.StatusOK, PaginatedAlbums{
		Data: out, Page: num, Limit: limit, Total: total, TotalPages: totalPages(total, limit),
	})
}

func (h *Handler) getAlbum(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r)
	if !ok {
		h.fail(w, http.StatusBadRequest, "invalid id")
		return
	}
	a, err := h.DB.GetAlbum(id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) || errors.Is(err, errNoRow) {
			h.fail(w, http.StatusNotFound, "album not found")
			return
		}
		h.fail(w, http.StatusInternalServerError, "get album: %v", err)
		return
	}
	tracks, _, err := h.DB.ListTracks(db.TrackFilter{Album: a.Name, Artist: a.Artist, Limit: 200})
	if err != nil {
		h.fail(w, http.StatusInternalServerError, "album tracks: %v", err)
		return
	}
	out := make([]Track, len(tracks))
	for i, t := range tracks {
		out[i] = toDTO(t)
	}
	// Ordered by track number for a sane album view.
	h.json(w, http.StatusOK, struct {
		Album
		Tracks []Track `json:"tracks"`
	}{toAlbumDTO(*a), out})
}

// getArt serves an album's cover image by album id or track id.
// Flick's flac_server protocol sends the track id as the marker.
// If the id is a track, we resolve its album and serve that album's art.
// The file path comes from albums.art_path via the resolver, so a client
// can never name a path itself. ServeContent handles Range, If-Range,
// ETag and Last-Modified, and streams from the file without buffering.
func (h *Handler) getArt(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r)
	if !ok {
		h.fail(w, http.StatusBadRequest, "invalid id")
		return
	}

	// First try as album id
	a, err := h.DB.GetAlbum(id)
	if err != nil && !(errors.Is(err, db.ErrNotFound) || errors.Is(err, errNoRow)) {
		h.fail(w, http.StatusInternalServerError, "get album: %v", err)
		return
	}

	// If not found as album, try as track id and get its album
	hasArt := a != nil && ((a.ArtPath != nil && *a.ArtPath != "") || (a.ArtHash != nil && *a.ArtHash != ""))
	if !hasArt {
		t, terr := h.DB.GetTrack(id)
		if terr != nil {
			if errors.Is(terr, db.ErrNotFound) || errors.Is(terr, errNoRow) {
				h.fail(w, http.StatusNotFound, "album not found")
				return
			}
			h.fail(w, http.StatusInternalServerError, "get track: %v", terr)
			return
		}
		if t.Album == nil || *t.Album == "" {
			h.fail(w, http.StatusNotFound, "track has no album")
			return
		}
		// Look up album by name + artist (same join key as in queries.go)
		artist := ""
		if t.Artist != nil {
			artist = *t.Artist
		}
		albumArtist := ""
		if t.AlbumArtist != nil {
			albumArtist = *t.AlbumArtist
		}
		if albumArtist == "" {
			albumArtist = artist
		}
		a, err = h.DB.GetAlbumByNameAndArtist(*t.Album, albumArtist)
		if err != nil || a == nil ||
			((a.ArtPath == nil || *a.ArtPath == "") && (a.ArtHash == nil || *a.ArtHash == "")) {
			h.fail(w, http.StatusNotFound, "no cover art")
			return
		}
	}
	// If the album only has extracted art (art_hash) and no on-disk art_path,
	// the bytes live in the art cache, not a library file. Redirect to the
	// artfile endpoint so the cache serves them.
	if a.ArtPath == nil || *a.ArtPath == "" {
		if a.ArtHash != nil && *a.ArtHash != "" {
			http.Redirect(w, r, "/api/artfile/"+*a.ArtHash, http.StatusTemporaryRedirect)
			return
		}
		h.fail(w, http.StatusNotFound, "no cover art")
		return
	}
	abs, err := h.Resolver.Abs(*a.ArtPath)
	if err != nil {
		h.Log.Warn("cover art path unresolved", "album", a.ID, "path", *a.ArtPath, "err", err)
		h.fail(w, http.StatusNotFound, "cover art not found")
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		if os.IsNotExist(err) {
			h.fail(w, http.StatusNotFound, "cover art not found on disk")
			return
		}
		h.fail(w, http.StatusInternalServerError, "open cover art: %v", err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		h.fail(w, http.StatusInternalServerError, "stat cover art: %v", err)
		return
	}
	w.Header().Set("Content-Type", artContentType(abs))
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, filepath.Base(abs), info.ModTime(), f)
}

// getArtFile serves an image from the art cache by its "hash.ext" filename.
//
// The client names the file, so the name is validated as exactly 32 hex chars
// plus one known extension, and the joined path is re-checked against the
// cache dir. The hex check alone still permits "..", so both are required.
func (h *Handler) getArtFile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	base, ext, ok := strings.Cut(name, ".")
	if !ok || len(base) != 32 || !isHex(base) {
		h.fail(w, http.StatusBadRequest, "invalid art name")
		return
	}
	switch strings.ToLower(ext) {
	case "jpg", "png", "webp", "img":
	default:
		h.fail(w, http.StatusBadRequest, "invalid art extension")
		return
	}
	dir := h.Scanner.ArtCacheDir
	if dir == "" {
		h.fail(w, http.StatusNotFound, "art cache disabled")
		return
	}
	abs := filepath.Join(dir, base+"."+strings.ToLower(ext))
	if !withinDir(dir, abs) {
		h.fail(w, http.StatusBadRequest, "invalid art name")
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		if os.IsNotExist(err) {
			h.fail(w, http.StatusNotFound, "art not found")
			return
		}
		h.fail(w, http.StatusInternalServerError, "open art: %v", err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		h.fail(w, http.StatusInternalServerError, "stat art: %v", err)
		return
	}
	w.Header().Set("Content-Type", artContentType(abs))
	w.Header().Set("Accept-Ranges", "bytes")
	// The name is a content hash, so the bytes behind it never change.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, filepath.Base(abs), info.ModTime(), f)
}

// isHex reports whether s is non-empty lowercase hex.
func isHex(s string) bool {
	for _, c := range []byte(s) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return len(s) > 0
}

// withinDir reports whether path is dir itself or below it.
func withinDir(dir, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// artContentType maps a cover's extension to a MIME type. Only the formats
// findCover can record are listed; anything else is served as a JPEG default
// so a renamed cover still renders in the UI.
func artContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "image/jpeg"
	}
}

// ----------------------------------------------------------------- artists --

func toArtistDTO(a db.Artist) Artist {
	return Artist{ID: a.ID, Name: a.Name, TrackCount: a.TrackCount, AlbumCount: a.AlbumCount}
}

func (h *Handler) listArtists(w http.ResponseWriter, r *http.Request) {
	limit, offset, num := page(r)
	artists, total, err := h.DB.ListArtists(r.URL.Query().Get("q"), limit, offset)
	if err != nil {
		h.fail(w, http.StatusInternalServerError, "list artists: %v", err)
		return
	}
	out := make([]Artist, len(artists))
	for i, a := range artists {
		out[i] = toArtistDTO(a)
	}
	h.json(w, http.StatusOK, PaginatedArtists{
		Data: out, Page: num, Limit: limit, Total: total, TotalPages: totalPages(total, limit),
	})
}

func (h *Handler) getArtist(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r)
	if !ok {
		h.fail(w, http.StatusBadRequest, "invalid id")
		return
	}
	a, err := h.DB.GetArtist(id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) || errors.Is(err, errNoRow) {
			h.fail(w, http.StatusNotFound, "artist not found")
			return
		}
		h.fail(w, http.StatusInternalServerError, "get artist: %v", err)
		return
	}
	albums, _, err := h.DB.ListAlbums(a.Name, "", 200, 0)
	if err != nil {
		h.fail(w, http.StatusInternalServerError, "artist albums: %v", err)
		return
	}
	tracks, _, err := h.DB.ListTracks(db.TrackFilter{Artist: a.Name, Limit: 200})
	if err != nil {
		h.fail(w, http.StatusInternalServerError, "artist tracks: %v", err)
		return
	}
	ao := make([]Album, len(albums))
	for i, x := range albums {
		ao[i] = toAlbumDTO(x)
	}
	to := make([]Track, len(tracks))
	for i, x := range tracks {
		to[i] = toDTO(x)
	}
	h.json(w, http.StatusOK, struct {
		Artist
		Albums []Album `json:"albums"`
		Tracks []Track `json:"tracks"`
	}{toArtistDTO(*a), ao, to})
}

func (h *Handler) listGenres(w http.ResponseWriter, r *http.Request) {
	genres, err := h.DB.ListGenres()
	if err != nil {
		h.fail(w, http.StatusInternalServerError, "list genres: %v", err)
		return
	}
	out := make([]Genre, len(genres))
	for i, g := range genres {
		out[i] = Genre{Name: g.Name, TrackCount: g.TrackCount}
	}
	h.json(w, http.StatusOK, out)
}
