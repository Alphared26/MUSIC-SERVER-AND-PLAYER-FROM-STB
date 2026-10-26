// Package streamer serves audio files with Range support.
package streamer

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// ErrNotFound is returned when the requested file cannot be served.
var ErrNotFound = errors.New("not found")

// Resolver maps an indexed path to an absolute path, and exposes the
// configured root -> absolute-path map (used by folder browsing).
type Resolver interface {
	Abs(indexed string) (string, error)
	Roots() map[string]string
}

// Serve streams the file at the indexed path to the client.
//
// It delegates Range/If-Range/HEAD handling to http.ServeContent, which is the
// stdlib implementation of exactly the 206/416 semantics ExoPlayer expects,
// and which streams straight from the file without buffering the whole body.
//
// Returns the absolute path served, or an error wrapping ErrNotFound.
func Serve(w http.ResponseWriter, r *http.Request, res Resolver, indexed string) (string, error) {
	abs, err := res.Abs(indexed)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	f, err := os.Open(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", ErrNotFound, abs)
		}
		return "", fmt.Errorf("open %s: %w", abs, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", abs, err)
	}

	w.Header().Set("Content-Type", ContentType(f))
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
	w.Header().Set("ETag", fmt.Sprintf("\"%x-%x\"", fi.ModTime().UnixNano(), fi.Size()))

	// ServeContent sets Content-Length / Content-Range and handles GET, HEAD,
	// single-range, multi-range and unsatisfiable ranges with valid If-Range and Last-Modified.
	http.ServeContent(w, r, filepath.Base(abs), fi.ModTime(), f)
	return abs, nil
}

// ContentType sniffs the audio format from the file header. Format is known
// from the indexed row, but sniffing keeps the endpoint correct even if a file
// is renamed without rescanning.
func ContentType(f *os.File) string {
	head := make([]byte, 12)
	n, err := f.ReadAt(head, 0)
	if err != nil && err != io.EOF {
		return "application/octet-stream"
	}
	head = head[:n]
	switch {
	case len(head) >= 4 && string(head[:4]) == "fLaC":
		return "audio/flac"
	case len(head) >= 3 && string(head[:3]) == "ID3":
		return "audio/mpeg"
	case len(head) >= 2 && head[0] == 0xFF && head[1]&0xE0 == 0xE0:
		return "audio/mpeg"
	case len(head) >= 4 && string(head[:4]) == "RIFF" && len(head) >= 12 && string(head[8:12]) == "WAVE":
		// WAV: streamer only special-cases flac/mp3; sniff the RIFF/WAVE
		// magic so PCM/FLAC-in-WAV clients get a usable audio type.
		return "audio/wav"
	}
	return "application/octet-stream"
}
