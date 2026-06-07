// playback — focused direct-play check: log in, then open the detail page and
// play one movie, one episode, and one track, asserting each <video>/<audio>
// element actually starts. Exits 1 on any gofin API failure, page error, or
// item that fails to play.
import {
  loadConfig,
  openSession,
  ErrorCollector,
  uiLogin,
  readStoredServer,
  JellyfinApi,
  gotoDetails,
  playCurrent,
  isPlaying,
  step,
  type BaseItem,
} from "../lib/index.js";

const config = loadConfig();
const collector = new ErrorCollector(config.baseURL);
const { browser, page } = await openSession(config);
collector.attach(page);

const notPlaying: string[] = [];

async function playItem(api: JellyfinApi, item: BaseItem, label: string): Promise<void> {
  await gotoDetails(page, config.baseURL, item.Id, api.serverId);
  const media = await playCurrent(page, label);
  if (!isPlaying(media)) notPlaying.push(label);
}

try {
  step("login");
  await uiLogin(page, config);
  const api = new JellyfinApi(page, config.baseURL, await readStoredServer(page));
  console.log("logged in", api.userId);

  for (const view of await api.userViews()) {
    const ct = view.CollectionType;
    if (ct === "movies") {
      const movie = await api.firstInLibrary(view.Id, "Movie");
      if (movie) await playItem(api, movie, `Movie ${movie.Name}`);
    } else if (ct === "tvshows") {
      const series = await api.firstInLibrary(view.Id, "Series");
      const episode = series && (await api.firstEpisode(series.Id));
      if (episode) await playItem(api, episode, `Episode ${episode.Name}`);
    } else if (ct === "music") {
      // Play from the album detail page — that's where the client renders a Play
      // button (a lone track has no standalone play control); it direct-plays
      // the album's first track, exercising the audio stream path.
      const album = await api.firstInLibrary(view.Id, "MusicAlbum");
      if (album) await playItem(api, album, `Album ${album.Name}`);
    }
  }
} catch (err) {
  const message = err instanceof Error ? err.message : String(err);
  console.log("\n!!! SCRIPT ERROR:", message);
  collector.noteScriptError(message);
} finally {
  await browser.close();
}

const { apiFailures, pageErrors } = collector.report();
if (notPlaying.length) console.log(`\nitems that did not play: ${notPlaying.join(", ")}`);
process.exit(apiFailures + pageErrors + notPlaying.length > 0 ? 1 : 0);
