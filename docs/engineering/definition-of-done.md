# Definition of Done

A change is done only when every applicable item below is satisfied. Reviewers
must block merges that skip an applicable item without a documented exception.

---

## 1. Correctness

- [ ] The change implements the approved phase scope.
- [ ] Financial invariants are preserved (no floating point, zero-sum journals,
      explicit rounding).
- [ ] Error paths return stable, documented error codes.
- [ ] Idempotency and retry behaviour are defined for externally triggered
      operations.
- [ ] Concurrency assumptions are documented and tested.

## 2. Tests

- [ ] Unit tests cover new logic and edge cases.
- [ ] Integration tests cover database, broker, or provider boundaries.
- [ ] Property or model-based tests cover financial arithmetic where relevant.
- [ ] Failure tests cover timeouts, restarts, duplicates, and partial failure.
- [ ] Coverage does not decrease without justification.

## 3. Security

- [ ] Inputs are validated and bounded.
- [ ] Authorization is checked server-side.
- [ ] No secret, key, or Restricted data is logged or committed.
- [ ] Dependencies are pinned and scanned.
- [ ] New external interfaces are threat-modelled.
- [ ] Admin actions are audited and require the correct role.

## 4. Observability

- [ ] Structured logs include `request_id` or correlation ID.
- [ ] Metrics cover requests, errors, latency, and saturation.
- [ ] Traces span external calls and critical state transitions.
- [ ] Alerts and dashboards exist for new failure modes.
- [ ] Runbook updated for operational changes.

## 5. Documentation

- [ ] Public interfaces are documented in `contracts/`.
- [ ] Architecture changes have an ADR.
- [ ] Configuration variables are added to `.env.example` and docs.
- [ ] Migration and rollback steps are documented.
- [ ] Phase report is updated.

## 6. Delivery

- [ ] CI is green: format, lint, test, build, contracts, security.
- [ ] No unrelated changes are included.
- [ ] Rollback is possible without data loss.
- [ ] CODEOWNERS approval is present.
- [ ] The change is deployable to staging.

## 7. Exceptions

Any unchecked item requires a written exception in the pull request with an
owner and a follow-up date. Exceptions affecting financial correctness or
security require architecture and security approval.
