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

### Changed
- Node 22 is the baseline: `engines.node >=22` in agents/, demo-app/ and web/; `@types/node` bumped to 22 everywhere.
- agents/: vitest 3.x → 5.x.

### Known gaps
- `npm audit` reports 5 high-severity advisories in web/ dev dependencies from create-next-app; not reviewed yet.
- No CORS, JSON helpers or read-only middleware on the API yet (internal/api comes with the first real endpoints).
- `agents/tests/_support/fixtures.ts` does not exist yet.
- No formatter or linter for agents/ and demo-app/ beyond `tsc` (no Prettier/ESLint there).
- `make dev` interleaves the three processes' output without prefixes, and `tsx watch` prints a few "Force killing" lines on Ctrl+C (harmless).
