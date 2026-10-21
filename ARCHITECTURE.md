# Architecture

## Overview
```
              ┌────────────────────────────────┐
              │   Next.js dashboard (web/)     │
              └───────────────┬────────────────┘
                    REST + SSE│
┌─────────────────────────────▼────────────────────────────────────┐
│ Go service (server/)                                              │
│ api · scheduler (cron) · runs service · agent supervisor · notify │
│                     SQLite: data/sentinel.db                      │
└───────┬──────────────────────────────────┬───────────────────────┘
        │ spawns                            │ spawns, reads JSONL events
        ▼                                   ▼
 npx playwright test                 tsx src/cli.ts <explore|triage|heal|probe>
 (agents/tests/<slug>)                ├─ LLM layer (Gemini | Anthropic)
        │                             └─ Playwright browser tools
        ▼                                   │
   target web app (e.g. demo-app :4000) ◄───┘
```
Notifications go to a Slack Incoming Webhook and SMTP email.

## Components
### Go service (server/)
- config: loads .env from the repo root, applies defaults, resolves relative paths to absolute ones and logs them at startup.
- store: SQLite access and embedded migrations.
- api: REST handlers, CORS, JSON helpers, read-only mode middleware.
- scheduler: robfig/cron with the TIMEZONE location. Loads enabled schedules at boot; adds and removes entries when the API changes them.
- runner: spawns Playwright and parses its JSON report.
- runs: orchestrates a test run end to end (lifecycle 1). At most one active run per app.
- agents: supervisor that spawns the agent CLI, reads stdout JSONL, stores events, publishes them to SSE and applies results.
- sse: in-memory pub/sub broker keyed by agent run ID.
- notify: Slack and email formatting and sending.

### Agents (agents/)
- src/cli.ts: entry point. `tsx src/cli.ts <kind> --input <file.json>`. Reads and validates the input, writes events as JSONL to stdout, logs to stderr. Exit 0 when the run completes (even if it found nothing); non-zero only on crashes.
- src/llm/: LLMProvider interface `generate({ system, messages, tools?, schema?, images? }) -> { text?, toolCalls?, usage }`; adapters gemini.ts and anthropic.ts; fake.ts for tests; pricing.ts.
- src/core/: agent loop, tool registry (zod-defined tools), budget tracker, event emitter, retry with backoff, untrusted-content helpers.
- src/browser/: Playwright session, snapshots (aria snapshot + interactive elements), origin guard, destructive-action guard.
- src/agents/{probe,explorer,triage,healer}/
- src/prompts/: prompt templates (*.md), each with a version header.
- tests/_support/fixtures.ts: shared fixture; on failure it attaches the page URL and an aria snapshot.

## Data model (SQLite)
Timestamps are stored as INTEGER unix milliseconds; the API returns RFC 3339 strings.
IDs are INTEGER PRIMARY KEY AUTOINCREMENT, so a deleted row's ID is never reused (IDs appear in URLs and name folders such as data/runs/<id>/). Migrations are numbered files in server/internal/store/migrations/, applied at startup and tracked in schema_migrations.

- apps: id, name, slug (unique, never changes; ADR-0006), base_url, notes (guidance for agents, e.g. demo login), allow_destructive (bool), created_at
- schedules: id, app_id → apps, cron, enabled, created_at
- test_cases: id, app_id, file_path, title, origin (manual|generated), status (active|quarantined|retired), agent_run_id (nullable), created_at. Unique (app_id, file_path, title).
- runs: id, app_id, trigger (manual|schedule|eval|heal), status (queued|running|passed|failed|error), total, passed, failed, flaky, skipped, duration_ms, error, started_at, finished_at
- test_results: id, run_id, test_case_id (nullable), title, file_path, line, status (passed|failed|flaky|skipped), duration_ms, error, retries, artifacts_json (screenshot, trace, aria snapshot, url)
- agent_runs: id, app_id, kind (probe|explore|triage|heal), status (queued|running|succeeded|failed|budget_exceeded|cancelled), input_json, summary_json, steps, tokens_in, tokens_out, cost_usd, started_at, finished_at, error
- agent_events: id, agent_run_id, seq, type, payload_json, created_at. Unique (agent_run_id, seq).
- triage_results: id, test_result_id (unique), agent_run_id (nullable for rule-based), category (real_bug|ui_change|flaky|test_bug|environment), confidence (0–1), summary, explanation, suggested_action, method (rule|llm), created_at
- heal_proposals: id, app_id, test_case_id, test_result_id, agent_run_id, file_path, base_sha256, new_content_path, patch (unified diff for display), reasoning, verified (bool), status (pending|approved|rejected|applied|failed), created_at, decided_at

## Lifecycles
### 1. Test run (manual or scheduled)
1. POST /api/apps/{id}/runs, or a cron entry fires. The runs service refuses if the app already has an active run, then inserts a run with status running.
2. runner: `npx playwright test tests/<slug> --reporter=json` in agents/, with BASE_URL, PLAYWRIGHT_JSON_OUTPUT_NAME=data/runs/<id>/report.json and PW_OUTPUT_DIR=data/runs/<id>/artifacts. One retry. Killed after RUN_TIMEOUT.
3. Parse the report into test_results and upsert test_cases. A non-zero exit with a report is a normal failing run; no report means status error.
4. If tests failed: triage. Rules first (passed on retry → flaky; connection refused, DNS or net::ERR_ errors → environment). Remaining failures go to one triage agent run; wait up to TRIAGE_TIMEOUT.
5. Failures classified ui_change start a heal agent run in the background; its proposals wait for approval.
6. Finish the run and send notifications: counts, each failure with its triage category and summary, and a dashboard link.

### 2. Explore
1. POST /api/apps/{id}/explore {max_pages?, max_tests?} creates an agent run. The supervisor writes data/agent-runs/<id>/input.json and spawns the CLI.
2. Each stdout line is an event: stored in agent_events, published to SSE.
3. The final done event carries the summary, including generated test files. The supervisor upserts test_cases with origin generated.
4. The dashboard follows GET /api/agent-runs/{id}/events (SSE): stored events are replayed, then live ones stream.

### 3. Heal approval
PATCH /api/heal-proposals/{id} {"action":"approve"}: the server checks the test file's current SHA-256 equals base_sha256 (otherwise status failed: file changed), writes new_content, then runs that test file (trigger heal). Status becomes applied or failed. Reject only marks the proposal rejected.

## Agent event protocol (stdout JSONL)
Each line: {"seq": 1, "ts": "<RFC 3339>", "type": "<type>", "data": {...}}
- run_started {agent, input_summary}
- phase {name, detail}
- llm_call {model, purpose, tokens_in, tokens_out, cost_usd, latency_ms}
- thought {text}: a short, user-facing summary of what the agent is doing and why
- tool_call {id, tool, args}
- tool_result {id, ok, output_preview, error?}
- artifact {kind: test_file|patch|screenshot|snapshot|sitemap, path, meta}
- test_validation {file, attempt, passed, error?}
- warning {message}
- done {status, summary}: always the last line; summary shape depends on the agent kind
Large payloads are written to data/agent-runs/<id>/ and events carry the paths. Nothing else may write to stdout.

## Agent designs
### Agent loop (src/core)
Loop until a final answer or a budget is hit: check budgets → call the LLM with system prompt, history and tool schemas → for each tool call, validate args with zod, run the tool, append the result (truncated, wrapped as untrusted) → for a final answer, validate it against the output schema; on failure retry once with the validation error. Every step emits events.

### Explorer
1. explore (agentic): the LLM drives a browser with tools (goto, click, fill, select, press, back, snapshot, record_page, finish_exploration) to discover pages and multi-step flows, starting from base_url and a list of same-origin links. It uses the app's notes for login. Bounded by max_pages (default 15). Recorded pages (URL, title, aria snapshot, interactive elements, forms) are saved to data/agent-runs/<id>/sitemap.json.
2. plan: the LLM returns scenarios {id, title, flow, steps[], expected[], priority, pages[]} (zod-validated), deduplicated against existing test titles, capped at max_tests (default 25).
3. write and validate, per scenario: the LLM writes one spec file using the fixtures and locator rules. Static checks: compiles (`playwright test --list`), at least one expect, no waitForTimeout, no forbidden selectors, and grounding: every role/label/text/test-id target must appear in the recorded sitemap. Then run it twice. On failure, feed back the error and a fresh snapshot; up to 3 repair attempts. Keep the test only if both runs pass. Saved to tests/<slug>/generated/<scenario-id>.spec.ts.
Summary: scenarios planned, tests kept, tests discarded with reasons, first-try valid count, repair attempts, tokens, cost.

### Triage
Input: failures left after the rule pass, each with error, file and line, retry info, aria snapshot and URL at failure, screenshot path, and the last 10 results of that test.
Tools: read_test_source, get_test_history, get_failure_snapshot, view_screenshot (only when the provider supports vision), open_current_page (fresh snapshot of the live app).
Output per failure: {category, confidence, summary (≤ 120 chars), explanation, suggested_action}. Confidence below 0.6 is reported as low confidence.

### Healer
Input: failed test results. Eligible only for locator failures (locator timeout, element not found, strict mode violation); everything else is skipped with a reason.
Steps: read the test source and failure snapshot, open the failing URL for a fresh snapshot, propose the minimal change to locator expressions only. Patch guard: reject if expected values, test titles or the number of expect calls change. Apply to a temporary copy and run it twice against the current app. Save a proposal with the verified flag. The healer never writes the real test file.

## API
### Conventions
Every endpoint follows these rules:
- JSON field names are snake_case. Timestamps are RFC 3339 strings in UTC with milliseconds (e.g. 2026-10-21T11:12:22.727Z).
- Lists are wrapped in an object named after the resource: {"apps": [...]}, {"tests": [...]}. An empty list is [], never null.
- Errors are {"error": "message"} with the matching status: 400 invalid input, 403 read-only mode, 404 unknown resource or non-numeric ID, 409 conflict, 413 body over 1 MB, 500 internal (details only in the server log), 503 database unavailable.
- Request bodies are a single JSON object; unknown fields are rejected with 400.
- PATCH is partial: absent fields stay unchanged, and an empty patch is a 400.
- With DEMO_READ_ONLY=true every method except GET, HEAD and OPTIONS returns 403.

### Endpoints
- GET /api/health → {ok, db, read_only, llm: {provider, model, api_key_configured}, notifications: {slack, email}}. Never includes keys. Email counts as configured when SMTP_HOST, SMTP_USER, SMTP_PASS and EMAIL_TO are all set. 503 if the database is unavailable.
- GET, POST /api/apps · GET, PATCH, DELETE /api/apps/{id}. POST {name, base_url, notes?, allow_destructive?} generates the slug and creates agents/tests/<slug>/{manual,generated}. PATCH can change those four fields, not the slug. DELETE removes the app's rows (cascade) and leaves its test files on disk.
- GET /api/apps/{id}/tests?origin&status · PATCH /api/tests/{id} {status} · GET /api/tests/{id}/source
- POST /api/apps/{id}/runs · GET /api/runs?app_id&limit · GET /api/runs/{id} (results with triage)
- GET, POST /api/schedules?app_id · PATCH, DELETE /api/schedules/{id} · GET /api/schedules/preview?cron= (next 5 fire times)
- POST /api/apps/{id}/probe · POST /api/apps/{id}/explore · POST /api/apps/{id}/heal {test_result_ids}
- GET /api/agent-runs?app_id&kind · GET /api/agent-runs/{id} · GET /api/agent-runs/{id}/events (SSE) · POST /api/agent-runs/{id}/cancel
- GET /api/heal-proposals?status · GET /api/heal-proposals/{id} · PATCH /api/heal-proposals/{id}
- GET /api/artifacts/{path...}: serves files under DATA_DIR only; path traversal blocked
- GET /api/stats/overview · GET /api/evals/latest · POST /api/notify/test

## Demo app (demo-app/)
"ShopLite": an Express server with server-rendered pages: login, products (search, sort), product detail, cart, checkout form with validation, order confirmation.
- BUGS (comma list) plants bugs: cart_badge, sort_price, checkout_total, search_case, login_error, form_validation.
- UI_VERSION=2 keeps behavior but changes the UI (button labels, test ids, layout); used to evaluate healing.
- FLAKY=1 makes one endpoint randomly slow or failing; used to evaluate flaky triage.

## Evals (evals/)
Experiments: coverage and validity, bug detection (manual vs generated suites), self-healing (including unsafe-heal rate), triage accuracy. Results go to evals/results/<timestamp>.json and latest.json, with a summary table in evals/RESULTS.md.

## Configuration (.env at repo root)
| Variable | Default | Purpose |
|---|---|---|
| PORT | 8080 | Go API port |
| TIMEZONE | Asia/Kolkata | Timezone for cron triggers |
| DATA_DIR | ../data | SQLite DB and artifacts (relative to server/) |
| AGENTS_DIR | ../agents | Agents and Playwright project |
| CORS_ORIGIN | http://localhost:3000 | Dashboard origin |
| DASHBOARD_URL | http://localhost:3000 | Links in notifications |
| RUN_TIMEOUT | 15m | Max duration of one test run |
| TRIAGE_TIMEOUT | 3m | How long a run waits for triage |
| DEMO_READ_ONLY | false | Disable all mutating endpoints (public demo) |
| LLM_PROVIDER | gemini | gemini or anthropic |
| LLM_MODEL | (required for agents) | Model name for the provider |
| LLM_VISION | true | Send screenshots to the model |
| GEMINI_API_KEY / ANTHROPIC_API_KEY | | Provider keys |
| AGENT_MAX_STEPS | 80 | Step budget per agent run |
| AGENT_MAX_COST_USD | 1.00 | Cost budget per agent run |
| AGENT_TIMEOUT | 20m | Wall-clock budget per agent run |
| AGENT_CONCURRENCY | 1 | Agent runs at the same time (others queue) |
| SLACK_WEBHOOK_URL | | Slack Incoming Webhook |
| SMTP_HOST, SMTP_PORT, SMTP_USER, SMTP_PASS, EMAIL_FROM, EMAIL_TO | smtp.gmail.com, 587 | Email reports |
web/.env.local: NEXT_PUBLIC_API_URL=http://localhost:8080

## Security notes
- The artifact endpoint only serves files inside DATA_DIR.
- The API has no authentication (local tool). Public deployments set DEMO_READ_ONLY=true so visitors can't start runs or spend API credits.
- Page content is untrusted input to the LLM (prompt injection); see CLAUDE.md agent rules.
