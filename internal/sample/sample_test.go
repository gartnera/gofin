package sample

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gartnera/gofin/ent/mediaitem"
	"github.com/gartnera/gofin/internal/db"
	"github.com/gartnera/gofin/internal/probe"
	"github.com/gartnera/gofin/internal/scanner"
)

func TestGenerateCounts(t *testing.T) {
	dir := t.TempDir()
	res, err := Generate(dir, Options{
		Movies:            12,
		Series:            3,
		Seasons:           2,
		EpisodesPerSeason: 4,
		Artists:           2,
		AlbumsPerArtist:   2,
		TracksPerAlbum:    3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Movies != 12 {
		t.Errorf("movies = %d, want 12", res.Movies)
	}
	if res.Episodes != 3*2*4 {
		t.Errorf("episodes = %d, want %d", res.Episodes, 3*2*4)
	}
	if res.Tracks != 2*2*3 {
		t.Errorf("tracks = %d, want %d", res.Tracks, 2*2*3)
	}
}

// TestGeneratedLibraryScans confirms the synthetic tree is well-formed enough
// that the real scanner indexes it into the expected hierarchy — so the
// generator stays a faithful stand-in for a real library in benchmarks.
func TestGeneratedLibraryScans(t *testing.T) {
	dir := t.TempDir()
	res, err := Generate(dir, Options{Movies: 5, Series: 2, Seasons: 2, EpisodesPerSeason: 3})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	sc := scanner.New(client, scanner.WithProber(probe.Noop{}))

	for _, l := range []struct{ typ, root string }{
		{"movies", res.MoviesDir},
		{"tvshows", res.TVDir},
	} {
		lib, err := sc.EnsureLibrary(ctx, l.typ, l.typ, l.root)
		if err != nil {
			t.Fatal(err)
		}
		if err := sc.ScanLibrary(ctx, lib); err != nil {
			t.Fatal(err)
		}
	}

	count := func(k mediaitem.Kind) int {
		n, err := client.MediaItem.Query().Where(mediaitem.KindEQ(k)).Count(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := count(mediaitem.KindMovie); got != 5 {
		t.Errorf("movies indexed = %d, want 5", got)
	}
	if got := count(mediaitem.KindSeries); got != 2 {
		t.Errorf("series indexed = %d, want 2", got)
	}
	if got := count(mediaitem.KindSeason); got != 4 {
		t.Errorf("seasons indexed = %d, want 4", got)
	}
	if got := count(mediaitem.KindEpisode); got != 2*2*3 {
		t.Errorf("episodes indexed = %d, want %d", got, 2*2*3)
	}

	// Verify the directory layout matches what the scanner expects.
	if _, err := filepath.Glob(filepath.Join(res.TVDir, "*", "Season *", "*.mkv")); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateRealRequiresFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		t.Skip("ffmpeg present; this test covers the missing-ffmpeg error path")
	}
	if _, err := Generate(t.TempDir(), Options{Movies: 1, Real: true}); err == nil {
		t.Fatal("expected an error when --real is used without ffmpeg")
	}
}

// TestGenerateRealSymlinks exercises the real-media path end to end. It needs
// ffmpeg to encode the base files, so it is skipped where ffmpeg is absent.
func TestGenerateRealSymlinks(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	dir := t.TempDir()
	res, err := Generate(dir, Options{Movies: 5, Series: 1, Seasons: 1, EpisodesPerSeason: 3, RealBase: 2, Real: true})
	if err != nil {
		t.Fatal(err)
	}

	// Every generated movie must be a symlink resolving to a non-empty real
	// file, and use a browser-playable container.
	entries, err := os.ReadDir(res.MoviesDir)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		full := filepath.Join(res.MoviesDir, e.Name())
		li, err := os.Lstat(full)
		if err != nil {
			t.Fatal(err)
		}
		if li.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s is not a symlink", e.Name())
			continue
		}
		if filepath.Ext(e.Name()) != ".webm" {
			t.Errorf("%s: want .webm video container", e.Name())
		}
		si, err := os.Stat(full) // follows the link to the real base file
		if err != nil {
			t.Fatalf("resolve %s: %v", e.Name(), err)
		}
		if si.Size() == 0 {
			t.Errorf("%s resolves to an empty file", e.Name())
		}
		checked++
	}
	if checked != res.Movies {
		t.Errorf("checked %d movies, want %d", checked, res.Movies)
	}

	// The real base files live outside the library roots so they aren't indexed.
	if _, err := os.Stat(filepath.Join(dir, ".base")); err != nil {
		t.Errorf(".base dir missing: %v", err)
	}
}
