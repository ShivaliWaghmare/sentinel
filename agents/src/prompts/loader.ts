// Prompt templates: *.md files in this folder. The first line is
// "version: <id>"; the rest is the template, with {{variable}} placeholders.
// The version is recorded in the run_started event, so results can be tied
// to the prompt that produced them.

import { readFile } from "node:fs/promises";
import { join } from "node:path";

const PROMPTS_DIR = import.meta.dirname;

export interface Prompt {
  name: string;
  version: string;
  text: string;
}

export async function loadPrompt(name: string, vars: Record<string, string>, dir = PROMPTS_DIR): Promise<Prompt> {
  const raw = await readFile(join(dir, `${name}.md`), "utf8");
  const { version, template } = parseTemplate(name, raw);
  return { name, version, text: render(name, template, vars) };
}

// promptVersion reads only the version header, for run_started.
export async function promptVersion(name: string, dir = PROMPTS_DIR): Promise<string> {
  return parseTemplate(name, await readFile(join(dir, `${name}.md`), "utf8")).version;
}

export function parseTemplate(name: string, raw: string): { version: string; template: string } {
  const newline = raw.indexOf("\n");
  const header = (newline === -1 ? raw : raw.slice(0, newline)).trim();
  const m = /^version:\s*(\S+)$/.exec(header);
  if (!m?.[1]) throw new Error(`prompt ${name}: first line must be "version: <id>"`);
  return { version: m[1], template: newline === -1 ? "" : raw.slice(newline + 1).trim() };
}

// render fills {{name}} placeholders. A placeholder without a value is an
// error, so a typo can't silently send "{{nmae}}" to the model.
export function render(name: string, template: string, vars: Record<string, string>): string {
  return template.replace(/\{\{\s*(\w+)\s*\}\}/g, (_match, key: string) => {
    const value = vars[key];
    if (value === undefined) throw new Error(`prompt ${name}: no value for {{${key}}}`);
    return value;
  });
}
