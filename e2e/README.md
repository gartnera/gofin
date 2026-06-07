# gofin e2e

Playwright scripts that drive the **bundled Jellyfin web client** against a
running gofin instance to verify browse + playback and surface any fetch errors
(console errors, uncaught page errors, and gofin 4xx/5xx responses).

## Layout

```
src/
  lib/            shared, modular helpers
    config.ts       CLI/env config + small utilities
    types.ts        partial Jellyfin DTO shapes used here
    logging.ts      ErrorCollector: console/page/network capture + summary
    browser.ts      launch + context (pins locale; see note below)
    auth.ts         UI login + reading the stored token
    jellyfin.ts     thin authed API client for id discovery
    navigation.ts   hash routing, library routes, tab clicking
    playback.ts     click Play, confirm a media element starts, stop
  scenarios/
    crawl.ts        click every library tab + play one item per library
    playback.ts     focused direct-play of a movie, episode, and track
```

## Prerequisites

A gofin server running with `web_root` pointed at an extracted `jellyfin-web`
bundle, and a scanned sample library (see `scripts/gen-sample-library.sh` and
the repo README). Then:

```sh
cd e2e
pnpm install
pnpm install:browser   # downloads the Chromium build Playwright needs
```

## Run

```sh
pnpm crawl        # full browse + playback crawl
pnpm playback     # focused direct-play check
pnpm typecheck    # tsc --noEmit

# override target / credentials (defaults: http://localhost:8096, demo/demo)
pnpm crawl http://localhost:8096 demo demo
# or via env: GOFIN_URL / GOFIN_USER / GOFIN_PASS
```

Each scenario exits non-zero if it sees a gofin API failure, an uncaught page
error, or (for `playback`) an item that fails to start.

> **Locale note:** the browser context is pinned to `en-US`. A host with no
> `LANG` set makes Chromium report `navigator.language` as `en-US@posix`, which
> the web client feeds into `toLocaleString` and crashes on (`RangeError:
> Invalid language tag`). Real browsers are unaffected.
