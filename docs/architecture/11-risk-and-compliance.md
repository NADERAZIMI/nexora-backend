# 11 — Risk and Compliance Controls

---

## 1. Risk framework

NEXORA separates risk into five layers:

| Layer | Question | Owner |
|-------|----------|-------|
| Pre-trade | Is this order allowed and funded? | Trading core + account core |
| Real-time | Is the account or market approaching a limit? | Risk engine |
| Post-trade | Are exposures, concentrations, and PnL within policy? | Risk + finance |
| Market integrity | Is behavior manipulative or abusive? | Surveillance |
| Compliance | Is the customer and transaction legal? | Compliance |

Each layer has explicit limits, owners, thresholds, and escalation paths.
Limits are versioned configuration, never hard-coded.

---

## 2. Pre-trade risk

### 2.1 Order-level checks

| Check | Default | Reject code |
|-------|---------|-------------|
| Max order notional | Tier-based | `RSK_ORDER_LIMIT` |
| Max order quantity | Instrument | `VAL_QTY_RANGE` |
| Price band | ±5% of mark | `MKT_PRICE_BAND` |
| Tick/lot alignment | Instrument | `VAL_*` |
| Max open orders | 200/symbol, 1,000/account | `RSK_MAX_OPEN_ORDERS` |
| Order rate | 50/s | `RATE_LIMIT_EXCEEDED` |
| Cancel ratio | 100:1 rolling 1 min | `RATE_CANCEL_RATIO` |
| Leverage | Tier maximum | `RSK_LEVERAGE_EXCEEDED` |
| Position limit | Risk tier | `RSK_POSITION_LIMIT` |
| Margin sufficiency | Initial margin + fees | `RSK_INSUFFICIENT_MARGIN` |
| Account status | Active | `ACC_FROZEN` |
| Symbol status | Active | `MKT_HALTED` |
| Self-trade | Policy | Matcher |
| Restricted asset | Jurisdiction | `ACC_RESTRICTED` |

### 2.2 Account-level checks

- KYC level sufficient for the product.
- No sanctions match.
- No open compliance case requiring restriction.
- Withdrawal-only or frozen status respected.
- API key scopes and IP allowlist.
- Country/jurisdiction product eligibility.
- Sub-account limits (if applicable).

### 2.3 Symbol-level checks

- Instrument active, not reduce-only or halted.
- Open interest below the symbol cap.
- Insurance fund above the emergency floor (otherwise reduce-only).
- Oracle healthy.
- Settlement lag within tolerance.
- Matching shard healthy.

### 2.4 Failure behavior

Pre-trade checks fail **closed**. A timeout or unknown state results in
rejection, not acceptance. The error is retryable only when the condition is
transient (e.g. backpressure), and the client receives `retry_after_ms`.

---

## 3. Real-time risk

### 3.1 Margin monitoring

- Recompute equity, maintenance margin, and margin ratio on every mark update.
- Bucket accounts by margin ratio: healthy (<50%), watch (50–80%), danger
  (80–100%), liquidatable (≥100%).
- Alert at 80% for large accounts.
- Auto-liquidation at 100%, with a small buffer to account for latency.

### 3.2 Exposure monitoring

| Metric | Granularity | Alert |
|--------|-------------|-------|
| Net open interest | Symbol | > 80% of cap |
| Long/short imbalance | Symbol | > 70/30 |
| Top-10 account concentration | Symbol | > 40% of OI |
| Account notional | Account | > risk tier |
| Cross-collateral usage | Account | > 90% |
| Insurance fund coverage | Platform | < 1.0 |
| Settlement lag | Shard | > 1 s |
| Liquidation queue | Platform | > 10 cases |

### 3.3 Stress testing

Run every 5 minutes and on demand:

- 10%, 20%, 30% adverse price moves per major asset.
- Correlated moves across BTC/ETH and altcoins.
- Liquidity shock: book depth halved.
- Funding shock: rate at cap for 3 intervals.
- Oracle failure: mark frozen for 10 minutes.
- Insurance fund depletion scenarios.

Outputs:

- Expected deficits per symbol.
- Number of accounts liquidatable.
- Insurance fund coverage.
- ADL volume required.
- Recommended leverage reductions.

Stress results are stored and reviewed daily by the risk team.

---

## 4. Post-trade risk

- Daily PnL attribution by symbol, desk, and account tier.
- Position and collateral reconciliation.
- Limit utilization reports.
- Concentration and correlated exposure reports.
- Margin model backtesting: compare predicted liquidation prices with realized
  outcomes; tune MMR buffers.
- Fee and rebate reconciliation.
- Insurance fund PnL and coverage.

Reports are generated from ClickHouse and reconciled to the ledger. Any
discrepancy above tolerance is a finding.

---

## 5. Market integrity and surveillance

### 5.1 Abuse patterns

| Pattern | Detection |
|---------|-----------|
| Spoofing | Large orders canceled before execution, repeated at the touch |
| Layering | Multiple orders on one side to create false depth |
| Wash trading | Self-trades or colluding accounts offsetting positions |
| Marking the close | Concentrated trades near funding/settlement timestamps |
| Momentum ignition | Aggressive orders followed by rapid reversal |
| Quote stuffing | Extreme order/cancel rates |
| Cross-account netting | Related accounts taking opposite sides |
| Front-running | Orders ahead of large known client flow |
| Oracle manipulation | Trades on external venues to move the index |

### 5.2 Detection inputs

- Full order and trade stream with account pseudonyms.
- Cancel-to-fill ratios.
- Order lifetime distributions.
- Account linkage graph (device, IP, KYC, funding source).
- Book depth and imbalance time series.
- External venue prices.

### 5.3 Alerts and cases

- Rules produce alerts with severity and evidence.
- Alerts are triaged by surveillance analysts.
- Cases can freeze accounts, restrict products, or refer to compliance.
- All evidence is retained and access-controlled.
- False-positive rates are reviewed monthly.

### 5.4 Market-maker conduct

- Market makers have obligations (quote presence, spread, size) defined in
  agreements.
- They use the same matching engine and APIs; no privileged order path.
- Rebates are configured in the fee schedule and audited.
- STP exemptions require approval and monitoring.

---

## 6. Circuit breakers

| Breaker | Trigger | Action | Reset |
|---------|---------|--------|-------|
| Price band | Order outside band | Reject order | Automatic |
| Volatility auction | > X% move in Y seconds | Freeze book, run auction | Automatic after window |
| Symbol halt | Oracle stale, invariant failure, manual | No new orders; cancels allowed | Manual |
| Reduce-only | Insurance fund below floor, extreme volatility | Only risk-reducing orders | Manual/automatic |
| Platform halt | Ledger mismatch, settlement lag, security incident | No trading; withdrawals paused | Manual, dual approval |
| Withdrawal pause | Custody reconciliation failure, chain incident | No withdrawals | Manual |
| Leverage reduction | Stress test breach | Lower max leverage for new positions | Risk committee |

All breaker transitions are events with actor, reason, and timestamp. Automatic
resets are allowed only for price bands and auctions; financial integrity
breakers require human review.

---

## 7. Compliance

### 7.1 KYC

| Level | Requirements | Permissions |
|-------|--------------|-------------|
| 0 | Email/phone | View markets only |
| 1 | Government ID + liveness | Deposit, spot trade, limited withdrawal |
| 2 | Proof of address + source of funds | Futures, higher limits |
| 3 | Enhanced due diligence | Institutional limits |

- KYC data is encrypted and access-logged.
- Providers are integrated through an adapter interface.
- Re-verification is triggered by risk signals or periodic review.
- Sanctions/PEP screening runs at onboarding and continuously.

### 7.2 AML

- Transaction monitoring rules on deposits, withdrawals, trades, and transfers.
- Velocity and structuring detection.
- Source-of-funds checks above thresholds.
- Suspicious activity reports (SAR) workflow with case management.
- Record retention per jurisdiction (minimum 5–7 years).
- Travel rule data attached to transfers where required.

### 7.3 Sanctions

- Screen names, addresses, and counterparties against lists (OFAC, EU, UN,
  local).
- Block deposits from sanctioned addresses.
- Freeze accounts on match, with compliance review.
- Chain analytics provider for address risk scoring.

### 7.4 Jurisdictional controls

- Product availability by country.
- IP geofencing for restricted regions.
- Withdrawal restrictions for users in restricted jurisdictions.
- Tax reporting exports where required.
- Data residency options for regulated regions.

### 7.5 Record keeping

- Financial records: 7 years minimum.
- KYC records: 5 years after account closure (or longer per jurisdiction).
- Audit logs: 7 years, WORM.
- Communications: per policy, with legal hold support.

---

## 8. Fraud and account security risk

| Risk | Control |
|------|---------|
| Credential stuffing | Rate limits, breached-password checks, device fingerprinting |
| Account takeover | 2FA, withdrawal address whitelist, session anomaly detection |
| API key leakage | Scoped keys, IP allowlists, immediate revocation, usage alerts |
| Phishing | Domain monitoring, user education, signed notifications |
| Insider abuse | Least privilege, four-eyes, audit, session recording |
| Withdrawal fraud | Velocity limits, AML screening, manual review above threshold |
| SIM swap | TOTP/WebAuthn preferred; SMS only as fallback with delays |
| Social engineering | Support scripts, callback verification, no password disclosure |

---

## 9. Risk governance

### 9.1 Committees and roles

| Role | Responsibility |
|------|----------------|
| Risk committee | Margin tiers, leverage, insurance fund policy |
| Compliance officer | KYC/AML, sanctions, regulatory reporting |
| CISO | Security strategy, incident response |
| Treasury | Custody, liquidity, fund management |
| Incident commander | Severity-1 coordination |
| Model risk owner | Margin/liquidation model validation |

### 9.2 Change control

- Margin, leverage, fee, and funding parameter changes require:
  1. Written proposal with impact analysis.
  2. Model validation and backtest.
  3. Risk committee approval.
  4. ADR and public notice.
  5. Canary deployment with monitoring.
  6. Post-change review.

### 9.3 Limit review

- Limits are reviewed quarterly and after stress events.
- Utilization and breach data inform adjustments.
- Emergency changes are allowed with post-hoc approval within 24 hours.

---

## 10. Incident severity

| Severity | Definition | Response |
|----------|------------|----------|
| SEV-1 | Ledger imbalance, fund loss, unauthorized withdrawal, trading on corrupt state | Immediate halt, incident commander, regulator notification assessment |
| SEV-2 | Settlement lag > 5 min, oracle failure, liquidation backlog, partial outage | Trading restrictions, on-call bridge |
| SEV-3 | Degraded performance, non-critical service down | Standard on-call |
| SEV-4 | Minor bug, cosmetic | Normal backlog |

SEV-1 and SEV-2 require a written post-incident review within 5 business days,
with corrective actions tracked to completion.

---

## 11. Risk metrics and reporting

Daily risk report:

- Open interest and volume by symbol.
- Long/short imbalance.
- Top exposures and concentration.
- Accounts in watch/danger/liquidatable buckets.
- Liquidations and ADL in the last 24 hours.
- Insurance fund balance and coverage.
- Margin model backtest results.
- Limit breaches and overrides.
- Oracle uptime and divergence.
- Settlement lag percentiles.

Monthly:

- Stress test summary.
- Parameter changes.
- Surveillance alerts and case outcomes.
- Compliance metrics (KYC approval rate, SARs, sanctions hits).
- Incident summary.

Reports are generated from immutable data and signed by the risk owner.

---

## 12. Control testing

| Control | Test | Frequency |
|---------|------|-----------|
| Pre-trade margin | Property tests + integration | Every release |
| Liquidation | Scenario replay + chaos | Every release |
| Insurance fund | Ledger reconciliation | Daily |
| ADL | Deterministic ranking tests | Every release |
| Oracle staleness | Fault injection | Weekly |
| Circuit breakers | Game day | Quarterly |
| Withdrawal limits | Integration + manual review | Every release |
| Sanctions screening | Synthetic list test | Monthly |
| Access control | Quarterly access review | Quarterly |
| Backup restore | Full restore drill | Quarterly |

Evidence is retained for auditors.
