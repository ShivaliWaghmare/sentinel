// A Playwright browser session for agents. The context has the app's
// baseURL, and top-level navigations to any other origin are blocked
// (CLAUDE.md: the Explorer stays on the app's origin).

import { chromium, type Browser, type BrowserContext, type Page } from "@playwright/test";

const ACTION_TIMEOUT_MS = 10_000;
const NAVIGATION_TIMEOUT_MS = 20_000;

export class OffOriginError extends Error {
  constructor(url: string, origin: string) {
    super(`blocked: ${url} is outside the app's origin ${origin}`);
    this.name = "OffOriginError";
  }
}

export interface PageInfo {
  url: string;
  title: string;
  status: number | null;
}

export class BrowserSession {
  // The last off-origin navigation the route handler aborted, so tools can
  // explain why a click "did nothing".
  lastBlocked: string | undefined;

  private constructor(
    private readonly browser: Browser,
    private readonly context: BrowserContext,
    readonly page: Page,
    readonly origin: string,
  ) {}

  static async launch(baseURL: string, opts: { headless?: boolean } = {}): Promise<BrowserSession> {
    const origin = new URL(baseURL).origin;
    const browser = await chromium.launch({ headless: opts.headless ?? true });
    try {
      const context = await browser.newContext({ baseURL });
      context.setDefaultTimeout(ACTION_TIMEOUT_MS);
      context.setDefaultNavigationTimeout(NAVIGATION_TIMEOUT_MS);
      const page = await context.newPage();
      const session = new BrowserSession(browser, context, page, origin);
      await context.route("**/*", async (route) => {
        const req = route.request();
        // Sub-resources (images, scripts, XHR) may come from anywhere; only
        // main-frame navigations are kept on the origin.
        const mainFrameNav = req.isNavigationRequest() && req.frame().parentFrame() === null;
        if (mainFrameNav && new URL(req.url()).origin !== origin) {
          session.lastBlocked = req.url();
          await route.abort("blockedbyclient");
          return;
        }
        await route.continue();
      });
      return session;
    } catch (err) {
      await browser.close();
      throw err;
    }
  }

  // goto opens a path (or a same-origin URL) and reports where it landed.
  async goto(pathOrURL: string): Promise<PageInfo> {
    const url = new URL(pathOrURL, this.origin + "/");
    if (url.origin !== this.origin) throw new OffOriginError(url.href, this.origin);
    this.lastBlocked = undefined;
    const res = await this.page.goto(url.href);
    return this.info(res?.status() ?? null);
  }

  // returnAfterBlocked undoes an aborted off-origin navigation: Chromium
  // leaves the tab on its error page (chrome-error://), which would strand
  // the agent. It reopens the page the action started from.
  async returnAfterBlocked(from: string): Promise<void> {
    if (this.lastBlocked === undefined || this.page.url() === from) return;
    if (new URL(from).origin !== this.origin) return;
    await this.page.goto(from);
  }

  async info(status: number | null = null): Promise<PageInfo> {
    return { url: this.page.url(), title: await this.page.title(), status };
  }

  async close(): Promise<void> {
    await this.context.close().catch(() => undefined);
    await this.browser.close().catch(() => undefined);
  }
}
