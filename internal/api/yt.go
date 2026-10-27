package api

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
)

// ytSearch performs a YouTube Music song search using pure Go InnerTube client.
func (h *Handler) ytSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		h.json(w, http.StatusOK, []any{})
		return
	}

	tracks, err := h.YT.Search(r.Context(), q)
	if err != nil {
		h.fail(w, http.StatusInternalServerError, "yt search error: "+err.Error())
		return
	}
	h.json(w, http.StatusOK, tracks)
}

// ytStream streams audio via STB proxy to prevent CORS issues, IP binding blocks, and ensure seek/range support.
func (h *Handler) ytStream(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	id = strings.TrimPrefix(id, "yt_")
	if id == "" {
		h.fail(w, http.StatusBadRequest, "missing video id")
		return
	}

	streamURL, err := h.YT.ResolveStream(r.Context(), id)
	if err != nil {
		h.fail(w, http.StatusBadGateway, "failed to resolve audio stream: "+err.Error())
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, streamURL, nil)
	if err != nil {
		h.fail(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Forward Range header if present
	if rng := r.Header.Get("Range"); rng != "" {
		req.Header.Set("Range", rng)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.fail(w, http.StatusBadGateway, "upstream cdn error: "+err.Error())
		return
	}
	defer resp.Body.Close()

	// Set CORS & streaming headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Range, Accept-Encoding")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Content-Length, Accept-Ranges")
	w.Header().Set("Accept-Ranges", "bytes")

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "audio/mp4")
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		w.Header().Set("Content-Range", cr)
	}

	w.WriteHeader(resp.StatusCode)
	if r.Method != http.MethodHead {
		if flusher, ok := w.(http.Flusher); ok {
			buf := make([]byte, 32*1024)
			for {
				n, rErr := resp.Body.Read(buf)
				if n > 0 {
					if _, wErr := w.Write(buf[:n]); wErr != nil {
						break
					}
					flusher.Flush()
				}
				if rErr != nil {
					break
				}
			}
		} else {
			_, _ = io.Copy(w, resp.Body)
		}
	}
}

// ytResolve pre-resolves the streaming URL so it gets cached before playback starts.
func (h *Handler) ytResolve(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	id = strings.TrimPrefix(id, "yt_")
	if id == "" {
		h.fail(w, http.StatusBadRequest, "missing video id")
		return
	}
	u, err := h.YT.ResolveStream(r.Context(), id)
	if err != nil {
		h.fail(w, http.StatusBadGateway, "failed to resolve audio stream: "+err.Error())
		return
	}
	h.json(w, http.StatusOK, map[string]any{"id": id, "ok": true, "cached": u != ""})
}

// ytDownload downloads the audio to the server storage (admin only).
func (h *Handler) ytDownload(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	id = strings.TrimPrefix(id, "yt_")
	if id == "" {
		h.fail(w, http.StatusBadRequest, "missing video id")
		return
	}

	destDir := "/music_writable/Streaming"
	if h.Opt.WritableRoot != "" {
		destDir = filepath.Join(h.Opt.WritableRoot, "Streaming")
	}

	go func() {
		ctx := context.Background()
		if err := h.YT.Download(ctx, id, destDir); err != nil {
			h.Log.Error("yt download failed", "id", id, "err", err)
			return
		}
		h.Log.Info("yt download completed, triggering library scan", "id", id)
		// Trigger library scan automatically
		h.startScan("yt_download")
	}()

	h.json(w, http.StatusOK, map[string]any{
		"message": "Unduhan YouTube Music dimulai di latar belakang. Lagu akan otomatis muncul di library setelah selesai.",
		"id":      id,
	})
}
