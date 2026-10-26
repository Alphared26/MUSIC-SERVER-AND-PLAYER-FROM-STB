// Package api contains the HTTP layer: routing, handlers and middleware.
package api

// DTO types for JSON responses.

type Health struct {
	Status    string `json:"status"`
	Version   string `json:"version"`
	BuildTime string `json:"build_time"`
	UptimeSec int64  `json:"uptime_seconds"`
}

type Track struct {
	ID            int64  `json:"id"`
	Path          string `json:"path"`
	Filename      string `json:"filename"`
	Title         string `json:"title,omitempty"`
	Artist        string `json:"artist,omitempty"`
	Album         string `json:"album,omitempty"`
	AlbumArtist   string `json:"album_artist,omitempty"`
	Composer      string `json:"composer,omitempty"`
	Genre         string `json:"genre,omitempty"`
	Year          int    `json:"year,omitempty"`
	TrackNum      int    `json:"track_num,omitempty"`
	DiscNum       int    `json:"disc_num,omitempty"`
	DurationMs    int64  `json:"duration_ms"`
	SampleRate    int    `json:"sample_rate"`
	BitsPerSample int    `json:"bits_per_sample"`
	Channels      int    `json:"channels"`
	Bitrate       int    `json:"bitrate"`
	Format        string   `json:"format"`
	FileSize      int64    `json:"file_size"`
	StreamURL     string   `json:"stream_url"`
	ArtURL        string   `json:"art_url,omitempty"`
	AddedAt       int64    `json:"added_at,omitempty"`
	SpotifyAt     *int64   `json:"spotify_at,omitempty"`
	SpotifyGenres *string  `json:"spotify_genres,omitempty"`
	Bpm           *float64 `json:"bpm,omitempty"`
	Energy        *float64 `json:"energy,omitempty"`
	Danceability  *float64 `json:"danceability,omitempty"`
}

type PaginatedTracks struct {
	Data       []Track `json:"data"`
	Page       int     `json:"page"`
	Limit      int     `json:"limit"`
	Total      int     `json:"total"`
	TotalPages int     `json:"total_pages"`
}

type Album struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Artist          string `json:"artist"`
	Year            int    `json:"year,omitempty"`
	TrackCount      int    `json:"track_count"`
	TotalDurationMs int64  `json:"total_duration_ms"`
	ArtURL          string `json:"art_url,omitempty"`
}

type PaginatedAlbums struct {
	Data       []Album `json:"data"`
	Page       int     `json:"page"`
	Limit      int     `json:"limit"`
	Total      int     `json:"total"`
	TotalPages int     `json:"total_pages"`
}

type Artist struct {
	ID         int64 `json:"id"`
	Name       string `json:"name"`
	TrackCount int   `json:"track_count"`
	AlbumCount int   `json:"album_count"`
}

type PaginatedArtists struct {
	Data       []Artist `json:"data"`
	Page       int      `json:"page"`
	Limit      int      `json:"limit"`
	Total      int      `json:"total"`
	TotalPages int      `json:"total_pages"`
}

type Genre struct {
	Name       string `json:"name"`
	TrackCount int    `json:"track_count"`
}

type Stats struct {
	Tracks          int64 `json:"tracks"`
	Albums          int64 `json:"albums"`
	Artists         int64 `json:"artists"`
	Genres          int64 `json:"genres"`
	TotalDurationMs int64 `json:"total_duration_ms"`
	TotalSizeBytes  int64 `json:"total_size_bytes"`
	FLACCount       int64 `json:"flac_count"`
	MP3Count        int64 `json:"mp3_count"`
	LastScan        int64 `json:"last_scan"`
}

type ScanJob struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	StartedAt    int64  `json:"started_at"`
	CompletedAt  int64  `json:"completed_at,omitempty"`
	FilesScanned int    `json:"files_scanned"`
	FilesAdded   int    `json:"files_added"`
	FilesUpdated int    `json:"files_updated"`
	FilesRemoved int    `json:"files_removed"`
	Error        string `json:"error,omitempty"`
}

type UploadResponse struct {
	Uploaded  []string      `json:"uploaded"`
	Failed    []UploadError `json:"failed,omitempty"`
	ScanJobID string        `json:"scan_job_id"`
}

type UploadError struct {
	Filename string `json:"filename"`
	Error    string `json:"error"`
}

type FolderEntry struct {
	Name  string `json:"name"`
	Size  int64  `json:"size,omitempty"`
	Mtime int64  `json:"mtime,omitempty"`
	IsDir bool   `json:"is_dir"`
}

type FolderListing struct {
	Folders []FolderEntry `json:"folders"`
	Files   []FolderEntry `json:"files"`
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Token     string `json:"token"`
	ExpiresIn int64  `json:"expires_in"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}