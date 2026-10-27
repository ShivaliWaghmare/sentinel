// Anthropic adapter (@anthropic-ai/sdk). Function calling uses tools with
// input_schema; structured output uses output_config.format (json_schema).

import Anthropic, { APIConnectionError, APIError } from "@anthropic-ai/sdk";
import type { ContentBlockParam, ImageBlockParam, MessageParam } from "@anthropic-ai/sdk/resources/messages";
import { toJSONSchema } from "./json-schema.ts";
import {
  isRetryableStatus,
  LLMError,
  type GenerateRequest,
  type GenerateResponse,
  type ImagePart,
  type LLMProvider,
  type Message,
  type ToolCall,
} from "./types.ts";

const MAX_TOKENS = 8192;

export class AnthropicProvider implements LLMProvider {
  readonly name = "anthropic";
  private readonly client: Anthropic;

  constructor(
    apiKey: string,
    readonly model: string,
  ) {
    // maxRetries 0: RetryingProvider does the retries (and reports them).
    this.client = new Anthropic({ apiKey, maxRetries: 0 });
  }

  async generate(req: GenerateRequest): Promise<GenerateResponse> {
    let res: Anthropic.Message;
    try {
      res = await this.client.messages.create(
        {
          model: this.model,
          max_tokens: MAX_TOKENS,
          system: req.system,
          messages: toMessages(req.messages, req.images),
          tools: req.tools?.map((t) => ({
            name: t.name,
            description: t.description,
            input_schema: { type: "object" as const, ...t.parameters },
          })),
          ...(req.schema
            ? { output_config: { format: { type: "json_schema" as const, schema: toJSONSchema(req.schema) } } }
            : {}),
        },
        { signal: req.signal },
      );
    } catch (err) {
      throw toLLMError(err, req.signal);
    }

    const text = res.content
      .flatMap((b) => (b.type === "text" ? [b.text] : []))
      .join("");
    const toolCalls: ToolCall[] = res.content.flatMap((b) =>
      b.type === "tool_use" ? [{ id: b.id, name: b.name, args: b.input }] : [],
    );
    const u = res.usage;
    return {
      model: this.model,
      text: text === "" ? undefined : text,
      toolCalls: toolCalls.length ? toolCalls : undefined,
      usage: {
        inputTokens: u.input_tokens + (u.cache_creation_input_tokens ?? 0) + (u.cache_read_input_tokens ?? 0),
        outputTokens: u.output_tokens,
      },
    };
  }
}

function toMessages(messages: Message[], extraImages: ImagePart[] | undefined): MessageParam[] {
  const lastUser = messages.map((m) => m.role).lastIndexOf("user");
  return messages.map((m, i): MessageParam => {
    switch (m.role) {
      case "user": {
        const images = [...(m.images ?? []), ...(i === lastUser ? (extraImages ?? []) : [])];
        return { role: "user", content: [{ type: "text", text: m.text }, ...images.map(imageBlock)] };
      }
      case "assistant": {
        const content: ContentBlockParam[] = [];
        if (m.text) content.push({ type: "text", text: m.text });
        for (const c of m.toolCalls ?? []) {
          content.push({ type: "tool_use", id: c.id, name: c.name, input: c.args });
        }
        return { role: "assistant", content };
      }
      case "tool":
        return {
          role: "user",
          content: m.results.map((r) => ({
            type: "tool_result" as const,
            tool_use_id: r.callId,
            content: r.output,
            is_error: r.isError,
          })),
        };
    }
  });
}

function imageBlock(img: ImagePart): ImageBlockParam {
  return { type: "image", source: { type: "base64", media_type: img.mimeType, data: img.dataBase64 } };
}

function toLLMError(err: unknown, signal: AbortSignal | undefined): unknown {
  if (signal?.aborted) return err; // the caller's deadline, not a provider error
  // Connection errors (including timeouts) have no status and are transient.
  if (err instanceof APIConnectionError) {
    return new LLMError(`anthropic: network error: ${err.message}`, true, { reason: "network" });
  }
  if (err instanceof APIError && err.status !== undefined) {
    const status = err.status;
    // retry-after is in seconds on 429 and 529 responses.
    const header = Number(err.headers?.get("retry-after"));
    const retryAfterMs = Number.isFinite(header) && header > 0 ? header * 1000 : undefined;
    const label = status === 429 ? "rate limited (HTTP 429)" : `HTTP ${status}`;
    return new LLMError(`anthropic: ${label}: ${err.message.slice(0, 300)}`, isRetryableStatus(status), {
      status,
      reason: status === 429 ? "rate_limited" : status === 529 ? "overloaded" : undefined,
      retryAfterMs,
    });
  }
  return err;
}
