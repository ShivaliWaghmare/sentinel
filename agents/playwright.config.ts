import { defineConfig, devices } from "@playwright/test";

// The Go runner adds `--reporter=json` and sets PLAYWRIGHT_JSON_OUTPUT_NAME,
// BASE_URL and PW_OUTPUT_DIR per run (see ARCHITECTURE.md, lifecycle 1).
const workers = process.env.PW_WORKERS ? Number(process.env.PW_WORKERS) : undefined;

export default defineConfig({
  testDir: "tests",
  retries: 1,
  workers,
  reporter: "list",
  outputDir: process.env.PW_OUTPUT_DIR ?? "test-results",
  use: {
    baseURL: process.env.BASE_URL,
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
