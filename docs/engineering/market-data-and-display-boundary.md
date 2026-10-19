# Market Data and Display Boundary

**Status:** Approved for B01
**Applies to:** Venue adapters, market data, pricing, admin configuration,
matching, risk, ledger, settlement, API, and WebSocket channels

This document defines the mandatory separation between raw external market
data, NEXORA's canonical pricing, and presentation-only display metrics.

---

## 1. Three layers

```text
Layer 1: Raw venue data (immutable)
  Bybit / MEXC / other -> venue adapter -> raw store + raw topics

Layer 2: Canonical data (authoritative for NEXORA)
  raw -> validation -> aggregation -> canonical price/book/funding inputs
  Used by matching, risk, funding, liquidation, settlement.

Layer 3: Display and presentation (non-authoritative)
  canonical + admin policy -> NEXORA quote + display metrics
  Used only by public API, WebSocket, and UI.
```

## 2. Layer 1 — Raw venue data

- Preserved exactly as received, including the original payload.
- Append-only. Never edited, normalised in place, or overwritten.
- Stored with venue, venue symbol, venue sequence, event time, receive time,
  payload hash, and ingest ID.
- Venue credentials are never stored in the payload store.
- Contracts: `contracts/proto/nexora/marketdata/v1/raw_market_data.proto`.
- Adapter interface:
  `contracts/proto/nexora/integrations/v1/venue_adapter.proto`.

## 3. Layer 2 — Canonical data

- Derived deterministically from raw data by versioned code.
- Used for charts, reference prices, risk, funding, liquidation, and any
  financial decision.
- Changes require a replay test proving that historical outputs are stable or
  that the change is explicitly versioned.
- Display configuration must never be an input to this layer.

## 4. Layer 3 — Display and presentation

The future **Market Pricing & Display Configuration** capability allows
administrators to configure:

- A NEXORA quote derived from raw or canonical data using a documented
  methodology (for example midpoint, last, or index with bounded offset).
- Display-only metrics such as synthetic volume, always labelled as synthetic.
- Per-symbol or per-region presentation settings.

Hard rules:

1. Display values are always labelled. A synthetic volume is never presented as
   executed trading volume.
2. Display values are never inputs to matching, ledger, settlement, risk,
   funding, liquidation, ADL, or reconciliation.
3. Display configuration changes require dual approval, versioning, an effective
   timestamp, and an audit record.
4. Risk and settlement services must not import `nexora.display.v1`.
5. Public APIs must expose a machine-readable `source` and `is_synthetic` flag
   for any display metric.
6. No display feature may alter executed prices, fills, fees, balances,
   positions, margin, or liquidation prices.

## 5. Enforcement

| Control | Mechanism | Phase |
|---------|-----------|-------|
| Package separation | `nexora.display.v1` distinct from `nexora.marketdata.v1` | B01 |
| Import ban | Architecture test rejects display imports in financial crates | B02+ |
| Review | CODEOWNERS approval for display and risk changes | B01 |
| Audit | Append-only config audit with actor and version | Admin phase |
| Replay | Canonical outputs reproducible from raw data | B12 |
| Labelling | API schema requires source flags | B10 |

## 6. Non-goals

- Simulating venue volume or prices as if they were executed.
- Using display adjustments to meet risk, funding, or liquidation targets.
- Modifying raw venue records to match a presentation view.
- Hiding fees, spreads, or execution quality from customers.

## 7. Open decisions

1. Exact NEXORA quote methodologies and bounds.
2. Regional display policy and regulatory review.
3. Whether synthetic volume is permitted at all; if so, the UI label wording.
4. Retention for display configuration audit records.
