// pw-crawl.mjs — log in to the bundled Jellyfin web client and click around the
// library the way a normal user would (library grid + tabs, item details,
// playback), capturing every console message, page error, and gofin network
// failure. Built to surface the API errors the web client triggers — especially
// in the Movies library — so they can be fixed in gofin.
//
// Usage: node scripts/pw-crawl.mjs [baseURL] [user] [pass]
import { chromium } from 'playwright';

const BASE = process.argv[2] || 'http://localhost:8096';
const USER = process.argv[3] || 'demo';
const PASS = process.argv[4] || 'demo';

const isOurs = (u) => u.startsWith(BASE) && !u.startsWith(`${BASE}/web/`);
const isAsset = (u) => u.startsWith(`${BASE}/web/`);

const apiFails = new Map();   // "METHOD STATUS /path" -> count  (gofin API)
const assetFails = new Map(); // /web/* 404s
const consoleErrors = [];
const pageErrors = [];

const norm = (u) => { try { const x = new URL(u); return x.pathname; } catch { return u; } };

function attach(page) {
  page.on('console', (m) => {
    if (m.type() === 'error') consoleErrors.push(m.text());
  });
  page.on('pageerror', (e) => { console.log(`[pageerror] ${e.message}`); pageErrors.push(e.message); });
  page.on('requestfailed', (req) => {
    const f = req.failure();
    if (f && f.errorText === 'net::ERR_ABORTED') return; // navigation-canceled; not a server error
    const line = `${req.method()} FAIL ${norm(req.url())} (${f ? f.errorText : ''})`;
    if (isOurs(req.url())) apiFails.set(line, (apiFails.get(line) || 0) + 1);
  });
  page.on('response', (resp) => {
    const s = resp.status();
    if (s < 400) return;
    const url = resp.url();
    const line = `${resp.request().method()} ${s} ${norm(url)}`;
    if (isAsset(url)) assetFails.set(line, (assetFails.get(line) || 0) + 1);
    else if (isOurs(url)) apiFails.set(line, (apiFails.get(line) || 0) + 1);
  });
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const step = (n) => console.log(`\n=== ${n} ===`);

async function login(page) {
  await page.goto(`${BASE}/web/`, { waitUntil: 'domcontentloaded' });
  await sleep(2500);
  const ub = page.locator(`button:has-text("${USER}")`).first();
  if (await ub.count()) await ub.click().catch(() => {});
  else await page.locator('button:has-text("Manual Login")').first().click().catch(() => {});
  await sleep(1200);
  const pw = page.locator('input[type=password]').first();
  await pw.waitFor({ state: 'visible', timeout: 10000 });
  await pw.fill(PASS);
  await pw.press('Enter');
  for (let i = 0; i < 40; i++) { await sleep(400); if (!/#\/login/.test(page.url())) break; }
  if (/#\/login/.test(page.url())) throw new Error('login failed');
}

// Click every emby tab button on the current view, one at a time.
async function clickTabs(page, label) {
  const tabs = page.locator('.emby-tab-button, button[is="emby-tab-button"], .pageTabContent ~ * .emby-tab-button');
  const headerTabs = page.locator('[data-role="controlgroup"] .emby-tab-button, .headerTabs .emby-tab-button, .tabs-viewmenubar .emby-tab-button');
  const all = page.locator('.emby-tab-button');
  const n = await all.count();
  console.log(`  ${label}: ${n} tabs`);
  for (let i = 0; i < n; i++) {
    const t = all.nth(i);
    const txt = (await t.textContent().catch(() => '') || '').trim();
    await t.click().catch(() => {});
    await sleep(2500);
    console.log(`    tab[${i}] "${txt}" -> ${norm(page.url())}`);
  }
}

const browser = await chromium.launch({ headless: true });
const ctx = await browser.newContext({ baseURL: BASE, locale: 'en-US' });
const page = await ctx.newPage();
attach(page);

let H = {}, uid, serverId;
const api = async (p) => {
  const r = await page.request.get(`${BASE}${p}`, { headers: H });
  return r.ok() ? r.json() : null;
};

try {
  step('login');
  await login(page);
  const srv = JSON.parse(await page.evaluate(() => localStorage.getItem('jellyfin_credentials'))).Servers[0];
  uid = srv.UserId; serverId = srv.Id; H = { 'X-Emby-Token': srv.AccessToken };
  console.log('logged in', uid);

  step('home — let dashboard sections load');
  await page.goto(`${BASE}/web/#/home`, { waitUntil: 'domcontentloaded' });
  await sleep(5000);

  const views = (await api(`/UserViews?userId=${uid}`))?.Items || [];

  // Library route per collection type — these render the tabbed library views
  // (Movies/Suggestions/Genres/Collections, etc.) a user actually clicks
  // through, unlike the legacy list.html grid which has no tabs.
  const route = { movies: 'movies', tvshows: 'tv', music: 'music', homevideos: 'homevideos' };

  for (const v of views) {
    step(`library "${v.Name}" [${v.CollectionType}] view + tabs`);
    const r = route[v.CollectionType] || 'list.html';
    await page.goto(`${BASE}/web/#/${r}?topParentId=${v.Id}&collectionType=${v.CollectionType}`, { waitUntil: 'domcontentloaded' });
    await sleep(3500);
    await clickTabs(page, v.Name);

    // Drill into the first item and play it (movies/episodes/albums).
    if (v.CollectionType === 'movies') {
      const m = ((await api(`/Items?ParentId=${v.Id}&userId=${uid}&IncludeItemTypes=Movie&Recursive=true`))?.Items || [])[0];
      if (m) { await openDetailAndPlay(page, m, 'Movie'); }
    } else if (v.CollectionType === 'tvshows') {
      const show = ((await api(`/Items?ParentId=${v.Id}&userId=${uid}&IncludeItemTypes=Series&Recursive=true`))?.Items || [])[0];
      if (show) {
        await page.goto(`${BASE}/web/#/details?id=${show.Id}&serverId=${serverId}`, { waitUntil: 'domcontentloaded' });
        await sleep(3500);
        const ep = ((await api(`/Shows/${show.Id}/Episodes?userId=${uid}`))?.Items || [])[0];
        if (ep) await openDetailAndPlay(page, ep, 'Episode');
      }
    } else if (v.CollectionType === 'music') {
      const album = ((await api(`/Items?ParentId=${v.Id}&userId=${uid}&IncludeItemTypes=MusicAlbum&Recursive=true`))?.Items || [])[0];
      if (album) await openDetailAndPlay(page, album, 'Album');
    }
  }

  step('settle');
  await sleep(2500);
} catch (e) {
  console.log('\n!!! SCRIPT ERROR:', e.message);
  pageErrors.push('SCRIPT: ' + e.message);
} finally {
  await browser.close();
}

async function openDetailAndPlay(page, item, kind) {
  step(`detail+play ${kind}: ${item.Name}`);
  await page.goto(`${BASE}/web/#/details?id=${item.Id}&serverId=${serverId}`, { waitUntil: 'domcontentloaded' });
  await sleep(4000);
  const sels = ['button[data-action="resume"]', 'button.btnPlay', 'button[title="Play"]', 'button:has-text("Play")'];
  for (const s of sels) {
    const b = page.locator(s).first();
    if (await b.count() && await b.isVisible().catch(() => false)) {
      await b.click().catch(() => {});
      console.log('  play clicked', s);
      break;
    }
  }
  await sleep(7000); // let PlaybackInfo + stream + Sessions/Playing complete (no abort)
  const media = await page.evaluate(() => {
    const el = document.querySelector('video, audio');
    return el ? { tag: el.tag, paused: el.paused, t: +el.currentTime.toFixed(1), rs: el.readyState, err: el.error?.code } : null;
  });
  console.log('  media:', JSON.stringify(media));
  // Let progress report a couple times, then stop cleanly via the stop button.
  await sleep(3000);
  const stop = page.locator('button[data-action="stop"], .btnStop, button[title="Stop"]').first();
  if (await stop.count() && await stop.isVisible().catch(() => false)) { await stop.click().catch(() => {}); }
  await sleep(2500);
}

console.log('\n========== SUMMARY ==========');
const dump = (m, title) => {
  console.log(`${title}: ${m.size}`);
  [...m.entries()].sort().forEach(([k, c]) => console.log(`  - ${k}${c > 1 ? `  x${c}` : ''}`));
};
dump(apiFails, 'gofin API failures');
dump(assetFails, 'web asset 404s');
const ce = [...new Set(consoleErrors)].filter((e) => !/ERR_CERT_AUTHORITY_INVALID|cast_sender/.test(e));
console.log(`console errors (deduped, excl. external cast): ${ce.length}`);
ce.slice(0, 40).forEach((e) => console.log('  - ' + e.split('\n')[0]));
console.log(`page errors: ${[...new Set(pageErrors)].length}`);
[...new Set(pageErrors)].forEach((e) => console.log('  - ' + e));

process.exit(apiFails.size + [...new Set(pageErrors)].length > 0 ? 1 : 0);
