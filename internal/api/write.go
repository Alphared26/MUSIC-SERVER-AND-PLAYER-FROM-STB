package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/go-chi/chi/v5"

	"flac-server/internal/db"
	"flac-server/internal/paths"
)

// ------------------------------------------------------------------- auth --

const sessionCookie = "flac_session"

// requireAuth gates write endpoints. With auth disabled every request passes.
func (h *Handler) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.Opt.AuthEnabled {
			next.ServeHTTP(w, r)
			return
		}
		// Cloudflare Access already authenticated the user and injects this
		// header. Trusting it is the whole point of running behind the tunnel.
		if email := r.Header.Get("Cf-Access-Authenticated-User-Email"); email != "" {
			if h.Opt.TrustedDomain == "" ||
				strings.HasSuffix(strings.ToLower(email), "@"+strings.ToLower(h.Opt.TrustedDomain)) {
				next.ServeHTTP(w, r)
				return
			}
		}
		if h.sessionValid(r) {
			next.ServeHTTP(w, r)
			return
		}
		h.fail(w, http.StatusUnauthorized, "authentication required")
	})
}

func (h *Handler) sessionValid(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	h.mu.Lock()
	exp, ok := h.sessions[c.Value]
	h.mu.Unlock()
	return ok && time.Now().Before(exp)
}

// login creates a session cookie for a local user (bcrypt hash).
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := decodeJSON(r, &req, 4096); err != nil {
		h.fail(w, http.StatusBadRequest, "invalid body")
		return
	}
	if len(h.Opt.LocalUsers) == 0 {
		h.fail(w, http.StatusNotImplemented, "local login disabled; use Cloudflare Access")
		return
	}
	ip := clientIP(r)
	if wait, locked := h.loginThrottled(ip); locked {
		// 429 keeps a scripted guesser from walking the whole user list; the
		// Retry-After tells an honest client when it may try again.
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())))
		h.fail(w, http.StatusTooManyRequests, "too many failed logins, try again later")
		return
	}
	for _, u := range h.Opt.LocalUsers {
		if !strings.EqualFold(u.Username, req.Username) {
			continue
		}
		if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)) == nil {
			h.clearLoginFails(ip)
			token := randomToken(32)
			ttl := h.Opt.SessionTTL
			h.mu.Lock()
			h.sessions[token] = time.Now().Add(ttl)
			h.mu.Unlock()
			http.SetCookie(w, &http.Cookie{
				Name: sessionCookie, Value: token, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteLaxMode,
				MaxAge: int(ttl.Seconds()),
			})
			h.json(w, http.StatusOK, LoginResponse{Token: token, ExpiresIn: int64(ttl.Seconds())})
			return
		}
	}
	// Same message for unknown user and wrong password: do not leak which exists.
	h.recordLoginFail(ip)
	h.fail(w, http.StatusUnauthorized, "invalid credentials")
}

// clientIP is the best guess at the caller's address. Behind Cloudflare the
// leftmost X-Forwarded-For entry is the original client; otherwise it is the
// socket peer.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			xff = xff[:i]
		}
		if ip := strings.TrimSpace(xff); ip != "" {
			return ip
		}
	}
	if rip := r.Header.Get("CF-Connecting-IP"); rip != "" {
		return strings.TrimSpace(rip)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// loginThrottled reports whether this IP is over its failed-attempt budget and
// how long it must wait. A zero wait means the caller may try.
func (h *Handler) loginThrottled(ip string) (time.Duration, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fw, ok := h.loginFails[ip]
	if !ok {
		return 0, false
	}
	if time.Since(fw.start) > loginWindow {
		delete(h.loginFails, ip)
		return 0, false
	}
	if fw.count < maxLoginFails {
		return 0, false
	}
	return loginWindow - time.Since(fw.start), true
}

// recordLoginFail counts one failed attempt for ip.
func (h *Handler) recordLoginFail(ip string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fw, ok := h.loginFails[ip]
	if !ok || time.Since(fw.start) > loginWindow {
		h.loginFails[ip] = &failWindow{count: 1, start: time.Now()}
		return
	}
	fw.count++
}

// clearLoginFails resets the counter after a successful login.
func (h *Handler) clearLoginFails(ip string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.loginFails, ip)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		h.mu.Lock()
		delete(h.sessions, c.Value)
		h.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	h.json(w, http.StatusOK, map[string]bool{"ok": true})
}

// authMe returns whether the caller has a valid session.
func (h *Handler) authMe(w http.ResponseWriter, r *http.Request) {
	if !h.Opt.AuthEnabled {
		h.json(w, http.StatusOK, map[string]any{"authenticated": true, "auth_enabled": false})
		return
	}
	valid := h.sessionValid(r)
	user := ""
	if valid && len(h.Opt.LocalUsers) > 0 {
		user = h.Opt.LocalUsers[0].Username
	}
	h.json(w, http.StatusOK, map[string]any{
		"authenticated": valid,
		"auth_enabled":  true,
		"username":      user,
	})
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(strconv.FormatInt(time.Now().UnixNano(), 16)))
	}
	return hex.EncodeToString(b)
}

func decodeJSON(r *http.Request, v any, max int64) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, max))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// ----------------------------------------------------------------- upload --

// upload accepts multipart FLAC/MP3 files and stores them under the writable
// root. The request body is size-capped before a single byte is written.
func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, h.Opt.MaxFileSize*2) // + multipart overhead
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		h.fail(w, http.StatusRequestEntityTooLarge, "upload too large or malformed: %v", err)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	dstDir, err := paths.SanitizeJoin(h.Opt.WritableRoot, r.URL.Query().Get("dir"))
	if err != nil {
		h.fail(w, http.StatusBadRequest, "invalid target dir: %v", err)
		return
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		h.fail(w, http.StatusInternalServerError, "mkdir: %v", err)
		return
	}

	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		files = r.MultipartForm.File["file"]
	}
	if len(files) == 0 {
		h.fail(w, http.StatusBadRequest, "no files in request")
		return
	}

	resp := UploadResponse{Uploaded: []string{}}
	for _, fh := range files {
		name, serr := safeFilename(fh.Filename)
		if serr != nil {
			resp.Failed = append(resp.Failed, UploadError{Filename: fh.Filename, Error: serr.Error()})
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		if !h.Opt.AllowedFormats[strings.TrimPrefix(ext, ".")] {
			resp.Failed = append(resp.Failed, UploadError{Filename: name, Error: "unsupported format " + ext})
			continue
		}
		if len(h.Opt.AllowedFormats) > 0 && !h.Opt.AllowedFormats[strings.TrimPrefix(ext, ".")] {
			resp.Failed = append(resp.Failed, UploadError{Filename: name, Error: strings.TrimPrefix(ext, ".") + " not allowed"})
			continue
		}
		finalPath, jerr := paths.SanitizeJoin(dstDir, name)
		if jerr != nil {
			resp.Failed = append(resp.Failed, UploadError{Filename: name, Error: "invalid name"})
			continue
		}
		src, oerr := fh.Open()
		if oerr != nil {
			resp.Failed = append(resp.Failed, UploadError{Filename: name, Error: oerr.Error()})
			continue
		}
		if werr := h.storeFile(src, finalPath); werr != nil {
			resp.Failed = append(resp.Failed, UploadError{Filename: name, Error: werr.Error()})
			continue
		}
		resp.Uploaded = append(resp.Uploaded, finalPath)
	}

	// Index the new files right away so the UI shows them without waiting for
	// the next periodic scan.
	resp.ScanJobID = h.startScan("upload")

	if len(resp.Uploaded) == 0 {
		h.json(w, http.StatusBadRequest, resp)
		return
	}
	h.json(w, http.StatusCreated, resp)
}

// storeFile streams src to dst. It never truncates an existing file and never
// leaves a partial file behind: writes go to dst.part and are renamed only on
// a clean, complete copy.
func (h *Handler) storeFile(src io.ReadCloser, dst string) error {
	defer src.Close()

	if _, err := os.Stat(dst); err == nil {
		return errors.New("file already exists")
	}
	tmp := dst + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	written, cerr := io.Copy(f, io.LimitReader(src, h.Opt.MaxFileSize+1))
	if cerr != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write: %w", cerr)
	}
	if written > h.Opt.MaxFileSize {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("exceeds max size of %d bytes", h.Opt.MaxFileSize)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close: %w", err)
	}
	return os.Rename(tmp, dst)
}

// --------------------------------------------------------- chunked upload --

// uploadChunk appends one raw-body chunk to dest+".part" at the requested
// offset. Each chunk is acked immediately, so a slow link never has the whole
// body buffered server-side before the client learns anything.
func (h *Handler) uploadChunk(w http.ResponseWriter, r *http.Request) {
	dest, ok := h.chunkDest(w, r)
	if !ok {
		return
	}
	part := dest + ".part"

	off, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || off < 0 {
		h.fail(w, http.StatusBadRequest, "invalid offset")
		return
	}

	// Cap the whole assembled file, not just this request, so a client cannot
	// creep past MaxFileSize by sending many small chunks.
	room := h.Opt.MaxFileSize - off
	if room <= 0 {
		h.fail(w, http.StatusRequestEntityTooLarge, "exceeds max size of %d bytes", h.Opt.MaxFileSize)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, room)

	// The .part must not exist until the first byte lands, otherwise an empty
	// first chunk would leave a stray file behind.
	flags := os.O_WRONLY
	create := off == 0
	if create {
		flags |= os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		h.fail(w, http.StatusInternalServerError, "open part: %v", err)
		return
	}
	defer f.Close()

	if !create {
		info, serr := f.Stat()
		if serr != nil {
			h.fail(w, http.StatusInternalServerError, "stat part: %v", serr)
			return
		}
		if info.Size() < off {
			// Client is behind its own confirmed offset: report the real size
			// so it can restart from there instead of writing a hole.
			h.json(w, http.StatusBadRequest, struct {
				Error  string `json:"error"`
				Offset int64  `json:"offset"`
			}{fmt.Sprintf("offset %d is beyond part size %d", off, info.Size()), info.Size()})
			return
		}
		if _, serr := f.Seek(off, io.SeekStart); serr != nil {
			h.fail(w, http.StatusInternalServerError, "seek: %v", serr)
			return
		}
	}

	written, cerr := io.Copy(f, r.Body)
	if cerr != nil {
		h.fail(w, http.StatusInternalServerError, "write: %v", cerr)
		return
	}
	if err := f.Sync(); err != nil {
		h.fail(w, http.StatusInternalServerError, "sync: %v", err)
		return
	}
	h.json(w, http.StatusOK, map[string]any{
		"ok":       true,
		"received": written,
		"offset":   off + written,
	})
}

// abortChunk deletes a half-finished .part so a cancelled upload leaves nothing
// behind for the scanner or the next attempt to trip over.
func (h *Handler) abortChunk(w http.ResponseWriter, r *http.Request) {
	dest, ok := h.chunkDest(w, r)
	if !ok {
		return
	}
	if err := os.Remove(dest + ".part"); err != nil && !errors.Is(err, os.ErrNotExist) {
		h.fail(w, http.StatusInternalServerError, "remove part: %v", err)
		return
	}
	h.json(w, http.StatusOK, map[string]bool{"ok": true})
}

// commitChunk promotes dest+".part" to dest and kicks off the index pass.
// The rename is the atomic commit point, so a completed file is never
// overwritten and a partial one is never visible to the library.
func (h *Handler) commitChunk(w http.ResponseWriter, r *http.Request) {
	dest, ok := h.chunkDest(w, r)
	if !ok {
		return
	}
	part := dest + ".part"
	if _, err := os.Stat(part); err != nil {
		h.fail(w, http.StatusBadRequest, "no partial upload to commit: %v", err)
		return
	}
	if _, err := os.Stat(dest); err == nil {
		h.fail(w, http.StatusConflict, "file already exists")
		return
	}
	if err := os.Rename(part, dest); err != nil {
		h.fail(w, http.StatusInternalServerError, "commit: %v", err)
		return
	}
	// Index right away so the UI shows the file without waiting for the next
	// periodic scan.
	h.json(w, http.StatusCreated, UploadResponse{
		Uploaded:  []string{dest},
		ScanJobID: h.startScan("upload"),
	})
}

// chunkDest resolves and validates the name/dir query pair shared by all three
// chunked-upload endpoints, and creates the target directory.
func (h *Handler) chunkDest(w http.ResponseWriter, r *http.Request) (string, bool) {
	q := r.URL.Query()
	name, err := safeFilename(q.Get("name"))
	if err != nil {
		h.fail(w, http.StatusBadRequest, "invalid name: %v", err)
		return "", false
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	if !h.Opt.AllowedFormats[ext] {
		h.fail(w, http.StatusBadRequest, "unsupported format .%s", ext)
		return "", false
	}
	dest, err := paths.SanitizeJoin(h.Opt.WritableRoot, q.Get("dir"), name)
	if err != nil {
		h.fail(w, http.StatusBadRequest, "invalid target dir: %v", err)
		return "", false
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		h.fail(w, http.StatusInternalServerError, "mkdir: %v", err)
		return "", false
	}
	return dest, true
}

// safeFilename strips any directory component from a client-supplied name.
func safeFilename(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "", errors.New("invalid filename")
	}
	if strings.HasPrefix(name, ".") {
		return "", errors.New("hidden files are not accepted")
	}
	return name, nil
}

// ----------------------------------------------------------------- delete --

// deleteTrack removes the audio file and its database row.
// The file goes first: a failure then leaves a stale row that the next scan
// removes, instead of an orphaned file the library can never show.
func (h *Handler) deleteTrack(w http.ResponseWriter, r *http.Request) {
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
	abs, err := h.Resolver.Abs(t.Path)
	if err != nil {
		h.fail(w, http.StatusBadRequest, "unresolvable path: %v", err)
		return
	}
	// Only files inside the writable root may be deleted. This is the real
	// guard: the library mount is read-only, and this refuses it explicitly
	// instead of relying on a failed write.
	if !strings.HasPrefix(abs, h.Opt.WritableRoot) {
		h.fail(w, http.StatusForbidden, "track is on the read-only library and cannot be deleted")
		return
	}
	if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
		h.fail(w, http.StatusInternalServerError, "delete file: %v", err)
		return
	}
	if _, err := h.DB.DeleteTrack(id); err != nil {
		h.fail(w, http.StatusInternalServerError, "delete row: %v", err)
		return
	}
	if err := h.DB.RebuildAggregates(); err != nil {
		h.Log.Warn("rebuild aggregates after delete", "err", err)
	}
	h.json(w, http.StatusOK, map[string]any{"deleted": true, "path": t.Path})
}

// ---------------------------------------------------------------- folders --

// listFolders browses the library or writable root one level at a time.
// Path form: "library" or "library/Artist" -> music_root, "upload" or
// "upload/Subdir" -> music_writable. Default (no/bare path) -> writable root
// for backwards compatibility with the web upload browser.
func (h *Handler) listFolders(w http.ResponseWriter, r *http.Request) {
	var abs string
	root := h.Resolver.Roots()
	param := r.URL.Query().Get("path")

	// Normalize the leading segment to a configured root.
	if param != "" {
		first, rest, _ := strings.Cut(param, "/")
		if base, ok := root[first]; ok {
			// rooted path: resolve the remainder under that root
			joined, err := paths.SanitizeJoin(base, rest)
			if err != nil {
				h.fail(w, http.StatusBadRequest, "invalid path: %v", err)
				return
			}
			abs = joined
		} else {
			// no matching root segment: treat whole param as a subpath of the
			// writable root (backwards compatible)
			joined, err := paths.SanitizeJoin(h.Opt.WritableRoot, param)
			if err != nil {
				h.fail(w, http.StatusBadRequest, "invalid path: %v", err)
				return
			}
			abs = joined
		}
	} else {
		abs = h.Opt.WritableRoot
	}

	entries, err := os.ReadDir(abs)
	if err != nil {
		h.fail(w, http.StatusNotFound, "read dir: %v", err)
		return
	}
	out := FolderListing{Folders: []FolderEntry{}, Files: []FolderEntry{}}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if e.IsDir() {
			out.Folders = append(out.Folders, FolderEntry{Name: name, IsDir: true})
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		if !h.Opt.AllowedFormats[strings.TrimPrefix(ext, ".")] {
			continue
		}
		fe := FolderEntry{Name: name}
		if info, ierr := e.Info(); ierr == nil {
			fe.Size = info.Size()
			fe.Mtime = info.ModTime().Unix()
		}
		out.Files = append(out.Files, fe)
	}
	h.json(w, http.StatusOK, out)
}

// ------------------------------------------------------------------- lrc --

// saveLRC writes or overwrites the sidecar .lrc for a track.
// Body is raw LRC text (or multipart file). Only tracks in writable root
// can have lyrics saved next to them.
func (h *Handler) saveLRC(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
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
	abs, err := h.Resolver.Abs(t.Path)
	if err != nil {
		h.fail(w, http.StatusBadRequest, "unresolvable path: %v", err)
		return
	}
	if !strings.HasPrefix(abs, h.Opt.WritableRoot) {
		h.fail(w, http.StatusForbidden, "track is on read-only library; cannot add sidecar lyrics")
		return
	}

	// Read body: accept both raw text/plain and multipart
	var content []byte
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		file, _, err := r.FormFile("file")
		if err != nil {
			h.fail(w, http.StatusBadRequest, "form field 'file' required")
			return
		}
		defer file.Close()
		content, err = io.ReadAll(io.LimitReader(file, 1<<20)) // 1MB max
		if err != nil {
			h.fail(w, http.StatusBadRequest, "read file: %v", err)
			return
		}
	} else {
		content, err = io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			h.fail(w, http.StatusBadRequest, "read body: %v", err)
			return
		}
	}

	if len(strings.TrimSpace(string(content))) == 0 {
		h.fail(w, http.StatusBadRequest, "empty lyrics content")
		return
	}

	lrcPath := strings.TrimSuffix(abs, filepath.Ext(abs)) + ".lrc"
	if err := os.WriteFile(lrcPath, content, 0o644); err != nil {
		h.fail(w, http.StatusInternalServerError, "write lrc: %v", err)
		return
	}

	h.json(w, http.StatusOK, map[string]any{
		"saved":    true,
		"track_id": id,
		"path":     lrcPath,
		"bytes":    len(content),
	})
}

// deleteLRC removes the sidecar .lrc for a track.
func (h *Handler) deleteLRC(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
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
	abs, err := h.Resolver.Abs(t.Path)
	if err != nil {
		h.fail(w, http.StatusBadRequest, "unresolvable path: %v", err)
		return
	}
	if !strings.HasPrefix(abs, h.Opt.WritableRoot) {
		h.fail(w, http.StatusForbidden, "track is on read-only library")
		return
	}

	lrcPath := strings.TrimSuffix(abs, filepath.Ext(abs)) + ".lrc"
	if err := os.Remove(lrcPath); err != nil && !os.IsNotExist(err) {
		h.fail(w, http.StatusInternalServerError, "delete lrc: %v", err)
		return
	}

	h.json(w, http.StatusOK, map[string]any{
		"deleted":  true,
		"track_id": id,
	})
}

// ---------------------------------------------------- track metadata & art --

type updateTrackReq struct {
	Title  *string  `json:"title"`
	Artist *string  `json:"artist"`
	Album  *string  `json:"album"`
	Genre  *string  `json:"genre"`
	Bpm    *float64 `json:"bpm"`
}

func (h *Handler) updateTrack(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r)
	if !ok {
		h.fail(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req updateTrackReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.fail(w, http.StatusBadRequest, "invalid json: %v", err)
		return
	}
	if err := h.DB.UpdateTrackMetadata(id, req.Title, req.Artist, req.Album, req.Genre, req.Bpm); err != nil {
		h.fail(w, http.StatusInternalServerError, "update track: %v", err)
		return
	}
	h.json(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) listFavorites(w http.ResponseWriter, r *http.Request) {
	ids, err := h.DB.ListFavorites()
	if err != nil {
		h.fail(w, http.StatusInternalServerError, "list favorites: %v", err)
		return
	}
	if ids == nil {
		ids = []int64{}
	}
	h.json(w, http.StatusOK, map[string]any{"data": ids})
}

func (h *Handler) addFavorite(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r)
	if !ok {
		h.fail(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.DB.AddFavorite(id); err != nil {
		h.fail(w, http.StatusInternalServerError, "add favorite: %v", err)
		return
	}
	h.json(w, http.StatusOK, map[string]any{"ok": true, "favorite": true})
}

func (h *Handler) deleteFavorite(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r)
	if !ok {
		h.fail(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.DB.RemoveFavorite(id); err != nil {
		h.fail(w, http.StatusInternalServerError, "remove favorite: %v", err)
		return
	}
	h.json(w, http.StatusOK, map[string]any{"ok": true, "favorite": false})
}

type updateArtReq struct {
	ImageURL string `json:"image_url"`
}

func (h *Handler) updateTrackArt(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r)
	if !ok {
		h.fail(w, http.StatusBadRequest, "invalid id")
		return
	}
	t, err := h.DB.GetTrack(id)
	if err != nil {
		h.fail(w, http.StatusNotFound, "track not found: %v", err)
		return
	}

	var imgBytes []byte
	contentType := r.Header.Get("Content-Type")

	if strings.HasPrefix(contentType, "application/json") {
		var req updateArtReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ImageURL == "" {
			h.fail(w, http.StatusBadRequest, "invalid image_url")
			return
		}
		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Get(req.ImageURL)
		if err != nil || resp.StatusCode != http.StatusOK {
			h.fail(w, http.StatusBadRequest, "failed to download art: %v", err)
			return
		}
		defer resp.Body.Close()
		imgBytes, err = io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		if err != nil || len(imgBytes) == 0 {
			h.fail(w, http.StatusBadRequest, "empty image")
			return
		}
	} else if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			h.fail(w, http.StatusBadRequest, "invalid multipart: %v", err)
			return
		}
		file, _, err := r.FormFile("art")
		if err != nil {
			h.fail(w, http.StatusBadRequest, "art form file required")
			return
		}
		defer file.Close()
		imgBytes, err = io.ReadAll(io.LimitReader(file, 10<<20))
		if err != nil || len(imgBytes) == 0 {
			h.fail(w, http.StatusBadRequest, "empty image")
			return
		}
	} else {
		imgBytes, err = io.ReadAll(io.LimitReader(r.Body, 10<<20))
		if err != nil || len(imgBytes) == 0 {
			h.fail(w, http.StatusBadRequest, "empty image")
			return
		}
	}

	dir := h.Scanner.ArtCacheDir
	if dir == "" {
		h.fail(w, http.StatusInternalServerError, "art cache dir not configured")
		return
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		h.fail(w, http.StatusInternalServerError, "mkdir art dir: %v", err)
		return
	}

	sum := sha256.Sum256(imgBytes)
	hashName := hex.EncodeToString(sum[:16]) + ".jpg"
	targetPath := filepath.Join(dir, hashName)

	if err := os.WriteFile(targetPath, imgBytes, 0644); err != nil {
		h.fail(w, http.StatusInternalServerError, "save art file: %v", err)
		return
	}

	albumName := ""
	if t.Album != nil && *t.Album != "" {
		albumName = *t.Album
	} else if t.Title != nil && *t.Title != "" {
		albumName = *t.Title
	} else {
		albumName = t.Filename
	}

	artistName := ""
	if t.AlbumArtist != nil && *t.AlbumArtist != "" {
		artistName = *t.AlbumArtist
	} else if t.Artist != nil && *t.Artist != "" {
		artistName = *t.Artist
	}

	if t.Album == nil || *t.Album == "" {
		_ = h.DB.UpdateTrackMetadata(id, nil, nil, &albumName, nil, nil)
	}

	if err := h.DB.SetAlbumArtHash(albumName, artistName, hashName); err != nil {
		h.fail(w, http.StatusInternalServerError, "set album art: %v", err)
		return
	}

	h.json(w, http.StatusOK, map[string]any{
		"ok":      true,
		"art_url": "/api/artfile/" + hashName,
	})
}