// Package db wraps SQLite access for the library.
package db

import (
	_ "embed"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// DB is the library database handle.
type DB struct {
	*sqlx.DB
}

type connOpts struct {
	path        string
	busyTimeout int
	maxOpen     int
	maxIdle     int
}

type Option func(*connOpts)

// WithBusyTimeout sets SQLite busy timeout in milliseconds.
func WithBusyTimeout(ms int) Option { return func(o *connOpts) { o.busyTimeout = ms } }

// WithPool sets max open/idle connections. Keep small on low-RAM hosts.
func WithPool(maxOpen, maxIdle int) Option {
	return func(o *connOpts) { o.maxOpen, o.maxIdle = maxOpen, maxIdle }
}

// Open connects to SQLite, applies pragmas and runs the schema.
func Open(path string, opts ...Option) (*DB, error) {
	o := connOpts{path: path, busyTimeout: 5000, maxOpen: 4, maxIdle: 2}
	for _, f := range opts {
		f(&o)
	}

	// WAL needs a writable directory, not the file path.
	dsn := "file:" + filepath.ToSlash(path) + "?" + url.Values{
		"_pragma": []string{
			"journal_mode(WAL)",
			"synchronous(NORMAL)",
			"temp_store(MEMORY)",
			"mmap_size(268435456)",
			"cache_size(-2000)", // ~2 MB page cache: deliberate RAM cap
			"busy_timeout(" + strconv.Itoa(o.busyTimeout) + ")",
			"foreign_keys(ON)",
		},
	}.Encode()

	sqlDB, err := sqlx.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite WAL allows 1 writer + N readers; keep writers serialized.
	sqlDB.SetMaxOpenConns(o.maxOpen)
	sqlDB.SetMaxIdleConns(o.maxIdle)
	sqlDB.SetConnMaxLifetime(time.Hour)

	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	d := &DB{sqlDB}
	if err := d.Migrate(); err != nil {
		return nil, err
	}
	return d, nil
}

// Migrate applies schema.sql. Safe to run on every start.
func (d *DB) Migrate() error {
	if _, err := d.Exec(schemaSQL); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// Columns added after the initial schema: CREATE TABLE IF NOT EXISTS does
	// not touch an existing table, so they are added here and a "duplicate
	// column name" error on the second start is the expected steady state.
	for _, c := range []string{
		`ALTER TABLE albums ADD COLUMN art_hash TEXT`,
		`CREATE TABLE IF NOT EXISTS favorites (
			track_id   INTEGER PRIMARY KEY,
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
		)`,
	} {
		if _, err := d.Exec(c); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("migrate: %s: %w", c, err)
		}
	}
	return nil
}
