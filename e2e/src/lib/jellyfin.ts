// Thin authenticated wrapper over the gofin/Jellyfin REST API, used only to
// discover item ids so the scenarios can navigate the UI to them. UI-triggered
// fetches (the ones we're verifying) go through the browser, not this client.
import type { Page } from "playwright";
import type { StoredServer } from "./types.js";
import { authHeader } from "./auth.js";
import type { BaseItem, QueryResult } from "./types.js";

export class JellyfinApi {
  private readonly headers: Record<string, string>;

  constructor(
    private readonly page: Page,
    private readonly baseURL: string,
    readonly server: StoredServer,
  ) {
    this.headers = authHeader(server);
  }

  get userId(): string {
    return this.server.UserId;
  }
  get serverId(): string {
    return this.server.Id;
  }

  /** GET a path and parse JSON, or return null on a non-2xx response. */
  async get<T>(path: string): Promise<T | null> {
    const resp = await this.page.request.get(`${this.baseURL}${path}`, { headers: this.headers });
    if (!resp.ok()) {
      console.log(`  ! api ${path} -> ${resp.status()}`);
      return null;
    }
    return (await resp.json()) as T;
  }

  async userViews(): Promise<BaseItem[]> {
    const res = await this.get<QueryResult>(`/UserViews?userId=${this.userId}`);
    return res?.Items ?? [];
  }

  /** First item of a kind within a library (recursive). */
  async firstInLibrary(parentId: string, includeItemType: string): Promise<BaseItem | undefined> {
    const res = await this.get<QueryResult>(
      `/Items?ParentId=${parentId}&userId=${this.userId}&IncludeItemTypes=${includeItemType}&Recursive=true`,
    );
    return res?.Items?.[0];
  }

  async firstEpisode(seriesId: string): Promise<BaseItem | undefined> {
    const res = await this.get<QueryResult>(`/Shows/${seriesId}/Episodes?userId=${this.userId}`);
    return res?.Items?.[0];
  }
}
