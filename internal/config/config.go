// Package config loads YAML config with env overrides.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Library   LibraryConfig   `yaml:"library"`
	Database  DatabaseConfig  `yaml:"database"`
	Streaming StreamingConfig `yaml:"streaming"`
	Logging   LoggingConfig   `yaml:"logging"`
	Auth      AuthConfig      `yaml:"auth"`
	Upload    UploadConfig    `yaml:"upload"`
}

type ServerConfig struct {
	Host         string   `yaml:"host"`
	Port         int      `yaml:"port"`
	ReadTimeout  Duration `yaml:"read_timeout"`
	WriteTimeout Duration `yaml:"write_timeout"`
	IdleTimeout  Duration `yaml:"idle_timeout"`
}

type LibraryConfig struct {
	MusicRoot       string   `yaml:"music_root"`
	MusicWritable   string   `yaml:"music_writable"`
	Extensions      []string `yaml:"extensions"`
	ScanInterval    int      `yaml:"scan_interval"`
	ExtractArt      bool     `yaml:"extract_art"`
	ArtMaxSize      int      `yaml:"art_max_size"`
	ArtCacheDir     string   `yaml:"art_cache_dir"`
}

type DatabaseConfig struct {
	Path         string `yaml:"path"`
	BusyTimeout  int    `yaml:"busy_timeout"`
	MaxOpenConns int    `yaml:"max_open_conns"`
	MaxIdleConns int    `yaml:"max_idle_conns"`
}

type StreamingConfig struct {
	ChunkSize int64 `yaml:"chunk_size"`
	Sendfile  bool  `yaml:"sendfile"`
}

type LoggingConfig struct {
	Level string `yaml:"level"`
}

type AuthConfig struct {
	Enabled           bool     `yaml:"enabled"`
	CloudflareAccess  bool     `yaml:"cloudflare_access"`
	TrustedEmailDomain string  `yaml:"trusted_email_domain"`
	LocalUsers        []User   `yaml:"local_users"`
	SessionTTL        Duration `yaml:"session_ttl"`
}

// User is a local fallback account. Password must be a bcrypt hash.
type User struct {
	Username     string `yaml:"username"`
	PasswordHash string `yaml:"password_hash"`
}

type UploadConfig struct {
	MaxFileSize   int64    `yaml:"max_file_size"`
	AllowedFormats []string `yaml:"allowed_formats"`
	TempDir       string   `yaml:"temp_dir"`
}

// Duration is a time.Duration that unmarshals from YAML strings like "30s".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) D() time.Duration { return time.Duration(d) }

// Addr is the listen address.
func (s ServerConfig) Addr() string { return fmt.Sprintf("%s:%d", s.Host, s.Port) }

// Load reads CONFIG_PATH (default /config.yaml) and applies env overrides.
// Missing config file is not fatal: defaults are used.
func Load(path string) (*Config, error) {
	cfg := defaults()

	if path == "" {
		path = os.Getenv("CONFIG_PATH")
	}
	if path == "" {
		path = "/config.yaml"
	}

	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(b, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	case os.IsNotExist(err):
		// defaults + env only
	default:
		return nil, err
	}

	applyEnv(cfg)
	return cfg, nil
}

func defaults() *Config {
	return &Config{
		Server: ServerConfig{
			Host:         "0.0.0.0",
			Port:         8080,
			ReadTimeout:  Duration(30 * time.Second),
			WriteTimeout: Duration(300 * time.Second),
			IdleTimeout:  Duration(120 * time.Second),
		},
		Library: LibraryConfig{
			MusicRoot:     "/music",
			MusicWritable: "/music_writable",
			Extensions:    []string{".flac", ".mp3"},
			ScanInterval:  300,
			ExtractArt:    true,
			ArtMaxSize:    500,
			ArtCacheDir:   "/data/art",
		},
		Database: DatabaseConfig{
			Path:         "/data/library.db",
			BusyTimeout:  5000,
			MaxOpenConns: 4,
			MaxIdleConns: 2,
		},
		Streaming: StreamingConfig{ChunkSize: 65536, Sendfile: true},
		Logging:   LoggingConfig{Level: "info"},
		Auth: AuthConfig{
			Enabled:      false,
			SessionTTL:   Duration(24 * time.Hour),
			LocalUsers:   []User{},
		},
		Upload: UploadConfig{
			MaxFileSize:    512 << 20,
			AllowedFormats: []string{"flac", "mp3"},
			TempDir:        "/data/tmp",
		},
	}
}

// applyEnv overrides config from env. Keys use SCREAMING_SNAKE with _ for .
func applyEnv(c *Config) {
	envStr("SERVER_HOST", &c.Server.Host)
	envInt("SERVER_PORT", &c.Server.Port)
	envStr("LIBRARY_MUSIC_ROOT", &c.Library.MusicRoot)
	envStr("LIBRARY_MUSIC_WRITABLE", &c.Library.MusicWritable)
	envInt("LIBRARY_SCAN_INTERVAL", &c.Library.ScanInterval)
	envInt("LIBRARY_ART_MAX_SIZE", &c.Library.ArtMaxSize)
	envStr("LIBRARY_ART_CACHE_DIR", &c.Library.ArtCacheDir)
	envStr("DATABASE_PATH", &c.Database.Path)
	envInt("DATABASE_BUSY_TIMEOUT", &c.Database.BusyTimeout)
	envInt64("STREAMING_CHUNK_SIZE", &c.Streaming.ChunkSize)
	envStr("LOGGING_LEVEL", &c.Logging.Level)
	envBool("AUTH_ENABLED", &c.Auth.Enabled)
	envBool("AUTH_CLOUDFLARE_ACCESS", &c.Auth.CloudflareAccess)
	envStr("AUTH_TRUSTED_EMAIL_DOMAIN", &c.Auth.TrustedEmailDomain)
	envInt64("UPLOAD_MAX_FILE_SIZE", &c.Upload.MaxFileSize)
	envStr("UPLOAD_TEMP_DIR", &c.Upload.TempDir)
	if v := os.Getenv("LIBRARY_EXTENSIONS"); v != "" {
		c.Library.Extensions = strings.Split(v, ",")
	}
}

func envStr(k string, dst *string) {
	if v, ok := os.LookupEnv(k); ok {
		*dst = v
	}
}

func envInt(k string, dst *int) {
	if v, ok := os.LookupEnv(k); ok {
		if n, err := strconv.Atoi(v); err == nil {
			*dst = n
		}
	}
}

func envInt64(k string, dst *int64) {
	if v, ok := os.LookupEnv(k); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			*dst = n
		}
	}
}

func envBool(k string, dst *bool) {
	if v, ok := os.LookupEnv(k); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			*dst = b
		}
	}
}
