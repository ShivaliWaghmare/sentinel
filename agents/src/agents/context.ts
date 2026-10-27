// What every agent gets from the CLI: its validated input and the shared
// plumbing (events, provider, budget, cost meter).

import { z } from "zod";
import type { Budget } from "../core/budget.ts";
import type { RuntimeConfig } from "../core/config.ts";
import type { Events } from "../core/events.ts";
import type { CostMeter } from "../llm/pricing.ts";
import type { LLMProvider } from "../llm/types.ts";

// input.json as the Go supervisor writes it (ARCHITECTURE.md, "Agent input").
// Kind-specific fields are added by each agent's own schema.
export const BaseInput = z.object({
  agent_run_id: z.number().int().positive(),
  run_dir: z.string().min(1), // absolute: DATA_DIR/agent-runs/<id>
  app: z.object({
    id: z.number().int().positive(),
    name: z.string(),
    slug: z.string(),
    base_url: z.url({ protocol: /^https?$/ }),
    notes: z.string(),
    allow_destructive: z.boolean(),
  }),
});
export type BaseInput = z.infer<typeof BaseInput>;

export interface AgentContext {
  input: BaseInput;
  config: RuntimeConfig;
  events: Events;
  provider: LLMProvider;
  budget: Budget;
  cost: CostMeter;
  // onCleanup registers work (closing the browser) that also runs when the
  // run is cancelled.
  onCleanup(fn: () => Promise<void>): void;
}

// An agent returns its kind-specific summary for the done event.
export type AgentFn = (ctx: AgentContext) => Promise<Record<string, unknown>>;
