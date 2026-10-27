// The agent loop (ARCHITECTURE.md, "Agent loop"; ADR-0002). Until a final
// answer or a budget is hit: check budgets → call the LLM → run each tool
// call with zod-validated args → append the results as untrusted text.
//
// The final answer is the arguments of a "finish" tool whose schema is the
// agent's output schema. Both providers handle this the same way, and the
// model can still call other tools in the turns before it. An invalid final
// answer gets one corrective retry.

import { z } from "zod";
import type { CostMeter } from "../llm/pricing.ts";
import type { LLMProvider, Message, ToolCall, ToolResult } from "../llm/types.ts";
import { Budget, BudgetExceeded } from "./budget.ts";
import type { Events } from "./events.ts";
import { toDeclaration, type Tool } from "./tools.ts";
import { truncate, untrusted } from "./untrusted.ts";

export const FINISH_TOOL = "finish";
const TOOL_OUTPUT_CHARS = 4000;
const PREVIEW_CHARS = 300;
const THOUGHT_CHARS = 1000;

// AgentFailed ends the run with status failed (for example, the final
// answer was still invalid after the corrective retry).
export class AgentFailed extends Error {
  constructor(message: string) {
    super(message);
    this.name = "AgentFailed";
  }
}

export interface AgentLoopOptions<O extends z.ZodType> {
  provider: LLMProvider;
  events: Events;
  budget: Budget;
  cost: CostMeter;
  purpose: string; // recorded on llm_call events, e.g. "probe"
  system: string;
  task: string; // the first user message
  tools: Tool[];
  output: { description: string; schema: O };
}

export async function runAgentLoop<O extends z.ZodType>(opts: AgentLoopOptions<O>): Promise<z.infer<O>> {
  const { provider, events, budget, cost, tools, output } = opts;
  const byName = new Map(tools.map((t) => [t.name, t]));
  const declarations = [
    ...tools.map(toDeclaration),
    toDeclaration({ name: FINISH_TOOL, description: output.description, schema: output.schema }),
  ];
  const messages: Message[] = [{ role: "user", text: opts.task }];
  let finishRetryUsed = false;

  for (;;) {
    budget.check();
    const signal = budget.signal();
    const started = Date.now();
    let res;
    try {
      res = await provider.generate({
        system: opts.system,
        messages,
        tools: declarations,
        signal,
        deadlineMs: budget.deadlineMs(),
      });
    } catch (err) {
      if (signal.aborted) throw budget.timeExceeded();
      throw err;
    }
    const callCost = cost.cost(res.model, res.usage);
    budget.recordStep(callCost);
    events.emit("llm_call", {
      model: res.model,
      purpose: opts.purpose,
      tokens_in: res.usage.inputTokens,
      tokens_out: res.usage.outputTokens,
      cost_usd: callCost,
      latency_ms: Date.now() - started,
      requests: res.requests ?? 1,
      wait_ms: res.waitedMs ?? 0,
    });

    messages.push({ role: "assistant", text: res.text, toolCalls: res.toolCalls, providerData: res.providerData });
    if (res.text?.trim()) events.emit("thought", { text: truncate(res.text.trim(), THOUGHT_CHARS) });

    const calls = res.toolCalls ?? [];
    if (calls.length === 0) {
      messages.push({
        role: "user",
        text: `Reply with tool calls only. When you are done, call ${FINISH_TOOL} with your final answer.`,
      });
      continue;
    }

    const results: ToolResult[] = [];
    let final: { value: z.infer<O> } | undefined;
    for (const call of calls) {
      const shownArgs = byName.get(call.name)?.redact?.(call.args) ?? call.args;
      events.emit("tool_call", { id: call.id, tool: call.name, args: shownArgs });
      if (final !== undefined) {
        // finish came earlier in this turn; the run is over.
        results.push(report(events, call, false, "skipped: the run already finished"));
        continue;
      }
      if (call.name === FINISH_TOOL) {
        const parsed = output.schema.safeParse(call.args);
        if (parsed.success) {
          final = { value: parsed.data };
          results.push(report(events, call, true, "final answer accepted"));
          continue;
        }
        const problem = `final answer does not match the schema:\n${z.prettifyError(parsed.error)}`;
        results.push(report(events, call, false, `${problem}\nCall ${FINISH_TOOL} again with corrected arguments.`));
        if (finishRetryUsed) throw new AgentFailed(`${problem}\n(after one corrective retry)`);
        finishRetryUsed = true;
        continue;
      }
      results.push(await runTool(events, byName.get(call.name), call, [...byName.keys(), FINISH_TOOL]));
    }

    if (final !== undefined) return final.value;
    messages.push({ role: "tool", results });
  }
}

async function runTool(events: Events, tool: Tool | undefined, call: ToolCall, names: string[]): Promise<ToolResult> {
  if (tool === undefined) {
    return report(events, call, false, `unknown tool "${call.name}"; available tools: ${names.join(", ")}`);
  }
  const parsed = tool.schema.safeParse(call.args);
  if (!parsed.success) {
    return report(events, call, false, `invalid arguments for ${tool.name}:\n${z.prettifyError(parsed.error)}`);
  }
  try {
    const out = await tool.run(parsed.data);
    return report(events, call, true, untrusted(`tool:${tool.name}`, out, TOOL_OUTPUT_CHARS));
  } catch (err) {
    if (err instanceof BudgetExceeded) throw err;
    const message = err instanceof Error ? err.message : String(err);
    // Tool errors can quote page content (Playwright does), so they are
    // untrusted too.
    return report(events, call, false, untrusted(`tool:${tool.name}:error`, message, TOOL_OUTPUT_CHARS));
  }
}

// report emits the tool_result event and builds the result for the model.
function report(events: Events, call: ToolCall, ok: boolean, output: string): ToolResult {
  events.emit("tool_result", {
    id: call.id,
    ok,
    output_preview: truncate(output, PREVIEW_CHARS),
    ...(ok ? {} : { error: truncate(output, PREVIEW_CHARS) }),
  });
  return { callId: call.id, name: call.name, output, isError: !ok };
}
