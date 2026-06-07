// pw-playback.mjs — drive the bundled Jellyfin web client against gofin through
// the real UI (login, browse, play) and log every console message, page error,
// and network response so fetch errors during normal (non-admin) user playback
// scenarios surface clearly.
//
// Usage: node scripts/pw-playback.mjs [baseURL] [user] [pass]
import { chromium } from 'playwright';

const BASE = process.argv[2] || 'http://localhost:8096';
const USER = process.argv[3] || 'demo';
const PASS = process.argv[4] || 'demo';

const failures = [];      // gofin (same-origin) network 4xx/5xx + failed requests
const externalFails = []; // 3rd-party (gstatic cast, etc.) — informational only
const consoleErrors = [];
const pageErrors = [];

const isOurs = (url) => url.startsWith(BASE);

function attachLogging(page) {
  page.on('console', (msg) => {
    const type = msg.type();
    if (type === 'error') { console.log(`[console.error] ${msg.text()}`); consoleErrors.push(msg.text()); }
    else if (type === 'warning') { console.log(`[console.warn] ${msg.text()}`); }
  });
  page.on('pageerror', (err) => { console.log(`[pageerror] ${err.message}`); pageErrors.push(err.message); });
  page.on('requestfailed', (req) => {
    const f = req.failure();
    const line = `${req.method()} ${req.url()} -> FAILED ${f ? f.errorText : ''}`;
    console.log(`[requestfailed] ${line}`);
    (isOurs(req.url()) ? failures : externalFails).push(line);
  });
  page.on('response', (resp) => {
    const status = resp.status();
    if (status >= 400) {
      const line = `${resp.request().method()} ${status} ${resp.url()}`;
      console.log(`[http>=400] ${line}`);
      (isOurs(resp.url()) ? failures : externalFails).push(line);
    }
  });
}

const step = (n) => console.log(`\n=== STEP: ${n} ===`);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function uiLogin(page) {
  await page.goto(`${BASE}/web/`, { waitUntil: 'domcontentloaded' });
  await sleep(3000);
  // Login page lists users as buttons; click ours (falls back to Manual Login).
  const userBtn = page.locator(`button:has-text("${USER}")`).first();
  if (await userBtn.count()) {
    await userBtn.click().catch(() => {});
  } else {
    await page.locator('button:has-text("Manual Login")').first().click().catch(() => {});
    await page.fill('#txtManualName, input[type=text]', USER).catch(() => {});
  }
  await sleep(1500);
  const pw = page.locator('input[type=password]').first();
  await pw.waitFor({ state: 'visible', timeout: 10000 });
  await pw.fill(PASS);
  // Submit: press Enter in the password field or click the sign-in button.
  await pw.press('Enter').catch(() => {});
  // Wait until we leave the login route.
  for (let i = 0; i < 40; i++) {
    await sleep(500);
    if (!/#\/login/.test(page.url())) break;
  }
  console.log('post-login url:', page.url());
  if (/#\/login/.test(page.url())) throw new Error('still on login page after submit');
}

async function gotoDetails(page, id, serverId, label) {
  step(`details: ${label}`);
  await page.goto(`${BASE}/web/#/details?id=${id}&serverId=${serverId}`, { waitUntil: 'domcontentloaded' });
  await sleep(3500); // let the detail page fire its XHRs (item, playbackinfo prep, similar, etc.)
  console.log('  url:', page.url());
}

// Click the first visible Play button on the current detail page and let the
// player start, capturing the PlaybackInfo + stream requests the client makes.
async function uiPlay(page, label) {
  step(`play (UI): ${label}`);
  const selectors = [
    'button[data-action="resume"]',
    'button.btnPlay',
    'button[title="Play"]',
    'button[aria-label="Play"]',
    '.detailButton-content:has-text("Play")',
    'button:has-text("Play")',
  ];
  let clicked = false;
  for (const sel of selectors) {
    const btn = page.locator(sel).first();
    if (await btn.count() && await btn.isVisible().catch(() => false)) {
      await btn.click().catch(() => {});
      clicked = true;
      console.log('  clicked', sel);
      break;
    }
  }
  if (!clicked) { console.log('  !! no Play button found'); return; }
  await sleep(6000); // playback start: PlaybackInfo + /stream range requests
  // Report whether a media element is actually playing.
  const media = await page.evaluate(() => {
    const el = document.querySelector('video, audio');
    if (!el) return { present: false };
    return { present: true, tag: el.tagName, src: (el.currentSrc || el.src || '').slice(0, 120),
             readyState: el.readyState, paused: el.paused, currentTime: el.currentTime, error: el.error && el.error.code };
  });
  console.log('  media:', JSON.stringify(media));
  // Stop playback so the next item starts clean.
  await page.keyboard.press('Escape').catch(() => {});
  await sleep(1500);
}

const browser = await chromium.launch({ headless: true });
// This container sets no LANG, so chromium otherwise reports navigator.language
// as "en-US@posix", which the web client feeds to toLocaleString and crashes on
// (RangeError: Invalid language tag). Pin a valid locale like a real browser.
const ctx = await browser.newContext({ baseURL: BASE, locale: 'en-US' });
const page = await ctx.newPage();
attachLogging(page);

// API helper (authenticated) to discover item IDs to navigate the UI to.
let H = {};
const getJSON = async (path) => {
  const r = await page.request.get(`${BASE}${path}`, { headers: H });
  if (!r.ok()) { console.log(`  ! api ${path} -> ${r.status()}`); return null; }
  return r.json();
};

try {
  step('UI login');
  await uiLogin(page);

  // Pull the token the client stored so our discovery API calls are authed.
  const creds = await page.evaluate(() => localStorage.getItem('jellyfin_credentials'));
  const srv = JSON.parse(creds).Servers[0];
  const uid = srv.UserId, serverId = srv.Id;
  H = { 'X-Emby-Authorization': `MediaBrowser Token="${srv.AccessToken}"` };
  console.log(`logged in: user ${uid}, server ${serverId}`);

  step('home');
  await page.goto(`${BASE}/web/#/home`, { waitUntil: 'domcontentloaded' });
  await sleep(4000);

  const views = await getJSON(`/UserViews?userId=${uid}`);
  console.log('  views:', (views?.Items || []).map((v) => `${v.Name}[${v.CollectionType}]`).join(', '));

  for (const v of views?.Items || []) {
    const ct = v.CollectionType;
    // Visit the library grid (fires /Items list fetch the way the user would).
    step(`library: ${v.Name}`);
    await page.goto(`${BASE}/web/#/list.html?parentId=${v.Id}&serverId=${serverId}`, { waitUntil: 'domcontentloaded' });
    await sleep(3000);

    if (ct === 'movies') {
      const items = await getJSON(`/Items?ParentId=${v.Id}&userId=${uid}&IncludeItemTypes=Movie&Recursive=true`);
      const m = (items?.Items || [])[0];
      if (m) { await gotoDetails(page, m.Id, serverId, `Movie ${m.Name}`); await uiPlay(page, m.Name); }
    } else if (ct === 'tvshows') {
      const shows = await getJSON(`/Items?ParentId=${v.Id}&userId=${uid}&IncludeItemTypes=Series&Recursive=true`);
      const show = (shows?.Items || [])[0];
      if (show) {
        await gotoDetails(page, show.Id, serverId, `Series ${show.Name}`);
        const eps = await getJSON(`/Shows/${show.Id}/Episodes?userId=${uid}`);
        const ep = (eps?.Items || [])[0];
        if (ep) { await gotoDetails(page, ep.Id, serverId, `Episode ${ep.Name}`); await uiPlay(page, ep.Name); }
      }
    } else if (ct === 'music') {
      const albums = await getJSON(`/Items?ParentId=${v.Id}&userId=${uid}&IncludeItemTypes=MusicAlbum&Recursive=true`);
      const album = (albums?.Items || [])[0];
      if (album) {
        await gotoDetails(page, album.Id, serverId, `Album ${album.Name}`);
        await uiPlay(page, `album ${album.Name}`);
      }
    }
  }

  step('settle');
  await sleep(2500);
} catch (e) {
  console.log('\n!!! SCRIPT ERROR:', e.message);
  pageErrors.push('SCRIPT: ' + e.message);
} finally {
  await page.screenshot({ path: '/tmp/pw-final.png', fullPage: true }).catch(() => {});
  await browser.close();
}

console.log('\n========== SUMMARY ==========');
console.log(`gofin network failures (>=400 / failed): ${failures.length}`);
for (const f of [...new Set(failures)]) console.log('  - ' + f);
console.log(`console errors: ${consoleErrors.length}`);
for (const e of [...new Set(consoleErrors)].slice(0, 60)) console.log('  - ' + e);
console.log(`page errors: ${pageErrors.length}`);
for (const e of [...new Set(pageErrors)]) console.log('  - ' + e);
console.log(`(external/non-gofin failures, informational): ${[...new Set(externalFails)].length}`);
for (const e of [...new Set(externalFails)]) console.log('  · ' + e);

process.exit(failures.length + pageErrors.length > 0 ? 1 : 0);
