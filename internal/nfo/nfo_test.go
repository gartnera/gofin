package nfo

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const movieNFO = `<?xml version="1.0" encoding="UTF-8"?>
<movie>
  <title>Inception</title>
  <plot>A thief who steals corporate secrets.</plot>
  <tagline>Your mind is the scene of the crime.</tagline>
  <year>2010</year>
  <premiered>2010-07-16</premiered>
  <mpaa>PG-13</mpaa>
  <genre>Action</genre>
  <genre>Science Fiction / Thriller</genre>
  <studio>Warner Bros.</studio>
  <rating>8.8</rating>
  <director>Christopher Nolan</director>
  <credits>Christopher Nolan</credits>
  <actor>
    <name>Leonardo DiCaprio</name>
    <role>Cobb</role>
  </actor>
</movie>`

func TestParseMovie(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "movie.nfo")
	write(t, p, movieNFO)

	info, err := Parse(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Title != "Inception" {
		t.Errorf("Title = %q", info.Title)
	}
	if info.Overview == "" || info.Tagline == "" {
		t.Errorf("Overview/Tagline empty: %+v", info)
	}
	if info.Year == nil || *info.Year != 2010 {
		t.Errorf("Year = %v", info.Year)
	}
	if info.PremiereDate == nil || info.PremiereDate.Year() != 2010 {
		t.Errorf("PremiereDate = %v", info.PremiereDate)
	}
	if info.OfficialRating != "PG-13" {
		t.Errorf("OfficialRating = %q", info.OfficialRating)
	}
	if info.CommunityRating == nil || *info.CommunityRating != 8.8 {
		t.Errorf("CommunityRating = %v", info.CommunityRating)
	}
	// "Science Fiction / Thriller" must split into two genres.
	want := []string{"Action", "Science Fiction", "Thriller"}
	if len(info.Genres) != len(want) {
		t.Fatalf("Genres = %v, want %v", info.Genres, want)
	}
	for i, g := range want {
		if info.Genres[i] != g {
			t.Errorf("Genres[%d] = %q, want %q", i, info.Genres[i], g)
		}
	}
	if len(info.Studios) != 1 || info.Studios[0] != "Warner Bros." {
		t.Errorf("Studios = %v", info.Studios)
	}
	// One actor, one director, one writer (from <credits>).
	var actors, directors, writers int
	for _, pn := range info.People {
		switch pn.Type {
		case "Actor":
			actors++
			if pn.Name != "Leonardo DiCaprio" || pn.Role != "Cobb" {
				t.Errorf("actor = %+v", pn)
			}
		case "Director":
			directors++
		case "Writer":
			writers++
		}
	}
	if actors != 1 || directors != 1 || writers != 1 {
		t.Errorf("people counts actors=%d directors=%d writers=%d (%+v)", actors, directors, writers, info.People)
	}
}

func TestParseNestedRatings(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.nfo")
	write(t, p, `<movie><title>X</title><ratings>
	  <rating name="imdb"><value>7.0</value></rating>
	  <rating name="tmdb" default="true"><value>9.1</value></rating>
	</ratings></movie>`)
	info, err := Parse(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.CommunityRating == nil || *info.CommunityRating != 9.1 {
		t.Errorf("CommunityRating = %v, want 9.1 (default entry)", info.CommunityRating)
	}
}

func TestParseEpisode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ep.nfo")
	write(t, p, `<episodedetails><title>Pilot</title><season>1</season><episode>1</episode><aired>2008-01-20</aired></episodedetails>`)
	info, err := Parse(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Title != "Pilot" {
		t.Errorf("Title = %q", info.Title)
	}
	if info.Season == nil || *info.Season != 1 || info.Episode == nil || *info.Episode != 1 {
		t.Errorf("season/episode = %v/%v", info.Season, info.Episode)
	}
	if info.PremiereDate == nil {
		t.Error("PremiereDate nil")
	}
}

// TestMovieLookup covers sidecar precedence and the generic "movie.nfo".
func TestMovieLookup(t *testing.T) {
	root := t.TempDir()
	movieDir := filepath.Join(root, "Inception (2010)")
	media := filepath.Join(movieDir, "Inception.mkv")
	write(t, media, "payload")

	// Only a generic movie.nfo present: used because the file is in its own dir.
	write(t, filepath.Join(movieDir, "movie.nfo"), `<movie><title>Generic</title><plot>g</plot></movie>`)
	if info := Movie(media, root); info == nil || info.Title != "Generic" {
		t.Fatalf("generic movie.nfo not used: %+v", info)
	}

	// Sidecar wins over the generic file for overlapping fields.
	write(t, filepath.Join(movieDir, "Inception.nfo"), `<movie><title>Sidecar</title></movie>`)
	if info := Movie(media, root); info == nil || info.Title != "Sidecar" {
		t.Fatalf("sidecar should win: %+v", info)
	}
}

// TestMovieRootGuard verifies a generic movie.nfo sitting directly in the
// library root is NOT attached to a bare top-level file (only its sidecar is).
func TestMovieRootGuard(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "Bare.mkv")
	write(t, media, "payload")
	write(t, filepath.Join(root, "movie.nfo"), `<movie><title>Stray</title></movie>`)

	if info := Movie(media, root); info != nil {
		t.Fatalf("root-level movie.nfo must be ignored for a bare file, got %+v", info)
	}

	// A sidecar in the root is still fine — it names the file exactly.
	write(t, filepath.Join(root, "Bare.nfo"), `<movie><title>Mine</title></movie>`)
	if info := Movie(media, root); info == nil || info.Title != "Mine" {
		t.Fatalf("sidecar in root should be read: %+v", info)
	}
}

// TestSeriesLookup verifies tvshow.nfo is found from a season folder but a
// tvshow.nfo loose in the library root is not applied to a flat file there.
func TestSeriesLookup(t *testing.T) {
	root := t.TempDir()
	showDir := filepath.Join(root, "Breaking Bad")
	ep := filepath.Join(showDir, "Season 01", "S01E01.mkv")
	write(t, ep, "payload")
	write(t, filepath.Join(showDir, "tvshow.nfo"), `<tvshow><title>BB</title><plot>p</plot></tvshow>`)
	write(t, filepath.Join(showDir, "Season 01", "season.nfo"), `<season><plot>s1</plot></season>`)

	if info := Series(ep, root); info == nil || info.Overview != "p" {
		t.Fatalf("tvshow.nfo from season folder not found: %+v", info)
	}
	if info := Season(ep, root); info == nil || info.Overview != "s1" {
		t.Fatalf("season.nfo not found: %+v", info)
	}

	// Guard: a flat episode directly in the root must not pick up a root tvshow.nfo.
	flat := filepath.Join(root, "Flat S01E01.mkv")
	write(t, flat, "payload")
	write(t, filepath.Join(root, "tvshow.nfo"), `<tvshow><title>Root</title></tvshow>`)
	if info := Series(flat, root); info != nil {
		t.Fatalf("root tvshow.nfo must not apply to a flat file: %+v", info)
	}
}
