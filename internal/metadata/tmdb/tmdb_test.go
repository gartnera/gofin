package tmdb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func mustDecode(t *testing.T, body string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
}

func TestMapMovieDetail(t *testing.T) {
	c := &Client{imageBase: "https://img/"}
	var d rawDetail
	mustDecode(t, `{
		"id": 27205,
		"title": "Inception",
		"overview": "A thief who steals corporate secrets.",
		"tagline": "Your mind is the scene of the crime.",
		"poster_path": "/poster.jpg",
		"release_date": "2010-07-16",
		"vote_average": 8.4,
		"genres": [{"name": "Action"}, {"name": "Science Fiction"}],
		"production_companies": [{"name": "Legendary Pictures"}],
		"credits": {
			"cast": [{"name": "Leonardo DiCaprio", "character": "Cobb"}],
			"crew": [{"name": "Christopher Nolan", "job": "Director"}, {"name": "Christopher Nolan", "job": "Writer"}]
		},
		"release_dates": {"results": [{"iso_3166_1": "US", "release_dates": [{"certification": "PG-13"}]}]}
	}`, &d)

	res := c.mapDetail(&d, false)
	if res.Title != "Inception" || res.ProviderIDs["Tmdb"] != "27205" {
		t.Errorf("title/id wrong: %q %v", res.Title, res.ProviderIDs)
	}
	if res.PosterURL != "https://img/w500/poster.jpg" {
		t.Errorf("poster url = %q", res.PosterURL)
	}
	if res.OfficialRating != "PG-13" {
		t.Errorf("official rating = %q, want PG-13", res.OfficialRating)
	}
	if res.Year == nil || *res.Year != 2010 || res.PremiereDate == nil {
		t.Errorf("year/premiere not parsed: %v %v", res.Year, res.PremiereDate)
	}
	if res.CommunityRating == nil || *res.CommunityRating < 8.3 {
		t.Errorf("community rating = %v", res.CommunityRating)
	}
	if len(res.Genres) != 2 || len(res.Studios) != 1 {
		t.Errorf("genres/studios = %v / %v", res.Genres, res.Studios)
	}
	// One actor, one director, one writer.
	var actors, directors, writers int
	for _, p := range res.People {
		switch p.Type {
		case "Actor":
			actors++
		case "Director":
			directors++
		case "Writer":
			writers++
		}
	}
	if actors != 1 || directors != 1 || writers != 1 {
		t.Errorf("people breakdown actors=%d directors=%d writers=%d", actors, directors, writers)
	}
}

func TestMapTVDetail(t *testing.T) {
	c := &Client{imageBase: "https://img/"}
	var d rawDetail
	mustDecode(t, `{
		"id": 1396,
		"name": "Breaking Bad",
		"overview": "A chemistry teacher turned meth cook.",
		"first_air_date": "2008-01-20",
		"content_ratings": {"results": [{"iso_3166_1": "US", "rating": "TV-MA"}]}
	}`, &d)

	res := c.mapDetail(&d, true)
	if res.Title != "Breaking Bad" || res.ProviderIDs["Tmdb"] != "1396" {
		t.Errorf("title/id wrong: %q %v", res.Title, res.ProviderIDs)
	}
	if res.OfficialRating != "TV-MA" {
		t.Errorf("official rating = %q, want TV-MA", res.OfficialRating)
	}
	if res.Year == nil || *res.Year != 2008 {
		t.Errorf("year not parsed from first_air_date: %v", res.Year)
	}
}

// TestSearchFlow drives MovieSearch end-to-end against a stub server, verifying
// the search → detail request sequence and auth header.
func TestSearchFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing bearer token: %q", r.Header.Get("Authorization"))
		}
		switch {
		case strings.Contains(r.URL.Path, "/search/movie"):
			w.Write([]byte(`{"results":[{"id":603}]}`))
		case strings.HasSuffix(r.URL.Path, "/movie/603"):
			w.Write([]byte(`{"id":603,"title":"The Matrix","overview":"o"}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	// Redirect the TMDb API base to the stub via a custom transport.
	c := New("tok", WithHTTPClient(&http.Client{Transport: rewriteHost{srv.URL}}))

	res, err := c.MovieSearch(context.Background(), "The Matrix", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "The Matrix" {
		t.Errorf("title = %q", res.Title)
	}
}

// rewriteHost redirects api.themoviedb.org (and /configuration) to the stub.
type rewriteHost struct{ base string }

func (rh rewriteHost) RoundTrip(r *http.Request) (*http.Response, error) {
	u := rh.base + r.URL.Path
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}
	req, _ := http.NewRequestWithContext(r.Context(), r.Method, u, r.Body)
	req.Header = r.Header
	return http.DefaultClient.Do(req)
}
