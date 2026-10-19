# Architecture Decision Records

This directory records decisions that affect more than one service or that
constrain future implementation.

## Process

1. Copy `0000-template.md` to the next number.
2. State the context, decision, consequences, and alternatives.
3. Open a pull request. The ADR is accepted when the owning architecture and
   security reviewers approve.
4. Never rewrite an accepted ADR. Supersede it with a new ADR and link both.
5. Financial invariants, ledger schema changes, matching rules, and security
   boundary changes require an ADR.

## B00 decisions

The B00 architecture decisions are canonically recorded in
[`../architecture/20-architecture-decision-records.md`](../architecture/20-architecture-decision-records.md).
They remain accepted and are indexed here for discoverability.

| ADR | Title | Status |
|-----|-------|--------|
| 001 | Rust for financial cores, Go for edge and support | Accepted |
| 002 | Modular services, not a monolith | Accepted |
| 003 | Integer money, no floating point | Accepted |
| 004 | Single-writer actors with Raft replication | Accepted |
| 005 | WAL + snapshots + asynchronous Postgres projection | Accepted |
| 006 | PostgreSQL as the transactional system of record | Accepted |
| 007 | Redpanda as the event backbone | Accepted |
| 008 | Transactional outbox, idempotent consumers, no 2PC | Accepted |
| 009 | Clearing-account settlement legs | Accepted |
| 010 | Holds are separate from journal entries | Accepted |
| 011 | Tick/lot integer units with explicit scale | Accepted |
| 012 | Deterministic matching with no wall clock | Accepted |
| 013 | USDT-M linear perpetuals with base-unit quantity | Accepted |
| 014 | Tiered margin with continuous maintenance | Accepted |
| 015 | Liquidation waterfall: margin → insurance → ADL → socialized loss | Accepted |
| 016 | REST/JSON and WebSocket with decimal strings | Accepted |
| 017 | Tiered HSM/MPC custody with quorum signing | Accepted |
| 018 | Shard by symbol and account | Accepted |
| 019 | Single primary region, warm DR | Accepted |
| 020 | Observability with OpenTelemetry, Prometheus, and WORM audit | Accepted |
| 021 | Public API versioning in the path | Accepted |
| 022 | Holds expire; orders do not silently roll back | Accepted |
| 023 | Reject floating point at compile time | Accepted |
| 024 | No application code in B00 | Superseded by B01 authorization |

## New decisions

| ADR | Title | Status |
|-----|-------|--------|
| 025 | Separate raw market data, canonical pricing, and display configuration | Accepted |
| 026 | B01 monorepo and engineering baseline | Accepted |
