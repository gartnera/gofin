package scanner

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/gartnera/gofin/ent/mediaitem"
	"github.com/gartnera/gofin/internal/db"
	"github.com/gartnera/gofin/internal/probe"
)

// writeIgnore writes a .ignore file with the given contents into dir.
func writeIgnore(t *testing.T, dir, contents string) {
	t.Helper()
	writeFileContent(t, filepath.Join(dir, ".ignore"), contents)
}

func TestScanHonoursIgnoreFiles(t *testing.T) {
	root := t.TempDir()
	movies := filepath.Join(root, "movies")

	// Indexed.
	writeFile(t, filepath.Join(movies, "Good Movie (2020).mp4"))
	writeFile(t, filepath.Join(movies, "Keep (2021).mkv"))
	// Excluded by a pattern .ignore at the library root.
	writeFile(t, filepath.Join(movies, "Trailer.avi"))
	writeFile(t, filepath.Join(movies, "specials", "Extra (2018).mp4"))
	writeIgnore(t, movies, "*.avi\nspecials/\n")
	// Excluded by an empty .ignore in its own directory.
	writeFile(t, filepath.Join(movies, "blooper", "Hidden (2017).mp4"))
	writeIgnore(t, filepath.Join(movies, "blooper"), "")

	ctx := context.Background()
	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	sc := New(client, WithProber(probe.Noop{}))
	lib, err := sc.EnsureLibrary(ctx, "Movies", "movies", movies)
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.ScanLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}

	if got := countKind(t, client, mediaitem.KindMovie); got != 2 {
		t.Fatalf("movie count = %d, want 2 (Good Movie + Keep)", got)
	}
	names, err := client.MediaItem.Query().Where(mediaitem.KindEQ(mediaitem.KindMovie)).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range names {
		if m.Name != "Good Movie" && m.Name != "Keep" {
			t.Errorf("unexpected indexed movie %q", m.Name)
		}
	}

	// The single-file Index path (used by the watcher) must also respect
	// .ignore rules.
	if err := sc.Index(ctx, lib, filepath.Join(movies, "specials", "Extra (2018).mp4")); err != nil {
		t.Fatal(err)
	}
	if err := sc.Index(ctx, lib, filepath.Join(movies, "blooper", "Hidden (2017).mp4")); err != nil {
		t.Fatal(err)
	}
	if got := countKind(t, client, mediaitem.KindMovie); got != 2 {
		t.Fatalf("after ignored Index calls: movie count = %d, want 2", got)
	}
}
