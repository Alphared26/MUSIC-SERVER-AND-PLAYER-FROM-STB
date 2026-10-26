-- SQLite schema for FLAC/MP3 library
-- Run via database.Migrate() on startup

PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA temp_store = MEMORY;
PRAGMA mmap_size = 268435456;
PRAGMA page_size = 4096;

CREATE TABLE IF NOT EXISTS tracks (
    id              INTEGER PRIMARY KEY,
    path            TEXT NOT NULL UNIQUE,
    filename        TEXT NOT NULL,
    title           TEXT,
    artist          TEXT,
    album           TEXT,
    album_artist    TEXT,
    composer        TEXT,
    genre           TEXT,
    year            INTEGER,
    track_num       INTEGER,
    disc_num        INTEGER,
    duration_ms     INTEGER NOT NULL,
    sample_rate     INTEGER NOT NULL,
    bits_per_sample INTEGER NOT NULL,
    channels        INTEGER NOT NULL,
    bitrate         INTEGER,
    format          TEXT NOT NULL CHECK (format IN ('flac','mp3','wav')),
    file_size       INTEGER NOT NULL,
    mtime           INTEGER NOT NULL,
    added_at        INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    updated_at      INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);

CREATE TABLE IF NOT EXISTS albums (
    id                  INTEGER PRIMARY KEY,
    name                TEXT NOT NULL,
    artist              TEXT NOT NULL,
    artist_id           INTEGER REFERENCES artists(id),
    year                INTEGER,
    track_count         INTEGER NOT NULL DEFAULT 0,
    total_duration_ms   INTEGER NOT NULL DEFAULT 0,
    art_path            TEXT,
    art_hash            TEXT,
    created_at          INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    updated_at          INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    UNIQUE(name, artist)
);

CREATE TABLE IF NOT EXISTS artists (
    id              INTEGER PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,
    track_count     INTEGER NOT NULL DEFAULT 0,
    album_count     INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    updated_at      INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);

CREATE TABLE IF NOT EXISTS genres (
    name            TEXT PRIMARY KEY,
    track_count     INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS scan_jobs (
    id              TEXT PRIMARY KEY,
    status          TEXT NOT NULL CHECK (status IN ('pending','running','completed','failed')),
    started_at      INTEGER NOT NULL,
    completed_at    INTEGER,
    files_scanned   INTEGER NOT NULL DEFAULT 0,
    files_added     INTEGER NOT NULL DEFAULT 0,
    files_updated   INTEGER NOT NULL DEFAULT 0,
    files_removed   INTEGER NOT NULL DEFAULT 0,
    error           TEXT
);

CREATE TABLE IF NOT EXISTS sessions (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL,
    email           TEXT,
    created_at      INTEGER NOT NULL,
    expires_at      INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_tracks_artist ON tracks(artist);
CREATE INDEX IF NOT EXISTS idx_tracks_album ON tracks(album);
CREATE INDEX IF NOT EXISTS idx_tracks_genre ON tracks(genre);
CREATE INDEX IF NOT EXISTS idx_tracks_format ON tracks(format);
CREATE INDEX IF NOT EXISTS idx_tracks_path ON tracks(path);
CREATE INDEX IF NOT EXISTS idx_tracks_mtime ON tracks(mtime);
CREATE INDEX IF NOT EXISTS idx_tracks_album_artist ON tracks(album_artist);
CREATE INDEX IF NOT EXISTS idx_albums_artist ON albums(artist);
CREATE INDEX IF NOT EXISTS idx_albums_artist_id ON albums(artist_id);
CREATE INDEX IF NOT EXISTS idx_scan_jobs_status ON scan_jobs(status);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);

CREATE TRIGGER IF NOT EXISTS trg_tracks_updated
AFTER UPDATE ON tracks
BEGIN
    UPDATE tracks SET updated_at = strftime('%s','now') WHERE id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS trg_albums_updated
AFTER UPDATE ON albums
BEGIN
    UPDATE albums SET updated_at = strftime('%s','now') WHERE id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS trg_artists_updated
AFTER UPDATE ON artists
BEGIN
    UPDATE artists SET updated_at = strftime('%s','now') WHERE id = NEW.id;
END;

CREATE VIEW IF NOT EXISTS v_library_stats AS
SELECT
    (SELECT COUNT(*) FROM tracks) AS tracks,
    (SELECT COUNT(*) FROM albums) AS albums,
    (SELECT COUNT(*) FROM artists) AS artists,
    (SELECT COUNT(*) FROM genres) AS genres,
    (SELECT COALESCE(SUM(duration_ms),0) FROM tracks) AS total_duration_ms,
    (SELECT COALESCE(SUM(file_size),0) FROM tracks) AS total_size_bytes,
    (SELECT COUNT(*) FROM tracks WHERE format='flac') AS flac_count,
    (SELECT COUNT(*) FROM tracks WHERE format='mp3') AS mp3_count,
    (SELECT COUNT(*) FROM tracks WHERE format='wav') AS wav_count,
    -- "Last scanned" is the newest finished scan job, not MAX(updated_at) of
    -- the tracks: a rescan that changes nothing still counts as a scan, and
    -- MAX(updated_at) would silently keep the old date forever.
    (SELECT MAX(completed_at) FROM scan_jobs WHERE completed_at IS NOT NULL) AS last_scan;

-- Additive migrations for databases created before a column existed.
-- CREATE TABLE IF NOT EXISTS above is a no-op on an existing table, so new
-- columns are added by db.addColumn at startup instead: putting the ALTER here
-- would abort the script at the first start and the trailing view would never
-- be created.

CREATE VIEW IF NOT EXISTS v_albums_with_art AS
SELECT a.*,
       CASE WHEN a.art_path IS NOT NULL AND a.art_path != ''
            THEN '/api/art/' || a.id
            WHEN a.art_hash IS NOT NULL AND a.art_hash != ''
            THEN '/api/artfile/' || a.art_hash
            ELSE NULL
       END as art_url
FROM albums a;