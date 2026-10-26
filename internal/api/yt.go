package api

import (
	"context"
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
		h.Log.Warn("yt search failed", "query", q, "err", err)
		h.fail(w, http.StatusBadGateway, "search failed: "+err.Error())
		return
	}

	h.json(w, http.StatusOK, tracks)
}

// ytStream redirects (HTTP 302) to the direct CDN googlevideo audio stream.
// Zero STB bandwidth consumption.
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

	url, err := h.YT.ResolveStream(r.Context(), id)
	if err != nil {
		h.Log.Warn("yt resolve stream failed", "id", id, "err", err)
		h.fail(w, http.StatusBadGateway, "resolve stream failed: "+err.Error())
		return
	}

	// 302 Found redirect to CDN
	http.Redirect(w, r, url, http.StatusFound)
}

// ytDownload downloads the YouTube Music track directly to the server's music_writable/Streaming folder.
// Admin-only endpoint.
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

	outputDir := filepath.Join(h.Opt.WritableRoot, "Streaming")
	go func() {
		ctx := context.Background()
		h.Log.Info("starting yt download", "id", id, "dest", outputDir)
		if err := h.YT.Download(ctx, id, outputDir); err != nil {
			h.Log.Error("yt download failed", "id", id, "err", err)
			return
		}
		h.Log.Info("yt download completed, triggering library scan", "id", id)
		// Trigger library scan automatically
		h.startScan("yt_download")
	}()

	h.json(w, http.StatusAccepted, map[string]any{
		"ok":      true,
		"message": "Lagu sedang didownload ke server dan akan otomatis terindeks di library setelah selesai.",
	})
}
