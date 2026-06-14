# gofin

A minimal, Jellyfin-API-compatible media server written in Go. It lets existing
Jellyfin clients connect, authenticate, browse a library, and **direct play**
video and audio. It is intentionally small: manual indexing, direct play only
(no transcoding), and a SQLite-backed catalog.

## What's supported

gofin deliberately implements a focused subset of Jellyfin. The lists below are
the source of truth for what works today — anything not listed should be assumed
unsupported.

### Supported

- [x] Jellyfin-compatible HTTP API — responses reuse the model structs from
  [`github.com/sj14/jellyfin-go`](https://github.com/sj14/jellyfin-go), so JSON
  field names match real Jellyfin and stock clients can connect.
- [x] Username/password auth with **bcrypt** hashing and persisted access tokens.
- [x] Admin vs. non-admin users (admin-only scan/refresh and metadata-edit
  endpoints).
- [x] Movies, TV (Series → Season → Episode) and Music (Artist → Album → Track).
- [x] **Direct play** of video and audio with HTTP range support (seeking) via
  `http.ServeContent`.
- [x] Filename-based metadata for video, embedded-tag metadata for audio,
  including Jellyfin-style TV episode naming (flat shows, anime absolute
  numbering, multi-episode ranges, date-based detection).
- [x] Local **NFO** sidecar metadata (Kodi/Jellyfin `.nfo`: overview, genres,
  studios, cast/crew, ratings, premiere date).
- [x] Local **artwork** — poster/cover/thumbnail image files discovered on disk
  (per-file `<name>-poster`/`<name>.jpg` sidecars and folder-level
  `poster`/`folder`/`cover`/`seasonNN-poster` images, following Kodi/Jellyfin
  naming) and served as the item's Primary image. Episodes and tracks without
  their own image inherit the series poster / album cover.
- [x] Stream/codec metadata and durations via `ffprobe` when available — video,
  audio and **embedded** subtitle streams are exposed as `MediaStreams`. Probing
  is pluggable and degrades gracefully when ffprobe is not installed.
- [x] Playback state: watched status, play count, resume positions
  (`/Sessions/Playing*`, `/UserItems/Resume`, `UserData` on items).
- [x] Paging, sorting and search on `/Items` (`Limit`, `StartIndex`, `SortBy`,
  `searchTerm`), plus `/Items/Latest` and `/Shows/NextUp`.
- [x] Metadata editing with per-field / whole-item **locks** that survive
  rescans (`POST /Items/{id}`).
- [x] Live index updates via a filesystem watcher (`fsnotify`); manual rescan via
  `POST /Library/Refresh`. To stay within Linux's `fs.inotify.max_user_watches`
  on large libraries, the watcher always watches container directories (so new
  folders are detected) but watches individual leaf folders only when modified
  within a recency window (default 7 days, `watch.window_days`); a periodic full
  rescan (default daily, `watch.rescan_hours`) heals anything the window skipped.
- [x] **Remote metadata** for movies and TV series via **TMDb** (overview,
  genres, studios, cast/crew, ratings, premiere date and **poster**) — opt-in
  (set `metadata.enabled` + a `tmdb_token` in config). Fetching runs in a
  background worker, never blocks scanning, reuses the local library before any
  remote call, and caches every response (including posters) on disk so rescans
  and restarts make no repeat requests. Local NFO/artwork and locked fields
  always win — remote only fills gaps.
- [x] **Quick Connect** — passwordless login via a short code approved from an
  already-signed-in session (`/QuickConnect/*` + `AuthenticateWithQuickConnect`).
  Enabled by default; disable with `quick_connect: false` in config.

### Not supported

These are either absent or intentionally stubbed (the endpoint exists and
returns an empty/benign response so stock clients don't error, but there is no
real implementation behind it):

- [ ] **Transcoding / remuxing** — direct play only; `SupportsTranscoding` is
  hard-disabled. Clients must support the source codecs/containers.
- [ ] **External subtitle files** (`.srt`, `.vtt`, `.ass`) — only subtitle
  streams already embedded in the media file are surfaced.
- [ ] **Remote metadata beyond movies/series** — TMDb enrichment covers movies
  and TV series (see Supported); per-episode/season remote details, **music**
  providers (MusicBrainz), backdrops, and other providers (TVDB, IMDb) are not
  fetched. Image art embedded inside media files (e.g. ID3 cover art) is also not
  extracted — only standalone and downloaded image files on disk are served.
- [ ] **Collections, Playlists, Favorites, user ratings.**
- [ ] **Live TV / DVR**, **DLNA/UPnP**, **plugins**, **SyncPlay** — stubbed or
  absent.
- [ ] **Recommendations / similar items**, **intro & credits detection**,
  genre/studio/artist *browse* facets — stubbed (return empty).
- [ ] **Multiple editions/versions** of one movie are not grouped (each file is a
  separate item).

## Layout on disk vs. in the API

Each library is a **type-tagged folder** (`movies`, `tvshows`, `music`)
declared in config. Within a library the on-disk layout is arbitrary: the
scanner walks every file, infers metadata (filename patterns for video,
embedded tags for audio), and **constructs** the Jellyfin hierarchy that the
API exposes.

## Configuration

Copy `gofin.example.yaml` to `gofin.yaml` and edit:

```yaml
server_name: gofin
listen: ":8096"
database: gofin.db
libraries:
  - name: Movies
    type: movies      # movies | tvshows | music
    path: /media/movies
```

The config path defaults to `gofin.yaml` (override with `--config` or
`GOFIN_CONFIG`).

## Usage

```sh
go build -o gofin ./cmd/gofin

./gofin migrate                                        # create/update the schema
./gofin user add --name demo --password demo --admin   # create a user
./gofin serve                                           # scan libraries and run the server
```

`serve` also runs migrations on startup, so `migrate` is only needed to
initialise the database before `user add` on a fresh install.

### Generating a large sample library

For benchmarking and load-testing, `gofin sample` writes a synthetic library
tree of empty placeholder files with realistic names and layouts — fast enough
to create tens of thousands of items:

```sh
./gofin sample --dir ./sample-large --movies 10000 --series 500 --seasons 2 --episodes-per-season 10
```

By default the files are empty placeholders — they exist to exercise scanning
and querying at scale and are not playable. Add `--real` to make the whole
library direct-play in a browser: a few base files are encoded once with ffmpeg
and every entry is symlinked to one of them (so 10k items cost a handful of
encodes, not 10k):

```sh
./gofin sample --dir ./sample-large --movies 10000 --series 500 --real
```

Point a `movies`/`tvshows`/`music` library at the generated subdirectories and
`serve`.

Point a Jellyfin client at `http://<host>:8096`, or exercise it directly:

```sh
curl http://localhost:8096/System/Info/Public
```

## Implemented endpoints

`GET /System/Info/Public`, `GET /System/Info`,
`POST /Users/AuthenticateByName`, `POST /Users/AuthenticateWithQuickConnect`,
`POST /QuickConnect/Initiate`, `GET /QuickConnect/Connect`,
`POST /QuickConnect/Authorize`, `GET /Users`, `GET /Users/Me`,
`GET /Users/{id}`, `GET /UserViews`, `GET /Items` (with `parentId`,
`recursive`, `includeItemTypes`), `GET /Items/{id}`,
`POST /Items/{id}/PlaybackInfo`, `GET /Videos|Audio/{id}/stream`,
`GET /Items/{id}/Images/{type}`, `/Sessions/Playing*` + `/UserPlayedItems/{id}`
play-state reporting, `GET /UserItems/Resume`, and assorted client-niceties
(`/Users/Public`, `/QuickConnect/Enabled`, `/DisplayPreferences/{id}`,
`/Sessions/Capabilities`, `/Sessions/Logout`).

Probing media with `ffprobe` (from ffmpeg) populates durations and
`MediaStreams`; without it the server still runs and direct-plays.

## Testing

```sh
go test ./... -race -cover
```

Unit tests cover password hashing, the authorization-header parser, filename/tag
parsing, and the item mapper. Integration tests start an `httptest` server and
drive it with the real `sj14/jellyfin-go` client to prove wire compatibility,
including a ranged stream request that asserts `206 Partial Content`.

> Coverage (`-cover`) requires a complete Go toolchain that includes the
> `covdata` tool.

### Benchmarks

Large-library benchmarks cover scanning and the hot query paths at 10k movies +
10k episodes:

```sh
go test -bench=. -benchtime=20x ./internal/scanner ./internal/server
```

`internal/scanner` benchmarks cold scans and steady-state rescans of a generated
library; `internal/server` seeds 10k movies and 10k episodes directly and drives
the real HTTP handlers (library grids, search, season/episode browsing, latest,
item-by-id). Set `GOFIN_BENCH_NOINDEX=1` to re-run the server benchmarks with
the MediaItem query indexes dropped, to quantify their effect.

### Browser verification (Playwright)

The [`e2e/`](e2e/) project drives the **bundled Jellyfin web client** against a
running gofin instance and reports every console message, page error, and gofin
network failure (4xx/5xx) — useful for catching endpoints the client needs that
gofin doesn't yet serve. It's a small modular TypeScript library (shared
auth/logging/navigation/playback helpers under `e2e/src/lib`) with two
scenarios: `crawl` (click through each library's tabs and play one item per
type) and `playback` (focused direct-play of a movie, episode, and track).

```sh
# serve gofin with web_root pointed at an extracted jellyfin-web bundle, seeded
# with scripts/gen-sample-library.sh (see e2e/README.md), then:
cd e2e
pnpm install && pnpm install:browser
pnpm crawl        # or: pnpm playback
```

See [`e2e/README.md`](e2e/README.md) for details (including the `en-US` locale
pin that works around a Chromium-on-`LANG`-less-host crash in the web client).
