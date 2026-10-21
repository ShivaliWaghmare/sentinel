import { defineConfig } from "vitest/config";

// Unit tests live next to the code as src/**/*.test.ts.
// Playwright specs under tests/ are run by Playwright, never by vitest.
export default defineConfig({
  test: {
    include: ["src/**/*.test.ts"],
  },
});
