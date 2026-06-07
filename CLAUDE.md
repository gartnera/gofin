# gofin — notes for Claude

Minimal Jellyfin-compatible media server in Go.

## Architecture
- `cmd/gofin` — cobra CLI: `serve`, `scan`, `user add`.
- `internal/config` — YAML config (`gofin.yaml`).
- `ent/` — ent schema + generated code (User, AccessToken, Library, MediaItem).
  Regenerate with `go generate ./ent/...` after editing `ent/schema/*`.
- `internal/db` — opens/migrates the SQLite (CGO `mattn/go-sqlite3`) ent client.
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
- `internal/watch` — `fsnotify` watcher that keeps the index live: new/modified
  files are indexed (debounced) and removals are dropped. Started by `serve`.
- `internal/probe` — `ffprobe`-backed media probing behind a `Prober`
  interface (with `Noop` fallback); JSON parsing is unit-tested separately.
- `internal/jellyfin` — maps ent rows to `sj14/jellyfin-go` `api.*` structs;
  IDs are emitted as 32-char dashless hex; builds `UserData` and `MediaStreams`.
- `internal/server` — `http.ServeMux` handlers + MediaBrowser auth middleware;
  play state lives in `playstate.go`, client-nicety stubs in `extras.go`.
  Admin-only scan endpoints in `library.go`: `POST /Library/Refresh` (background
  rescan of all libraries) and `POST /Items/{itemId}/Refresh` (re-probe one
  item), gated by `requireAdmin`.

## Conventions
- Response bodies reuse `github.com/sj14/jellyfin-go/api` model structs so JSON
  field names stay Jellyfin-exact.
- Item IDs on the wire are dashless hex (`jellyfin.FormatID`); parse with
  `jellyfin.ParseID`.
- Direct play only — no transcoding. Streaming uses `http.ServeContent` for
  range/seek support.

## Testing
- `go test ./... -race -cover`
- Integration tests (`internal/server`) drive the running server with the real
  `sj14/jellyfin-go` client. `-cover` needs a complete toolchain (`covdata`).
