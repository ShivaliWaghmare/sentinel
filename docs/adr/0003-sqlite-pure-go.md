# ADR-0003: SQLite through a pure-Go driver
Status: Accepted

## Context
The system needs durable storage for apps, schedules, runs and agent events at small scale, with zero setup.

## Decision
One SQLite file in DATA_DIR, WAL mode, modernc.org/sqlite (no CGO), numbered migrations embedded in the binary, hand-written SQL.

## Consequences
- Zero setup locally; simple Docker image.
- Single writer, which is fine for this scale.
- Deployments need a persistent volume.
