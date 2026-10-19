# 15 — Precision, Atomicity and Idempotency

---

## 1. Financial precision

### 1.1 Representation

| Value | Type | Unit | Storage |
|-------|------|------|---------|
| Price | `i64` / `BIGINT` | ticks | Integer |
| Quantity | `i64` / `BIGINT` | lots | Integer |
| Amount | `i128` / `NUMERIC(38,0)` | atomic units | Integer |
| Rate | `i32` | basis points | Integer |
| Multiplier | `i64` numerator/denominator | exact rational | Integer pair |
| Timestamp | `i64` | nanoseconds | Integer |

Atomic units:

```
1 USDT = 1,000,000 atomic units        (6 decimals)
1 BTC  = 100,000,000 atomic units      (8 decimals)
1 ETH  = 1,000,000,000,000,000,000     (18 decimals)
```

The asset's `decimals` defines the scale. No amount is ever interpreted without
its asset.

### 1.2 Prohibited types

- `f32`, `f64`, `REAL`, `DOUBLE PRECISION`, `FLOAT` on any financial path.
- JSON numbers for money at the public API.
- Implicit unit conversions.
- `as` casts that can truncate; use `try_from` and explicit rounding.
- Unchecked arithmetic in release builds.

Rust lint configuration denies float arithmetic and lossy casts in the
`nexora-types`, `nexora-ledger`, `nexora-margin`, and `nexora-book` crates.

### 1.3 Ranges and overflow

| Computation | Intermediate | Final check |
|-------------|--------------|-------------|
| `price * qty` | `i128` | Must fit `i128`; reject otherwise |
| `amount * bps` | `i128` | Divide by 10,000 with explicit rounding |
| Sum of balances | `i128` | Must fit `NUMERIC(38,0)` |
| Position notional | `i128` | Max 1e30 atomic units |
| Funding payment | `i128` | Must fit asset balance range |

Rules:

- Use `checked_add`, `checked_sub`, `checked_mul`, `checked_div`.
- On overflow, reject the command with `VAL_OVERFLOW` and alert; never wrap.
- Validate instrument limits so that `max_qty * max_price` fits in `i128`.
- Database `NUMERIC(38,0)` rejects out-of-range values as a last defense.

### 1.4 Unit conversions

Price ticks to quote value:

```
quote_atomic = price_ticks * tick_size_atomic * qty_base_atomic / base_scale
```

To avoid rounding, perform the multiplication before division and use exact
rational arithmetic:

```
quote = (price_ticks * tick_size_num * qty_lots * lot_size_num)
        / (tick_size_den * lot_size_den * 10^quote_decimals)
```

If the result is not integral, apply the rounding rule for the operation
(section 3). Conversion is centralized in `nexora-types::convert` and property
tested.

### 1.5 Decimal parsing at the edge

- Accept `^-?[0-9]+(\.[0-9]+)?$`.
- Reject exponents, `NaN`, `Infinity`, leading `+`, and whitespace.
- Reject more decimal places than the asset/instrument allows; do not silently
  round user input.
- Reject values that exceed the maximum for the field.
- Normalize to atomic units using string parsing, not floating point.
- Store the original string in the request log for audit.

---

## 2. Rounding rules

Rounding is explicit per operation. The default is **round half away from zero**
unless stated otherwise.

| Operation | Rule | Rationale |
|-----------|------|-----------|
| Price from average fill | Nearest tick, half-up | Display consistency |
| Quantity from close-position | Down to lot | Never exceed position |
| Fee (maker/taker) | Up to atomic unit | Protects exchange revenue |
| Withdrawal network fee | Up | Covers network cost |
| Deposit credit | Down | Never credit more than received |
| Withdrawal debit | Up | Never debit less than sent |
| Realized PnL | Nearest atomic unit, half-up | Symmetric between parties |
| Unrealized PnL (display) | Nearest atomic unit | Informational |
| Funding payment | Nearest atomic unit, half-up | Symmetric; residue to insurance |
| Maintenance margin | Up | Conservative |
| Initial margin | Up | Conservative |
| Liquidation price (long) | Up (earlier liquidation) | Conservative |
| Liquidation price (short) | Down (earlier liquidation) | Conservative |
| Liquidation fee | Up | Protects insurance fund |
| Interest/funding rate | Nearest bp | Published precision |

### 2.1 Residue accounts

When rounding creates a residual:

- Fees: residual accrues to the fee account.
- Funding: residual goes to the insurance fund.
- Conversions: residual goes to the rounding account.
- Deposits: residual stays in the custody/rounding account.

Residue is tracked and reported; it must never disappear.

### 2.2 Example: fee rounding

Notional 60,000.10 USDT, taker fee 5 bps:

```
fee = 60,000.10 * 5 / 10,000 = 30.00005 USDT
```

Rounded up to 6 decimals: `30.000050` (already exact). If the result were
`30.0000501`, it would round to `30.000051`.

### 2.3 Example: funding residue

Three positions with payments `+1.0000005`, `−1.0000005`, `+0.0000002`:

- Round half-up: `+1.000001`, `−1.000001`, `+0.000000`.
- Residue: `−0.0000002` → insurance fund adjustment `+0.0000002` so the batch
  sums to zero.

---

## 3. Atomicity

### 3.1 Local atomicity

A financial mutation is one database transaction:

- Journal batch + entries.
- Balance update.
- Hold update.
- Position update.
- Margin cache update.
- Outbox record.

Either all commit or none. There is no partial state visible to readers.

### 3.2 Matching atomicity

A command and all resulting events (order accepted, trades, cancels) are written
as one WAL batch. The batch is replicated and committed as a unit. A crash
either has the entire batch or none of it.

### 3.3 Cross-service atomicity

There is no distributed transaction. Consistency is achieved by:

1. **Clearing-account legs:** each participant's settlement is independently
   balanced against a system clearing account.
2. **Idempotent consumers:** each leg is applied exactly once.
3. **Outbox:** the event is published only if the state change commits.
4. **Sagas:** failures trigger compensating actions (release hold, cancel order).
5. **Reconciliation:** detects and repairs divergence.

Acceptable intermediate states:

| State | Duration | Bound |
|-------|----------|-------|
| Order accepted, not yet settled | < 250 ms p99 | 5 s alert |
| Buyer leg settled, seller leg pending | < 250 ms p99 | Clearing imbalance alert |
| Hold created, order not yet committed | < 2 s decision / 30 s safety TTL | Release or bind |
| Withdrawal broadcast, ledger not settled | Until confirmation | Hold retained |

Every intermediate state is observable and bounded. Unbounded intermediate
states are incidents.

### 3.4 Settlement transaction example

```sql
BEGIN;
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '5s';

SELECT account_id, posted, held
FROM ledger.accounts
WHERE account_id IN ($user, $clearing)
ORDER BY account_id
FOR UPDATE;

-- Verify hold
SELECT remaining FROM ledger.holds
WHERE hold_id = $hold_id AND status = 'active'
FOR UPDATE;

INSERT INTO ledger.journal_batches (...) VALUES (...);
INSERT INTO ledger.journal_entries (...) VALUES (...), (...);

UPDATE ledger.accounts
SET posted = posted + $delta,
    held = held - $consumed,
    version = version + 1,
    updated_at = now()
WHERE account_id = $account_id;

UPDATE ledger.positions ...;
UPDATE ledger.margin_accounts ...;
INSERT INTO ops.outbox (...) VALUES (...);

COMMIT;
```

No network calls, file I/O, or user code inside the transaction.

---

## 4. Idempotency

### 4.1 Why

Clients retry on timeouts; services retry on transient failures; event buses
deliver at least once. Every financial effect must be safe to attempt multiple
times.

### 4.2 Key matrix

| Operation | Idempotency key | Scope | Retention |
|-----------|-----------------|-------|-----------|
| Place order | `(account_id, client_order_id)` | Account | 7 days |
| Cancel order | `order_id` + terminal state | Order | Forever |
| Authorize | `authorization_key` | Account | 7 days |
| Create hold | `(purpose, reference_id)` | Account | Life of hold |
| Settle trade | `(trade_id, participant_side)` | Global per trade | Forever |
| Apply funding | `(funding_id, position_id)` | Global | Forever |
| Deposit credit | `(network, txid, vout)` | Global | Forever |
| Withdrawal | `withdrawal_id` / `X-Idempotency-Key` | Account | 30 days |
| Transfer | `X-Idempotency-Key` | Account | 7 days |
| Admin adjustment | `adjustment_id` | Global | Forever |
| Event consumption | `(consumer, event_id)` | Consumer | 30 days |

### 4.3 Idempotency record

```sql
CREATE TABLE trading.idempotency_keys (
    account_id    uuid NOT NULL,
    key           text NOT NULL,
    request_hash  bytea NOT NULL,
    status        text NOT NULL,   -- in_progress | succeeded | failed
    response      jsonb,
    created_at    timestamptz NOT NULL,
    expires_at    timestamptz NOT NULL,
    PRIMARY KEY (account_id, key)
);
```

Algorithm:

```
INSERT ... ON CONFLICT DO NOTHING
if inserted:
    execute
    update status + response
else:
    if request_hash != stored: conflict
    if status == succeeded: return stored response
    if status == failed: return stored error
    if status == in_progress: return 409 SYS_IN_PROGRESS
```

### 4.4 Exactly-once effects

At-least-once delivery + idempotent application = effectively-once.

Consumer pattern:

```
BEGIN;
INSERT INTO ops.inbox (consumer, topic, partition, offset, event_id)
VALUES (...) ON CONFLICT DO NOTHING;
if inserted:
    apply effect
    update aggregate
    INSERT INTO ops.outbox (...)
COMMIT;
```

The inbox insert and the effect are in the same transaction. If the transaction
rolls back, the event is redelivered. If it commits, a redelivery is ignored.

### 4.5 Deterministic IDs

Use deterministic IDs where possible:

- `funding_id = symbol + ":" + interval_start_unix`
- `settlement_id = trade_id + ":" + participant_side`
- `deposit_id = network + ":" + txid + ":" + vout`
- `authorization_id` is random but stored on the order

Deterministic IDs make deduplication trivial and debugging easier.

### 4.6 Timeout semantics

A timeout is **not** a failure. The operation may have committed.

Rules:

- The client receives a request ID and, for orders, a client order ID.
- The client queries before retrying.
- The server returns the committed result for the same idempotency key.
- Internal callers use the same rule; they never assume a timeout means rollback.

---

## 5. Concurrency rules

### 5.1 Single writer

- One actor per symbol shard.
- One leader per account shard.
- No concurrent mutation of the same aggregate.

### 5.2 Database locking

- `SELECT ... FOR UPDATE` on the account row for hold changes.
- Lock order: ascending `account_id`.
- Lock timeout 2 s; transaction timeout 5 s.
- No lock held across a network call.
- Batch operations lock accounts in sorted order.

### 5.3 Optimistic concurrency

- Aggregates carry a `version`.
- Updates use `WHERE version = $expected`.
- A mismatch returns `SYS_CONFLICT` and the caller retries with fresh state.
- Used for admin config and low-contention records, not for balance updates.

### 5.4 Hot accounts

Market makers and high-frequency accounts can concentrate load. Mitigations:

- Per-account shard assignment spreads accounts.
- In-memory actor queues absorb bursts.
- Batch settlement per account.
- Rate limits per account.
- Dedicated shards for institutional accounts if needed.

### 5.5 Deadlock prevention

- Consistent lock ordering.
- Short transactions.
- No user-defined callbacks inside transactions.
- Retry on deadlock (SQLSTATE 40P01) with jitter, up to 3 times.

### 5.6 Lost update prevention

- Balance updates use `posted = posted + delta` under a row lock, not
  read-modify-write in application memory.
- Version checks for configuration.
- Idempotency constraints for duplicates.

---

## 6. Failure matrix

| Failure | Effect | Recovery |
|---------|--------|----------|
| Client retries after timeout | Duplicate request | Idempotency returns original result |
| Service crashes after commit, before response | Client sees timeout | Query by key; operation is durable |
| Service crashes before commit | No effect | Client retries |
| Event delivered twice | Duplicate effect | Inbox dedupe |
| Event delivered out of order | Stale update | Per-key ordering + version check |
| Settlement leg fails | Clearing imbalance | Retry; alert if > 5 s |
| Hold leak | Funds reserved but no order | TTL + reconciliation |
| Duplicate settlement | Double debit | Unique `(trade_id, side)` rejects |
| Withdrawal rebroadcast | Double spend risk | Signer dedupe by tx hash + withdrawal ID |
| Partial batch write | Impossible | Single transaction |
| WAL torn write | Corrupt tail | CRC rejects; truncate to last valid record |
| Database failover mid-transaction | Rollback | Client retries idempotently |

---

## 7. Testing

### 7.1 Property tests

- Addition and subtraction of amounts are associative and commutative where
  expected.
- Conversion round-trips within one atomic unit.
- Fee rounding never undercharges.
- Funding batch sums to zero after residue.
- Journal batches always balance.
- Position quantity is conserved across fills.
- Idempotent replay produces identical state.

### 7.2 Model-based tests

Compare the production ledger and margin implementations against a simple
reference model on randomized sequences of deposits, orders, fills, funding,
and withdrawals. Any divergence fails the test.

### 7.3 Fuzz tests

- Decimal parser.
- Journal batch builder.
- Order validation.
- Margin formulas with extreme values.
- WAL reader with truncated/corrupt records.

### 7.4 Integration tests

- Place order → settle → reconcile, with real Postgres and Redpanda.
- Inject duplicate events, out-of-order events, and consumer restarts.
- Kill the service between commit and response; verify client recovery.
- Simulate database failover mid-transaction.

### 7.5 Acceptance criteria

- Zero duplicate settlements in a 24-hour soak with 1% duplicate injection.
- Zero ledger imbalance across 10 million randomized operations.
- All rounding rules verified by table-driven tests.
- Recovery from a kill -9 at every step of the order flow.
- No floating point in financial crates (CI lint).
