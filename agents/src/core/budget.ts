// Budgets for one agent run: steps (LLM calls), cost and wall-clock time.
// Hitting any of them throws BudgetExceeded, which ends the run with status
// budget_exceeded.

export type BudgetKind = "steps" | "requests" | "cost" | "time";

export class BudgetExceeded extends Error {
  constructor(
    readonly kind: BudgetKind,
    message: string,
  ) {
    super(message);
    this.name = "BudgetExceeded";
  }
}

export interface BudgetLimits {
  maxSteps: number; // LLM calls the agent makes
  maxRequests: number; // requests sent to the provider, retries included
  maxCostUSD: number;
  timeoutMs: number;
}

// limitsFor applies an agent's own, tighter limits to the configured ones.
// An agent can only shorten a budget, never extend it past the
// configuration (the server's kill backstop is based on AGENT_TIMEOUT).
export function limitsFor(configured: BudgetLimits, agent: Partial<BudgetLimits> = {}): BudgetLimits {
  return {
    maxSteps: Math.min(configured.maxSteps, agent.maxSteps ?? Infinity),
    maxRequests: Math.min(configured.maxRequests, agent.maxRequests ?? Infinity),
    maxCostUSD: Math.min(configured.maxCostUSD, agent.maxCostUSD ?? Infinity),
    timeoutMs: Math.min(configured.timeoutMs, agent.timeoutMs ?? Infinity),
  };
}

export class Budget {
  private stepCount = 0;
  private requestCount = 0;
  private spent = 0;
  private readonly startedAt: number;

  constructor(
    readonly limits: BudgetLimits,
    private readonly now: () => number = Date.now,
  ) {
    this.startedAt = now();
  }

  get steps(): number {
    return this.stepCount;
  }

  get requests(): number {
    return this.requestCount;
  }

  get costUSD(): number {
    return this.spent;
  }

  elapsedMs(): number {
    return this.now() - this.startedAt;
  }

  remainingMs(): number {
    return Math.max(0, this.limits.timeoutMs - this.elapsedMs());
  }

  // check runs before every LLM call.
  check(): void {
    const { maxSteps, maxCostUSD, timeoutMs } = this.limits;
    if (this.stepCount >= maxSteps) {
      throw new BudgetExceeded("steps", `step budget exhausted (${this.stepCount} of ${maxSteps} steps)`);
    }
    this.checkRequests();
    if (this.spent >= maxCostUSD) {
      throw new BudgetExceeded("cost", `cost budget exhausted ($${this.spent.toFixed(4)} of $${maxCostUSD.toFixed(2)})`);
    }
    if (this.remainingMs() === 0) {
      throw new BudgetExceeded("time", `time budget exhausted (${Math.round(timeoutMs / 1000)} s)`);
    }
  }

  // takeRequest counts one request to the provider. It runs before every
  // attempt, retries included, since each one uses daily quota.
  takeRequest(): void {
    this.checkRequests();
    this.requestCount += 1;
  }

  private checkRequests(): void {
    if (this.requestCount >= this.limits.maxRequests) {
      throw new BudgetExceeded(
        "requests",
        `request budget exhausted (${this.requestCount} of ${this.limits.maxRequests} LLM requests, LLM_MAX_REQUESTS_PER_RUN)`,
      );
    }
  }

  // deadlineMs is when the wall-clock budget runs out (epoch ms).
  deadlineMs(): number {
    return Date.now() + this.remainingMs();
  }

  // timeExceeded is the error for work cut off by signal().
  timeExceeded(): BudgetExceeded {
    return new BudgetExceeded("time", `time budget exhausted (${Math.round(this.limits.timeoutMs / 1000)} s)`);
  }

  recordStep(costUSD: number): void {
    this.stepCount += 1;
    this.spent += costUSD;
  }

  // signal aborts when the wall-clock budget runs out, so a slow LLM call
  // can't outlive it.
  signal(): AbortSignal {
    return AbortSignal.timeout(Math.max(1, this.remainingMs()));
  }
}
