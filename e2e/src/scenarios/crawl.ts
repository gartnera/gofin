// crawl — log in and click through every library's tabbed view (Movies /
// Suggestions / Genres / Collections / TV Networks / Artists, …) the way a
// normal user would, then open and play one item per library. Surfaces the
// gofin API errors the web client triggers while browsing (exit 1 on any).
import {
  loadConfig,
  openSession,
  ErrorCollector,
  uiLogin,
  readStoredServer,
  JellyfinApi,
  gotoLibrary,
  gotoDetails,
  clickAllTabs,
  playCurrent,
  sleep,
  step,
} from "../lib/index.js";

const config = loadConfig();
const collector = new ErrorCollector(config.baseURL);
const { browser, page } = await openSession(config);
collector.attach(page);

try {
  step("login");
  await uiLogin(page, config);
  const api = new JellyfinApi(page, config.baseURL, await readStoredServer(page));
  console.log("logged in", api.userId);

  step("home — let dashboard sections load");
  await page.goto(`${config.baseURL}/web/#/home`, { waitUntil: "domcontentloaded" });
  await sleep(5000);

  for (const view of await api.userViews()) {
    const ct = view.CollectionType;
    step(`library "${view.Name}" [${ct}] view + tabs`);
    await gotoLibrary(page, config.baseURL, view.Id, ct);
    await clickAllTabs(page, view.Name);

    if (ct === "movies") {
      const movie = await api.firstInLibrary(view.Id, "Movie");
      if (movie) {
        await gotoDetails(page, config.baseURL, movie.Id, api.serverId);
        await playCurrent(page, `Movie ${movie.Name}`);
      }
    } else if (ct === "tvshows") {
      const series = await api.firstInLibrary(view.Id, "Series");
      if (series) {
        await gotoDetails(page, config.baseURL, series.Id, api.serverId);
        const episode = await api.firstEpisode(series.Id);
        if (episode) {
          await gotoDetails(page, config.baseURL, episode.Id, api.serverId);
          await playCurrent(page, `Episode ${episode.Name}`);
        }
      }
    } else if (ct === "music") {
      const album = await api.firstInLibrary(view.Id, "MusicAlbum");
      if (album) {
        await gotoDetails(page, config.baseURL, album.Id, api.serverId);
        await playCurrent(page, `Album ${album.Name}`);
      }
    }
  }

  step("settle");
  await sleep(2500);
} catch (err) {
  const message = err instanceof Error ? err.message : String(err);
  console.log("\n!!! SCRIPT ERROR:", message);
  collector.noteScriptError(message);
} finally {
  await browser.close();
}

const { apiFailures, pageErrors, socketFailures } = collector.report();
// The crawl's pass/fail signal is gofin API health and the live-events socket.
// Web-client page errors are printed above for visibility but don't gate the
// run: the broad tab-switching this scenario does can trip client-side
// view-transition races that reproduce against any backend. The focused
// `playback` scenario treats page errors as fatal, where they're deterministic
// and meaningful.
if (pageErrors > 0) console.log(`\nnote: ${pageErrors} web-client page error(s) above (not gating; see comment)`);
if (socketFailures > 0) console.log(`\nFAIL: ${socketFailures} WebSocket problem(s) above`);
process.exit(apiFailures > 0 || socketFailures > 0 ? 1 : 0);
