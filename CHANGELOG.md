# Changelog

All notable changes to this project are documented here. Format: Keep a Changelog.

## [Unreleased]
### Added
- Project guidelines: CLAUDE.md, ARCHITECTURE.md, DEPENDENCIES.md, ADRs 0001–0005.
- Monorepo scaffold:
  - Root: Makefile (setup, dev, server, web, demo, test, lint, eval), .gitignore, .editorconfig, .env.example with every configuration variable, short README.
  - server/: Go module with `cmd/sentinel` and `internal/config` (hand-written .env loader, defaults, absolute DATA_DIR/AGENTS_DIR, typed parsing that reports all errors, secret-free logging, table-driven tests). Serves `GET /api/health` → `{"ok": true}`; graceful shutdown on SIGINT/SIGTERM.
  - agents/: TypeScript package (strict, ES modules) with typecheck/test/cli scripts, vitest (unit tests in `src/**/*.test.ts`), `playwright.config.ts`, and a usage-only `src/cli.ts`.
  - web/: Next.js App Router app from create-next-app (TypeScript, Tailwind, ESLint), default page, `web/.env.example`.
  - demo-app/: ShopLite Express server (TypeScript via tsx) with a placeholder page on PORT (default 4000).
- DEPENDENCIES.md: web dev tooling from create-next-app (ESLint, @tailwindcss/turbopack, type packages) and demo-app typecheck packages.
- `.nvmrc` pinning Node 22.
- Go core storage (`server/internal/store`):
  - Opens `DATA_DIR/sentinel.db` with modernc.org/sqlite v1.36.1 (the newest release that still supports Go 1.22), WAL, foreign keys, a 5 s busy timeout and a single connection.
  - Embedded numbered migrations, each applied in its own transaction and recorded in `schema_migrations`. `0001_init.sql` creates every table in the data model, with CHECK constraints on enums, cascading deletes from apps, and AUTOINCREMENT IDs so deleted IDs are never reused.
  - Queries for apps (create, get, get by slug, list, partial update, delete, slug check) and test_cases (upsert by app+file+title that keeps status and created_at, list with origin/status filters, get, update status). Returns `store.ErrNotFound` / `store.ErrConflict`.
- REST API (`server/internal/api`):
  - JSON helpers (1 MB limit, unknown fields rejected, readable decode errors).
  - Middleware: slog request logging (method, path, status, duration), panic recovery to a 500, CORS for `CORS_ORIGIN` with preflight handling, and `DEMO_READ_ONLY` returning 403 for non-GET requests.
  - `GET /api/health` reports database status, read-only mode, the LLM provider/model and whether its key is set, and the configured notification channels. It never includes secrets.
  - `GET/POST /api/apps`, `GET/PATCH/DELETE /api/apps/{id}`, `GET /api/apps/{id}/tests?origin&status`, `PATCH /api/tests/{id}`. Creating an app validates name and base_url, generates a unique slug and creates `agents/tests/<slug>/{manual,generated}`. Deleting an app leaves its files on disk and says so in the response.
  - Table-driven store tests on a temporary database; httptest handler tests for success, validation errors, 404s, slug collisions, read-only mode, CORS and panics.
- ARCHITECTURE.md: an "API conventions" section (wrapped lists, snake_case, RFC 3339, error format and status codes) and the health response shape.
- ADR-0006: app slugs are stable and never reused.
- ShopLite demo app (`demo-app/`):
  - Express pages: login, products (search, sort by name and price), product detail, cart (quantity, remove), checkout (name, address, zip, card validation, 8% tax), order confirmation, and logout. In-memory sessions (`sid` cookie), carts and orders. Users `demo`/`demo123` and `locked`/`demo123`. Labels, headings and `data-testid`s for locators.
  - `BUGS` plants cart_badge, sort_price, checkout_total, search_case, login_error and form_validation. `UI_VERSION=2` renames buttons, changes test ids and moves search into the header. `FLAKY=1` delays `GET /api/products` by 4–8 s on about 30% of requests. Invalid values stop startup. `GET /__health` reports the mode.
  - README describing the modes, the bugs and the v1 → v2 differences.
- `agents/tests/_support/fixtures.ts`: an auto fixture that attaches `url.txt` and `aria-snapshot.txt` when a test fails, and re-exports `expect` and the `Page` type.
- `agents/tests/shoplite/manual/`: 8-test hand-written baseline suite (auth, cart, search, checkout). It passes in the default mode, catches only `cart_badge` among the planted bugs, and 7 of 8 tests fail with locator errors under `UI_VERSION=2`.
- Test runs (lifecycle 1, steps 1–3 and 6):
  - `server/internal/runner`: runs `npx playwright test tests/<slug>` (or one spec file) with `--reporter=json` in AGENTS_DIR, with BASE_URL, PLAYWRIGHT_JSON_OUTPUT_NAME and PW_OUTPUT_DIR set per run under `DATA_DIR/runs/<id>/`. Output goes to `playwright.log`. Playwright runs in its own process group, and RUN_TIMEOUT or a server stop kills the whole group. The parser covers title paths, file/line, status mapping, summed duration, retries, the first error with ANSI stripped, and screenshot/trace/url/aria-snapshot artifacts relative to DATA_DIR. The fixture's inlined (base64) url.txt and aria-snapshot.txt are written out to files. A missing report, a timeout or zero tests is an error, with the log tail.
  - `server/internal/runs`: `StartRun` refuses a second active run per app. It executes in a goroutine, upserts test_cases (origin from `manual/` or `generated/`), stores linked test_results, then finishes the run (failed if any test failed, flaky doesn't fail it) and calls a `Notifier` hook (no-op for now). On boot, leftover running runs are marked error ("server restarted"). On shutdown, in-flight runs are stopped and recorded as error before the database closes.
  - Store: runs (atomic create-if-no-active-run, finish, get, list, fail interrupted) and test_results (insert in one transaction, list).
  - API: `POST /api/apps/{id}/runs` (202 / 409), `GET /api/runs?app_id&limit`, `GET /api/runs/{id}` with results, and `GET /api/artifacts/{path...}`. Artifacts are served only from `DATA_DIR/runs/`; `..`, absolute paths and symlinks escaping the folder are refused, and content types are set for png, zip and txt.
  - Tests: parser tests against two real Playwright 1.64 reports in `runner/testdata` (all passing; failure + flaky), runner tests with a shell script in place of Playwright (env and target, missing report, no tests, process-group kill on timeout, cancel), a runs service test with a fake runner, store tests, and handler tests including path traversal and symlink escapes.
- ARCHITECTURE.md: run status rules, run folder layout, parsing rules, run and artifact endpoint details, and the allowed artifact folders.
- Scheduled runs (lifecycle 1, step 1 by cron):
  - `server/internal/scheduler`: robfig/cron v3.0.1 in the TIMEZONE location with a 5-field parser plus descriptors (@daily, @weekly, …). `Parse` also rejects `@every` and `TZ=`/`CRON_TZ=` prefixes (the latter would otherwise panic inside robfig's parser for input like `TZ=UTC`). Loads enabled schedules at boot; `Add`, `Remove`, `NextRuns(id, n)` and `Preview(spec, n)`. A firing re-reads its schedule (deleted or disabled → entry removed) and starts a run with trigger `schedule`; if the app already has an active run it is skipped and logged, never queued. Stopped on shutdown before in-flight runs are awaited.
  - Store: schedules (create with app check, get, list by app, list enabled, enable/disable, delete).
  - API: `GET/POST /api/schedules?app_id`, `PATCH /api/schedules/{id}` `{enabled}`, `DELETE /api/schedules/{id}`, `GET /api/schedules/preview?cron=` (validity, error, timezone and the next 5 fire times; 400 for an invalid expression). Each schedule includes `next_run`. Deleting an app also removes its cron entries.
- Run notifications (lifecycle 1, step 6), `server/internal/notify`:
  - A format-neutral report: status emoji, passed/total, trigger, duration, flaky and skipped counts, up to 10 failures with title and first error line, "and N more failures", run error for error runs, and a link to `DASHBOARD_URL/runs/<id>`.
  - Slack: Incoming Webhook with Block Kit (header, context line, one section per failure, "View run" button, fallback text), mrkdwn-escaped and truncated to Slack's limits. The webhook URL is stripped from connection errors.
  - Email: HTML with inline styles plus a plain-text alternative (multipart/alternative, quoted-printable), RFC 2047 subject like `❌ ShopLite: 6/8 passed (run #12)`, sent with `net/smtp` over required STARTTLS (works with Gmail app passwords on port 587). EMAIL_TO accepts a comma-separated list; EMAIL_FROM defaults to SMTP_USER.
  - Each channel is enabled only when configured; failures are logged and never change the run. `STEP 9 (triage)` comments mark where triage category and summary go in both formats.
  - `NOTIFY_ON`: `all` (default) or `failures` (only failed and error runs). Added to `.env.example` and the Configuration table.
  - `POST /api/notify/test` sends a sample "[Test]" report to every configured channel and returns per-channel `{configured, ok, error}`.
- Tests: cron validation table, next fire times in Asia/Kolkata from a fixed clock (`0 9 * * 1,4`, `*/2`, `@daily`, never-firing), firing/skip/removal with a real runs service; Slack payload golden file (`notify/testdata/slack_failed_run.golden.json`, refresh with `go test ./internal/notify -update`), httptest webhook (success, Slack error reason, no URL leak), email MIME structure (parts, order, encoding, escaping, line length), a fake SMTP server that checks STARTTLS before AUTH and refuses to send without it, NOTIFY_ON per run status, and handler tests for schedules and notify/test.
- ADR-0007: schedule and notification semantics (skip instead of queue, one time zone, no catch-up after downtime, NOTIFY_ON).

### Changed
- `api.New` takes the runs service (a `RunStarter`) in addition to config, store and logger.
- `api.New` now takes an `api.Services` struct (runs, scheduler, notifier) instead of the runs service alone.
- `config`: `SlackConfigured()` and `EmailConfigured()` are shared by the health endpoint and the notifier; NOTIFY_ON is validated at startup.
- The runs service uses the real notifier instead of `NopNotifier` (still used in tests).
- Node 22 is the baseline: `engines.node >=22` in agents/, demo-app/ and web/; `@types/node` bumped to 22 everywhere.
- agents/: vitest 3.x → 5.x.
- agents/ and demo-app/ tsconfig: `allowImportingTsExtensions`, so imports use `.ts` paths (tsx and Playwright run them directly). demo-app/ also typechecks `src/`.
- ARCHITECTURE.md: FLAKY now means 4–8 s delays on the products API (no failures), plus details on ShopLite state, startup validation and `/__health`.

### Known gaps
- `npm audit` reports 5 high-severity advisories in web/ dev dependencies from create-next-app; not reviewed yet.
- Unknown routes and wrong methods get Go's default plain-text 404/405, not the JSON error format.
- App and test lists have no pagination.
- `GET /api/tests/{id}/source` is not implemented yet.
- ShopLite state is in memory and resets on restart; sessions never expire.
- `demo-app/public/app.js` is plain JavaScript and isn't typechecked or linted.
- Under `FLAKY=1` mainly the search test turns flaky, because actions wait up to 30 s for the product list while only `expect` uses the 5 s timeout.
- No formatter or linter for agents/ and demo-app/ beyond `tsc` (no Prettier/ESLint there).
- Runs start manually via the API or from schedules; the dashboard runs and schedules pages come in later steps. There is no endpoint to cancel a run yet.
- Not verified against real services yet (no Slack workspace or Gmail account set up): `/api/notify/test` posting to a real Slack webhook, a real Gmail delivery over STARTTLS with an app password, how the HTML email renders in Gmail and other clients, and Slack rendering the Block Kit message. Delivery was tested only against fakes: an httptest webhook, an in-process SMTP server, and a local listener during a live scheduled run.
- Email supports STARTTLS only (port 587); implicit TLS on port 465 is not supported.
- Notifications are sent one channel after the other inside the run goroutine; a slow SMTP server (up to the 30 s timeout) delays the run's completion log, not its stored status.
- Schedules can only be enabled, disabled or deleted; changing a schedule's cron means deleting and recreating it.
- Firings missed while the server is down are not caught up (ADR-0007), and skipped firings are only visible in the server log.
- The "View run" link points to `DASHBOARD_URL/runs/<id>`, which the dashboard doesn't have yet.
- Killing the Playwright process group uses Unix process groups (`Setpgid`), so the server doesn't build for Windows.
- Run folders under `DATA_DIR/runs/` are never cleaned up (a trace is about 0.5 MB per failed attempt).
- Only one Playwright project is assumed: with several, each project's result is stored under the same title and file.
- With `BUGS=cart_badge,login_error` the manual suite fails only the cart badge test; `login_error` isn't covered by the baseline suite, so it needs generated tests.
- `make dev` interleaves the three processes' output without prefixes, and `tsx watch` prints a few "Force killing" lines on Ctrl+C (harmless).
