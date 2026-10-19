# 13 — Authentication and Security

---

## 1. Identity model

### 1.1 Principals

| Principal | Credential | Used by |
|-----------|------------|---------|
| User (web/mobile) | Password + 2FA + session | Retail UI |
| API key | Key ID + HMAC secret or Ed25519 private key | Trading bots, market makers |
| Service | mTLS certificate + SPIFFE ID | Internal services |
| Operator | SSO + hardware MFA | Back office |
| Batch job | Kubernetes service account + Vault token | Scheduled jobs |

Every principal has a stable ID, permissions, and audit trail. Human and machine
identities are separate; an API key cannot perform administrative actions.

### 1.2 Roles and scopes

API key scopes:

| Scope | Allows |
|-------|--------|
| `read` | Balances, orders, positions, history |
| `trade` | Place/cancel orders, set leverage/margin mode |
| `transfer` | Spot ↔ futures transfers |
| `withdraw` | Withdrawal requests (still requires 2FA) |
| `admin` | Internal only, never issued to customers |

UI roles:

| Role | Allows |
|------|--------|
| `customer` | Own account and trading |
| `support` | Read-only customer view, case notes |
| `risk_operator` | Freeze accounts, cancel orders, view risk |
| `compliance` | KYC/AML cases, restrictions |
| `treasury` | Custody and fund operations (dual approval) |
| `admin` | Configuration, no direct fund movement |
| `auditor` | Read-only audit access |

Separation of duties: the `admin` role cannot approve withdrawals; the
`treasury` role cannot change its own limits; `risk_operator` cannot adjust
ledgers.

---

## 2. Passwords

- Minimum 12 characters, maximum 128.
- Argon2id: memory 64 MiB, iterations 3, parallelism 4, 16-byte salt.
- Pepper stored in Vault/HSM and mixed into the hash input.
- Breached-password check against a local k-anonymized list at registration and
  password change.
- No password hints, security questions, or password reuse across environments.
- Password reset tokens: 32 bytes, single-use, 15-minute TTL, hashed at rest.
- Failed login: exponential backoff after 5 failures, CAPTCHA after 10,
  temporary lock after 20; alerts for distributed attacks.
- Password change requires current password + 2FA and revokes all other
  sessions.

---

## 3. Sessions

- Access token: JWT, EdDSA (Ed25519), 15-minute TTL, includes `sub`, `sid`,
  `roles`, `iat`, `exp`, `jti`.
- Refresh token: 256-bit random, 30-day TTL, rotating on every use, stored as a
  hash.
- Refresh token reuse detection: reuse of an old token revokes the entire token
  family and requires re-authentication.
- Sessions are bound to a device fingerprint and coarse IP / ASN; a significant
  change requires 2FA.
- Maximum 10 active sessions per user; oldest is revoked.
- Logout revokes the session and its refresh family.
- JWTs are validated locally by the edge with a cached JWKS; revocation is
  enforced by a short TTL plus a revocation list for high-risk events.

---

## 4. Multi-factor authentication

| Method | Use | Notes |
|--------|-----|-------|
| TOTP (RFC 6238) | Default 2FA | 30-second window, ±1 step, replay prevention |
| WebAuthn/FIDO2 | Preferred | Hardware keys, platform authenticators |
| Recovery codes | Backup | 10 single-use codes, hashed |
| SMS | Fallback only | Delays and limits; not sufficient for withdrawals above threshold |

Withdrawals and API key creation require a 2FA proof no older than 60 seconds.
The proof is a signed assertion from the identity service, bound to the action
and amount, and verified by the wallet service.

---

## 5. API keys

### 5.1 Format

```
nx_live_<keyId>_<secret>
```

- `keyId`: 16 random bytes, base32.
- `secret`: 32 random bytes, base62.
- HMAC keys: the secret is stored encrypted with a KMS-backed envelope key and
  decrypted only inside the auth service to verify request signatures. Access
  is audited. This is required because HMAC verification needs the secret.
- Ed25519 keys: only the public key is stored; the client signs with its private
  key. Ed25519 is preferred for high-value and institutional keys.
- The full key is shown once at creation.

### 5.2 Verification

1. Parse `keyId` and look up the record (cached in Redis for 60 s).
2. Reject if revoked, expired, or IP not allowlisted.
3. Verify the request signature:
   - HMAC: decrypt the secret, compute HMAC over the canonical request, and
     compare in constant time.
   - Ed25519: verify the signature against the stored public key.
4. Verify timestamp within the receive window.
5. Check scopes.
6. Update `last_used_at` asynchronously.

Verification runs in `identity-service` (or a co-located auth sidecar) over
mTLS; the edge never persists API secrets. Ed25519 public keys may be cached at
the edge for local verification, avoiding a hop for high-volume institutional
clients. HMAC verification results are never cached beyond the request lifetime.

### 5.3 Lifecycle

- Maximum 10 active keys per user.
- Optional expiry (default 90 days, max 1 year).
- Rotation: create new key, migrate, revoke old key.
- Compromise: revoke immediately; all requests fail within the cache TTL (≤60 s)
  and instantly on cache invalidation.
- Keys are never logged, included in URLs, or stored in browser storage.

---

## 6. Service identity

- Every service has a SPIFFE ID: `spiffe://nexora/<env>/<service>/<shard>`.
- mTLS certificates are short-lived (1 hour) and rotated automatically by SPIRE.
- Authorization policies are deny-by-default and specify which service can call
  which RPC.
- No shared service accounts or static tokens between services.
- Database credentials are dynamic, short-lived, issued by Vault, and scoped to
  the service role.
- Kubernetes network policies restrict pod-to-pod traffic to declared
  dependencies.

---

## 7. Threat model (STRIDE)

| Threat | Example | Control |
|--------|---------|---------|
| Spoofing | Stolen API key | HMAC, IP allowlist, scopes, revocation, anomaly detection |
| Tampering | Modified order payload | TLS, request signing, server-side validation |
| Repudiation | User denies an order | Signed requests, audit logs, immutable order events |
| Information disclosure | Balance leak | Authorization checks, no IDOR, encrypted PII, field-level access |
| Denial of service | Order flood | Rate limits, WAF, backpressure, priority lanes |
| Elevation of privilege | API key performs admin action | Separate admin surface, RBAC, scopes |
| Replay | Re-sent withdrawal | Timestamp window, idempotency keys, signature cache |
| Insider abuse | Manual balance credit | Four-eyes, audit, least privilege, session recording |
| Supply chain | Malicious dependency | Pinned versions, SBOM, signed images, dependency review |
| Data corruption | Partial write | Transactions, WAL, checksums, reconciliation |

Additional abuse cases:

- Credential stuffing → breached-password checks, rate limits, device reputation.
- Account takeover → 2FA, withdrawal whitelist, session anomaly detection.
- API key leakage → scoped keys, IP allowlist, usage alerts.
- Wash trading → surveillance, STP, account linkage analysis.
- Oracle manipulation → multi-venue median, outlier filters, circuit breakers.
- Front-running → no privileged order path, fair sequencing, rate limits.

---

## 8. Application security

### 8.1 Secure development

- Threat modeling for every new service and financial flow.
- Security review for changes touching auth, custody, ledger, or matching.
- Mandatory code review by at least one owner and one security-aware reviewer.
- SAST, dependency scanning, secret scanning, and IaC scanning in CI.
- Fuzzing for parsers, matching, and ledger arithmetic.
- No `unsafe` in financial Rust crates without an approved ADR.
- No dynamic SQL; parameterized queries only.
- Output encoding and input validation at every boundary.

### 8.2 Runtime

- Distroless or minimal container images, non-root user, read-only root
  filesystem.
- Seccomp and AppArmor profiles.
- No shell in production containers.
- Signed images verified by admission control.
- Resource limits and pod disruption budgets.
- Egress restricted to declared dependencies.

### 8.3 Secrets

- Never in source, images, environment files, or logs.
- Vault dynamic secrets with short TTL.
- Kubernetes secrets are not used for long-lived credentials.
- Secret rotation is automated and tested.
- Break-glass credentials are sealed, audited, and rotated after use.

---

## 9. Data protection

| Data | At rest | In transit | Access |
|------|---------|------------|--------|
| Passwords | Argon2id + pepper | TLS 1.3 | Auth service only |
| API secrets | KMS-encrypted (HMAC) or public key (Ed25519) | TLS 1.3 | Auth service only |
| PII | Envelope encryption (KMS) | TLS 1.3 | Identity/compliance, audited |
| Financial records | Encrypted volumes | TLS 1.3 | Owning service + auditors |
| Audit logs | WORM + encryption | TLS 1.3 | Security/compliance |
| Backups | Encrypted, separate keys | TLS 1.3 | Ops with break-glass |

- Data classification: Public, Internal, Confidential, Restricted.
- Retention and deletion policies per class and jurisdiction.
- PII is tokenized in analytics; joins use pseudonymous IDs.
- Logs redact secrets, tokens, full addresses (truncated), and PII.
- Database access is audited; production reads require a ticket or break-glass.
- Encryption keys are rotated annually and on compromise.

---

## 10. Network and edge security

- CDN + WAF + DDoS scrubbing in front of all public endpoints.
- TLS 1.3 only, HSTS, modern cipher suites, OCSP stapling.
- Bot management and device fingerprinting for login and registration.
- Per-IP, per-account, and per-key rate limits with adaptive thresholds.
- Geo/IP restrictions for sanctioned or restricted jurisdictions.
- WebSocket connection caps and message rate limits.
- No direct access to core services from the internet.
- Admin ingress on a separate hostname, VPN/zero-trust proxy, hardware MFA.
- Regular external attack surface scans.

---

## 11. Audit and monitoring

### 11.1 Audit events

- Authentication success/failure, MFA changes, password changes.
- API key creation/revocation.
- Order placement/cancel (sampled for high volume, full for admin).
- Withdrawal request, approval, broadcast, settlement.
- Manual ledger adjustments.
- Parameter and permission changes.
- Break-glass access.
- Data exports and admin reads of PII.

### 11.2 Audit properties

- Append-only, hash-chained, WORM storage.
- Includes actor, action, target, before/after, timestamp, IP, user agent,
  request ID.
- Tamper-evident (daily Merkle root anchored to an independent store).
- Retained 7 years; exportable for regulators.
- SIEM ingestion with correlation rules and alerting.

### 11.3 Security monitoring

- Impossible travel and new-device logins.
- Credential stuffing patterns.
- API key usage anomalies (new IP, volume spike).
- Withdrawal velocity and address changes.
- Privileged action outside business hours.
- Failed authorization spikes.
- Database access anomalies.
- Container and host runtime alerts.

---

## 12. Incident response

| Phase | Actions |
|-------|---------|
| Detect | Alert, customer report, or monitoring |
| Triage | Severity, scope, affected funds/accounts |
| Contain | Revoke keys, freeze accounts, pause withdrawals/trading |
| Eradicate | Patch, rotate credentials, remove access |
| Recover | Restore service, reconcile, communicate |
| Review | Post-incident report, corrective actions |

Security incidents involving fund loss or data breach trigger legal,
compliance, and regulatory notification assessment within the required
timeframes. Evidence is preserved; communications go through a single
spokesperson.

---

## 13. Security testing and assurance

| Activity | Frequency |
|----------|-----------|
| SAST/DAST | Every release |
| Dependency and container scan | Every build |
| Secret scan | Every commit |
| Fuzzing | Continuous |
| Internal penetration test | Quarterly |
| External penetration test | Before launch, annually |
| Red team | Annually |
| Bug bounty | Continuous post-launch |
| Access review | Quarterly |
| Disaster recovery test | Quarterly |
| Key ceremony rehearsal | Annually |

Findings are tracked with severity and SLA:

| Severity | Fix SLA |
|----------|---------|
| Critical | 24 hours |
| High | 7 days |
| Medium | 30 days |
| Low | 90 days |

---

## 14. Security invariants

1. No customer funds move without a ledger journal and an authenticated,
   authorized request.
2. No single person can withdraw customer funds.
3. No private key exists in plaintext outside an HSM or approved MPC boundary.
4. No service trusts an identity asserted by another service without mTLS.
5. No admin action bypasses audit logging.
6. No secret is stored in source, images, or logs.
7. No production database is reachable from the internet.
8. No deploy bypasses CI, image signing, or policy checks.
9. No unknown or unpatched critical vulnerability is deployed.
10. No access is granted without a documented business need and expiry.
