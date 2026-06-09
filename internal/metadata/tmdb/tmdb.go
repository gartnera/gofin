// Package tmdb implements metadata.Provider against The Movie Database (TMDb)
// using only the standard library. Construct a client with a v4 read-access
// token (https://developer.themoviedb.org/docs); without one, callers should use
// metadata.Noop instead. All requests funnel through a conservative rate gate
// and a single retry on HTTP 429 so a large cold scan stays within TMDb's
// limits.
package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gartnera/gofin/internal/metadata"
	"github.com/gartnera/gofin/internal/nfo"
)

const (
	apiBase = "https://api.themoviedb.org/3"
	// fallbackImageBase is used until /configuration is fetched (or if it fails).
	fallbackImageBase = "https://image.tmdb.org/t/p/"
	posterSize        = "w500"
)

// Client is a TMDb-backed metadata.Provider.
type Client struct {
	token string
	http  *http.Client
	limit <-chan time.Time

	cfgOnce   sync.Once
	imageBase string // secure base URL ending in "/", e.g. https://image.tmdb.org/t/p/
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient overrides the HTTP client (tests point it at a stub server).
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// New returns a TMDb Client authenticated with a v4 read-access token.
func New(token string, opts ...Option) *Client {
	c := &Client{
		token: token,
		http:  &http.Client{Timeout: 15 * time.Second},
		// ~20 req/s is comfortably under TMDb's documented ceiling.
		limit:     time.Tick(50 * time.Millisecond),
		imageBase: fallbackImageBase,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Name implements metadata.Provider.
func (c *Client) Name() string { return "Tmdb" }

// rawMovie/rawTV mirror the subset of TMDb detail responses gofin maps.
type rawDetail struct {
	ID             int               `json:"id"`
	Title          string            `json:"title"` // movies
	Name           string            `json:"name"`  // tv
	Overview       string            `json:"overview"`
	Tagline        string            `json:"tagline"`
	PosterPath     string            `json:"poster_path"`
	ReleaseDate    string            `json:"release_date"`   // movies
	FirstAirDate   string            `json:"first_air_date"` // tv
	VoteAverage    float32           `json:"vote_average"`
	Genres         []idName          `json:"genres"`
	ProductionCos  []idName          `json:"production_companies"`
	Credits        rawCredit         `json:"credits"`
	ReleaseDates   rawRegionDates    `json:"release_dates"`   // movies
	ContentRatings rawContentRatings `json:"content_ratings"` // tv
}

type idName struct {
	Name string `json:"name"`
}

type rawCredit struct {
	Cast []struct {
		Name      string `json:"name"`
		Character string `json:"character"`
	} `json:"cast"`
	Crew []struct {
		Name string `json:"name"`
		Job  string `json:"job"`
	} `json:"crew"`
}

type rawRegionDates struct {
	Results []struct {
		Region       string `json:"iso_3166_1"`
		ReleaseDates []struct {
			Certification string `json:"certification"`
		} `json:"release_dates"`
	} `json:"results"`
}

type rawContentRatings struct {
	Results []struct {
		Region string `json:"iso_3166_1"`
		Rating string `json:"rating"`
	} `json:"results"`
}

type searchResult struct {
	Results []struct {
		ID int `json:"id"`
	} `json:"results"`
}

// MovieSearch implements metadata.Provider.
func (c *Client) MovieSearch(ctx context.Context, title string, year *int32) (metadata.Result, error) {
	q := url.Values{"query": {title}}
	if year != nil {
		q.Set("year", strconv.Itoa(int(*year)))
	}
	var sr searchResult
	if err := c.get(ctx, "/search/movie", q, &sr); err != nil {
		return metadata.Result{}, err
	}
	if len(sr.Results) == 0 {
		return metadata.Result{}, metadata.ErrNotFound
	}
	var d rawDetail
	dq := url.Values{"append_to_response": {"credits,release_dates"}}
	if err := c.get(ctx, fmt.Sprintf("/movie/%d", sr.Results[0].ID), dq, &d); err != nil {
		return metadata.Result{}, err
	}
	return c.mapDetail(&d, false), nil
}

// SeriesSearch implements metadata.Provider.
func (c *Client) SeriesSearch(ctx context.Context, name string, year *int32) (metadata.Result, error) {
	q := url.Values{"query": {name}}
	if year != nil {
		q.Set("first_air_date_year", strconv.Itoa(int(*year)))
	}
	var sr searchResult
	if err := c.get(ctx, "/search/tv", q, &sr); err != nil {
		return metadata.Result{}, err
	}
	if len(sr.Results) == 0 {
		return metadata.Result{}, metadata.ErrNotFound
	}
	var d rawDetail
	dq := url.Values{"append_to_response": {"credits,content_ratings"}}
	if err := c.get(ctx, fmt.Sprintf("/tv/%d", sr.Results[0].ID), dq, &d); err != nil {
		return metadata.Result{}, err
	}
	return c.mapDetail(&d, true), nil
}

// mapDetail converts a TMDb detail response into a normalized metadata.Result.
// It is pure (no I/O beyond the already-resolved imageBase) so it can be unit
// tested against captured JSON fixtures.
func (c *Client) mapDetail(d *rawDetail, isTV bool) metadata.Result {
	res := metadata.Result{
		ProviderIDs: metadata.ProviderIDs{"Tmdb": strconv.Itoa(d.ID)},
		Overview:    strings.TrimSpace(d.Overview),
		Tagline:     strings.TrimSpace(d.Tagline),
	}
	if isTV {
		res.Title = strings.TrimSpace(d.Name)
	} else {
		res.Title = strings.TrimSpace(d.Title)
	}
	for _, g := range d.Genres {
		if n := strings.TrimSpace(g.Name); n != "" {
			res.Genres = append(res.Genres, n)
		}
	}
	for _, s := range d.ProductionCos {
		if n := strings.TrimSpace(s.Name); n != "" {
			res.Studios = append(res.Studios, n)
		}
	}
	for _, a := range d.Credits.Cast {
		if n := strings.TrimSpace(a.Name); n != "" {
			res.People = append(res.People, nfo.Person{Name: n, Role: strings.TrimSpace(a.Character), Type: "Actor"})
		}
	}
	for _, cr := range d.Credits.Crew {
		switch cr.Job {
		case "Director":
			res.People = append(res.People, nfo.Person{Name: strings.TrimSpace(cr.Name), Type: "Director"})
		case "Writer", "Screenplay":
			res.People = append(res.People, nfo.Person{Name: strings.TrimSpace(cr.Name), Type: "Writer"})
		}
	}
	if d.VoteAverage > 0 {
		v := d.VoteAverage
		res.CommunityRating = &v
	}
	if t := parseDate(d.ReleaseDate, d.FirstAirDate); t != nil {
		res.PremiereDate = t
		y := int32(t.Year())
		res.Year = &y
	}
	res.OfficialRating = d.certification()
	if d.PosterPath != "" {
		res.PosterURL = c.imageBase + posterSize + d.PosterPath
	}
	return res
}

func (d *rawDetail) certification() string {
	for _, r := range d.ReleaseDates.Results { // movies
		if r.Region == "US" {
			for _, rd := range r.ReleaseDates {
				if c := strings.TrimSpace(rd.Certification); c != "" {
					return c
				}
			}
		}
	}
	for _, r := range d.ContentRatings.Results { // tv
		if r.Region == "US" {
			if c := strings.TrimSpace(r.Rating); c != "" {
				return c
			}
		}
	}
	return ""
}

var dateLayouts = []string{"2006-01-02", time.RFC3339}

func parseDate(vals ...string) *time.Time {
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		for _, layout := range dateLayouts {
			if t, err := time.Parse(layout, v); err == nil {
				return &t
			}
		}
	}
	return nil
}

// loadConfig fetches /configuration once to learn the secure image base URL.
func (c *Client) loadConfig(ctx context.Context) {
	c.cfgOnce.Do(func() {
		var cfg struct {
			Images struct {
				SecureBaseURL string `json:"secure_base_url"`
			} `json:"images"`
		}
		if err := c.get(ctx, "/configuration", nil, &cfg); err == nil && cfg.Images.SecureBaseURL != "" {
			c.imageBase = cfg.Images.SecureBaseURL
		}
	})
}

// get performs a rate-limited GET against the TMDb API, decoding the JSON body
// into out. A 429 is retried once after honoring Retry-After.
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	if path != "/configuration" {
		c.loadConfig(ctx)
	}
	u := apiBase + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	for attempt := 0; attempt < 2; attempt++ {
		<-c.limit
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt == 0 {
			wait := 1 * time.Second
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if secs, err := strconv.Atoi(ra); err == nil {
					wait = time.Duration(secs) * time.Second
				}
			}
			resp.Body.Close()
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("tmdb %s: status %d", path, resp.StatusCode)
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return fmt.Errorf("tmdb %s: rate limited", path)
}
