// Browser tools shared by the agents (the probe now, the Explorer later).
//
// Convention: every tool that acts on the page (open_page, click, fill, and
// later select, press, back) returns its outcome followed by a snapshot of
// the page it led to. The model sees the result of an action in the same
// tool result, so it doesn't spend an LLM request on a separate snapshot
// call after each step. snapshot() stays for when the page changes on its
// own (a list that loads late) or the model wants a fresh look.

import { z } from "zod";
import type { Page } from "@playwright/test";
import { defineTool, type Tool } from "../core/tools.ts";
import { assertSafe } from "./guards.ts";
import type { BrowserSession } from "./session.ts";
import { formatSnapshot, takeSnapshot } from "./snapshot.ts";

// Roles the model may click. The type check keeps the list to roles
// Playwright's getByRole knows.
type AriaRole = Parameters<Page["getByRole"]>[0];
export const CLICKABLE_ROLES = [
  "button",
  "link",
  "checkbox",
  "radio",
  "tab",
  "menuitem",
  "option",
  "switch",
  "combobox",
  "treeitem",
] as const satisfies readonly AriaRole[];

export interface BrowserToolOptions {
  allowDestructive: boolean;
}

// withSnapshot appends the current page's snapshot to an action's outcome.
// from is the URL the action started on, for returning after a blocked
// off-origin navigation.
async function withSnapshot(session: BrowserSession, outcome: string, from: string): Promise<string> {
  let blocked = "";
  if (session.lastBlocked !== undefined) {
    blocked = `\nA navigation to ${session.lastBlocked} was blocked (other origin); you are still on ${from}.`;
    await session.returnAfterBlocked(from);
  }
  return `${outcome}${blocked}\n\n${formatSnapshot(await takeSnapshot(session.page))}`;
}

export function openPageTool(session: BrowserSession): Tool {
  return defineTool({
    name: "open_page",
    description:
      "Open a page of the app by path, e.g. '/' or '/login'. Only the app's own origin is allowed. Returns a snapshot of the page.",
    schema: z.object({
      path: z.string().min(1).describe("Path such as '/products', or a full URL on the app's origin"),
    }),
    async run({ path }) {
      const from = session.page.url();
      const info = await session.goto(path);
      return withSnapshot(session, `Opened ${info.url} (HTTP ${info.status ?? "unknown"}).`, from);
    },
  });
}

export function snapshotTool(session: BrowserSession): Tool {
  return defineTool({
    name: "snapshot",
    description:
      "Read the current page again: accessibility tree and interactive elements. Actions already return a snapshot; use this only when the page may have changed on its own.",
    schema: z.object({}),
    async run() {
      return formatSnapshot(await takeSnapshot(session.page));
    },
  });
}

export function clickTool(session: BrowserSession, opts: BrowserToolOptions): Tool {
  const page = session.page;
  return defineTool({
    name: "click",
    description:
      "Click the element with this ARIA role and accessible name (from a snapshot). Returns a snapshot of the page afterwards.",
    schema: z.object({
      role: z.enum(CLICKABLE_ROLES),
      name: z.string().min(1).describe("Accessible name, exactly as shown in the snapshot"),
    }),
    async run({ role, name }) {
      session.lastBlocked = undefined;
      const from = page.url();
      let target = page.getByRole(role, { name, exact: true });
      if ((await target.count()) === 0) target = page.getByRole(role, { name });
      const count = await target.count();
      if (count === 0) throw new Error(`no ${role} named "${name}" on this page; take a snapshot to see what is there`);
      const element = target.first();
      const actualName = (await element.getAttribute("aria-label")) ?? (await element.innerText()).trim();
      assertSafe([name, actualName], opts.allowDestructive);
      await element.click();
      await page.waitForLoadState("domcontentloaded");
      const note = count > 1 ? ` (${count} matches; clicked the first)` : "";
      return withSnapshot(session, `Clicked ${role} "${actualName || name}"${note}.`, from);
    },
  });
}

export function fillTool(session: BrowserSession): Tool {
  const page = session.page;
  return defineTool({
    name: "fill",
    description:
      "Type a value into the form field with this label (or placeholder). Replaces what is there. Returns a snapshot of the page afterwards.",
    schema: z.object({
      label: z.string().min(1).describe("The field's label or placeholder, as shown in the snapshot"),
      value: z.string().max(200),
    }),
    async run({ label, value }) {
      session.lastBlocked = undefined;
      const from = page.url();
      let field = page.getByLabel(label, { exact: true });
      if ((await field.count()) === 0) field = page.getByPlaceholder(label);
      if ((await field.count()) === 0) throw new Error(`no field labelled "${label}" on this page`);
      await field.first().fill(value);
      return withSnapshot(session, `Filled "${label}".`, from);
    },
    redact: (args) => (isSecretField(args) ? { ...(args as object), value: "[redacted]" } : args),
  });
}

// isSecretField reports whether fill args target a password-like field,
// whose value must not appear in events.
export function isSecretField(args: unknown): boolean {
  const label = z.object({ label: z.string() }).safeParse(args);
  return label.success && /pass(word|code)?|secret|token|pin/i.test(label.data.label);
}
