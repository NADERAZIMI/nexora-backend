# NEXORA Backend

Production backend monorepo for the NEXORA centralized cryptocurrency exchange.

This repository implements the architecture approved in B00
([`docs/architecture/`](docs/architecture/)). B01 established the engineering
baseline. B02 adds exact shared domain primitives: identifiers, money units,
decimal parsing, rounding, unit conversion, time, error codes, and
instrument/contract/margin/fee validation. It does **not** implement trading,
ledger, wallet, or market-data functionality.

## Status

| Phase | Scope | Status |
|-------|-------|--------|
| B00 | Architecture documentation | Complete |
| B01 | Repository bootstrap and engineering baseline | Complete |
| B02 | Domain primitives | Complete |

See [`reports/B01/REPORT.md`](reports/B01/REPORT.md) and
[`reports/B02/REPORT.md`](reports/B02/REPORT.md) for acceptance evidence and
known limitations.

## Repository layout

```text
contracts/          Protobuf, OpenAPI, AsyncAPI, and lint configuration
crates/             Shared Rust crates (config, telemetry, types)
services/           Runnable services (Go edge services, Rust core services)
libs/go/            Shared Go packages
infra/              Local development infrastructure and collector config
scripts/            Developer and CI helper scripts
tools/              Deterministic repository tooling (reserved)
docs/               Architecture, ADRs, security, and engineering standards
reports/            Phase reports and acceptance evidence
```

## Toolchain

| Tool | Pinned version | Purpose |
|------|----------------|---------|
| Rust | 1.99.1 (`rust-toolchain.toml`) | Financial cores, matching engine, risk |
| Go | 1.27.2 (`go.work`, `go.mod`) | APIs, edge, identity, wallets, admin |
| Node.js | 24.x (`.nvmrc`) | Contract and documentation linting |
| PostgreSQL | 17 | Transactional system of record |
| Redpanda | 25.3 | Event backbone |
| Redis | 8.8 | Non-authoritative cache |
| ClickHouse | 24.8 | Analytics |
| MinIO | S3-compatible object storage | WAL archive, snapshots, audit |

Rust is pinned in [`rust-toolchain.toml`](rust-toolchain.toml). Go is pinned by
the `go` and `toolchain` directives in each module and by `go.work`.

## Quick start

Prerequisites: Docker with Compose v2, Go 1.27+, Rust 1.99+, Node.js 24+,
and GNU Make (optional; PowerShell scripts are provided for Windows).

```bash
cp .env.example .env
make dev            # infrastructure + both sample services
make test           # unit tests for Go and Rust
make lint           # formatting and static analysis
make contracts      # protobuf/OpenAPI/AsyncAPI validation
```

On Windows without GNU Make:

```powershell
Copy-Item .env.example .env
./scripts/dev.ps1
./scripts/check.ps1
```

### Sample services

| Service | Language | Port | Endpoints |
|---------|----------|------|-----------|
| `hello-service` | Go | 8080 | `/healthz`, `/readyz`, `/metrics`, `/v1/hello` |
| `core-template` | Rust | 8081 | `/healthz`, `/readyz`, `/metrics`, `/v1/hello` |

Both emit JSON logs, Prometheus metrics, and OTLP traces. The local collector
accepts OTLP on `4317` (gRPC) and `4318` (HTTP).

## Commands

| Command | Description |
|---------|-------------|
| `make dev` | Start local infrastructure and both sample services |
| `make dev-infra` | Start Postgres, Redpanda, Redis, ClickHouse, MinIO, OTel |
| `make test` | Run Go and Rust unit tests plus domain invariants |
| `make domain-gate` | Run domain tests and the 10M-operation invariant gate |
| `make bench-domain` | Run conversion microbenchmarks |
| `make lint` | Check formatting and run linters |
| `make build` | Build all Rust crates and both services |
| `make contracts` | Lint and generate contracts |
| `make docs-check` | Markdown lint |
| `make security` | Secret, dependency, and filesystem scans |
| `make down` | Stop local infrastructure and services |

## Configuration

All services use environment variables with the `NEXORA_` prefix and validate
configuration at startup. See [`.env.example`](.env.example). Secrets in that
file are development-only and must never be used outside local development.

## Security

- Default branch protection, signed commits, and CODEOWNERS review are required.
- CI enforces secret scanning, dependency vulnerability checks, SAST, SBOM
  generation, and license checks.
- Report vulnerabilities according to [`SECURITY.md`](SECURITY.md).
- Do not commit credentials, private keys, seed phrases, or production data.

## Documentation

- Architecture baseline: [`docs/architecture/`](docs/architecture/)
- ADR process: [`docs/adr/`](docs/adr/)
- Threat model v0: [`docs/security/threat-model-v0.md`](docs/security/threat-model-v0.md)
- Data classification: [`docs/security/data-classification.md`](docs/security/data-classification.md)
- Definition of done: [`docs/engineering/definition-of-done.md`](docs/engineering/definition-of-done.md)
- Dependency and license policy: [`docs/engineering/dependency-policy.md`](docs/engineering/dependency-policy.md)

## License

Proprietary. See [`LICENSE`](LICENSE). No right is granted to use, copy,
modify, or distribute this software without written permission.
