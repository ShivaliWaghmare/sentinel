// The probe agent: a small end-to-end check of the agent plumbing. It opens
// the app in a browser, looks around and describes what the app does and
// its main user flows.

import { z } from "zod";
import { runAgentLoop } from "../../core/agent-loop.ts";
import type { BudgetLimits } from "../../core/budget.ts";
import type { Tool } from "../../core/tools.ts";
import { BrowserSession } from "../../browser/session.ts";
import { clickTool, fillTool, openPageTool, snapshotTool } from "../../browser/tools.ts";
import { loadPrompt } from "../../prompts/loader.ts";
import type { AgentContext } from "../context.ts";

export const ProbeOutput = z.object({
  app_summary: z.string().min(20).describe("Two to four sentences on what the application is and who uses it."),
  flows: z
    .array(
      z.object({
        name: z.string().min(1).describe("Short name, e.g. 'Log in'"),
        description: z.string().min(1).describe("One sentence describing the steps"),
        pages: z.array(z.string()).describe("Paths of the pages involved, e.g. '/login'"),
      }),
    )
    .min(1)
    .max(15),
});
export type ProbeOutput = z.infer<typeof ProbeOutput>;

export async function runProbe(ctx: AgentContext): Promise<Record<string, unknown>> {
  const { app } = ctx.input;
  const system = await loadPrompt("probe.system", {});
  const task = await loadPrompt("probe.task", {
    app_name: app.name,
    base_url: app.base_url,
    notes: app.notes.trim() || "(none)",
  });
  ctx.events.emit("phase", { name: "probe", detail: `Exploring ${app.base_url}` });

  const session = await BrowserSession.launch(app.base_url);
  ctx.onCleanup(() => session.close());
  try {
    const output = await runAgentLoop({
      provider: ctx.provider,
      events: ctx.events,
      budget: ctx.budget,
      cost: ctx.cost,
      purpose: "probe",
      system: system.text,
      task: task.text,
      tools: probeTools(session, app.allow_destructive),
      output: {
        description: "Give the final answer: what the app does and its main user flows. Call this exactly once.",
        schema: ProbeOutput,
      },
    });
    return output;
  } finally {
    await session.close();
  }
}

function isSecretField(args: unknown): boolean {
  const label = z.object({ label: z.string() }).safeParse(args);
  return label.success && /pass(word|code)?|secret|token|pin/i.test(label.data.label);
}

// The probe's own budgets. At LLM_RPM 5 a request takes about 12 s, so 15
// requests need about 3 minutes plus model latency: 4 minutes covers that.
// limitsFor never lets these exceed the configured AGENT_* limits.
export const PROBE_BUDGET: Partial<BudgetLimits> = { maxRequests: 15, timeoutMs: 4 * 60_000 };

// The prompt versions this agent uses, for run_started.
export const PROBE_PROMPTS = ["probe.system", "probe.task"];

export function probeTools(session: BrowserSession, allowDestructive: boolean): Tool[] {
  return [
    openPageTool(session),
    clickTool(session, { allowDestructive }),
    fillTool(session),
    snapshotTool(session),
  ];
}
