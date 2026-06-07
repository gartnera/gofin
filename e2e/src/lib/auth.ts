// Authentication helpers: drive the web client's login UI, then read back the
// credentials it persisted so API discovery calls can reuse the same token.
import type { Page } from "playwright";
import type { Config } from "./config.js";
import { sleep } from "./config.js";
import type { StoredCredentials, StoredServer } from "./types.js";

/**
 * Log in through the real login UI. The login page lists known users as
 * buttons; we click ours (falling back to "Manual Login") and submit the
 * password. Throws if we're still on the login route afterwards.
 */
export async function uiLogin(page: Page, config: Config): Promise<void> {
  await page.goto(`${config.baseURL}/web/`, { waitUntil: "domcontentloaded" });
  await sleep(2500);

  const userButton = page.locator(`button:has-text("${config.user}")`).first();
  if (await userButton.count()) {
    await userButton.click().catch(() => {});
  } else {
    await page.locator('button:has-text("Manual Login")').first().click().catch(() => {});
    await page.fill("#txtManualName, input[type=text]", config.user).catch(() => {});
  }
  await sleep(1200);

  const password = page.locator("input[type=password]").first();
  await password.waitFor({ state: "visible", timeout: 10_000 });
  await password.fill(config.pass);
  await password.press("Enter");

  for (let i = 0; i < 40; i++) {
    await sleep(400);
    if (!/#\/login/.test(page.url())) break;
  }
  if (/#\/login/.test(page.url())) {
    throw new Error("login failed: still on login page after submit");
  }
}

/** Read the credentials the web client stored in localStorage post-login. */
export async function readStoredServer(page: Page): Promise<StoredServer> {
  const raw = await page.evaluate(() => localStorage.getItem("jellyfin_credentials"));
  if (!raw) throw new Error("no jellyfin_credentials in localStorage");
  const creds = JSON.parse(raw) as StoredCredentials;
  const server = creds.Servers?.[0];
  if (!server) throw new Error("no server in stored credentials");
  return server;
}

/** Authorization header that authenticates raw API requests as this user. */
export const authHeader = (server: StoredServer): Record<string, string> => ({
  "X-Emby-Token": server.AccessToken,
});
