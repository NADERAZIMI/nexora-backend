# ADR 0027: Versioned Reference-Data Snapshots and Atomic Activation

- **Status:** Proposed (B04 implementation complete; independent review and B05
  approval outstanding)
- **Date:** 2026-10-22
- **Owners:** Platform Engineering, Risk, Security
- **Related:** ADR 003, 006, 011, 013, 014, 020, 025; B04 roadmap

## Context

Assets, networks, instruments, fee schedules, margin tiers, and presentation
policies are read by trading, risk, and public services. If each consumer reads
mutable tables independently, a process can observe an instrument with a new
tick size but an old fee schedule, or a network mapping that no longer matches
its asset. Reference data also needs maker/checker approval, immutable history,
explicit rollback, and deterministic behaviour across Go and Rust consumers.

## Decision

1. A complete reference-data configuration is one canonical JSON document. The
   document contains only reference configuration: assets, networks,
   asset-network mappings, instruments, fee schedules, margin tier sets, and
   presentation policies. It never contains balances, positions, orders, trades,
   private keys, or live custody addresses.
2. PostgreSQL is authoritative. Draft state, immutable proposals, approvals,
   published versions, activations, and a hash-chained audit live in the
   `reference` schema. The canonical document is stored as `bytea`, so the
   database never re-encodes financial values.
3. Published versions are immutable. The SHA-256 `content_hash` covers the exact
   canonical bytes served. Consumers verify the hash and schema before applying a
   snapshot and never re-serialize it for verification.
4. The maker/checker workflow requires two distinct approvals. The proposal
   creator cannot approve their own proposal; this is enforced by both the Go
   service and a PostgreSQL trigger.
5. Publication creates an immutable version. Activation is a separate privileged
   action with an expected-version guard, an idempotency key, and an explicit
   rollback operation. A lower version is rejected unless the caller uses the
   rollback endpoint, and a rollback records the version it replaced.
6. Activation is one PostgreSQL transaction. A shard's `reference.active` pointer
   and the activation record change together; a shard therefore observes either
   the previous complete snapshot or the next complete snapshot. Scheduled
   activations store the expected active version, are promoted by a bounded
   ticker, and fail closed if the shard moved first.
7. Consumers install snapshots through `libs/go/refdata.Consumer` or
   `crates/nexora-refdata`. Replacement is atomic, version regression is rejected
   unless explicitly marked as a rollback, and any failed verification retains
   the last valid snapshot.
8. Administrative authorization is delegated to the existing identity service
   through an additive internal `Authorize` RPC. The reference-data service never
   accepts roles, permissions, MFA state, or step-up assertions from the caller.
   It adds no local accounts and does not become a second identity authority.
9. Market presentation policy is schema-only in B04. It defines source priority,
   freshness and deviation bounds, and a mandatory
   `authoritative_for_risk=false`. It cannot influence canonical pricing, risk,
   margin, funding, or liquidation.

## Consequences

- Consumers receive complete snapshots, not a stream of partial registry
  mutations, eliminating mixed-version reads.
- A configuration can be reproduced and audited from its version, content hash,
  proposal, approvals, and activation record.
- Operators must approve and activate a new version to change behavior; editing
  a draft has no runtime effect.
- Immutable versions occupy storage proportional to complete documents. The
  document is small and compressed by PostgreSQL TOAST where necessary.
- Any future change to canonical serialization requires a new schema version and
  a migration plan for Go and Rust consumers.
- Shard rollout remains single-service, per-shard activation in B04. Real trading
  shards and distributed consensus are later phases and are not claimed here.

## Alternatives considered

- **Normalized mutable registry tables.** Rejected: independent reads can observe
  mixed versions and duplicating validation across tables is error-prone.
- **Consumers read the working draft.** Rejected: drafts are not approved and
  have no immutable identity.
- **Cross-language re-canonicalization for hash verification.** Rejected: number
  and string canonicalization differences could silently change hashes. The
  served bytes are hashed exactly.
- **Implicit backward activation.** Rejected: rollback must be explicit,
  audited, and attributable.
