# ADR-0001: Go orchestrator, TypeScript agents as subprocesses
Status: Accepted

## Context
The service (scheduler, API, notifications) fits Go well. Playwright's most complete API is TypeScript, and the agents need to control browsers.

## Decision
Go owns state, scheduling and the API. Agents are a TypeScript CLI spawned once per agent run. They communicate through an input JSON file and JSONL events on stdout (protocol in ARCHITECTURE.md).

## Consequences
- Each agent run is an isolated process that can be killed on timeout or cancel.
- The event stream is easy to store, replay and test.
- Two languages to maintain; the event protocol must change on both sides together.
- About a second of process start-up per agent run, which is negligible here.
