# NEXORA Exchange — Backend Architecture

**Document set:** B00 Architecture
**Status:** Approved baseline for implementation
**Version:** 1.0
**Last updated:** 2026-10-19
**Owner:** Platform Architecture
**Audience:** Backend, infrastructure, security, compliance, and product engineering

---

## 1. Purpose

This directory is the single source of truth for the architecture of the NEXORA
centralized cryptocurrency exchange backend. It covers:

- Spot trading and USDT-margined perpetual futures (USDT-M).
- Matching engine and order management.
- Double-entry ledger and wallets.
- One-way and hedge position modes.
- Cross and isolated margin.
- Funding, liquidation, insurance fund, and auto-deleveraging (ADL).
- REST API, WebSocket streaming, authentication, and security.
- High concurrency, horizontal scalability, and crash recovery.

B00 is **documentation only**. No application code, infrastructure code, or
additional project folders are created in this phase.

---

## 2. Reading order

| # | Document | What it answers |
|---|----------|-----------------|
| 00 | [Scope, Principles and Glossary](00-scope-principles-and-glossary.md) | Goals, non-goals, invariants, terminology |
| 01 | [System Architecture](01-system-architecture.md) | Context, services, data flow, deployment topology |
| 02 | [Technology Stack](02-technology-stack.md) | Languages, storage, messaging, platform choices and rationale |
| 03 | [Domain Model](03-domain-model.md) | Bounded contexts, aggregates, entities, state machines |
| 04 | [Modules and Interfaces](04-modules-and-interfaces.md) | Service boundaries, commands, events, internal contracts |
| 05 | [Database Architecture](05-database-architecture.md) | Schemas, tables, keys, indexes, partitioning, retention |
| 06 | [Matching Engine](06-matching-engine.md) | Order book, determinism, sequencing, market data |
| 07 | [Order Management](07-order-management.md) | Order lifecycle, validation, cancel/replace, triggers |
| 08 | [Ledger, Wallets and Settlement](08-ledger-wallets-settlement.md) | Double-entry model, holds, deposits, withdrawals, reconciliation |
| 09 | [Futures and Margin Engine](09-futures-margin-engine.md) | Positions, PnL, leverage, cross/isolated, hedge/one-way |
| 10 | [Funding, Liquidation, Insurance Fund and ADL](10-funding-liquidation-insurance-adl.md) | Oracle, mark price, funding, liquidation waterfall, ADL |
| 11 | [Risk and Compliance Controls](11-risk-and-compliance.md) | Pre/post-trade risk, surveillance, limits, circuit breakers |
| 12 | [REST API and WebSocket](12-api-rest-and-websocket.md) | External contracts, errors, auth headers, streaming protocol |
| 13 | [Authentication and Security](13-authentication-and-security.md) | Identity, sessions, API keys, application security, threat model |
| 14 | [Custody and Key Management](14-custody-and-key-management.md) | Hot/warm/cold custody, HSMs, MPC, withdrawal governance |
| 15 | [Precision, Atomicity and Idempotency](15-precision-atomicity-idempotency.md) | Money representation, rounding, exactly-once effects |
| 16 | [Concurrency, Consistency and Scalability](16-concurrency-consistency-and-scalability.md) | Serialization, locking, sharding, backpressure, capacity |
| 17 | [Crash Recovery and Disaster Recovery](17-crash-recovery-and-disaster-recovery.md) | WAL, snapshots, replay, failover, regional DR |
| 18 | [Observability, Testing and SLOs](18-observability-testing-and-slo.md) | Metrics, tracing, test strategy, performance gates |
| 19 | [Implementation Roadmap](19-implementation-roadmap.md) | Small-step delivery plan B01–B16 with exit gates |
| 20 | [Architecture Decision Records](20-architecture-decision-records.md) | Accepted decisions and trade-offs |

---

## 3. Architectural summary

NEXORA is a **modular, event-driven exchange** built around three deterministic
state machines:

1. **Trading Core** — one single-writer actor per symbol, owning the order book,
   order state, and trade generation.
2. **Account Core** — sharded by account, owning balances, holds, positions, and
   the double-entry journal.
3. **Risk Engine** — computes margin, funding, liquidation, and ADL decisions
   from deterministic inputs and issues commands to Account Core and Trading Core.

The edge is stateless and horizontally scalable. Durable event logs, write-ahead
logs (WAL), and an outbox connect the state machines without distributed
transactions. Money is represented as signed integers in atomic units; floating
point is prohibited on all financial paths.

```
Client -> CDN/WAF -> edge-gateway -> trading-core (per symbol) -> account-core (per account shard)
                          |                    |                          |
                          +------ market-data -+------ event bus --------+
                                               |
                                       risk / funding / liquidation
```

---

## 4. Document conventions

- **MUST**, **MUST NOT**, **SHOULD**, and **MAY** follow RFC 2119 semantics.
- All monetary examples use USDT with 6 decimals unless stated otherwise.
- `i64` price values are integer **ticks**; `i64` quantity values are integer
  **lots**; `i128` amounts are integer **atomic units** of an asset.
- Sequence diagrams use Mermaid and render on GitHub/GitLab and most Markdown
  viewers.
- SQL and interface snippets are normative sketches, not final migrations or
  generated code.
- Timestamps are UTC, ISO-8601 at the edge, and Unix nanoseconds internally.

---

## 5. Scope boundaries

In scope for the first production release:

- Spot markets with limit/market/IOC/FOK/post-only orders.
- USDT-M perpetual futures with up to 125x leverage, tiered margin.
- One-way and hedge position modes; cross and isolated margin.
- Funding every 8 hours, mark/index price, liquidation, insurance fund, ADL.
- REST and WebSocket APIs, API keys, 2FA, withdrawal controls.
- Double-entry ledger, deposit/withdrawal lifecycle, reconciliation.
- Single-region active/active edge, per-shard Raft replication, cross-region DR.

Explicitly out of scope for the first release (see
[00-scope-principles-and-glossary.md](00-scope-principles-and-glossary.md)):

- Coin-margined futures, options, spot margin borrowing, and multi-asset collateral.
- Fiat payment rails and banking integrations.
- Institutional prime brokerage, sub-account hierarchies beyond two levels.
- Market maker rebate programs beyond a single fee tier table.

---

## 6. Change control

1. Any change to a financial invariant, ledger schema, or matching rule requires
   an ADR in [20-architecture-decision-records.md](20-architecture-decision-records.md).
2. Schema changes require an expand/contract migration plan and a rollback path.
3. Interface changes require versioning and a deprecation window of at least two
   release cycles for public APIs.
4. This document set is versioned with the repository. The version in this file
   is the authoritative baseline for B00.

---

## 7. Phase status

| Phase | Description | Status |
|-------|-------------|--------|
| B00 | Architecture documentation | **Complete** |
| B01 | Repository bootstrap and engineering baseline | Planned |
| B02 | Money, time, and domain primitives | Planned |
| B03 | Identity and authentication | Planned |
| B04 | Reference data and configuration | Planned |
| B05 | Ledger core and double-entry invariants | Planned |
| B06 | WAL, snapshots, Raft, and recovery | Planned |
| B07 | Matching engine | Planned |
| B08 | Order management | Planned |
| B09 | Settlement and event backbone | Planned |
| B10 | Public REST and WebSocket | Planned |
| B11 | USDT-M futures, positions, margin | Planned |
| B12 | Oracle, mark price, funding | Planned |
| B13 | Liquidation, insurance fund, ADL | Planned |
| B14 | Wallets, deposits, withdrawals, custody | Planned |
| B15 | Risk, surveillance, compliance, back office | Planned |
| B16 | Hardening, DR, and launch | Planned |

The detailed breakdown, including acceptance criteria for each step, is in
[19-implementation-roadmap.md](19-implementation-roadmap.md).
