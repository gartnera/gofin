// Package metadata fetches item metadata (overviews, genres, cast, ratings and
// posters) from a remote source behind a pluggable Provider interface, mirroring
// the internal/probe Prober pattern. A TMDb implementation lives in the tmdb
// subpackage; Noop is the default when remote metadata is not configured, so
// enrichment degrades to a no-op rather than a network call.
//
// The normalized Result type is a superset of nfo.Info so a remote result
// overlays onto the same MediaItem fields a local NFO would, letting the scanner
// treat remote metadata as a gap-filling fallback that never overrides local
// NFO or user-locked fields.
package metadata

import (
	"context"
	"errors"
	"time"

	"github.com/gartnera/gofin/internal/nfo"
)

// ProviderIDs maps a provider's name to its identifier for an item (e.g.
// {"Tmdb": "27205"}). It is persisted on a MediaItem so a remote lookup happens
// once and is reused: a series is searched a single time and every episode (and
// rescan) reuses its IDs instead of re-searching.
type ProviderIDs map[string]string

// Result is the normalized metadata a Provider returns for an item. Zero/empty
// fields mean the provider did not supply that value, in which case the scanner
// keeps whatever it already has.
type Result struct {
	ProviderIDs     ProviderIDs
	Title           string
	Overview        string
	Tagline         string
	Year            *int32
	PremiereDate    *time.Time
	CommunityRating *float32
	OfficialRating  string
	Genres          []string
	Studios         []string
	People          []nfo.Person
	// PosterURL is an absolute URL to a poster image, already resolved to a
	// concrete size by the provider; empty when none is available.
	PosterURL string
}

// ErrNotFound is returned by a Provider when no remote match exists for the
// query. Callers treat it as "leave the item's fields as they are" — distinct
// from a transport error — and it is cached negatively so the same miss is not
// re-searched on every rescan.
var ErrNotFound = errors.New("metadata: no match found")

// Provider fetches metadata from a remote source. Implementations must be safe
// for concurrent use. Each method performs both the search and the detail fetch
// so a result can be cached under a single key.
type Provider interface {
	// Name returns the provider's identifier, also used as the ProviderIDs key
	// (e.g. "Tmdb").
	Name() string
	// MovieSearch resolves a movie by title and optional year.
	MovieSearch(ctx context.Context, title string, year *int32) (Result, error)
	// SeriesSearch resolves a TV series by name and optional year.
	SeriesSearch(ctx context.Context, name string, year *int32) (Result, error)
}

// Noop is a Provider that finds nothing. It is the default when remote metadata
// is disabled, so the scanner's enrichment path becomes inert.
type Noop struct{}

func (Noop) Name() string { return "Noop" }

func (Noop) MovieSearch(context.Context, string, *int32) (Result, error) {
	return Result{}, ErrNotFound
}

func (Noop) SeriesSearch(context.Context, string, *int32) (Result, error) {
	return Result{}, ErrNotFound
}
