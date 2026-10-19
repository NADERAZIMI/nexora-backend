SHELL := /bin/sh
GO ?= go
CARGO ?= cargo
NPM ?= npm
COMPOSE ?= docker compose

.PHONY: help bootstrap dev dev-infra app-up down logs fmt fmt-check lint test build \
	contracts docs-check security check clean

help:
	@echo "NEXORA backend commands"
	@echo "  make dev          Start local infrastructure and sample services"
	@echo "  make dev-infra    Start infrastructure only"
	@echo "  make down         Stop the local stack"
	@echo "  make fmt          Format Go and Rust"
	@echo "  make lint         Format check, vet, clippy"
	@echo "  make test         Unit tests for Go and Rust"
	@echo "  make build        Build Rust workspace and Go services"
	@echo "  make contracts    Lint and generate contracts"
	@echo "  make docs-check   Markdown lint"
	@echo "  make security     Secret, dependency, and filesystem scans"
	@echo "  make check        Format, lint, test, build, contracts"

bootstrap:
	$(GO) version
	$(CARGO) --version
	node --version
	$(COMPOSE) version

dev: dev-infra app-up
	@echo "NEXORA local stack is running:"
	@echo "  hello-service  http://localhost:8080/healthz"
	@echo "  core-template  http://localhost:8081/healthz"
	@echo "  Prometheus     http://localhost:9090"
	@echo "  MinIO console  http://localhost:9001"

dev-infra:
	$(COMPOSE) up -d postgres redpanda redis clickhouse minio otel-collector prometheus

app-up:
	$(COMPOSE) --profile app up --build -d

down:
	$(COMPOSE) --profile app down --remove-orphans

logs:
	$(COMPOSE) --profile app logs -f --tail=200

fmt:
	$(CARGO) fmt --all
	cd libs/go && $(GO) fmt ./...
	cd services/hello-service && $(GO) fmt ./...

fmt-check:
	$(CARGO) fmt --all -- --check
	@test -z "$$(gofmt -l libs/go services/hello-service)" || \
		(echo "gofmt required for:"; gofmt -l libs/go services/hello-service; exit 1)

lint: fmt-check
	$(CARGO) clippy --workspace --all-targets --all-features -- -D warnings
	cd libs/go && $(GO) vet ./...
	cd services/hello-service && $(GO) vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then \
		cd libs/go && golangci-lint run ./... && \
		cd ../../services/hello-service && golangci-lint run ./...; \
	else echo "golangci-lint not installed; skipped"; fi

test:
	$(CARGO) test --workspace --all-features
	cd libs/go && $(GO) test -race ./...
	cd services/hello-service && $(GO) test -race ./...

build:
	$(CARGO) build --workspace --all-targets
	cd services/hello-service && $(GO) build ./...

contracts:
	cd contracts && buf lint
	cd contracts && buf build
	cd contracts && buf generate
	$(NPM) run lint:openapi
	$(NPM) run lint:asyncapi

docs-check:
	$(NPM) run lint:md

security:
	gitleaks detect --source . --redact --no-banner
	$(NPM) run security:npm
	cargo deny check
	cd libs/go && govulncheck ./...
	cd services/hello-service && govulncheck ./...
	trivy fs --scanners vuln,secret,misconfig --severity CRITICAL --exit-code 1 \
		--skip-dirs node_modules --skip-dirs target --skip-dirs gen --skip-dirs .git .

check: fmt-check lint test build contracts docs-check

clean:
	$(CARGO) clean
	rm -rf gen node_modules/.cache
