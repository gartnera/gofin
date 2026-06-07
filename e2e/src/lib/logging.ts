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
      const status = resp.status();
      if (status < 400) return;
      const url = resp.url();
      const line = `${resp.request().method()} ${status} ${pathOf(url)}`;
      if (this.isAsset(url)) bump(this.assetFails, line);
      else if (this.isApi(url)) bump(this.apiFails, line);
      else bump(this.externalFails, line);
    });
  }

  noteScriptError(message: string): void {
    this.pageErrors.push(`SCRIPT: ${message}`);
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

    return { apiFailures: this.apiFails.size, pageErrors: uniquePageErrors.length };
  }
}
