// createProvider builds the configured provider, wrapped with retries,
// pacing (LLM_RPM) and the per-call timeout. onAttempt runs before every
// request (the CLI counts them against LLM_MAX_REQUESTS_PER_RUN).

import type { RuntimeConfig } from "../core/config.ts";
import type { Events } from "../core/events.ts";
import { AnthropicProvider } from "./anthropic.ts";
import { GeminiProvider } from "./gemini.ts";
import { RetryingProvider } from "./retry.ts";
import type { LLMProvider } from "./types.ts";

export function createProvider(cfg: RuntimeConfig, events: Events, onAttempt?: () => void): LLMProvider {
  if (cfg.model === "") throw new Error("LLM_MODEL is not set (see .env.example)");
  const keyName = cfg.provider === "gemini" ? "GEMINI_API_KEY" : "ANTHROPIC_API_KEY";
  if (cfg.apiKey === "") throw new Error(`${keyName} is not set (LLM_PROVIDER=${cfg.provider})`);
  const inner =
    cfg.provider === "gemini" ? new GeminiProvider(cfg.apiKey, cfg.model) : new AnthropicProvider(cfg.apiKey, cfg.model);
  return new RetryingProvider(inner, events, { callTimeoutMs: cfg.llmCallTimeoutMs, rpm: cfg.llmRPM, onAttempt });
}
