# 02 — Technology Stack

---

## 1. Selection criteria

| Criterion | Weight | What we evaluate |
|-----------|--------|------------------|
| Correctness | Highest | Strong typing, no GC pauses on money paths, deterministic behavior |
| Latency | High | Tail latency under load, allocation control, efficient I/O |
| Operational maturity | High | Proven at scale, hiring pool, tooling, observability |
| Ecosystem | Medium | Libraries for gRPC, Postgres, Kafka, crypto, serialization |
| Security | High | Memory safety, dependency audit, supply-chain support |
| Team velocity | Medium | Onboarding cost, testability, local development |

The stack deliberately uses **two primary languages** rather than one:

- **Rust** where correctness and tail latency dominate (matching, ledger, risk).
- **Go** where network fan-out and iteration speed dominate (edge, identity,
  wallet, back office).

Using one language everywhere was rejected (see ADR-002). Using more than two
was rejected to keep the operational and hiring surface manageable.

---

## 2. Language and runtime

### 2.1 Rust 1.8x (stable, pinned via `rust-toolchain.toml`)

Used for:

- `trading-core` (matching engine, OMS, market data publication).
- `account-core` (ledger, holds, positions, settlement).
- `risk-engine`, `oracle-service`, `funding-service`, `market-data`.

Why:

- No garbage collector: predictable p99.9 latency.
- Ownership and `Send`/`Sync` prevent data races in concurrent shards.
- Strong newtypes (`Price`, `Qty`, `Amount`, `AssetId`) make unit confusion a
  compile error.
- Excellent async ecosystem (`tokio`, `tonic`, `axum`), serialization
  (`serde`, `prost`), and storage (`sqlx`, `tokio-postgres`).
- Mature consensus libraries (`openraft`) and WAL primitives.
- Memory safety reduces the severity of remote-code-execution classes.

Key crates (approved baseline; versions pinned in `Cargo.lock`):

| Concern | Crate |
|---------|-------|
| Async runtime | `tokio` |
| gRPC | `tonic`, `prost` |
| HTTP (internal/admin) | `axum`, `tower` |
| Serialization | `serde`, `bincode` (WAL), `prost` (wire) |
| Postgres | `sqlx` (compile-time checked queries) |
| Raft | `openraft` |
| Metrics | `metrics`, `metrics-exporter-prometheus` |
| Tracing | `tracing`, `tracing-opentelemetry` |
| Crypto | `ed25519-dalek`, `k256`, `sha2`, `hmac`, `argon2` |
| Decimal/integer | native `i64`/`i128`; `rust_decimal` only at reporting edges |
| Property tests | `proptest`, `quickcheck` |
| Fuzzing | `cargo-fuzz` (libFuzzer) |
| Error handling | `thiserror` (libraries), `anyhow` (binaries only) |

Rust lint policy for financial crates:

```rust
#![deny(
    clippy::float_arithmetic,
    clippy::cast_possible_truncation,
    clippy::cast_sign_loss,
    clippy::unwrap_used,
    clippy::expect_used,
    clippy::panic,
    clippy::indexing_slicing
)]
```

Explicitly allowed only in tests and benchmark code.

### 2.2 Go 1.24+

Used for:

- `edge-gateway` (REST, WebSocket, rate limiting).
- `identity-service`, `wallet-service`, `back-office-service`,
  `notification-service`.
- Operational tooling and CLIs.

Why:

- Excellent HTTP/WebSocket concurrency with predictable memory per connection.
- Fast compile and deploy cycles for CRUD-heavy services.
- Strong standard library, simple operational model.
- Large hiring pool for platform and security work.

Rules:

- `go vet`, `staticcheck`, `golangci-lint` in CI.
- No `float64` in financial structs; use `shopspring/decimal` or integer types
  only at boundaries.
- Context propagation mandatory; no unbounded goroutines.
- `pprof` endpoints on the private admin port only.

### 2.3 Python 3.12+ (offline only)

Used for analytics, reporting, surveillance feature engineering, and notebook
research. **Never** on the trading or settlement path. Reads from ClickHouse and
object storage only.

### 2.4 TypeScript

Admin UI only (out of scope for this repository). Consumes back-office APIs.

---

## 3. Data stores

### 3.1 PostgreSQL 17 (primary transactional store)

Used for:

- Identity, API keys, KYC state.
- Orders, fills, trades (durable projection), instruments, fee schedules.
- Ledger journal, balances, holds, positions, funding payments.
- Wallet deposits, withdrawals, approvals.
- Outbox, inbox, idempotency keys, audit indexes.

Why:

- ACID transactions with well-understood isolation.
- Rich constraints (`CHECK`, `UNIQUE`, deferred `FK`) that encode invariants.
- Mature HA (Patroni), PITR (WAL-G/pgBackRest), logical replication.
- `NUMERIC(38,0)` supports 128-bit integer amounts losslessly.
- Partitioning and declarative constraints for time-series tables.

Configuration principles:

- One database cluster per bounded context (`identity`, `trading`, `ledger`,
  `wallet`, `ops`) to limit blast radius and tune independently.
- Primary + synchronous standby + asynchronous replica per cluster.
- `synchronous_commit = remote_apply` for financial writes.
- Connection pooling via PgBouncer in transaction mode; services use bounded
  pools.
- No long-running transactions on the hot path (< 50 ms target).
- Autovacuum tuned per table; append-only tables use minimal fillfactor.

### 3.2 Redpanda (Kafka API) — event backbone

Why Redpanda over Kafka:

- No JVM, lower tail latency, simpler operations.
- Kafka-compatible clients and ecosystem.
- Tiered storage to object storage for long retention.
- Rack-aware replication and fast leader election.

Configuration:

- RF=3 for financial topics; `min.insync.replicas=2`; producer `acks=all`.
- Idempotent producers; transactional producers for outbox relay.
- Per-key ordering; partition counts sized for 10x growth.
- Retention: 7 days for commands/events, 30 days for account events, 1 day for
  public market data (rebuildable from snapshots).

NATS JetStream was considered for lower latency but rejected due to weaker
partitioned-log semantics and operational overlap (ADR-004).

### 3.3 ClickHouse 24.x — analytics and time series

Used for:

- Trades, fills, order events (long-term).
- Candles (1s, 1m, 5m, 15m, 1h, 4h, 1d).
- Funding history, liquidation history.
- Risk exposures, PnL attribution, surveillance features.
- Reconciliation reports.

Why: columnar compression, high ingest throughput, SQL, materialized views.
ClickHouse is **not** a system of record and never authorizes a financial action.

### 3.4 Redis 7.x — ephemeral acceleration

Used for:

- API rate-limit token buckets (with local fallback).
- Session and API-key validation cache (short TTL, revocable).
- WebSocket connection registry and presence.
- Market data fan-out hints and hot snapshots.
- Distributed leases for singleton jobs (with fencing tokens).

Never used for balances, holds, orders, or anything requiring durability.
Loss of Redis must degrade gracefully, not corrupt state.

### 3.5 Object storage (S3-compatible)

Used for:

- WAL archives and snapshots for trading/account cores.
- Database base backups and WAL segments.
- Immutable audit logs (Object Lock, WORM).
- Reconciliation artifacts, reports, and exports.
- Model and reference data (instrument icons, docs).

Versioning and Object Lock enabled on audit and financial archives.

### 3.6 Local NVMe

Each stateful core node has dedicated local NVMe for:

- Write-ahead log segments.
- Hot snapshots.
- Raft log.

Replicated across nodes; not a single point of failure. Instance store is
preferred over network disks for predictable fsync latency.

---

## 4. Communication

| Path | Protocol | Rationale |
|------|----------|-----------|
| Client → edge | HTTPS/JSON, WSS | Universally supported |
| Edge → core | gRPC over mTLS | Typed contracts, streaming, deadlines |
| Core → core | gRPC for commands, events for facts | Low latency where synchronous; decoupling otherwise |
| Core → bus | Kafka protocol, transactional producer | Durable ordered log |
| Service → Postgres | TCP + TLS, PgBouncer | Standard |
| Service → Redis | RESP over TLS | Standard |
| Metrics | Prometheus scrape / OTLP | Standard |
| Traces | OTLP → collector | Vendor-neutral |

### 4.1 Timeouts and retries

- Every gRPC call has a deadline. No unbounded calls.
- Retries only on idempotent operations, with exponential backoff and jitter.
- Circuit breakers on all cross-service calls.
- Hedged reads only for idempotent market data, never for financial commands.
- A timeout on a financial command is **ambiguous**, not a failure; callers
  reconcile by idempotency key.

---

## 5. Platform and infrastructure

| Concern | Choice | Notes |
|---------|--------|-------|
| Container runtime | Kubernetes 1.31+ | Stateful cores via StatefulSets + local PV |
| Service mesh | Istio (ambient mode) | mTLS, authorization policy, telemetry |
| Ingress | Envoy Gateway + global LB | TLS termination, weighted routing |
| CDN / WAF | Cloudflare or AWS CloudFront + WAF | DDoS, bot management |
| IaC | Terraform + Terragrunt | All infra declarative, reviewed |
| GitOps | Argo CD | Environment promotion via PR |
| Secrets | HashiCorp Vault + cloud KMS | Dynamic DB credentials, short TTL |
| Identity (workload) | SPIFFE/SPIRE | mTLS identities |
| Image registry | Signed images (cosign), SBOM | Admission control verifies signatures |
| Build | GitHub Actions or GitLab CI | Reproducible, pinned toolchains |
| Artifacts | OCI images + cargo/go module proxies | Internal mirror, vulnerability scanning |
| Batch jobs | Kubernetes CronJobs + Argo Workflows | Funding, reconciliation, reports |
| Feature flags | Unleash or internal config service | Audited changes, kill switches |

### 5.1 Node pools

| Pool | Workload | Characteristics |
|------|----------|-----------------|
| `edge` | edge-gateway, market-data | Network-optimized, autoscaled |
| `core` | trading-core, account-core | CPU-pinned, local NVMe, no oversubscription |
| `platform` | identity, wallet, back office | General purpose |
| `data` | Postgres, Redpanda, ClickHouse | Memory/storage optimized, dedicated |
| `batch` | reconciliation, reports | Spot/preemptible where safe |

Stateful cores are **not** autoscaled horizontally on CPU. Scaling is a
deliberate re-shard operation with a runbook.

---

## 6. Observability stack

| Signal | Tool | Retention |
|--------|------|-----------|
| Metrics | Prometheus + Thanos | 15 d local, 13 mo long-term |
| Dashboards | Grafana | Versioned as code |
| Logs | Loki (or OpenSearch) | 30 d hot, 1 y cold |
| Traces | Tempo / Jaeger via OTel | 7 d sampled, 30 d tail-sampled errors |
| Profiling | Pyroscope / pprof | 7 d |
| Alerting | Alertmanager → PagerDuty | Sev-based routing |
| Audit | WORM object storage + SIEM | 7 y |
| Synthetic monitoring | Global probes | 1 min interval |

Every log line includes `trace_id`, `span_id`, `service`, `env`, `shard`,
`account_id` (hashed where required), and `order_id` when applicable. PII is
never logged.

---

## 7. Security tooling

| Layer | Tooling |
|-------|---------|
| SAST | Semgrep, CodeQL |
| Dependency scanning | `cargo audit`, `govulncheck`, Dependabot/Renovate |
| Container scanning | Trivy, Grype |
| Secret scanning | Gitleaks, GitHub secret scanning |
| IaC scanning | Checkov, tfsec |
| DAST | OWASP ZAP in staging |
| Fuzzing | `cargo-fuzz`, Go native fuzzing |
| Pen testing | External firm before launch, annually |
| Bug bounty | Public program post-launch |
| Supply chain | SLSA level 3 target, signed provenance |

---

## 8. Rejected alternatives

| Alternative | Why rejected |
|-------------|--------------|
| All-in-one Rust monolith | Single failure domain; couples scaling of matching and accounts; harder team ownership |
| All-in-one Go | GC pauses and weaker type safety on the money path; insufficient tail-latency control |
| Node.js/TypeScript backend | Insufficient CPU predictability; weak numeric types |
| Java/Kotlin core | Viable, but JVM tuning and GC on the hot path add operational risk; Rust chosen for determinism |
| Erlang/Elixir | Excellent concurrency, but fewer low-latency storage and matching examples; team familiarity |
| MySQL | Weaker partitioning, JSON, and constraint features for this workload; Postgres ecosystem preferred |
| MongoDB as ledger | No multi-document ACID guarantees at the required isolation and maturity; schema flexibility is a liability for money |
| Redis as ledger | Durability and consistency insufficient |
| Kafka + Flink for matching | Adds latency and operational complexity; matching is a state machine, not analytics |
| Blockchain settlement | Regulatory and latency constraints for a CEX; custody remains centralized |
| gRPC-only public API | Poor browser and partner compatibility; REST/WS chosen at edge |
| GraphQL public API | Complexity, query-cost attacks, and cacheability issues for market data |
| 2PC across services | Fragile, blocking, and unnecessary with clearing-account legs |

---

## 9. Versioning and upgrade policy

- Rust and Go toolchains pinned; upgrades are deliberate PRs with benchmarks.
- Postgres major upgrades via logical replication with a rollback window.
- Redpanda rolling upgrades with one broker at a time; RF=3 maintained.
- Public API versioned in the path (`/api/v1`); breaking changes require a new
  version and a deprecation window.
- Event schemas use a registry and backward-compatible evolution only.
- WAL format is versioned; readers support the current and previous version.
- Snapshots embed the state schema version and code version.

---

## 10. Local development

`docker compose` provides:

- Postgres 17 with the same extensions and settings as production.
- Redpanda single node.
- Redis.
- ClickHouse.
- MinIO for object storage.
- Jaeger or Grafana Tempo for traces.
- Prometheus + Grafana.

A `make dev` target builds and runs the stack. Seed scripts create instruments,
accounts, and a synthetic order book. Integration tests use Testcontainers and
run against real Postgres/Redpanda, not mocks, for financial paths.

---

## 11. Stack summary

```
Languages      Rust (cores), Go (edge/support), Python (analytics only)
Contracts      gRPC/protobuf internal, REST/JSON + WebSocket public
State          In-memory deterministic actors + WAL + snapshots
Database       PostgreSQL 17 (ACID, system of record for projections)
Bus            Redpanda (Kafka API), transactional outbox, idempotent consumers
Analytics      ClickHouse
Cache          Redis (non-authoritative)
Objects        S3-compatible with versioning + Object Lock
Platform       Kubernetes, Istio, Terraform, Argo CD, Vault, SPIRE
Observability  OpenTelemetry, Prometheus, Grafana, Loki, Tempo
Security       HSM/MPC custody, mTLS, SSO + hardware MFA, signed images
```
