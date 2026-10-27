# ADR-0008: Agent run supervision: server-owned seq, layered timeouts, finish tool
Status: Accepted

## Context
The Go supervisor turns an agent CLI's stdout into stored, streamable events (ADR-0001). Agents crash, hang, get cancelled and print malformed lines, and the dashboard needs a stream that always ends and can resume after a reconnect. On the agent side, the loop needs one way to receive a validated final answer from two different providers.

## Decision
- **The server owns seq.** Stored events are numbered 1, 2, 3… per run by the supervisor, and that number is the SSE event ID (Last-Event-ID). The CLI's seq is only checked for order. Warnings the server adds (malformed lines, events after done) and a done it writes itself fit into the same sequence.
- **Every run ends with a done event.** If the CLI exits, is killed or never starts without writing done, the supervisor writes one (failed, cancelled, or the timeout/shutdown reason). SSE handlers close on done.
- **Layered timeouts.** AGENT_TIMEOUT is the agent's own budget: the CLI stops before the next LLM call (and aborts a running one) and ends cleanly with budget_exceeded. The supervisor kills the process group only at AGENT_TIMEOUT + 30 s. Cancel is SIGTERM to the group, then SIGKILL after 10 s.
- **Queue instead of refuse.** Runs beyond AGENT_CONCURRENCY are stored as queued and started oldest first when a slot frees. (Test runs refuse a second active run per app instead; agent runs are cheap to queue and are often started by other lifecycles, such as triage after a test run.)
- **The final answer is a `finish` tool call** whose schema is the agent's output schema, validated with zod and given one corrective retry. Both providers treat it like any other tool, so no provider-specific "final JSON" mode is needed inside the loop.

## Consequences
- Event IDs in the database never collide, and replay from any Last-Event-ID is a single query.
- The CLI's seq values are not visible to clients; debugging the raw CLI output means reading its stdout directly (`npm run cli`).
- A hung agent costs at most AGENT_TIMEOUT + 30 s of wall clock.
- Queued runs wait without a time limit; they are failed at the next server start if the server stops first.
- The model must call `finish` to end a run; a model that keeps answering in plain text is nudged and eventually runs out of steps.
