package server_test

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/ent/mediaitem"
	"github.com/gartnera/gofin/internal/db"
	"github.com/gartnera/gofin/internal/jellyfin"
	"github.com/gartnera/gofin/internal/server"
	"github.com/google/uuid"
)

// benchEnv is a server backed by a large, directly-seeded library: 10k movies
// and 10k episodes (500 series x 2 seasons x 10). Seeding bypasses the scanner
// (bulk inserts) so the benchmark isolates query/serialisation cost.
type benchEnv struct {
	srv      *httptest.Server
	token    string
	moviesID string // library id (dashless hex)
	tvID     string
	seriesID string // a representative series with full season/episode tree
	seasonID string
	movieID  string
}

const (
	benchMovies           = 10000
	benchSeries           = 500
	benchSeasonsPerSeries = 2
	benchEpsPerSeason     = 10
)

func setupBenchEnv(b *testing.B) *benchEnv {
	b.Helper()
	ctx := context.Background()
	client, err := db.OpenMemory(ctx, b.Name())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { client.Close() })

	seedUser(b, ctx, client)

	env := &benchEnv{}
	env.movieID = seedMovies(b, ctx, client, &env.moviesID)
	env.seriesID, env.seasonID = seedEpisodes(b, ctx, client, &env.tvID)

	// A/B switch: with GOFIN_BENCH_NOINDEX=1 drop the composite indexes added
	// for large-library queries so a benchmark run quantifies their effect.
	// A second connection to the same shared-cache in-memory database operates
	// on the same data the ent client serves from.
	if os.Getenv("GOFIN_BENCH_NOINDEX") == "1" {
		dropQueryIndexes(b)
	}

	srv := httptest.NewServer(server.New(client, "bench-server"))
	b.Cleanup(srv.Close)
	env.srv = srv
	env.token = authToken(b, srv.URL)
	return env
}

// get issues an authenticated GET and fully drains the body, returning the
// status. It fails the benchmark on transport errors.
func (e *benchEnv) get(b *testing.B, path string) {
	req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
	req.Header.Set("Authorization", fmt.Sprintf(`MediaBrowser Token=%q`, e.token))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		b.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b.Fatalf("GET %s: status %d", path, resp.StatusCode)
	}
}

// seedMovies inserts benchMovies Movie rows in one library and returns a
// representative movie id; libID is set to the library's dashless-hex id.
func seedMovies(b *testing.B, ctx context.Context, client *ent.Client, libID *string) string {
	b.Helper()
	lib, err := client.Library.Create().SetName("Movies").SetType("movies").SetPath("/bench/movies").Save(ctx)
	if err != nil {
		b.Fatal(err)
	}
	*libID = jellyfin.FormatID(lib.ID)

	builders := make([]*ent.MediaItemCreate, 0, benchMovies)
	for i := 0; i < benchMovies; i++ {
		name := fmt.Sprintf("Movie %05d %s", i, word(i))
		builders = append(builders, client.MediaItem.Create().
			SetKind(mediaitem.KindMovie).
			SetName(name).
			SetSortName(strings.ToLower(name)).
			SetPath(fmt.Sprintf("/bench/movies/%05d.mkv", i)).
			SetContainer("mkv").
			SetRunTimeTicks(72_000_000_000).
			SetMtime(int64(i)).
			SetProductionYear(int32(1970+i%55)).
			SetLibrary(lib))
	}
	saved := saveAll(b, ctx, client, builders)
	return jellyfin.FormatID(saved[len(saved)/2].ID)
}

// seedEpisodes builds a TV library of benchSeries series, each with
// benchSeasonsPerSeries seasons of benchEpsPerSeason episodes. Returns a
// representative series id and one of its season ids.
func seedEpisodes(b *testing.B, ctx context.Context, client *ent.Client, libID *string) (string, string) {
	b.Helper()
	lib, err := client.Library.Create().SetName("TV").SetType("tvshows").SetPath("/bench/tv").Save(ctx)
	if err != nil {
		b.Fatal(err)
	}
	*libID = jellyfin.FormatID(lib.ID)

	var pickSeries, pickSeason uuid.UUID
	for si := 0; si < benchSeries; si++ {
		sName := fmt.Sprintf("Series %04d %s", si, word(si))
		series, err := client.MediaItem.Create().
			SetKind(mediaitem.KindSeries).SetName(sName).SetSortName(strings.ToLower(sName)).
			SetLibrary(lib).Save(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if si == benchSeries/2 {
			pickSeries = series.ID
		}
		for sea := 1; sea <= benchSeasonsPerSeries; sea++ {
			seasonName := fmt.Sprintf("Season %02d", sea)
			season, err := client.MediaItem.Create().
				SetKind(mediaitem.KindSeason).SetName(seasonName).SetSortName(seasonName).
				SetIndexNumber(int32(sea)).SetParentID(series.ID).SetLibrary(lib).Save(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if si == benchSeries/2 && sea == 1 {
				pickSeason = season.ID
			}
			builders := make([]*ent.MediaItemCreate, 0, benchEpsPerSeason)
			for ep := 1; ep <= benchEpsPerSeason; ep++ {
				epName := fmt.Sprintf("%s S%02dE%02d", sName, sea, ep)
				builders = append(builders, client.MediaItem.Create().
					SetKind(mediaitem.KindEpisode).
					SetName(epName).SetSortName(fmt.Sprintf("%04d", ep)).
					SetPath(fmt.Sprintf("/bench/tv/%04d/%02d/%02d.mkv", si, sea, ep)).
					SetContainer("mkv").SetRunTimeTicks(72_000_000_000).
					SetIndexNumber(int32(ep)).SetParentIndexNumber(int32(sea)).
					SetParentID(season.ID).SetLibrary(lib))
			}
			saveAll(b, ctx, client, builders)
		}
	}
	return jellyfin.FormatID(pickSeries), jellyfin.FormatID(pickSeason)
}

// saveAll inserts builders in batches via CreateBulk and returns the saved rows.
func saveAll(b *testing.B, ctx context.Context, client *ent.Client, builders []*ent.MediaItemCreate) []*ent.MediaItem {
	b.Helper()
	const batch = 500
	out := make([]*ent.MediaItem, 0, len(builders))
	for i := 0; i < len(builders); i += batch {
		end := i + batch
		if end > len(builders) {
			end = len(builders)
		}
		saved, err := client.MediaItem.CreateBulk(builders[i:end]...).Save(ctx)
		if err != nil {
			b.Fatal(err)
		}
		out = append(out, saved...)
	}
	return out
}

// dropQueryIndexes removes the MediaItem query indexes from the shared-cache
// in-memory database the current benchmark uses, for A/B measurement.
func dropQueryIndexes(b *testing.B) {
	b.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_fk=1", b.Name())
	sqlDB, err := sql.Open("sqlite3", dsn)
	if err != nil {
		b.Fatal(err)
	}
	defer sqlDB.Close()
	for _, idx := range []string{
		"mediaitem_kind_sort_name_library_items",
		"mediaitem_media_item_children",
		"mediaitem_library_items",
		"mediaitem_mtime_library_items",
	} {
		if _, err := sqlDB.Exec("DROP INDEX IF EXISTS " + idx); err != nil {
			b.Fatalf("drop %s: %v", idx, err)
		}
	}
}

func word(i int) string {
	w := []string{"River", "Empire", "Horizon", "Shadow", "Phoenix", "Garden", "Machine", "Voyage"}
	return w[i%len(w)]
}

func BenchmarkListMoviesFirstPage(b *testing.B) {
	env := setupBenchEnv(b)
	path := "/Items?ParentId=" + env.moviesID + "&IncludeItemTypes=Movie&Recursive=true&SortBy=SortName&Limit=50&StartIndex=0"
	b.ResetTimer()
	for b.Loop() {
		env.get(b, path)
	}
}

func BenchmarkListMoviesDeepPage(b *testing.B) {
	env := setupBenchEnv(b)
	path := "/Items?ParentId=" + env.moviesID + "&IncludeItemTypes=Movie&Recursive=true&SortBy=SortName&Limit=50&StartIndex=9000"
	b.ResetTimer()
	for b.Loop() {
		env.get(b, path)
	}
}

func BenchmarkSearchMovies(b *testing.B) {
	env := setupBenchEnv(b)
	path := "/Items?IncludeItemTypes=Movie&Recursive=true&SearchTerm=Phoenix&Limit=50"
	b.ResetTimer()
	for b.Loop() {
		env.get(b, path)
	}
}

func BenchmarkLatestMovies(b *testing.B) {
	env := setupBenchEnv(b)
	path := "/Items/Latest?ParentId=" + env.moviesID + "&Limit=20"
	b.ResetTimer()
	for b.Loop() {
		env.get(b, path)
	}
}

func BenchmarkSeriesSeasons(b *testing.B) {
	env := setupBenchEnv(b)
	path := "/Shows/" + env.seriesID + "/Seasons"
	b.ResetTimer()
	for b.Loop() {
		env.get(b, path)
	}
}

func BenchmarkSeasonEpisodes(b *testing.B) {
	env := setupBenchEnv(b)
	path := "/Shows/" + env.seriesID + "/Episodes?SeasonId=" + env.seasonID
	b.ResetTimer()
	for b.Loop() {
		env.get(b, path)
	}
}

func BenchmarkAllSeriesEpisodes(b *testing.B) {
	env := setupBenchEnv(b)
	path := "/Shows/" + env.seriesID + "/Episodes"
	b.ResetTimer()
	for b.Loop() {
		env.get(b, path)
	}
}

func BenchmarkItemByID(b *testing.B) {
	env := setupBenchEnv(b)
	path := "/Items/" + env.movieID
	b.ResetTimer()
	for b.Loop() {
		env.get(b, path)
	}
}
