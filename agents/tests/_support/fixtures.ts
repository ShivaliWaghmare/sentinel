// Shared Playwright fixture. Every suite imports test and expect from here,
// never from @playwright/test directly (CLAUDE.md).
//
// When a test fails it attaches the page URL (url.txt) and an aria snapshot
// of the page (aria-snapshot.txt). Triage and the Healer read these instead of
// guessing what the page looked like.
import { test as base, expect } from "@playwright/test";

export const test = base.extend<{ failureContext: void }>({
  failureContext: [
    async ({ page }, use, testInfo) => {
      await use();
      if (testInfo.status === testInfo.expectedStatus) return;

      await testInfo.attach("url.txt", { body: page.url(), contentType: "text/plain" });
      // The page may be closed or mid-navigation; record why instead of
      // throwing, so the test's real error stays the reported one.
      const snapshot = await page
        .locator("body")
        .ariaSnapshot({ timeout: 5_000 })
        .catch((err: unknown) => `aria snapshot unavailable: ${err instanceof Error ? err.message : String(err)}`);
      await testInfo.attach("aria-snapshot.txt", { body: snapshot, contentType: "text/plain" });
    },
    { auto: true },
  ],
});

export { expect };
export type { Page } from "@playwright/test";
