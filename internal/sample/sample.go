// Package sample generates large synthetic media libraries on disk for
// benchmarking and load-testing the scanner and HTTP query handlers.
//
// By default it writes empty placeholder files with realistic names and
// directory layouts as fast as the filesystem allows, so generating tens of
// thousands of items is cheap. Those files are not playable; they exercise
// indexing and querying at scale.
//
// With Options.Real it instead encodes a small pool of genuinely playable base
// files once (via ffmpeg) and symlinks every generated entry to one of them, so
// a huge library is browsable AND every item direct-plays in a browser — at the
// cost of a handful of encodes, not tens of thousands. (scripts/gen-sample-
// library.sh remains the way to produce a few standalone real files without
// this package.)
package sample

import (
	"fmt"
	"os"
	"os/exec"
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
	// Real generates genuinely playable media: a small pool of base files is
	// encoded once with ffmpeg and every entry is symlinked to one of them.
	// Requires ffmpeg on PATH.
	Real bool
	// RealBase is the number of distinct base files to encode per media type in
	// Real mode (entries round-robin across them). Defaults to 3.
	RealBase int
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

	p, err := newPlacer(dir, opts)
	if err != nil {
		return res, err
	}

	if opts.Movies > 0 {
		n, err := generateMovies(res.MoviesDir, opts.Movies, p)
		if err != nil {
			return res, err
		}
		res.Movies = n
	}
	if opts.Series > 0 {
		n, err := generateEpisodes(res.TVDir, opts, p)
		if err != nil {
			return res, err
		}
		res.Episodes = n
	}
	if opts.Artists > 0 {
		n, err := generateMusic(res.MusicDir, opts, p)
		if err != nil {
			return res, err
		}
		res.Tracks = n
	}
	return res, nil
}

func generateMovies(dir string, count int, p *placer) (int, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	for i := 0; i < count; i++ {
		// Year cycles through a plausible range so ProductionYear-sorted
		// queries see a realistic spread.
		year := 1970 + (i % 55)
		name := fmt.Sprintf("%s (%d)%s", movieTitle(i), year, p.videoExt)
		if err := p.placeVideo(filepath.Join(dir, name), i); err != nil {
			return i, err
		}
	}
	return count, nil
}

func generateEpisodes(dir string, opts Options, p *placer) (int, error) {
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
				name := fmt.Sprintf("%s - S%02dE%02d%s", series, season, ep, p.videoExt)
				if err := p.placeVideo(filepath.Join(seasonDir, name), total); err != nil {
					return total, err
				}
				total++
			}
		}
	}
	return total, nil
}

func generateMusic(dir string, opts Options, p *placer) (int, error) {
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
				// No embedded tags; the scanner falls back to the Artist/Album/
				// Track path layout, which is what we lay out here.
				name := fmt.Sprintf("%02d Track %d%s", tr, tr, p.audioExt)
				if err := p.placeAudio(filepath.Join(albumDir, name), total); err != nil {
					return total, err
				}
				total++
			}
		}
	}
	return total, nil
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

// placer decides how each generated entry is materialised: an empty touch
// (placeholder mode) or a symlink to a pre-encoded base file (Real mode).
type placer struct {
	videoExt string   // extension the generated filenames use for video
	audioExt string   // …and for audio
	videos   []string // absolute paths to base video files (Real mode only)
	audios   []string // …and base audio files
}

// newPlacer prepares the materialisation strategy. In Real mode it requires
// ffmpeg and encodes RealBase playable base files per media type once, under
// <dir>/.base (outside every library root, so they are never indexed).
func newPlacer(dir string, opts Options) (*placer, error) {
	if !opts.Real {
		// Browser-irrelevant containers are fine for placeholders: nothing reads
		// the (empty) bytes.
		return &placer{videoExt: ".mkv", audioExt: ".mp3"}, nil
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, fmt.Errorf("--real requires ffmpeg on PATH: %w", err)
	}
	n := opts.RealBase
	if n < 1 {
		n = 3
	}
	baseDir := filepath.Join(dir, ".base")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, err
	}
	// Browser-friendly codecs (VP9/Opus in .webm, MP3 audio) so items direct
	// play in Chromium without transcoding, matching gen-sample-library.sh.
	p := &placer{videoExt: ".webm", audioExt: ".mp3"}
	for i := 0; i < n; i++ {
		freq := 220 + i*110
		vid := filepath.Join(baseDir, fmt.Sprintf("base%d.webm", i))
		if err := encodeVideo(vid, freq); err != nil {
			return nil, err
		}
		abs, err := filepath.Abs(vid)
		if err != nil {
			return nil, err
		}
		p.videos = append(p.videos, abs)

		aud := filepath.Join(baseDir, fmt.Sprintf("base%d.mp3", i))
		if err := encodeAudio(aud, freq); err != nil {
			return nil, err
		}
		abs, err = filepath.Abs(aud)
		if err != nil {
			return nil, err
		}
		p.audios = append(p.audios, abs)
	}
	return p, nil
}

func (p *placer) placeVideo(dst string, seq int) error {
	if len(p.videos) == 0 {
		return touch(dst)
	}
	return symlink(p.videos[seq%len(p.videos)], dst)
}

func (p *placer) placeAudio(dst string, seq int) error {
	if len(p.audios) == 0 {
		return touch(dst)
	}
	return symlink(p.audios[seq%len(p.audios)], dst)
}

// touch creates an empty file.
func touch(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// symlink points dst at target, replacing any existing dst so regeneration is
// idempotent.
func symlink(target, dst string) error {
	_ = os.Remove(dst)
	return os.Symlink(target, dst)
}

// encodeVideo writes a short VP9/Opus .webm test clip (a test pattern plus a
// sine tone) — small and fast, but a real, seekable, direct-playable file.
func encodeVideo(path string, freq int) error {
	return runFFmpeg(
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=15",
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:sample_rate=48000", freq),
		"-t", "3",
		"-c:v", "libvpx-vp9", "-b:v", "200k", "-deadline", "realtime", "-cpu-used", "8", "-pix_fmt", "yuv420p",
		"-c:a", "libopus", "-b:a", "48k",
		path,
	)
}

// encodeAudio writes a short MP3 sine tone — a real, playable audio file.
func encodeAudio(path string, freq int) error {
	return runFFmpeg(
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:sample_rate=48000:duration=3", freq),
		"-c:a", "libmp3lame", "-b:a", "96k",
		path,
	)
}

func runFFmpeg(args ...string) error {
	full := append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)
	cmd := exec.Command("ffmpeg", full...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg %v: %w: %s", args, err, out)
	}
	return nil
}
