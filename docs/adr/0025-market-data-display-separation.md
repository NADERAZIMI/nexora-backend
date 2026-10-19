# ADR-025 — Separate raw market data, canonical pricing, and display configuration

- **Status:** Accepted
- **Date:** 2026-10-19
- **Owners:** Architecture, Risk, Security
- **Supersedes:** None
- **Superseded by:** None

## Context

NEXORA will ingest market data from venues such as Bybit and MEXC and will
offer an admin-controlled Market Pricing & Display Configuration capability.
Mixing raw venue data, NEXORA pricing, and presentation-only adjustments would
create audit, risk, and regulatory problems. A display change must never alter
matching, settlement, risk, funding, or liquidation.

## Decision

1. Raw venue data is preserved append-only and unmodified, with the original
   payload, hash, receive time, and ingest ID.
2. Canonical data is deterministically derived from raw data and is the only
   market-data input to financial systems.
3. Display configuration is a separate presentation layer in
   `nexora.display.v1`. It MUST NOT be imported by matching, ledger,
   settlement, risk, funding, liquidation, or ADL modules.
4. Every display metric carries a machine-readable source and synthetic label.
   Synthetic volume MUST NOT be presented as executed trading volume.
5. Display configuration changes require versioning, an effective timestamp,
   dual approval, and an audit record.

## Consequences

### Positive

- Clear audit trail from raw venue data to canonical data to presentation.
- Risk and settlement cannot be influenced by display adjustments.
- Public APIs can disclose the provenance of every displayed metric.

### Negative

- Additional storage and processing layer.
- Display features require governance and review.
- Two pricing concepts (canonical and NEXORA quote) must be explained to users.

### Risks

- Future engineers may bypass the boundary. Enforce with package layout, import
  tests, CODEOWNERS, and review.
- Synthetic volume may still mislead if client labels are unclear. Require
  explicit labels in API and UI contracts.

## Alternatives considered

| Alternative | Reason rejected |
|-------------|-----------------|
| Store display adjustments in raw market data | Destroys provenance and auditability |
| Let display configuration feed risk | Makes risk and liquidation manipulable |
| Single price field with a "display" flag | Easy to misuse; no structural separation |
| No display configuration capability | Rejected by product requirement; reserve it safely instead |

## Enforcement

- `nexora.display.v1` is a separate protobuf package.
- Financial crates must not depend on display contracts; an architecture test is
  added in B02.
- Admin changes require dual approval and audit.
- API schemas expose `source` and `is_synthetic`.

## References

- [`../engineering/market-data-and-display-boundary.md`](../engineering/market-data-and-display-boundary.md)
- [`../architecture/10-funding-liquidation-insurance-adl.md`](../architecture/10-funding-liquidation-insurance-adl.md)
- [`../architecture/12-api-rest-and-websocket.md`](../architecture/12-api-rest-and-websocket.md)
