# 19 — Implementation Roadmap

---

## 1. How to use this roadmap

- Phases are ordered by dependency. Each phase ends with an **exit gate**; do
  not start the next phase until the gate passes.
- Steps are intentionally small (1–5 engineer-days each) so progress is
  measurable and reversible.
- Every phase includes tests and operational work, not only features.
- Indicative durations assume a team of 6–8 engineers (3 Rust, 2 Go, 1 SRE, 1
  QA/security) and are ranges, not commitments.
- “Done” means deployed to staging with tests, dashboards, alerts, and a
  runbook where applicable.

### 1.1 Critical path

```
B01 → B02 → B05 → B06 → B07 → B08 → B09 → B10 → B11 → B12 → B13 → B16
```

Parallelizable workstreams:

- B03/B04 (identity, reference data) can run alongside B05/B06.
- B14 (wallets) can start after B05.
- B15 (risk/compliance) can start after B09.
- Documentation and threat modeling run continuously.

### 1.2 Delivery milestones

| Milestone | Phases | Outcome |
|-----------|--------|---------|
| M1 Foundation | B01–B04 | Buildable repo, types, auth, reference data |
| M2 Ledger & durability | B05–B06 | Balanced ledger, WAL, Raft, replay |
| M3 Spot trading | B07–B10 | End-to-end spot order flow and public APIs |
| M4 Futures | B11–B13 | Perps, margin, funding, liquidation, ADL |
| M5 Money movement | B14 | Deposits, withdrawals, custody |
| M6 Production | B15–B16 | Risk/compliance, hardening, launch |

---

## 2. Phase B01 — Engineering baseline

**Goal:** A reproducible, secure, observable monorepo that every engineer can
build and run locally.

**Indicative duration:** 1–2 weeks.

### Steps

1. Create the repository layout: `contracts/`, `crates/`, `services/`,
   `tools/`, `infra/`, `docs/`, `scripts/`.
2. Pin toolchains: `rust-toolchain.toml`, `go.mod` + `toolchain`, Node version
   for docs tooling. Add `.editorconfig`, license, and ownership files.
3. Configure formatting and linting: `rustfmt`, `clippy` (deny floats in
   financial crates), `gofmt`, `golangci-lint`, `markdownlint`.
4. Build CI: format, lint, unit tests, build, SAST, dependency scan, secret
   scan, license check, SBOM.
5. Add `docker compose` for Postgres 17, Redpanda, Redis, ClickHouse, MinIO,
   and an OTel collector.
6. Create a service template with health endpoints, structured logging, OTel
   tracing, Prometheus metrics, graceful shutdown, and request IDs.
7. Add contract tooling: `buf` for protobuf, Spectral for OpenAPI, AsyncAPI
   validator; generate code in CI.
8. Establish ADR process and template; add the B00 ADRs.
9. Write the threat model v0 and data classification.
10. Define the definition-of-done checklist and PR template.

### Deliverables

- Monorepo with CI pipeline.
- `make dev`, `make test`, `make lint`, `make contracts`.
- Service template and one running “hello” service.
- ADR template and initial ADRs.
- Threat model v0.

### Exit gate

- [ ] Fresh clone builds and runs locally with one command.
- [ ] CI is green on a trivial PR and blocks on lint/test failures.
- [ ] A sample service emits logs, metrics, and traces to the local stack.
- [ ] Secrets and dependency scans are enforced.
- [ ] ADR and DoD templates are in use.

---

## 3. Phase B02 — Domain primitives

**Goal:** Exact, safe, well-tested money, identifiers, time, and errors that
every service shares.

**Indicative duration:** 2–3 weeks.

### Steps

1. Implement `AssetId`, `SymbolId`, `AccountId`, `OrderId`, `TradeId`,
   `JournalId`, `HoldId` with parsing, formatting, and serde support.
2. Implement `Price`, `Qty`, `Amount`, `Bps`, `Multiplier` as distinct types
   with checked arithmetic.
3. Implement decimal string parsing and formatting with scale validation and
   canonical output.
4. Implement rounding primitives (half-up, up, down) with explicit operation
   mapping.
5. Implement unit conversion (`price_ticks * qty_lots` → quote amount, lot/tick
   alignment) using exact rational arithmetic.
6. Implement time types: Unix nanoseconds internally, ISO-8601 at the edge, UTC
   only.
7. Implement the error taxonomy and stable error codes shared by all services.
8. Implement instrument, contract, margin tier, and fee schedule structs with
   validation.
9. Add property tests, fuzz tests for parsers, and overflow tests.
10. Add microbenchmarks for hot conversions; publish crate docs.

### Deliverables

- `nexora-types` crate with 100% branch coverage on financial math.
- Fuzz corpus for decimal and instrument parsing.
- Error code registry.
- Benchmarks and documentation.

### Exit gate

- [ ] CI denies float arithmetic and lossy casts in financial crates.
- [ ] 10 million randomized operations preserve ledger invariants.
- [ ] Parse → format → parse round-trips exactly.
- [ ] Overflow and scale violations return typed errors, never panic.
- [ ] Benchmarks meet the conversion budget (< 50 ns per conversion).

---

## 4. Phase B03 — Identity and authentication

**Goal:** Secure registration, login, sessions, API keys, 2FA, and RBAC.

**Indicative duration:** 3–4 weeks.

### Steps

1. Create the identity database schema and migrations (users, credentials,
   sessions, API keys, KYC cases).
2. Implement registration with email/phone verification and Argon2id password
   hashing.
3. Implement login, access tokens (Ed25519 JWT), refresh rotation, and reuse
   detection.
4. Implement TOTP enrollment/verification and WebAuthn registration/assertion.
5. Implement API key creation, scopes, IP allowlists, expiry, and constant-time
   verification.
6. Implement RBAC and permission checks; separate customer and admin roles.
7. Add rate limiting and lockout with exponential backoff.
8. Emit audit events for all auth and key lifecycle actions.
9. Add security tests: auth bypass, IDOR, replay, token reuse, scope escalation.
10. Add session revocation and key revocation propagation (< 60 s).

### Deliverables

- `identity-service` deployed to staging.
- Auth SDK/middleware for Go and Rust services.
- Security test suite.
- Runbook for account takeover and key compromise.

### Exit gate

- [ ] OWASP ASVS L2 controls for authentication are met.
- [ ] No API secret is retrievable through the public API after creation.
- [ ] Revocation takes effect within 60 seconds and is audited.
- [ ] Replay of a signed request outside the window fails.
- [ ] Pen-test-style auth tests pass.

---

## 5. Phase B04 — Reference data and configuration

**Goal:** Versioned, validated instruments, assets, networks, fees, and margin
tiers that cores can load safely.

**Indicative duration:** 2 weeks.

### Steps

1. Create asset and network tables with decimals, limits, and status.
2. Create instrument tables for spot and perp, including contract specs.
3. Create fee schedules and maker/taker tiers.
4. Create margin tier tables with continuity validation.
5. Implement a config service with immutable versions and effective timestamps.
6. Implement hot reload in cores with version checks and rollback on invalid
   config.
7. Implement admin APIs for config changes with dual approval and audit.
8. Seed development and staging data for BTC, ETH, and USDT pairs.
9. Add validation tests for tick/lot alignment, min/max, tier boundaries, and
   fee monotonicity.
10. Document the config change and rollback runbook.

### Deliverables

- Reference data service and schema.
- Config SDK with hot reload.
- Admin API and audit.
- Seed data.

### Exit gate

- [ ] Invalid config is rejected before it reaches a core.
- [ ] A config change is atomic across a shard and auditable.
- [ ] Rollback to the previous version takes < 1 minute.
- [ ] Tier boundaries are continuous; tests prove no liquidation cliffs.

---

## 6. Phase B05 — Ledger core

**Goal:** A correct double-entry ledger with holds, balances, transfers, and
reconciliation.

**Indicative duration:** 4–5 weeks.

### Steps

1. Create ledger schema: accounts, holds, journal batches, journal entries.
2. Implement the posting engine with per-asset zero-sum validation and hash
   chaining.
3. Implement balance projection updates and database constraints.
4. Implement hold creation, consumption, release, expiry, and available balance.
5. Implement spot ↔ futures transfers (single-shard).
6. Implement deposit and withdrawal journal adapters (no chain integration yet).
7. Implement admin adjustments with maker/checker and audit.
8. Implement the nightly reconciliation job and findings table.
9. Add property tests, model-based tests, and crash tests.
10. Benchmark hold and settlement throughput; tune indexes.

### Deliverables

- `account-core` ledger module.
- Journal hash chain and reconciliation job.
- Admin adjustment workflow.
- Ledger invariants test suite.

### Exit gate

- [ ] 10 million randomized journal operations balance exactly.
- [ ] `posted = sum(entries)`, `held = sum(active holds)`, `available >= 0`.
- [ ] Kill -9 at any point leaves no partial journal.
- [ ] Reconciliation detects injected mismatches within 1 minute.
- [ ] Settlement throughput meets 5k tx/s per shard on target hardware.

---

## 7. Phase B06 — Durability framework (WAL, snapshots, Raft)

**Goal:** A reusable deterministic state-machine framework with crash recovery
and replication.

**Indicative duration:** 4–5 weeks.

### Steps

1. Define the WAL record format with length, CRC32C, sequence, term, type, and
   versioned payload.
2. Implement segmented WAL writer with group commit and fsync.
3. Implement WAL reader, torn-tail detection, and truncation to the last valid
   record.
4. Implement snapshot creation, compression, encryption, and checksums.
5. Implement state hash computation and verification.
6. Integrate Raft (`openraft`) with the state machine, snapshots, and log
   storage.
7. Implement leader leases, fencing, step-down, and read barriers.
8. Implement shard lifecycle: bootstrap, join, catch-up, drain, restore.
9. Build CLI tools: inspect WAL, replay, verify hash, dump snapshot.
10. Add chaos tests: kill leader, partition, corrupt tail, disk full, clock
    skew.

### Deliverables

- `nexora-wal` crate and state-machine framework.
- Raft-integrated shard runtime.
- Recovery CLI.
- Chaos test suite.

### Exit gate

- [ ] Replay of 1 million commands reproduces the state hash exactly.
- [ ] Leader kill causes < 5 s pause and zero lost committed commands.
- [ ] Corrupt tail is detected and truncated without manual intervention.
- [ ] Minority partition cannot accept writes.
- [ ] Snapshot restore meets the 30 s RTO target.

---

## 8. Phase B07 — Matching engine

**Goal:** Deterministic, low-latency matching for spot and perp instruments.

**Indicative duration:** 5–6 weeks.

### Steps

1. Implement the order book: price levels, FIFO lists, slab index, best price.
2. Implement limit order matching with price-time priority.
3. Implement market, IOC, FOK, and post-only orders.
4. Implement self-trade prevention with configurable policies.
5. Implement cancel and cancel-all with O(1) removal.
6. Implement book snapshots, L2 deltas, and trade prints with sequence numbers.
7. Integrate the WAL/Raft framework and authorization hook (stubbed).
8. Implement state hash over book and order state.
9. Add property, model-based, fuzz, and replay tests.
10. Benchmark 30k orders/s and p99.9 latency; tune allocations and fsync.

### Deliverables

- `nexora-book` crate.
- `trading-core` matching service.
- Market data event stream.
- Benchmark report.

### Exit gate

- [ ] No crossed book under any test sequence.
- [ ] Quantity and price-time priority invariants hold.
- [ ] Replay reproduces state hash after 10 million operations.
- [ ] p99.9 match latency < 1 ms at target load.
- [ ] Fuzz tests find no panics or invariant violations in 24 hours.

---

## 9. Phase B08 — Order management

**Goal:** Complete order lifecycle, validation, idempotency, triggers, and
queries.

**Indicative duration:** 4–5 weeks.

### Steps

1. Implement the validation pipeline with stable reject codes.
2. Implement the idempotency store and conflict handling.
3. Integrate pre-trade authorization with `account-core` (holds).
4. Implement the order state machine and terminal-state guarantees.
5. Implement cancel, cancel-all, and cancel-replace amend.
6. Implement stop, take-profit, and trailing orders with a timer wheel.
7. Implement reduce-only, close-position, and one-way/hedge semantics.
8. Implement order queries and the Postgres projection.
9. Implement order/hold reconciliation and repair tooling.
10. Add integration and load tests, including duplicate and timeout scenarios.

### Deliverables

- OMS module in `trading-core`.
- Order APIs (internal gRPC).
- Projection and reconciliation job.
- Order lifecycle test suite.

### Exit gate

- [ ] Every order reaches exactly one terminal state.
- [ ] Duplicate submissions return the original result.
- [ ] Holds are released on cancel/reject/expiry within 2 seconds.
- [ ] Trigger orders fire once and in deterministic order.
- [ ] Reconciliation detects orphan orders and holds.

---

## 10. Phase B09 — Settlement and event backbone

**Goal:** End-to-end spot trading: match → settle → balances → projections.

**Indicative duration:** 4–5 weeks.

### Steps

1. Create outbox and inbox tables with relay and consumer patterns.
2. Define Redpanda topics, partitions, keys, and schemas.
3. Implement the settlement worker with per-account ordering.
4. Implement spot clearing legs, fees, and journal postings.
5. Implement trade/fill projections in Postgres.
6. Implement replay and rebuild tooling for projections.
7. Add duplicate, out-of-order, and poison-message handling.
8. Add settlement lag metrics and backpressure.
9. Run a full spot vertical slice: deposit → order → match → settle → withdraw
   (mock chain).
10. Inject 1% duplicates and kill consumers; verify exactly-once effects.

### Deliverables

- Settlement service.
- Event schemas and registry.
- Spot E2E flow.
- Replay tooling.

### Exit gate

- [ ] No duplicate settlement under 1% duplicate injection.
- [ ] Settlement p99 < 250 ms at target load.
- [ ] Clearing account nets to zero after each batch.
- [ ] Projection rebuild matches the live state.
- [ ] Ledger reconciliation passes after a 24-hour soak.

---

## 11. Phase B10 — Public REST and WebSocket

**Goal:** Production-grade edge for market data, orders, and accounts.

**Indicative duration:** 4–5 weeks.

### Steps

1. Implement the edge gateway skeleton with routing, timeouts, and circuit
   breakers.
2. Implement API key authentication, HMAC signatures, and timestamp windows.
3. Implement rate limits (IP, account, key) with headers and 429 responses.
4. Implement market data REST endpoints (tickers, book, trades, candles).
5. Implement order and account REST endpoints.
6. Implement public WebSocket channels with snapshot + delta and sequence
   checks.
7. Implement private WebSocket authentication (listen key and signed login).
8. Publish OpenAPI 3.1 and AsyncAPI 3 specifications.
9. Add request validation, body limits, and error envelopes.
10. Load test 100k connections and 500k messages/s; tune conflation.

### Deliverables

- `edge-gateway` service.
- Public REST and WebSocket APIs.
- OpenAPI/AsyncAPI specs.
- Load test report.

### Exit gate

- [ ] API contract tests pass against the spec.
- [ ] Signature, replay, and rate-limit tests pass.
- [ ] WebSocket gap detection and resync work.
- [ ] p99 order ack < 15 ms in the primary region.
- [ ] 100k connections with < 1% dropped messages.

---

## 12. Phase B11 — Futures positions and margin

**Goal:** USDT-M perpetual trading with one-way/hedge and cross/isolated margin.

**Indicative duration:** 5–6 weeks.

### Steps

1. Add perp instrument configs and contract specs.
2. Create position and margin account schemas.
3. Implement one-way position accounting: increase, reduce, flip.
4. Implement hedge mode with separate long/short positions.
5. Implement entry price, realized PnL, and fee accounting.
6. Implement cross and isolated margin models.
7. Implement leverage, margin mode, and position mode APIs.
8. Integrate perp authorization and settlement with the ledger.
9. Compute and cache liquidation/bankruptcy prices.
10. Add model-based tests against the reference formulas and worked examples.

### Deliverables

- Futures position engine.
- Margin models.
- Futures APIs.
- Margin test suite with published examples.

### Exit gate

- [ ] PnL and entry price match the reference model for 10 million fills.
- [ ] Cross/isolated and one-way/hedge combinations behave as specified.
- [ ] Leverage/margin changes are rejected when unsafe.
- [ ] Liquidation price is monotonic and conservative.
- [ ] Position reconciliation passes after replay.

---

## 13. Phase B12 — Oracle, mark price, and funding

**Goal:** Reliable index/mark prices and exactly-once funding.

**Indicative duration:** 3–4 weeks.

### Steps

1. Implement venue adapters with timeouts, retries, and staleness checks.
2. Implement index aggregation with median, outlier filtering, and quorum.
3. Implement mark price with impact prices, EMA, and clamping.
4. Implement staleness, divergence, and circuit-breaker logic.
5. Implement premium sampling and time-weighted funding rate calculation.
6. Implement funding rate publication and history.
7. Implement funding batch application with idempotency and residue handling.
8. Add funding APIs and WebSocket channels.
9. Add replay tests and scenario tests for extreme markets.
10. Add dashboards for basis, funding, and oracle health.

### Deliverables

- `oracle-service` and `funding-service`.
- Mark/index price streams.
- Funding application and history.
- Oracle/funding dashboards.

### Exit gate

- [ ] Index survives one venue outage and rejects stale/outlier data.
- [ ] Mark price cannot be moved by a single trade.
- [ ] Funding batch nets to zero except documented residue.
- [ ] Funding is applied exactly once per interval.
- [ ] Oracle failure freezes risk-increasing orders, not the whole exchange.

---

## 14. Phase B13 — Liquidation, insurance fund, and ADL

**Goal:** Deterministic, solvent liquidation with a governed insurance fund and
ADL.

**Indicative duration:** 5–6 weeks.

### Steps

1. Implement the risk engine evaluation loop over mark updates.
2. Implement margin monitoring and liquidation case creation.
3. Implement case state machine, freeze, and cancel-all.
4. Implement liquidation order planning, tranches, and backstop.
5. Implement deficit calculation and insurance fund ledger postings.
6. Implement insurance fund policy, limits, and reporting.
7. Implement ADL ranking, queue indicators, and execution.
8. Add notifications, audit, and case export.
9. Add scenario tests: gap risk, empty book, fund depletion, ADL.
10. Run a liquidation game day with injected volatility.

### Deliverables

- `risk-engine` liquidation module.
- Insurance fund accounting and policy.
- ADL engine.
- Liquidation runbook and dashboards.

### Exit gate

- [ ] Liquidation never leaves a negative customer balance.
- [ ] Deficit is fully accounted for by fund or ADL.
- [ ] A case resumes correctly after a crash.
- [ ] ADL ranking is deterministic and reproducible.
- [ ] Game day meets RTO and does not create duplicate fills.

---

## 15. Phase B14 — Wallets, deposits, withdrawals, and custody

**Goal:** Safe money movement on supported chains with custody controls.

**Indicative duration:** 5–6 weeks.

### Steps

1. Implement asset/network metadata and address derivation.
2. Implement the custody adapter interface with a mock and an HSM/MPC provider.
3. Implement chain watchers for at least BTC, ETH, and TRON (or the launch
   set).
4. Implement deposit detection, confirmations, crediting, and reorg handling.
5. Implement withdrawal policy, velocity limits, whitelists, and 2FA.
6. Implement hold → sign → broadcast → confirm → settle.
7. Implement signer policy checks, quorum, and transaction deduplication.
8. Implement custody reconciliation and proof-of-reserves generation.
9. Add security tests for replay, double-spend, and address substitution.
10. Run testnet E2E for deposits and withdrawals.

### Deliverables

- `wallet-service`.
- Chain adapters and watchers.
- Custody/HSM integration.
- Reconciliation and PoR tooling.

### Exit gate

- [ ] Deposit credited exactly once per tx output.
- [ ] Withdrawal cannot be broadcast twice.
- [ ] Reorg after credit is detected and handled.
- [ ] No single operator can sign a withdrawal.
- [ ] Daily custody reconciliation passes on testnet.

---

## 16. Phase B15 — Risk, compliance, and back office

**Goal:** Operational controls, surveillance, KYC/AML, and reconciliation
workflows.

**Indicative duration:** 4–5 weeks.

### Steps

1. Implement pre-trade limits and account/symbol restrictions.
2. Implement post-trade exposure and concentration reports.
3. Implement surveillance rules (spoofing, wash, layering, cancel ratios).
4. Integrate KYC/AML providers through adapters.
5. Implement sanctions screening and ongoing monitoring.
6. Implement back-office APIs: freeze, cancel-all, adjustments, cases.
7. Implement reconciliation dashboards and findings workflow.
8. Implement case management and evidence retention.
9. Conduct access reviews and separation-of-duties tests.
10. Implement regulatory export and audit packages.

### Deliverables

- Risk and compliance services.
- Back-office APIs and UI hooks.
- Surveillance alerts and cases.
- Reconciliation and regulatory reports.

### Exit gate

- [ ] All admin actions require the correct role and are audited.
- [ ] Sanctions screening blocks test cases.
- [ ] Surveillance detects synthetic abuse scenarios.
- [ ] Reconciliation findings have owners and SLAs.
- [ ] Regulatory export is complete and reproducible.

---

## 17. Phase B16 — Hardening, DR, and launch

**Goal:** Prove the system under load, failure, and attack; launch safely.

**Indicative duration:** 5–6 weeks.

### Steps

1. Tune performance: WAL, fsync, allocator, network, Postgres, Kafka.
2. Run sustained and burst load tests at 2x target.
3. Run a 7-day soak with duplicate/late event injection.
4. Run chaos tests: leader kills, AZ partition, DB failover, disk full.
5. Execute a full region-failover drill and measure RPO/RTO.
6. Complete external penetration test and remediate findings.
7. Prepare bug bounty and incident response.
8. Finalize runbooks, on-call rotation, and status page.
9. Canary 1% → 10% → 50% → 100% with automatic rollback.
10. Launch in reduce-only mode, monitor, then enable full trading.

### Deliverables

- Performance and chaos reports.
- DR drill report.
- Penetration test closure.
- Launch checklist and runbooks.
- Production canary and launch.

### Exit gate

- [ ] All SLOs met for 7 consecutive days in staging/perf.
- [ ] No Sev-1/Sev-2 in the final 14 days of soak.
- [ ] DR drill meets RPO < 5 min and RTO < 4 h.
- [ ] Critical/high security findings closed.
- [ ] Ledger and custody reconciliation clean for 30 days.
- [ ] Incident commander and on-call sign-off.

---

## 18. Cross-cutting workstreams

These run in every phase:

| Workstream | Cadence |
|------------|---------|
| Threat modeling | Per feature |
| ADRs | Per significant decision |
| Documentation | Per phase |
| Observability | Per service |
| Load/regression tests | Per release |
| Dependency updates | Weekly |
| Access reviews | Quarterly |
| DR drills | Quarterly |
| Pen tests | Pre-launch, annually |
| Post-incident reviews | Per incident |

---

## 19. Risk register

| Risk | Impact | Mitigation | Owner |
|------|--------|------------|-------|
| Ledger bug causes fund loss | Critical | Property tests, reconciliation, dual control, canary | Accounts |
| Matching nondeterminism | Critical | Pure functions, replay hash, fuzzing | Trading |
| Liquidation failure in gap | Critical | Backstop, insurance fund, ADL, stress tests | Risk |
| Custody key compromise | Critical | HSM/MPC, quorum, segregation, drills | Security |
| Oracle manipulation | High | Multi-venue median, impact prices, circuit breakers | Risk |
| Settlement lag | High | Backpressure, batching, sharding, alerts | Platform |
| Scope creep | High | Phase gates, non-goals, ADRs | Product |
| Key-person dependency | Medium | Documentation, pairing, on-call rotation | Engineering |
| Vendor outage | Medium | Multi-provider, failover, contractual SLAs | Platform |
| Regulatory change | Medium | Configurable controls, legal review | Compliance |

---

## 20. What “done” means for B00

B00 is complete when:

- [x] All architecture documents exist in `docs/architecture/`.
- [x] Scope, non-goals, and invariants are defined.
- [x] Stack, modules, interfaces, and database architecture are specified.
- [x] Precision, atomicity, idempotency, and concurrency rules are explicit.
- [x] Security, custody, and recovery principles are documented.
- [x] A small-step roadmap with exit gates exists.
- [x] No application code, extra project folder, or file deletion occurred.
- [x] All documentation is in English.
