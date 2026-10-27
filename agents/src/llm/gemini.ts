// Gemini adapter (@google/genai). Function calling uses
// FunctionDeclaration.parametersJsonSchema; structured output uses
// responseMimeType "application/json" with responseJsonSchema.

import { z } from "zod";
import { ApiError, GoogleGenAI, type Content, type GenerateContentResponse, type Part } from "@google/genai";
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

const PROVIDER = "gemini";
// Gemini may return function calls without an id. We make one up so the
// agent loop can pair calls and results, and leave it out when replying.
const LOCAL_ID_PREFIX = "gemini-local-";

export class GeminiProvider implements LLMProvider {
  readonly name = PROVIDER;
  private readonly client: GoogleGenAI;
  private localIds = 0;

  constructor(
    apiKey: string,
    readonly model: string,
  ) {
    // No retryOptions: the SDK doesn't retry, RetryingProvider does.
    this.client = new GoogleGenAI({ apiKey });
  }

  async generate(req: GenerateRequest): Promise<GenerateResponse> {
    let res: GenerateContentResponse;
    try {
      res = await this.client.models.generateContent({
        model: this.model,
        contents: toContents(req.messages, req.images),
        config: {
          systemInstruction: req.system,
          tools: req.tools?.length
            ? [
                {
                  functionDeclarations: req.tools.map((t) => ({
                    name: t.name,
                    description: t.description,
                    parametersJsonSchema: t.parameters,
                  })),
                },
              ]
            : undefined,
          ...(req.schema ? { responseMimeType: "application/json", responseJsonSchema: toJSONSchema(req.schema) } : {}),
          abortSignal: req.signal,
        },
      });
    } catch (err) {
      throw toLLMError(err, req.signal);
    }

    const content = res.candidates?.[0]?.content;
    const parts = content?.parts ?? [];
    const text = parts
      .filter((p) => p.text !== undefined && !p.thought)
      .map((p) => p.text)
      .join("");
    const toolCalls: ToolCall[] = parts
      .filter((p) => p.functionCall !== undefined)
      .map((p) => ({
        id: p.functionCall?.id ?? `${LOCAL_ID_PREFIX}${++this.localIds}`,
        name: p.functionCall?.name ?? "",
        args: p.functionCall?.args ?? {},
      }));
    const usage = res.usageMetadata;
    return {
      model: this.model,
      text: text === "" ? undefined : text,
      toolCalls: toolCalls.length ? toolCalls : undefined,
      usage: {
        inputTokens: (usage?.promptTokenCount ?? 0) + (usage?.toolUsePromptTokenCount ?? 0),
        // Thinking tokens are billed as output.
        outputTokens: (usage?.candidatesTokenCount ?? 0) + (usage?.thoughtsTokenCount ?? 0),
      },
      // The model's turn is sent back as is: it carries thought signatures
      // that Gemini requires when function calling continues.
      providerData: content ? { provider: PROVIDER, raw: content } : undefined,
    };
  }
}

function toContents(messages: Message[], extraImages: ImagePart[] | undefined): Content[] {
  const lastUser = messages.map((m) => m.role).lastIndexOf("user");
  return messages.map((m, i): Content => {
    switch (m.role) {
      case "user": {
        const images = [...(m.images ?? []), ...(i === lastUser ? (extraImages ?? []) : [])];
        return { role: "user", parts: [{ text: m.text }, ...images.map(imagePart)] };
      }
      case "assistant": {
        if (m.providerData?.provider === PROVIDER) return m.providerData.raw as Content;
        const parts: Part[] = [];
        if (m.text) parts.push({ text: m.text });
        for (const c of m.toolCalls ?? []) {
          parts.push({ functionCall: { id: replyId(c.id), name: c.name, args: asRecord(c.args) } });
        }
        return { role: "model", parts };
      }
      case "tool":
        return {
          role: "user",
          parts: m.results.map((r) => ({
            functionResponse: {
              id: replyId(r.callId),
              name: r.name,
              response: r.isError ? { error: r.output } : { output: r.output },
            },
          })),
        };
    }
  });
}

function imagePart(img: ImagePart): Part {
  return { inlineData: { mimeType: img.mimeType, data: img.dataBase64 } };
}

function replyId(id: string): string | undefined {
  return id.startsWith(LOCAL_ID_PREFIX) ? undefined : id;
}

function asRecord(v: unknown): Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v) ? (v as Record<string, unknown>) : {};
}

function toLLMError(err: unknown, signal: AbortSignal | undefined): unknown {
  if (signal?.aborted) return err; // the caller's deadline, not a provider error
  if (err instanceof ApiError) return geminiError(err.status, err.message);
  // fetch reports network failures (DNS, reset, refused) as TypeError.
  if (err instanceof TypeError) return new LLMError(`gemini: network error: ${err.message}`, true, { reason: "network" });
  return err;
}

// The error body Gemini returns (google.rpc.Status). The SDK puts it, as
// JSON text, in ApiError.message. Only the fields we use are listed.
const GeminiErrorBody = z.object({
  error: z.object({
    message: z.string().optional(),
    details: z
      .array(
        z.looseObject({
          "@type": z.string(),
          retryDelay: z.string().optional(), // RetryInfo, e.g. "70492s"
          violations: z // QuotaFailure
            .array(z.object({ description: z.string().optional(), quotaId: z.string().optional() }))
            .optional(),
        }),
      )
      .optional(),
  }),
});

// geminiError turns an HTTP error into an LLMError. For 429 it reads
// RetryInfo (how long Gemini wants us to wait) and QuotaFailure (which
// quota), so a daily free-tier limit reads "quota exceeded" and a
// per-minute limit reads "rate limited".
export function geminiError(status: number, raw: string): LLMError {
  let body: z.infer<typeof GeminiErrorBody> | undefined;
  try {
    const parsed = GeminiErrorBody.safeParse(JSON.parse(raw));
    if (parsed.success) body = parsed.data;
  } catch {
    // not JSON
  }
  const message = firstLine(body?.error.message ?? raw);
  const details = body?.error.details ?? [];
  const retryDelay = details.find((d) => d.retryDelay !== undefined)?.retryDelay;
  const retryAfterMs = retryDelay === undefined ? undefined : parseGoogleDuration(retryDelay);
  const violation = details.flatMap((d) => d.violations ?? [])[0];

  if (status === 429) {
    const perDay = /PerDay/i.test(violation?.quotaId ?? "") || /per day/i.test(message);
    const reason = perDay ? "quota_exceeded" : "rate_limited";
    const what = violation?.description ?? message;
    const wait = retryAfterMs === undefined ? "" : `; Gemini asks to retry in ${formatWait(retryAfterMs)}`;
    return new LLMError(`gemini: ${reason === "quota_exceeded" ? "quota exceeded" : "rate limited"} (HTTP 429): ${what}${wait}`, true, {
      status,
      reason,
      retryAfterMs,
    });
  }
  return new LLMError(`gemini: HTTP ${status}: ${message}`, isRetryableStatus(status), {
    status,
    reason: status === 503 ? "overloaded" : undefined,
    retryAfterMs,
  });
}

// parseGoogleDuration reads protobuf Duration JSON like "70492s" or "1.5s".
function parseGoogleDuration(s: string): number | undefined {
  const m = /^(\d+(?:\.\d+)?)s$/.exec(s);
  return m?.[1] === undefined ? undefined : Math.round(Number(m[1]) * 1000);
}

export function formatWait(ms: number): string {
  const s = Math.round(ms / 1000);
  if (s < 120) return `${s} s`;
  if (s < 7200) return `${Math.round(s / 60)} min`;
  return `${Math.floor(s / 3600)} h ${Math.round((s % 3600) / 60)} min`;
}

function firstLine(s: string): string {
  return (s.split("\n")[0] ?? "").trim().slice(0, 300);
}
