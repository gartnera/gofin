package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Library describes a single type-tagged media folder.
type Library struct {
	Name string `yaml:"name"`
	// Type is one of: movies, tvshows, music.
	Type string `yaml:"type"`
	Path string `yaml:"path"`
}

// Metadata configures optional remote metadata fetching. When Enabled is false
// (the default) gofin makes no outbound network calls and relies solely on local
// filename/NFO/artwork sources.
type Metadata struct {
	Enabled bool `yaml:"enabled"`
	// TMDbToken is a TMDb v4 read-access token (required when Enabled).
	TMDbToken string `yaml:"tmdb_token"`
	// CacheDir is where downloaded posters are cached on disk.
	CacheDir string `yaml:"cache_dir"`
	// TTLDays is how long a cached remote response is considered fresh.
	TTLDays int `yaml:"ttl_days"`
}

// Watch tunes the filesystem watcher that keeps the index live. Its defaults
// bound the number of inotify watches on large libraries (Linux enforces
// fs.inotify.max_user_watches): container directories are always watched so new
// folders are detected, but individual leaf directories (a movie folder, a
// season, an album) get a watch only when recently modified.
type Watch struct {
	// WindowDays is the recency window for watching a leaf directory at startup:
	// only leaves modified within this many days are watched. 0 watches every
	// directory (the pre-windowing behaviour). Omitted defaults to 7.
	WindowDays *int `yaml:"window_days"`
	// RescanHours is the interval of a periodic full rescan that heals drift in
	// directories the window left unwatched (e.g. a file replaced in an old movie
	// folder). 0 disables it. Omitted defaults to 24.
	RescanHours *int `yaml:"rescan_hours"`
}

// Config is the gofin server configuration loaded from YAML.
type Config struct {
	ServerName string    `yaml:"server_name"`
	Listen     string    `yaml:"listen"`
	Database   string    `yaml:"database"`
	WebRoot    string    `yaml:"web_root"`
	Libraries  []Library `yaml:"libraries"`
	Metadata   Metadata  `yaml:"metadata"`
	Watch      Watch     `yaml:"watch"`
	// QuickConnect toggles Quick Connect login. Enabled by default; set to false
	// to advertise it as disabled and reject the handshake endpoints.
	QuickConnect *bool `yaml:"quick_connect"`
	// Discovery toggles the UDP client auto-discovery responder (port 7359).
	// Enabled by default; set to false to stop answering discovery broadcasts.
	Discovery *bool `yaml:"discovery"`
}

// QuickConnectEnabled reports whether Quick Connect is enabled, defaulting to
// true when the config omits the field.
func (c *Config) QuickConnectEnabled() bool {
	return c.QuickConnect == nil || *c.QuickConnect
}

// DiscoveryEnabled reports whether the UDP auto-discovery responder is enabled,
// defaulting to true when the config omits the field.
func (c *Config) DiscoveryEnabled() bool {
	return c.Discovery == nil || *c.Discovery
}

// WatchWindowDays returns the leaf-directory recency window in days, defaulting
// to DefaultWatchWindowDays when the config omits it (an explicit 0 means watch
// every directory).
func (c *Config) WatchWindowDays() int {
	if c.Watch.WindowDays == nil {
		return DefaultWatchWindowDays
	}
	return *c.Watch.WindowDays
}

// WatchRescanHours returns the periodic full-rescan interval in hours,
// defaulting to DefaultWatchRescanHours when the config omits it (an explicit 0
// disables the periodic rescan).
func (c *Config) WatchRescanHours() int {
	if c.Watch.RescanHours == nil {
		return DefaultWatchRescanHours
	}
	return *c.Watch.RescanHours
}

// Default values applied when a field is omitted from the YAML file.
const (
	DefaultServerName   = "gofin"
	DefaultListen       = ":8096"
	DefaultDatabase     = "gofin.db"
	DefaultMetaCacheDir = "metadata-cache"
	DefaultMetaTTLDays  = 14
	// DefaultWatchWindowDays is the leaf-directory watch recency window applied
	// when watch.window_days is omitted.
	DefaultWatchWindowDays = 7
	// DefaultWatchRescanHours is the periodic full-rescan interval applied when
	// watch.rescan_hours is omitted.
	DefaultWatchRescanHours = 24
)

// Load reads and validates a YAML config file from path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.ServerName == "" {
		c.ServerName = DefaultServerName
	}
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	if c.Database == "" {
		c.Database = DefaultDatabase
	}
	if c.Metadata.CacheDir == "" {
		c.Metadata.CacheDir = DefaultMetaCacheDir
	}
	if c.Metadata.TTLDays == 0 {
		c.Metadata.TTLDays = DefaultMetaTTLDays
	}
}

func (c *Config) validate() error {
	valid := map[string]bool{"movies": true, "tvshows": true, "music": true}
	for i, lib := range c.Libraries {
		if lib.Name == "" {
			return fmt.Errorf("library %d: name is required", i)
		}
		if lib.Path == "" {
			return fmt.Errorf("library %q: path is required", lib.Name)
		}
		if !valid[lib.Type] {
			return fmt.Errorf("library %q: type must be movies, tvshows or music (got %q)", lib.Name, lib.Type)
		}
	}
	if c.Metadata.Enabled && c.Metadata.TMDbToken == "" {
		return fmt.Errorf("metadata.tmdb_token is required when metadata.enabled is true")
	}
	return nil
}
