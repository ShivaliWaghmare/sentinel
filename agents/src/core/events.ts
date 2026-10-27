// The event emitter: the only writer to stdout (agent event protocol in
// ARCHITECTURE.md). Each event is one JSON line with an increasing seq.

export type RunStatus = "succeeded" | "failed" | "budget_exceeded" | "cancelled";

export interface EventMap {
  run_started: { agent: string; input_summary: Record<string, unknown>; prompts: Record<string, string> };
  phase: { name: string; detail?: string };
  llm_call: {
    model: string;
    purpose: string;
    tokens_in: number;
    tokens_out: number;
    cost_usd: number;
    latency_ms: number; // includes wait_ms
    requests: number; // sent for this call: 1 plus retries
    wait_ms: number; // pacing and retry waits
  };
  thought: { text: string };
  tool_call: { id: string; tool: string; args: unknown };
  tool_result: { id: string; ok: boolean; output_preview: string; error?: string };
  artifact: { kind: string; path: string; meta?: Record<string, unknown> };
  test_validation: { file: string; attempt: number; passed: boolean; error?: string };
  warning: { message: string };
  done: { status: RunStatus; summary: Record<string, unknown>; error?: string };
}

export type EventType = keyof EventMap;

// A sink receives complete lines (without the newline). The default writes
// to stdout; tests collect lines in an array.
export type Sink = (line: string) => Promise<void>;

export const stdoutSink: Sink = (line) =>
  new Promise((resolve, reject) => {
    process.stdout.write(line + "\n", (err) => (err ? reject(err) : resolve()));
  });

export class Events {
  private seq = 0;
  private finished = false;
  private pending: Promise<void> = Promise.resolve();

  constructor(
    private readonly sink: Sink = stdoutSink,
    private readonly now: () => Date = () => new Date(),
  ) {}

  // emit writes one event and returns its seq. After done, further events
  // are dropped (done is always the last line) and logged to stderr.
  emit<K extends EventType>(type: K, data: EventMap[K]): number {
    if (this.finished) {
      console.error(`event after done dropped: ${type}`);
      return this.seq;
    }
    if (type === "done") this.finished = true;
    this.seq += 1;
    const line = JSON.stringify({ seq: this.seq, ts: this.now().toISOString(), type, data });
    // A broken stdout leaves nobody to report to; log and carry on.
    const write = this.sink(line).catch((err: unknown) => console.error("write event:", err));
    this.pending = this.pending.then(() => write);
    return this.seq;
  }

  get isDone(): boolean {
    return this.finished;
  }

  // flush resolves once every emitted line has been written, so the process
  // can exit without losing the last events (stdout pipes are async on macOS).
  flush(): Promise<void> {
    return this.pending;
  }
}

// redirectConsole sends console.log/info/debug to stderr, so stray logging
// from our code or a library can never corrupt the event stream.
export function redirectConsole(): void {
  const toStderr = (...args: unknown[]): void => console.error(...args);
  console.log = toStderr;
  console.info = toStderr;
  console.debug = toStderr;
}
