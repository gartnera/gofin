package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/ent/mediaitem"
	"github.com/gartnera/gofin/ent/metadatacache"
	"github.com/gartnera/gofin/internal/metadata"
	"github.com/google/uuid"
)

const (
	// defaultMetaTTL is how long a found remote response stays fresh.
	defaultMetaTTL = 14 * 24 * time.Hour
	// negativeTTL is the (shorter) freshness window for a cached miss, so a
	// title that gains a match later is eventually retried.
	negativeTTL = 24 * time.Hour
	// sweepInterval re-scans the DB for not-yet-enriched items, catching
	// anything whose channel enqueue was dropped (full queue) or added offline.
	sweepInterval = 15 * time.Minute
)

// imageHTTP downloads posters; separate from the provider's client so a slow
// image host can't stall API calls.
var imageHTTP = &http.Client{Timeout: 30 * time.Second}

// StartEnricher launches the background metadata enricher. It is a no-op unless
// a real provider was configured via WithMetadataProvider. The goroutine runs
// until ctx is cancelled. Call it once (e.g. from serve) after the initial scan.
func (s *Scanner) StartEnricher(ctx context.Context) {
	if !s.metaEnabled {
		return
	}
	go s.enrichLoop(ctx)
}

func (s *Scanner) enrichLoop(ctx context.Context) {
	s.sweep(ctx)
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-s.enrichQ:
			s.enrichItem(ctx, id)
			s.dequeued(id)
		case <-t.C:
			s.sweep(ctx)
		}
	}
}

// sweep enqueues every Movie/Series not yet enriched. This is the durable
// backstop for the channel: it picks up items added while the provider was
// disabled, after a restart, or whose non-blocking enqueue was dropped.
func (s *Scanner) sweep(ctx context.Context) {
	ids, err := s.client.MediaItem.Query().
		Where(
			mediaitem.MetadataSyncedAtIsNil(),
			mediaitem.KindIn(mediaitem.KindMovie, mediaitem.KindSeries),
		).
		IDs(ctx)
	if err != nil {
		return
	}
	for _, id := range ids {
		s.enqueueID(id)
	}
}

// maybeEnrich enqueues an item for enrichment when remote metadata is enabled
// and the item is an un-enriched Movie or Series. Called by the index funcs
// right after an item is created/updated.
func (s *Scanner) maybeEnrich(it *ent.MediaItem) {
	if !s.metaEnabled || it == nil || it.MetadataSyncedAt != nil {
		return
	}
	switch it.Kind {
	case mediaitem.KindMovie, mediaitem.KindSeries:
	default:
		return
	}
	s.enqueueID(it.ID)
	// Suppress re-enqueueing the same cached folder row: a Series struct is
	// shared across all its episodes during a scan, so without this every
	// episode would re-enqueue it. The worker still reloads from the DB (so the
	// real state is authoritative) and the sweep is the backstop if the
	// non-blocking enqueue was dropped.
	now := time.Now()
	it.MetadataSyncedAt = &now
}

// enqueueID sends an id to the enricher at most once until it is processed,
// never blocking the caller (a scan). A full queue is fine — the id is forgotten
// so the next sweep re-enqueues it.
func (s *Scanner) enqueueID(id uuid.UUID) {
	s.enqMu.Lock()
	if _, ok := s.enqueued[id]; ok {
		s.enqMu.Unlock()
		return
	}
	s.enqueued[id] = struct{}{}
	s.enqMu.Unlock()

	select {
	case s.enrichQ <- id:
	default:
		s.enqMu.Lock()
		delete(s.enqueued, id)
		s.enqMu.Unlock()
	}
}

func (s *Scanner) dequeued(id uuid.UUID) {
	s.enqMu.Lock()
	delete(s.enqueued, id)
	s.enqMu.Unlock()
}

// enrichItem resolves and applies remote metadata for one item. Network happens
// outside s.mu; only the final write takes the lock. Errors are swallowed: a
// transport failure leaves metadata_synced_at nil so the next sweep retries,
// while a definitive not-found is marked synced so it is not retried in vain.
func (s *Scanner) enrichItem(ctx context.Context, id uuid.UUID) {
	it, err := s.client.MediaItem.Get(ctx, id)
	if err != nil || it.MetadataSyncedAt != nil {
		return
	}
	// A locked item keeps all its metadata; mark it synced so it isn't re-swept.
	if it.LockData {
		s.markSynced(ctx, id)
		return
	}

	var res metadata.Result
	var lookErr error
	switch it.Kind {
	case mediaitem.KindMovie:
		res, lookErr = s.lookup(ctx, "movie-search", movieKey(it), func() (metadata.Result, error) {
			return s.meta.MovieSearch(ctx, it.Name, it.ProductionYear)
		})
	case mediaitem.KindSeries:
		res, lookErr = s.lookup(ctx, "series-search", seriesKey(it), func() (metadata.Result, error) {
			return s.meta.SeriesSearch(ctx, it.Name, it.ProductionYear)
		})
	default:
		s.markSynced(ctx, id)
		return
	}
	if lookErr != nil {
		if errors.Is(lookErr, metadata.ErrNotFound) {
			s.markSynced(ctx, id)
		}
		return
	}
	s.applyRemote(ctx, id, res)
}

// lookup resolves a remote result for (kind, key), consulting the persistent
// MetadataCache first so a title is fetched at most once. A cached miss returns
// ErrNotFound without a network call; a stale or absent entry triggers fetch and
// (re)caches the outcome.
func (s *Scanner) lookup(ctx context.Context, kind, key string, fetch func() (metadata.Result, error)) (metadata.Result, error) {
	provider := s.meta.Name()
	row, err := s.client.MetadataCache.Query().
		Where(metadatacache.Provider(provider), metadatacache.Kind(kind), metadatacache.Key(key)).
		Only(ctx)
	switch {
	case err == nil:
		if row.NotFound {
			if fresh(row.FetchedAt, negativeTTL) {
				return metadata.Result{}, metadata.ErrNotFound
			}
		} else if fresh(row.FetchedAt, s.metaTTL) {
			var res metadata.Result
			if json.Unmarshal(row.Payload, &res) == nil {
				return res, nil
			}
		}
	case !ent.IsNotFound(err):
		return metadata.Result{}, err
	}

	res, ferr := fetch()
	if errors.Is(ferr, metadata.ErrNotFound) {
		s.storeCache(ctx, provider, kind, key, nil, true)
		return metadata.Result{}, ferr
	}
	if ferr != nil {
		// Transport error: don't cache, so the next sweep retries.
		return metadata.Result{}, ferr
	}
	payload, _ := json.Marshal(res)
	s.storeCache(ctx, provider, kind, key, payload, false)
	return res, nil
}

// storeCache upserts a cache row keyed by (provider, kind, key). Best-effort:
// caching is an optimization, so failures are ignored.
func (s *Scanner) storeCache(ctx context.Context, provider, kind, key string, payload []byte, notFound bool) {
	existing, err := s.client.MetadataCache.Query().
		Where(metadatacache.Provider(provider), metadatacache.Kind(kind), metadatacache.Key(key)).
		Only(ctx)
	if err == nil {
		_ = existing.Update().
			SetPayload(payload).
			SetNotFound(notFound).
			SetFetchedAt(time.Now()).
			Exec(ctx)
		return
	}
	_, _ = s.client.MetadataCache.Create().
		SetProvider(provider).
		SetKind(kind).
		SetKey(key).
		SetPayload(payload).
		SetNotFound(notFound).
		Save(ctx)
}

// applyRemote overlays a remote result onto the item, filling only fields that
// are currently empty and not user-locked — the mirror of applyNFO, so a local
// NFO (applied at scan time) and user edits always win and remote merely fills
// gaps. provider_ids is always recorded (it's identity, not editable metadata),
// and metadata_synced_at is stamped so the item is considered done.
func (s *Scanner) applyRemote(ctx context.Context, id uuid.UUID, res metadata.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, err := s.client.MediaItem.Get(ctx, id)
	if err != nil {
		return
	}
	// Re-check under the lock: a concurrent edit may have locked or already
	// synced the item between the network call and now.
	if it.LockData || it.MetadataSyncedAt != nil {
		return
	}

	upd := it.Update()
	if it.Overview == "" && res.Overview != "" && !metaLocked(it, "Overview") {
		upd.SetOverview(res.Overview)
	}
	if it.Tagline == "" && res.Tagline != "" {
		upd.SetTagline(res.Tagline)
	}
	if it.OfficialRating == "" && res.OfficialRating != "" && !metaLocked(it, "OfficialRating") {
		upd.SetOfficialRating(res.OfficialRating)
	}
	if len(it.Genres) == 0 && len(res.Genres) > 0 && !metaLocked(it, "Genres") {
		upd.SetGenres(res.Genres)
	}
	if len(it.Studios) == 0 && len(res.Studios) > 0 && !metaLocked(it, "Studios") {
		upd.SetStudios(res.Studios)
	}
	if len(it.People) == 0 && len(res.People) > 0 && !metaLocked(it, "Cast") {
		upd.SetPeople(res.People)
	}
	if it.ProductionYear == nil && res.Year != nil && !metaLocked(it, "ProductionYear") {
		upd.SetProductionYear(*res.Year)
	}
	if it.CommunityRating == nil && res.CommunityRating != nil {
		upd.SetCommunityRating(*res.CommunityRating)
	}
	if it.PremiereDate == nil && res.PremiereDate != nil {
		upd.SetPremiereDate(*res.PremiereDate)
	}
	if len(res.ProviderIDs) > 0 {
		upd.SetProviderIds(mergeIDs(it.ProviderIds, res.ProviderIDs))
	}
	// Only fall back to the remote poster when no local artwork was found.
	if it.ImagePath == "" {
		if p := s.cacheImage(ctx, res); p != "" {
			upd.SetImagePath(p)
		}
	}
	upd.SetMetadataSyncedAt(time.Now())
	_ = upd.Exec(ctx)
}

func (s *Scanner) markSynced(ctx context.Context, id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.client.MediaItem.UpdateOneID(id).SetMetadataSyncedAt(time.Now()).Exec(ctx)
}

// cacheImage downloads a poster to the image cache dir and returns its local
// path, skipping the download if the file already exists. Best-effort: any
// failure returns "" and the item simply keeps no image.
func (s *Scanner) cacheImage(ctx context.Context, res metadata.Result) string {
	if s.imageCache == "" || res.PosterURL == "" {
		return ""
	}
	id := res.ProviderIDs[s.meta.Name()]
	if id == "" {
		return ""
	}
	ext := path.Ext(res.PosterURL)
	if ext == "" {
		ext = ".jpg"
	}
	dest := filepath.Join(s.imageCache, fmt.Sprintf("%s-%s-poster%s", strings.ToLower(s.meta.Name()), id, ext))
	if _, err := os.Stat(dest); err == nil {
		return dest
	}
	if err := os.MkdirAll(s.imageCache, 0o755); err != nil {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, res.PosterURL, nil)
	if err != nil {
		return ""
	}
	resp, err := imageHTTP.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return ""
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return ""
	}
	f.Close()
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return ""
	}
	return dest
}

// fresh reports whether a cache row fetched at t is still within ttl.
func fresh(t time.Time, ttl time.Duration) bool {
	return time.Since(t) < ttl
}

// mergeIDs overlays add onto base, returning a new map (base may be nil).
func mergeIDs(base, add metadata.ProviderIDs) metadata.ProviderIDs {
	out := metadata.ProviderIDs{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range add {
		out[k] = v
	}
	return out
}

// movieKey / seriesKey build the MetadataCache key for an item. Using the
// normalized name (and year, for movies) means two items with the same title
// share one cached lookup — the "search our own library first" dedup.
func movieKey(it *ent.MediaItem) string {
	key := strings.ToLower(strings.TrimSpace(it.Name))
	if it.ProductionYear != nil {
		key += fmt.Sprintf("|%d", *it.ProductionYear)
	}
	return key
}

func seriesKey(it *ent.MediaItem) string {
	return strings.ToLower(strings.TrimSpace(it.Name))
}
