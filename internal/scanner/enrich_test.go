package scanner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/ent/mediaitem"
	"github.com/gartnera/gofin/internal/db"
	"github.com/gartnera/gofin/internal/metadata"
	"github.com/gartnera/gofin/internal/nfo"
	"github.com/gartnera/gofin/internal/probe"
)

// fakeProvider is a counting metadata.Provider for tests. Its name is "Tmdb" so
// the provider-id key, cache rows and poster filenames match a real run.
type fakeProvider struct {
	mu        sync.Mutex
	movie     int
	series    int
	poster    string          // PosterURL returned for movies
	notFound  map[string]bool // titles that resolve to ErrNotFound
	transport map[string]bool // titles that fail with a transport error
}

func (f *fakeProvider) Name() string { return "Tmdb" }

func (f *fakeProvider) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.movie, f.series
}

func (f *fakeProvider) MovieSearch(_ context.Context, title string, _ *int32) (metadata.Result, error) {
	f.mu.Lock()
	f.movie++
	f.mu.Unlock()
	if f.transport[title] {
		return metadata.Result{}, http.ErrServerClosed // any non-ErrNotFound error
	}
	if f.notFound[title] {
		return metadata.Result{}, metadata.ErrNotFound
	}
	return metadata.Result{
		ProviderIDs: metadata.ProviderIDs{"Tmdb": "m-" + title},
		Overview:    "Remote overview of " + title,
		Genres:      []string{"Drama"},
		People:      []nfo.Person{{Name: "Some Actor", Type: "Actor"}},
		PosterURL:   f.poster,
	}, nil
}

func (f *fakeProvider) SeriesSearch(_ context.Context, name string, _ *int32) (metadata.Result, error) {
	f.mu.Lock()
	f.series++
	f.mu.Unlock()
	if f.notFound[name] {
		return metadata.Result{}, metadata.ErrNotFound
	}
	return metadata.Result{
		ProviderIDs: metadata.ProviderIDs{"Tmdb": "s-" + name},
		Overview:    "Remote overview of " + name,
		Genres:      []string{"Sci-Fi"},
	}, nil
}

// drainEnrich processes the enrich queue synchronously until it is empty, so
// tests observe enrichment deterministically without racing the worker.
func (s *Scanner) drainEnrich(ctx context.Context) {
	for {
		select {
		case id := <-s.enrichQ:
			s.enrichItem(ctx, id)
			s.dequeued(id)
		default:
			return
		}
	}
}

func newEnrichScanner(client *ent.Client, prov metadata.Provider, imageDir string) *Scanner {
	return New(client,
		WithProber(probe.Noop{}),
		WithMetadataProvider(prov),
		WithImageCacheDir(imageDir),
		WithMetadataTTL(time.Hour),
	)
}

func movieByName(t *testing.T, client *ent.Client, name string) *ent.MediaItem {
	t.Helper()
	it, err := client.MediaItem.Query().
		Where(mediaitem.KindEQ(mediaitem.KindMovie), mediaitem.NameEQ(name)).
		First(context.Background())
	if err != nil {
		t.Fatalf("query movie %q: %v", name, err)
	}
	return it
}

func TestEnrichFillsAndCachesAcrossDuplicates(t *testing.T) {
	root := t.TempDir()
	// Two distinct files with the same parsed title+year: enrichment must hit
	// the provider once (the second resolves from the persistent cache).
	writeFile(t, filepath.Join(root, "movies", "Inception (2010).mp4"))
	writeFile(t, filepath.Join(root, "movies", "extra", "Inception (2010).mkv"))

	ctx := context.Background()
	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	prov := &fakeProvider{}
	sc := newEnrichScanner(client, prov, filepath.Join(root, "cache"))
	lib, err := sc.EnsureLibrary(ctx, "Movies", "movies", filepath.Join(root, "movies"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.ScanLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	sc.drainEnrich(ctx)

	if m, _ := prov.counts(); m != 1 {
		t.Errorf("MovieSearch calls = %d, want 1 (duplicate title should hit cache)", m)
	}
	movies, err := client.MediaItem.Query().Where(mediaitem.KindEQ(mediaitem.KindMovie)).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(movies) != 2 {
		t.Fatalf("movie count = %d, want 2", len(movies))
	}
	for _, m := range movies {
		if m.Overview == "" {
			t.Errorf("movie %q: overview not filled", m.Path)
		}
		if m.ProviderIds["Tmdb"] != "m-Inception" {
			t.Errorf("movie %q: provider id = %v, want m-Inception", m.Path, m.ProviderIds)
		}
		if m.MetadataSyncedAt == nil {
			t.Errorf("movie %q: metadata_synced_at not set", m.Path)
		}
	}

	// The marker means the sweep finds nothing more to do.
	sc.sweep(ctx)
	sc.drainEnrich(ctx)
	if m, _ := prov.counts(); m != 1 {
		t.Errorf("after sweep MovieSearch calls = %d, want 1", m)
	}
}

func TestEnrichRespectsNFO(t *testing.T) {
	root := t.TempDir()
	moviesDir := filepath.Join(root, "movies")
	writeFile(t, filepath.Join(moviesDir, "Heat (1995).mp4"))
	// A local NFO supplies the overview; remote must not overwrite it, but may
	// fill the genres the NFO omitted.
	writeFileContent(t, filepath.Join(moviesDir, "Heat (1995).nfo"),
		`<movie><title>Heat</title><plot>Local plot wins</plot></movie>`)

	ctx := context.Background()
	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	prov := &fakeProvider{}
	sc := newEnrichScanner(client, prov, filepath.Join(root, "cache"))
	lib, _ := sc.EnsureLibrary(ctx, "Movies", "movies", moviesDir)
	if err := sc.ScanLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	sc.drainEnrich(ctx)

	m := movieByName(t, client, "Heat")
	if m.Overview != "Local plot wins" {
		t.Errorf("overview = %q, want local NFO value preserved", m.Overview)
	}
	if len(m.Genres) != 1 || m.Genres[0] != "Drama" {
		t.Errorf("genres = %v, want remote [Drama] (NFO left them empty)", m.Genres)
	}
}

func TestEnrichSkipsLockedItem(t *testing.T) {
	ctx := context.Background()
	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	prov := &fakeProvider{}
	sc := newEnrichScanner(client, prov, t.TempDir())

	it, err := client.MediaItem.Create().
		SetKind(mediaitem.KindMovie).
		SetName("Locked Movie").
		SetOverview("Keep me").
		SetLockData(true).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sc.enqueueID(it.ID)
	sc.drainEnrich(ctx)

	if m, _ := prov.counts(); m != 0 {
		t.Errorf("MovieSearch calls = %d, want 0 for locked item", m)
	}
	got, _ := client.MediaItem.Get(ctx, it.ID)
	if got.Overview != "Keep me" {
		t.Errorf("overview = %q, want unchanged", got.Overview)
	}
	if got.MetadataSyncedAt == nil {
		t.Error("locked item should be marked synced so it isn't re-swept")
	}
}

func TestEnrichDisabledMakesNoCalls(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "movies", "Alien (1979).mp4"))

	ctx := context.Background()
	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	// Default scanner: no provider configured.
	sc := New(client, WithProber(probe.Noop{}))
	if sc.metaEnabled {
		t.Fatal("metaEnabled should be false without a provider")
	}
	lib, _ := sc.EnsureLibrary(ctx, "Movies", "movies", filepath.Join(root, "movies"))
	if err := sc.ScanLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	sc.drainEnrich(ctx)

	m := movieByName(t, client, "Alien")
	if len(m.ProviderIds) != 0 {
		t.Errorf("provider ids = %v, want none when disabled", m.ProviderIds)
	}
	if m.MetadataSyncedAt != nil {
		t.Error("metadata_synced_at should be nil when enrichment is disabled")
	}
}

func TestEnrichDownloadsPosterAndLocalWins(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Write([]byte("JPEGBYTES"))
	}))
	defer srv.Close()

	root := t.TempDir()
	moviesDir := filepath.Join(root, "movies")
	writeFile(t, filepath.Join(moviesDir, "Dune (2021).mp4"))
	// A second movie that already has a local poster sidecar — remote must not
	// override it.
	writeFile(t, filepath.Join(moviesDir, "Arrival (2016).mp4"))
	localPoster := filepath.Join(moviesDir, "Arrival (2016)-poster.jpg")
	writeFileContent(t, localPoster, "localart")

	ctx := context.Background()
	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	cacheDir := filepath.Join(root, "cache")
	prov := &fakeProvider{poster: srv.URL + "/poster.jpg"}
	sc := newEnrichScanner(client, prov, cacheDir)
	lib, _ := sc.EnsureLibrary(ctx, "Movies", "movies", moviesDir)
	if err := sc.ScanLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	sc.drainEnrich(ctx)

	dune := movieByName(t, client, "Dune")
	if filepath.Dir(dune.ImagePath) != cacheDir {
		t.Errorf("Dune image_path = %q, want a file in the cache dir %q", dune.ImagePath, cacheDir)
	}
	if b, err := os.ReadFile(dune.ImagePath); err != nil || string(b) != "JPEGBYTES" {
		t.Errorf("cached poster contents = %q (err %v), want JPEGBYTES", b, err)
	}

	arrival := movieByName(t, client, "Arrival")
	if arrival.ImagePath != localPoster {
		t.Errorf("Arrival image_path = %q, want local poster %q (local wins)", arrival.ImagePath, localPoster)
	}
	if hits != 1 {
		t.Errorf("poster downloads = %d, want 1 (only Dune; Arrival uses local art)", hits)
	}
}

func TestEnrichNegativeCacheAvoidsRefetch(t *testing.T) {
	ctx := context.Background()
	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	prov := &fakeProvider{notFound: map[string]bool{"Unknown Film": true}}
	sc := newEnrichScanner(client, prov, t.TempDir())

	it, err := client.MediaItem.Create().
		SetKind(mediaitem.KindMovie).
		SetName("Unknown Film").
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sc.enqueueID(it.ID)
	sc.drainEnrich(ctx)

	got, _ := client.MediaItem.Get(ctx, it.ID)
	if got.MetadataSyncedAt == nil {
		t.Error("a definitive not-found should mark the item synced")
	}

	// Force another attempt: the negative cache must prevent a second call.
	if err := got.Update().ClearMetadataSyncedAt().Exec(ctx); err != nil {
		t.Fatal(err)
	}
	sc.enqueueID(it.ID)
	sc.drainEnrich(ctx)
	if m, _ := prov.counts(); m != 1 {
		t.Errorf("MovieSearch calls = %d, want 1 (negative cache should suppress refetch)", m)
	}
}

func TestEnrichTransportErrorIsRetryable(t *testing.T) {
	ctx := context.Background()
	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	prov := &fakeProvider{transport: map[string]bool{"Flaky": true}}
	sc := newEnrichScanner(client, prov, t.TempDir())

	it, err := client.MediaItem.Create().
		SetKind(mediaitem.KindMovie).
		SetName("Flaky").
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sc.enqueueID(it.ID)
	sc.drainEnrich(ctx)

	got, _ := client.MediaItem.Get(ctx, it.ID)
	if got.MetadataSyncedAt != nil {
		t.Error("a transport error must leave the item unsynced so the sweep retries")
	}
	// The sweep should re-enqueue it (still unsynced).
	sc.sweep(ctx)
	sc.drainEnrich(ctx)
	if m, _ := prov.counts(); m != 2 {
		t.Errorf("MovieSearch calls = %d, want 2 (retried after transport error)", m)
	}
}

func TestEnrichSeries(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "tv", "Firefly", "Firefly S01E01.mkv"))
	writeFile(t, filepath.Join(root, "tv", "Firefly", "Firefly S01E02.mkv"))

	ctx := context.Background()
	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	prov := &fakeProvider{}
	sc := newEnrichScanner(client, prov, filepath.Join(root, "cache"))
	lib, _ := sc.EnsureLibrary(ctx, "TV", "tvshows", filepath.Join(root, "tv"))
	if err := sc.ScanLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	sc.drainEnrich(ctx)

	// Two episodes, one series: SeriesSearch must run exactly once.
	if _, srs := prov.counts(); srs != 1 {
		t.Errorf("SeriesSearch calls = %d, want 1", srs)
	}
	series, err := client.MediaItem.Query().Where(mediaitem.KindEQ(mediaitem.KindSeries)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if series.Overview == "" || series.ProviderIds["Tmdb"] != "s-Firefly" {
		t.Errorf("series not enriched: overview=%q ids=%v", series.Overview, series.ProviderIds)
	}
}
