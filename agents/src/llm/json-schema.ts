// Converts zod schemas to the JSON Schema objects providers accept for tool
// parameters and structured output.

import { z } from "zod";

export function toJSONSchema(schema: z.ZodType): Record<string, unknown> {
  // $schema is noise to the providers and Gemini rejects unknown keywords.
  const { $schema: _ignored, ...rest } = z.toJSONSchema(schema);
  return rest;
}
