// Page snapshots for agents: Playwright's aria snapshot of the body plus a
// list of interactive elements with the attributes locators are built from.
// Both are truncated so one snapshot stays well inside a tool result.

import { z } from "zod";
import type { Page } from "@playwright/test";

export const InteractiveElement = z.object({
  role: z.string(),
  name: z.string(),
  test_id: z.string().optional(),
  label: z.string().optional(),
  placeholder: z.string().optional(),
  href: z.string().optional(),
});
export type InteractiveElement = z.infer<typeof InteractiveElement>;

export interface PageSnapshot {
  url: string;
  title: string;
  aria: string;
  elements: InteractiveElement[];
  omittedElements: number;
}

export interface SnapshotLimits {
  maxAriaChars: number;
  maxElements: number;
}

const DEFAULT_LIMITS: SnapshotLimits = { maxAriaChars: 2500, maxElements: 60 };

// Runs in the page. It is a string, not a function, because tsx adds helper
// calls (__name) to compiled functions that don't exist in the browser.
// The accessible name is an approximation of the spec: aria-label,
// aria-labelledby, <label>, text, alt, title, then placeholder.
const ELEMENTS_SCRIPT = `(() => {
  const clean = (s) => (s || "").replace(/\\s+/g, " ").trim().slice(0, 120);
  const visible = (el) => el.getClientRects().length > 0 && !el.closest("[hidden], [aria-hidden='true']");
  const inputRole = (el) => {
    const t = (el.getAttribute("type") || "text").toLowerCase();
    if (t === "hidden") return "";
    if (["button", "submit", "reset", "image"].includes(t)) return "button";
    if (t === "checkbox") return "checkbox";
    if (t === "radio") return "radio";
    if (t === "number") return "spinbutton";
    if (t === "range") return "slider";
    if (t === "search") return "searchbox";
    return "textbox";
  };
  const roleOf = (el) => {
    const explicit = el.getAttribute("role");
    if (explicit) return explicit.split(" ")[0];
    const tag = el.tagName.toLowerCase();
    if (tag === "a") return el.hasAttribute("href") ? "link" : "";
    if (tag === "button" || tag === "summary") return "button";
    if (tag === "input") return inputRole(el);
    if (tag === "select") return el.multiple || el.size > 1 ? "listbox" : "combobox";
    if (tag === "textarea") return "textbox";
    return "";
  };
  const labelOf = (el) => (el.labels && el.labels.length ? clean(el.labels[0].textContent) : "");
  const nameOf = (el) => {
    const aria = el.getAttribute("aria-label");
    if (aria) return clean(aria);
    const by = el.getAttribute("aria-labelledby");
    if (by) {
      const text = by.split(/\\s+/).map((id) => document.getElementById(id)?.textContent || "").join(" ");
      if (clean(text)) return clean(text);
    }
    const label = labelOf(el);
    if (label) return label;
    const tag = el.tagName.toLowerCase();
    if (tag === "input" && ["button", "submit", "reset"].includes((el.type || "").toLowerCase())) {
      return clean(el.value);
    }
    if (tag !== "input" && tag !== "select" && tag !== "textarea") {
      const text = clean(el.innerText);
      if (text) return text;
      const img = el.querySelector("img[alt]");
      if (img) return clean(img.getAttribute("alt"));
    }
    return clean(el.getAttribute("title") || el.getAttribute("placeholder"));
  };
  const selector = "a[href], button, input, select, textarea, summary, [role], [tabindex]:not([tabindex='-1'])";
  const out = [];
  const seen = new Set();
  for (const el of document.querySelectorAll(selector)) {
    if (!visible(el)) continue;
    const role = roleOf(el);
    if (!role || ["presentation", "none", "generic"].includes(role)) continue;
    const item = { role, name: nameOf(el) };
    const testId = el.getAttribute("data-testid");
    if (testId) item.test_id = testId;
    const label = labelOf(el);
    if (label) item.label = label;
    const placeholder = el.getAttribute("placeholder");
    if (placeholder) item.placeholder = clean(placeholder);
    const href = el.getAttribute("href");
    if (href && el.tagName.toLowerCase() === "a") item.href = href.slice(0, 200);
    const key = JSON.stringify(item);
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(item);
  }
  return out;
})()`;

export async function takeSnapshot(page: Page, limits: SnapshotLimits = DEFAULT_LIMITS): Promise<PageSnapshot> {
  const aria = await page.locator("body").ariaSnapshot();
  // The page's own data: validate before trusting its shape.
  const all = z.array(InteractiveElement).parse(await page.evaluate(ELEMENTS_SCRIPT));
  return {
    url: page.url(),
    title: await page.title(),
    aria: truncateLines(aria, limits.maxAriaChars),
    elements: all.slice(0, limits.maxElements),
    omittedElements: Math.max(0, all.length - limits.maxElements),
  };
}

// formatSnapshot renders a snapshot as compact text for the model.
export function formatSnapshot(s: PageSnapshot): string {
  const elements = s.elements.map((e) => {
    const attrs = [
      e.test_id && `test_id=${e.test_id}`,
      e.label && `label="${e.label}"`,
      e.placeholder && `placeholder="${e.placeholder}"`,
      e.href && `href=${e.href}`,
    ].filter(Boolean);
    return `- ${e.role} "${e.name}"${attrs.length ? " " + attrs.join(" ") : ""}`;
  });
  if (s.omittedElements > 0) elements.push(`…and ${s.omittedElements} more elements`);
  return [
    `URL: ${s.url}`,
    `Title: ${s.title}`,
    "",
    "Aria snapshot:",
    s.aria,
    "",
    "Interactive elements:",
    ...(elements.length ? elements : ["(none)"]),
  ].join("\n");
}

// truncateLines cuts at a line boundary and says how much was left out.
export function truncateLines(text: string, maxChars: number): string {
  if (text.length <= maxChars) return text;
  const cut = text.lastIndexOf("\n", maxChars);
  const kept = text.slice(0, cut > 0 ? cut : maxChars);
  const omitted = text.slice(kept.length).split("\n").filter((l) => l.trim() !== "").length;
  return `${kept}\n…(${omitted} more lines)`;
}
