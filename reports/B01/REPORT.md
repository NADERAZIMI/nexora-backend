# B01 — Repository Bootstrap and Engineering Baseline

**Phase:** B01
**Status:** Implementation complete; local verification complete except for Docker/GitHub-runner checks
**Date:** 2026-10-19
**Scope:** Engineering baseline only. No trading, ledger, wallet, market-data,
or B02 domain functionality was implemented.

---

## 1. Executive summary

B01 delivers a buildable Go + Rust monorepo with pinned toolchains, shared
service foundations, contract tooling, local infrastructure, CI, security
scanning, and engineering standards.

Key outcomes:

- Go service template and Rust core template both run, return health/readiness,
  emit Prometheus metrics, propagate request IDs, and expose structured logs.
- Rust workspace establishes the financial-core toolchain and denies floating
  point through workspace lints.
- Go workspace provides reusable configuration, logging, observability, and HTTP
  middleware packages.
- Protobuf, OpenAPI, and AsyncAPI contracts validate and generate code.
- Docker Compose defines PostgreSQL 17, Redpanda, Redis, ClickHouse, MinIO,
  OpenTelemetry Collector, and Prometheus.
- CI enforces formatting, linting, tests, builds, contract checks, secret
  scanning, SAST, dependency/license checks, SBOM generation, and Compose
  validation.
- ADR, threat-model, data-classification, dependency-policy, and DoD processes
  are in place.
- Raw market data, canonical pricing, and display configuration are structurally
  separated; display data is explicitly banned from financial paths.

The implementation was built and tested locally. Docker images and the full
local stack could not be run because Docker is not installed in the execution
environment, and GitHub Actions could not be executed because there is no
remote runner. Those items are called out explicitly in section 7.

---

## 2. Constraints honored

| Constraint | Result |
|------------|--------|
| Backend only; never modify frontend | No frontend files exist or were touched |
| Do not implement trading functionality | Only health, readiness, metrics, and hello endpoints |
| Do not advance to B02 | No domain primitives, ledger, matching, or margin logic |
| Use Go for APIs/edge/wallets/admin/infra | Go workspace, shared libraries, and Go service template |
| Use Rust for matching/financial cores | Rust workspace, config/telemetry crates, Rust core template |
| Preserve raw external market data | Append-only raw contracts and AsyncAPI channel |
| Reserve Market Pricing & Display Configuration | Separate `nexora.display.v1` package and ADR-025 |
| Keep display data out of risk/settlement | Documented boundary, package separation, review rules |
| PostgreSQL 17, Redpanda, Redis, approved infra | Compose stack and configuration |
| Extensible Bybit/MEXC and custody interfaces | Provider-neutral protobuf adapters |
| B00 architecture frozen | No files under `docs/architecture/` were modified |

---

## 3. Repository structure delivered

```text
.
├── .github/                 CI, CODEOWNERS, PR template, Dependabot
├── contracts/               Protobuf, OpenAPI, AsyncAPI, Buf, Spectral
├── crates/                  Rust shared crates
│   ├── nexora-config/       Validated environment configuration
│   └── nexora-telemetry/    JSON logs, OTLP traces, Prometheus metrics
├── docs/
│   ├── adr/                 ADR process, template, B00 index, ADR-025/026
│   ├── engineering/         DoD, service template, display boundary, deps
│   └── security/            Threat model v0, data classification
├── infra/                   OTel collector, Prometheus, Postgres bootstrap
├── libs/go/                 Shared Go config/logging/observability/HTTP
├── reports/B01/             This report
├── scripts/                 PowerShell developer and validation scripts
├── services/
│   ├── hello-service/       Go reference service
│   └── core-template/       Rust reference service
└── tools/                   Reserved for deterministic repository tooling
```

Root files include `Cargo.toml`, `Cargo.lock`, `rust-toolchain.toml`,
`go.work`, `Makefile`, `compose.yaml`, `package.json`, `package-lock.json`,
`.env.example`, `.editorconfig`, `.gitignore`, `.dockerignore`, `LICENSE`,
`SECURITY.md`, and `README.md`.

---

## 4. Requirement traceability

### 4.1 Go backend surface

- `libs/go/config`: validated `NEXORA_*` configuration with fail-fast errors.
- `libs/go/logging`: JSON `slog` logger with service/environment/version.
- `libs/go/observability`: OTLP/gRPC trace export and Prometheus registry.
- `libs/go/httpx`: request IDs, panic recovery, access logs, request metrics.
- `libs/go/version`: link-time build metadata.
- `services/hello-service`: reference Go HTTP service with graceful shutdown,
  health/readiness, metrics, and request-ID propagation.

### 4.2 Rust financial-core surface

- Cargo workspace with pinned Rust 1.99.1.
- Workspace lints deny `float_arithmetic` and `float_cmp` and warn on lossy
  casts.
- `nexora-config`: validated, typed service configuration.
- `nexora-telemetry`: JSON tracing, OTLP trace export, Prometheus recorder.
- `core-template`: Axum service with health/readiness, metrics, request IDs,
  body limits, panic catching, and graceful shutdown.

No matching, order-book, ledger, margin, funding, or liquidation code exists.

### 4.3 Reliability and performance baseline

- Stateless services with bounded request bodies.
- Explicit HTTP read/header/write/idle timeouts in Go.
- Graceful shutdown with a 1–60 second validated budget.
- Request correlation on every response.
- Panic recovery that does not leak internals.
- Prometheus request counters and latency histograms.
- OTLP trace export that can be disabled with an empty endpoint.
- Released Rust binary built with `lto = "thin"`, `codegen-units = 1`,
  `panic = "abort"`, and stripped symbols.

### 4.4 Local infrastructure

`compose.yaml` includes:

| Component | Version/tag |
|-----------|-------------|
| PostgreSQL | `17-alpine` |
| Redpanda | `v25.3.17` |
| Redis | `8.8.3-alpine` |
| ClickHouse | `24.8-alpine` |
| MinIO | `pgsty/minio:RELEASE.2026-08-04T00-00-00Z` (community mirror) |
| OTel Collector Contrib | `0.163.1` |
| Prometheus | `v3.5.0` |

The `app` profile adds `hello-service` and `core-template`. Prometheus scrapes
both services and the collector. PostgreSQL initialization enables `pgcrypto`
and creates the `platform` schema only.

### 4.5 External integration interfaces

- `contracts/proto/nexora/integrations/v1/venue_adapter.proto`:
  provider-neutral `VenueAdapterService` for Bybit, MEXC, and future venues.
- `contracts/proto/nexora/custody/v1/custody_provider.proto`:
  provider-neutral `CustodyProviderService` with HSM/MPC capabilities,
  idempotent signing, approval context, and no private-key export.
- These are interfaces only; no venue or custody implementation is present.

### 4.6 Raw data and display separation

- `contracts/proto/nexora/marketdata/v1/raw_market_data.proto` preserves venue
  payloads, hashes, receive times, and ingest IDs.
- `contracts/asyncapi/marketdata/v1/asyncapi.yaml` defines the raw trade channel
  `market.raw.{venue}.{symbol}`.
- `contracts/proto/nexora/display/v1/display_config.proto` reserves the
  admin-controlled Market Pricing & Display Configuration capability.
- Display metrics carry an explicit source and synthetic label; the message is
  marked display-only.
- `docs/engineering/market-data-and-display-boundary.md` and ADR-025 prohibit
  display data from entering matching, ledger, settlement, risk, funding,
  liquidation, or ADL calculations.
- Synthetic volume must never be presented as executed trading volume.

### 4.7 Contracts and generation

- Buf lint, build, and generation pass.
- Generated Go gRPC/protobuf stubs are produced under `gen/go` (ignored by Git,
  uploaded as a CI artifact).
- Spectral validates OpenAPI 3.1 with no warnings or errors.
- The AsyncAPI 3.1 document validates with the official parser.

### 4.8 CI and security

CI jobs:

1. Rust: format, Clippy with `-D warnings`, tests, build.
2. Go: format, vet, race tests, build, golangci-lint.
3. Contracts: Buf lint/build/generate, OpenAPI, AsyncAPI.
4. Security: gitleaks, cargo-deny, govulncheck, npm audit, Trivy, SBOM.
5. SAST: Semgrep in a separate job.
6. Dockerfiles and Compose: Hadolint plus `docker compose config`.
7. Documentation: markdownlint.

Security controls:

- `.gitleaks.toml` with default rules plus NEXORA-specific key/secret rules.
- `deny.toml` with advisories, bans, license, and source policy.
- `govulncheck` clean after upgrading `golang.org/x/net` to `v0.60.0`.
- Non-critical npm advisories documented in the dependency policy.
- Least-privilege workflow permissions (`contents: read`,
  `pull-requests: read`).

### 4.9 ADR, threat model, and DoD

- `docs/adr/README.md`: process, B00 ADR index, new ADRs.
- `docs/adr/0000-template.md`: reusable ADR template.
- `docs/adr/0025-market-data-display-separation.md`: accepted boundary decision.
- `docs/adr/0026-b01-monorepo-baseline.md`: accepted baseline decision.
- `docs/security/threat-model-v0.md`: assets, actors, trust boundaries, STRIDE,
  priority threats, residual risks.
- `docs/security/data-classification.md`: Public/Internal/Confidential/Restricted
  handling, retention, and field guidance.
- `docs/engineering/dependency-policy.md`: allowed licenses and scan policy.
- `docs/engineering/definition-of-done.md` and
  `.github/pull_request_template.md`.

---

## 5. Verification evidence

All commands were executed on Windows with Go 1.27.2, Rust 1.99.1
(`x86_64-pc-windows-gnu`), Node.js 24.21.0, Buf 1.74.0, cargo-deny 0.20.2, and
gitleaks 8.30.1.

### 5.1 Rust

| Check | Result |
|-------|--------|
| `cargo fmt --all -- --check` | Pass |
| `cargo clippy --workspace --all-targets --all-features -- -D warnings` | Pass, 0 warnings |
| `cargo test --workspace --all-features` | Pass, 13 tests |
| `cargo build --release --workspace` | Pass |
| `cargo deny check` | Advisories, bans, licenses, sources all pass |

### 5.2 Go

| Check | Result |
|-------|--------|
| `gofmt -l libs/go services/hello-service` | Empty |
| `go vet ./...` (both modules) | Pass |
| `go test -race ./...` (both modules) | Pass |
| `go build ./...` (both modules) | Pass |
| `golangci-lint run ./...` (both modules) | 0 issues |
| `govulncheck ./...` (both modules) | No vulnerabilities found |

Go top-level tests: 11, plus 6 invalid-config subtests.
Rust tests: 13.

### 5.3 Contracts and documentation

| Check | Result |
|-------|--------|
| `buf lint` | Pass |
| `buf build` | Pass |
| `buf generate` | Pass; Go stubs generated |
| `npm run lint:openapi` | Pass; no warnings/errors |
| `npm run lint:asyncapi` | Pass; document valid |
| `npm run lint:md` | Pass; 39 Markdown files, 0 issues |
| Relative link check across all Markdown files | Pass; 0 broken links |
| Hadolint v2.15.1 on both Dockerfiles | Pass, 0 findings |
| `docker compose config --quiet` (standalone Compose v5.6.0) | Pass |
| `actionlint` on CI workflow | Pass, 0 findings |
| OTel Collector `validate` (v0.163.1) | Pass |
| `promtool check config` (v3.5.0) | Pass |
| `scripts/check.ps1` (PowerShell 7.6.7) | Pass |
| YAML parse of Compose, CI, Dependabot, OTel, Prometheus | Pass |

### 5.4 Security

| Check | Result |
|-------|--------|
| `gitleaks dir .` | No leaks found |
| `gitleaks detect` (Git history) | 1 commit scanned; no leaks found |
| `cargo deny check` | All checks pass |
| `govulncheck` | No vulnerabilities |
| `npm audit --audit-level=critical` | Pass |
| npm advisory detail | 10 non-critical dev-tooling advisories remain |

### 5.5 Runtime smoke tests

Both services were started locally with OTLP export disabled because no
collector was running.

**Go `hello-service` on `127.0.0.1:18080`:**

- `/healthz` → 200, `{"service":"hello-service","status":"ok","version":"dev"}`
- `/readyz` → 200, status `ready`
- `/v1/hello` → 200, generated request ID echoed in header and body
- `/metrics` → 200, contains `http_requests_total`
- stdout logs are JSON with service, environment, version, request ID, method,
  path, status, and duration
- stderr empty

**Rust `core-template` on `127.0.0.1:18081`:**

- `/healthz` → 200, `{"status":"ok","service":"core-template","version":"0.1.0"}`
- `/readyz` → 200, status `ready`
- `/v1/hello` → 200, generated request ID echoed in header and body
- `/metrics` → 200, contains `http_requests_total`
- stdout logs are JSON with service metadata and request spans
- stderr empty

### 5.6 Repository

- Git repository initialized on `main`.
- No build output (`target/`, `gen/`, `node_modules/`) is tracked.
- `.env` is not tracked; only `.env.example` is present.
- B00 architecture files are unchanged.

---

## 6. Acceptance-gate status

| B01 exit gate | Status | Evidence / limitation |
|---------------|--------|-----------------------|
| Fresh clone builds and runs locally with one command | **Partially verified** | Both services build and run; Compose model validates and `make dev` is provided, but Docker is unavailable to start the full stack |
| CI is green on a trivial PR and blocks on lint/test failures | **Configured, not executed** | Workflow defines required jobs with no `continue-on-error`; no GitHub remote/runner exists here |
| Sample service emits logs, metrics, and traces to the local stack | **Partially verified** | JSON logs and Prometheus metrics verified; OTLP export is configured but no collector was running |
| Secrets and dependency scans are enforced | **Partially verified** | CI is configured; local gitleaks, cargo-deny, govulncheck, and npm audit were run |
| ADR and DoD templates are in use | **Yes** | ADR template/index/025/026, DoD, and PR template exist |

B01 implementation is complete. The three partial gates require Docker and a
GitHub Actions runner, which are environmental prerequisites rather than
missing implementation.

---

## 7. Checks not performed and honest limitations

1. **Docker Compose was not started.** Docker is not installed in the execution
   environment. The Compose model was validated with standalone Compose v5.6.0,
   and the OTel Collector and Prometheus configurations were validated with
   their native validators, but `docker compose up`, image builds,
   service-to-service networking, PostgreSQL initialization, Redpanda, Redis,
   ClickHouse, MinIO, and Prometheus were not runtime-tested.
2. **Container images were not built.** Both Dockerfiles were written and
   reviewed, but `docker build` could not run.
3. **GitHub Actions did not run.** There is no remote repository or runner in
   this environment. Workflow YAML was parsed successfully, but job execution,
   Semgrep, Trivy, Syft, and artifact upload are unverified.
4. **GNU Make is not installed.** The Makefile was not executed as a whole.
   Equivalent commands were run directly for Go, Rust, Buf, npm, and scanners.
   `scripts/check.ps1` was executed successfully with portable PowerShell 7.6.7.
5. **Linux builds were not executed locally.** Rust was built with the Windows
   GNU toolchain. CI covers Ubuntu builds.
6. **Rust MSVC was not used.** The environment has no Visual Studio build tools;
   the GNU toolchain and MinGW were used for local verification.
7. **OTLP traces were not observed in a collector.** Export is configured and
   can be disabled; no collector was available to confirm end-to-end delivery.
8. **Git history scanning covers only the initial commit.** `gitleaks detect`
   scanned one commit and found no leaks. CI will scan all future history.
9. **Semgrep, Trivy, and Syft were not run locally.** They are configured in CI.
10. **Branch protection and signed commits cannot be enforced from repository
    files.** They require hosting-platform administration.

---

## 8. Risks and follow-ups

| Risk | Severity | Follow-up |
|------|----------|-----------|
| MinIO official community image is unavailable; a community mirror is used | Medium | Pin a verified official image or digest before shared use |
| Non-critical npm advisories in documentation tooling | Low | Upgrade when upstream fixes exist; critical advisories already block CI |
| OTLP exporter uses insecure gRPC and no TLS configuration | Medium | Add TLS/mTLS and workload identity before internal exposure |
| CI actions are pinned by tag, not commit SHA | Medium | Pin every action to a reviewed SHA before production use |
| Rust dependency tree is large (OTel/tonic/rustls) | Low | Monitor build time and binary size; review at B07 |
| No Go license automation yet | Low | Add automated Go license reporting with the first production service |
| Docker/GitHub gates unverified | Medium | Run `make dev`, `make check`, and the CI workflow on a machine with Docker and a GitHub remote |
| Metrics use raw request paths as labels | Low | Replace with route templates before high-cardinality traffic |
| No rate limiting or internal mTLS yet | Expected | Delivered in B10/B15 hardening, not B01 |

---

## 9. Explicit non-goals preserved

- No matching engine, order book, OMS, ledger, wallet, margin, funding,
  liquidation, ADL, or settlement implementation.
- No B02 money/time/identifier primitives.
- No frontend code.
- No production credentials or key material.
- No synthetic volume presented as executed volume.
- No display configuration path into risk or settlement.

---

## 10. Completion statement

B01 is complete and stopped. No B02 work has started. The next phase requires
explicit authorization and should begin only after the Docker and GitHub CI
gates in section 6 are exercised in an environment that provides those
prerequisites.
