# Data Classification

**Status:** Approved for B01
**Owner:** Security Engineering
**Last updated:** 2026-10-19

Every dataset, log field, metric label, and contract field must be assigned one
of the classes below. Classification determines storage, access, retention, and
logging rules.

---

## 1. Classes

| Class | Definition | Examples | Handling |
|-------|------------|----------|----------|
| **Public** | Safe to disclose to anyone | Published instrument list, status page, public trades | No restriction; integrity still required |
| **Internal** | Non-public operational data | Service names, deploy metadata, aggregate latency | Authenticated staff only; no public buckets |
| **Confidential** | Sensitive business or customer data | Order history, positions, balances, KYC status | Encrypted at rest and in transit; least privilege; audited access |
| **Restricted** | Compromise causes severe financial or legal harm | Private keys, seed material, API secrets, full KYC documents, ledger journals | HSM/MPC or secret manager; dual control; no logs; strict retention |

## 2. Rules by class

### Public

- May be cached at the edge.
- Must still be integrity-checked and versioned.
- Display-only values must be labelled as synthetic when applicable.

### Internal

- Stored in private networks only.
- May appear in aggregate metrics without customer identifiers.
- Retained according to operational need.

### Confidential

- Encrypted in transit (TLS 1.2+) and at rest.
- Access through authenticated, authorized, audited paths.
- Never placed in URL paths, query strings, or metric labels.
- Logs may contain identifiers only when necessary and approved.

### Restricted

- Never logged, traced, or emitted in metrics.
- Never stored in plaintext outside the approved custody or secret system.
- Access requires break-glass approval and produces an audit record.
- Withdrawal signing requires quorum and separation of duties.

## 3. Field-level guidance

| Field | Class | Logging rule |
|-------|-------|--------------|
| `request_id` | Internal | May log |
| `account_id` | Confidential | Log only in audit context |
| `order_id` | Confidential | Log only in audit context |
| Balance, position, PnL | Confidential | Never log values |
| API key secret | Restricted | Never log; show once at creation |
| Private key, seed phrase | Restricted | Never log, never transmit to application |
| Raw venue payload | Internal | May store; redact venue credentials |
| Display configuration | Internal | Audit changes; expose synthetic labels |
| Audit event | Confidential | Append-only, restricted access |

## 4. Data flows

1. Raw venue data is ingested into an append-only raw store. It is Internal.
2. Canonical market data derived from raw data is Internal and used by matching
   and risk. It must not contain display adjustments.
3. Display configuration is Internal and presentation-only. It must never be
   joined into financial calculations.
4. Customer financial data is Confidential and lives in transactional stores.
5. Key material is Restricted and stays inside the custody boundary.

## 5. Retention

| Data | Minimum | Maximum | Notes |
|------|---------|---------|-------|
| Raw market data | 90 days hot | 7 years archive | Immutable |
| Audit events | 7 years | Per regulation | WORM storage |
| Ledger journals | 7 years | Per regulation | Never mutate |
| Application logs | 30 days | 1 year | Redacted |
| Traces | 7 days | 30 days for sampled errors | No Restricted fields |
| Metrics | 90 days | 2 years | Aggregated only |

## 6. Enforcement checklist

- [ ] New field has an assigned class.
- [ ] Logs and traces contain no Restricted values.
- [ ] Metrics labels contain no customer identifiers.
- [ ] Storage encryption and access controls match the class.
- [ ] Retention is configured.
- [ ] Display-only data is marked and separated from financial paths.
