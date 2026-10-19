# 20 — Architecture Decision Records

Format: Context → Decision → Consequences → Alternatives.
Status values: `Proposed`, `Accepted`, `Superseded`, `Deprecated`.

---

## ADR-001 — Rust for financial cores, Go for edge and support

**Status:** Accepted
**Date:** 2026-10-19
**Owners:** Platform Architecture

### Context

Matching, ledger, and risk require predictable tail latency, exact arithmetic,
and strong compile-time guarantees. Edge and support services require fast
iteration, high connection fan-out, and a large hiring pool.

### Decision

- Rust for `trading-core`, `account-core`, `risk-engine`, `oracle-service`,
  `funding-service`, and `market-data`.
- Go for `edge-gateway`, `identity-service`, `wallet-service`, `back-office`,
  and `notification-service`.
- Python only for offline analytics.

### Consequences

- Two toolchains, two dependency policies, two on-call skill sets.
- Clear performance and correctness boundaries.
- Shared contracts via protobuf and OpenAPI prevent drift.
- Hiring must cover both languages.

### Alternatives

- All Rust: rejected due to slower CRUD iteration and smaller hiring pool.
- All Go: rejected due to GC pauses and weaker numeric type safety on the hot
  path.
- JVM: viable, but GC tuning and operational overhead were judged higher.

---

## ADR-002 — Modular services, not a monolith

**Status:** Accepted

### Context

Trading and accounts scale on different dimensions and have different failure
domains. A monolith would couple latency, deployment, and blast radius.

### Decision

Separate `trading-core` (per symbol) from `account-core` (per account), with a
small gRPC contract between them. Supporting services are independently
deployable.

### Consequences

- More operational surface and network hops.
- Independent scaling and failure isolation.
- Contract versioning and distributed tracing become mandatory.
- No shared database or shared in-process state.

### Alternatives

- Modular monolith: simpler operations, but a single failure domain and coupled
  releases.
- Microservice-per-function: rejected as excessive fragmentation.

---

## ADR-003 — Integer money, no floating point

**Status:** Accepted

### Context

Binary floating point cannot represent decimal money exactly and produces
audit-defeating rounding drift.

### Decision

Represent prices as integer ticks, quantities as integer lots, and amounts as
signed 128-bit integers in atomic units. Use explicit rounding rules and decimal
strings at the API boundary.

### Consequences

- Exact arithmetic and reproducible audits.
- Every operation must define scale and rounding.
- Conversions require careful rational arithmetic.
- Lint rules and tests enforce the prohibition.

### Alternatives

- Decimal libraries everywhere: correct but slower and easy to mix scales.
- Floating point with tolerances: rejected outright.

---

## ADR-004 — Single-writer actors with Raft replication

**Status:** Accepted

### Context

Order books and account balances need linearizable updates under high
concurrency. Lock-based shared state creates contention and nondeterminism.

### Decision

One actor per symbol shard and one leader per account shard. Commands are
sequenced, appended to a WAL, and replicated via Raft. Followers apply the same
deterministic state machine.

### Consequences

- Predictable latency and no lock contention in the hot path.
- Horizontal scaling by adding shards.
- Leader failover pauses a shard briefly.
- Cross-shard operations require sagas or clearing legs.

### Alternatives

- Shared-memory locking: rejected for contention and nondeterminism.
- Database-only matching: rejected for latency and lock contention.
- Active-active multi-region: rejected for consensus latency.

---

## ADR-005 — WAL + snapshots + asynchronous Postgres projection

**Status:** Accepted

### Context

Financial state must survive crashes without making the database the hot-path
bottleneck.

### Decision

Cores persist commands and events to a local WAL with group commit, replicate
via Raft, and snapshot periodically to local disk and object storage. Postgres
holds an asynchronous, queryable projection. Recovery replays WAL from the last
snapshot and verifies a state hash.

### Consequences

- Low-latency durable writes.
- Two representations to reconcile (WAL and projection).
- Snapshot and replay tooling is critical.
- Postgres lag does not block trading but is monitored.

### Alternatives

- Synchronous Postgres writes on every command: simpler, but higher latency and
  a database bottleneck.
- Event sourcing without snapshots: too slow to recover at scale.

---

## ADR-006 — PostgreSQL as the transactional system of record

**Status:** Accepted

### Context

Identity, ledger projections, orders, and wallets need ACID transactions,
constraints, and mature HA.

### Decision

Use PostgreSQL 17 with Patroni, synchronous replication, partitioned tables, and
`NUMERIC(38,0)` for atomic-unit amounts. One database per bounded context.

### Consequences

- Strong consistency and familiar tooling.
- Sharding is required at scale.
- Partitioning and autovacuum need active management.
- Cross-database joins are forbidden; services compose data via APIs/events.

### Alternatives

- MySQL: weaker partitioning/constraint features for this workload.
- Distributed SQL: operational complexity and unpredictable tail latency.
- NoSQL: insufficient multi-row transactional guarantees for money.

---

## ADR-007 — Redpanda as the event backbone

**Status:** Accepted

### Context

Services need a durable, ordered, replayable log with Kafka compatibility and
low operational overhead.

### Decision

Use Redpanda with RF=3, `acks=all`, idempotent producers, and tiered storage.
Topics are keyed by symbol or account to preserve ordering.

### Consequences

- Kafka ecosystem compatibility without JVM brokers.
- Per-key ordering and replay.
- At-least-once delivery requires idempotent consumers.
- Another stateful system to operate and monitor.

### Alternatives

- Apache Kafka: mature but heavier operations.
- NATS JetStream: lower latency but weaker partitioned-log semantics for this
  use case.
- RabbitMQ: not designed for long retention and replay.

---

## ADR-008 — Transactional outbox, idempotent consumers, no 2PC

**Status:** Accepted

### Context

A database commit and an event publish cannot be atomic across systems. Tw-phase
commit is blocking and fragile.

### Decision

Write an outbox row in the same transaction as the state change. A relay
publishes it. Consumers deduplicate by `event_id` in an inbox table within the
same transaction as the effect.

### Consequences

- At-least-once delivery with effectively-once effects.
- Eventual consistency and lag monitoring.
- Every consumer needs an inbox and idempotent handler.
- No distributed transactions.

### Alternatives

- 2PC: rejected for availability and operational complexity.
- Publish-then-commit: can lose or duplicate events.
- Change data capture only: insufficient for cross-service business events.

---

## ADR-009 — Clearing-account settlement legs

**Status:** Accepted

### Context

A trade touches two accounts that may live on different shards. A cross-shard
transaction would require 2PC or distributed locks.

### Decision

Settle each participant independently against a system clearing account. Each leg
is a balanced journal batch and is idempotent on `(trade_id, side)`. The clearing
account carries transient imbalances that are monitored and reconciled.

### Consequences

- No cross-shard locks or 2PC.
- Horizontal scalability.
- Temporary clearing imbalance; requires monitoring and a circuit breaker.
- Global zero-sum is verified asynchronously.

### Alternatives

- Single global ledger database: simpler but a scaling bottleneck.
- 2PC across shards: rejected for availability.
- Shard-by-symbol settlement: complex account replication.

---

## ADR-010 — Holds are separate from journal entries

**Status:** Accepted

### Context

Pending orders and withdrawals must reserve funds without moving them.

### Decision

Model reservations as holds with `remaining` amounts. Holds adjust `held` and
`available` but do not create journal entries. Settlement consumes the hold and
posts the journal; cancel releases it.

### Consequences

- Clean journal of actual transfers.
- `held` must be reconciled with active holds.
- Holds need TTLs and crash recovery.
- Available balance is a derived value.

### Alternatives

- Post a journal entry to a suspense account on reserve: doubles journal volume
  and complicates reversal.
- No holds, check balance at settlement: allows overspending.

---

## ADR-011 — Tick/lot integer units with explicit scale

**Status:** Accepted

### Context

Decimal prices and quantities invite rounding ambiguity and unit confusion.

### Decision

Prices are integer ticks; quantities are integer lots. Each instrument stores
`tick_size` and `lot_size`. Conversions use exact rational arithmetic with
explicit rounding.

### Consequences

- No representation ambiguity.
- Clients must format/parse decimals at the edge.
- Instrument config changes require care.
- Cross-instrument aggregation needs conversion.

### Alternatives

- Decimal prices everywhere: flexible but slower and error-prone.
- Fixed 8-decimal amounts for all assets: lossy for 18-decimal tokens.

---

## ADR-012 — Deterministic matching with no wall clock

**Status:** Accepted

### Context

Replay and audit require that the same inputs produce the same book and trades.

### Decision

The sequencer assigns event time and sequence. Matching never reads the wall
clock, uses randomness, iterates hash maps, or performs I/O. State hashes are
verified after replay.

### Consequences

- Reproducible state and auditable trades.
- External data must enter as recorded events.
- Timer events are injected into the sequencer.
- Any nondeterminism is a Sev-1 bug.

### Alternatives

- Wall-clock timestamps in matching: rejected for replay divergence.
- Best-effort replay: rejected for audit and recovery.

---

## ADR-013 — USDT-M linear perpetuals with base-unit quantity

**Status:** Accepted

### Context

The first futures product must be simple to reason about and settle in USDT.

### Decision

Launch linear USDT-M perpetuals. Quantity is in base units; contract size is 1
base unit for launch instruments, with a `Multiplier` type for future variants.
PnL, margin, and funding are denominated in USDT.

### Consequences

- Simple PnL and margin formulas.
- No inverse-contract complexity.
- Contract-size changes require a new instrument version.
- Coin-margined products are a future extension.

### Alternatives

- Inverse contracts first: more complex PnL and margin.
- Quanto contracts: basis and FX complexity.

---

## ADR-014 — Tiered margin with continuous maintenance

**Status:** Accepted

### Context

Large positions need higher margin requirements, but discontinuous tiers create
liquidation cliffs and gaming opportunities.

### Decision

Use notional-based tiers with `MM = notional * mmr - maintenance_amount`.
`maintenance_amount` is derived to make MM continuous at boundaries. Tier
selection uses the higher of current and post-order notional.

### Consequences

- Smooth risk curve and no boundary cliff.
- More complex configuration and validation.
- Backtests must verify continuity.
- Tier changes require governance and notice.

### Alternatives

- Flat MMR: unsafe for large positions.
- Discontinuous tiers: exploit at boundaries.
- Risk-based portfolio margin: out of scope for launch.

---

## ADR-015 — Liquidation waterfall: margin → insurance → ADL → socialized loss

**Status:** Accepted

### Context

Liquidation can produce a deficit when the market gaps or liquidity disappears.
The exchange must remain solvent without arbitrary loss allocation.

### Decision

Use the position's remaining margin first, then the insurance fund, then ADL of
profitable opposing positions, and only as a last resort (governance-approved)
socialized loss. Backstop liquidity may be used before the fund.

### Consequences

- Predictable, documented loss allocation.
- Insurance fund and ADL are critical path.
- ADL ranking must be transparent and deterministic.
- Socialized loss remains a rare, governed emergency power.

### Alternatives

- Auto-socialize immediately: unfair and reputationally damaging.
- Exchange absorbs all losses: unsustainable.
- No ADL: insolvency risk when the fund is depleted.

---

## ADR-016 — REST/JSON and WebSocket with decimal strings

**Status:** Accepted

### Context

Retail, institutional, and browser clients need a broadly compatible API.
Binary protocols complicate integration and debugging.

### Decision

Public API is REST/JSON over HTTPS and WebSocket for streaming. Monetary values
are decimal strings. Internal services use gRPC/protobuf. OpenAPI 3.1 and
AsyncAPI 3 are published.

### Consequences

- Broad compatibility and easy debugging.
- JSON overhead versus binary; mitigated by compression and conflation.
- String parsing must be strict.
- Internal and external models are separate.

### Alternatives

- gRPC-only public API: poor browser support.
- GraphQL: query-cost and caching complexity for market data.
- FIX only: useful for institutions but not retail.

---

## ADR-017 — Tiered HSM/MPC custody with quorum signing

**Status:** Accepted

### Context

Customer funds are the highest-value target. A single compromised key or
operator must not be able to move them.

### Decision

Use hot/warm/cold tiers, HSM or MPC keys, quorum signing, address whitelists,
and daily reconciliation. Cold storage holds the majority of assets. No single
person can sign a withdrawal.

### Consequences

- Strong security guarantees.
- Operational complexity and ceremony overhead.
- Withdrawal latency for large amounts.
- Vendor and key-recovery risk must be managed.

### Alternatives

- Single hot wallet: unacceptable risk.
- Fully external custody: less control and higher fees.
- On-chain multisig only: chain-specific and less flexible.

---

## ADR-018 — Shard by symbol and account

**Status:** Accepted

### Context

A single matching engine or ledger cannot scale indefinitely, and different
workloads shard naturally on different keys.

### Decision

Symbol shards for trading; account (user-co-located) shards for ledger,
positions, and holds. Shard maps are versioned and cached. New shards are added
for growth; existing shards are moved only with a controlled migration.

### Consequences

- Horizontal scalability.
- Cross-shard operations need clearing legs or sagas.
- Shard routing and rebalancing are operational concerns.
- Hot symbols/accounts need dedicated capacity.

### Alternatives

- Single shard: simple but limited.
- Shard by trade: breaks account locality.
- Consistent hashing with automatic rebalancing: risky for financial state.

---

## ADR-019 — Single primary region, warm DR

**Status:** Accepted

### Context

Low-latency matching requires a single writer per shard. Multi-region
active-active consensus would add unacceptable latency.

### Decision

Run cores in one primary region across three AZs. Maintain a warm standby region
with asynchronous replication of WAL, Postgres, and object storage. Fail over
via a documented runbook.

### Consequences

- Low latency and simple consistency.
- Cross-region RPO up to 5 minutes.
- DR drills and replication monitoring are mandatory.
- Users far from the primary region experience higher latency.

### Alternatives

- Active-active multi-region: rejected for consensus latency and complexity.
- Single AZ: unacceptable availability.
- Multi-region active-passive with synchronous replication: latency prohibitive.

---

## ADR-020 — Observability with OpenTelemetry, Prometheus, and WORM audit

**Status:** Accepted

### Context

Financial systems need end-to-end traceability, SLO monitoring, and
tamper-evident audit records.

### Decision

Instrument every service with OpenTelemetry traces and metrics, export to
Prometheus/Tempo/Loki, and write audit events to WORM object storage with a hash
chain. Trace IDs are returned to clients.

### Consequences

- Strong debugging and auditability.
- Telemetry cost and cardinality control.
- Sensitive data must be redacted.
- Audit storage is immutable and retained for 7 years.

### Alternatives

- Logs only: insufficient for latency attribution.
- Vendor APM lock-in: avoided by using open standards.
- Mutable audit tables: rejected for tamper evidence.

---

## ADR-021 — Public API versioning in the path

**Status:** Accepted

### Context

Clients integrate deeply and need predictable breaking-change management.

### Decision

Use `/v1` in the REST path and versioned WebSocket channels. Additive changes
are non-breaking. Breaking changes require a new major version and a 90-day
deprecation window.

### Consequences

- Clear compatibility contract.
- Multiple versions may run concurrently.
- Deprecation and telemetry are required.
- Internal contracts use protobuf compatibility rules instead.

### Alternatives

- Header-based versioning: less visible and cache-friendly.
- No versioning: breaks clients unpredictably.
- Permanent backward compatibility: accumulates complexity.

---

## ADR-022 — Holds expire; orders do not silently roll back

**Status:** Accepted

### Context

Authorization and order commitment are separate steps. A crash between them can
leave a hold without an order.

### Decision

An authorization decision is valid for 2 seconds; the underlying hold has a
30-second safety TTL. On commit, the hold is bound to the order and lives until
the order is terminal or expires. Unbound holds are released on expiry. Once an
order is committed to the WAL, it is never silently rolled back; a client
timeout is resolved by querying the order. Reconciliation repairs orphan holds.

### Consequences

- No lost orders and no stuck funds.
- Clients must handle ambiguous timeouts.
- Reconciliation is required for crash windows.
- Hold TTL must exceed normal commit latency.

### Alternatives

- No TTL: stuck holds after crashes.
- Auto-rollback on timeout: can cancel an order the client believes exists.
- Synchronous authorize-and-commit: couples services and increases latency.

---

## ADR-023 — Reject floating point at compile time

**Status:** Accepted

### Context

Code review alone cannot prevent accidental `f64` use in a large codebase.

### Decision

Enable Clippy `float_arithmetic` and lossy-cast denials in financial crates.
CI also greps for `f32`/`f64`/`REAL`/`DOUBLE` in financial paths. Exceptions
require an ADR and a local `allow` with justification.

### Consequences

- Strong guarantee against a class of money bugs.
- Some library APIs require conversion wrappers.
- Reporting/display code may use decimals outside the core.

### Alternatives

- Convention only: insufficient.
- Runtime checks: too late.

---

## ADR-024 — No application code in B00

**Status:** Accepted

### Context

B00 is an architecture phase. Writing code now would lock in decisions before
review and blur the deliverable.

### Decision

B00 produces documentation only under `docs/architecture/`. No application
code, no additional project folder, no deletion or modification of existing
files.

### Consequences

- Decisions are reviewed before implementation.
- Implementation starts in B01 with a clean baseline.
- Documentation must be detailed enough to guide B01.

### Alternatives

- Prototype code in B00: rejected for scope control and review quality.

---

## Index

| ADR | Title | Status |
|-----|-------|--------|
| 001 | Rust cores, Go edge | Accepted |
| 002 | Modular services | Accepted |
| 003 | Integer money | Accepted |
| 004 | Single-writer actors + Raft | Accepted |
| 005 | WAL + snapshots + projection | Accepted |
| 006 | PostgreSQL system of record | Accepted |
| 007 | Redpanda event backbone | Accepted |
| 008 | Outbox + idempotent consumers | Accepted |
| 009 | Clearing-account legs | Accepted |
| 010 | Holds separate from journal | Accepted |
| 011 | Tick/lot integers | Accepted |
| 012 | Deterministic matching | Accepted |
| 013 | USDT-M linear perps | Accepted |
| 014 | Continuous tiered margin | Accepted |
| 015 | Liquidation waterfall | Accepted |
| 016 | REST/WS + decimal strings | Accepted |
| 017 | Tiered HSM/MPC custody | Accepted |
| 018 | Shard by symbol/account | Accepted |
| 019 | Single primary region + DR | Accepted |
| 020 | OTel + WORM audit | Accepted |
| 021 | API path versioning | Accepted |
| 022 | Holds expire, orders do not roll back | Accepted |
| 023 | Compile-time float rejection | Accepted |
| 024 | Documentation-only B00 | Accepted |
