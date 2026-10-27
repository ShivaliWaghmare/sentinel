import { describe, expect, it } from "vitest";
import { recordingEvents } from "../core/test-helpers.ts";
import { FakeProvider } from "./fake.ts";
import { geminiError } from "./gemini.ts";
import { backoffDelay, RetryingProvider, type RetryOptions } from "./retry.ts";
import { LLMError } from "./types.ts";

function retrying(script: ConstructorParameters<typeof FakeProvider>[0], override: Partial<RetryOptions> = {}) {
  const { events, recorded } = recordingEvents();
  const sleeps: number[] = [];
  const opts: RetryOptions = {
    maxAttempts: 4,
    baseDelayMs: 1000,
    maxDelayMs: 16_000,
    callTimeoutMs: 60_000,
    maxRetryAfterMs: 60_000,
    rpm: 0,
    now: Date.now,
    sleep: async (ms) => {
      sleeps.push(ms);
    },
    random: () => 0.5,
  };
  const fake = new FakeProvider(script);
  return { provider: new RetryingProvider(fake, events, { ...opts, ...override }), fake, sleeps, recorded };
}

const req = { system: "s", messages: [] };

describe("RetryingProvider", () => {
  it("retries 429 and 5xx with backoff and a warning each time", async () => {
    const { provider, sleeps, recorded } = retrying([
      { error: new LLMError("HTTP 429", true, { status: 429 }) },
      { error: new LLMError("HTTP 503", true, { status: 503 }) },
      { text: "ok" },
    ]);

    await expect(provider.generate(req)).resolves.toMatchObject({ text: "ok" });
    expect(sleeps).toEqual([500, 1000]);
    const warnings = recorded().filter((e) => e.type === "warning");
    expect(warnings).toHaveLength(2);
    expect(warnings[0]?.data.message).toContain("retry 1 of 3");
  });

  it("gives up after 4 attempts and says so", async () => {
    const err = new LLMError("gemini: rate limited (HTTP 429): too many requests", true, { status: 429, reason: "rate_limited" });
    const { provider, fake } = retrying([{ error: err }, { error: err }, { error: err }, { error: err }, { text: "never" }]);

    const final = await provider.generate(req).catch((e: unknown) => e);
    expect(final).toBeInstanceOf(LLMError);
    expect((final as LLMError).message).toBe("gemini: rate limited (HTTP 429): too many requests (gave up after 4 attempts)");
    expect((final as LLMError).reason).toBe("rate_limited");
    expect(fake.calls).toBe(4);
  });

  it("waits as long as the provider asks instead of backing off", async () => {
    const limited = new LLMError("gemini: rate limited (HTTP 429): per minute", true, { status: 429, retryAfterMs: 7000 });
    const { provider, sleeps, recorded } = retrying([{ error: limited }, { text: "ok" }]);

    await expect(provider.generate(req)).resolves.toMatchObject({ text: "ok" });
    expect(sleeps).toEqual([7000]);
    expect(recorded()[0]?.data.message).toContain("as the provider asked");
  });

  it("fails at once when the requested wait is too long (daily quota)", async () => {
    const quota = new LLMError("gemini: quota exceeded (HTTP 429): per day", true, {
      status: 429,
      reason: "quota_exceeded",
      retryAfterMs: 70_492_000,
    });
    const { provider, fake, sleeps } = retrying([{ error: quota }, { text: "never" }]);

    await expect(provider.generate(req)).rejects.toBe(quota);
    expect(fake.calls).toBe(1);
    expect(sleeps).toEqual([]);
  });

  it("fails fast with rate limited when the requested wait outlasts the run's budget", async () => {
    const limited = new LLMError("gemini: rate limited (HTTP 429): per minute; Gemini asks to retry in 47 s", true, {
      status: 429,
      reason: "rate_limited",
      retryAfterMs: 47_000,
    });
    const { provider, fake, sleeps, recorded } = retrying([{ error: limited }, { text: "never" }]);

    const err = (await provider.generate({ ...req, deadlineMs: Date.now() + 20_000 }).catch((e: unknown) => e)) as LLMError;

    expect(err).toBeInstanceOf(LLMError);
    expect(err.reason).toBe("rate_limited");
    expect(err.retryable).toBe(false);
    expect(err.message).toMatch(/^gemini: rate limited \(HTTP 429\).*not retrying: the requested wait \(47 s\) is longer than the (19|20) s left in this run's budget$/);
    expect(fake.calls).toBe(1);
    expect(sleeps).toEqual([]);
    expect(recorded()).toEqual([]);
  });

  it("still waits when the requested delay fits in the budget", async () => {
    const limited = new LLMError("gemini: rate limited (HTTP 429)", true, { status: 429, retryAfterMs: 5000 });
    const { provider, sleeps } = retrying([{ error: limited }, { text: "ok" }]);

    await expect(provider.generate({ ...req, deadlineMs: Date.now() + 20_000 })).resolves.toMatchObject({ text: "ok" });
    expect(sleeps).toEqual([5000]);
  });

  it("counts every attempt and stops when onAttempt throws", async () => {
    let attempts = 0;
    const stop = new Error("request budget exhausted");
    const busy = new LLMError("HTTP 503", true, { status: 503 });
    const { provider, fake } = retrying([{ error: busy }, { error: busy }, { text: "never" }], {
      onAttempt: () => {
        attempts += 1;
        if (attempts > 2) throw stop;
      },
    });

    await expect(provider.generate(req)).rejects.toBe(stop);
    expect(attempts).toBe(3);
    expect(fake.calls).toBe(2); // the third request was never sent
  });

  it("returns how many requests a call took and how long it waited", async () => {
    const busy = new LLMError("HTTP 503", true, { status: 503 });
    const { provider } = retrying([{ error: busy }, { text: "ok" }]);

    await expect(provider.generate(req)).resolves.toMatchObject({ text: "ok", requests: 2, waitedMs: 500 });
  });

  it("paces requests to LLM_RPM, retries included", async () => {
    let clock = 0;
    const sleeps: number[] = [];
    const { events } = recordingEvents();
    const fake = new FakeProvider([{ text: "a" }, { error: new LLMError("HTTP 503", true, { status: 503 }) }, { text: "b" }, { text: "c" }]);
    const provider = new RetryingProvider(fake, events, {
      rpm: 5, // one request per 12 s
      now: () => clock,
      random: () => 0.25, // backoff 500 ms
      sleep: async (ms) => {
        sleeps.push(ms);
        clock += ms;
      },
    });

    await provider.generate(req); // t=0: no wait
    clock += 1000; // the agent works for 1 s
    await provider.generate(req); // t=1 s: wait 11 s; 503; backoff 0.5 s; wait 11.5 s for the next slot
    await provider.generate(req); // right after: wait a full 12 s

    expect(sleeps).toEqual([11_000, 500, 11_500, 12_000]);
    expect(fake.calls).toBe(4);
  });

  it("times out a stuck call and retries it", async () => {
    const { provider, fake, recorded } = retrying([{ hang: true }, { text: "ok" }], { callTimeoutMs: 20 });

    await expect(provider.generate(req)).resolves.toMatchObject({ text: "ok" });
    expect(fake.calls).toBe(2);
    expect(recorded()[0]?.data.message).toContain("timed out after 0.02 s (LLM_CALL_TIMEOUT)");
  });

  it("reports repeated timeouts as a timeout", async () => {
    const hang = { hang: true } as const;
    const { provider } = retrying([hang, hang, hang, hang], { callTimeoutMs: 10 });

    const final = (await provider.generate(req).catch((e: unknown) => e)) as LLMError;
    expect(final.reason).toBe("timeout");
    expect(final.message).toContain("timed out after 0.01 s (LLM_CALL_TIMEOUT) (gave up after 4 attempts)");
  });

  it("leaves the caller's own deadline alone", async () => {
    const { provider, fake } = retrying([{ hang: true }, { text: "never" }], { callTimeoutMs: 60_000 });
    const deadline = AbortSignal.timeout(10);

    await expect(provider.generate({ ...req, signal: deadline })).rejects.toBeDefined();
    expect(fake.calls).toBe(1);
  });

  it("does not retry other errors", async () => {
    const err = new LLMError("HTTP 400", false, { status: 400 });
    const { provider, fake } = retrying([{ error: err }, { text: "never" }]);

    await expect(provider.generate(req)).rejects.toBe(err);
    expect(fake.calls).toBe(1);
  });
});

describe("backoffDelay", () => {
  it("doubles the cap per attempt up to the maximum", () => {
    const opts = { baseDelayMs: 1000, maxDelayMs: 5000, random: () => 0.999 };
    expect([1, 2, 3, 4].map((a) => backoffDelay(a, opts))).toEqual([999, 1998, 3996, 4995]);
  });
});

describe("geminiError", () => {
  // The body Gemini returned for this project's free-tier key on 2026-10-27.
  const dailyQuota = JSON.stringify({
    error: {
      code: 429,
      message:
        "You exceeded your current quota, please check your plan and billing details.\n* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_requests, limit: 20, model: gemini-3.8-flash\nPlease retry in 19h34m52.62946508s.",
      status: "RESOURCE_EXHAUSTED",
      details: [
        { "@type": "type.googleapis.com/google.rpc.Help", links: [] },
        {
          "@type": "type.googleapis.com/google.rpc.QuotaFailure",
          violations: [
            {
              description: "Generate content requests per day limit exceeded on Free Tier.",
              quotaId: "GenerateRequestsPerDayPerProjectPerModel-FreeTier",
            },
          ],
        },
        { "@type": "type.googleapis.com/google.rpc.RetryInfo", retryDelay: "70492s" },
      ],
    },
  });

  it("reads a daily free-tier limit as quota exceeded with the retry delay", () => {
    const err = geminiError(429, dailyQuota);
    expect(err.reason).toBe("quota_exceeded");
    expect(err.retryAfterMs).toBe(70_492_000);
    expect(err.message).toBe(
      "gemini: quota exceeded (HTTP 429): Generate content requests per day limit exceeded on Free Tier.; Gemini asks to retry in 19 h 35 min",
    );
  });

  it("reads a per-minute limit as rate limited", () => {
    const body = JSON.stringify({
      error: {
        message: "Resource exhausted",
        details: [
          {
            "@type": "type.googleapis.com/google.rpc.QuotaFailure",
            violations: [{ quotaId: "GenerateRequestsPerMinutePerProjectPerModel-FreeTier" }],
          },
          { "@type": "type.googleapis.com/google.rpc.RetryInfo", retryDelay: "12.5s" },
        ],
      },
    });
    const err = geminiError(429, body);
    expect(err.reason).toBe("rate_limited");
    expect(err.retryAfterMs).toBe(12_500);
    expect(err.message).toBe("gemini: rate limited (HTTP 429): Resource exhausted; Gemini asks to retry in 13 s");
  });

  it("keeps other errors readable", () => {
    const overloaded = geminiError(503, JSON.stringify({ error: { message: "This model is currently experiencing high demand." } }));
    expect(overloaded).toMatchObject({ retryable: true, reason: "overloaded" });
    expect(overloaded.message).toBe("gemini: HTTP 503: This model is currently experiencing high demand.");

    const bad = geminiError(400, "not json\nsecond line");
    expect(bad).toMatchObject({ retryable: false, message: "gemini: HTTP 400: not json" });
  });
});
