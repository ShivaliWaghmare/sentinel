.PHONY: setup dev server web demo test lint eval

# Install all dependencies and the Playwright browser.
setup:
	@test -f .env || (cp .env.example .env && echo "Created .env from .env.example")
	@test -f web/.env.local || (cp web/.env.example web/.env.local && echo "Created web/.env.local from web/.env.example")
	cd server && go mod download
	cd agents && npm ci && npx playwright install chromium
	cd web && npm ci
	cd demo-app && npm ci

# Start the Go server, dashboard and demo app together. Ctrl+C stops all three:
# the trap kills the whole process group when the shell gets a signal or exits.
# It removes itself first so the TERM that kill 0 sends back to this shell
# doesn't run it again.
dev:
	@trap 'trap - INT TERM EXIT; kill 0' INT TERM EXIT; \
	(cd server && go run ./cmd/sentinel) & \
	(cd web && npm run dev) & \
	(cd demo-app && npm run dev) & \
	wait

server:
	cd server && go run ./cmd/sentinel

web:
	cd web && npm run dev

demo:
	cd demo-app && npm run dev

test:
	cd server && go test ./...
	cd agents && npm test

lint:
	@cd server && unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi
	cd server && go vet ./...
	cd agents && npm run typecheck
	cd demo-app && npm run typecheck
	cd web && npm run lint

eval:
	@echo "make eval: not implemented yet"
