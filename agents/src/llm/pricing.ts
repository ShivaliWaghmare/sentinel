// USD per 1M tokens, per model (ADR-0005: prices live in one table).
//
// PLACEHOLDER VALUES. Fill them from the providers' pricing pages before
// trusting any cost number:
//   Gemini:    https://ai.google.dev/gemini-api/docs/pricing
//   Anthropic: https://www.anthropic.com/pricing (API section)
// Output prices include thinking tokens, which both providers bill as output.

import type { Events } from "../core/events.ts";
import type { Usage } from "./types.ts";

export interface Price {
  inputPerM: number;
  outputPerM: number;
}

export const PRICES: Readonly<Record<string, Price>> = {
  "gemini-3.8-flash": { inputPerM: 0.3, outputPerM: 2.5 },
  "gemini-3.8-pro": { inputPerM: 1.25, outputPerM: 10 },
  "gemini-3.5-flash": { inputPerM: 0.3, outputPerM: 2.5 },
  "claude-haiku-5-5": { inputPerM: 1, outputPerM: 5 },
  "claude-sonnet-5-5": { inputPerM: 3, outputPerM: 15 },
  "claude-opus-5-5": { inputPerM: 5, outputPerM: 25 },
};

// CostMeter prices usage. An unknown model costs 0 and emits one warning
// per model, so the budget can't silently rely on a missing price.
export class CostMeter {
  private readonly warned = new Set<string>();

  constructor(
    private readonly events: Events,
    private readonly prices: Readonly<Record<string, Price>> = PRICES,
  ) {}

  cost(model: string, usage: Usage): number {
    const price = this.prices[model];
    if (price === undefined) {
      if (!this.warned.has(model)) {
        this.warned.add(model);
        this.events.emit("warning", {
          message: `no price for model "${model}"; its cost counts as 0 (add it to src/llm/pricing.ts)`,
        });
      }
      return 0;
    }
    return (usage.inputTokens * price.inputPerM + usage.outputTokens * price.outputPerM) / 1_000_000;
  }
}
