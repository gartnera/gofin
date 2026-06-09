package scanner

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/ent/mediaitem"
	"github.com/gartnera/gofin/internal/db"
	"github.com/gartnera/gofin/internal/probe"
)

// onlyKind returns the single item of the given kind, failing if there isn't
// exactly one.
func onlyKind(t *testing.T, client *ent.Client, kind mediaitem.Kind) *ent.MediaItem {
	t.Helper()
	it, err := client.MediaItem.Query().Where(mediaitem.KindEQ(kind)).Only(context.Background())
	if err != nil {
		t.Fatalf("query single %s: %v", kind, err)
	}
	return it
}

func TestScanPopulatesImagePaths(t *testing.T) {
	root := t.TempDir()

	// Movie in its own folder with a generic poster.
	moviePoster := filepath.Join(root, "movies", "Inception (2010)", "poster.jpg")
	writeFile(t, filepath.Join(root, "movies", "Inception (2010)", "Inception (2010).mp4"))
	writeFile(t, moviePoster)

	// TV: series poster, season folder image, episode thumb.
	seriesPoster := filepath.Join(root, "tv", "Breaking Bad", "poster.jpg")
	seasonFolder := filepath.Join(root, "tv", "Breaking Bad", "Season 01", "folder.jpg")
	epThumb := filepath.Join(root, "tv", "Breaking Bad", "Season 01", "Breaking Bad - S01E01-thumb.jpg")
	writeFile(t, filepath.Join(root, "tv", "Breaking Bad", "Season 01", "Breaking Bad - S01E01.mp4"))
	writeFile(t, seriesPoster)
	writeFile(t, seasonFolder)
	writeFile(t, epThumb)

	// Music: artist image and album cover.
	artistImg := filepath.Join(root, "music", "Artist", "folder.jpg")
	albumCover := filepath.Join(root, "music", "Artist", "Album", "cover.jpg")
	writeFile(t, filepath.Join(root, "music", "Artist", "Album", "01 Track.mp3"))
	writeFile(t, artistImg)
	writeFile(t, albumCover)

	ctx := context.Background()
	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	sc := New(client, WithProber(probe.Noop{}))
	for _, l := range []struct{ name, typ, sub string }{
		{"Movies", "movies", "movies"},
		{"TV", "tvshows", "tv"},
		{"Music", "music", "music"},
	} {
		lib, err := sc.EnsureLibrary(ctx, l.name, l.typ, filepath.Join(root, l.sub))
		if err != nil {
			t.Fatalf("ensure %s: %v", l.name, err)
		}
		if err := sc.ScanLibrary(ctx, lib); err != nil {
			t.Fatalf("scan %s: %v", l.name, err)
		}
	}

	cases := []struct {
		kind mediaitem.Kind
		want string
	}{
		{mediaitem.KindMovie, moviePoster},
		{mediaitem.KindSeries, seriesPoster},
		{mediaitem.KindSeason, seasonFolder},
		{mediaitem.KindEpisode, epThumb},
		{mediaitem.KindMusicArtist, artistImg},
		{mediaitem.KindMusicAlbum, albumCover},
	}
	for _, c := range cases {
		if got := onlyKind(t, client, c.kind).ImagePath; got != c.want {
			t.Errorf("%s image_path = %q, want %q", c.kind, got, c.want)
		}
	}
}

// TestRescanRefreshesMovieImage proves a poster added after the first scan is
// picked up on the next (the file's mtime changes are not required — image
// discovery runs whenever the item is (re-)indexed), and that a removed poster
// is cleared.
func TestRescanRefreshesMovieImage(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "movies", "Inception (2010)", "Inception (2010).mp4")
	writeFile(t, media)

	ctx := context.Background()
	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	sc := New(client, WithProber(probe.Noop{}))
	lib, err := sc.EnsureLibrary(ctx, "Movies", "movies", filepath.Join(root, "movies"))
	if err != nil {
		t.Fatal(err)
	}

	// First index, no poster yet.
	if err := sc.Index(ctx, lib, media); err != nil {
		t.Fatal(err)
	}
	if got := onlyKind(t, client, mediaitem.KindMovie).ImagePath; got != "" {
		t.Fatalf("expected no image initially, got %q", got)
	}

	// Add a poster and force a re-index of the file.
	poster := filepath.Join(root, "movies", "Inception (2010)", "poster.jpg")
	writeFile(t, poster)
	if err := sc.RefreshItem(ctx, onlyKind(t, client, mediaitem.KindMovie)); err != nil {
		t.Fatal(err)
	}
	if got := onlyKind(t, client, mediaitem.KindMovie).ImagePath; got != poster {
		t.Errorf("after adding poster: image_path = %q, want %q", got, poster)
	}
}
