# gofin — notes for Claude

Minimal Jellyfin-compatible media server in Go.

## Architecture
- `cmd/gofin` — cobra CLI: `serve`, `migrate`, `user add`, `sample`.
- `internal/config` — YAML config (`gofin.yaml`).
- `internal/sample` — generates large synthetic libraries (realistic
  movie/episode/track names + layouts) for benchmarking and load-testing; backs
  the `gofin sample` command. Default mode writes empty placeholder files (fast,
  not playable). `--real` encodes a few browser-playable base files once via
  ffmpeg and symlinks every entry to one of them (a `placer` chooses touch vs
  symlink), so a 10k-item library direct-plays without 10k encodes — the scanner
  indexes symlinks normally and ffprobe/ServeContent follow them to the real
  bytes (see `TestScanFollowsSymlinks`). `scripts/gen-sample-library.sh` remains
  the way to make a few standalone real files without this package.
- `ent/` — ent schema + generated code (User, AccessToken, Library, MediaItem).
  Regenerate with `go generate ./ent/...` after editing `ent/schema/*`. MediaItem
  carries composite/edge indexes for the large-library query paths: a
  `(kind, sort_name, library)` index for library grids, edge-only `parent` and
  `library` indexes leading with the FK for folder browsing and scoping, and a
  `(mtime, library)` index for Latest. Note ent always orders index fields
  before edge columns, so a parent-leading composite is impossible — use an
  edge-only index where the FK must lead.
- `internal/db` — opens/migrates the SQLite (CGO `mattn/go-sqlite3`) ent client.
  The on-disk DSN sets `_journal_mode=WAL&_synchronous=NORMAL` so a large scan's
  per-row inserts don't each fsync and watcher writes don't block HTTP reads.
- `internal/auth` — bcrypt hashing + opaque token generation.
- `internal/scanner` — walks type-tagged libraries and builds the item
  hierarchy (movies / tvshows / music dispatchers); probes files via a
  pluggable `probe.Prober` (`WithProber`). Already-indexed files are skipped
  when their `mtime`+`size` are unchanged (no re-probe); `prune` drops items
  whose files vanished. Honours Jellyfin `.ignore` files (`ignore.go`): an empty
  one excludes its directory, a non-empty one applies gitignore-style patterns.
  Index mutations are serialised by a mutex so scans and the watcher don't race.
  TV episode naming (`episode.go`) is a port of Jellyfin's `Emby.Naming/TV`
  rules: an ordered regex list (transcribed from `NamingOptions.cs`) is matched
  against the full path with `github.com/dlclark/regexp2` (.NET semantics —
  needed for the lookaheads/named groups RE2 can't express). This handles flat
  shows, anime absolute numbering, multi-episode ranges (`index_number_end`),
  and date-based detection; series names are normalised (year/quality tags
  stripped) so a show's seasons merge into one Series even across directories.
  Episodes with no season default to season 1; date-only episodes are skipped.
  `fillAdditional` diverges from the verbatim Jellyfin port for performance
  (regexp2 backtracking made the multi-episode pass ~3.4ms/episode): it gates the
  multipleEpisodeExpressions behind a cheap necessary-condition check
  (`couldBeMultiEpisode`) and scopes them to the basename. Both are
  behaviour-preserving and proven so by `TestEpisodeParseMatchesUpstream`, which
  diffs the optimized parser against a verbatim copy of upstream over a corpus —
  keep that test green when touching episode parsing.
  A `ScanLibrary` builds a per-scan cache (`scanCache`): all folder rows
  (Series/Season/Artist/Album — the bounded set) are loaded once and reused, and
  each directory's existing playable rows are fetched in one batched query by
  `walk` (which now indexes a directory's files before recursing). This avoids a
  lookup-or-create query per file while keeping resident memory bounded by the
  largest single directory, not the library size. The cache is only set for the
  duration of a scan (under `mu`); the watcher's single-file `Index` path leaves
  it nil and queries directly.
  Probing dominates a real-media scan (~40ms/file serially, since each file is an
  out-of-process ffprobe exec). `walk` processes each directory in chunks and
  `prefetchProbes` runs the chunk's probes concurrently (a worker pool sized to
  `runtime.NumCPU()`, overridable via `GOFIN_SCAN_PROBE_WORKERS`) into
  `scanCache.probeCache`, which `probeFile` then serves during the serial index
  pass — DB writes and the folder cache stay single-threaded. On 4 cores a
  400-file real scan drops from ~16s to ~4s. Symlinked media files are followed
  normally (the entry is indexed; ffprobe and ServeContent resolve to the
  target), which is how `gofin sample --real` builds large playable libraries
  from a few base files.
- `internal/watch` — `fsnotify` watcher that keeps the index live: new/modified
  files are indexed (debounced) and removals are dropped. Started by `serve`.
- `internal/probe` — `ffprobe`-backed media probing behind a `Prober`
  interface (with `Noop` fallback); JSON parsing is unit-tested separately.
- `internal/nfo` — parses local Kodi/Jellyfin `.nfo` sidecar metadata
  (overview, genres, studios, cast/crew, ratings, premiere date) into an `Info`
  struct (root-agnostic XML, so `<movie>`/`<episodedetails>`/`<tvshow>`/
  `<season>`/`artist`/`album` all decode). Lookup is layered: a `<name>.nfo`
  sidecar wins over a generic `movie.nfo`/`tvshow.nfo`/`season.nfo`/`album.nfo`/
  `artist.nfo`, and generic/parent-directory files are only consulted when the
  media file lives *below* the library root (`belowRoot` guard) — so a stray
  `.nfo` sitting directly in a library root is never attached to a bare
  top-level file. NFO files are never indexed as media themselves (they don't
  match the audio/video extensions), so the scanner and watcher ignore them;
  an NFO is read only as a side effect of indexing the media file it describes.
  The scanner's `applyNFO` overlays the parsed metadata onto the item row after
  its filename/probe-derived fields are set (folders like Series/Season/Album/
  Artist are enriched only while still bare). It honours metadata locks
  (`metaLocked`): a locked field — or a fully locked item — is never overwritten
  by an NFO, just as it isn't by the filename/probe pass.
- `internal/jellyfin` — maps ent rows to `sj14/jellyfin-go` `api.*` structs;
  IDs are emitted as 32-char dashless hex; builds `UserData` and `MediaStreams`.
- `internal/server` — `http.ServeMux` handlers + MediaBrowser auth middleware;
  play state lives in `playstate.go`, client-nicety stubs in `extras.go`.
  Admin-only scan endpoints in `library.go`: `POST /Library/Refresh` (background
  rescan of all libraries) and `POST /Items/{itemId}/Refresh` (re-probe one
  item), gated by `requireAdmin`. Metadata editing in `metadata.go`:
  `POST /Items/{itemId}` (admin) accepts a `BaseItemDto` and persists the fields
  gofin stores (name/sort/overview/year/index numbers) plus Jellyfin's
  `LockData`/`LockedFields`. Locked items/fields are preserved by the scanner
  (see `metaLocked` in `internal/scanner`) so rescans don't clobber edits.

## Conventions
- Response bodies reuse `github.com/sj14/jellyfin-go/api` model structs so JSON
  field names stay Jellyfin-exact.
- Item IDs on the wire are dashless hex (`jellyfin.FormatID`); parse with
  `jellyfin.ParseID`.
- Direct play only — no transcoding. Streaming uses `http.ServeContent` for
  range/seek support.
- README.md has a "What's supported" / "Not supported" checklist that is the
  user-facing source of truth for capabilities. Whenever you add, remove, or
  change a feature (a new endpoint, media/library type, metadata source,
  un-stubbing something, etc.), update those checkboxes in the same change so the
  README stays accurate — move items between the two lists or add new ones as
  needed.

## Testing
- `go test ./... -race -cover`
- Integration tests (`internal/server`) drive the running server with the real
  `sj14/jellyfin-go` client. `-cover` needs a complete toolchain (`covdata`).
- Benchmarks: `go test -bench=. ./internal/scanner ./internal/server`. Scanner
  benches cold-scan/rescan a generated 10k library; server benches seed 10k
  movies + 10k episodes and hit the real handlers. `GOFIN_BENCH_NOINDEX=1`
  re-runs the server benches with the query indexes dropped (A/B for index
  effect). The e2e `crawl` also prints a SLOW API ROUTES report from the real
  web client (`e2e/src/lib/logging.ts`).

## Driving the real web client (finding slow APIs at scale)

Exercising the bundled Jellyfin web client against a large library is how the
slow-API regressions (e.g. unknown `IncludeItemTypes` triggering a full-library
scan) get caught. End-to-end recipe:

1. Build jellyfin-web from the latest release, **development (non-minified)**
   build so console/page errors and the SLOW API report are readable:
   ```sh
   tag=$(git ls-remote --tags --refs https://github.com/jellyfin/jellyfin-web.git \
     | awk -F/ '{print $NF}' | grep -E '^v[0-9.]+$' | sort -V | tail -1)
   git clone --depth 1 --branch "$tag" https://github.com/jellyfin/jellyfin-web.git /tmp/jellyfin-web
   cd /tmp/jellyfin-web && npm ci && npm run build:development   # output in dist/
   ```
   (Use the dev build, not `build:production`: production is minified and
   mangles stack traces.)
2. Generate a large library and point gofin at it with `web_root` set to the
   `dist/`:
   ```sh
   go build -o /tmp/gofin ./cmd/gofin
   /tmp/gofin sample --dir /tmp/media --movies 10000 --series 500 \
     --seasons 2 --episodes-per-season 10 --artists 300 --albums-per-artist 3 --tracks-per-album 10
   # gofin.yaml: web_root: /tmp/jellyfin-web/dist + the three library paths
   /tmp/gofin --config gofin.yaml migrate
   /tmp/gofin --config gofin.yaml user add --name demo --password demo --admin
   /tmp/gofin --config gofin.yaml serve      # scans ~29k items on startup
   ```
3. Run the crawl (Chromium launches headless; in this container no
   `--no-sandbox` is needed). `PLAYWRIGHT_BROWSERS_PATH` is preset:
   ```sh
   cd e2e && pnpm install && pnpm install:browser
   pnpm crawl http://127.0.0.1:8096 demo demo
   ```
   The closing **SLOW API ROUTES** section ranks the gofin endpoints the client
   hit by max/mean latency (ids collapsed to `:id`). Placeholder media from
   `gofin sample` isn't playable, so "no Play button found" and a few client
   page errors are expected; the crawl gates only on gofin API failures.
