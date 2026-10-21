# ADR-0002: Hand-written agent loop, no agent framework
Status: Accepted

## Context
Agent frameworks (LangGraph, CrewAI) speed up demos but hide the loop. For this project, understanding and explaining how tool calls, retries, validation and budgets work is the point.

## Decision
Implement a small agent loop with zod-defined tools, budgets, output validation and event emission. Provider adapters stay thin.

## Consequences
- Full control and explainability; fewer dependencies.
- We own provider-specific message formatting and JSON Schema conversion.
