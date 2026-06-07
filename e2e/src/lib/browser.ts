// Browser/context lifecycle helpers. The locale pin is load-bearing: a host
// with no LANG set makes Chromium report navigator.language as "en-US@posix",
// which the web client feeds straight into toLocaleString and crashes on
// (RangeError: Invalid language tag). Real browsers are unaffected.
import { chromium, type Browser, type BrowserContext, type Page } from "playwright";
import type { Config } from "./config.js";

export interface Session {
  browser: Browser;
  context: BrowserContext;
  page: Page;
}

export async function openSession(config: Config): Promise<Session> {
  const browser = await chromium.launch({ headless: config.headless });
  const context = await browser.newContext({ baseURL: config.baseURL, locale: "en-US" });
  const page = await context.newPage();
  return { browser, context, page };
}
