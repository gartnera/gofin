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

func ptr(v int32) *int32 { return &v }
