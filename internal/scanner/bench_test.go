package scanner

import (
	"context"
	"fmt"
	"testing"

	"github.com/gartnera/gofin/internal/db"
	"github.com/gartnera/gofin/internal/probe"
	"github.com/gartnera/gofin/internal/sample"
)

// benchScan covers a cold (first) scan of a freshly generated library tree: a
// fresh database every iteration so each measures indexing from empty.
func benchScan(b *testing.B, typ string, opts sample.Options) {
	dir := b.TempDir()
	res, err := sample.Generate(dir, opts)
	if err != nil {
		b.Fatal(err)
	}
	var root string
	switch typ {
	case "movies":
		root = res.MoviesDir
	case "tvshows":
		root = res.TVDir
	case "music":
		root = res.MusicDir
	}
	ctx := context.Background()

	b.ReportMetric(float64(res.Movies+res.Episodes+res.Tracks), "items")
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		b.StopTimer()
		client, err := db.OpenMemory(ctx, fmt.Sprintf("%s-%d", b.Name(), i))
		if err != nil {
			b.Fatal(err)
		}
		sc := New(client, WithProber(probe.Noop{}))
		lib, err := sc.EnsureLibrary(ctx, typ, typ, root)
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()

		if err := sc.ScanLibrary(ctx, lib); err != nil {
			b.Fatal(err)
		}

		b.StopTimer()
		client.Close()
		b.StartTimer()
	}
}

// benchRescan covers the steady-state rescan: the library is already fully
// indexed and nothing has changed on disk, so the cost is dominated by the
// existence checks the scanner performs for every file (the common case for a
// `serve` startup scan or a /Library/Refresh on an unchanged library).
func benchRescan(b *testing.B, typ string, opts sample.Options) {
	dir := b.TempDir()
	res, err := sample.Generate(dir, opts)
	if err != nil {
		b.Fatal(err)
	}
	var root string
	switch typ {
	case "movies":
		root = res.MoviesDir
	case "tvshows":
		root = res.TVDir
	case "music":
		root = res.MusicDir
	}
	ctx := context.Background()
	client, err := db.OpenMemory(ctx, b.Name())
	if err != nil {
		b.Fatal(err)
	}
	defer client.Close()
	sc := New(client, WithProber(probe.Noop{}))
	lib, err := sc.EnsureLibrary(ctx, typ, typ, root)
	if err != nil {
		b.Fatal(err)
	}
	if err := sc.ScanLibrary(ctx, lib); err != nil {
		b.Fatal(err)
	}

	b.ReportMetric(float64(res.Movies+res.Episodes+res.Tracks), "items")
	b.ResetTimer()
	for b.Loop() {
		if err := sc.ScanLibrary(ctx, lib); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkScanMovies(b *testing.B)   { benchScan(b, "movies", sample.Options{Movies: 10000}) }
func BenchmarkRescanMovies(b *testing.B) { benchRescan(b, "movies", sample.Options{Movies: 10000}) }

// 500 series x 2 seasons x 10 episodes = 10000 episodes.
var tvOpts = sample.Options{Series: 500, Seasons: 2, EpisodesPerSeason: 10}

func BenchmarkScanEpisodes(b *testing.B)   { benchScan(b, "tvshows", tvOpts) }
func BenchmarkRescanEpisodes(b *testing.B) { benchRescan(b, "tvshows", tvOpts) }
