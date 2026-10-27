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
- scheduler: robfig/cron with the TIMEZONE location. Loads enabled schedules at boot; adds and removes entries when the API changes them. Rules in ADR-0007: a firing for an app with an active run is skipped (logged, never queued); firings missed while the server is down are not caught up; a firing re-reads its schedule and does nothing if it was deleted or disabled.
- runner: spawns Playwright and parses its JSON report.
- runs: orchestrates a test run end to end (lifecycle 1). At most one active run per app.
- agents: supervisor that spawns the agent CLI, reads stdout JSONL, stores events, publishes them to SSE and applies results.
- sse: in-memory pub/sub broker keyed by agent run ID.
- notify: Slack (Incoming Webhook, Block Kit) and email (net/smtp with required STARTTLS, multipart/alternative HTML + plain text) formatting and sending. Implements the runs service's Notifier. A channel is enabled only when configured; NOTIFY_ON picks which runs are reported.

### Agents (agents/)
- src/cli.ts: entry point. `tsx src/cli.ts <kind> --input <file.json>`. Loads the repo's `.env` for variables not already set, validates the input with zod, writes events as JSONL to stdout, logs to stderr (console.log/info/debug are redirected there). Exit 0 once a done event is written, whatever its status; non-zero only for usage errors and crashes. On SIGTERM it writes done with status cancelled, closes the browser (at most 3 s) and exits 0. explore, triage and heal end with done failed "not implemented yet" until their steps land.
- src/llm/: LLMProvider interface `generate({ system, messages, tools?, schema?, images?, signal? }) -> { model, text?, toolCalls?, usage, providerData? }`. Messages are user (text, optional images), assistant (text, tool calls) and tool (results for every call of the previous assistant turn). `schema` (zod) asks for JSON output; callers still validate it. `images` attach to the latest user message. Adapters gemini.ts (function declarations with `parametersJsonSchema`, `responseJsonSchema`; the model's turn is sent back unchanged in `providerData` because it carries thought signatures) and anthropic.ts (tools with `input_schema`, `output_config.format`); both disable SDK retries. retry.ts paces every attempt to LLM_RPM (pacer.ts: requests spaced 60/LLM_RPM seconds apart, retries included), counts it against LLM_MAX_REQUESTS_PER_RUN before sending, gives it its own LLM_CALL_TIMEOUT, and retries 429, 5xx, network errors and timed-out attempts: 4 attempts, exponential backoff with full jitter (2 s base, 16 s cap), a warning event per retry. When the provider says how long to wait (Gemini `google.rpc.RetryInfo.retryDelay`, Anthropic `retry-after`), that delay is used instead; a requested wait over 60 s (for example a free-tier daily quota), or longer than the time left in the run's budget, fails the call at once ("…; not retrying: the requested wait (47 s) is longer than the 20 s left in this run's budget"). Errors say what happened: "quota exceeded" for a per-day quota (Gemini `QuotaFailure` quota id with PerDay), "rate limited" for other 429s, "timed out after N s (LLM_CALL_TIMEOUT)", and "(gave up after 4 attempts)" when retries run out. fake.ts replays scripted responses for tests. pricing.ts: USD per 1M tokens per model (placeholder values to fill from the pricing pages); an unknown model costs 0 and emits one warning.
- src/core/: agent loop, tools (`defineTool({ name, description, schema, run, redact? })`, zod → JSON Schema), budget tracker, event emitter (the only writer to stdout), runtime config from the environment, untrusted-content helpers (`<untrusted source="...">` with truncation and neutralized closing tags).
- src/browser/: Playwright session (Chromium, context baseURL; main-frame navigations to other origins are aborted), snapshots (aria snapshot of the body cut at a line boundary at 2,500 characters, plus up to 60 interactive elements with role, accessible name, test id, label, placeholder and href; the in-page accessible name is an approximation), destructive-action guard (accessible names matching delete, remove account, pay, purchase, place order, send; checked against both the requested and the matched element's name; allowed only with allow_destructive), and the browser tools shared by the agents (tools.ts: open_page, click, fill, snapshot).
- Browser tool convention: every tool that acts on the page returns its outcome followed by a snapshot of the page it led to (same format and truncation as snapshot()), so the agent never spends an LLM request on a separate snapshot after an action. This covers open_page, click and fill now, and select, press and back when the Explorer adds them. snapshot() remains for when the page changes on its own (late-loading content) or a fresh look is needed. After a blocked off-origin navigation the session returns to the page the action started from (Chromium would otherwise leave the tab on its error page) and the outcome says so.
- src/agents/{probe,explorer,triage,healer}/, plus context.ts (the shared input schema and what the CLI hands each agent).
- src/prompts/: prompt templates (*.md). The first line is `version: <id>`; `{{name}}` placeholders, and a placeholder without a value is an error. The versions of the prompts an agent uses are recorded in run_started.
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
2. runner: `npx playwright test tests/<slug> --reporter=json` in agents/ (or a single spec file, used later for heals), with BASE_URL, PLAYWRIGHT_JSON_OUTPUT_NAME=data/runs/<id>/report.json and PW_OUTPUT_DIR=data/runs/<id>/artifacts. One retry. Playwright runs in its own process group; after RUN_TIMEOUT, or when the server stops, the whole group (npx, node, browsers) is killed.
3. Parse the report into test_results and upsert test_cases. A non-zero exit with a report is a normal failing run; no report, a timeout or a report with no tests means status error.
   - Title: describe titles and the test title joined with " › ". File: relative to agents/ (`tests/<slug>/manual/x.spec.ts`).
   - Status: Playwright `expected` → passed, `flaky` → flaky, `skipped` → skipped, anything else → failed. Duration is summed over attempts; retries = attempts − 1.
   - Error and artifacts come from the last attempt that did not pass (for a flaky test, the failed attempt before the passing retry). The error is its first error message with ANSI codes stripped.
   - Origin comes from the folder: `manual/` or `generated/` directly under `tests/<slug>/`. Results from other paths are stored with test_case_id null.
4. If tests failed: triage. Rules first (passed on retry → flaky; connection refused, DNS or net::ERR_ errors → environment). Remaining failures go to one triage agent run; wait up to TRIAGE_TIMEOUT.
5. Failures classified ui_change start a heal agent run in the background; its proposals wait for approval.
6. Finish the run and send notifications: counts, each failure with its triage category and summary, and a dashboard link. The runs service calls a Notifier interface; notification errors are logged and never change the run. With NOTIFY_ON=failures, passed runs are not reported.
   - Both formats show: status emoji (✅ passed, ❌ failed, ⚠️ error), app name and passed/total (or "run error" plus the error's first line), a context line (run number, trigger, duration, flaky, skipped), up to 10 failures with title and first error line (200 characters max), "and N more failures", and a "View run" link to DASHBOARD_URL/runs/<id>.
   - Slack: header, context, one section per failure, actions button; the message's `text` is the subject line as a fallback. Test titles and errors are escaped for mrkdwn.
   - Email: subject like "❌ ShopLite: 6/8 passed (run #12)" (RFC 2047 encoded), multipart/alternative with text/plain then text/html (inline styles only), quoted-printable. EMAIL_TO is a comma-separated list; EMAIL_FROM defaults to SMTP_USER. The server must offer STARTTLS, otherwise nothing is sent.
   - Triage category and summary per failure are added in roadmap step 9; both formats mark the spot with a `STEP 9 (triage)` comment.

Run status rules:
- running while Playwright runs (queued is reserved for later; it counts as active).
- failed if at least one test failed; passed otherwise. Flaky and skipped tests are counted but don't fail the run.
- error if Playwright produced no usable report, timed out, ran no tests, or the server stopped during the run. The run's error field says which; for a missing report it includes the tail of playwright.log.
- At boot, runs still queued or running are marked error ("server restarted").
- At most one active (queued or running) run per app; the check and insert are one SQL statement.

Run folder (DATA_DIR/runs/<id>/):
- report.json: Playwright's JSON report.
- playwright.log: combined stdout and stderr.
- artifacts/<test folder>/: test-failed-1.png (screenshot), trace.zip, url.txt, aria-snapshot.txt. The fixture's url.txt and aria-snapshot.txt arrive inlined in report.json as base64; the runner writes them next to the screenshot so every artifact is a file.
- test_results.artifacts_json maps screenshot, trace, url and aria_snapshot to paths relative to DATA_DIR.

### Agent runs (supervisor)
Every agent kind runs the same way (server/internal/agents):
1. `Start(kind, app, input)` inserts an agent run: running if fewer than AGENT_CONCURRENCY runs are active, otherwise queued. When a run ends, the oldest queued run starts. The slot check, insert and launch happen under one lock.
2. It writes DATA_DIR/agent-runs/<id>/input.json: the kind-specific input plus `agent_run_id`, `run_dir` (that folder, absolute) and `app {id, name, slug, base_url, notes, allow_destructive}`. agent_runs.input_json keeps only the kind-specific part.
3. It spawns `npx tsx src/cli.ts <kind> --input <file>` in AGENTS_DIR, in its own process group, with the server's environment. stderr goes to agent.log in the run folder. stdout is an os.Pipe the server owns, so a browser that inherited it can't keep the run open: after the CLI exits, the rest of the group gets 10 s to close stdout and is then killed.
4. stdout is read line by line (lines over 1 MB are dropped with a warning). Each valid line is stored in agent_events, its usage is added to the run (llm_call: steps +1, tokens, cost; same transaction), and it is published to the SSE broker. Invalid lines (not JSON, no seq, unknown type, data not an object), events after done and out-of-order CLI seqs become warning events.
5. Seq is owned by the server: stored events are numbered 1, 2, 3… per run, and that number is the SSE event ID. The CLI's own seq is only checked for order. This lets the server add warnings and a done of its own without colliding with the CLI's numbers.
6. The done event's status, summary and error are saved on the run in the same transaction that stores the done event, before it is published, so a client that sees done always reads the finished run. If the CLI exits without done, the server writes a done itself (status failed with the exit status and the stderr tail; or cancelled, or the timeout/shutdown reason), so SSE clients always see the end of a run.
7. A completion callback per kind (`OnDone`) runs after the run is stored; later steps use it (explore upserts test cases, triage stores results).

Agent timeouts are layered:
- AGENT_TIMEOUT is the agent's own wall-clock budget. The CLI checks it before every LLM call and aborts a call that would outlive it; the run ends cleanly with budget_exceeded. An agent may set tighter budgets of its own (the probe: 15 requests and 4 minutes), never looser ones.
- LLM_CALL_TIMEOUT (60 s) caps each LLM request attempt inside that budget, so one stuck or very slow request can't use it all up.
- Budgets per run: steps (AGENT_MAX_STEPS, LLM calls), requests (LLM_MAX_REQUESTS_PER_RUN, every request sent including retries, so a run can't use up a daily quota), cost (AGENT_MAX_COST_USD) and wall clock. Any of them ends the run with budget_exceeded.
- The supervisor kills the process group only if the agent is still running 30 s after AGENT_TIMEOUT (a hung tool or a crashed event loop). That run ends failed ("agent killed").

Cancel (POST /api/agent-runs/{id}/cancel) sends SIGTERM to the group; the CLI writes done cancelled. If the group is still alive 10 s later it gets SIGKILL and the server writes the done. A queued run is cancelled at once. At boot, agent runs left queued or running become failed ("server restarted"); on shutdown running agents are killed and recorded as failed ("server stopped during the agent run").

Agent run folder (DATA_DIR/agent-runs/<id>/): input.json, agent.log (the CLI's stderr), and whatever the agent writes (served by the artifact endpoint).

### 2. Explore
1. POST /api/apps/{id}/explore {max_pages?, max_tests?} creates an agent run (see Agent runs above).
2. Each stdout line is an event: stored in agent_events, published to SSE.
3. The final done event carries the summary, including generated test files. The supervisor upserts test_cases with origin generated.
4. The dashboard follows GET /api/agent-runs/{id}/events (SSE): stored events are replayed, then live ones stream.

### 3. Heal approval
PATCH /api/heal-proposals/{id} {"action":"approve"}: the server checks the test file's current SHA-256 equals base_sha256 (otherwise status failed: file changed), writes new_content, then runs that test file (trigger heal). Status becomes applied or failed. Reject only marks the proposal rejected.

## Agent event protocol (stdout JSONL)
Each line: {"seq": 1, "ts": "<RFC 3339>", "type": "<type>", "data": {...}}. The CLI's seq increases by one per event; the server renumbers stored events with its own seq (see Agent runs).
- run_started {agent, input_summary, prompts: {name: version}}
- phase {name, detail}
- llm_call {model, purpose, tokens_in, tokens_out, cost_usd, latency_ms, requests, wait_ms}: requests is 1 plus retries; latency_ms includes wait_ms (pacing and retry waits)
- thought {text}: a short, user-facing summary of what the agent is doing and why
- tool_call {id, tool, args}
- tool_result {id, ok, output_preview, error?}
- artifact {kind: test_file|patch|screenshot|snapshot|sitemap, path, meta}
- test_validation {file, attempt, passed, error?}
- warning {message}
- done {status, summary, error?}: always the last line. status is succeeded, failed, budget_exceeded or cancelled. summary depends on the agent kind and always has usage {steps, requests, cost_usd, elapsed_ms} when written by the CLI.
Large payloads are written to data/agent-runs/<id>/ and events carry the paths. Nothing else may write to stdout.

## Agent designs
### Agent loop (src/core)
Loop until a final answer or a budget is hit: check budgets → call the LLM with system prompt, history and tool schemas → for each tool call, validate args with zod, run the tool, append the result (truncated, wrapped as untrusted) → for a final answer, validate it against the output schema; on failure retry once with the validation error. Every step emits events.
- The final answer is the arguments of a `finish` tool whose schema is the agent's output schema (ADR-0008). Invalid finish args get one corrective retry with the zod error; a second invalid answer fails the run.
- Invalid tool args, unknown tools and tool exceptions come back as error tool results, so the model can correct itself. Tool output and tool errors are wrapped as untrusted and truncated to 4,000 characters.
- A reply without tool calls gets a short "call a tool or finish" nudge.
- One step is one LLM call (which may take several requests when retried). Each call emits llm_call {model, purpose, tokens_in, tokens_out, cost_usd, latency_ms, requests, wait_ms}; text the model writes alongside tool calls becomes a thought event. Tools can redact their args in tool_call events (the probe hides password fields).

### Probe
A small agent that proves the plumbing end to end. Tools: open_page(path), snapshot(), click(role, name), fill(label, value) (needed because most apps, ShopLite included, show nothing but a login page before signing in) and finish. Task: "Describe what this web app does and list its main user flows", using the app's notes for login. Output and done summary: {app_summary, flows: [{name, description, pages[]}]}. Prompts: probe.system.md, probe.task.md. Its own budgets are 15 LLM requests and 4 minutes (at LLM_RPM 5 a request takes about 12 s), each capped by the configured AGENT_TIMEOUT and LLM_MAX_REQUESTS_PER_RUN. The tools follow the browser tool convention, so a login is fill, fill, click in one turn with no snapshot calls; the prompt asks for independent actions in one turn.

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
  - POST starts a manual run: 202 with the run (status running), 404 unknown app, 409 `{"error": "app 1 already has an active run (run 7)"}`.
  - Run fields: id, app_id, trigger, status, total, passed, failed, flaky, skipped, duration_ms, error (null if none), started_at, finished_at (null while running).
  - GET /api/runs → {"runs": [...]} newest first, without results. app_id filters by app; limit defaults to 20, maximum 100; invalid values are a 400.
  - GET /api/runs/{id} → the run plus results: [{id, test_case_id, title, file_path, line, status, duration_ms, error, retries, artifacts: {screenshot?, trace?, url?, aria_snapshot?}}]. Triage is added with lifecycle step 4.
- GET, POST /api/schedules?app_id · PATCH, DELETE /api/schedules/{id} · GET /api/schedules/preview?cron= (next 5 fire times)
  - Schedule fields: id, app_id, cron, enabled, created_at, next_run (null while disabled). The human-readable description is made by the dashboard (cronstrue), not the server.
  - GET → {"schedules": [...]} ordered by id; app_id filters (an unknown app gives []).
  - POST {app_id, cron, enabled?} → 201. enabled defaults to true; cron is trimmed. Invalid cron → 400 "invalid cron: <parser message>"; unknown app → 404. Allowed: 5-field cron and descriptors (@daily, @weekly, @hourly, …); `@every` and `TZ=`/`CRON_TZ=` prefixes are rejected (ADR-0007).
  - PATCH {enabled} → 200 with the schedule. DELETE → 204. Deleting an app deletes its schedules and their cron entries.
  - preview → {valid, error, timezone, next_runs}. Times are UTC like every API timestamp; timezone (TIMEZONE) tells the dashboard where they fire. Invalid or missing cron → 400 with the same shape (valid false, error set, next_runs []).
- POST /api/apps/{id}/probe · POST /api/apps/{id}/explore · POST /api/apps/{id}/heal {test_result_ids}
  - POST probe takes no body (or {}) → 202 with the agent run (running or queued); 404 unknown app.
- GET /api/agent-runs?app_id&kind · GET /api/agent-runs/{id} · GET /api/agent-runs/{id}/events (SSE) · POST /api/agent-runs/{id}/cancel
  - Agent run fields: id, app_id, kind, status, input (object), summary (object, null until finished), steps, tokens_in, tokens_out, cost_usd, error, started_at, finished_at.
  - GET /api/agent-runs → {"agent_runs": [...]} newest first; app_id and kind filter, limit defaults to 20 (max 100); invalid values are a 400.
  - cancel → 202 with the run (a queued run is already cancelled; a running one ends when its process does); 409 if it already finished.
  - events: `text/event-stream`. Stored events after Last-Event-ID (header, or `?last_event_id=` for curl) are replayed, then live events stream until done, and the server closes the stream. Each message is `id: <seq>` and `data: {"seq", "ts", "type", "data"}` with no `event:` field, so EventSource's onmessage gets everything. A `: heartbeat` comment every 15 s. A subscriber more than 256 events behind is disconnected and catches up on reconnect.
- GET /api/heal-proposals?status · GET /api/heal-proposals/{id} · PATCH /api/heal-proposals/{id}
- GET /api/artifacts/{path...}: serves files under DATA_DIR only; path traversal blocked
  - Allowed folders: DATA_DIR/runs/ and DATA_DIR/agent-runs/. Everything else in DATA_DIR, including sentinel.db, is a 404.
  - The path must be clean and relative: "..", ".", empty segments, backslashes and absolute paths are a 400. Symlinks are resolved and the real file must stay inside the allowed folder; directories and missing files are a 404.
  - Content-Type by extension: png → image/png, zip → application/zip, txt/log/md → text/plain; charset=utf-8, json → application/json, anything else → application/octet-stream. Always sent with X-Content-Type-Options: nosniff.
- GET /api/stats/overview · GET /api/evals/latest · POST /api/notify/test
  - POST /api/notify/test sends a sample failing report (marked "[Test]") to every configured channel, whatever NOTIFY_ON says, and answers 200 with {"slack": {configured, ok, error}, "email": {...}}. A failing channel is reported in its error field, not as an HTTP error. Secrets (webhook URL, SMTP password) never appear in errors.

## Demo app (demo-app/)
"ShopLite": an Express server with server-rendered pages: login, products (search, sort), product detail, cart, checkout form with validation, order confirmation.
- State (sessions, carts, orders) is in memory. Users: demo/demo123 and locked/demo123 (shows "account locked"). The product list is rendered by a little vanilla JS from `GET /api/products`; everything else is plain HTML forms.
- BUGS (comma list) plants bugs: cart_badge, sort_price, checkout_total, search_case, login_error, form_validation. Each changes one behavior (see demo-app/README.md).
- UI_VERSION=2 keeps behavior but changes the UI (button labels, test ids, search moved into the header); used to evaluate healing.
- FLAKY=1 makes `GET /api/products` wait 4–8 s on about 30% of requests, so the product list sometimes outlasts Playwright's 5 s expect timeout; used to evaluate flaky triage.
- Invalid BUGS, UI_VERSION or FLAKY values stop the server at startup. `GET /__health` returns `{"ok": true, "bugs": [...], "ui_version": 1, "flaky": false}` so evals can confirm the mode.
- The hand-written baseline suite is agents/tests/shoplite/manual/ (8 tests).

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
| LLM_CALL_TIMEOUT | 60s | Timeout for one LLM request attempt; a timed-out attempt is retried (must be positive) |
| LLM_RPM | 5 | Client-side pacing: LLM requests per minute per agent run, retries included, spaced evenly (0 = no pacing) |
| LLM_MAX_REQUESTS_PER_RUN | 15 | Budget: LLM requests per agent run, retries included; hitting it ends the run with budget_exceeded (at least 1) |
| GEMINI_API_KEY / ANTHROPIC_API_KEY | | Provider keys |
| AGENT_MAX_STEPS | 80 | Step budget per agent run |
| AGENT_MAX_COST_USD | 1.00 | Cost budget per agent run |
| AGENT_TIMEOUT | 20m | Wall-clock budget per agent run; the server kills the agent 30 s later (must be positive) |
| AGENT_CONCURRENCY | 1 | Agent runs at the same time (others queue; at least 1) |
| SLACK_WEBHOOK_URL | | Slack Incoming Webhook |
| SMTP_HOST, SMTP_PORT, SMTP_USER, SMTP_PASS, EMAIL_FROM, EMAIL_TO | smtp.gmail.com, 587 | Email reports (STARTTLS required; EMAIL_TO comma-separated; EMAIL_FROM defaults to SMTP_USER) |
| NOTIFY_ON | all | Which runs are reported: all, or failures (failed and error runs only) |
web/.env.local: NEXT_PUBLIC_API_URL=http://localhost:8080

## Security notes
- The artifact endpoint only serves files inside DATA_DIR's allowed folders (runs/ and agent-runs/), never the database; symlinks escaping them are refused.
- The API has no authentication (local tool). Public deployments set DEMO_READ_ONLY=true so visitors can't start runs or spend API credits.
- Page content is untrusted input to the LLM (prompt injection); see CLAUDE.md agent rules. Agents run with the server's environment (including provider keys) and never put keys in events.
- Agent browsers stay on the app's origin (main-frame navigations elsewhere are aborted) and destructive controls are refused unless the app has allow_destructive.
