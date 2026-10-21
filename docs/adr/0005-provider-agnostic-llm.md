# ADR-0005: Provider-agnostic LLM layer, Gemini by default
Status: Accepted

## Context
Development needs a cheap provider; results should not depend on one vendor.

## Decision
An LLMProvider interface with adapters for Gemini (default, free tier for development) and Anthropic. The model name comes only from the environment. Prices live in one pricing table.

## Consequences
- Switching provider is an environment change.
- Eval results record provider and model.
- Missing features degrade gracefully (LLM_VISION=false disables screenshots).
