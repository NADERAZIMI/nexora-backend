# ADR-026 — B01 monorepo and engineering baseline

- **Status:** Accepted
- **Date:** 2026-10-19
- **Owners:** Platform Engineering, Security
- **Supersedes:** None
- **Superseded by:** None

## Context

B01 must provide a reproducible, secure, observable monorepo for a Go edge and
Rust financial cores. Toolchains, service templates, contracts, local
infrastructure, and CI must be consistent before domain work starts.

## Decision

1. Use one repository with `contracts/`, `crates/`, `services/`, `libs/go/`,
   `infra/`, `scripts/`, `docs/`, and `reports/`.
2. Pin Rust with `rust-toolchain.toml` and Go with module `go`/`toolchain`
   directives plus `go.work`.
3. Use a Cargo workspace for Rust and Go workspace modules for Go.
4. Use one service template per language with health, readiness, metrics, JSON
   logs, OTLP traces, request IDs, and graceful shutdown.
5. Use PostgreSQL 17, Redpanda, Redis, ClickHouse, MinIO, an OTel collector, and
   Prometheus for local development via Docker Compose.
6. Enforce format, lint, test, build, contract, secret, dependency, and SBOM
   checks in CI.
7. Keep financial arithmetic free of floating point through workspace lints and
   review.

## Consequences

### Positive

- One command starts the local stack.
- New services start from a consistent observable baseline.
- Contracts are validated before implementation.
- Toolchain drift is reduced.

### Negative

- Docker is required for the full local stack.
- Rust and Go toolchains both need maintenance.
- CI has more jobs and therefore more runtime.

### Risks

- Floating local image tags can drift; pin digests before shared use.
- Third-party CI actions can be compromised; pin by SHA.
- A monorepo can become tightly coupled; enforce module boundaries.

## Alternatives considered

| Alternative | Reason rejected |
|-------------|-----------------|
| Polyrepo | Slower contract changes and duplicated CI |
| Single-language monorepo | Cannot satisfy Rust performance and Go delivery needs |
| Kubernetes-only local development | Too heavy for daily engineering |
| No local observability stack | Traces and metrics would be untested until staging |

## Enforcement

- CI blocks on format, lint, test, contract, secret, and dependency failures.
- CODEOWNERS review is required for build, security, and financial paths.
- The service template guide is the checklist for new services.

## References

- [`../architecture/19-implementation-roadmap.md`](../architecture/19-implementation-roadmap.md)
- [`../engineering/service-template.md`](../engineering/service-template.md)
- [`../../README.md`](../../README.md)
