package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/ent/mediaitem"
	"github.com/gartnera/gofin/internal/db"
)

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func countKind(t *testing.T, client *ent.Client, kind mediaitem.Kind) int {
	t.Helper()
	n, err := client.MediaItem.Query().Where(mediaitem.KindEQ(kind)).Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestScanLibraries(t *testing.T) {
	root := t.TempDir()
	// Arbitrary nesting on disk within each typed library.
	writeFile(t, filepath.Join(root, "movies", "Inception (2010).mp4"))
	writeFile(t, filepath.Join(root, "movies", "deep", "nest", "The Matrix (1999).mkv"))
	writeFile(t, filepath.Join(root, "tv", "Breaking Bad", "Season 01", "Breaking Bad - S01E01 - Pilot.mp4"))
	writeFile(t, filepath.Join(root, "tv", "Breaking Bad", "Season 01", "Breaking Bad - S01E02.mp4"))
	writeFile(t, filepath.Join(root, "music", "Artist", "Album", "01 Track.mp3"))

	ctx := context.Background()
	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	sc := New(client)
	libs := []struct{ name, typ, sub string }{
		{"Movies", "movies", "movies"},
		{"TV", "tvshows", "tv"},
		{"Music", "music", "music"},
	}
	scanAll := func() {
		for _, l := range libs {
			lib, err := sc.EnsureLibrary(ctx, l.name, l.typ, filepath.Join(root, l.sub))
			if err != nil {
				t.Fatalf("ensure %s: %v", l.name, err)
			}
			if err := sc.ScanLibrary(ctx, lib); err != nil {
				t.Fatalf("scan %s: %v", l.name, err)
			}
		}
	}

	scanAll()

	checks := map[mediaitem.Kind]int{
		mediaitem.KindMovie:       2,
		mediaitem.KindSeries:      1,
		mediaitem.KindSeason:      1,
		mediaitem.KindEpisode:     2,
		mediaitem.KindMusicArtist: 1,
		mediaitem.KindMusicAlbum:  1,
		mediaitem.KindAudio:       1,
	}
	for kind, want := range checks {
		if got := countKind(t, client, kind); got != want {
			t.Errorf("after scan: %s count = %d, want %d", kind, got, want)
		}
	}

	// Rescanning must be idempotent: counts unchanged, no duplicates.
	scanAll()
	for kind, want := range checks {
		if got := countKind(t, client, kind); got != want {
			t.Errorf("after rescan: %s count = %d, want %d", kind, got, want)
		}
	}

	// Libraries are reused, not duplicated.
	if n, _ := client.Library.Query().Count(ctx); n != 3 {
		t.Errorf("library count = %d, want 3", n)
	}
}
