package api

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed assets/*
var assetsFS embed.FS

// webUI serves the embedded single-page app. Any unknown path under /web
// falls back to index.html so client-side routes survive a page reload.
func (h *Handler) webUI(w http.ResponseWriter, r *http.Request) {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		h.fail(w, http.StatusInternalServerError, "assets unavailable")
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/web")
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		rel = "index.html"
	}
	clean := path.Clean("/" + rel)[1:]

	data, err := fs.ReadFile(sub, clean)
	if err != nil {
		// Unknown path: hand back the SPA shell so client routing works.
		data, err = fs.ReadFile(sub, "index.html")
		if err != nil {
			h.fail(w, http.StatusNotFound, "not found")
			return
		}
		clean = "index.html"
	}
	w.Header().Set("Content-Type", contentTypeFor(clean))
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, clean, time.Time{}, bytes.NewReader(data))
}

func contentTypeFor(name string) string {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".json":
		return "application/json"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".ico":
		return "image/x-icon"
	default:
		return "application/octet-stream"
	}
}
