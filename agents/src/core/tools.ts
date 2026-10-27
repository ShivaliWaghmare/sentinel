// Tools are defined once with a zod schema. The schema validates the model's
// arguments and is converted to the JSON Schema that providers declare.

import type { z } from "zod";
import { toJSONSchema } from "../llm/json-schema.ts";
import type { ToolDeclaration } from "../llm/types.ts";

export interface Tool<S extends z.ZodType = z.ZodType> {
  name: string;
  description: string;
  schema: S;
  // Returns text for the model. Throwing reports an error tool_result,
  // so the model can react to it.
  run(args: z.infer<S>): Promise<string>;
  // redact returns the args as shown in events (for example, hiding a
  // password). Without it, events show the raw args.
  redact?(args: unknown): unknown;
}

// defineTool exists for inference: run's argument type comes from schema.
export function defineTool<S extends z.ZodType>(tool: Tool<S>): Tool<S> {
  return tool;
}

export function toDeclaration(tool: Pick<Tool, "name" | "description" | "schema">): ToolDeclaration {
  return { name: tool.name, description: tool.description, parameters: toJSONSchema(tool.schema) };
}
