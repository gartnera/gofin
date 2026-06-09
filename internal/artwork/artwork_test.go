package artwork

import (
	"os"
	"path/filepath"
	"testing"
)

// touch creates an empty file (and any parent directories).
func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMovieSidecarWinsOverFolder(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Inception (2010)")
	media := filepath.Join(dir, "Inception (2010).mkv")
	touch(t, media)
	folder := filepath.Join(dir, "folder.jpg")
	sidecar := filepath.Join(dir, "Inception (2010)-poster.png")
	touch(t, folder)
	touch(t, sidecar)

	if got := Movie(media, root); got != sidecar {
		t.Errorf("Movie() = %q, want sidecar %q", got, sidecar)
	}
}

func TestMovieFolderFallbackPreference(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Inception (2010)")
	media := filepath.Join(dir, "Inception (2010).mkv")
	touch(t, media)
	// poster is preferred over folder.
	folder := filepath.Join(dir, "folder.jpg")
	poster := filepath.Join(dir, "poster.jpg")
	touch(t, folder)
	touch(t, poster)

	if got := Movie(media, root); got != poster {
		t.Errorf("Movie() = %q, want %q", got, poster)
	}
}

func TestMovieGenericIgnoredAtLibraryRoot(t *testing.T) {
	root := t.TempDir()
	// A bare movie file directly in the library root must not pick up a stray
	// poster.jpg sitting in that root.
	media := filepath.Join(root, "Inception (2010).mkv")
	touch(t, media)
	touch(t, filepath.Join(root, "poster.jpg"))

	if got := Movie(media, root); got != "" {
		t.Errorf("Movie() = %q, want empty (generic image at root must be ignored)", got)
	}
}

func TestMovieSidecarAtLibraryRoot(t *testing.T) {
	root := t.TempDir()
	// A per-file sidecar is still honoured even at the library root.
	media := filepath.Join(root, "Inception (2010).mkv")
	sidecar := filepath.Join(root, "Inception (2010).jpg")
	touch(t, media)
	touch(t, sidecar)

	if got := Movie(media, root); got != sidecar {
		t.Errorf("Movie() = %q, want %q", got, sidecar)
	}
}

func TestEpisodeThumb(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Breaking Bad", "Season 01")
	media := filepath.Join(dir, "Breaking Bad - S01E01.mkv")
	thumb := filepath.Join(dir, "Breaking Bad - S01E01-thumb.jpg")
	touch(t, media)
	touch(t, thumb)

	if got := Episode(media); got != thumb {
		t.Errorf("Episode() = %q, want %q", got, thumb)
	}
}

func TestSeriesPosterFoundUpward(t *testing.T) {
	root := t.TempDir()
	showDir := filepath.Join(root, "Breaking Bad")
	media := filepath.Join(showDir, "Season 01", "Breaking Bad - S01E01.mkv")
	poster := filepath.Join(showDir, "poster.jpg")
	touch(t, media)
	touch(t, poster)

	if got := Series(media, root); got != poster {
		t.Errorf("Series() = %q, want %q", got, poster)
	}
}

func TestSeasonNamedPosterInSeriesDir(t *testing.T) {
	root := t.TempDir()
	showDir := filepath.Join(root, "Breaking Bad")
	media := filepath.Join(showDir, "Season 01", "Breaking Bad - S01E01.mkv")
	// Jellyfin's seasonNN-poster lives in the show directory.
	seasonPoster := filepath.Join(showDir, "season01-poster.jpg")
	touch(t, media)
	touch(t, seasonPoster)

	if got := Season(media, root, 1); got != seasonPoster {
		t.Errorf("Season() = %q, want %q", got, seasonPoster)
	}
}

func TestSeasonFolderImageInSeasonDir(t *testing.T) {
	root := t.TempDir()
	seasonDir := filepath.Join(root, "Breaking Bad", "Season 01")
	media := filepath.Join(seasonDir, "Breaking Bad - S01E01.mkv")
	folder := filepath.Join(seasonDir, "folder.jpg")
	touch(t, media)
	touch(t, folder)

	if got := Season(media, root, 1); got != folder {
		t.Errorf("Season() = %q, want %q", got, folder)
	}
}

func TestAlbumAndArtist(t *testing.T) {
	root := t.TempDir()
	artistDir := filepath.Join(root, "Artist")
	albumDir := filepath.Join(artistDir, "Album")
	media := filepath.Join(albumDir, "01 Track.mp3")
	cover := filepath.Join(albumDir, "cover.jpg")
	artistImg := filepath.Join(artistDir, "folder.png")
	touch(t, media)
	touch(t, cover)
	touch(t, artistImg)

	if got := Album(media, root); got != cover {
		t.Errorf("Album() = %q, want %q", got, cover)
	}
	if got := Artist(media, root); got != artistImg {
		t.Errorf("Artist() = %q, want %q", got, artistImg)
	}
}

func TestNoImageReturnsEmpty(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "Movie", "Movie.mkv")
	touch(t, media)
	if got := Movie(media, root); got != "" {
		t.Errorf("Movie() = %q, want empty", got)
	}
	if got := Episode(media); got != "" {
		t.Errorf("Episode() = %q, want empty", got)
	}
}
