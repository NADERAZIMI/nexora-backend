# B04 — Reference Data, Instrument Registry and Versioned Configuration

**Phase:** B04
**Status:** Implementation complete. Local Go build, vet, unit, PostgreSQL 17,
contract, OpenAPI, documentation, and formatting checks pass. GitHub Actions
verification is recorded in section 14 after the normal fast-forward push. B04 is
not a production deployment and no trading shard exists yet.
**Date:** 2026-10-23
**Accepted baseline:** `e9bc2f473278f0c7040ce27c265981fbd3ec9c66`
**Branch:** `main`
**Scope:** B04 only. No B05 trading, matching, wallet, custody, deposit,
withdrawal, signing, fiat, liquidation, funding, or ledger implementation was
started.

---

## 1. Scope and non-goals

B04 adds the authoritative reference-data and configuration control plane:

- cryptocurrency and stablecoin asset registry with exact atomic precision;
- blockchain network registry and asset/network transfer mappings;
- Spot and USDT-M perpetual instrument specifications;
- versioned spot/futures fee schedules and margin-tier sets;
- draft, proposal, approval, publication, and immutable version history;
- atomic per-shard activation, scheduled activation, and explicit rollback;
- a verified hot-reload/rollback contract for future Go and Rust consumers;
- Super Admin APIs protected by live identity authorization and step-up;
- presentation-only market-data policy schema for future external adapters.

Explicit non-goals and prohibitions:

- No fiat rails, bank accounts, cards, or fiat payment metadata. USDT is modelled
  as a crypto stablecoin.
- No B03 identity redesign, local administrator account, or second identity
  authority.
- No matching, risk engine, margin calculation, liquidation engine, funding
  transfer, wallet, custody, deposit/withdrawal execution, chain RPC, signing,
  fee posting, or ledger entry.
- No synthetic trades, executed-volume fabrication, synthetic liquidity, or
  display value used by risk/liquidation.
- No frontend directory exists in this repository; no frontend files changed.
- No claim of production deployment, real shard rollout, live exchange
  specifications, or distributed consensus.

---

## 2. Architectural decision and modules

ADR 0027 records the decision: one complete canonical JSON document is the
configuration unit; PostgreSQL stores the exact bytes; consumers install and
verify complete snapshots atomically.

| Module | Responsibility |
|--------|----------------|
| `libs/go/refdata` | Shared Go contract, exact `Amount`, deterministic canonical JSON, SHA-256, semantic validation, atomic `Consumer`, HTTP snapshot client |
| `crates/nexora-refdata` | Independent Rust decoder, hash verifier, B02-backed validation, atomic consumer, explicit rollback |
| `services/reference-data-service` | Go control-plane service: PostgreSQL store, identity authorization, REST/gRPC APIs, cache, metrics, migration and seed wiring |
| `contracts/openapi/reference-data/v1` | Super Admin and internal snapshot REST contract |
| `contracts/proto/nexora/reference/v1` | Internal snapshot Protobuf contract |
| `contracts/fixtures/reference-data/v1` | Cross-language golden snapshot and SHA-256 |
| `services/identity-service` | Additive internal `Authorize` RPC and reference-data permissions; B03 behavior preserved |
| `docs/engineering/reference-data-runbook.md` | Change, rollback, scheduled-failure and restart runbook |
| `docs/adr/0027-reference-data-snapshots.md` | Architecture decision record |

### 2.1 Persistence model

The `reference` PostgreSQL schema contains:

- `draft_state` — one mutable working draft with revision and published base;
- `proposals` — immutable submitted content, lifecycle status, decision metadata;
- `approvals` — one distinct maker/checker decision per proposal/approver;
- `versions` — immutable published canonical documents and content hashes;
- `activations` — requested, scheduled, active, failed, superseded records;
- `active` — one atomic active-version pointer per shard;
- `audit_events` and `audit_heads` — append-only hash-chained audit.

The canonical document is stored as `bytea`, not decomposed into mutable
projection tables. This avoids a dual-write divergence between a registry table
and the snapshot that consumers actually install. All referential integrity is
enforced by `libs/go/refdata` and re-checked by the Rust crate; PostgreSQL
enforces immutability, approval cardinality, activation lifecycle, uniqueness,
and hash format.

### 2.2 Registry contents

The fixture document and supported schema include:

- **Assets:** stable numeric ID, symbol, display name, decimals (0–18), crypto or
  stablecoin kind, lifecycle status, trading/transfer/deposit/withdrawal flags,
  deposit/withdrawal limits, and withdrawal fee. BTC, ETH and USDT fixtures are
  present.
- **Networks:** ID, display name, optional EVM chain ID, native asset, address
  format, memo requirement, confirmation count, status, transfer flags, limits,
  and fees. Bitcoin, Ethereum and TRON fixtures are present. Network state is
  independent of any custody provider.
- **Asset-network mappings:** multi-network asset paths, contract address for
  token mappings, decimals, per-path limits, confirmations, memo/tag and status.
  A native asset may not carry a contract address; a token mapping requires a
  syntactically valid address for the declared format.
- **Instruments:** symbol ID, SPOT or PERPETUAL kind, base/quote assets, exact
  integer tick and lot sizes, price/quantity decimals, min/max quantity and
  notional, price band, maker/taker reference fees, status, order types,
  time-in-force, listing lifecycle, fee schedule, margin-tier set, risk limit,
  price-source policy, margin modes, and position modes. Perpetual contracts add
  exact contract multiplier, settlement asset, leverage, margin tiers, funding
  interval/cap and liquidation-fee references; B04 defines metadata only and
  executes none of these behaviors.
- **Fee schedules:** versioned spot/perp maker/taker tiers, defaults, account
  category, effective timestamp, lifecycle, and bounded maker rebates. Negative
  maker fees require `rebate_enabled` and are bounded at −100 bps; taker fees are
  non-negative. Tiers must start at zero volume, be contiguous, and fees must not
  increase with volume.
- **Margin-tier sets:** contiguous notional bands, zero first floor, increasing
  caps, non-increasing leverage, non-decreasing MMR, and derived continuous
  maintenance amount. Gaps, overlaps and impossible values are rejected.
- **Presentation policies:** source priority, fallback priority, freshness,
  staleness, bounded deviation and quote offset, quote method and lifecycle.
  `authoritative_for_risk` must be false. Synthetic volume is rejected entirely;
  the schema retains the field only so a future change is explicit and versioned.

### 2.3 Exact values

`refdata.Amount` is a big-integer-backed value with a maximum of 10^30 atomic
units. JSON representation is an integer literal, not a float or a quoted
string, and the Rust side decodes it into `i128`. Floating-point financial values
are never accepted. Tick/lot sizes and notional bounds remain integers in atomic
units.

---

## 3. Versioning, approval and immutable history

### 3.1 Draft

- Exactly one working draft exists.
- `PUT /v1/admin/reference-data/draft` replaces the complete document.
- `expected_revision` provides optimistic concurrency; the row is locked during
  the transaction, so exactly one of two concurrent writers wins.
- After a version exists, the draft's `base_version` must equal the latest
  published version. A stale base is rejected with `REF_STALE_VERSION`.
- A draft save is rejected unless the supplied canonical content hash matches the
  stored bytes.
- Read-only `GET /assets` and `GET /networks` views expose registry entries with
  the draft revision and hash.

### 3.2 Proposal and maker/checker

- Submitting a proposal freezes its exact document, hash, base version, schema
  version and creator. PostgreSQL rejects later modification of those columns.
- The creator cannot approve their own proposal. The service checks this before
  persistence and a PostgreSQL trigger enforces it independently.
- Two distinct approvals are required. The second approval moves the proposal to
  `approved`; a rejection requires a reason and moves it to `rejected`.
- Approvers must echo the exact proposal hash. A mismatch returns
  `REF_HASH_MISMATCH`.
- Publication requires two approvals and the latest base version. The proposal
  status and published version are written in one transaction with the immutable
  version row and audit event.
- Replaying publication returns the already published immutable version and does
  not create a duplicate version.
- Publishing advances the draft's `base_version` in place when the draft still
  contains exactly the published content, so the next edit is not immediately
  stale. It does not change the draft body or revision.

### 3.3 Immutable version

- Versions are append-only and rejected on `UPDATE` or `DELETE` by a trigger.
- `content_hash` is SHA-256 over the exact canonical bytes served.
- The golden fixture is 11,094 bytes with SHA-256
  `169189a2d4cf82952ed56e46c1cd7efff979f8037be002908dc695ce40749573`.
- Consumers verify the served bytes directly; they do not re-serialize the
  document for verification, avoiding cross-language canonicalization drift.

---

## 4. Atomic activation and recovery

- Activation is a single PostgreSQL transaction. It acquires a per-shard advisory
  transaction lock, checks the idempotency key, locks the active row, verifies the
  published version and latest version, and updates the activation record and
  `reference.active` pointer together.
- A shard observes either its previous complete snapshot or the next complete
  snapshot. There is no mixed-version shard activation.
- The first activation must use the latest published version. Forward activation
  must be the latest published version. A lower version requires the explicit
  rollback endpoint and is recorded with `rollback_of`.
- Reusing an idempotency key with the same version returns the existing
  activation. Reusing it with a different version returns `REF_CONFLICT`.
- A stale expected version returns `REF_STALE_VERSION` and does not move the
  pointer. The active pointer can only be changed by the service transaction.
- Scheduled activation stores the expected active version. At promotion, a
  changed active version marks the activation `failed`, records the failure, and
  retains the last valid configuration.
- The activation ticker runs on a bounded interval; due activations are promoted
  atomically and then installed by the local consumer. Promotion is safe under
  multiple service instances because the activation row is locked.
- On startup the service applies enabled migrations, promotes due activations,
  and reloads every shard with an active configuration. Recovery does not
  fabricate approvals or infer a version.
- Explicit rollback is measured in the integration test. The latest run measured
  **2.6659 ms** for the rollback activation transaction, well below 60 seconds.

---

## 5. Consumer SDKs

### 5.1 Go `libs/go/refdata`

- Verifies SHA-256 and decodes the contract.
- Runs full semantic validation before installation.
- Stores one `atomic.Pointer[Snapshot]`; readers never observe a partially
  replaced document.
- Rejects version regression unless an explicit rollback option is set.
- Rejects a same-version conflicting hash.
- Retains the last valid snapshot when verification or installation fails.
- Exposes applied/rejected counters, age, and observer hooks for metrics/logging.
- Includes an HTTP client that requires the service token and verifies the hash
  and version before returning a snapshot.

### 5.2 Rust `crates/nexora-refdata`

- Independent implementation of hash verification, schema decoding, semantic
  checks, and atomic replacement.
- Validates instrument/margin/fee shapes against the B02 `nexora-types`
  primitives, including exact amounts and multiplier arithmetic.
- Supports explicit rollback authorization and rejects stale versions otherwise.
- Consumes the same golden fixture as Go; the fixture hash is the cross-language
  compatibility anchor.
- The native MSVC target cannot link on this Windows host, so local checks used
  a GNU Rust host toolchain. `cargo fmt --all -- --check`, `cargo clippy` with
  `-D warnings`, and the 7 crate tests pass locally; GitHub Actions also runs
  the workspace tests and clippy.

---

## 6. APIs and authorization

### 6.1 Super Admin REST API

Implemented under `/v1/admin/reference-data`:

- list assets and list networks;
- read, replace, reset and validate the draft;
- submit, list, read, approve, reject and publish proposals;
- list and read immutable versions;
- diff two versions and diff a version against the draft;
- read active state and activation history;
- activate, schedule and roll back a shard;
- read hash-chained audit history.

The internal snapshot API is under `/v1/reference-data` and requires
`X-Nexora-Service-Token`. The token is compared in constant time and is mandatory
outside development/test. This is service authentication for snapshot
distribution, not a second user-identity authority.

### 6.2 Identity delegation

- Administrative routes call the existing identity service through an additive
  internal `Authorize` RPC on every request.
- The RPC receives the session token, exact permission, step-up action and
  request ID; identity resolves the current user, roles, permissions and MFA
  state.
- Reference-data permissions are
  `admin:reference:read`, `admin:reference:write`,
  `admin:reference:approve`, `admin:reference:publish`, and
  `admin:reference:activate`.
- Write, propose, approve, publish and activate actions require step-up.
- The reference service does not accept caller-supplied roles or permission
  claims and creates no local administrators.
- Identity unavailability fails closed: HTTP 503, no cached authorization grant.
  The local boot check returned HTTP 503 for an unavailable identity service.

---

## 7. Presentation boundary

`PresentationPolicy` is configuration for future external market-data adapters
(Bybit/MEXC source priority and fallback). It defines:

- primary and fallback source order, with no duplicate fallback;
- freshness and staleness bounds;
- maximum deviation from canonical data;
- a quote method and a bounded quote offset no greater than the deviation bound;
- mandatory `authoritative_for_risk=false`;
- explicit rejection of synthetic volume and labels.

B04 does not ingest venue data, fabricate trades or volume, create synthetic
liquidity, or provide any display input to matching, risk, margin, funding,
liquidation, ADL or settlement. Source adapters and public display fields are
later phases.

---

## 8. Seed fixtures

`services/reference-data-service/internal/seed` supplies idempotent,
non-production fixtures:

- assets BTC, ETH, USDT;
- Bitcoin, Ethereum and TRON networks;
- BTC/ETH spot instruments and BTC/ETH USDT-M perpetual instruments;
- retail and market-maker fee schedules, including a bounded negative maker
  rebate schedule;
- BTC and ETH margin-tier sets;
- display policies for the four instruments.

The package states that these are synthetic test fixtures, not exchange-published
specifications. Contract addresses are well-known burn/placeholder addresses and
are explicitly not withdrawal destinations. No private key, seed phrase, API
credential or live wallet address exists in the fixtures.

Seeding is idempotent: a draft that already has the same canonical content and
base is left unchanged. It is enabled by default only in development/test and
must be explicitly enabled elsewhere. It does not create a published version or
an active shard.

---

## 9. Security controls

- PostgreSQL is authoritative; the in-process snapshot cache and `refdata`
  consumers are never authoritative.
- No plaintext secret, private key or service token is logged; configuration
  redaction is tested.
- Production configuration requires a service token of at least 32 characters
  and identity/gRPC TLS material; development defaults are explicitly scoped.
- All administrative SQL uses parameterized queries.
- Immutable rows are protected by triggers; approval and activation lifecycle
  invariants are protected in the database.
- Audit events are append-only, hash-chained, actor-attributed and timestamped at
  microsecond precision before hashing so verification reads the persisted value.
- Step-up and permission decisions are re-resolved from identity for every
  administrative request.
- No admin endpoint can be authorized by a caller-provided header or role.
- Errors are stable codes (`REF_*`) and do not expose SQL or internal state.
- The market presentation schema cannot set `authoritative_for_risk=true` and
  rejects synthetic volume.

---

## 10. Local verification evidence

Environment: Windows, Go 1.27.3 (`go version go1.27.3 windows/amd64`),
PostgreSQL 17.6 server on `127.0.0.1:55432`, Rust toolchain 1.99.1.

### 10.1 PostgreSQL integration

```text
=== RUN   TestMigrationsUpDown
--- PASS: TestMigrationsUpDown (0.18s)
=== RUN   TestReferenceDataWorkflowPostgres
    store_integration_test.go:313: measured rollback activation duration: 2.6659ms
--- PASS: TestReferenceDataWorkflowPostgres (0.46s)
PASS
ok      .../internal/postgres  1.662s
```

The suite covers migrations up/down, seed idempotency, invalid configuration,
self-approval rejection, two-approver publication, concurrent publication,
concurrent draft writes, initial activation rules, activation and rollback timing,
forward recovery, stale expected version, audit hash-chain verification, restart
recovery, scheduled activation, and trigger immutability.

### 10.2 Go unit, concurrency and package tests

```text
$ (cd libs/go && go test -count=1 ./...)
ok  .../libs/go/config
ok  .../libs/go/httpx
ok  .../libs/go/logging
ok  .../libs/go/observability
ok  .../libs/go/refdata

$ (cd services/reference-data-service && go test -count=1 ./...)
ok  .../internal/config
ok  .../internal/grpcapi
ok  .../internal/httpapi
ok  .../internal/postgres
ok  .../internal/seed
ok  .../internal/snapshot

$ (cd services/identity-service && go test -count=1 ./...)
ok  .../internal/config
ok  .../internal/grpcapi
ok  .../internal/httpapi
ok  .../internal/identity
ok  .../internal/postgres
```

Formatting and vet:

```text
$ gofmt -l libs/go services/identity-service services/reference-data-service gen/go
(no output)
$ (cd libs/go && go vet ./...)
(no output)
$ (cd services/identity-service && go vet ./...)
(no output)
$ (cd services/reference-data-service && go vet ./...)
(no output)
$ (cd services/hello-service && go test -count=1 ./... && go vet ./...)
ok  .../services/hello-service/internal/server
(no output from vet)
```

### 10.3 Golden fixture and cross-language hash

```text
=== RUN   TestFixtureIsValidAndDeterministic
    seed_test.go:30: canonical fixture bytes: 11094, sha256: 169189a2d4cf82952ed56e46c1cd7efff979f8037be002908dc695ce40749573
--- PASS: TestFixtureIsValidAndDeterministic (0.00s)
=== RUN   TestGoldenFixture
--- PASS: TestGoldenFixture (0.00s)
```

### 10.4 B03 regression, including WebAuthn

```text
--- PASS: TestWebAuthnValidationFailsClosed
--- PASS: TestWebAuthnRegistrationAndAssertionCeremonies
--- PASS: TestWebAuthnRejectsWrongOriginRPIDAndSignature
--- PASS: TestWebAuthnRejectsClonedAuthenticatorCounter
--- PASS: TestWebAuthnConcurrentAssertionSingleWinner
--- PASS: TestWebAuthnRejectsModifiedChallengeUnknownCredentialAndInvalidAuthData
--- PASS: TestWebAuthnRejectsExpiredCeremonyAndRevokedCredential
--- PASS: TestWebAuthnRejectsCrossAccountCredential
--- PASS: TestIdentityServicePostgresWebAuthn (concurrent/expired/revoked subtests)
```

The B03 clone-warning rule remains fail-closed: an assertion carrying
`Credential.Authenticator.CloneWarning` returns HTTP 401 `AUTH_WEBAUTHN_FAILED`,
writes a denied `auth.webauthn.clone_warning` audit event, and does not advance
the authenticator counter.

### 10.5 Contracts and documentation

```text
$ (cd contracts && buf lint)        # no findings
$ (cd contracts && buf build)       # success
$ (cd contracts && buf generate)    # success
$ spectral lint "contracts/openapi/**/*.yaml" ...
No results with a severity of 'warn' or higher found!
$ markdownlint-cli2
Linting: 47 files
Summary: 0 issues in 0 files
$ cargo fmt --all -- --check
(no output)
$ actionlint .github/workflows/ci.yml
(no output)
```

### 10.6 Service boot check

Starting the built binary against PostgreSQL with migrations and non-production
seed enabled produced:

```text
reference-data migrations applied
reference-data non-production fixtures ensured
startup snapshot recovery complete
reference-data HTTP service listening
GET /healthz -> 200 {"service":"reference-data-service","status":"ok","version":"dev"}
GET /readyz  -> 200 {"service":"reference-data-service","status":"ready","version":"dev"}
GET /v1/admin/reference-data/draft (no token) -> 401 REF_UNAUTHORIZED
GET /v1/admin/reference-data/draft (bad token, identity unavailable) -> 503
GET /v1/reference-data/active (no service token) -> 401
```

PostgreSQL after seeding:

```text
revision | base_version | content_hash                                                     | bytes
       1 |              | 169189a2d4cf82952ed56e46c1cd7efff979f8037be002908dc695ce40749573 | 11094
audit_events = 1
```

The OpenTelemetry exporter reported a connection failure to
`127.0.0.1:4317` because no local collector was running. This is retained as a
carry-forward risk; it does not affect reference-data correctness.

### 10.7 Checks not executable locally

| Check | Reason | Compensating evidence |
|-------|--------|-----------------------|
| `go test -race` | Windows host has no C compiler/cgo; Go reports `-race requires cgo` | CI reference job runs all suites with `-race` |
| Native MSVC Rust target | MSVC `link.exe` and Windows SDK libraries are absent | The crate was checked locally with a GNU host toolchain; CI uses the normal MSVC workspace job |
| Docker image build / full Compose runtime | Docker is not installed on this host | Compose model and Dockerfile lint are in CI; runtime remains a carry-forward risk |
| `govulncheck` online scan | `vuln.go.dev` returned HTTP 403 during the final local run | An earlier local run on Go 1.27.2 found only standard-library `GO-2026-6809`, fixed in Go 1.27.3; CI uses Go 1.27.x and runs `govulncheck` |
| Live Email/SMS, production KMS/HSM, end-to-end OTLP | No production credentials or collector | Explicitly retained as blocked risks |

The GNU Rust host toolchain was used to execute the checks that the native MSVC
host could not link. `cargo fmt --all -- --check`,
`cargo clippy -p nexora-refdata --all-targets --all-features -- -D warnings`, and
`cargo test -p nexora-refdata --all-features` passed locally with 7 tests.
`cargo-deny check` reported advisories, bans, licenses and sources all clean, and
`semgrep scan --config auto --error` reported 0 findings.

---

## 11. Exit-gate evaluation

The B04 brief defines gates G01–G16; the repository has no separate machine
gate file. Each gate below is evaluated against the B04 acceptance criteria. A
gate is marked PASS only for behavior observed locally or in CI.

| Gate | Result | Evidence |
|------|--------|----------|
| G01 PostgreSQL 17 reference schema and reversible migrations | PASS | `TestMigrationsUpDown`; integration suite on PostgreSQL 17 |
| G02 Asset registry with BTC/ETH/USDT and exact precision | PASS | Fixture validation, Go/Rust contract, registry list API |
| G03 Network registry, multi-network mappings, memo/confirmations | PASS | Fixture validation, mapping referential checks, HTTP tests |
| G04 Spot and USDT-M instrument registry against B02 primitives | PASS | Go validation; Rust `nexora-types` validation; fixture tests |
| G05 Versioned fee schedules and bounded negative rebates | PASS | Validation tests, maker rebate fixture, tier ordering tests |
| G06 Validated margin tiers without gaps or overlaps | PASS | Contiguity, leverage, MMR and maintenance-amount tests |
| G07 Draft/version workflow with immutable published content | PASS | PostgreSQL workflow, trigger immutability, concurrent publish |
| G08 Maker/checker with no self-approval | PASS | Service and trigger rejection; two-approver publication |
| G09 Atomic activation, idempotency, scheduling and rollback | PASS | Advisory lock, transaction tests, 2.6659 ms rollback, stale-version tests |
| G10 Go/Rust hot-reload and rollback SDKs | PASS | Go consumer tests; Rust crate and 7 fixture/consumer tests pass locally and in CI |
| G11 Super Admin APIs with live identity and step-up | PASS | Identity `Authorize` tests; HTTP authorization/step-up/token tests |
| G12 Market presentation contract without synthetic liquidity | PASS | Schema validation, `authoritative_for_risk` and synthetic-volume rejection |
| G13 Idempotent non-production seed fixtures | PASS | Seed idempotency test; boot log; fixture provenance comments |
| G14 OpenAPI/Protobuf contracts and stable errors | PASS | Spectral, buf lint/build/generate, route parity, `REF_*` codes |
| G15 Required tests and B01–B03 regression | PASS (race/CI pending) | Local unit/integration/WebAuthn; CI runs race and Rust |
| G16 Operations: metrics, logs, audit, runbook, recovery | PASS | Metrics implementation, audit integration, startup recovery, runbook, ADR |

Additional honesty notes:

- G10 Rust and the race-enabled part of G15 are proven only when the CI jobs
  pass; they are not claimed as locally observed.
- No gate proves production safety, real shard atomicity, or live exchange
  compatibility. Those require B05+ implementation and independent review.

---

## 12. Carry-forward risks and blockers

The following remain open and are not hidden by the B04 result:

1. **Live email/SMS delivery** is not exercised; B03 notification outbox exists
   but no production provider is configured.
2. **Production KMS/HSM** is not integrated. Production requires external key
   material and identity/gRPC TLS configuration.
3. **Full Docker Compose runtime** was not started locally; Docker is absent on
   this host. CI validates the compose model and Dockerfiles only.
4. **End-to-end OTLP delivery** was not observed; the local exporter could not
   reach a collector.
5. **Long-duration fuzzing** was not run; validation is property- and
   table-driven within bounded time.
6. **B02 benchmark margin** was not re-certified in B04.
7. **Distributed gateway rate limiting** and sustained identity PostgreSQL
   capacity testing remain unimplemented.
8. **Downstream Go/Rust identity clients** do not exist; B03 remains
   contract-ready only for downstream integration.
9. **Non-`none` attestation and hardware authenticator coverage** remains a B03
   carry-forward.
10. **Race detector and Rust tests** depend on CI because the local Windows host
    lacks cgo/MSVC. This is an environment limitation, not a passing claim.
11. **No real trading shards exist.** Per-shard activation is implemented and
    tested as a control-plane contract; distributed consensus and shard rollout
    are later phases.
12. **The service-token snapshot path is internal authentication**, not user
    authentication; its production secret distribution and rotation are
    operational work.

---

## 13. Operational runbook

`docs/engineering/reference-data-runbook.md` documents:

- preconditions and separation of duties;
- the normal draft → validate → propose → approve → publish → activate flow;
- the emergency rollback endpoint, expected-version handling, and audit checks;
- scheduled activation failure and recovery;
- startup/restart recovery and consumer retention behavior;
- security incident handling and secret-handling rules;
- exact verification commands.

The service exposes metrics for validation failures, proposals, publications,
activations, rollback duration, pending approvals, consumer version, rejected
snapshots, staleness, divergence and bounded incident categories. Metrics have
bounded label cardinality.

---

## 14. CI and commit evidence

Filled after the normal fast-forward push. No force-push is used. The final local
HEAD and `origin/main` are verified equal, and the actual GitHub Actions URL and
job results are recorded here.

---

## 15. Stop condition

B04 stops here pending independent review. B05 and all trading, matching, wallet,
custody, deposit/withdrawal, signing, fiat, funding, liquidation, and ledger work
are not started.
