# NetInv developer entrypoints (doc 25 §6). `make dev` boots infra; run Go
# services on the host for the fast inner loop.
.PHONY: dev dev-down build test lint fmt run-% compose-app frontend-dev

dev: ## start infra stack (PG, Redis, RabbitMQ, VictoriaMetrics, snmpsim)
	docker compose up -d --wait postgres redis rabbitmq victoriametrics snmpsim

dev-down:
	docker compose down

compose-app: ## everything in containers, including the six services
	docker compose --profile app up --build

build:
	cd backend && go build ./...

test:
	cd backend && go test ./...
	@if [ -d frontend/node_modules ]; then cd frontend && npm test --if-present; fi

lint:
	cd backend && go vet ./...
	@command -v golangci-lint >/dev/null && (cd backend && golangci-lint run) || echo "golangci-lint not installed — go vet only"

fmt:
	cd backend && gofmt -w .

run-%: ## run one service locally, e.g. make run-api
	cd backend && go run ./cmd/$*

frontend-dev:
	cd frontend && npm run dev
