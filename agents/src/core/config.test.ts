import { describe, expect, it } from "vitest";
import { recordingEvents } from "./test-helpers.ts";
import { loadRuntimeConfig, parseDuration } from "./config.ts";
import { CostMeter } from "../llm/pricing.ts";
import { limitsFor } from "./budget.ts";
import { PROBE_BUDGET } from "../agents/probe/index.ts";

describe("parseDuration", () => {
  it.each([
    ["20m", 1_200_000],
    ["90s", 90_000],
    ["1h30m", 5_400_000],
    ["500ms", 500],
    ["1.5s", 1500],
  ])("%s", (s, ms) => {
    expect(parseDuration(s)).toBe(ms);
  });

  it.each(["", "20", "m", "20 m", "20d"])("rejects %j", (s) => {
    expect(() => parseDuration(s)).toThrow();
  });
});

describe("loadRuntimeConfig", () => {
  it("applies defaults and picks the selected provider's key", () => {
    const cfg = loadRuntimeConfig({ LLM_MODEL: "m", GEMINI_API_KEY: "g", ANTHROPIC_API_KEY: "a", AGENT_MAX_STEPS: "" });
    expect(cfg).toEqual({
      provider: "gemini",
      model: "m",
      vision: true,
      apiKey: "g",
      llmCallTimeoutMs: 60_000,
      llmRPM: 5,
      budget: { maxSteps: 80, maxRequests: 15, maxCostUSD: 1, timeoutMs: 1_200_000 },
    });
  });

  it("reads LLM_CALL_TIMEOUT and rejects a zero duration", () => {
    expect(loadRuntimeConfig({ LLM_CALL_TIMEOUT: "45s" }).llmCallTimeoutMs).toBe(45_000);
    expect(() => loadRuntimeConfig({ LLM_CALL_TIMEOUT: "0s" })).toThrow(/LLM_CALL_TIMEOUT/);
  });

  it("reads LLM_RPM (0 = off) and LLM_MAX_REQUESTS_PER_RUN", () => {
    const cfg = loadRuntimeConfig({ LLM_RPM: "0", LLM_MAX_REQUESTS_PER_RUN: "40" });
    expect(cfg.llmRPM).toBe(0);
    expect(cfg.budget.maxRequests).toBe(40);
    expect(() => loadRuntimeConfig({ LLM_RPM: "-1" })).toThrow(/LLM_RPM/);
    expect(() => loadRuntimeConfig({ LLM_MAX_REQUESTS_PER_RUN: "0" })).toThrow(/LLM_MAX_REQUESTS_PER_RUN/);
  });

  it("reports invalid values", () => {
    expect(() => loadRuntimeConfig({ LLM_PROVIDER: "openai", AGENT_TIMEOUT: "soon" })).toThrow(/LLM_PROVIDER[\s\S]*AGENT_TIMEOUT|AGENT_TIMEOUT[\s\S]*LLM_PROVIDER/);
  });
});

describe("CostMeter", () => {
  it("prices known models and warns once for unknown ones", async () => {
    const { events, recorded } = recordingEvents();
    const meter = new CostMeter(events, { known: { inputPerM: 2, outputPerM: 10 } });

    expect(meter.cost("known", { inputTokens: 1_000_000, outputTokens: 100_000 })).toBeCloseTo(3);
    expect(meter.cost("mystery", { inputTokens: 5, outputTokens: 5 })).toBe(0);
    expect(meter.cost("mystery", { inputTokens: 5, outputTokens: 5 })).toBe(0);

    const warnings = recorded().filter((e) => e.type === "warning");
    expect(warnings).toHaveLength(1);
    expect(warnings[0]?.data.message).toContain("mystery");
  });
});

describe("limitsFor", () => {
  const configured = { maxSteps: 80, maxRequests: 40, maxCostUSD: 1, timeoutMs: 1_200_000 };

  it("gives the probe 15 requests and 4 minutes", () => {
    expect(limitsFor(configured, PROBE_BUDGET)).toEqual({ ...configured, maxRequests: 15, timeoutMs: 240_000 });
  });

  it("never extends the configured limits", () => {
    const tight = { ...configured, maxRequests: 5, timeoutMs: 60_000 };
    expect(limitsFor(tight, PROBE_BUDGET)).toEqual(tight);
    expect(limitsFor(configured)).toEqual(configured);
  });
});
