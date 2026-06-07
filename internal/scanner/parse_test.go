package scanner

import "testing"

func TestParseMovie(t *testing.T) {
	tests := []struct {
		path      string
		wantTitle string
		wantYear  *int32
	}{
		{"/m/Inception (2010).mp4", "Inception", ptr(2010)},
		{"/m/The.Matrix.1999.mkv", "The Matrix 1999", nil},
		{"/m/sub/Blade Runner (1982).mkv", "Blade Runner", ptr(1982)},
		{"/m/Some_Movie.mp4", "Some Movie", nil},
	}
	for _, tt := range tests {
		got := ParseMovie(tt.path)
		if got.Title != tt.wantTitle {
			t.Errorf("%s: title = %q, want %q", tt.path, got.Title, tt.wantTitle)
		}
		if (got.Year == nil) != (tt.wantYear == nil) {
			t.Errorf("%s: year presence mismatch: %v vs %v", tt.path, got.Year, tt.wantYear)
		} else if got.Year != nil && *got.Year != *tt.wantYear {
			t.Errorf("%s: year = %d, want %d", tt.path, *got.Year, *tt.wantYear)
		}
	}
}

func TestParseEpisode(t *testing.T) {
	tests := []struct {
		path        string
		wantOK      bool
		wantSeries  string
		wantSeason  int32
		wantEpisode int32
		wantTitle   string
	}{
		{"/tv/Show Name - S01E02 - Pilot.mp4", true, "Show Name", 1, 2, "Pilot"},
		{"/tv/Another.Show.S02E03.mkv", true, "Another Show", 2, 3, ""},
		{"/tv/Breaking Bad/Season 01/Breaking Bad - S01E05.mkv", true, "Breaking Bad", 1, 5, ""},
		{"/tv/Some Show/Season 3/3x07 The One.mp4", true, "Some Show", 3, 7, "The One"},
		{"/tv/NotAnEpisode.mp4", false, "", 0, 0, ""},
		// Flat show: episodes live directly in the series folder.
		{"/tv/Firefly/Firefly S01E01.mkv", true, "Firefly", 1, 1, ""},
		{"/tv/The Office/The Office - 1x01 - Pilot.mkv", true, "The Office", 1, 1, "Pilot"},
		// Series name recovered from a parent directory when absent in the file.
		{"/tv/Extras/Breaking Bad/S02E01 - Title.mkv", true, "Breaking Bad", 2, 1, "Title"},
		// Year is stripped so the show matches regardless of folder layout.
		{"/tv/Breaking Bad (2008)/Season 01/Breaking Bad - S01E01.mkv", true, "Breaking Bad", 1, 1, ""},
		// Anime: absolute numbering, bracketed group, no season -> season 1.
		{"/anime/[HorribleSubs] Cowboy Bebop [05][1080p].mkv", true, "Cowboy Bebop", 1, 5, ""},
		{"/anime/Naruto - 101.mkv", true, "Naruto", 1, 101, ""},
		// Loose numeric naming: episode resolves but no marker for a title.
		{"/tv/The Show/01 - Pilot.avi", true, "The Show", 1, 1, ""},
		// Date-based episodes are not indexable yet.
		{"/tv/The Daily Show/2009.12.31.mkv", false, "", 0, 0, ""},
		// A resolution in parentheses must not be read as season 1920 episode 1080.
		{"/tv/Show/Show Special (1920x1080).mkv", false, "", 0, 0, ""},
	}
	for _, tt := range tests {
		got := ParseEpisode(tt.path)
		if got.OK != tt.wantOK {
			t.Errorf("%s: OK = %v, want %v", tt.path, got.OK, tt.wantOK)
			continue
		}
		if !tt.wantOK {
			continue
		}
		if got.Series != tt.wantSeries {
			t.Errorf("%s: series = %q, want %q", tt.path, got.Series, tt.wantSeries)
		}
		if got.Season != tt.wantSeason {
			t.Errorf("%s: season = %d, want %d", tt.path, got.Season, tt.wantSeason)
		}
		if got.Episode != tt.wantEpisode {
			t.Errorf("%s: episode = %d, want %d", tt.path, got.Episode, tt.wantEpisode)
		}
		if got.Title != tt.wantTitle {
			t.Errorf("%s: title = %q, want %q", tt.path, got.Title, tt.wantTitle)
		}
	}
}

func TestParseEpisodeMultiEpisode(t *testing.T) {
	tests := []struct {
		path      string
		wantEp    int32
		wantEnd   *int32
		wantTitle string
	}{
		// Multi-episode ranges set an ending episode number.
		{"/tv/Show/Season 1/Show - S01E01-E02.mkv", 1, ptr(2), ""},
		{"/tv/Show/Show 1x01x02.mkv", 1, ptr(2), ""},
		// A trailing resolution must not be mistaken for a range: E14 not E14-108.
		{"/tv/series-s09e14-1080p.mkv", 14, nil, ""},
	}
	for _, tt := range tests {
		got := ParseEpisode(tt.path)
		if !got.OK {
			t.Errorf("%s: OK = false, want true", tt.path)
			continue
		}
		if got.Episode != tt.wantEp {
			t.Errorf("%s: episode = %d, want %d", tt.path, got.Episode, tt.wantEp)
		}
		switch {
		case (got.EndEpisode == nil) != (tt.wantEnd == nil):
			t.Errorf("%s: end-episode presence mismatch: %v vs %v", tt.path, got.EndEpisode, tt.wantEnd)
		case got.EndEpisode != nil && *got.EndEpisode != *tt.wantEnd:
			t.Errorf("%s: end-episode = %d, want %d", tt.path, *got.EndEpisode, *tt.wantEnd)
		}
		if got.Title != tt.wantTitle {
			t.Errorf("%s: title = %q, want %q", tt.path, got.Title, tt.wantTitle)
		}
	}
}

func ptr(v int32) *int32 { return &v }
