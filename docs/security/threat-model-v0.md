# Threat Model v0 — B01 Baseline

**Status:** Draft for B01
**Owner:** Security Engineering
**Last updated:** 2026-10-19
**Scope:** Repository, CI, service skeletons, contracts, and local development stack

This is an initial threat model for the engineering baseline. It is not a
complete model of the exchange. Trading, ledger, custody, and market-data
threat models are extended in the phases that implement them.

---

## 1. Assets

| Asset | Classification | Impact if compromised |
|-------|----------------|-----------------------|
| Customer funds and custody keys | Restricted | Catastrophic loss |
| Ledger and settlement state | Restricted | Financial misstatement, loss |
| Matching state and order data | Confidential | Market abuse, unfair advantage |
| API keys, sessions, credentials | Restricted | Account takeover |
| Source code and CI credentials | Confidential | Supply-chain compromise |
| Market-data raw feeds | Internal | Manipulation, incorrect pricing |
| Display configuration | Internal | Misleading customers, regulatory breach |
| Telemetry and audit logs | Confidential | Data leakage, evidence loss |

## 2. Actors

- External customer and unauthenticated internet client.
- Market-data venue (Bybit, MEXC) and its API.
- Custody or wallet provider.
- Insider with repository, CI, or production access.
- Malicious dependency or build artifact.
- Automated scanner or bot.

## 3. Trust boundaries

```text
Internet
  |  TLS, WAF, rate limits
  v
Edge services (Go)  --->  Core services (Rust)  --->  PostgreSQL / Redpanda
  |                          |                          |
  |                          +--> Custody provider      +--> Object storage
  |                          +--> Venue adapters
  v
Observability stack (logs, metrics, traces)
```

Boundaries that require explicit authentication and authorization:

1. Internet to edge.
2. Edge to internal gRPC services.
3. Core services to databases and brokers.
4. Services to third-party venues and custody providers.
5. CI runners to artifact registries and production deploy.
6. Operators to admin and back-office interfaces.

## 4. STRIDE summary for B01

| Category | Threat | B01 control | Residual risk |
|----------|--------|-------------|---------------|
| Spoofing | Forged service identity on internal calls | mTLS and workload identity planned for B10; local stack is isolated | Internal auth not yet enforced |
| Tampering | Modified dependency or build artifact | Lockfiles, checksums, CI SBOM, dependency review | Registry compromise |
| Repudiation | Untracked configuration change | Git history, CODEOWNERS, ADR process | No production audit trail yet |
| Information disclosure | Secret committed to repository | gitleaks in CI, `.gitignore`, secret review | Developer bypass |
| Denial of service | Resource exhaustion of sample services | Body limits, timeouts, graceful shutdown | No rate limiting until B10 |
| Elevation of privilege | Over-broad CI token | Least-privilege workflow permissions | Third-party action compromise |
| Supply chain | Malicious npm/cargo/go package | Lockfiles, cargo-deny, govulncheck, npm audit, pinned toolchains | New zero-days |
| Misconfiguration | Local stack exposed publicly | Bind to localhost by default, dev-only credentials | Developer exposes ports |
| Data integrity | Display data used in financial paths | Separate `nexora.display.v1` package and ADR-025 | Future code may ignore boundary |

## 5. Priority threats and treatments

### T1 — Customer fund loss through ledger or settlement defects

- **Phase:** B05–B09.
- **Controls:** Double-entry invariants, property tests, reconciliation,
  crash tests, maker/checker adjustments, separation of duties.
- **B01 contribution:** No financial logic is implemented; the baseline
  prevents accidental scope creep.

### T2 — Custody key compromise

- **Phase:** B14.
- **Controls:** HSM/MPC, quorum signing, address allowlists, withdrawal
  velocity limits, independent reconciliation.
- **B01 contribution:** Custody provider interface forbids private-key export.

### T3 — Market-data manipulation or misleading display

- **Phase:** B12 and admin configuration phase.
- **Controls:** Raw venue data retained append-only, multi-venue aggregation,
  outlier filtering, display layer physically separate from risk inputs.
- **B01 contribution:** `nexora.marketdata.v1` raw contracts and
  `nexora.display.v1` presentation-only contract, plus ADR-025.

### T4 — CI supply-chain compromise

- **Phase:** B01 onward.
- **Controls:** Pinned toolchains, lockfiles, least-privilege workflow token,
  dependency and secret scans, SBOM, review of workflow changes.
- **Residual:** Third-party GitHub Actions are trusted dependencies and must be
  pinned by SHA before production use.

### T5 — Secret leakage in logs or telemetry

- **Phase:** B01 onward.
- **Controls:** Structured logging with explicit fields, no request bodies or
  credentials in logs, data classification rules.
- **Residual:** Developers can still log sensitive values; review is required.

## 6. Security requirements for B01 exit

- [x] Secret scanning configured and enforced in CI.
- [x] Dependency scanning for Go, Rust, and Node tooling configured.
- [x] SBOM generated in CI.
- [x] Least-privilege workflow permissions.
- [x] No production secrets in the repository.
- [x] Data classification documented.
- [x] ADR process established.
- [ ] Branch protection and signed commits enabled in the hosting platform
      (requires repository administration, not code).
- [ ] Penetration test of the service skeleton (deferred to B16).

## 7. Open items

1. Replace floating local image tags with digests before shared use.
2. Pin GitHub Actions to commit SHAs.
3. Add mTLS/workload identity before any internal service is exposed.
4. Define log redaction rules per data classification.
5. Add threat models for ledger, matching, custody, and market data in their
   implementation phases.
