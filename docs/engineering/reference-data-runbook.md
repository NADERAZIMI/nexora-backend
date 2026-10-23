# Reference Data Change and Rollback Runbook

**Scope:** `reference-data-service` (B04), the `reference` PostgreSQL schema,
and consumers of `/v1/reference-data/active`.

This runbook covers a normal configuration change, an emergency rollback, a
failed scheduled activation, and recovery after a restart. It does not authorize
a production deployment. B04 is pending independent review and B05.

## 1. Preconditions

- Two distinct administrators hold `admin:reference:*` permissions and valid
  step-up authenticators. The proposer cannot approve their own proposal.
- The identity service is reachable over mTLS and the reference service is
  configured with `NEXORA_REFERENCE_IDENTITY_GRPC_ADDR` plus CA/client
  certificate paths outside development.
- `NEXORA_REFERENCE_INTERNAL_TOKEN` is at least 32 characters and is distributed
  only to trusted snapshot consumers.
- PostgreSQL 17 is healthy and the `reference` schema migration is applied.
  Migrations are never run with the destructive `down` mode in production.
- The latest CI `Reference data (Go, PostgreSQL 17)` job is green and the golden
  fixture hash matches `contracts/fixtures/reference-data/v1/snapshot.sha256`.

## 2. Normal change

1. `GET /v1/admin/reference-data/draft` and record `revision`, `base_version`,
   and `content_hash`.
2. `PUT /v1/admin/reference-data/draft` with the complete document, the observed
   `expected_revision`, and `base_version` equal to the latest published version.
   Include `Authorization: Bearer <session>` and `X-Step-Up-Token`.
3. `POST /v1/admin/reference-data/draft/validate`. Stop on any issue. Validation
   failures return `REF_VALIDATION_FAILED`; do not bypass them.
4. `POST /v1/admin/reference-data/proposals` with the draft revision. This
   freezes an immutable proposal and returns its `content_hash`.
5. A second administrator reviews the diff and calls
   `POST /v1/admin/reference-data/proposals/{id}/approve` with the exact
   `proposal_hash`. A third approval is allowed; two are required.
6. A publisher calls `POST /v1/admin/reference-data/proposals/{id}/publish`.
   Record the returned immutable version and effective timestamp.
7. Inspect `GET /v1/admin/reference-data/active?shard_id=<shard>`. If no shard is
   active yet, activate the latest published version.
8. `POST /v1/admin/reference-data/activations` with `shard_id`, `version`,
   `expected_version`, a unique `idempotency_key`, and an optional
   `effective_at`. Reuse of a key with the same version is safe; reuse with a
   different version returns `REF_CONFLICT`.
9. Verify `GET /v1/reference-data/active?shard_id=<shard>` with the service token.
   Compare `X-Nexora-Config-Version` and `X-Nexora-Config-Hash` to the published
   version. Consumer metrics must show
   `nexora_reference_snapshot_applied_total` increasing and
   `nexora_reference_config_stale` returning to 0.

## 3. Emergency rollback

Use this when a published version causes a verified functional or risk incident.
Do not mutate a published version; it is immutable and the database rejects it.

1. Identify the target older version from
   `GET /v1/admin/reference-data/versions`.
2. Record the current active version and activation ID.
3. `POST /v1/admin/reference-data/rollbacks` with:
   - `shard_id`
   - `version` = the older verified version
   - `expected_version` = the currently active version
   - `reason` = incident or change identifier
   - `idempotency_key` = a new unique value
4. The response is the rollback activation. `rollback_of` must equal the version
   being replaced. The database writes an immutable activation and audit event.
5. Verify the internal snapshot endpoint and the consumer metric
   `nexora_reference_rollback_duration_seconds`. The B04 integration test asserts
   the measured activation completes within 60 seconds; the observed local time
   is recorded in `reports/B04/REPORT.md`.
6. If the rollback endpoint returns `REF_STALE_VERSION`, the active version moved.
   Re-read the active version, re-assess, and retry with the new expected value.
   Never edit `reference.active` or `reference.activations` by hand.
7. If the service or consumer fails to refresh, see section 5. The consumer keeps
   its last verified snapshot; it never serves a partial document.

## 4. Scheduled activation failure

- A scheduled activation stores the expected active version at scheduling time.
- At promotion, if the shard's active version changed, the activation becomes
  `failed` and the shard keeps its last valid configuration.
- Inspect `GET /v1/admin/reference-data/activations` and the audit action
  `reference.activation.fail`.
- Re-schedule against the current active version with a new idempotency key.
- `POST /v1/admin/reference-data/activations` is the only supported recovery
  path; do not edit tables directly.

## 5. Restart and recovery

1. Start PostgreSQL before the reference service.
2. On startup the service applies migrations only when
   `NEXORA_REFERENCE_AUTO_MIGRATE=true` (default in development/test), then
   promotes due scheduled activations and reloads each active shard from
   PostgreSQL.
3. If no active version exists, readiness still requires PostgreSQL but the
   internal snapshot endpoint returns `REF_NOT_FOUND`. This is a safe empty state,
   not a reason to fabricate approvals.
4. A consumer that cannot reach the service keeps its last verified snapshot and
   increments `nexora_reference_snapshot_rejected_total` or
   `nexora_reference_config_stale`. Investigate rather than force-installing an
   unverified document.
5. Audit chain verification: query `reference.audit_events` through
   `GET /v1/admin/reference-data/audit`; each event's `prev_hash` must equal the
   preceding event's `event_hash`, and each event hash covers the persisted
   metadata and microsecond-truncated timestamp.

## 6. Security and incident handling

- Never log access tokens, step-up tokens, service tokens, TLS keys, or document
  contents that include future live contract addresses.
- A `REF_FORBIDDEN`, `REF_STEP_UP_REQUIRED`, or `REF_SELF_APPROVAL` response is a
  control working as intended. Do not retry by reusing another administrator's
  credentials.
- If an unauthorized publication is suspected, inspect the hash-chained audit,
  revoke the identity session through the identity service, and roll the shard
  back to a verified version. B04 has no direct session revocation path of its
  own.
- Seed fixtures are non-production only. They are not exchange-published tick
  sizes, fees, margin tiers, or live custody destinations.

## 7. Verification commands

```bash
# Full Go unit, concurrency, atomicity, and PostgreSQL integration suite
cd services/reference-data-service
NEXORA_TEST_DATABASE_URL=postgres://... go test -race -count=1 ./...

# PostgreSQL evidence only
NEXORA_TEST_DATABASE_URL=postgres://... go test -race -count=1 \
  -run 'Test(MigrationsUpDown|ReferenceDataWorkflowPostgres)$' -v ./internal/postgres

# Cross-language golden fixture and deterministic hash
go test -count=1 -run 'TestGoldenFixture|TestFixtureIsValidAndDeterministic$' -v ./internal/seed

# OpenAPI lint
npm run lint:openapi
```
