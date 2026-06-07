package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/internal/auth"
	"github.com/gartnera/gofin/internal/db"
	"github.com/gartnera/gofin/internal/probe"
	"github.com/gartnera/gofin/internal/scanner"
	"github.com/gartnera/gofin/internal/server"
	jfapi "github.com/sj14/jellyfin-go/api"
)

// fakeRuntimeTicks is the duration the fake prober reports for every file, so
// resume/played thresholds are deterministic in tests.
const fakeRuntimeTicks int64 = 1_000_000

// fakeProber returns fixed duration and streams for every file.
type fakeProber struct{}

func (fakeProber) Probe(context.Context, string) (probe.Result, error) {
	return probe.Result{
		RunTimeTicks: fakeRuntimeTicks,
		Streams: []probe.Stream{
			{Index: 0, Type: "Video", Codec: "h264", Width: 1920, Height: 1080},
			{Index: 1, Type: "Audio", Codec: "aac", Channels: 2},
		},
	}, nil
}

const (
	testUser     = "demo"
	testPassword = "demo-pass"
	clientHeader = `MediaBrowser Client="gofin-test", Device="ci", DeviceId="dev-1", Version="1.0"`
)

// testEnv holds the running server and a pre-authenticated access token.
type testEnv struct {
	srv   *httptest.Server
	token string
}

// writeMedia writes a file with placeholder content, creating parent dirs.
func writeMedia(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setupEnv builds a temp media tree (with arbitrary nesting), indexes it,
// seeds a user, and starts an httptest server.
func setupEnv(t *testing.T) *testEnv {
	t.Helper()
	root := t.TempDir()

	// Movies library (note the nested subdirectory: arbitrary on-disk layout).
	writeMedia(t, filepath.Join(root, "movies", "Inception (2010).mp4"), "inception-video-payload")
	writeMedia(t, filepath.Join(root, "movies", "nested", "The Matrix (1999).mkv"), "matrix-video-payload")
	// TV library.
	writeMedia(t, filepath.Join(root, "tv", "Breaking Bad", "Season 01", "Breaking Bad - S01E01 - Pilot.mp4"), "bb-s01e01-payload")
	// Music library (tags absent -> path-derived hierarchy).
	writeMedia(t, filepath.Join(root, "music", "Daft Punk", "Discovery", "01 One More Time.mp3"), "music-payload")

	ctx := context.Background()
	client, err := db.OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })

	seedUser(t, ctx, client)

	sc := scanner.New(client, scanner.WithProber(fakeProber{}))
	for _, l := range []struct{ name, typ, sub string }{
		{"Movies", "movies", "movies"},
		{"TV Shows", "tvshows", "tv"},
		{"Music", "music", "music"},
	} {
		lib, err := sc.EnsureLibrary(ctx, l.name, l.typ, filepath.Join(root, l.sub))
		if err != nil {
			t.Fatalf("ensure %s: %v", l.name, err)
		}
		if err := sc.ScanLibrary(ctx, lib); err != nil {
			t.Fatalf("scan %s: %v", l.name, err)
		}
	}

	srv := httptest.NewServer(server.New(client, "test-server"))
	t.Cleanup(srv.Close)

	env := &testEnv{srv: srv}
	env.token = env.authenticate(t)
	return env
}

func seedUser(t *testing.T, ctx context.Context, client *ent.Client) {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.User.Create().SetName(testUser).SetPasswordHash(hash).SetIsAdmin(true).Save(ctx); err != nil {
		t.Fatal(err)
	}
}

// anonClient returns a client with only client-identity headers (no token).
func anonClient(url string) *jfapi.APIClient {
	cfg := jfapi.NewConfiguration()
	cfg.Servers = jfapi.ServerConfigurations{{URL: url}}
	cfg.DefaultHeader = map[string]string{"Authorization": clientHeader}
	return jfapi.NewAPIClient(cfg)
}

// authedClient returns a client whose Authorization header carries the token.
func authedClient(url, token string) *jfapi.APIClient {
	cfg := jfapi.NewConfiguration()
	cfg.Servers = jfapi.ServerConfigurations{{URL: url}}
	cfg.DefaultHeader = map[string]string{
		"Authorization": fmt.Sprintf(`MediaBrowser Client="gofin-test", Device="ci", DeviceId="dev-1", Version="1.0", Token="%s"`, token),
	}
	return jfapi.NewAPIClient(cfg)
}

func (e *testEnv) authenticate(t *testing.T) string {
	t.Helper()
	body := jfapi.NewAuthenticateUserByName()
	body.SetUsername(testUser)
	body.SetPw(testPassword)
	res, _, err := anonClient(e.srv.URL).UserAPI.AuthenticateUserByName(context.Background()).
		AuthenticateUserByName(*body).Execute()
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if res.GetAccessToken() == "" {
		t.Fatal("empty access token")
	}
	return res.GetAccessToken()
}

func TestPublicSystemInfo(t *testing.T) {
	env := setupEnv(t)
	info, _, err := anonClient(env.srv.URL).SystemAPI.GetPublicSystemInfo(context.Background()).Execute()
	if err != nil {
		t.Fatalf("GetPublicSystemInfo: %v", err)
	}
	if info.GetServerName() != "test-server" {
		t.Errorf("ServerName = %q, want test-server", info.GetServerName())
	}
}

func TestAuthenticateWrongPassword(t *testing.T) {
	env := setupEnv(t)
	body := jfapi.NewAuthenticateUserByName()
	body.SetUsername(testUser)
	body.SetPw("wrong")
	_, resp, err := anonClient(env.srv.URL).UserAPI.AuthenticateUserByName(context.Background()).
		AuthenticateUserByName(*body).Execute()
	if err == nil {
		t.Fatal("expected error for wrong password")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %v", resp)
	}
}

func TestUserViews(t *testing.T) {
	env := setupEnv(t)
	views, _, err := authedClient(env.srv.URL, env.token).UserViewsAPI.GetUserViews(context.Background()).Execute()
	if err != nil {
		t.Fatalf("GetUserViews: %v", err)
	}
	if got := len(views.Items); got != 3 {
		t.Fatalf("view count = %d, want 3", got)
	}
}

func TestBrowseHierarchy(t *testing.T) {
	env := setupEnv(t)
	client := authedClient(env.srv.URL, env.token)
	ctx := context.Background()

	views, _, err := client.UserViewsAPI.GetUserViews(ctx).Execute()
	if err != nil {
		t.Fatal(err)
	}

	for _, view := range views.Items {
		switch view.GetCollectionType() {
		case jfapi.COLLECTIONTYPE_MOVIES:
			items := childrenOf(t, client, view.GetId())
			if len(items.Items) != 2 {
				t.Errorf("movies count = %d, want 2", len(items.Items))
			}
		case jfapi.COLLECTIONTYPE_TVSHOWS:
			series := childrenOf(t, client, view.GetId())
			if len(series.Items) != 1 || series.Items[0].GetType() != jfapi.BASEITEMKIND_SERIES {
				t.Fatalf("expected 1 series, got %+v", series.Items)
			}
			seasons := childrenOf(t, client, series.Items[0].GetId())
			if len(seasons.Items) != 1 || seasons.Items[0].GetType() != jfapi.BASEITEMKIND_SEASON {
				t.Fatalf("expected 1 season, got %+v", seasons.Items)
			}
			episodes := childrenOf(t, client, seasons.Items[0].GetId())
			if len(episodes.Items) != 1 || episodes.Items[0].GetType() != jfapi.BASEITEMKIND_EPISODE {
				t.Fatalf("expected 1 episode, got %+v", episodes.Items)
			}
		case jfapi.COLLECTIONTYPE_MUSIC:
			artists := childrenOf(t, client, view.GetId())
			if len(artists.Items) != 1 || artists.Items[0].GetType() != jfapi.BASEITEMKIND_MUSIC_ARTIST {
				t.Fatalf("expected 1 artist, got %+v", artists.Items)
			}
			albums := childrenOf(t, client, artists.Items[0].GetId())
			if len(albums.Items) != 1 || albums.Items[0].GetType() != jfapi.BASEITEMKIND_MUSIC_ALBUM {
				t.Fatalf("expected 1 album, got %+v", albums.Items)
			}
			tracks := childrenOf(t, client, albums.Items[0].GetId())
			if len(tracks.Items) != 1 || tracks.Items[0].GetType() != jfapi.BASEITEMKIND_AUDIO {
				t.Fatalf("expected 1 track, got %+v", tracks.Items)
			}
		}
	}
}

func childrenOf(t *testing.T, client *jfapi.APIClient, parentID string) *jfapi.BaseItemDtoQueryResult {
	t.Helper()
	res, _, err := client.ItemsAPI.GetItems(context.Background()).ParentId(parentID).Execute()
	if err != nil {
		t.Fatalf("GetItems(parentId=%s): %v", parentID, err)
	}
	return res
}

func TestPlaybackInfoAndDirectPlay(t *testing.T) {
	env := setupEnv(t)
	client := authedClient(env.srv.URL, env.token)
	ctx := context.Background()

	// Find a movie.
	res, _, err := client.ItemsAPI.GetItems(ctx).
		Recursive(true).
		IncludeItemTypes([]jfapi.BaseItemKind{jfapi.BASEITEMKIND_MOVIE}).
		Execute()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) == 0 {
		t.Fatal("no movies found")
	}
	movieID := res.Items[0].GetId()

	// PlaybackInfo should advertise direct play.
	pb, _, err := client.MediaInfoAPI.GetPostedPlaybackInfo(ctx, movieID).Execute()
	if err != nil {
		t.Fatalf("PlaybackInfo: %v", err)
	}
	if len(pb.MediaSources) != 1 || !pb.MediaSources[0].GetSupportsDirectPlay() {
		t.Fatalf("expected a direct-play media source, got %+v", pb.MediaSources)
	}

	// Full direct-play download.
	full := streamRequest(t, env, movieID, "")
	if full.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", full.StatusCode)
	}
	body, _ := io.ReadAll(full.Body)
	full.Body.Close()
	if len(body) == 0 {
		t.Fatal("empty stream body")
	}

	// Ranged request proves seek support.
	ranged := streamRequest(t, env, movieID, "bytes=0-1")
	if ranged.StatusCode != http.StatusPartialContent {
		t.Fatalf("ranged status = %d, want 206", ranged.StatusCode)
	}
	if cr := ranged.Header.Get("Content-Range"); cr == "" {
		t.Error("missing Content-Range header on ranged response")
	}
	rb, _ := io.ReadAll(ranged.Body)
	ranged.Body.Close()
	if len(rb) != 2 {
		t.Errorf("ranged body length = %d, want 2", len(rb))
	}
}

func streamRequest(t *testing.T, env *testEnv, itemID, rangeHeader string) *http.Response {
	t.Helper()
	url := fmt.Sprintf("%s/Videos/%s/stream?ApiKey=%s", env.srv.URL, itemID, env.token)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// mustFindItem returns the first indexed item of the given kind, failing the
// test if none exists.
func mustFindItem(t *testing.T, client *jfapi.APIClient, kind jfapi.BaseItemKind) jfapi.BaseItemDto {
	t.Helper()
	res, _, err := client.ItemsAPI.GetItems(context.Background()).
		Recursive(true).
		IncludeItemTypes([]jfapi.BaseItemKind{kind}).
		Execute()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) == 0 {
		t.Fatalf("no %s items indexed", kind)
	}
	return res.Items[0]
}

// authedGET issues a GET to path on the test server using the env's access
// token (via ApiKey query parameter).
func authedGET(t *testing.T, env *testEnv, path string) *http.Response {
	t.Helper()
	sep := "?"
	if containsRune(path, '?') {
		sep = "&"
	}
	url := fmt.Sprintf("%s%s%sApiKey=%s", env.srv.URL, path, sep, env.token)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// getJSON GETs path with the env token and decodes the body into T.
func getJSON[T any](t *testing.T, fullURL, token string) T {
	t.Helper()
	sep := "?"
	if containsRune(fullURL, '?') {
		sep = "&"
	}
	req, err := http.NewRequest("GET", fullURL+sep+"ApiKey="+token, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s = %d: %s", fullURL, resp.StatusCode, body)
	}
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode %s: %v", fullURL, err)
	}
	return v
}

// firstMovie returns the id of an indexed movie.
func firstMovie(t *testing.T, client *jfapi.APIClient) string {
	t.Helper()
	res, _, err := client.ItemsAPI.GetItems(context.Background()).
		Recursive(true).
		IncludeItemTypes([]jfapi.BaseItemKind{jfapi.BASEITEMKIND_MOVIE}).
		Execute()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) == 0 {
		t.Fatal("no movies indexed")
	}
	return res.Items[0].GetId()
}

func TestMediaStreamsInPlaybackInfo(t *testing.T) {
	env := setupEnv(t)
	client := authedClient(env.srv.URL, env.token)
	movieID := firstMovie(t, client)

	pb, _, err := client.MediaInfoAPI.GetPostedPlaybackInfo(context.Background(), movieID).Execute()
	if err != nil {
		t.Fatal(err)
	}
	if len(pb.MediaSources) != 1 {
		t.Fatalf("media sources = %d, want 1", len(pb.MediaSources))
	}
	src := pb.MediaSources[0]
	if src.GetRunTimeTicks() != fakeRuntimeTicks {
		t.Errorf("RunTimeTicks = %d, want %d", src.GetRunTimeTicks(), fakeRuntimeTicks)
	}
	if len(src.MediaStreams) != 2 {
		t.Fatalf("media streams = %d, want 2", len(src.MediaStreams))
	}
	if src.MediaStreams[0].GetCodec() != "h264" {
		t.Errorf("video codec = %q, want h264", src.MediaStreams[0].GetCodec())
	}
}

func TestProgressResumeAndPlayed(t *testing.T) {
	env := setupEnv(t)
	client := authedClient(env.srv.URL, env.token)
	ctx := context.Background()
	movieID := firstMovie(t, client)

	// Report progress at 40% -> appears in Resume with the saved position.
	prog := jfapi.NewPlaybackProgressInfo()
	prog.SetItemId(movieID)
	prog.SetPositionTicks(400_000)
	if _, err := client.PlaystateAPI.ReportPlaybackProgress(ctx).PlaybackProgressInfo(*prog).Execute(); err != nil {
		t.Fatalf("ReportPlaybackProgress: %v", err)
	}

	resume, _, err := client.ItemsAPI.GetResumeItems(ctx).Execute()
	if err != nil {
		t.Fatalf("GetResumeItems: %v", err)
	}
	if len(resume.Items) != 1 {
		t.Fatalf("resume items = %d, want 1", len(resume.Items))
	}
	if ud := resume.Items[0].GetUserData(); ud.GetPlaybackPositionTicks() != 400_000 || ud.GetPlayed() {
		t.Errorf("unexpected resume UserData: %+v", ud)
	}

	// Report stopped near the end (95% >= 90% threshold) -> marked played,
	// resume position cleared.
	stop := jfapi.NewPlaybackStopInfo()
	stop.SetItemId(movieID)
	stop.SetPositionTicks(950_000)
	if _, err := client.PlaystateAPI.ReportPlaybackStopped(ctx).PlaybackStopInfo(*stop).Execute(); err != nil {
		t.Fatalf("ReportPlaybackStopped: %v", err)
	}

	resume2, _, err := client.ItemsAPI.GetResumeItems(ctx).Execute()
	if err != nil {
		t.Fatal(err)
	}
	if len(resume2.Items) != 0 {
		t.Errorf("resume items after finishing = %d, want 0", len(resume2.Items))
	}

	items, _, err := client.ItemsAPI.GetItems(ctx).
		Recursive(true).
		IncludeItemTypes([]jfapi.BaseItemKind{jfapi.BASEITEMKIND_MOVIE}).
		Execute()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, it := range items.Items {
		if it.GetId() == movieID {
			found = true
			if ud := it.GetUserData(); !ud.GetPlayed() || ud.GetPlaybackPositionTicks() != 0 {
				t.Errorf("expected played with reset position, got %+v", ud)
			}
		}
	}
	if !found {
		t.Fatal("movie not found in item list")
	}
}

func TestShowsSeasonsAndEpisodes(t *testing.T) {
	env := setupEnv(t)
	client := authedClient(env.srv.URL, env.token)
	ctx := context.Background()

	series := mustFindItem(t, client, jfapi.BASEITEMKIND_SERIES)

	seasons, _, err := client.TvShowsAPI.GetSeasons(ctx, series.GetId()).Execute()
	if err != nil {
		t.Fatalf("GetSeasons: %v", err)
	}
	if len(seasons.Items) != 1 || seasons.Items[0].GetType() != jfapi.BASEITEMKIND_SEASON {
		t.Fatalf("seasons = %+v, want 1 Season", seasons.Items)
	}
	seasonID := seasons.Items[0].GetId()

	// All episodes under the series.
	eps, _, err := client.TvShowsAPI.GetEpisodes(ctx, series.GetId()).Execute()
	if err != nil {
		t.Fatalf("GetEpisodes: %v", err)
	}
	if len(eps.Items) != 1 || eps.Items[0].GetType() != jfapi.BASEITEMKIND_EPISODE {
		t.Fatalf("episodes = %+v, want 1 Episode", eps.Items)
	}

	// Episodes filtered by season id.
	scoped, _, err := client.TvShowsAPI.GetEpisodes(ctx, series.GetId()).SeasonId(seasonID).Execute()
	if err != nil {
		t.Fatalf("GetEpisodes(season): %v", err)
	}
	if len(scoped.Items) != 1 {
		t.Errorf("episodes for season = %d, want 1", len(scoped.Items))
	}

	// Unknown series id resolves to an empty result, not a 5xx.
	bogus, _, err := client.TvShowsAPI.GetSeasons(ctx, "ffffffffffffffffffffffffffffffff").Execute()
	if err != nil {
		t.Fatalf("GetSeasons(bogus): %v", err)
	}
	if len(bogus.Items) != 0 {
		t.Errorf("bogus seasons = %d, want 0", len(bogus.Items))
	}
}

func TestLatestAndNextUpAndIntros(t *testing.T) {
	env := setupEnv(t)
	token := env.token

	// Latest is a bare array; NextUp / Intros are QueryResult-shaped with an
	// `Items: []` array even when empty.
	latest := getJSON[[]map[string]any](t, env.srv.URL+"/Users/_/Items/Latest?Limit=10", token)
	if len(latest) == 0 {
		t.Error("Latest returned no items")
	}

	nextUp := getJSON[map[string]any](t, env.srv.URL+"/Shows/NextUp", token)
	if _, ok := nextUp["Items"].([]any); !ok {
		t.Errorf("NextUp.Items = %v, want []", nextUp["Items"])
	}

	client := authedClient(env.srv.URL, env.token)
	movieID := firstMovie(t, client)
	intros := getJSON[map[string]any](t, env.srv.URL+"/Users/_/Items/"+movieID+"/Intros", token)
	if _, ok := intros["Items"].([]any); !ok {
		t.Errorf("Intros.Items = %v, want []", intros["Items"])
	}
}

func TestLibraryViewResolvableAsItem(t *testing.T) {
	env := setupEnv(t)
	client := authedClient(env.srv.URL, env.token)
	ctx := context.Background()

	views, _, err := client.UserViewsAPI.GetUserViews(ctx).Execute()
	if err != nil {
		t.Fatal(err)
	}
	if len(views.Items) == 0 {
		t.Fatal("no views")
	}
	libID := views.Items[0].GetId()

	// The web client requests /Items/{libraryId} as part of its navigation;
	// gofin used to 404 for any id not in the MediaItem table.
	got, _, err := client.UserLibraryAPI.GetItem(ctx, libID).Execute()
	if err != nil {
		t.Fatalf("GetItem(library): %v", err)
	}
	if got.GetType() != jfapi.BASEITEMKIND_COLLECTION_FOLDER {
		t.Errorf("type = %v, want CollectionFolder", got.GetType())
	}
}

func TestEndpointAndBitrateTestAndDisplayPrefsWrite(t *testing.T) {
	env := setupEnv(t)

	ep := getJSON[map[string]any](t, env.srv.URL+"/System/Endpoint", env.token)
	if local, _ := ep["IsLocal"].(bool); !local {
		t.Errorf("IsLocal = %v, want true", ep["IsLocal"])
	}

	resp := authedGET(t, env, "/Playback/BitrateTest?Size=4096")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("BitrateTest status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) != 4096 {
		t.Errorf("BitrateTest body len = %d, want 4096", len(body))
	}

	// POST to DisplayPreferences must be accepted; the web client treats a
	// 405/404 here as fatal during navigation.
	dprResp := postAuthed(t, env, "/DisplayPreferences/usersettings?client=emby")
	defer dprResp.Body.Close()
	if dprResp.StatusCode != http.StatusNoContent {
		t.Errorf("POST DisplayPreferences status = %d, want 204", dprResp.StatusCode)
	}
}

func TestStreamWithContainerExtension(t *testing.T) {
	env := setupEnv(t)
	client := authedClient(env.srv.URL, env.token)
	movieID := firstMovie(t, client)

	// The web client appends the container as a fake extension; path
	// normalisation should rewrite it back to /stream.
	url := fmt.Sprintf("%s/Videos/%s/stream.mp4?Static=true&ApiKey=%s", env.srv.URL, movieID, env.token)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream.mp4 status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct == "" || ct[:5] != "video" {
		t.Errorf("Content-Type = %q, want video/*", ct)
	}
}

// TestGetItemsByIds mirrors the web client's playback flow: when starting
// playback it fetches the exact items to queue by id. Without Ids filtering the
// server returned unrelated top-level items, so the player saw an item with no
// MediaType ("No player found for the requested media: undefined").
func TestGetItemsByIds(t *testing.T) {
	env := setupEnv(t)
	client := authedClient(env.srv.URL, env.token)
	ctx := context.Background()

	tracks, _, err := client.ItemsAPI.GetItems(ctx).
		Recursive(true).
		IncludeItemTypes([]jfapi.BaseItemKind{jfapi.BASEITEMKIND_AUDIO}).
		Execute()
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks.Items) == 0 {
		t.Fatal("no audio items indexed")
	}
	wantID := tracks.Items[0].GetId()

	res, _, err := client.ItemsAPI.GetItems(ctx).Ids([]string{wantID}).Execute()
	if err != nil {
		t.Fatalf("GetItems(Ids): %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("GetItems(Ids) returned %d items, want 1", len(res.Items))
	}
	got := res.Items[0]
	if got.GetId() != wantID {
		t.Errorf("returned id = %q, want %q", got.GetId(), wantID)
	}
	if got.GetMediaType() != jfapi.MEDIATYPE_AUDIO {
		t.Errorf("MediaType = %q, want Audio", got.GetMediaType())
	}
}

func TestAudioItemExposesAlbumLinks(t *testing.T) {
	env := setupEnv(t)
	client := authedClient(env.srv.URL, env.token)
	ctx := context.Background()

	tracks, _, err := client.ItemsAPI.GetItems(ctx).
		Recursive(true).
		IncludeItemTypes([]jfapi.BaseItemKind{jfapi.BASEITEMKIND_AUDIO}).
		Execute()
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks.Items) == 0 {
		t.Fatal("no audio items indexed")
	}
	track := tracks.Items[0]

	// AlbumId is what makes "open the album" links work in the now-playing
	// bar, and the album page reads ArtistItems/AlbumArtists unconditionally.
	if track.GetAlbumId() == "" {
		t.Error("AlbumId is empty")
	}
	if len(track.GetArtistItems()) == 0 {
		t.Error("ArtistItems is empty")
	}
	if len(track.GetAlbumArtists()) == 0 {
		t.Error("AlbumArtists is empty")
	}
}

func TestStreamRequiresAuth(t *testing.T) {
	env := setupEnv(t)
	resp, err := http.Get(fmt.Sprintf("%s/Videos/%s/stream", env.srv.URL, "deadbeef"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}
