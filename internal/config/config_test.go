package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gofin.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDefaults(t *testing.T) {
	path := writeConfig(t, "libraries: []\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ServerName != DefaultServerName {
		t.Errorf("ServerName = %q, want %q", cfg.ServerName, DefaultServerName)
	}
	if cfg.Listen != DefaultListen {
		t.Errorf("Listen = %q, want %q", cfg.Listen, DefaultListen)
	}
	if cfg.Database != DefaultDatabase {
		t.Errorf("Database = %q, want %q", cfg.Database, DefaultDatabase)
	}
}

func TestLoadFull(t *testing.T) {
	path := writeConfig(t, `
server_name: myserver
listen: ":9000"
database: /tmp/test.db
libraries:
  - name: Movies
    type: movies
    path: /media/movies
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ServerName != "myserver" || cfg.Listen != ":9000" {
		t.Errorf("unexpected config: %+v", cfg)
	}
	if len(cfg.Libraries) != 1 || cfg.Libraries[0].Type != "movies" {
		t.Errorf("unexpected libraries: %+v", cfg.Libraries)
	}
}

func TestWatchDefaults(t *testing.T) {
	path := writeConfig(t, "libraries: []\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.WatchWindowDays(); got != DefaultWatchWindowDays {
		t.Errorf("WatchWindowDays() = %d, want %d", got, DefaultWatchWindowDays)
	}
	if got := cfg.WatchRescanHours(); got != DefaultWatchRescanHours {
		t.Errorf("WatchRescanHours() = %d, want %d", got, DefaultWatchRescanHours)
	}
}

func TestWatchExplicitZero(t *testing.T) {
	// An explicit 0 is distinct from omitted: watch everything / disable rescan.
	path := writeConfig(t, "libraries: []\nwatch:\n  window_days: 0\n  rescan_hours: 0\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.WatchWindowDays(); got != 0 {
		t.Errorf("WatchWindowDays() = %d, want 0", got)
	}
	if got := cfg.WatchRescanHours(); got != 0 {
		t.Errorf("WatchRescanHours() = %d, want 0", got)
	}
}

func TestWatchOverride(t *testing.T) {
	path := writeConfig(t, "libraries: []\nwatch:\n  window_days: 30\n  rescan_hours: 6\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.WatchWindowDays(); got != 30 {
		t.Errorf("WatchWindowDays() = %d, want 30", got)
	}
	if got := cfg.WatchRescanHours(); got != 6 {
		t.Errorf("WatchRescanHours() = %d, want 6", got)
	}
}

func TestLoadInvalidType(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: Bad
    type: photos
    path: /media/photos
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for invalid library type")
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load("/nonexistent/gofin.yaml"); err == nil {
		t.Fatal("expected error for missing file")
	}
}
