// Navigation helpers for the web client's hash-routed SPA.
import type { Page } from "playwright";
import { sleep } from "./config.js";

/**
 * Library route per collection type. These render the tabbed library views
 * (Movies / Suggestions / Genres / Collections, …) a user actually clicks
 * through — unlike the legacy list.html grid, which has no tabs and so never
 * exercises the per-tab endpoints.
 */
const LIBRARY_ROUTE: Record<string, string> = {
  movies: "movies",
  tvshows: "tv",
  music: "music",
  homevideos: "homevideos",
};

export function libraryRoute(collectionType: string | undefined): string {
  return (collectionType && LIBRARY_ROUTE[collectionType]) || "list.html";
}

export async function gotoHash(page: Page, baseURL: string, hash: string, settleMs = 3500): Promise<void> {
  await page.goto(`${baseURL}/web/#/${hash}`, { waitUntil: "domcontentloaded" });
  await sleep(settleMs);
}

function libraryHash(parentId: string, collectionType: string | undefined, tab?: number): string {
  const route = libraryRoute(collectionType);
  const tabSuffix = tab === undefined ? "" : `&tab=${tab}`;
  return `${route}?topParentId=${parentId}&collectionType=${collectionType}${tabSuffix}`;
}

export async function gotoLibrary(
  page: Page,
  baseURL: string,
  parentId: string,
  collectionType: string | undefined,
): Promise<void> {
  await gotoHash(page, baseURL, libraryHash(parentId, collectionType));
}

export async function gotoDetails(page: Page, baseURL: string, id: string, serverId: string): Promise<void> {
  await gotoHash(page, baseURL, `details?id=${id}&serverId=${serverId}`, 4000);
}

/**
 * Click each library tab in turn so every per-tab fetch fires the way a user
 * triggers it. Switching views rapidly can trip a client-side view-disposal
 * race (an async init calling addEventListener on an element the outgoing view
 * already removed); that's a web-client bug independent of gofin, so the crawl
 * surfaces it as a warning rather than gating on it — its signal is gofin API
 * failures.
 */
export async function clickAllTabs(page: Page, label: string): Promise<void> {
  const tabs = page.locator(".emby-tab-button");
  const count = await tabs.count();
  console.log(`  ${label}: ${count} tabs`);
  for (let i = 0; i < count; i++) {
    const tab = tabs.nth(i);
    const text = ((await tab.textContent().catch(() => "")) ?? "").trim();
    await tab.click().catch(() => {});
    await sleep(2500);
    console.log(`    tab[${i}] "${text}"`);
  }
}
