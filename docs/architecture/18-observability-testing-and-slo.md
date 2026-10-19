# 18 — Observability, Testing and SLOs

---

## 1. Service level objectives

| SLO | Target | Measurement | Window |
|-----|--------|-------------|--------|
| Trading API availability | 99.99% | Successful non-5xx responses / total | 30 days |
| Order acknowledgement p99 | < 15 ms | Edge request to commit response | 30 days |
| Matching latency p99.9 | < 1 ms | Ingress to match complete | 30 days |
| Market data p99 | < 50 ms | Match to client send | 30 days |
| Settlement lag p99 | < 250 ms | Trade commit to ledger commit | 30 days |
| Withdrawal processing p95 | < 30 min | Request to broadcast | 30 days |
| WebSocket availability | 99.95% | Successful connections / attempts | 30 days |
| Ledger reconciliation | 100% | Balanced batches / total | Continuous |
| Duplicate settlement | 0 | Count | Continuous |
| Data durability | RPO 0 | Committed data present after failover | Continuous |

### 1.1 Error budgets

- 99.99% monthly availability = 4.32 minutes of downtime.
- 99.95% WebSocket = 21.6 minutes.
- Budget burn alerts:
  - 2% budget in 1 hour → page.
  - 10% budget in 6 hours → ticket.
  - 50% budget in 3 days → review and freeze risky deploys.

Error budget exhaustion pauses feature releases until reliability is restored.

---

## 2. Observability pillars

### 2.1 Metrics

Naming: `<service>_<subsystem>_<metric>_<unit>`.

Examples:

```
trading_orders_submitted_total
trading_orders_rejected_total{reason}
trading_match_duration_seconds_bucket
trading_book_depth_levels{symbol}
trading_wal_fsync_seconds_bucket
trading_raft_commit_lag_seconds
account_settlements_total{result}
account_settlement_duration_seconds_bucket
account_holds_active{asset}
account_journal_balance_mismatch_total
ledger_clearing_imbalance_atomic{asset}
risk_liquidations_total{symbol}
risk_margin_ratio_bps{account_tier}
risk_insurance_fund_balance_atomic
funding_rate_bps{symbol}
oracle_staleness_seconds{venue}
ws_connections_active
ws_messages_sent_total{channel}
ws_send_buffer_bytes
api_requests_total{route,status}
api_request_duration_seconds_bucket{route}
```

Rules:

- Labels are bounded; never use account IDs or order IDs as labels.
- Histograms use buckets tuned to SLO thresholds.
- Counters are monotonic; use `_total`.
- Units are in the metric name.
- Every service exposes `/metrics` on a private port.

### 2.2 Tracing

- OpenTelemetry SDK in every service.
- W3C `traceparent` propagated across HTTP, gRPC, and Kafka headers.
- Spans for:
  - Edge request.
  - Authorization RPC.
  - WAL append and fsync.
  - Raft commit.
  - Matching.
  - Settlement.
  - Journal commit.
  - Event publish/consume.
- Tail sampling: 100% of errors, 100% of slow requests (> p99), 1% of normal.
- Trace IDs are returned to clients in `X-Request-ID` / error payloads.

### 2.3 Logs

- Structured JSON, one line per event.
- Required fields: `ts`, `level`, `service`, `env`, `trace_id`, `span_id`,
  `shard`, `msg`.
- Contextual fields: `order_id`, `trade_id`, `account_id_hash`, `symbol`,
  `journal_id`, `case_id`.
- No PII, secrets, tokens, full addresses, or raw request bodies.
- Levels: `ERROR` for actionable failures, `WARN` for degraded, `INFO` for
  state changes, `DEBUG` off in production.
- Log volume is sampled for high-frequency paths; financial state changes are
  always logged at `INFO`.

### 2.4 Audit

- Separate append-only audit stream.
- Every admin action, withdrawal, manual adjustment, parameter change, and
  permission change.
- WORM storage and SIEM ingestion.
- Retained 7 years.

---

## 3. Dashboards

| Dashboard | Audience | Contents |
|-----------|----------|----------|
| Executive | Leadership | Availability, volume, incidents, fund balance |
| Trading | Trading ops | Orders/s, rejects, latency, halts, book depth |
| Matching | Core engineers | Queue depth, fsync, Raft, hash, memory |
| Settlement | Accounts | Lag, duplicates, clearing imbalance, holds |
| Risk | Risk team | Margin buckets, liquidations, ADL, insurance fund |
| Market data | Edge | Connections, fan-out, conflation, gaps |
| Wallet | Treasury | Deposits, withdrawals, stuck tx, chain health |
| Infrastructure | SRE | CPU, memory, disk, network, DB, Kafka |
| Security | CISO | Auth failures, key usage, anomalies, audit |

Dashboards are versioned as code and reviewed in PRs.

---

## 4. Alerting

| Severity | Condition examples | Routing |
|----------|--------------------|---------|
| P1 | Ledger mismatch, duplicate settlement, trading halted, fund loss | Page + incident bridge |
| P2 | Settlement lag > 5 s, oracle stale, insurance fund below floor, WAL fsync p99 > 50 ms | Page |
| P3 | Elevated rejects, queue depth > 80%, single broker down | Ticket + Slack |
| P4 | Disk > 70%, certificate expiry < 30 days | Ticket |

Alert quality:

- Every alert has a runbook link.
- No alert without an owner.
- Symptom-based alerts are preferred over cause-based.
- Suppression during planned maintenance.
- Weekly review of noisy alerts; tune or delete.

---

## 5. Testing strategy

### 5.1 Test pyramid

```
          /\
         /  \        E2E / acceptance (few, critical paths)
        /----\
       /      \      Integration (services + real Postgres/Redpanda)
      /--------\
     /          \    Contract (gRPC/OpenAPI/AsyncAPI compatibility)
    /------------\
   /              \  Property / model-based / fuzz (core logic)
  /----------------\
 /                  \ Unit (pure functions, edge cases)
/--------------------\
```

### 5.2 Unit tests

- Every pure function: money, rounding, margin, PnL, validation.
- Table-driven cases for boundary values.
- No mocks for arithmetic.
- Coverage target: 90% line, 100% branch on financial modules.

### 5.3 Property-based tests

- Journal balance.
- Book invariants (no cross, quantity conservation).
- Position conservation across fills.
- Margin monotonicity.
- Idempotency.
- Serialization round-trips.
- Decimal parsing.

### 5.4 Model-based tests

A reference implementation (simple, slow, obviously correct) is compared to the
production implementation on randomized command sequences:

- Order book: compare fills, order states, and depth.
- Ledger: compare balances and journal.
- Margin: compare equity, IM, MM, liquidation price.
- Funding: compare payments and residue.

Divergence is a test failure with a minimal reproducible sequence.

### 5.5 Fuzz tests

- WAL reader with truncated, corrupted, and adversarial records.
- Decimal parser.
- Order validator.
- JSON/Protobuf decoders.
- Matching engine with extreme prices/quantities.
- Margin formulas with overflow candidates.

### 5.6 Integration tests

- Real Postgres, Redpanda, Redis via Testcontainers.
- Place → match → settle → reconcile.
- Duplicate and out-of-order event injection.
- Consumer restart mid-batch.
- Database failover.
- Leader election.
- Withdrawal lifecycle with a mock chain adapter.

### 5.7 Contract tests

- Protobuf backward compatibility (buf breaking).
- OpenAPI schema validation (spectral).
- AsyncAPI validation.
- Consumer-driven contracts between services.
- Public API response snapshots.

### 5.8 End-to-end tests

- Register → KYC (mock) → deposit (mock chain) → trade → withdraw.
- Spot buy/sell.
- Perp open/close, funding, liquidation, ADL.
- Cross and isolated margin.
- One-way and hedge mode.
- API key trading.
- WebSocket order/fill/position streams.

### 5.9 Load tests

- Sustained 30k orders/s per shard for 1 hour.
- Burst 2x for 5 minutes.
- 1M WebSocket connections with 5M updates/s.
- 24-hour soak with memory and disk growth checks.
- Tail latency under GC/compaction/fsync pressure.
- Failure injection during load (leader kill, DB failover).

### 5.10 Chaos tests

- Kill processes at every step of the order flow.
- Partition networks.
- Fill disks.
- Corrupt WAL tails.
- Delay or drop events.
- Freeze the oracle.
- Fail the custody provider.
- Clock skew.

### 5.11 Security tests

- SAST, DAST, dependency scanning.
- Auth bypass and IDOR tests.
- Rate-limit and replay tests.
- Fuzzing of public endpoints.
- Penetration tests before launch.
- Secret scanning.

### 5.12 Recovery tests

- Restore from backup and verify.
- Replay WAL and compare state hash.
- Region failover drill.
- Custody key recovery drill.

---

## 6. Test data and environments

| Environment | Data | Purpose |
|-------------|------|---------|
| Unit | In-memory | Fast feedback |
| Integration | Ephemeral containers | Service contracts |
| Staging | Synthetic + replay corpus | E2E and release validation |
| Perf | Synthetic at scale | Capacity and latency |
| Chaos | Synthetic + fault injection | Resilience |
| Prod | Real (read-only tests) | Canary and smoke |

Replay corpus:

- Anonymized production command/event logs (or synthetic equivalents).
- Includes peak bursts, halts, liquidations, and funding.
- Used for regression and performance tests.
- Never contains PII or secrets.

---

## 7. CI/CD quality gates

Every pull request:

- Formatting and lint (Rustfmt/Clippy, gofmt/golangci-lint).
- Unit, property, and contract tests.
- SAST and dependency scan.
- Build and image signing.
- Migration lint (lock time, backward compatibility).
- No floats in financial crates.

Every merge to main:

- Integration tests.
- Replay determinism test.
- Performance smoke (no > 10% regression).
- SBOM generation.

Every release:

- Full E2E.
- Load test on perf environment.
- Security scan.
- Canary to 1% of traffic.
- Rollback plan verified.

---

## 8. Release process

1. **Freeze** the release branch; tag.
2. **Build** reproducible artifacts; sign images.
3. **Deploy** to staging; run migrations in expand mode.
4. **Validate** E2E, replay, and performance.
5. **Canary** 1% of edge traffic and one non-critical shard.
6. **Observe** SLOs, errors, and reconciliation for 30 minutes.
7. **Roll out** progressively (10%, 50%, 100%) with automatic rollback on SLO
   burn.
8. **Verify** reconciliation and ledger balance.
9. **Announce** changes and deprecations.

Database migrations:

- Expand first, deploy code, backfill, contract later.
- Never destructive in the same release.
- Rollback is a new migration or a feature flag, not a schema revert.

---

## 9. Incident management

- Severity matrix in [11-risk-and-compliance.md](11-risk-and-compliance.md).
- On-call rotation with primary/secondary.
- Incident commander for P1/P2.
- Status page updates within 15 minutes of customer impact.
- Post-incident review within 5 business days.
- Action items tracked with owners and due dates.
- Blameless culture; focus on systemic fixes.

---

## 10. Definition of done for a service

A service is production-ready only when:

- [ ] SLOs defined and dashboards exist.
- [ ] Alerts have runbooks.
- [ ] Traces cover the critical path.
- [ ] Logs are structured and redacted.
- [ ] Unit, property, integration, and contract tests pass.
- [ ] Load test meets targets.
- [ ] Chaos test passes.
- [ ] Backup/restore verified.
- [ ] Security review completed.
- [ ] Runbook written and rehearsed.
- [ ] Rollback tested.
- [ ] Ownership and escalation documented.
