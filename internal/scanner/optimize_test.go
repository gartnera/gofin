package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gartnera/gofin/ent/mediaitem"
	"github.com/gartnera/gofin/internal/db"
	"github.com/gartnera/gofin/internal/probe"
)

// writeFileContent writes specific content to path, creating parent dirs.
func writeFileContent(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// countingProber records how many times Probe is invoked.
type countingProber struct {
	mu sync.Mutex
	n  int
}

func (c *countingProber) Probe(context.Context, string) (probe.Result, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return probe.Result{}, nil
}

func (c *countingProber) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func TestScanSkipsUnchangedFiles(t *testing.T) {
	root := t.TempDir()
	movies := filepath.Join(root, "movies")
	writeFileContent(t, filepath.Join(movies, "A (2001).mp4"), "aaa")
	writeFileContent(t, filepath.Join(movies, "B (2002).mp4"), "bbb")

	ctx := context.Background()
	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	prober := &countingProber{}
	sc := New(client, WithProber(prober))
	lib, err := sc.EnsureLibrary(ctx, "Movies", "movies", movies)
	if err != nil {
		t.Fatal(err)
	}

	if err := sc.ScanLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	if prober.count() != 2 {
		t.Fatalf("after first scan: probe count = %d, want 2", prober.count())
	}

	// Rescan with nothing changed: no files should be re-probed.
	if err := sc.ScanLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	if prober.count() != 2 {
		t.Fatalf("after unchanged rescan: probe count = %d, want 2 (no re-probe)", prober.count())
	}

	// Change one file's size; only it should be re-probed.
	writeFileContent(t, filepath.Join(movies, "A (2001).mp4"), "aaaaaaaa")
	if err := sc.ScanLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	if prober.count() != 3 {
		t.Fatalf("after modifying one file: probe count = %d, want 3", prober.count())
	}
}

// TestScanLargeFlatDirectory exercises loadDirPaths's batched IN(...) lookup: a
// flat directory with more files than the batch size must index correctly, and
// a rescan must still recognise every file as unchanged (skip re-probing) —
// which only works if the per-directory byPath cache is populated across all
// batches.
func TestScanLargeFlatDirectory(t *testing.T) {
	root := t.TempDir()
	movies := filepath.Join(root, "movies")
	const n = 1200 // > the 500-path batch in loadDirPaths
	for i := 0; i < n; i++ {
		writeFileContent(t, filepath.Join(movies, fmt.Sprintf("Movie %04d (2000).mp4", i)), fmt.Sprintf("payload-%d", i))
	}

	ctx := context.Background()
	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	prober := &countingProber{}
	sc := New(client, WithProber(prober))
	lib, err := sc.EnsureLibrary(ctx, "Movies", "movies", movies)
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.ScanLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	if got := countKind(t, client, mediaitem.KindMovie); got != n {
		t.Fatalf("indexed = %d, want %d", got, n)
	}
	if prober.count() != n {
		t.Fatalf("first scan probe count = %d, want %d", prober.count(), n)
	}
	// Rescan: nothing changed, so the batched byPath lookup must mark every file
	// unchanged and re-probe none.
	if err := sc.ScanLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	if prober.count() != n {
		t.Errorf("after unchanged rescan: probe count = %d, want %d (no re-probe)", prober.count(), n)
	}
}

func TestScanPrunesMissingFiles(t *testing.T) {
	root := t.TempDir()
	tv := filepath.Join(root, "tv")
	ep1 := filepath.Join(tv, "Show", "Season 01", "Show - S01E01.mp4")
	ep2 := filepath.Join(tv, "Show", "Season 01", "Show - S01E02.mp4")
	writeFile(t, ep1)
	writeFile(t, ep2)

	ctx := context.Background()
	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	sc := New(client, WithProber(probe.Noop{}))
	lib, err := sc.EnsureLibrary(ctx, "TV", "tvshows", tv)
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.ScanLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	if got := countKind(t, client, mediaitem.KindEpisode); got != 2 {
		t.Fatalf("episode count = %d, want 2", got)
	}

	// Remove one episode and rescan: the orphaned item is pruned, the series
	// and season remain because they still have a child.
	if err := os.Remove(ep2); err != nil {
		t.Fatal(err)
	}
	if err := sc.ScanLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	if got := countKind(t, client, mediaitem.KindEpisode); got != 1 {
		t.Fatalf("after prune: episode count = %d, want 1", got)
	}
	if got := countKind(t, client, mediaitem.KindSeason); got != 1 {
		t.Fatalf("after prune: season count = %d, want 1", got)
	}

	// Remove the last episode: the now-empty season and series are pruned too.
	if err := os.Remove(ep1); err != nil {
		t.Fatal(err)
	}
	if err := sc.ScanLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	for _, k := range []mediaitem.Kind{mediaitem.KindEpisode, mediaitem.KindSeason, mediaitem.KindSeries} {
		if got := countKind(t, client, k); got != 0 {
			t.Fatalf("after full prune: %s count = %d, want 0", k, got)
		}
	}
}
