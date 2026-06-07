package watch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/ent/mediaitem"
	"github.com/gartnera/gofin/internal/db"
	"github.com/gartnera/gofin/internal/probe"
	"github.com/gartnera/gofin/internal/scanner"
)

// eventually polls fn until it returns true or the deadline elapses.
func eventually(t *testing.T, d time.Duration, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fn()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func countMovies(t *testing.T, client *ent.Client) int {
	t.Helper()
	n, err := client.MediaItem.Query().Where(mediaitem.KindEQ(mediaitem.KindMovie)).Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestWatcherIndexesAndRemoves(t *testing.T) {
	root := t.TempDir()
	movies := filepath.Join(root, "movies")
	if err := os.MkdirAll(movies, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	sc := scanner.New(client, scanner.WithProber(probe.Noop{}))
	lib, err := sc.EnsureLibrary(ctx, "Movies", "movies", movies)
	if err != nil {
		t.Fatal(err)
	}

	w, err := New(sc, []*ent.Library{lib}, WithDebounce(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = w.Run(ctx) }()
	// Give the event loop a moment to start.
	time.Sleep(50 * time.Millisecond)

	// Creating a file should index it.
	movie := filepath.Join(movies, "New Movie (2024).mp4")
	writeFile(t, movie, "payload")
	if !eventually(t, 3*time.Second, func() bool { return countMovies(t, client) == 1 }) {
		t.Fatalf("file not indexed by watcher: movie count = %d", countMovies(t, client))
	}

	// Removing the file should drop it from the index.
	if err := os.Remove(movie); err != nil {
		t.Fatal(err)
	}
	if !eventually(t, 3*time.Second, func() bool { return countMovies(t, client) == 0 }) {
		t.Fatalf("file not removed by watcher: movie count = %d", countMovies(t, client))
	}
}

func TestWatcherIndexesNewSubdirectory(t *testing.T) {
	root := t.TempDir()
	movies := filepath.Join(root, "movies")
	if err := os.MkdirAll(movies, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	sc := scanner.New(client, scanner.WithProber(probe.Noop{}))
	lib, err := sc.EnsureLibrary(ctx, "Movies", "movies", movies)
	if err != nil {
		t.Fatal(err)
	}

	w, err := New(sc, []*ent.Library{lib}, WithDebounce(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = w.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	// A whole new directory with a file inside should be picked up: the watcher
	// adds a watch for the new dir and indexes its existing contents.
	sub := filepath.Join(movies, "Collection")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sub, "Deep (2023).mkv"), "payload")
	if !eventually(t, 3*time.Second, func() bool { return countMovies(t, client) == 1 }) {
		t.Fatalf("file in new subdir not indexed: movie count = %d", countMovies(t, client))
	}
}
