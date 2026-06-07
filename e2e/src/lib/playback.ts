// Playback helpers: click the detail page's Play button and confirm a media
// element actually starts, then stop cleanly.
import type { Page } from "playwright";
import { sleep, step } from "./config.js";
import type { MediaState } from "./types.js";

const PLAY_SELECTORS = [
  'button[data-action="resume"]',
  "button.btnPlay",
  'button[title="Play"]',
  'button[aria-label="Play"]',
  'button:has-text("Play")',
];

const STOP_SELECTORS = ['button[data-action="stop"]', ".btnStop", 'button[title="Stop"]'];

async function clickFirstVisible(page: Page, selectors: string[]): Promise<string | null> {
  for (const selector of selectors) {
    const btn = page.locator(selector).first();
    if ((await btn.count()) && (await btn.isVisible().catch(() => false))) {
      await btn.click().catch(() => {});
      return selector;
    }
  }
  return null;
}

/** Wait (up to timeoutMs) for any of the selectors to become visible, then
 * click it. Detail pages render their Play button slightly after load, and a
 * just-dismissed player overlay can briefly cover it. */
async function clickWhenVisible(page: Page, selectors: string[], timeoutMs: number): Promise<string | null> {
  const deadline = Date.now() + timeoutMs;
  do {
    const clicked = await clickFirstVisible(page, selectors);
    if (clicked) return clicked;
    await sleep(500);
  } while (Date.now() < deadline);
  return null;
}

export async function readMediaState(page: Page): Promise<MediaState> {
  return page.evaluate(() => {
    const el = document.querySelector("video, audio") as HTMLMediaElement | null;
    if (!el) return { present: false };
    return {
      present: true,
      tag: el.tagName,
      src: (el.currentSrc || el.src || "").slice(0, 120),
      readyState: el.readyState,
      paused: el.paused,
      currentTime: Number(el.currentTime.toFixed(1)),
      error: el.error?.code ?? null,
    };
  });
}

/**
 * Start playback from the current detail page, wait for it to actually run
 * (so PlaybackInfo + stream + Sessions/Playing all complete without abort),
 * report the media state, then stop.
 */
export async function playCurrent(page: Page, label: string): Promise<MediaState> {
  step(`play: ${label}`);
  const clicked = await clickWhenVisible(page, PLAY_SELECTORS, 8000);
  if (!clicked) {
    console.log("  !! no Play button found");
    return { present: false };
  }
  console.log(`  clicked ${clicked}`);

  await sleep(7000);
  const media = await readMediaState(page);
  console.log("  media:", JSON.stringify(media));

  // Let progress report a couple times, then stop so the next item is clean.
  await sleep(3000);
  await clickFirstVisible(page, STOP_SELECTORS);
  await page.keyboard.press("Escape").catch(() => {});
  await sleep(2000);
  return media;
}

export const isPlaying = (m: MediaState): boolean =>
  m.present === true && m.paused === false && (m.currentTime ?? 0) > 0 && m.error == null;
