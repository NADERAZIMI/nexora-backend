<!--
Use this template for every pull request. Keep sections that apply and delete
the rest. Do not include secrets, customer data, or production credentials.
-->

## Summary

<!-- What changed and why? Link the phase and ADR. -->

- Phase:
- ADR:
- Related issue:

## Scope

<!-- What is intentionally out of scope? -->

## Test evidence

<!-- Commands run and results. Screenshots are not a substitute for logs. -->

```text
cargo fmt --all -- --check
cargo clippy --workspace --all-targets --all-features -- -D warnings
cargo test --workspace --all-features
(cd libs/go && go test ./...)
(cd services/hello-service && go test ./...)
buf lint
npm run lint:contracts
npm run lint:md
```

## Financial and security impact

- [ ] No floating-point arithmetic added to financial paths.
- [ ] No new secret, key, or Restricted data in code, logs, or tests.
- [ ] Authorization checked server-side for new operations.
- [ ] Idempotency and retry behaviour defined where applicable.
- [ ] Threat model updated if a trust boundary changed.
- [ ] Display data cannot reach matching, ledger, risk, or settlement.

## Definition of Done

- [ ] Tests added or updated.
- [ ] Observability added (logs, metrics, traces, alerts).
- [ ] Contracts and `.env.example` updated.
- [ ] Migration and rollback documented.
- [ ] CI is green.
- [ ] CODEOWNERS approval obtained.

## Rollback

<!-- How do we revert safely? Include data implications. -->
