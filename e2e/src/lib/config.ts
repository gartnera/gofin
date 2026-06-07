// Runtime configuration for the e2e scenarios, resolved from CLI args then
// environment, with sensible local-dev defaults.
//
//   tsx src/scenarios/crawl.ts [baseURL] [user] [pass]
//   GOFIN_URL=... GOFIN_USER=... GOFIN_PASS=... tsx src/scenarios/crawl.ts

export interface Config {
  baseURL: string;
  user: string;
  pass: string;
  /** Slow everything down for debugging when set. */
  headless: boolean;
}

export function loadConfig(argv: string[] = process.argv.slice(2)): Config {
  return {
    baseURL: argv[0] ?? process.env.GOFIN_URL ?? "http://localhost:8096",
    user: argv[1] ?? process.env.GOFIN_USER ?? "demo",
    pass: argv[2] ?? process.env.GOFIN_PASS ?? "demo",
    headless: process.env.GOFIN_HEADFUL !== "1",
  };
}

export const sleep = (ms: number): Promise<void> =>
  new Promise((resolve) => setTimeout(resolve, ms));

export const step = (name: string): void => console.log(`\n=== ${name} ===`);
