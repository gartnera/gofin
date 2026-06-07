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

// Config is the gofin server configuration loaded from YAML.
type Config struct {
	ServerName string    `yaml:"server_name"`
	Listen     string    `yaml:"listen"`
	Database   string    `yaml:"database"`
	WebRoot    string    `yaml:"web_root"`
	Libraries  []Library `yaml:"libraries"`
}

// Default values applied when a field is omitted from the YAML file.
const (
	DefaultServerName = "gofin"
	DefaultListen     = ":8096"
	DefaultDatabase   = "gofin.db"
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
	return nil
}
