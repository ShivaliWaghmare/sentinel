// The shared browser tools against a real Chromium and a tiny local site.

import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { DestructiveActionBlocked } from "./guards.ts";
import { BrowserSession, OffOriginError } from "./session.ts";
import { clickTool, fillTool, isSecretField, openPageTool, snapshotTool } from "./tools.ts";

const PAGES: Record<string, string> = {
  "/": `<h1>Home</h1><nav><a href="/login">Log in</a> <a href="https://example.com/">Elsewhere</a></nav>
        <button>Delete everything</button>`,
  "/login": `<h1>Log in</h1><form action="/done">
        <label for="u">Username</label><input id="u" name="u">
        <label for="p">Password</label><input id="p" name="p" type="password">
        <button type="submit">Sign in</button></form>`,
  "/done": `<h1>Welcome</h1>`,
};

let server: Server;
let session: BrowserSession;
let baseURL: string;

beforeAll(async () => {
  server = createServer((req, res) => {
    const body = PAGES[new URL(req.url ?? "/", "http://x").pathname];
    res.writeHead(body ? 200 : 404, { "content-type": "text/html" });
    res.end(`<!doctype html><title>Test site</title><body>${body ?? "not found"}</body>`);
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  baseURL = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  session = await BrowserSession.launch(baseURL);
}, 30_000);

afterAll(async () => {
  await session?.close();
  await new Promise((resolve) => server?.close(resolve));
});

describe("browser tools return the resulting page snapshot", () => {
  it("open_page", async () => {
    const out = await openPageTool(session).run({ path: "/" });
    expect(out).toContain(`Opened ${baseURL}/ (HTTP 200).`);
    expect(out).toContain('heading "Home"');
    expect(out).toContain('- link "Log in" href=/login');
  });

  it("click", async () => {
    await openPageTool(session).run({ path: "/" });
    const out = await clickTool(session, { allowDestructive: false }).run({ role: "link", name: "Log in" });
    expect(out).toContain('Clicked link "Log in".');
    expect(out).toContain(`URL: ${baseURL}/login`);
    expect(out).toContain('- textbox "Username" label="Username"');
  });

  it("fill, then click submit", async () => {
    await openPageTool(session).run({ path: "/login" });
    const filled = await fillTool(session).run({ label: "Username", value: "demo" });
    expect(filled).toContain('Filled "Username".');
    expect(filled).toContain(`URL: ${baseURL}/login`);

    const submitted = await clickTool(session, { allowDestructive: false }).run({ role: "button", name: "Sign in" });
    expect(submitted).toContain('heading "Welcome"');
  });

  it("snapshot still reads the page on its own", async () => {
    await openPageTool(session).run({ path: "/login" });
    const out = await snapshotTool(session).run({});
    expect(out).toContain(`URL: ${baseURL}/login`);
    expect(out).not.toContain("Opened");
  });
});

describe("browser tool guards", () => {
  it("refuses destructive clicks unless allowed", async () => {
    await openPageTool(session).run({ path: "/" });
    await expect(
      clickTool(session, { allowDestructive: false }).run({ role: "button", name: "Delete everything" }),
    ).rejects.toBeInstanceOf(DestructiveActionBlocked);
  });

  it("refuses other origins and reports a blocked navigation", async () => {
    await expect(openPageTool(session).run({ path: "https://example.com/" })).rejects.toBeInstanceOf(OffOriginError);

    await openPageTool(session).run({ path: "/" });
    const out = await clickTool(session, { allowDestructive: false }).run({ role: "link", name: "Elsewhere" });
    expect(out).toContain(`A navigation to https://example.com/ was blocked (other origin); you are still on ${baseURL}/.`);
    expect(out).toContain(`URL: ${baseURL}/`);
    expect(out).toContain('heading "Home"');
    expect(session.page.url()).toBe(`${baseURL}/`);
  });

  it("names a missing element", async () => {
    await openPageTool(session).run({ path: "/" });
    await expect(clickTool(session, { allowDestructive: false }).run({ role: "button", name: "Nope" })).rejects.toThrow(
      'no button named "Nope"',
    );
    await expect(fillTool(session).run({ label: "Nope", value: "x" })).rejects.toThrow('no field labelled "Nope"');
  });

  it("redacts password fields in events", () => {
    const fill = fillTool(session);
    expect(fill.redact?.({ label: "Password", value: "demo123" })).toEqual({ label: "Password", value: "[redacted]" });
    expect(fill.redact?.({ label: "Username", value: "demo" })).toEqual({ label: "Username", value: "demo" });
    expect(isSecretField({ label: "PIN" })).toBe(true);
  });
});
