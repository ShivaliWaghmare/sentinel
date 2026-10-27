import { describe, expect, it } from "vitest";
import { z } from "zod";
import { FakeProvider } from "../llm/fake.ts";
import { RetryingProvider } from "../llm/retry.ts";
import { LLMError } from "../llm/types.ts";
import { CostMeter } from "../llm/pricing.ts";
import type { Message } from "../llm/types.ts";
import { AgentFailed, runAgentLoop, type AgentLoopOptions } from "./agent-loop.ts";
import { Budget, BudgetExceeded } from "./budget.ts";
import { recordingEvents } from "./test-helpers.ts";
import { defineTool } from "./tools.ts";

const Output = z.object({ answer: z.string().min(1), count: z.number().int() });

const echo = defineTool({
  name: "echo",
  description: "Echo the text back",
  schema: z.object({ text: z.string().min(1) }),
  async run({ text }) {
    return `echo: ${text}`;
  },
});

function setup(script: ConstructorParameters<typeof FakeProvider>[0], maxSteps = 10) {
  const { events, recorded } = recordingEvents();
  const provider = new FakeProvider(script);
  const opts: AgentLoopOptions<typeof Output> = {
    provider,
    events,
    budget: new Budget({ maxSteps, maxRequests: 100, maxCostUSD: 1, timeoutMs: 60_000 }),
    cost: new CostMeter(events, { "fake-model": { inputPerM: 1, outputPerM: 2 } }),
    purpose: "test",
    system: "system prompt",
    task: "do the task",
    tools: [echo],
    output: { description: "final answer", schema: Output },
  };
  return { provider, opts, recorded };
}

function lastToolMessage(messages: Message[]) {
  const last = messages.at(-1);
  if (last?.role !== "tool") throw new Error(`expected a tool message, got ${last?.role}`);
  return last.results;
}

describe("runAgentLoop", () => {
  it("runs a tool call, then returns the final answer", async () => {
    const { provider, opts, recorded } = setup([
      { text: "Echoing first.", toolCalls: [{ name: "echo", args: { text: "hi" } }] },
      { toolCalls: [{ name: "finish", args: { answer: "done", count: 1 } }] },
    ]);

    await expect(runAgentLoop(opts)).resolves.toEqual({ answer: "done", count: 1 });

    // The second request carries the tool result, wrapped as untrusted.
    const results = lastToolMessage(provider.requests[1]?.messages ?? []);
    expect(results).toHaveLength(1);
    expect(results[0]?.isError).toBe(false);
    expect(results[0]?.output).toContain('<untrusted source="tool:echo">');
    expect(results[0]?.output).toContain("echo: hi");
    // finish is declared next to the agent's tools.
    expect(provider.requests[0]?.tools?.map((t) => t.name)).toEqual(["echo", "finish"]);

    const types = recorded().map((e) => e.type);
    expect(types).toEqual(["llm_call", "thought", "tool_call", "tool_result", "llm_call", "tool_call", "tool_result"]);
    const llm = recorded()[0]?.data;
    expect(llm).toMatchObject({ model: "fake-model", purpose: "test", tokens_in: 100, tokens_out: 20 });
    expect(llm?.cost_usd).toBeCloseTo((100 * 1 + 20 * 2) / 1e6);
    expect(opts.budget.steps).toBe(2);
  });

  it("returns invalid tool args as an error so the model can correct itself", async () => {
    const { provider, opts, recorded } = setup([
      { toolCalls: [{ name: "echo", args: { text: 42 } }] },
      { toolCalls: [{ name: "echo", args: { text: "fixed" } }] },
      { toolCalls: [{ name: "finish", args: { answer: "ok", count: 2 } }] },
    ]);

    await expect(runAgentLoop(opts)).resolves.toEqual({ answer: "ok", count: 2 });

    const first = lastToolMessage(provider.requests[1]?.messages ?? [])[0];
    expect(first?.isError).toBe(true);
    expect(first?.output).toContain("invalid arguments for echo");
    const second = lastToolMessage(provider.requests[2]?.messages ?? [])[0];
    expect(second?.isError).toBe(false);
    expect(second?.output).toContain("echo: fixed");

    const toolResults = recorded().filter((e) => e.type === "tool_result");
    expect(toolResults.map((e) => e.data.ok)).toEqual([false, true, true]);
  });

  it("reports unknown tools and tool exceptions as error results", async () => {
    const failing = defineTool({
      name: "boom",
      description: "Always fails",
      schema: z.object({}),
      async run() {
        throw new Error("page crashed");
      },
    });
    const { provider, opts } = setup([
      { toolCalls: [{ name: "nope", args: {} }, { name: "boom", args: {} }] },
      { toolCalls: [{ name: "finish", args: { answer: "ok", count: 0 } }] },
    ]);
    opts.tools = [echo, failing];

    await runAgentLoop(opts);

    const [unknown, thrown] = lastToolMessage(provider.requests[1]?.messages ?? []);
    expect(unknown?.output).toContain('unknown tool "nope"');
    expect(thrown?.isError).toBe(true);
    expect(thrown?.output).toContain('<untrusted source="tool:boom:error">');
    expect(thrown?.output).toContain("page crashed");
  });

  it("throws BudgetExceeded when the step budget runs out", async () => {
    const loop = { toolCalls: [{ name: "echo", args: { text: "again" } }] };
    const { provider, opts } = setup([loop, loop, loop, loop], 3);

    const err = await runAgentLoop(opts).catch((e: unknown) => e);

    expect(err).toBeInstanceOf(BudgetExceeded);
    expect((err as BudgetExceeded).kind).toBe("steps");
    expect(provider.calls).toBe(3);
  });

  it("throws BudgetExceeded when the cost budget runs out", async () => {
    const big = { toolCalls: [{ name: "echo", args: { text: "x" } }], usage: { inputTokens: 600_000, outputTokens: 0 } };
    const { opts } = setup([big, big, big]);
    opts.budget = new Budget({ maxSteps: 10, maxRequests: 100, maxCostUSD: 1, timeoutMs: 60_000 });

    const err = await runAgentLoop(opts).catch((e: unknown) => e);

    expect((err as BudgetExceeded).kind).toBe("cost");
    expect(opts.budget.steps).toBe(2); // $0.60 + $0.60 crosses $1 after the second call
  });

  it("ends with BudgetExceeded when the request budget runs out, retries included", async () => {
    const overloaded = { error: new LLMError("gemini: HTTP 503: busy", true, { status: 503 }) };
    const echoCall = { toolCalls: [{ name: "echo", args: { text: "x" } }] };
    // Call 1 needs 2 requests (one retry), call 2 one more: 3 of 3 used.
    const { provider: fake, opts } = setup([overloaded, echoCall, echoCall, echoCall]);
    const budget = new Budget({ maxSteps: 10, maxRequests: 3, maxCostUSD: 1, timeoutMs: 60_000 });
    opts.budget = budget;
    opts.provider = new RetryingProvider(fake, opts.events, {
      onAttempt: () => budget.takeRequest(),
      sleep: async () => undefined,
    });

    const err = await runAgentLoop(opts).catch((e: unknown) => e);

    expect(err).toBeInstanceOf(BudgetExceeded);
    expect((err as BudgetExceeded).kind).toBe("requests");
    expect((err as BudgetExceeded).message).toContain("3 of 3 LLM requests");
    expect(fake.calls).toBe(3);
    expect(budget.steps).toBe(2);
  });

  it("reports requests and waits on llm_call", async () => {
    const { provider: fake, opts, recorded } = setup([
      { error: new LLMError("gemini: HTTP 503: busy", true, { status: 503 }) },
      { toolCalls: [{ name: "finish", args: { answer: "ok", count: 1 } }] },
    ]);
    opts.provider = new RetryingProvider(fake, opts.events, { sleep: async () => undefined, random: () => 0.5 });

    await runAgentLoop(opts);

    const llm = recorded().find((e) => e.type === "llm_call")?.data;
    expect(llm).toMatchObject({ requests: 2, wait_ms: 1000 });
  });

  it("gives an invalid final answer one corrective retry", async () => {
    const { provider, opts } = setup([
      { toolCalls: [{ name: "finish", args: { answer: "", count: "three" } }] },
      { toolCalls: [{ name: "finish", args: { answer: "fixed", count: 3 } }] },
    ]);

    await expect(runAgentLoop(opts)).resolves.toEqual({ answer: "fixed", count: 3 });

    const feedback = lastToolMessage(provider.requests[1]?.messages ?? [])[0];
    expect(feedback?.isError).toBe(true);
    expect(feedback?.output).toContain("final answer does not match the schema");
    expect(feedback?.output).toContain("count");
  });

  it("fails when the final answer is still invalid after the retry", async () => {
    const bad = { toolCalls: [{ name: "finish", args: { answer: "x" } }] };
    const { provider, opts } = setup([bad, bad, bad]);

    await expect(runAgentLoop(opts)).rejects.toBeInstanceOf(AgentFailed);
    expect(provider.calls).toBe(2);
  });

  it("nudges the model when it answers with text only", async () => {
    const { provider, opts } = setup([
      { text: "I think I am done." },
      { toolCalls: [{ name: "finish", args: { answer: "ok", count: 1 } }] },
    ]);

    await runAgentLoop(opts);

    const nudge = provider.requests[1]?.messages.at(-1);
    expect(nudge?.role).toBe("user");
    expect(nudge?.role === "user" && nudge.text).toContain("call finish");
  });

  it("shows redacted args in events but runs the tool with the real ones", async () => {
    let received = "";
    const secret = defineTool({
      name: "secret",
      description: "Takes a secret",
      schema: z.object({ value: z.string() }),
      async run({ value }) {
        received = value;
        return "ok";
      },
      redact: () => ({ value: "[redacted]" }),
    });
    const { opts, recorded } = setup([
      { toolCalls: [{ name: "secret", args: { value: "hunter2" } }] },
      { toolCalls: [{ name: "finish", args: { answer: "ok", count: 1 } }] },
    ]);
    opts.tools = [secret];

    await runAgentLoop(opts);

    expect(received).toBe("hunter2");
    expect(JSON.stringify(recorded())).not.toContain("hunter2");
  });

  it("numbers events with strictly increasing seq", async () => {
    const { opts, recorded } = setup([
      { text: "a", toolCalls: [{ name: "echo", args: { text: "1" } }, { name: "echo", args: { text: "2" } }] },
      { toolCalls: [{ name: "echo", args: {} }] },
      { toolCalls: [{ name: "finish", args: { answer: "ok", count: 1 } }] },
    ]);

    await runAgentLoop(opts);

    const seqs = recorded().map((e) => e.seq);
    expect(seqs.length).toBeGreaterThan(5);
    expect(seqs[0]).toBe(1);
    for (let i = 1; i < seqs.length; i++) expect(seqs[i]).toBe((seqs[i - 1] ?? 0) + 1);
  });
});
