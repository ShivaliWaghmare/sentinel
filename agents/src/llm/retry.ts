// Retries for transient LLM errors (429, 5xx, network, per-call timeout).
// Every attempt is a request to the provider, so each one is paced
// (LLM_RPM), counted (onAttempt, for LLM_MAX_REQUESTS_PER_RUN) and has its
// own timeout (LLM_CALL_TIMEOUT). When the provider says how long to wait
// (Gemini RetryInfo, Anthropic retry-after), that delay is used instead of
// the backoff; when it is longer than we may wait (over 60 s, or past the
// run's deadline), the call fails at once. Each retry emits a warning event.

import type { Events } from "../core/events.ts";
import { formatWait } from "./gemini.ts";
import { Pacer } from "./pacer.ts";
import { LLMError, type GenerateRequest, type GenerateResponse, type LLMProvider } from "./types.ts";

export interface RetryOptions {
  maxAttempts: number; // including the first call
  baseDelayMs: number;
  maxDelayMs: number;
  callTimeoutMs: number; // per attempt
  maxRetryAfterMs: number; // longest provider-requested wait we honor
  rpm: number; // requests per minute; 0 = no pacing
  // onAttempt runs before every request is sent. Throwing (a request
  // budget) stops the call without sending it.
  onAttempt?: () => void;
  sleep: (ms: number, signal?: AbortSignal) => Promise<void>;
  random: () => number; // [0, 1)
  now: () => number;
}

export const DEFAULT_RETRY: RetryOptions = {
  maxAttempts: 4,
  baseDelayMs: 2000, // with 4 attempts: up to 2 + 4 + 8 s of waiting
  maxDelayMs: 16_000,
  callTimeoutMs: 60_000,
  maxRetryAfterMs: 60_000,
  rpm: 0,
  sleep: abortableSleep,
  random: Math.random,
  now: Date.now,
};

// backoffDelay is "full jitter": a random delay between 0 and the
// exponential cap, which spreads out clients that failed together.
export function backoffDelay(attempt: number, opts: Pick<RetryOptions, "baseDelayMs" | "maxDelayMs" | "random">): number {
  const cap = Math.min(opts.maxDelayMs, opts.baseDelayMs * 2 ** (attempt - 1));
  return Math.floor(opts.random() * cap);
}

export class RetryingProvider implements LLMProvider {
  readonly name: string;
  readonly model: string;
  private readonly opts: RetryOptions;
  private readonly pacer: Pacer;

  constructor(
    private readonly inner: LLMProvider,
    private readonly events: Events,
    opts: Partial<RetryOptions> = {},
  ) {
    this.name = inner.name;
    this.model = inner.model;
    this.opts = { ...DEFAULT_RETRY, ...opts };
    this.pacer = new Pacer(this.opts.rpm, this.opts.now);
  }

  async generate(req: GenerateRequest): Promise<GenerateResponse> {
    const { maxAttempts, callTimeoutMs, maxRetryAfterMs, sleep, now } = this.opts;
    let waitedMs = 0;
    for (let attempt = 1; ; attempt++) {
      const pace = this.pacer.reserve();
      if (pace > 0) {
        waitedMs += pace;
        await sleep(pace, req.signal);
      }
      this.opts.onAttempt?.();

      const timeout = AbortSignal.timeout(callTimeoutMs);
      const signal = req.signal ? AbortSignal.any([req.signal, timeout]) : timeout;
      let err: unknown;
      try {
        const res = await this.inner.generate({ ...req, signal });
        return { ...res, requests: attempt, waitedMs };
      } catch (e) {
        err = e;
      }
      // The caller's own deadline (the run's budget): not ours to retry.
      if (req.signal?.aborted) throw err;
      if (timeout.aborted) {
        err = new LLMError(`${this.name}: LLM call timed out after ${callTimeoutMs / 1000} s (LLM_CALL_TIMEOUT)`, true, {
          reason: "timeout",
        });
      }
      if (!(err instanceof LLMError) || !err.retryable) throw err;
      // A long requested wait (a daily quota) won't pass within this run.
      if (err.retryAfterMs !== undefined && err.retryAfterMs > maxRetryAfterMs) throw err;
      if (attempt >= maxAttempts) {
        throw new LLMError(`${err.message} (gave up after ${maxAttempts} attempts)`, false, err);
      }

      const asked = err.retryAfterMs !== undefined;
      const delay = asked ? (err.retryAfterMs ?? 0) : backoffDelay(attempt, this.opts);
      // Waiting past the run's deadline only turns a clear "rate limited"
      // into a vague budget_exceeded, so fail now and say why.
      const left = req.deadlineMs === undefined ? Infinity : req.deadlineMs - now();
      if (asked && delay > left) {
        throw new LLMError(
          `${err.message}; not retrying: the requested wait (${formatWait(delay)}) is longer than the ${formatWait(Math.max(0, left))} left in this run's budget`,
          false,
          err,
        );
      }
      this.events.emit("warning", {
        message: `LLM call failed (${err.message}); retry ${attempt} of ${maxAttempts - 1} in ${delay} ms${asked ? " (as the provider asked)" : ""}`,
      });
      waitedMs += delay;
      await sleep(delay, req.signal);
    }
  }
}

function abortableSleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(signal.reason);
      return;
    }
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    const onAbort = (): void => {
      clearTimeout(timer);
      reject(signal?.reason);
    };
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}
