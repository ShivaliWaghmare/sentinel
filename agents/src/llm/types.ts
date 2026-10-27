// The provider-neutral LLM interface (ARCHITECTURE.md, src/llm/).
// Adapters translate these types to and from one SDK; nothing outside
// src/llm/ imports an SDK.

import type { z } from "zod";

export interface ImagePart {
  mimeType: "image/png" | "image/jpeg";
  dataBase64: string;
}

export interface ToolCall {
  id: string;
  name: string;
  args: unknown; // raw model JSON; the agent loop validates it with zod
}

export interface ToolResult {
  callId: string;
  name: string;
  output: string;
  isError: boolean;
}

// Opaque provider data an adapter attaches to its own assistant turns, so it
// can send them back unchanged (Gemini thought signatures, for example).
export interface ProviderData {
  provider: string;
  raw: unknown;
}

export type Message =
  | { role: "user"; text: string; images?: ImagePart[] }
  | { role: "assistant"; text?: string; toolCalls?: ToolCall[]; providerData?: ProviderData }
  // The results for every tool call of the preceding assistant turn.
  | { role: "tool"; results: ToolResult[] };

export interface ToolDeclaration {
  name: string;
  description: string;
  parameters: Record<string, unknown>; // JSON Schema of an object
}

export interface GenerateRequest {
  system: string;
  messages: Message[];
  tools?: ToolDeclaration[];
  // Structured output: the reply text is JSON matching this schema. The
  // caller still validates it; models don't always comply.
  schema?: z.ZodType;
  // Images for the latest user message (screenshots).
  images?: ImagePart[];
  signal?: AbortSignal;
  // When the run's budget runs out (epoch ms). A retry that would have to
  // wait past it fails at once instead.
  deadlineMs?: number;
}

export interface Usage {
  inputTokens: number;
  outputTokens: number;
}

export interface GenerateResponse {
  model: string;
  text?: string;
  toolCalls?: ToolCall[];
  usage: Usage;
  providerData?: ProviderData;
  // Filled in by RetryingProvider: requests sent for this call (1 plus
  // retries) and time spent pacing and waiting between them.
  requests?: number;
  waitedMs?: number;
}

export interface LLMProvider {
  readonly name: string;
  readonly model: string;
  generate(req: GenerateRequest): Promise<GenerateResponse>;
}

// Why an LLM call failed, when the adapter can tell. "quota_exceeded" is a
// quota that won't come back soon (for example a free-tier daily limit);
// "rate_limited" is a short-term limit worth waiting for.
export type LLMErrorReason = "rate_limited" | "quota_exceeded" | "overloaded" | "timeout" | "network";

export interface LLMErrorDetails {
  status?: number;
  reason?: LLMErrorReason;
  // How long the provider asked us to wait before retrying.
  retryAfterMs?: number;
}

// LLMError is what adapters throw. retryable marks 429, 5xx, network errors
// and per-call timeouts; everything else (bad request, auth) fails at once.
export class LLMError extends Error {
  readonly status?: number;
  readonly reason?: LLMErrorReason;
  readonly retryAfterMs?: number;

  constructor(
    message: string,
    readonly retryable: boolean,
    details: LLMErrorDetails = {},
  ) {
    super(message);
    this.name = "LLMError";
    this.status = details.status;
    this.reason = details.reason;
    this.retryAfterMs = details.retryAfterMs;
  }
}

export function isRetryableStatus(status: number): boolean {
  return status === 429 || status >= 500;
}
