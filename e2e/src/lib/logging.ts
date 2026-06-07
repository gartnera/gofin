// ErrorCollector attaches to a Playwright Page and records everything that
// matters when verifying the web client against gofin: console errors, uncaught
// page errors, and network failures — split into gofin API failures (the ones
// we care about fixing), bundled web-asset 404s, and external/3rd-party misses.
import type { Page, Request, Response } from "playwright";

const pathOf = (url: string): string => {
  try {
    return new URL(url).pathname;
  } catch {
    return url;
  }
};

// routeOf collapses an API path into a stable route key by replacing the
// variable id segments (32-char dashless hex or UUIDs, and numeric ids) with
// ":id", so per-item requests aggregate into one timing bucket.
const routeOf = (url: string): string =>
  pathOf(url)
    .replace(/\/[0-9a-fA-F]{32}(?=\/|$)/g, "/:id")
    .replace(/\/[0-9a-fA-F-]{36}(?=\/|$)/g, "/:id")
    .replace(/\/\d+(?=\/|$)/g, "/:id");

interface Timing {
  count: number;
  total: number;
  max: number;
}

const bump = (map: Map<string, number>, key: string): void => {
  map.set(key, (map.get(key) ?? 0) + 1);
};

export interface CollectorSummary {
  apiFailures: number;
  pageErrors: number;
}

export class ErrorCollector {
  readonly apiFails = new Map<string, number>();
  readonly assetFails = new Map<string, number>();
  readonly externalFails = new Map<string, number>();
  readonly consoleErrors: string[] = [];
  readonly pageErrors: string[] = [];
  // Per-route response timings for successful gofin API calls, so the crawl
  // surfaces slow endpoints (not just failing ones).
  readonly apiTimings = new Map<string, Timing>();

  constructor(private readonly baseURL: string) {}

  private isApi(url: string): boolean {
    return url.startsWith(this.baseURL) && !url.startsWith(`${this.baseURL}/web/`);
  }
  private isAsset(url: string): boolean {
    return url.startsWith(`${this.baseURL}/web/`);
  }

  /** Wire up the page event listeners. Call once per page. */
  attach(page: Page): void {
    page.on("console", (msg) => {
      if (msg.type() === "error") this.consoleErrors.push(msg.text());
    });
    page.on("pageerror", (err) => {
      console.log(`[pageerror] ${err.message}`);
      this.pageErrors.push(err.message);
    });
    page.on("requestfailed", (req: Request) => {
      const failure = req.failure();
      // ERR_ABORTED means the request was canceled by navigation (e.g. we moved
      // on mid-playback) — not a server error, so don't count it.
      if (failure?.errorText === "net::ERR_ABORTED") return;
      const line = `${req.method()} FAIL ${pathOf(req.url())} (${failure?.errorText ?? ""})`;
      if (this.isApi(req.url())) bump(this.apiFails, line);
      else if (!this.isAsset(req.url())) bump(this.externalFails, line);
    });
    page.on("response", (resp: Response) => {
      const url = resp.url();
      const status = resp.status();
      // Record timing for successful gofin API responses.
      if (status < 400 && this.isApi(url)) {
        const t = resp.request().timing();
        // responseEnd is ms from request start to the last response byte; -1
        // when the browser couldn't measure it (e.g. served from cache).
        if (t && t.responseEnd >= 0) {
          this.recordTiming(`${resp.request().method()} ${routeOf(url)}`, t.responseEnd);
        }
      }
      if (status < 400) return;
      const line = `${resp.request().method()} ${status} ${pathOf(url)}`;
      if (this.isAsset(url)) bump(this.assetFails, line);
      else if (this.isApi(url)) bump(this.apiFails, line);
      else bump(this.externalFails, line);
    });
  }

  noteScriptError(message: string): void {
    this.pageErrors.push(`SCRIPT: ${message}`);
  }

  private recordTiming(route: string, ms: number): void {
    const cur = this.apiTimings.get(route) ?? { count: 0, total: 0, max: 0 };
    cur.count += 1;
    cur.total += ms;
    cur.max = Math.max(cur.max, ms);
    this.apiTimings.set(route, cur);
  }

  /** Print the slowest gofin API routes the web client exercised, by max and
   * by mean latency. */
  reportSlowAPIs(topN = 15): void {
    if (this.apiTimings.size === 0) return;
    const rows = [...this.apiTimings.entries()].map(([route, t]) => ({
      route,
      count: t.count,
      avg: t.total / t.count,
      max: t.max,
    }));
    console.log("\n========== SLOW API ROUTES (web client) ==========");
    console.log("by max latency:");
    [...rows]
      .sort((a, b) => b.max - a.max)
      .slice(0, topN)
      .forEach((r) => console.log(`  ${r.max.toFixed(0).padStart(6)}ms max  ${r.avg.toFixed(0).padStart(6)}ms avg  x${r.count}  ${r.route}`));
  }

  /** Print a human-readable summary and return the hard-failure tallies. */
  report(): CollectorSummary {
    const dump = (map: Map<string, number>, title: string) => {
      console.log(`${title}: ${map.size}`);
      [...map.entries()].sort().forEach(([k, c]) => console.log(`  - ${k}${c > 1 ? `  x${c}` : ""}`));
    };
    console.log("\n========== SUMMARY ==========");
    dump(this.apiFails, "gofin API failures");
    dump(this.assetFails, "web asset 404s");

    const cleanConsole = [...new Set(this.consoleErrors)].filter(
      (e) => !/ERR_CERT_AUTHORITY_INVALID|cast_sender/.test(e),
    );
    console.log(`console errors (deduped, excl. external cast): ${cleanConsole.length}`);
    cleanConsole.slice(0, 40).forEach((e) => console.log("  - " + e.split("\n")[0]));

    const uniquePageErrors = [...new Set(this.pageErrors)];
    console.log(`page errors: ${uniquePageErrors.length}`);
    uniquePageErrors.forEach((e) => console.log("  - " + e));

    if (this.externalFails.size) dump(this.externalFails, "external (informational)");

    this.reportSlowAPIs();

    return { apiFailures: this.apiFails.size, pageErrors: uniquePageErrors.length };
  }
}
