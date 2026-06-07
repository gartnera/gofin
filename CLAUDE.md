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
  hierarchy (movies / tvshows / music dispatchers).
- `internal/jellyfin` — maps ent rows to `sj14/jellyfin-go` `api.*` structs;
  IDs are emitted as 32-char dashless hex.
- `internal/server` — `http.ServeMux` handlers + MediaBrowser auth middleware.

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
