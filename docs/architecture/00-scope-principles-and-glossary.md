# 00 — Scope, Principles and Glossary

---

## 1. Business context

NEXORA is a centralized exchange (CEX) offering two product lines:

1. **Spot** — immediate delivery of base asset against quote asset, with a
   central limit order book (CLOB).
2. **USDT-M Perpetual Futures** — linear contracts settled in USDT, with no
   expiry, periodic funding, leveraged positions, and forced liquidation.

The backend must behave correctly under:

- Bursts of market volatility and order flow.
- Partial failures of processes, hosts, racks, and availability zones.
- Operator error during deploys and incident response.
- Adversarial clients attempting to exploit races, replay, or price manipulation.

Correctness of money and positions takes precedence over availability and
latency. When the two conflict, the system halts trading rather than risk an
inconsistent ledger.

---

## 2. Goals

| ID | Goal | Measure |
|----|------|---------|
| G1 | Correct money movement | Zero unexplained ledger imbalance; daily reconciliation |
| G2 | Deterministic matching | Replay of the same event log reproduces byte-identical state hash |
| G3 | Exactly-once financial effects | No duplicate fills, settlements, funding payments, or withdrawals |
| G4 | Low-latency trading | Order ack p99 < 15 ms; matching p99.9 < 1 ms (design target) |
| G5 | Horizontal scale | Add symbol/account shards without downtime |
| G6 | Crash recovery | RPO 0 for acknowledged commands; matching RTO < 30 s |
| G7 | Auditability | Every balance change traceable to a journal batch and source event |
| G8 | Security | No single person can move customer funds; keys never leave HSMs |
| G9 | Operability | Every service observable, deployable independently, and reversible |
| G10 | Regulatory readiness | KYC/AML hooks, audit export, data retention, access controls |

### 2.1 Design targets (to be validated in B15)

These are engineering targets, not contractual SLAs:

- 30,000 orders/s per symbol shard; 300,000 orders/s platform-wide.
- 1,000,000 concurrent WebSocket connections.
- 5,000,000 order-book updates/s aggregate egress.
- 99.99% monthly availability for the trading API.
- p99 order acknowledgement < 15 ms within the primary region.
- p99.9 matching latency < 1 ms, excluding network and durable commit.
- Zero data loss for commands acknowledged after quorum commit.

---

## 3. Non-goals (first release)

- Coin-margined (inverse) futures and options.
- Spot margin lending and short selling.
- Portfolio margin and multi-asset collateral.
- Fiat deposits/withdrawals and banking rails.
- Decentralized custody, on-chain settlement, or DEX aggregation.
- Copy trading, trading bots, or strategy marketplace.
- Sub-account trees deeper than two levels.
- Cross-collateralization between spot and futures wallets.
- Negative-balance socialized loss beyond the insurance fund and ADL waterfall.

Non-goals are excluded from interfaces but must not be architecturally
precluded. Extension points are noted in the relevant documents.

---

## 4. Architectural principles

### P1 — Money is exact

All monetary values are integer atomic units or exact decimals with an explicit
scale. `f32`/`f64` are forbidden in financial crates and SQL columns. JSON
amounts are decimal strings, never JSON numbers.

### P2 — Single writer per aggregate

Each order book is owned by exactly one actor at a time. Each account is owned
by exactly one shard leader. This removes lock contention and makes state
transitions linearizable.

### P3 — Deterministic state machines

Trading and account cores consume ordered command logs. Time, randomness, and
external data enter as explicit events with assigned sequence numbers. Replay
must reproduce the same state hash.

### P4 — The journal is the source of truth

Balances are a projection of the double-entry journal. If a balance and the
journal disagree, the journal wins and the projection is rebuilt.

### P5 — No distributed transactions on the hot path

Cross-service consistency uses ordered events, idempotent consumers, the
transactional outbox, and compensating actions. Two-phase commit is prohibited.

### P6 — Idempotency everywhere

Every externally initiated command carries a client idempotency key. Every
internally generated event has a deterministic identity. Every consumer stores
its inbox position.

### P7 — Fail closed on financial ambiguity

Unknown state, failed reconciliation, stale oracle, or lost quorum results in
rejection, trading halt, or withdrawal pause — never a best-effort guess.

### P8 — Reserve before match, settle after match

Funds and margin are authorized before an order enters the book. Settlement
consumes the reservation. This prevents overspending during asynchronous
settlement.

### P9 — Boring, observable infrastructure

Prefer proven components (PostgreSQL, Kafka/Redpanda, object storage) over novel
storage. Every hop carries a trace ID and every queue has a depth metric.

### P10 — Reversibility

Schema changes use expand/contract. Deploys are canaried. Feature flags gate
new trading behavior. Every runbook has a rollback or halt procedure.

---

## 5. Financial invariants

These invariants are enforced by tests, database constraints, and continuous
reconciliation. Violation is a Sev-1 incident.

| ID | Invariant |
|----|-----------|
| I1 | For every journal batch and every asset, the sum of all entries is exactly zero. |
| I2 | For every account and asset, `posted_balance = sum(journal entries)`. |
| I3 | `available = posted - held`, and `available >= 0` for customer accounts. |
| I4 | `held = sum(active hold remaining amounts)` for each account and asset. |
| I5 | A trade is settled at most once per side; `(trade_id, side)` is unique. |
| I6 | Sum of all customer and system balances per asset across all shards is zero. |
| I7 | Positions are derivable from the ordered fill stream; no orphan position changes. |
| I8 | Funding for a symbol and interval is applied exactly once and nets to zero across longs and shorts, excluding rounding residue booked to the insurance fund. |
| I9 | A liquidation never creates a negative customer balance; any deficit is absorbed by the insurance fund or ADL. |
| I10 | Every acknowledged order has a unique `(account_id, client_order_id)` and exactly one terminal state. |
| I11 | Order book state is a pure function of the ordered command log. |
| I12 | A withdrawal cannot exceed available balance and cannot be broadcast twice. |

---

## 6. Consistency model

| Data | Consistency | Mechanism |
|------|-------------|-----------|
| Order book | Linearizable per symbol | Single-writer actor + Raft log |
| Balances and holds | Linearizable per account | Single-writer shard + row locks |
| Positions | Linearizable per account | Account shard transaction |
| Cross-account settlement | Atomic per side, eventually consistent globally | Clearing-account legs + idempotent retries |
| Market data | Ordered per symbol, snapshot + delta | Sequenced fan-out |
| Public REST reads | Read-your-writes for the placing client via token/sequence | Read barrier |
| Analytics | Eventually consistent | ClickHouse ingestion |

The clearing-account pattern is central: a trade produces two independent,
balanced journal batches, one per participant, each against a system clearing
account. There is no cross-shard lock and no distributed transaction. The
clearing account may carry a transient balance; it is monitored and reconciled
continuously.

---

## 7. Failure philosophy

| Failure | Behavior |
|---------|----------|
| Matching leader lost | Raft elects a new leader; uncommitted commands are discarded; clients retry idempotently |
| Account shard leader lost | Same as above; holds and balances replay from WAL + snapshot |
| Event bus unavailable | Outbox accumulates; trading continues if pre-trade authorization is local; settlement lags and is backpressured |
| Oracle stale or divergent | Mark price freezes; new risk-increasing orders rejected; liquidation uses last good price with widened bands |
| Database primary lost | Patroni promotes replica; services reconnect with bounded retry and jitter |
| Region lost | Cross-region restore from WAL archive + snapshots; RPO target 5 minutes |
| Reconciliation mismatch | Trading and withdrawals for affected accounts paused; incident declared; manual review |
| Disk full / WAL write failure | Process fences itself (steps down); no further acknowledgements |

---

## 8. Glossary

| Term | Definition |
|------|------------|
| **ADL** | Auto-deleveraging. Forced reduction of profitable opposing positions when the insurance fund cannot absorb a liquidation deficit. |
| **Aggregate** | Consistency boundary in the domain model; mutated by one command at a time. |
| **Atomic unit** | Smallest indivisible unit of an asset, e.g. 1 micro-USDT = 0.000001 USDT. |
| **Available balance** | Posted balance minus active holds. |
| **Bankruptcy price** | Price at which a position's equity is zero. |
| **Basis** | Mark price minus index price, or futures minus spot. |
| **Client order ID** | Caller-supplied unique identifier, scoped to an account, used for idempotency. |
| **Clearing account** | System ledger account that is the counterparty to each settlement leg. |
| **Cross margin** | Shared collateral across all positions of a futures wallet. |
| **Fill** | The execution of one order against one or more counterparties. |
| **Funding** | Periodic transfer between long and short perpetual positions. |
| **Hedge mode** | Long and short positions may coexist on the same symbol. |
| **Hold / reservation** | A lien on available balance for a pending order or withdrawal. |
| **Index price** | Externally derived spot reference price for an asset. |
| **Insurance fund** | Segregated pool that absorbs liquidation deficits. |
| **Isolated margin** | Collateral assigned to a single position, capped at that position's loss. |
| **Journal batch** | A balanced set of double-entry lines committed atomically. |
| **Lot** | Smallest tradable quantity increment for an instrument. |
| **Mark price** | Fair value used for PnL, margin, funding, and liquidation. |
| **MMR** | Maintenance margin rate. |
| **One-way mode** | A single net position per symbol; orders reduce or flip it. |
| **Post-only** | Order that must rest as a maker or is rejected. |
| **Premium index** | Perpetual price premium over index, used in funding. |
| **Raft** | Consensus protocol used for replicated state machines. |
| **Reduce-only** | Order allowed only to decrease an existing position. |
| **Settlement** | Applying a trade, funding payment, or liquidation to balances and positions. |
| **Slippage** | Difference between expected and executed price. |
| **Taker / maker** | Liquidity remover / provider. |
| **Tick** | Smallest price increment for an instrument. |
| **WAL** | Write-ahead log; durable ordered record of commands and events. |
| **WORM** | Write once, read many; immutable audit storage. |

---

## 9. Assumptions and constraints

1. The exchange operates in a single primary region with three availability
   zones; a secondary region is warm-standby for disaster recovery.
2. Cloud object storage provides eleven-nines durability and versioning.
3. The custody provider or HSM supports secp256k1/Ed25519 signing through a
   PKCS#11 or proprietary API with audit logs.
4. Regulators may require data residency; deployment is region-parameterized.
5. The first release supports a maximum of 500 instruments and 100 million
   accounts, with sharding designed for 10x growth.
6. USDT is the only futures collateral asset at launch. The ledger is
   multi-asset from day one.
7. Chain reorg depth is bounded per network and configured conservatively.
8. Market makers connect via the same public APIs; no privileged matching path.
9. Operators are trusted but audited; four-eyes controls apply to sensitive
   actions.
10. All code and documentation are in English.

---

## 10. Compliance posture

The architecture provides hooks, not legal conclusions:

- KYC states gate deposit, trading, and withdrawal permissions.
- Sanctions and PEP screening run before account activation and on an ongoing
  basis.
- Transaction monitoring consumes the event stream and produces cases.
- Travel-rule data is attached to transfers where required.
- Audit logs are immutable, access-controlled, and retained per jurisdiction.
- Data subject requests are served by the identity service with a documented
  erasure procedure that preserves legally required financial records.

See [11-risk-and-compliance.md](11-risk-and-compliance.md) for controls.
