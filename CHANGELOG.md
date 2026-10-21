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

### Changed
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
- `make dev` interleaves the three processes' output without prefixes, and `tsx watch` prints a few "Force killing" lines on Ctrl+C (harmless).
