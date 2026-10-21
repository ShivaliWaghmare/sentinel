# Sentinel

## Status: work in progress
Sentinel is a multi-agent system that explores web apps, writes Playwright tests, triages failures and heals broken tests.
Built so far: the Go API with SQLite storage, the ShopLite demo app with planted bugs, and a manual baseline test suite.
Coming next: test runs with scheduled triggers, the AI agents, the dashboard and evaluation results.

Sentinel is a multi-agent system for autonomous web app testing. AI agents explore a web app and write Playwright tests, explain test failures and repair tests broken by UI changes, while a Go service runs the suites on cron triggers, reports to Slack and email, and a Next.js dashboard shows live agent activity. It is under active development; see [ARCHITECTURE.md](ARCHITECTURE.md) for the design and [CLAUDE.md](CLAUDE.md) for commands (`make setup`, then `make dev`). Requires Node 22+ (`nvm use` reads `.nvmrc`) and Go 1.22+.
