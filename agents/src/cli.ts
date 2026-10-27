// Entry point for agent runs: `tsx src/cli.ts <kind> --input <file.json>`.
// Writes protocol events (ARCHITECTURE.md) as JSONL to stdout; everything
// else goes to stderr. Exits 0 once a done event is written, whatever its
// status; non-zero only for usage errors and crashes.

import { existsSync, readFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { parseEnv } from "node:util";
import { z } from "zod";
import { BaseInput, type AgentContext, type AgentFn } from "./agents/context.ts";
import { PROBE_BUDGET, PROBE_PROMPTS, runProbe } from "./agents/probe/index.ts";
import { AgentFailed } from "./core/agent-loop.ts";
import { Budget, BudgetExceeded, limitsFor, type BudgetLimits } from "./core/budget.ts";
import { loadRuntimeConfig } from "./core/config.ts";
import { Events, redirectConsole, type RunStatus } from "./core/events.ts";
import { createProvider } from "./llm/index.ts";
import { CostMeter } from "./llm/pricing.ts";
import { promptVersion } from "./prompts/loader.ts";

const KINDS = ["probe", "explore", "triage", "heal"] as const;
type Kind = (typeof KINDS)[number];

// Agents not listed here are not implemented yet. budget tightens the
// configured AGENT_* limits for that agent.
const AGENTS: Partial<Record<Kind, { run: AgentFn; prompts: string[]; budget?: Partial<BudgetLimits> }>> = {
  probe: { run: runProbe, prompts: PROBE_PROMPTS, budget: PROBE_BUDGET },
};

const USAGE = `Usage: tsx src/cli.ts <kind> --input <file.json>

Kinds: ${KINDS.join(", ")}`;

// How long cleanup (closing the browser) may take after SIGTERM.
const CANCEL_CLEANUP_MS = 3000;

const Args = z.object({
  kind: z.enum(KINDS),
  input: z.string().min(1),
});

redirectConsole();
const events = new Events();
const cleanups: Array<() => Promise<void>> = [];
let budget: Budget | undefined;

main(process.argv.slice(2)).then(
  async (code) => {
    await events.flush();
    process.exit(code);
  },
  async (err: unknown) => {
    console.error("agent crashed:", err);
    finish("failed", {}, `agent crashed: ${errorMessage(err)}`);
    await events.flush();
    process.exit(1);
  },
);

async function main(argv: string[]): Promise<number> {
  if (argv.length === 0 || argv[0] === "--help" || argv[0] === "-h") {
    console.error(USAGE);
    return argv.length === 0 ? 2 : 0;
  }
  const args = Args.safeParse({ kind: argv[0], input: flagValue(argv, "--input") });
  if (!args.success) {
    console.error(`${z.prettifyError(args.error)}\n\n${USAGE}`);
    return 2;
  }
  const { kind } = args.data;

  loadDotEnv(join(import.meta.dirname, "..", "..", ".env"));
  process.once("SIGTERM", () => void cancel());

  let input: BaseInput;
  try {
    input = BaseInput.parse(JSON.parse(await readFile(args.data.input, "utf8")));
  } catch (err) {
    const msg = err instanceof z.ZodError ? z.prettifyError(err) : errorMessage(err);
    finish("failed", {}, `invalid input ${args.data.input}: ${msg}`);
    return 1;
  }

  const agent = AGENTS[kind];
  const prompts = Object.fromEntries(
    await Promise.all((agent?.prompts ?? []).map(async (p) => [p, await promptVersion(p)] as const)),
  );
  events.emit("run_started", {
    agent: kind,
    input_summary: { agent_run_id: input.agent_run_id, app: input.app.slug, base_url: input.app.base_url },
    prompts,
  });
  if (agent === undefined) {
    console.error(`${kind}: not implemented yet`);
    finish("failed", {}, `${kind} is not implemented yet`);
    return 0;
  }

  try {
    const config = loadRuntimeConfig(process.env);
    const runBudget = new Budget(limitsFor(config.budget, agent.budget));
    budget = runBudget;
    const ctx: AgentContext = {
      input,
      config,
      events,
      provider: createProvider(config, events, () => runBudget.takeRequest()),
      budget: runBudget,
      cost: new CostMeter(events),
      onCleanup: (fn) => cleanups.push(fn),
    };
    const summary = await agent.run(ctx);
    finish("succeeded", summary);
  } catch (err) {
    if (err instanceof BudgetExceeded) {
      finish("budget_exceeded", {}, err.message);
    } else {
      if (!(err instanceof AgentFailed)) console.error(err);
      finish("failed", {}, errorMessage(err));
    }
  } finally {
    await runCleanups();
  }
  return 0;
}

// finish emits done with the run's usage added to the summary. It is a
// no-op after the first done (for example, cancel won the race).
function finish(status: RunStatus, summary: Record<string, unknown>, error?: string): void {
  if (events.isDone) return;
  const usage = budget
    ? { steps: budget.steps, requests: budget.requests, cost_usd: budget.costUSD, elapsed_ms: budget.elapsedMs() }
    : {};
  events.emit("done", { status, summary: { ...summary, usage }, ...(error ? { error } : {}) });
}

// cancel handles SIGTERM from the supervisor: done first (so it is never
// lost), then a short best-effort cleanup.
async function cancel(): Promise<void> {
  finish("cancelled", {}, "cancelled");
  await Promise.race([runCleanups(), new Promise((r) => setTimeout(r, CANCEL_CLEANUP_MS))]);
  await events.flush();
  process.exit(0);
}

async function runCleanups(): Promise<void> {
  const fns = cleanups.splice(0);
  await Promise.allSettled(fns.map((fn) => fn()));
}

// loadDotEnv sets variables from the repo's .env that are not already set:
// the real environment wins, as in the Go config loader.
function loadDotEnv(path: string): void {
  if (!existsSync(path)) return;
  for (const [key, value] of Object.entries(parseEnv(readFileSync(path, "utf8")))) {
    if (process.env[key] === undefined) process.env[key] = value;
  }
}

function flagValue(argv: string[], flag: string): string | undefined {
  const i = argv.indexOf(flag);
  return i === -1 ? undefined : argv[i + 1];
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
