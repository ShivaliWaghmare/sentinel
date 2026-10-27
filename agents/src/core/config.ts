// Runtime settings for agent runs, read from the environment (the Go server
// passes its own; the CLI also loads the repo's .env). Names and defaults
// match the Configuration table in ARCHITECTURE.md.

import { z } from "zod";

const UNITS_MS: Record<string, number> = { ms: 1, s: 1000, m: 60_000, h: 3_600_000 };

// parseDuration accepts Go-style durations like "20m", "1h30m" or "45s",
// since the same .env value is read by the Go server.
export function parseDuration(s: string): number {
  const parts = [...s.matchAll(/(\d+(?:\.\d+)?)(ms|s|m|h)/g)];
  if (parts.length === 0 || parts.map((p) => p[0]).join("") !== s) {
    throw new Error(`invalid duration "${s}" (use e.g. 20m, 90s, 1h30m)`);
  }
  return parts.reduce((ms, p) => ms + Number(p[1]) * (UNITS_MS[p[2] ?? ""] ?? 0), 0);
}

// duration is a zod schema for a positive Go-style duration, in ms.
function duration(def: string) {
  return z
    .string()
    .default(def)
    .transform((v, ctx) => {
      try {
        const ms = parseDuration(v);
        if (ms > 0) return ms;
        ctx.addIssue({ code: "custom", message: `duration must be positive, got "${v}"` });
      } catch (err) {
        ctx.addIssue({ code: "custom", message: err instanceof Error ? err.message : String(err) });
      }
      return z.NEVER;
    });
}

const boolString = z.enum(["true", "false", "1", "0"]).transform((v) => v === "true" || v === "1");

const EnvSchema = z.object({
  LLM_PROVIDER: z.enum(["gemini", "anthropic"]).default("gemini"),
  LLM_MODEL: z.string().default(""),
  LLM_VISION: boolString.default(true),
  GEMINI_API_KEY: z.string().default(""),
  ANTHROPIC_API_KEY: z.string().default(""),
  AGENT_MAX_STEPS: z.coerce.number().int().positive().default(80),
  AGENT_MAX_COST_USD: z.coerce.number().positive().default(1),
  AGENT_TIMEOUT: duration("20m"),
  LLM_CALL_TIMEOUT: duration("60s"),
  LLM_RPM: z.coerce.number().int().min(0).default(5),
  LLM_MAX_REQUESTS_PER_RUN: z.coerce.number().int().positive().default(15),
});

export interface RuntimeConfig {
  provider: "gemini" | "anthropic";
  model: string;
  vision: boolean;
  apiKey: string; // for the selected provider; "" if unset
  llmCallTimeoutMs: number; // per LLM request attempt
  llmRPM: number; // requests per minute; 0 = no pacing
  budget: { maxSteps: number; maxRequests: number; maxCostUSD: number; timeoutMs: number };
}

export function loadRuntimeConfig(env: NodeJS.ProcessEnv): RuntimeConfig {
  // Empty strings mean "unset", as in the Go config loader.
  const present = Object.fromEntries(Object.entries(env).filter(([, v]) => v !== undefined && v !== ""));
  const parsed = EnvSchema.safeParse(present);
  if (!parsed.success) throw new Error(`invalid configuration:\n${z.prettifyError(parsed.error)}`);
  const e = parsed.data;
  return {
    provider: e.LLM_PROVIDER,
    model: e.LLM_MODEL,
    vision: e.LLM_VISION,
    apiKey: e.LLM_PROVIDER === "gemini" ? e.GEMINI_API_KEY : e.ANTHROPIC_API_KEY,
    llmCallTimeoutMs: e.LLM_CALL_TIMEOUT,
    llmRPM: e.LLM_RPM,
    budget: {
      maxSteps: e.AGENT_MAX_STEPS,
      maxRequests: e.LLM_MAX_REQUESTS_PER_RUN,
      maxCostUSD: e.AGENT_MAX_COST_USD,
      timeoutMs: e.AGENT_TIMEOUT,
    },
  };
}
