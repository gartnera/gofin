// Package sample generates large synthetic media libraries on disk for
// benchmarking and load-testing the scanner and HTTP query handlers.
//
// Unlike scripts/gen-sample-library.sh — which uses ffmpeg to produce a handful
// of real, direct-playable files for the e2e suite — this package writes empty
// placeholder files with realistic names and directory layouts as fast as the
// filesystem allows, so generating tens of thousands of items is cheap. The
// files are not playable; their purpose is to exercise indexing and querying at
// scale.
package sample

import (
	"fmt"
	"os"
	"path/filepath"
)

// Options controls the size and shape of a generated library tree. A zero count
// disables that media type.
type Options struct {
	// Movies is the number of movie files to create under <Dir>/movies.
	Movies int
	// Series and EpisodesPerSeason shape the TV tree under <Dir>/tv: each
	// series gets Seasons seasons, each with EpisodesPerSeason episodes.
	Series            int
	Seasons           int
	EpisodesPerSeason int
	// Artists/AlbumsPerArtist/TracksPerAlbum shape the music tree under
	// <Dir>/music. Left zero, no music library is generated.
	Artists         int
	AlbumsPerArtist int
	TracksPerAlbum  int
}

// Result reports what Generate wrote, per library subdirectory.
type Result struct {
	MoviesDir string
	TVDir     string
	MusicDir  string
	Movies    int
	Episodes  int
	Tracks    int
}

// Generate writes the requested library tree under dir, creating the standard
// movies/tv/music subdirectories as needed. It returns the per-type paths and
// counts so callers can register libraries against the right roots.
func Generate(dir string, opts Options) (Result, error) {
	res := Result{
		MoviesDir: filepath.Join(dir, "movies"),
		TVDir:     filepath.Join(dir, "tv"),
		MusicDir:  filepath.Join(dir, "music"),
	}

	if opts.Movies > 0 {
		n, err := generateMovies(res.MoviesDir, opts.Movies)
		if err != nil {
			return res, err
		}
		res.Movies = n
	}
	if opts.Series > 0 {
		n, err := generateEpisodes(res.TVDir, opts)
		if err != nil {
			return res, err
		}
		res.Episodes = n
	}
	if opts.Artists > 0 {
		n, err := generateMusic(res.MusicDir, opts)
		if err != nil {
			return res, err
		}
		res.Tracks = n
	}
	return res, nil
}

func generateMovies(dir string, count int) (int, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	for i := 0; i < count; i++ {
		// Year cycles through a plausible range so ProductionYear-sorted
		// queries see a realistic spread.
		year := 1970 + (i % 55)
		name := fmt.Sprintf("%s (%d).mkv", movieTitle(i), year)
		if err := touch(filepath.Join(dir, name)); err != nil {
			return i, err
		}
	}
	return count, nil
}

func generateEpisodes(dir string, opts Options) (int, error) {
	seasons := opts.Seasons
	if seasons < 1 {
		seasons = 1
	}
	eps := opts.EpisodesPerSeason
	if eps < 1 {
		eps = 1
	}
	total := 0
	for sIdx := 0; sIdx < opts.Series; sIdx++ {
		series := seriesTitle(sIdx)
		for season := 1; season <= seasons; season++ {
			seasonDir := filepath.Join(dir, series, fmt.Sprintf("Season %02d", season))
			if err := os.MkdirAll(seasonDir, 0o755); err != nil {
				return total, err
			}
			for ep := 1; ep <= eps; ep++ {
				name := fmt.Sprintf("%s - S%02dE%02d.mkv", series, season, ep)
				if err := touch(filepath.Join(seasonDir, name)); err != nil {
					return total, err
				}
				total++
			}
		}
	}
	return total, nil
}

func generateMusic(dir string, opts Options) (int, error) {
	albums := opts.AlbumsPerArtist
	if albums < 1 {
		albums = 1
	}
	tracks := opts.TracksPerAlbum
	if tracks < 1 {
		tracks = 1
	}
	total := 0
	for aIdx := 0; aIdx < opts.Artists; aIdx++ {
		artist := artistName(aIdx)
		for al := 1; al <= albums; al++ {
			album := fmt.Sprintf("%s Vol %d", artist, al)
			albumDir := filepath.Join(dir, artist, album)
			if err := os.MkdirAll(albumDir, 0o755); err != nil {
				return total, err
			}
			for tr := 1; tr <= tracks; tr++ {
				// No embedded tags (empty file); the scanner falls back to the
				// Artist/Album/Track path layout, which is what we lay out here.
				name := fmt.Sprintf("%02d Track %d.mp3", tr, tr)
				if err := touch(filepath.Join(albumDir, name)); err != nil {
					return total, err
				}
				total++
			}
		}
	}
	return total, nil
}

// touch creates an empty file, creating parent directories as needed.
func touch(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// movieTitle returns a deterministic, mostly-unique title for the i-th movie.
func movieTitle(i int) string {
	return fmt.Sprintf("%s %s %d", adjectives[i%len(adjectives)], nouns[(i/len(adjectives))%len(nouns)], i)
}

func seriesTitle(i int) string {
	return fmt.Sprintf("%s %s Show %d", adjectives[i%len(adjectives)], nouns[(i/len(adjectives))%len(nouns)], i)
}

func artistName(i int) string {
	return fmt.Sprintf("The %s %s %d", adjectives[i%len(adjectives)], nouns[(i/len(adjectives))%len(nouns)], i)
}

// Small word banks keep generated names varied enough that NameContainsFold
// search and sort_name ordering see realistic data without a dictionary file.
var adjectives = []string{
	"Silent", "Crimson", "Hidden", "Broken", "Golden", "Frozen", "Electric",
	"Distant", "Savage", "Velvet", "Iron", "Lunar", "Solar", "Wild", "Quiet",
}

var nouns = []string{
	"River", "Empire", "Horizon", "Shadow", "Phoenix", "Garden", "Machine",
	"Voyage", "Legacy", "Mirage", "Harbor", "Summit", "Echo", "Compass",
}
