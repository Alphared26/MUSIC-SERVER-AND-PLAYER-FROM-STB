package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Track is a row in the tracks table.
type Track struct {
	ID            int64     `db:"id"             json:"id"`
	Path          string    `db:"path"           json:"path"`
	Filename      string    `db:"filename"       json:"filename"`
	Title         *string   `db:"title"          json:"title"`
	Artist        *string   `db:"artist"         json:"artist"`
	Album         *string   `db:"album"          json:"album"`
	AlbumArtist   *string   `db:"album_artist"   json:"album_artist"`
	Composer      *string   `db:"composer"       json:"composer"`
	Genre         *string   `db:"genre"          json:"genre"`
	Year          *int      `db:"year"           json:"year"`
	TrackNum      *int      `db:"track_num"      json:"track_num"`
	DiscNum       *int      `db:"disc_num"       json:"disc_num"`
	DurationMs    int64     `db:"duration_ms"    json:"duration_ms"`
	SampleRate    int       `db:"sample_rate"    json:"sample_rate"`
	BitsPerSample int       `db:"bits_per_sample" json:"bits_per_sample"`
	Channels      int       `db:"channels"       json:"channels"`
	Bitrate       int       `db:"bitrate"        json:"bitrate"`
	Format        string    `db:"format"         json:"format"`
	FileSize      int64     `db:"file_size"      json:"file_size"`
	Mtime         int64     `db:"mtime"          json:"mtime"`
	AddedAt       int64     `db:"added_at"       json:"added_at"`
	UpdatedAt     int64     `db:"updated_at"     json:"updated_at"`
	SpotifyAt     *int64    `db:"spotify_at"     json:"spotify_at,omitempty"`
	SpotifyGenres *string   `db:"spotify_genres" json:"spotify_genres,omitempty"`
	Bpm           *float64  `db:"bpm"            json:"bpm,omitempty"`
	Energy        *float64  `db:"energy"         json:"energy,omitempty"`
	Danceability  *float64  `db:"danceability"   json:"danceability,omitempty"`
	ArtURL        *string   `db:"art_url"        json:"-"`
}

// Album is a row in the albums table.
type Album struct {
	ID              int64  `db:"id"                json:"id"`
	Name            string `db:"name"              json:"name"`
	Artist          string `db:"artist"            json:"artist"`
	ArtistID        *int64 `db:"artist_id"         json:"artist_id"`
	Year            *int   `db:"year"              json:"year"`
	TrackCount      int    `db:"track_count"       json:"track_count"`
	TotalDurationMs int64  `db:"total_duration_ms" json:"total_duration_ms"`
	ArtPath         *string `db:"art_path"         json:"-"`
	ArtHash         *string `db:"art_hash"         json:"-"`
	ArtURL          *string `db:"art_url"          json:"art_url"`
}

// Artist is a row in the artists table.
type Artist struct {
	ID         int64  `db:"id"          json:"id"`
	Name       string `db:"name"        json:"name"`
	TrackCount int    `db:"track_count" json:"track_count"`
	AlbumCount int    `db:"album_count" json:"album_count"`
}

// Genre is a row in the genres table.
type Genre struct {
	Name       string `db:"name"        json:"name"`
	TrackCount int    `db:"track_count" json:"track_count"`
}

// Stats aggregates library counters.
type Stats struct {
	Tracks          int64 `db:"tracks"            json:"tracks"`
	Albums          int64 `db:"albums"            json:"albums"`
	Artists         int64 `db:"artists"           json:"artists"`
	Genres          int64 `db:"genres"            json:"genres"`
	TotalDurationMs int64 `db:"total_duration_ms" json:"total_duration_ms"`
	TotalSizeBytes  int64 `db:"total_size_bytes"  json:"total_size_bytes"`
	FLACCount       int64 `db:"flac_count"        json:"flac_count"`
	MP3Count        int64 `db:"mp3_count"         json:"mp3_count"`
	WAVCount        int64 `db:"wav_count"         json:"wav_count"`
	LastScan        int64 `db:"last_scan"         json:"last_scan"`
}

// ScanJob tracks one library scan.
type ScanJob struct {
	ID           string `db:"id"            json:"id"`
	Status       string `db:"status"        json:"status"`
	StartedAt    int64  `db:"started_at"    json:"started_at"`
	CompletedAt  *int64 `db:"completed_at"  json:"completed_at"`
	FilesScanned int    `db:"files_scanned" json:"files_scanned"`
	FilesAdded   int    `db:"files_added"   json:"files_added"`
	FilesUpdated int    `db:"files_updated" json:"files_updated"`
	FilesRemoved int    `db:"files_removed" json:"files_removed"`
	Error        *string `db:"error"        json:"error"`
}

const trackCols = `tracks.id, tracks.path, tracks.filename, tracks.title, tracks.artist,
	tracks.album, tracks.album_artist, tracks.composer, tracks.genre, tracks.year,
	tracks.track_num, tracks.disc_num, tracks.duration_ms, tracks.sample_rate,
	tracks.bits_per_sample, tracks.channels, tracks.bitrate, tracks.format,
	tracks.file_size, tracks.mtime, tracks.added_at, tracks.updated_at,
	tracks.spotify_at, tracks.spotify_genres, tracks.bpm, tracks.energy, tracks.danceability`

// artCol is appended to trackCols so a track row carries its album cover URL.
// It mirrors v_albums_with_art: NULL when there is no cover, so the client
// falls back to a placeholder instead of a broken image. The join key is the
// same (album, COALESCE(album_artist, artist)) pair RebuildAggregates groups by,
// and albums is UNIQUE(name, artist) so the join cannot fan out track rows.
// The AS art_url alias is required: sqlx maps by column name for both
// rows.Scan and the struct-scanning Get, and an unaliased expression has none.
const artCol = `, CASE WHEN al.art_path IS NOT NULL AND al.art_path != ''
	THEN '/api/art/' || al.id
	WHEN al.art_hash IS NOT NULL AND al.art_hash != ''
	THEN '/api/artfile/' || al.art_hash
	ELSE NULL END AS art_url`

const artJoin = ` LEFT JOIN albums al ON al.name = tracks.album
	AND al.artist = COALESCE(tracks.album_artist, tracks.artist)`

// UpsertTrack inserts or refreshes a track row by path.
// Returns true when a new row was inserted.
func (d *DB) UpsertTrack(t *Track) (inserted bool, err error) {
	var exists int
	err = d.Get(&exists, `SELECT COUNT(*) FROM tracks WHERE path = ?`, t.Path)
	if err != nil {
		return false, err
	}
	inserted = exists == 0

	_, err = d.Exec(`
		INSERT INTO tracks (path, filename, title, artist, album, album_artist, composer, genre,
			year, track_num, disc_num, duration_ms, sample_rate, bits_per_sample, channels,
			bitrate, format, file_size, mtime)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET
			filename=excluded.filename, title=excluded.title, artist=excluded.artist,
			album=excluded.album, album_artist=excluded.album_artist, composer=excluded.composer,
			genre=excluded.genre, year=excluded.year, track_num=excluded.track_num,
			disc_num=excluded.disc_num, duration_ms=excluded.duration_ms,
			sample_rate=excluded.sample_rate, bits_per_sample=excluded.bits_per_sample,
			channels=excluded.channels, bitrate=excluded.bitrate, format=excluded.format,
			file_size=excluded.file_size, mtime=excluded.mtime`,
		t.Path, t.Filename, t.Title, t.Artist, t.Album, t.AlbumArtist, t.Composer, t.Genre,
		t.Year, t.TrackNum, t.DiscNum, t.DurationMs, t.SampleRate, t.BitsPerSample, t.Channels,
		t.Bitrate, t.Format, t.FileSize, t.Mtime)
	return inserted, err
}

// GetTrack loads a track by id.
func (d *DB) GetTrack(id int64) (*Track, error) {
	var t Track
	err := d.Get(&t, `SELECT `+trackCols+artCol+` FROM tracks`+artJoin+` WHERE tracks.id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &t, err
}

// TrackFilter narrows a track listing.
type TrackFilter struct {
	Query         string
	Artist        string
	Album         string
	Genre         string
	Format        string
	FavoritesOnly bool
	// Sort: "" (default), "title", "artist", "added"
	Sort string
	// Desc flips sort direction for sortable fields.
	Desc   bool
	Limit  int
	Offset int
	// NoAlbum matches only untagged tracks (empty/NULL album). Used by the
	// Subsonic layer to expose a synthetic album so untagged files stay
	// reachable in clients that only browse album->song.
	NoAlbum bool
}

// ListTracks returns a page of tracks plus the total match count.
func (d *DB) ListTracks(f TrackFilter) ([]Track, int, error) {
	where, args := f.clauses()

	var total int
	if err := d.Get(&total, `SELECT COUNT(*) FROM tracks`+where, args...); err != nil {
		return nil, 0, err
	}

	limit := f.Limit
	if limit <= 0 {
		limit = -1
	}
	order := ` ORDER BY COALESCE(tracks.artist,''), COALESCE(tracks.album,''), COALESCE(tracks.track_num, 0), tracks.filename`
	if f.FavoritesOnly && (f.Sort == "" || f.Sort == "added") {
		dir := "DESC"
		if !f.Desc && f.Sort == "added" {
			dir = "ASC"
		}
		order = ` ORDER BY (SELECT created_at FROM favorites WHERE favorites.track_id = tracks.id) ` + dir + `, tracks.id ` + dir
	} else if f.Sort == "added" {
		dir := "DESC"
		if !f.Desc {
			dir = "ASC"
		}
		order = ` ORDER BY CAST(COALESCE(tracks.spotify_at, tracks.added_at) AS INTEGER) ` + dir + `, tracks.id ` + dir
	} else if f.Sort == "title" {
		dir := "ASC"
		if f.Desc {
			dir = "DESC"
		}
		order = ` ORDER BY COALESCE(tracks.title, tracks.filename) ` + dir
	} else if f.Sort == "artist" {
		dir := "ASC"
		if f.Desc {
			dir = "DESC"
		}
		order = ` ORDER BY COALESCE(tracks.artist, '') ` + dir + `, COALESCE(tracks.title, '') ` + dir
	}
	q := `SELECT ` + trackCols + artCol + ` FROM tracks` + artJoin + where + order + ` LIMIT ? OFFSET ?`
	rows, err := d.Query(q, append(append([]any{}, args...), limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	list, err := scanTracks(rows)
	return list, total, err
}

func (f TrackFilter) clauses() (string, []any) {
	// Column names are qualified with "tracks." because the track query joins
	// albums, whose columns (artist) would otherwise be ambiguous.
	var cond []string
	var args []any
	add := func(clause string, v any) {
		cond = append(cond, clause)
		args = append(args, v)
	}
	if f.Query != "" {
		like := "%" + f.Query + "%"
		cond = append(cond, `(tracks.title LIKE ? OR tracks.filename LIKE ? OR tracks.artist LIKE ? OR tracks.album LIKE ?)`)
		args = append(args, like, like, like, like)
	}
	if f.Artist != "" {
		cond = append(cond, `(tracks.artist = ? OR tracks.album_artist = ?)`)
		args = append(args, f.Artist, f.Artist)
	}
	if f.Album != "" {
		add(`tracks.album = ?`, f.Album)
	}
	if f.Genre != "" {
		add(`tracks.genre = ?`, f.Genre)
	}
	if f.Format != "" {
		add(`tracks.format = ?`, f.Format)
	}
	if f.FavoritesOnly {
		cond = append(cond, `EXISTS (SELECT 1 FROM favorites WHERE favorites.track_id = tracks.id)`)
	}
	if f.NoAlbum {
		cond = append(cond, `(tracks.album IS NULL OR tracks.album = '')`)
	}
	if len(cond) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(cond, " AND "), args
}

func scanTracks(rows *sql.Rows) ([]Track, error) {
	var out []Track
	for rows.Next() {
		var t Track
		if err := rows.Scan(&t.ID, &t.Path, &t.Filename, &t.Title, &t.Artist, &t.Album,
			&t.AlbumArtist, &t.Composer, &t.Genre, &t.Year, &t.TrackNum, &t.DiscNum,
			&t.DurationMs, &t.SampleRate, &t.BitsPerSample, &t.Channels, &t.Bitrate,
			&t.Format, &t.FileSize, &t.Mtime, &t.AddedAt, &t.UpdatedAt,
			&t.SpotifyAt, &t.SpotifyGenres, &t.Bpm, &t.Energy, &t.Danceability,
			&t.ArtURL); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteTrack removes a track row and returns its path.
func (d *DB) DeleteTrack(id int64) (path string, err error) {
	t, err := d.GetTrack(id)
	if err != nil {
		return "", err
	}
	if _, err := d.Exec(`DELETE FROM tracks WHERE id = ?`, id); err != nil {
		return "", err
	}
	return t.Path, nil
}

// RemoveMissingPaths deletes rows whose path is not in the existing set.
// It deletes by explicit list to stay correct even if a subtree is unreadable.
func (d *DB) RemoveMissingPaths(missing []string) (int, error) {
	if len(missing) == 0 {
		return 0, nil
	}
	tx, err := d.Beginx()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n := 0
	for _, p := range missing {
		res, err := tx.Exec(`DELETE FROM tracks WHERE path = ?`, p)
		if err != nil {
			return 0, err
		}
		if r, _ := res.RowsAffected(); r > 0 {
			n++
		}
	}
	return n, tx.Commit()
}

// AllTrackPaths returns every indexed path. Used to diff against disk.
func (d *DB) AllTrackPaths() (map[string]bool, error) {
	rows, err := d.Query(`SELECT path FROM tracks`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]bool, 1024)
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out[p] = true
	}
	return out, rows.Err()
}

// AllTrackMtimes returns indexed path -> mtime for cheap rescan diffing.
func (d *DB) AllTrackMtimes() (map[string]int64, error) {
	rows, err := d.Query(`SELECT path, mtime FROM tracks`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int64, 1024)
	for rows.Next() {
		var p string
		var m int64
		if err := rows.Scan(&p, &m); err != nil {
			return nil, err
		}
		out[p] = m
	}
	return out, rows.Err()
}

// GetStats reads the library stats view.
func (d *DB) GetStats() (*Stats, error) {
	var s Stats
	err := d.Get(&s, `SELECT tracks, albums, artists, genres, total_duration_ms,
		total_size_bytes, flac_count, mp3_count, wav_count, COALESCE(last_scan, 0) AS last_scan FROM v_library_stats`)
	return &s, err
}

// RebuildAggregates recomputes artists, albums and genres from tracks.
// Called after a scan; cheap enough for tens of thousands of rows.
func (d *DB) RebuildAggregates() error {
	tx, err := d.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// artists
	// Both roles count: tracks.artist is the performer, tracks.album_artist is
	// the credited act (often the one on the cover). Seeding from only one
	// role hides the other from the artist list, so a track tagged
	// artist="X" album_artist="Y" used to list X but never Y.
	if _, err := tx.Exec(`
		INSERT INTO artists (name)
		SELECT name FROM (
			SELECT artist AS name FROM tracks WHERE artist IS NOT NULL AND artist != ''
			UNION
			SELECT album_artist AS name FROM tracks WHERE album_artist IS NOT NULL AND album_artist != ''
		) WHERE name != ''
		ON CONFLICT(name) DO NOTHING`); err != nil {
		return err
	}
	// albums
	if _, err := tx.Exec(`
		INSERT INTO albums (name, artist) SELECT album, COALESCE(album_artist, artist, 'Unknown Artist')
		FROM tracks WHERE album IS NOT NULL AND album != '' AND COALESCE(artist,'') != ''
		GROUP BY album, COALESCE(album_artist, artist, 'Unknown Artist')
		ON CONFLICT(name, artist) DO NOTHING`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE albums SET
			artist_id = (SELECT id FROM artists WHERE artists.name = albums.artist),
			year = (SELECT MIN(year) FROM tracks WHERE tracks.album = albums.name
			        AND COALESCE(tracks.album_artist, tracks.artist) = albums.artist),
			track_count = (SELECT COUNT(*) FROM tracks WHERE tracks.album = albums.name
			        AND COALESCE(tracks.album_artist, tracks.artist) = albums.artist),
			total_duration_ms = (SELECT COALESCE(SUM(duration_ms),0) FROM tracks
			        WHERE tracks.album = albums.name AND COALESCE(tracks.album_artist, tracks.artist) = albums.artist)
		`); err != nil {
		return err
	}
	// Drop albums whose tracks are all gone. Without this a deleted album keeps
	// showing in the UI with track_count 0, and its cover too.
	if _, err := tx.Exec(`
		DELETE FROM albums WHERE NOT EXISTS (
			SELECT 1 FROM tracks
			WHERE tracks.album = albums.name
			  AND COALESCE(tracks.album_artist, tracks.artist) = albums.artist)`); err != nil {
		return err
	}
	// genres
	if _, err := tx.Exec(`DELETE FROM genres`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO genres (name, track_count) SELECT genre, COUNT(*) FROM tracks
		WHERE genre IS NOT NULL AND genre != '' GROUP BY genre`); err != nil {
		return err
	}
	// artist counters
	// Drop artists that no longer appear on any track in either role, so the
	// list does not accumulate zero-count rows after a delete.
	if _, err := tx.Exec(`
		DELETE FROM artists WHERE NOT EXISTS (
			SELECT 1 FROM tracks
			WHERE artists.name IN (tracks.artist, tracks.album_artist))`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE artists SET
			track_count = (SELECT COUNT(*) FROM tracks
				WHERE artists.name IN (tracks.artist, tracks.album_artist)),
			album_count = (SELECT COUNT(*) FROM albums WHERE albums.artist = artists.name)
		`); err != nil {
		return err
	}
	return tx.Commit()
}

// ListAlbums returns a page of albums with an optional artist filter.
func (d *DB) ListAlbums(artist, query string, limit, offset int) ([]Album, int, error) {
	var cond []string
	var args []any
	if artist != "" {
		cond = append(cond, `artist = ?`)
		args = append(args, artist)
	}
	if query != "" {
		cond = append(cond, `name LIKE ?`)
		args = append(args, "%"+query+"%")
	}
	where := ""
	if len(cond) > 0 {
		where = " WHERE " + strings.Join(cond, " AND ")
	}
	if limit <= 0 {
		limit = -1
	}
	var total int
	if err := d.Get(&total, `SELECT COUNT(*) FROM albums`+where, args...); err != nil {
		return nil, 0, err
	}
	rows, err := d.Query(`SELECT id, name, artist, artist_id, year, track_count,
		total_duration_ms, art_path, art_hash,
		CASE WHEN art_path IS NOT NULL AND art_path != ''
		THEN '/api/art/' || id
		WHEN art_hash IS NOT NULL AND art_hash != ''
		THEN '/api/artfile/' || art_hash
		ELSE NULL END AS art_url
		FROM albums`+where+
		` ORDER BY artist, year, name LIMIT ? OFFSET ?`,
		append(append([]any{}, args...), limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Album{}
	for rows.Next() {
		var a Album
		if err := rows.Scan(&a.ID, &a.Name, &a.Artist, &a.ArtistID, &a.Year, &a.TrackCount,
			&a.TotalDurationMs, &a.ArtPath, &a.ArtHash, &a.ArtURL); err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}

// GetAlbum loads one album.
func (d *DB) GetAlbum(id int64) (*Album, error) {
	var a Album
	// The art_url literal needs an alias: sqlx maps columns by name, and a bare
	// NULL has none. Building the URL here keeps the caller from having to.
	err := d.Get(&a, `SELECT id, name, artist, artist_id, year, track_count,
		total_duration_ms, art_path, art_hash,
		CASE WHEN art_path IS NOT NULL AND art_path != ''
		THEN '/api/art/' || id
		WHEN art_hash IS NOT NULL AND art_hash != ''
		THEN '/api/artfile/' || art_hash
		ELSE NULL END AS art_url
		FROM albums WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

// GetAlbumByNameAndArtist loads an album by name and artist (join key).
func (d *DB) GetAlbumByNameAndArtist(name, artist string) (*Album, error) {
	var a Album
	err := d.Get(&a, `SELECT id, name, artist, artist_id, year, track_count,
		total_duration_ms, art_path, art_hash,
		CASE WHEN art_path IS NOT NULL AND art_path != ''
		THEN '/api/art/' || id
		WHEN art_hash IS NOT NULL AND art_hash != ''
		THEN '/api/artfile/' || art_hash
		ELSE NULL END AS art_url
		FROM albums WHERE name = ? AND artist = ?`, name, artist)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

// ListArtists returns artists with an optional name filter.
func (d *DB) ListArtists(query string, limit, offset int) ([]Artist, int, error) {
	where, args := "", []any{}
	if query != "" {
		where = ` WHERE name LIKE ?`
		args = append(args, "%"+query+"%")
	}
	if limit <= 0 {
		limit = -1
	}
	var total int
	if err := d.Get(&total, `SELECT COUNT(*) FROM artists`+where, args...); err != nil {
		return nil, 0, err
	}
	rows, err := d.Query(`SELECT id, name, track_count, album_count FROM artists`+where+
		` ORDER BY name LIMIT ? OFFSET ?`, append(append([]any{}, args...), limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Artist{}
	for rows.Next() {
		var a Artist
		if err := rows.Scan(&a.ID, &a.Name, &a.TrackCount, &a.AlbumCount); err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}

// GetArtist loads one artist.
func (d *DB) GetArtist(id int64) (*Artist, error) {
	var a Artist
	err := d.Get(&a, `SELECT id, name, track_count, album_count FROM artists WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

// ListGenres returns all genres with counts.
func (d *DB) ListGenres() ([]Genre, error) {
	rows, err := d.Query(`SELECT name, track_count FROM genres ORDER BY track_count DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Genre{}
	for rows.Next() {
		var g Genre
		if err := rows.Scan(&g.Name, &g.TrackCount); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SetAlbumArt records the extracted cover path for an album.
func (d *DB) SetAlbumArt(albumID int64, artPath string) error {
	_, err := d.Exec(`UPDATE albums SET art_path = ? WHERE id = ?`, artPath, albumID)
	return err
}

// SetAlbumArtHash records the cache filename of art extracted from an album's
// embedded PICTURE blocks. It is separate from SetAlbumArt because art_path
// means a real file under a library root, which the resolver can serve.
func (d *DB) SetAlbumArtHash(name, artist, hash string) error {
	res, err := d.Exec(`UPDATE albums SET art_hash = ? WHERE name = ? AND artist = ?`,
		hash, name, artist)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		_, err = d.Exec(`INSERT OR IGNORE INTO albums (name, artist, art_hash) VALUES (?, ?, ?)`,
			name, artist, hash)
	}
	return err
}

// UpdateTrackMetadata updates editable tags of a track and refreshes aggregates.
func (d *DB) UpdateTrackMetadata(id int64, title, artist, album, genre *string, bpm *float64) error {
	_, err := d.Exec(`
		UPDATE tracks SET
			title = COALESCE(?, title),
			artist = COALESCE(?, artist),
			album = COALESCE(?, album),
			genre = COALESCE(?, genre),
			bpm = COALESCE(?, bpm),
			updated_at = strftime('%s','now')
		WHERE id = ?`,
		title, artist, album, genre, bpm, id)
	if err != nil {
		return err
	}
	go func() {
		_ = d.RebuildAggregates()
	}()
	return nil
}

// AddFavorite adds a track to favorites.
func (d *DB) AddFavorite(trackID int64) error {
	_, err := d.Exec(`INSERT OR IGNORE INTO favorites (track_id, created_at) VALUES (?, strftime('%s','now'))`, trackID)
	return err
}

// RemoveFavorite removes a track from favorites.
func (d *DB) RemoveFavorite(trackID int64) error {
	_, err := d.Exec(`DELETE FROM favorites WHERE track_id = ?`, trackID)
	return err
}

// ListFavorites returns all favorited track IDs ordered newest first.
func (d *DB) ListFavorites() ([]int64, error) {
	var ids []int64
	err := d.Select(&ids, `SELECT track_id FROM favorites ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// AlbumArtTarget is one album plus the indexed path of one of its tracks.
// The sample path lets the scanner derive the album directory on disk without
// storing a directory column.
type AlbumArtTarget struct {
	ID      int64
	ArtPath string
	Sample  string
}

// ListAlbumArt returns every album with its current art_path and one sample
// track path, for cover discovery after a scan.
func (d *DB) ListAlbumArt() ([]AlbumArtTarget, error) {
	rows, err := d.Query(`SELECT a.id, COALESCE(a.art_path,''), COALESCE((
		SELECT t.path FROM tracks t
		WHERE t.album = a.name AND COALESCE(t.album_artist, t.artist) = a.artist
		LIMIT 1), '') FROM albums a`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlbumArtTarget{}
	for rows.Next() {
		var a AlbumArtTarget
		if err := rows.Scan(&a.ID, &a.ArtPath, &a.Sample); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ScanJob persistence -------------------------------------------------

func (d *DB) CreateScanJob(id string) error {
	_, err := d.Exec(`INSERT INTO scan_jobs (id, status, started_at) VALUES (?, 'pending', ?)`,
		id, time.Now().Unix())
	return err
}

func (d *DB) UpdateScanJob(id, status string, added, updated, removed int, jobErr error) error {
	var errStr *string
	if jobErr != nil {
		s := jobErr.Error()
		errStr = &s
	}
	var completedAt *int64
	if status == "completed" || status == "failed" {
		now := time.Now().Unix()
		completedAt = &now
	}
	_, err := d.Exec(`UPDATE scan_jobs SET status = ?, completed_at = ?,
		files_added = ?, files_updated = ?, files_removed = ?, error = ? WHERE id = ?`,
		status, completedAt, added, updated, removed, errStr, id)
	return err
}

// GetScanJob loads a scan job by id.
func (d *DB) GetScanJob(id string) (*ScanJob, error) {
	var j ScanJob
	err := d.Get(&j, `SELECT id, status, started_at, completed_at, files_scanned,
		files_added, files_updated, files_removed, error FROM scan_jobs WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &j, err
}

var _ = fmt.Sprintf
