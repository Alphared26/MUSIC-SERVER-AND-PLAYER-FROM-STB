// Command flac-server is a bit-perfect FLAC/MP3 streaming server with a
// web management UI. It is designed for low-power hosts such as an Amlogic
// HG680P running Armbian, so the whole runtime fits in a small memory budget.
package main

import (
	"runtime/debug"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"flac-server/internal/api"
	"flac-server/internal/config"
	"flac-server/internal/db"
	"flac-server/internal/paths"
	"flac-server/internal/scanner"
)

// Injected at build time via -ldflags.
var (
	version   = "dev"
	buildTime = "unknown"
)

func main() {
	// Set memory limit and GC percent for low-RAM hosts (e.g. HG680P)
	debug.SetMemoryLimit(350 * 1024 * 1024)
	debug.SetGCPercent(50)

	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	log := newLogger(cfg.Logging.Level)
	slog.SetDefault(log)

	// Ensure writable directories exist before anything tries to use them.
	for _, dir := range []string{
		filepath.Dir(cfg.Database.Path),
		cfg.Upload.TempDir,
		cfg.Library.ArtCacheDir,
		cfg.Library.MusicWritable,
	} {
		if dir == "" || dir == "/" {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Warn("mkdir failed", "dir", dir, "err", err)
		}
	}

	database, err := db.Open(cfg.Database.Path,
		db.WithBusyTimeout(cfg.Database.BusyTimeout),
		db.WithPool(cfg.Database.MaxOpenConns, cfg.Database.MaxIdleConns))
	if err != nil {
		return err
	}
	defer database.Close()

	// Two roots: the read-only library and the writable upload target. The
	// root name is the first segment of every indexed path.
	roots := map[string]string{
		"library": cfg.Library.MusicRoot,
		"upload":  cfg.Library.MusicWritable,
	}
	resolver := paths.New(roots)
	sc := scanner.New(log, database, roots, cfg.Library.Extensions, cfg.Library.ArtCacheDir)

	allowed := make(map[string]bool, len(cfg.Upload.AllowedFormats))
	for _, f := range cfg.Upload.AllowedFormats {
		allowed[strings.ToLower(strings.TrimPrefix(f, "."))] = true
	}
	handler := api.New(database, sc, resolver, api.Options{
		MaxFileSize:    cfg.Upload.MaxFileSize,
		AllowedFormats: allowed,
		TempDir:        cfg.Upload.TempDir,
		WritableRoot:   cfg.Library.MusicWritable,
		WritableName:   "upload",
		AuthEnabled:    cfg.Auth.Enabled,
		TrustedDomain:  cfg.Auth.TrustedEmailDomain,
		SessionTTL:     cfg.Auth.SessionTTL.D(),
		LocalUsers:     cfg.Auth.LocalUsers,
	}, log, version, buildTime)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	// The player issues long-lived streaming requests; the server's own
	// timeouts are configured on http.Server below, not here.
	r.Use(logRequests(log))
	r.Use(corsForTunnel())
	handler.Mount(r)

	srv := &http.Server{
		Addr:         cfg.Server.Addr(),
		Handler:      r,
		ReadTimeout:  cfg.Server.ReadTimeout.D(),
		WriteTimeout: cfg.Server.WriteTimeout.D(),
		IdleTimeout:  cfg.Server.IdleTimeout.D(),
	}

	stop := make(chan struct{})

	// First scan on boot so the library is not empty when the UI loads.
	go func() {
		res, err := sc.Scan()
		if err != nil {
			log.Error("initial scan failed", "err", err)
			return
		}
		log.Info("initial scan complete", "scanned", res.FilesScanned, "added", res.Added,
			"updated", res.Updated, "removed", res.Removed)
	}()

	// Periodic rescans pick up files added outside the web UI (USB copy, SMB).
	go sc.Periodic(cfg.Library.ScanInterval, stop, nil)

	go func() {
		log.Info("listening", "addr", srv.Addr, "version", version,
			"library", cfg.Library.MusicRoot, "writable", cfg.Library.MusicWritable)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server stopped", "err", err)
			close(stop)
			os.Exit(1)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	close(stop)
	log.Info("shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		return err
	}
	log.Info("stopped")
	return nil
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}

// logRequests logs one line per request. Streamed audio is logged on
// completion, which is what the status code reflects.
func logRequests(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			next.ServeHTTP(ww, r)
			log.Info("request",
				"method", r.Method, "path", r.URL.Path, "status", ww.Status(),
				"bytes", ww.BytesWritten(), "dur", time.Since(start).String())
		})
	}
}

// corsForTunnel allows the Android client and any local web frontend to call
// the API from another origin. Credentials are not needed: auth travels in
// the Cloudflare header or an HttpOnly cookie on same-site requests.
func corsForTunnel() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Range, Content-Type, X-API-Key")
			w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Accept-Ranges, Content-Length")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
