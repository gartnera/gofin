// ErrorCollector attaches to a Playwright Page and records everything that
// matters when verifying the web client against gofin: console errors, uncaught
// page errors, and network failures — split into gofin API failures (the ones
// we care about fixing), bundled web-asset 404s, and external/3rd-party misses.
import type { Page, Request, Response, WebSocket } from "playwright";

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

// SocketRecord captures what the web client's live-events WebSocket actually did
// so the crawl can assert the /socket endpoint works end-to-end (not just that
// it didn't 404): the upgrade succeeded, the server sent its ForceKeepAlive
// greeting, and the keepalive handshake round-tripped.
interface SocketRecord {
  url: string;
  received: number;
  sent: number;
  serverMessageTypes: Set<string>;
  clientMessageTypes: Set<string>;
  errored?: string;
  closed: boolean;
}

// messageType extracts the Jellyfin MessageType from a WebSocket frame payload,
// or null if the frame isn't the JSON envelope we expect.
const messageType = (payload: string | Buffer): string | null => {
  try {
    const s = typeof payload === "string" ? payload : payload.toString("utf8");
    const o = JSON.parse(s) as { MessageType?: unknown };
    return typeof o.MessageType === "string" ? o.MessageType : null;
  } catch {
    return null;
  }
};

const bump = (map: Map<string, number>, key: string): void => {
  map.set(key, (map.get(key) ?? 0) + 1);
};

export interface CollectorSummary {
  apiFailures: number;
  pageErrors: number;
  socketFailures: number;
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
  // Live-events WebSocket connections the web client opened to gofin's /socket.
  readonly sockets: SocketRecord[] = [];

  constructor(private readonly baseURL: string) {}

  private isApi(url: string): boolean {
    return url.startsWith(this.baseURL) && !url.startsWith(`${this.baseURL}/web/`);
  }
  private isAsset(url: string): boolean {
    return url.startsWith(`${this.baseURL}/web/`);
  }
  // isSocket matches a ws(s):// URL pointing at this server's /socket endpoint.
  private isSocket(url: string): boolean {
    try {
      const u = new URL(url);
      const b = new URL(this.baseURL);
      return (u.protocol === "ws:" || u.protocol === "wss:") && u.host === b.host && u.pathname === "/socket";
    } catch {
      return false;
    }
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
    // Record per-route timing on requestfinished: the ResourceTiming is only
    // fully populated once the response body is received (in the `response`
    // event responseEnd is still -1).
    page.on("requestfinished", (req: Request) => {
      const url = req.url();
      if (!this.isApi(url)) return;
      const t = req.timing();
      if (t && t.responseEnd >= 0) {
        this.recordTiming(`${req.method()} ${routeOf(url)}`, t.responseEnd);
      }
    });
    // Observe the live-events WebSocket so the crawl can validate the keepalive
    // handshake and server-pushed messages, not just that /socket didn't 404.
    page.on("websocket", (ws: WebSocket) => {
      if (!this.isSocket(ws.url())) return;
      const rec: SocketRecord = {
        url: ws.url(),
        received: 0,
        sent: 0,
        serverMessageTypes: new Set(),
        clientMessageTypes: new Set(),
        closed: false,
      };
      this.sockets.push(rec);
      ws.on("framereceived", (data) => {
        rec.received++;
        const t = messageType(data.payload);
        if (t) rec.serverMessageTypes.add(t);
      });
      ws.on("framesent", (data) => {
        rec.sent++;
        const t = messageType(data.payload);
        if (t) rec.clientMessageTypes.add(t);
      });
      ws.on("socketerror", (err) => {
        rec.errored = String(err);
      });
      ws.on("close", () => {
        rec.closed = true;
      });
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

  /** Validate the web client's live-events WebSocket(s) and print a report.
   * Returns the number of problems found (0 = healthy). Requires that the client
   * opened a /socket connection, the server greeted it with ForceKeepAlive, and
   * the socket didn't error. */
  reportSockets(): number {
    console.log(`\n========== WEBSOCKET (/socket) ==========`);
    console.log(`connections: ${this.sockets.length}`);
    if (this.sockets.length === 0) {
      console.log("  - FAIL: web client never opened a /socket WebSocket");
      return 1;
    }
    let problems = 0;
    this.sockets.forEach((s, i) => {
      const srv = [...s.serverMessageTypes].join(", ") || "(none)";
      const cli = [...s.clientMessageTypes].join(", ") || "(none)";
      console.log(`  [${i}] received=${s.received} sent=${s.sent} closed=${s.closed}`);
      console.log(`      server->client: {${srv}}`);
      console.log(`      client->server: {${cli}}`);
      if (s.errored) {
        console.log(`      FAIL: socket error: ${s.errored}`);
        problems++;
      }
      if (!s.serverMessageTypes.has("ForceKeepAlive")) {
        console.log(`      FAIL: no ForceKeepAlive received from server`);
        problems++;
      }
    });
    if (problems === 0) console.log("  OK: handshake completed");
    return problems;
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

    const socketFailures = this.reportSockets();

    this.reportSlowAPIs();

    return { apiFailures: this.apiFails.size, pageErrors: uniquePageErrors.length, socketFailures };
  }
}
