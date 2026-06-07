package scanner

import (
	"context"
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

// TestProbeDedupesSharedTargets verifies that files which resolve to the same
// content via symlinks — the shape `gofin sample --real` produces — are probed
// once, not once per link. ffprobe is the dominant scan cost, so this turns a
// huge symlinked library into a handful of probes.
func TestProbeDedupesSharedTargets(t *testing.T) {
	root := t.TempDir()
	movies := filepath.Join(root, "movies")
	if err := os.MkdirAll(movies, 0o755); err != nil {
		t.Fatal(err)
	}
	// Two distinct real files; three symlinks each pointing at the first.
	baseA := filepath.Join(root, "baseA.mp4")
	baseB := filepath.Join(root, "baseB.mp4")
	writeFileContent(t, baseA, "aaa")
	writeFileContent(t, baseB, "bbb")
	for _, name := range []string{"M1 (2001).mp4", "M2 (2002).mp4", "M3 (2003).mp4"} {
		if err := os.Symlink(baseA, filepath.Join(movies, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(baseB, filepath.Join(movies, "M4 (2004).mp4")); err != nil {
		t.Fatal(err)
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
	// 4 movies indexed, but only 2 unique targets probed.
	if got := countKind(t, client, mediaitem.KindMovie); got != 4 {
		t.Errorf("movies indexed = %d, want 4", got)
	}
	if prober.count() != 2 {
		t.Errorf("probe count = %d, want 2 (one per unique target)", prober.count())
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
