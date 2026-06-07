package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gartnera/gofin/ent/mediaitem"
	"github.com/gartnera/gofin/internal/db"
	"github.com/gartnera/gofin/internal/probe"
)

// writeText writes arbitrary content (e.g. an .nfo body) to path.
func writeText(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestScanMovieNFO verifies a sidecar NFO populates a movie's metadata fields.
func TestScanMovieNFO(t *testing.T) {
	root := t.TempDir()
	movies := filepath.Join(root, "movies")
	// Movie in its own folder, with a sidecar NFO.
	dir := filepath.Join(movies, "Inception (2010)")
	writeFile(t, filepath.Join(dir, "Inception.mkv"))
	writeText(t, filepath.Join(dir, "Inception.nfo"), `<movie>
	  <title>Inception</title>
	  <plot>A thief.</plot>
	  <tagline>Your mind is the scene of the crime.</tagline>
	  <year>2010</year>
	  <mpaa>PG-13</mpaa>
	  <rating>8.8</rating>
	  <genre>Action</genre>
	  <genre>Sci-Fi</genre>
	  <studio>Warner Bros.</studio>
	  <actor><name>Leonardo DiCaprio</name><role>Cobb</role></actor>
	  <director>Christopher Nolan</director>
	</movie>`)

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

	m, err := client.MediaItem.Query().Where(mediaitem.KindEQ(mediaitem.KindMovie)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "Inception" {
		t.Errorf("Name = %q", m.Name)
	}
	if m.Overview != "A thief." {
		t.Errorf("Overview = %q", m.Overview)
	}
	if m.Tagline == "" {
		t.Error("Tagline empty")
	}
	if m.OfficialRating != "PG-13" {
		t.Errorf("OfficialRating = %q", m.OfficialRating)
	}
	if m.CommunityRating == nil || *m.CommunityRating != 8.8 {
		t.Errorf("CommunityRating = %v", m.CommunityRating)
	}
	if len(m.Genres) != 2 {
		t.Errorf("Genres = %v", m.Genres)
	}
	if len(m.Studios) != 1 {
		t.Errorf("Studios = %v", m.Studios)
	}
	if len(m.People) != 2 {
		t.Errorf("People = %+v", m.People)
	}
}

// TestScanSeriesNFO verifies tvshow.nfo enriches the Series folder while the
// episode sidecar sets the episode title.
func TestScanSeriesNFO(t *testing.T) {
	root := t.TempDir()
	tv := filepath.Join(root, "tv")
	show := filepath.Join(tv, "Breaking Bad")
	writeFile(t, filepath.Join(show, "Season 01", "Breaking Bad - S01E01.mkv"))
	writeText(t, filepath.Join(show, "tvshow.nfo"),
		`<tvshow><title>Breaking Bad</title><plot>A teacher cooks.</plot><genre>Drama</genre></tvshow>`)
	writeText(t, filepath.Join(show, "Season 01", "Breaking Bad - S01E01.nfo"),
		`<episodedetails><title>Pilot</title><plot>It begins.</plot></episodedetails>`)

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

	series, err := client.MediaItem.Query().Where(mediaitem.KindEQ(mediaitem.KindSeries)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if series.Overview != "A teacher cooks." {
		t.Errorf("series Overview = %q", series.Overview)
	}
	if len(series.Genres) != 1 || series.Genres[0] != "Drama" {
		t.Errorf("series Genres = %v", series.Genres)
	}

	ep, err := client.MediaItem.Query().Where(mediaitem.KindEQ(mediaitem.KindEpisode)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ep.Name != "Pilot" {
		t.Errorf("episode Name = %q (sidecar title should win)", ep.Name)
	}
	if ep.Overview != "It begins." {
		t.Errorf("episode Overview = %q", ep.Overview)
	}
}
