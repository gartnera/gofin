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

// newMoviesWatcher sets up an in-memory scanner over a fresh movies library and
// returns the library dir plus the running watcher's scanner/client.
func newMoviesWatcher(t *testing.T, ctx context.Context, opts ...Option) (string, *ent.Client, *Watcher) {
	t.Helper()
	root := t.TempDir()
	movies := filepath.Join(root, "movies")
	if err := os.MkdirAll(movies, 0o755); err != nil {
		t.Fatal(err)
	}
	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	sc := scanner.New(client, scanner.WithProber(probe.Noop{}))
	lib, err := sc.EnsureLibrary(ctx, "Movies", "movies", movies)
	if err != nil {
		t.Fatal(err)
	}
	w, err := New(sc, []*ent.Library{lib}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return movies, client, w
}

// makeStale backdates a directory's mtime so it falls outside the watch window.
func makeStale(t *testing.T, dir string) {
	t.Helper()
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
}

// TestWatcherWindowWatchesContainerChild verifies that a stale directory which
// nonetheless contains a subdirectory (a "container", e.g. a dormant show
// folder) is still watched, so a brand-new child directory dropped into it is
// picked up live even though the parent is older than the window.
func TestWatcherWindowWatchesContainerChild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	movies, client, w := newMoviesWatcher(t, ctx, WithDebounce(50*time.Millisecond), WithWatchWindow(24*time.Hour))

	// An old container: Old/Season exists at startup, both stale.
	oldDir := filepath.Join(movies, "Old")
	if err := os.MkdirAll(filepath.Join(oldDir, "Season"), 0o755); err != nil {
		t.Fatal(err)
	}
	makeStale(t, filepath.Join(oldDir, "Season"))
	makeStale(t, oldDir)

	// New() registered watches; re-walk now that the stale mtimes are in place.
	// The stale Old/ is a container (it holds Season/), so it is watched despite
	// its age; Season/ is a stale leaf and is not. Assert this before Run starts,
	// while the watched map is only touched by this goroutine.
	w.addTree(movies)
	if !w.watched[oldDir] {
		t.Fatalf("stale container %q not watched", oldDir)
	}
	if w.watched[filepath.Join(oldDir, "Season")] {
		t.Fatalf("stale leaf %q should not be watched", filepath.Join(oldDir, "Season"))
	}
	go func() { _ = w.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	// A new child directory under the stale container must be detected.
	newChild := filepath.Join(oldDir, "New (2024)")
	if err := os.MkdirAll(newChild, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(newChild, "New (2024).mkv"), "payload")
	if !eventually(t, 3*time.Second, func() bool { return countMovies(t, client) == 1 }) {
		t.Fatalf("new dir under stale container not indexed: movie count = %d", countMovies(t, client))
	}
}

// TestWatcherWindowSkipsStaleLeafHealedByRescan verifies that a stale leaf
// directory is left unwatched (a file dropped into it is not picked up live)
// but that the periodic rescan backstop eventually indexes it.
func TestWatcherWindowSkipsStaleLeafHealedByRescan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	movies, client, w := newMoviesWatcher(t, ctx,
		WithDebounce(50*time.Millisecond),
		WithWatchWindow(24*time.Hour),
		WithRescanInterval(400*time.Millisecond),
	)

	// A stale leaf directory (no subdirs) present at startup → left unwatched.
	stale := filepath.Join(movies, "Old Movie (2010)")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	makeStale(t, stale)

	// Re-walk with the stale mtime applied. The leaf has no subdirectories so
	// nothing forces it to be watched, and it is older than the window: assert it
	// was left out of the watch set (deterministic, unlike testing for the
	// absence of a live event, which the macOS kqueue backend would surface). On
	// Linux inotify this means a write into it produces no event at all.
	w.addTree(movies)
	if w.watched[stale] {
		t.Fatalf("stale leaf %q should not be watched", stale)
	}
	if !w.watched[movies] {
		t.Fatalf("library root %q must always be watched", movies)
	}
	go func() { _ = w.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	// Dropping a file into the unwatched leaf and the periodic rescan backstop
	// must pick it up.
	writeFile(t, filepath.Join(stale, "Old Movie (2010).mkv"), "payload")
	if !eventually(t, 3*time.Second, func() bool { return countMovies(t, client) == 1 }) {
		t.Fatalf("periodic rescan did not heal the stale leaf: movie count = %d", countMovies(t, client))
	}
}

func countItems(t *testing.T, client *ent.Client) int {
	t.Helper()
	n, err := client.MediaItem.Query().Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestWatcherRemovesFolderAndPrunesEmptyParents verifies that deleting a whole
// show folder removes its episodes and the now-childless Series/Season folder
// rows (which RemovePrefix alone leaves behind), without waiting for a rescan.
func TestWatcherRemovesFolderAndPrunesEmptyParents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	root := t.TempDir()
	tv := filepath.Join(root, "tv")
	season := filepath.Join(tv, "Breaking Bad", "Season 01")
	if err := os.MkdirAll(season, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(season, "Breaking Bad S01E01.mkv"), "payload")
	writeFile(t, filepath.Join(season, "Breaking Bad S01E02.mkv"), "payload")

	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	sc := scanner.New(client, scanner.WithProber(probe.Noop{}))
	lib, err := sc.EnsureLibrary(ctx, "TV", "tvshows", tv)
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.ScanLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	// Episodes plus the Series and Season folder rows.
	if n := countItems(t, client); n <= 2 {
		t.Fatalf("expected episodes + folder rows after scan, got %d", n)
	}

	w, err := New(sc, []*ent.Library{lib}, WithDebounce(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = w.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	// Deleting the whole show folder must clear everything for the library.
	if err := os.RemoveAll(filepath.Join(tv, "Breaking Bad")); err != nil {
		t.Fatal(err)
	}
	if !eventually(t, 3*time.Second, func() bool { return countItems(t, client) == 0 }) {
		t.Fatalf("folder rows not pruned after show deletion: item count = %d", countItems(t, client))
	}
}

// TestWatcherPrunesParentsOnFileByFileDeletion verifies that deleting the
// playable files individually (rather than removing the whole folder) still
// prunes the now-childless Season/Series rows, without waiting for a rescan.
func TestWatcherPrunesParentsOnFileByFileDeletion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	root := t.TempDir()
	tv := filepath.Join(root, "tv")
	season := filepath.Join(tv, "Breaking Bad", "Season 01")
	if err := os.MkdirAll(season, 0o755); err != nil {
		t.Fatal(err)
	}
	ep1 := filepath.Join(season, "Breaking Bad S01E01.mkv")
	ep2 := filepath.Join(season, "Breaking Bad S01E02.mkv")
	writeFile(t, ep1, "payload")
	writeFile(t, ep2, "payload")

	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	sc := scanner.New(client, scanner.WithProber(probe.Noop{}))
	lib, err := sc.EnsureLibrary(ctx, "TV", "tvshows", tv)
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.ScanLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}

	w, err := New(sc, []*ent.Library{lib}, WithDebounce(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = w.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	// Delete the episode files one at a time, leaving the (now empty) Season and
	// show directories on disk. The folder rows must still be pruned.
	if err := os.Remove(ep1); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ep2); err != nil {
		t.Fatal(err)
	}
	if !eventually(t, 3*time.Second, func() bool { return countItems(t, client) == 0 }) {
		t.Fatalf("empty Season/Series rows not pruned after file-by-file deletion: item count = %d", countItems(t, client))
	}
}

// TestWatcherUnwatchAllowsRewatch verifies the dedupe set is purged on removal,
// so a directory that is deleted and later recreated is watched again rather
// than skipped as already-added.
func TestWatcherUnwatchAllowsRewatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	movies, _, w := newMoviesWatcher(t, ctx)
	sub := filepath.Join(movies, "Foo")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	w.addTree(movies)
	if !w.watched[sub] {
		t.Fatalf("subdir %q not watched after addTree", sub)
	}

	// Simulate the removal event for the subtree.
	w.handleRemove(ctx, sub)
	if w.watched[sub] {
		t.Fatalf("subdir %q still in watched set after removal", sub)
	}

	// A recreated directory must be re-watchable.
	w.add(sub)
	if !w.watched[sub] {
		t.Fatalf("subdir %q not re-watched after recreation", sub)
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
