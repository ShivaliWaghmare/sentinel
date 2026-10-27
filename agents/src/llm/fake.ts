// FakeProvider replays scripted responses, for unit tests. It records every
// request so tests can check what the agent loop sent.

import type { GenerateRequest, GenerateResponse, LLMProvider, ToolCall, Usage } from "./types.ts";

export type ScriptedStep =
  | { text?: string; toolCalls?: Omit<ToolCall, "id">[]; usage?: Usage }
  | { error: Error }
  // hang never answers; it rejects when the request's signal aborts, like
  // a stuck HTTP request cut off by a timeout.
  | { hang: true };

export class FakeProvider implements LLMProvider {
  readonly name = "fake";
  readonly requests: GenerateRequest[] = [];
  private next = 0;
  private callIds = 0;

  constructor(
    private readonly script: ScriptedStep[],
    readonly model = "fake-model",
  ) {}

  async generate(req: GenerateRequest): Promise<GenerateResponse> {
    // Copy the history: the loop keeps appending to the same array.
    this.requests.push({ ...req, messages: [...req.messages] });
    const step = this.script[this.next];
    if (step === undefined) throw new Error(`FakeProvider: script exhausted after ${this.next} responses`);
    this.next += 1;
    if ("error" in step) throw step.error;
    if ("hang" in step) {
      const signal = req.signal;
      return new Promise((_resolve, reject) => {
        if (signal === undefined) return; // hangs forever
        if (signal.aborted) reject(signal.reason);
        signal.addEventListener("abort", () => reject(signal.reason), { once: true });
      });
    }
    return {
      model: this.model,
      text: step.text,
      toolCalls: step.toolCalls?.map((c) => ({ ...c, id: `call_${++this.callIds}` })),
      usage: step.usage ?? { inputTokens: 100, outputTokens: 20 },
    };
  }

  get calls(): number {
    return this.requests.length;
  }
}
