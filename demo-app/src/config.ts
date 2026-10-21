// Reads the ShopLite mode from the environment. Invalid values stop startup,
// so an eval never runs against a different mode than it asked for.

export const BUG_NAMES = [
  "cart_badge",
  "sort_price",
  "checkout_total",
  "search_case",
  "login_error",
  "form_validation",
] as const;

export type BugName = (typeof BUG_NAMES)[number];

export interface Config {
  port: number;
  bugs: Set<BugName>;
  uiVersion: 1 | 2;
  flaky: boolean;
}

function isBugName(name: string): name is BugName {
  return (BUG_NAMES as readonly string[]).includes(name);
}

export function loadConfig(env: NodeJS.ProcessEnv): Config {
  const errors: string[] = [];

  const port = Number(env.PORT ?? 4000);
  if (!Number.isInteger(port) || port <= 0) {
    errors.push(`PORT must be a positive integer, got "${env.PORT}"`);
  }

  const bugs = new Set<BugName>();
  for (const raw of (env.BUGS ?? "").split(",")) {
    const name = raw.trim();
    if (name === "") continue;
    if (isBugName(name)) {
      bugs.add(name);
    } else {
      errors.push(`unknown bug "${name}" in BUGS (valid: ${BUG_NAMES.join(", ")})`);
    }
  }

  const rawVersion = env.UI_VERSION ?? "1";
  if (rawVersion !== "1" && rawVersion !== "2") {
    errors.push(`UI_VERSION must be 1 or 2, got "${rawVersion}"`);
  }
  const uiVersion = rawVersion === "2" ? 2 : 1;

  const rawFlaky = env.FLAKY ?? "0";
  if (rawFlaky !== "0" && rawFlaky !== "1") {
    errors.push(`FLAKY must be 0 or 1, got "${rawFlaky}"`);
  }

  if (errors.length > 0) {
    throw new Error(`invalid ShopLite configuration:\n  ${errors.join("\n  ")}`);
  }
  return { port, bugs, uiVersion, flaky: rawFlaky === "1" };
}
