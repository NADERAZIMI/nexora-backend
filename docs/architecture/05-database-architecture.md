# 05 — Database Architecture

---

## 1. Principles

1. **One writer per dataset.** Each table has exactly one service allowed to
   write it. Read replicas and analytics are projections.
2. **The journal is the source of truth.** `ledger.journal_entry` is append-only
   and authoritative. `ledger.account` is a materialized balance projection
   updated in the same transaction.
3. **Money is integer.** Amounts use `NUMERIC(38,0)` in atomic units. Prices and
   quantities use `BIGINT` ticks/lots. No `FLOAT`, `REAL`, or `DOUBLE PRECISION`
   in financial schemas.
4. **No cross-database foreign keys.** Logical references are validated by the
   owning service. FKs are used only inside a database where they do not cross a
   partition boundary.
5. **Append-only by default.** Orders, trades, journal entries, and audit
   records are never updated in place except for lifecycle columns explicitly
   listed.
6. **Partition before growth.** High-volume tables are partitioned from day one;
   retrofitting partitions during an incident is not acceptable.
7. **Every table has an owner, retention policy, and backup policy.**

---

## 2. Database topology

| Database | Owner | Purpose | HA |
|----------|-------|---------|----|
| `nexora_identity` | identity-service | Users, credentials, sessions, API keys, KYC | Patroni, sync replica |
| `nexora_trading` | trading-core / back-office | Instruments, orders, trades, fills, fee schedules | Patroni, sync replica |
| `nexora_ledger` | account-core | Accounts, holds, journal, positions, funding | Patroni, sync replica, 4 physical × 4 logical shards |
| `nexora_wallet` | wallet-service | Assets, addresses, deposits, withdrawals, custody | Patroni, sync replica |
| `nexora_ops` | platform | Outbox, inbox, audit, config, reconciliation | Patroni, sync replica |
| `nexora_ch` (ClickHouse) | analytics | Trades, candles, funding, surveillance | ReplicatedMergeTree |

### 2.1 Ledger sharding

Accounts are assigned to a logical shard by consistent hashing:

```
shard = rendezvous_hash(owner_id, shard_count)   -- user_id for user accounts
```

User accounts are co-located by `user_id` so spot and futures wallets on the
same user share a shard and can be transferred atomically. System accounts are
assigned explicitly.

Each logical shard has its own schema (`ledger_00` … `ledger_15`). A logical
shard maps to one of four physical Patroni clusters (four logical shards per
cluster at launch). The mapping is stored in `ops.shard_map` and is versioned;
rebalancing is an explicit migration with dual writes and a cutover, not an
automatic operation.

Within a shard:

- All account mutations are serialized by the shard leader.
- Holds, balances, positions, and journal entries for an account live together.
- Cross-shard settlement uses the clearing-account pattern; no distributed
  transaction.

### 2.2 Read replicas

- Synchronous replica: failover only; not used for reads.
- Asynchronous replica: reporting and back-office reads; may lag.
- Analytics replica: logical replication to ClickHouse via the event pipeline,
  not direct replication.

Customer-facing reads that must reflect the latest write are served by the
leader or by a read barrier. Stale reads are labeled in the API.

---

## 3. Schema: identity

```sql
CREATE SCHEMA IF NOT EXISTS identity;

CREATE TABLE identity.users (
    user_id            uuid PRIMARY KEY,
    email_ciphertext   bytea NOT NULL,
    email_hash         bytea NOT NULL UNIQUE,
    phone_ciphertext   bytea,
    phone_hash         bytea UNIQUE,
    password_hash      text NOT NULL,          -- Argon2id
    status             text NOT NULL CHECK (status IN
                         ('pending','active','frozen','closed')),
    kyc_level          smallint NOT NULL DEFAULT 0,
    country_code       char(2),
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    version            bigint NOT NULL DEFAULT 0
);

CREATE TABLE identity.credentials (
    credential_id      uuid PRIMARY KEY,
    user_id            uuid NOT NULL REFERENCES identity.users(user_id),
    kind               text NOT NULL CHECK (kind IN
                         ('password','totp','webauthn','recovery_code')),
    secret_ciphertext  bytea NOT NULL,
    metadata           jsonb NOT NULL DEFAULT '{}',
    created_at         timestamptz NOT NULL DEFAULT now(),
    last_used_at       timestamptz,
    revoked_at         timestamptz
);

CREATE TABLE identity.sessions (
    session_id         uuid PRIMARY KEY,
    user_id            uuid NOT NULL REFERENCES identity.users(user_id),
    refresh_hash       bytea NOT NULL UNIQUE,
    device_fingerprint text,
    ip_hash            bytea,
    user_agent         text,
    created_at         timestamptz NOT NULL DEFAULT now(),
    last_seen_at       timestamptz NOT NULL DEFAULT now(),
    expires_at         timestamptz NOT NULL,
    revoked_at         timestamptz
);
CREATE INDEX sessions_user_active ON identity.sessions (user_id)
    WHERE revoked_at IS NULL;

CREATE TABLE identity.api_keys (
    key_id             uuid PRIMARY KEY,
    user_id            uuid NOT NULL REFERENCES identity.users(user_id),
    label              text NOT NULL,
    key_prefix         text NOT NULL UNIQUE,   -- first 8 chars, lookupable
    key_type           text NOT NULL CHECK (key_type IN ('hmac','ed25519')),
    secret_ciphertext  bytea,                  -- HMAC keys, KMS envelope encryption
    public_key         bytea,                  -- Ed25519 keys
    scopes             text[] NOT NULL,
    ip_allowlist       inet[],
    created_at         timestamptz NOT NULL DEFAULT now(),
    last_used_at       timestamptz,
    expires_at         timestamptz,
    revoked_at         timestamptz,
    CHECK (
        (key_type = 'hmac'    AND secret_ciphertext IS NOT NULL AND public_key IS NULL)
     OR (key_type = 'ed25519' AND public_key IS NOT NULL AND secret_ciphertext IS NULL)
    )
);
CREATE INDEX api_keys_user ON identity.api_keys (user_id)
    WHERE revoked_at IS NULL;

CREATE TABLE identity.kyc_cases (
    case_id            uuid PRIMARY KEY,
    user_id            uuid NOT NULL REFERENCES identity.users(user_id),
    provider           text NOT NULL,
    provider_ref       text,
    status             text NOT NULL CHECK (status IN
                         ('pending','approved','rejected','expired')),
    level              smallint,
    reason             text,
    submitted_at       timestamptz NOT NULL DEFAULT now(),
    decided_at         timestamptz,
    payload            jsonb NOT NULL DEFAULT '{}'
);
```

PII columns are encrypted with a KMS-backed envelope key. `*_hash` columns use
HMAC-SHA256 with a pepper for equality lookups without decryption.

---

## 4. Schema: trading

```sql
CREATE SCHEMA IF NOT EXISTS trading;

CREATE TABLE trading.instruments (
    symbol_id          integer PRIMARY KEY,
    symbol             text NOT NULL UNIQUE,
    kind               text NOT NULL CHECK (kind IN ('spot','perp')),
    base_asset_id      integer NOT NULL,
    quote_asset_id     integer NOT NULL,
    tick_size          bigint NOT NULL CHECK (tick_size > 0),
    lot_size           bigint NOT NULL CHECK (lot_size > 0),
    min_qty_lots       bigint NOT NULL,
    max_qty_lots       bigint NOT NULL,
    min_notional       numeric(38,0) NOT NULL,
    max_notional       numeric(38,0) NOT NULL,
    price_band_bps     integer NOT NULL,
    maker_fee_bps      integer NOT NULL,
    taker_fee_bps      integer NOT NULL,
    contract_spec      jsonb,                  -- perp only, validated by app
    status             text NOT NULL CHECK (status IN
                         ('active','reduce_only','halted','delisted')),
    config_version     bigint NOT NULL DEFAULT 1,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);
```

### 4.1 Orders

```sql
CREATE TABLE trading.orders (
    symbol_id          integer NOT NULL,
    order_id           bigint NOT NULL,
    account_id         uuid NOT NULL,
    client_order_id    text NOT NULL,
    side               text NOT NULL CHECK (side IN ('buy','sell')),
    order_type         text NOT NULL,
    time_in_force      text NOT NULL,
    price_ticks        bigint,
    stop_price_ticks   bigint,
    qty_lots           bigint NOT NULL CHECK (qty_lots > 0),
    filled_qty_lots    bigint NOT NULL DEFAULT 0,
    remaining_qty_lots bigint NOT NULL,
    avg_fill_price     bigint,
    status             text NOT NULL,
    position_side      text NOT NULL DEFAULT 'both',
    reduce_only        boolean NOT NULL DEFAULT false,
    post_only          boolean NOT NULL DEFAULT false,
    close_position     boolean NOT NULL DEFAULT false,
    authorization_id   uuid,
    created_seq        bigint NOT NULL,
    updated_seq        bigint NOT NULL,
    rejection_code     text,
    created_at         timestamptz NOT NULL,
    updated_at         timestamptz NOT NULL,
    expires_at         timestamptz,
    version            bigint NOT NULL DEFAULT 1,
    PRIMARY KEY (symbol_id, order_id),
    CHECK (filled_qty_lots + remaining_qty_lots = qty_lots),
    CHECK (status IN ('accepted','partially_filled','filled',
                      'canceled','rejected','expired','pending_trigger'))
) PARTITION BY HASH (symbol_id);

CREATE TABLE trading.orders_p0 PARTITION OF trading.orders
    FOR VALUES WITH (MODULUS 16, REMAINDER 0);
-- ... p1 through p15

CREATE INDEX orders_account_time ON trading.orders
    (account_id, created_at DESC);
CREATE INDEX orders_account_open ON trading.orders
    (account_id, symbol_id)
    WHERE status IN ('accepted','partially_filled','pending_trigger');
CREATE INDEX orders_client_id ON trading.orders
    (account_id, client_order_id);
```

The authoritative idempotency record lives in `trading.idempotency_keys`. A
unique index on `(account_id, client_order_id)` is intentionally not used on the
partitioned orders table: PostgreSQL requires the partition key in unique
constraints, and client IDs must be unique across symbols. The idempotency table
enforces that rule.

### 4.2 Order events

```sql
CREATE TABLE trading.order_events (
    symbol_id          integer NOT NULL,
    order_id           bigint NOT NULL,
    seq                bigint NOT NULL,
    event_type         text NOT NULL,
    payload            jsonb NOT NULL,
    occurred_at        timestamptz NOT NULL,
    PRIMARY KEY (symbol_id, order_id, seq, occurred_at)
) PARTITION BY RANGE (occurred_at);

CREATE TABLE trading.order_events_2026_10 PARTITION OF trading.order_events
    FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');
```

### 4.3 Trades and fills

```sql
CREATE TABLE trading.trades (
    symbol_id          integer NOT NULL,
    trade_id           bigint NOT NULL,
    price_ticks        bigint NOT NULL,
    qty_lots           bigint NOT NULL,
    buyer_order_id     bigint NOT NULL,
    seller_order_id    bigint NOT NULL,
    buyer_account_id   uuid NOT NULL,
    seller_account_id  uuid NOT NULL,
    taker_side         text NOT NULL,
    maker_fee          numeric(38,0) NOT NULL,
    taker_fee          numeric(38,0) NOT NULL,
    executed_at        timestamptz NOT NULL,
    match_seq          bigint NOT NULL,
    PRIMARY KEY (symbol_id, trade_id, executed_at)
) PARTITION BY RANGE (executed_at);

CREATE INDEX trades_symbol_time ON trading.trades
    (symbol_id, executed_at DESC);
CREATE INDEX trades_buyer ON trading.trades (buyer_account_id, executed_at DESC);
CREATE INDEX trades_seller ON trading.trades (seller_account_id, executed_at DESC);

CREATE TABLE trading.fills (
    symbol_id          integer NOT NULL,
    trade_id           bigint NOT NULL,
    fill_index         smallint NOT NULL,       -- 0 buyer, 1 seller
    order_id           bigint NOT NULL,
    account_id         uuid NOT NULL,
    side               text NOT NULL,
    price_ticks        bigint NOT NULL,
    qty_lots           bigint NOT NULL,
    fee                numeric(38,0) NOT NULL,
    fee_asset_id       integer NOT NULL,
    is_maker           boolean NOT NULL,
    executed_at        timestamptz NOT NULL,
    PRIMARY KEY (symbol_id, trade_id, fill_index, executed_at)
) PARTITION BY RANGE (executed_at);
```

### 4.4 Idempotency

```sql
CREATE TABLE trading.idempotency_keys (
    account_id         uuid NOT NULL,
    key                text NOT NULL,
    request_hash       bytea NOT NULL,
    status             text NOT NULL CHECK (status IN ('in_progress','succeeded','failed')),
    response           jsonb,
    created_at         timestamptz NOT NULL DEFAULT now(),
    expires_at         timestamptz NOT NULL,
    PRIMARY KEY (account_id, key)
) PARTITION BY HASH (account_id);
```

Rules:

- Insert with `ON CONFLICT DO NOTHING`; if inserted, execute.
- If an existing row has a different `request_hash`, return
  `ORD_IDEMPOTENCY_CONFLICT`.
- If the existing row is `in_progress` and younger than the command deadline,
  return `SYS_IN_PROGRESS` with retry hint.
- Rows expire after 7 days; a background job deletes expired rows in batches.

### 4.5 Funding rates

```sql
CREATE TABLE trading.funding_rates (
    symbol_id          integer NOT NULL,
    interval_start     timestamptz NOT NULL,
    interval_end       timestamptz NOT NULL,
    premium_index_bps  integer NOT NULL,
    interest_bps       integer NOT NULL,
    funding_rate_bps   integer NOT NULL,
    mark_price_ticks   bigint NOT NULL,
    published_at       timestamptz NOT NULL,
    PRIMARY KEY (symbol_id, interval_start)
);
```

---

## 5. Schema: ledger (per shard)

The following DDL is replicated in each `ledger_XX` schema. `shard_id` is
implicit in the schema name but stored on journal batches for audit exports.
Each schema is created by the same versioned migration, applied once per logical
shard.

### 5.1 Accounts and balances

```sql
CREATE SCHEMA IF NOT EXISTS ledger;

CREATE TABLE ledger.accounts (
    account_id         uuid PRIMARY KEY,
    owner_type         text NOT NULL CHECK (owner_type IN ('user','system')),
    owner_id           uuid NOT NULL,
    wallet_type        text NOT NULL CHECK (wallet_type IN
                         ('spot','futures','funding','custody','clearing',
                          'fee','insurance','adl','funding_pool','transit',
                          'rounding','adjustment')),
    asset_id           integer NOT NULL,
    status             text NOT NULL CHECK (status IN
                         ('active','frozen','withdrawal_only','closed')),
    posted             numeric(38,0) NOT NULL DEFAULT 0,
    held               numeric(38,0) NOT NULL DEFAULT 0,
    available          numeric(38,0) GENERATED ALWAYS AS (posted - held) STORED,
    version            bigint NOT NULL DEFAULT 0,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CHECK (held >= 0),
    CHECK (owner_type = 'system' OR posted >= 0),
    CHECK (owner_type = 'system' OR held <= posted),
    UNIQUE (owner_id, wallet_type, asset_id)
);

CREATE INDEX accounts_owner ON ledger.accounts (owner_id, wallet_type);
CREATE INDEX accounts_asset ON ledger.accounts (asset_id);
```

For system accounts, negative posted balances are allowed (clearing, ADL,
funding pool). The sum across all accounts per asset must remain zero (I6).

### 5.2 Holds

```sql
CREATE TABLE ledger.holds (
    hold_id            uuid PRIMARY KEY,
    account_id         uuid NOT NULL REFERENCES ledger.accounts(account_id),
    asset_id           integer NOT NULL,
    purpose            text NOT NULL CHECK (purpose IN
                         ('order','withdrawal','fee','liquidation','funding')),
    reference_id       text NOT NULL,
    amount             numeric(38,0) NOT NULL CHECK (amount > 0),
    remaining          numeric(38,0) NOT NULL,
    status             text NOT NULL CHECK (status IN
                         ('active','consumed','released','expired')),
    created_at         timestamptz NOT NULL DEFAULT now(),
    expires_at         timestamptz,
    version            bigint NOT NULL DEFAULT 0,
    CHECK (remaining >= 0),
    CHECK (remaining <= amount),
    UNIQUE (purpose, reference_id)
);

CREATE INDEX holds_account_active ON ledger.holds (account_id, asset_id)
    WHERE status = 'active';
CREATE INDEX holds_expiry ON ledger.holds (expires_at)
    WHERE status = 'active';
```

### 5.3 Journal

```sql
CREATE TABLE ledger.journal_batches (
    journal_id         uuid NOT NULL,
    shard_id           smallint NOT NULL,
    seq                bigint NOT NULL,
    kind               text NOT NULL CHECK (kind IN
                         ('trade','funding','deposit','withdrawal','fee',
                          'liquidation','insurance','adl','adjustment',
                          'transfer')),
    source_type        text NOT NULL,
    source_id          text NOT NULL,
    correlation_id     uuid NOT NULL,
    occurred_at        timestamptz NOT NULL,
    effective_at       timestamptz NOT NULL,
    prev_hash          bytea NOT NULL,
    hash               bytea NOT NULL,
    entry_count        smallint NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (journal_id, effective_at)
) PARTITION BY RANGE (effective_at);

-- Idempotency registry for journal sources. Not partitioned, so the unique
-- constraint spans all time partitions.
CREATE TABLE ledger.journal_keys (
    source_type        text NOT NULL,
    source_id          text NOT NULL,
    shard_id           smallint NOT NULL,
    journal_id         uuid NOT NULL,
    effective_at       timestamptz NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (source_type, source_id, shard_id)
);

CREATE TABLE ledger.journal_entries (
    journal_id         uuid NOT NULL,
    effective_at       timestamptz NOT NULL,
    line_no            smallint NOT NULL,
    account_id         uuid NOT NULL,
    asset_id           integer NOT NULL,
    amount             numeric(38,0) NOT NULL CHECK (amount <> 0),
    balance_after      numeric(38,0) NOT NULL,
    metadata           jsonb,
    PRIMARY KEY (journal_id, effective_at, line_no)
) PARTITION BY RANGE (effective_at);

CREATE INDEX journal_entries_account ON ledger.journal_entries
    (account_id, effective_at DESC);
CREATE INDEX journal_entries_asset ON ledger.journal_entries
    (asset_id, effective_at DESC);
```

Balance checks are enforced by a deferred constraint trigger at batch commit:

```sql
CREATE CONSTRAINT TRIGGER journal_must_balance
AFTER INSERT ON ledger.journal_entries
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION ledger.assert_batch_balanced();
```

The trigger aggregates the batch and raises unless every asset nets to zero.
The application performs the same check before writing; the trigger is the last
line of defense.

### 5.4 Positions

```sql
CREATE TABLE ledger.positions (
    account_id          uuid NOT NULL,
    symbol_id           integer NOT NULL,
    position_side       text NOT NULL CHECK (position_side IN ('long','short','both')),
    net_side            text NOT NULL CHECK (net_side IN ('long','short','flat')),
    qty_lots            bigint NOT NULL CHECK (qty_lots >= 0),
    entry_price_ticks   bigint NOT NULL,
    entry_rounding      numeric(38,0) NOT NULL DEFAULT 0,
    realized_pnl        numeric(38,0) NOT NULL DEFAULT 0,
    margin_mode         text NOT NULL CHECK (margin_mode IN ('cross','isolated')),
    leverage            smallint NOT NULL,
    isolated_margin     numeric(38,0) NOT NULL DEFAULT 0,
    liquidation_price   bigint,
    bankruptcy_price    bigint,
    status              text NOT NULL CHECK (status IN
                         ('open','reducing','closed','liquidating')),
    version             bigint NOT NULL DEFAULT 0,
    updated_at          timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, symbol_id, position_side)
);

CREATE INDEX positions_symbol ON ledger.positions (symbol_id)
    WHERE status <> 'closed';
```

Hedge mode uses separate `long` and `short` rows, with `net_side` matching the
row side. One-way mode uses a single `both` row; `net_side` records whether the
net position is long, short, or flat, and `qty_lots` is the absolute size. A
flat one-way position is closed and may be deleted after the close is settled.

### 5.5 Margin accounts

```sql
CREATE TABLE ledger.margin_accounts (
    account_id          uuid PRIMARY KEY,
    position_mode       text NOT NULL CHECK (position_mode IN ('one_way','hedge')),
    default_margin_mode text NOT NULL CHECK (default_margin_mode IN ('cross','isolated')),
    wallet_balance      numeric(38,0) NOT NULL DEFAULT 0,
    unrealized_pnl      numeric(38,0) NOT NULL DEFAULT 0,
    maintenance_margin  numeric(38,0) NOT NULL DEFAULT 0,
    initial_margin      numeric(38,0) NOT NULL DEFAULT 0,
    available_margin    numeric(38,0) NOT NULL DEFAULT 0,
    margin_ratio_bps    integer NOT NULL DEFAULT 0,
    version             bigint NOT NULL DEFAULT 0,
    updated_at          timestamptz NOT NULL DEFAULT now()
);
```

These columns are a cache for risk and API reads. They are recomputed from the
ledger and positions on every settlement transaction and verified by a nightly
reconciliation job.

### 5.6 Funding payments

```sql
CREATE TABLE ledger.funding_payments (
    funding_id          text NOT NULL,          -- e.g. BTCUSDT:1730000000
    interval_start      timestamptz NOT NULL,
    symbol_id           integer NOT NULL,
    account_id          uuid NOT NULL,
    position_id         text NOT NULL,
    notional            numeric(38,0) NOT NULL,
    rate_bps            integer NOT NULL,
    payment             numeric(38,0) NOT NULL, -- positive = received
    journal_id          uuid NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (funding_id, account_id, position_id, interval_start)
) PARTITION BY RANGE (interval_start);
```

### 5.7 Liquidation and insurance

```sql
CREATE TABLE ledger.liquidation_cases (
    case_id             uuid PRIMARY KEY,
    account_id          uuid NOT NULL,
    symbol_id           integer NOT NULL,
    position_side       text NOT NULL,
    trigger_price       bigint NOT NULL,
    mark_price          bigint NOT NULL,
    equity_at_trigger   numeric(38,0) NOT NULL,
    maintenance_margin  numeric(38,0) NOT NULL,
    deficit             numeric(38,0) NOT NULL DEFAULT 0,
    state               text NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    closed_at           timestamptz,
    detail              jsonb NOT NULL DEFAULT '{}'
);

CREATE TABLE ledger.insurance_fund_ledger (
    entry_id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    journal_id          uuid NOT NULL,
    case_id             uuid,
    direction           text NOT NULL CHECK (direction IN ('contribution','drawdown','residual')),
    amount              numeric(38,0) NOT NULL CHECK (amount > 0),
    balance_after       numeric(38,0) NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now()
);
```

The insurance fund balance is always read from `ledger.accounts` for the
insurance account; the table above is an audit trail, not the balance.

---

## 6. Schema: wallet

```sql
CREATE SCHEMA IF NOT EXISTS wallet;

CREATE TABLE wallet.assets (
    asset_id           integer PRIMARY KEY,
    symbol             text NOT NULL UNIQUE,
    decimals           smallint NOT NULL CHECK (decimals BETWEEN 0 AND 18),
    kind               text NOT NULL,
    status             text NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE wallet.asset_networks (
    asset_id           integer NOT NULL REFERENCES wallet.assets(asset_id),
    network            text NOT NULL,
    decimals           smallint NOT NULL,
    min_deposit        numeric(38,0) NOT NULL,
    min_withdrawal     numeric(38,0) NOT NULL,
    withdrawal_fee     numeric(38,0) NOT NULL,
    confirmations      smallint NOT NULL,
    contract_address   text,
    memo_required      boolean NOT NULL DEFAULT false,
    status             text NOT NULL,
    PRIMARY KEY (asset_id, network)
);

CREATE TABLE wallet.deposit_addresses (
    address_id         uuid PRIMARY KEY,
    user_id            uuid NOT NULL,
    asset_id           integer NOT NULL,
    network            text NOT NULL,
    address            text NOT NULL,
    memo               text,
    custody_wallet_id  uuid,
    created_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (asset_id, network, address)
);

CREATE TABLE wallet.deposits (
    deposit_id         uuid PRIMARY KEY,
    user_id            uuid NOT NULL,
    asset_id           integer NOT NULL,
    network            text NOT NULL,
    txid               text NOT NULL,
    vout               integer NOT NULL DEFAULT 0,
    from_address       text,
    to_address         text NOT NULL,
    amount             numeric(38,0) NOT NULL,
    confirmations      integer NOT NULL DEFAULT 0,
    status             text NOT NULL,
    credited_journal   uuid,
    observed_at        timestamptz NOT NULL,
    credited_at        timestamptz,
    UNIQUE (network, txid, vout)
);

CREATE TABLE wallet.withdrawals (
    withdrawal_id      uuid PRIMARY KEY,
    user_id            uuid NOT NULL,
    asset_id           integer NOT NULL,
    network            text NOT NULL,
    to_address         text NOT NULL,
    memo               text,
    amount             numeric(38,0) NOT NULL,
    fee                numeric(38,0) NOT NULL,
    status             text NOT NULL,
    hold_id            uuid,
    txid               text,
    approvals          jsonb NOT NULL DEFAULT '[]',
    risk_score         numeric(6,2),
    created_at         timestamptz NOT NULL DEFAULT now(),
    approved_at        timestamptz,
    broadcast_at       timestamptz,
    confirmed_at       timestamptz,
    settled_journal    uuid,
    UNIQUE (network, txid)
);

CREATE INDEX withdrawals_user_time ON wallet.withdrawals
    (user_id, created_at DESC);
CREATE INDEX withdrawals_pending ON wallet.withdrawals (status)
    WHERE status IN ('requested','review','approved','signed','broadcast');

CREATE TABLE wallet.withdrawal_approvals (
    withdrawal_id      uuid NOT NULL REFERENCES wallet.withdrawals(withdrawal_id),
    approver_id        uuid NOT NULL,
    decision           text NOT NULL,
    reason             text,
    decided_at         timestamptz NOT NULL,
    PRIMARY KEY (withdrawal_id, approver_id)
);

CREATE TABLE wallet.chain_events (
    event_id           uuid PRIMARY KEY,
    network            text NOT NULL,
    txid               text NOT NULL,
    block_height       bigint,
    block_hash         text,
    event_type         text NOT NULL,
    payload            jsonb NOT NULL,
    observed_at        timestamptz NOT NULL,
    UNIQUE (network, txid, event_type, block_hash)
);

CREATE INDEX chain_events_tx ON wallet.chain_events (network, txid);
```

---

## 7. Schema: ops

```sql
CREATE SCHEMA IF NOT EXISTS ops;

CREATE TABLE ops.outbox (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    aggregate_type     text NOT NULL,
    aggregate_id       text NOT NULL,
    event_type         text NOT NULL,
    payload            bytea NOT NULL,          -- protobuf EventEnvelope payload
    headers            jsonb NOT NULL DEFAULT '{}',
    created_at         timestamptz NOT NULL DEFAULT now(),
    published_at       timestamptz,
    attempts           integer NOT NULL DEFAULT 0,
    last_error         text
);
CREATE INDEX outbox_unpublished ON ops.outbox (id)
    WHERE published_at IS NULL;

CREATE TABLE ops.inbox (
    consumer           text NOT NULL,
    topic              text NOT NULL,
    partition          integer NOT NULL,
    offset             bigint NOT NULL,
    event_id           uuid NOT NULL,
    processed_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (consumer, topic, partition, offset),
    UNIQUE (consumer, event_id)
);

CREATE TABLE ops.audit_log (
    id                 bigint GENERATED ALWAYS AS IDENTITY,
    occurred_at        timestamptz NOT NULL,
    actor_type         text NOT NULL,
    actor_id           text,
    action             text NOT NULL,
    target_type        text NOT NULL,
    target_id          text,
    before             jsonb,
    after              jsonb,
    ip_hash            bytea,
    user_agent         text,
    hash_prev          bytea NOT NULL,
    hash               bytea NOT NULL,
    PRIMARY KEY (id, occurred_at)
) PARTITION BY RANGE (occurred_at);

CREATE TABLE ops.config (
    key                text PRIMARY KEY,
    value              jsonb NOT NULL,
    version            bigint NOT NULL,
    updated_by         text NOT NULL,
    updated_at         timestamptz NOT NULL DEFAULT now(),
    effective_at       timestamptz
);

CREATE TABLE ops.feature_flags (
    name               text PRIMARY KEY,
    enabled            boolean NOT NULL,
    rollout_percent    smallint NOT NULL DEFAULT 0,
    updated_by         text NOT NULL,
    updated_at         timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ops.reconciliation_runs (
    run_id             uuid PRIMARY KEY,
    scope              text NOT NULL,
    started_at         timestamptz NOT NULL,
    finished_at        timestamptz,
    status             text NOT NULL,
    checked_count      bigint NOT NULL DEFAULT 0,
    mismatch_count     bigint NOT NULL DEFAULT 0,
    summary            jsonb NOT NULL DEFAULT '{}'
);

CREATE TABLE ops.reconciliation_findings (
    finding_id         uuid PRIMARY KEY,
    run_id             uuid NOT NULL REFERENCES ops.reconciliation_runs(run_id),
    severity           text NOT NULL,
    entity_type        text NOT NULL,
    entity_id          text NOT NULL,
    expected           jsonb,
    actual             jsonb,
    resolution         text,
    resolved_at        timestamptz
);

CREATE TABLE ops.shard_map (
    shard_id           smallint PRIMARY KEY,
    physical_cluster   text NOT NULL,
    state              text NOT NULL CHECK (state IN ('active','draining','read_only')),
    version            bigint NOT NULL
);
```

---

## 8. Indexing strategy

| Query pattern | Index |
|---------------|-------|
| Open orders by account | Partial index on `(account_id, symbol_id) WHERE status IN (...)` |
| Order lookup by id | Primary key `(symbol_id, order_id)` |
| Client idempotency | PK `(account_id, key)` |
| Trades by symbol/time | `(symbol_id, executed_at DESC)` |
| Trades by account | `(buyer_account_id, executed_at DESC)` + seller equivalent |
| Ledger by account/time | `(account_id, effective_at DESC)` |
| Holds for available balance | Partial index `(account_id, asset_id) WHERE status='active'` |
| Expired holds | Partial index on `expires_at` |
| Outbox relay | Partial index on `id WHERE published_at IS NULL` |
| Inbox dedupe | PK + unique `(consumer, event_id)` |
| Audit by target | `(target_type, target_id, occurred_at DESC)` |

Rules:

- Every index is justified by a query in the access-pattern catalog.
- Unused indexes are removed quarterly using `pg_stat_user_indexes`.
- Partial indexes are preferred for status-scoped queries.
- `BRIN` is used on append-only time columns where appropriate; `BTREE` for
  point lookups.
- Index creation on production uses `CREATE INDEX CONCURRENTLY` and is
  decoupled from schema migrations.

---

## 9. Transactions and isolation

| Operation | Isolation | Notes |
|-----------|-----------|-------|
| Hold creation | `READ COMMITTED` + `SELECT ... FOR UPDATE` | Lock account row, recheck available |
| Settlement | `READ COMMITTED` + row locks | Lock accounts in ascending `account_id` order |
| Journal insert | Same transaction | Batch + entries + balance update + outbox |
| Admin adjustment | `SERIALIZABLE` | Rare, correctness over throughput |
| Reconciliation read | `REPEATABLE READ` | Consistent snapshot |
| Order queries | `READ COMMITTED` | Served from projection |

Deadlock prevention:

- Multi-account transactions lock accounts in lexicographic `account_id` order.
- Trade settlement locks only one participant account plus the clearing account;
  the clearing account is locked last by convention? No — use a global order:
  sort all account IDs including clearing. Because each leg touches exactly one
  user account and one clearing account, sorting is trivial and consistent.
- No transaction holds a lock while making a network call.

Lock timeouts:

```sql
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '5s';
SET LOCAL idle_in_transaction_session_timeout = '10s';
```

A lock timeout returns a retryable error; the client retries with the same
idempotency key.

---

## 10. Partitioning and retention

| Table | Partition | Retention (hot) | Archive |
|-------|-----------|-----------------|---------|
| `trading.orders` | HASH(symbol_id) 16 | 2 years | Parquet in S3, 7 years |
| `trading.order_events` | RANGE(month) | 12 months | S3, 7 years |
| `trading.trades` | RANGE(month) | 24 months | ClickHouse + S3, 7 years |
| `trading.fills` | RANGE(month) | 24 months | ClickHouse + S3, 7 years |
| `ledger.journal_batches` | RANGE(month) | 7 years | S3 WORM, 10 years |
| `ledger.journal_entries` | RANGE(month) | 7 years | S3 WORM, 10 years |
| `ledger.funding_payments` | RANGE(month) | 7 years | S3, 10 years |
| `ops.audit_log` | RANGE(month) | 12 months | WORM, 7 years |
| `ops.outbox` | none (purged) | 7 days after publish | n/a |

Partition maintenance:

- A job creates the next 3 months of partitions on the first day of each month.
- Detaching old partitions is a two-step process: detach, export, verify
  checksum, then drop.
- No `DELETE` on multi-million-row financial tables; use partition drop.
- `pg_partman` may be used for range partitions, with an audited config.

---

## 11. Migrations

- Tooling: `sqlx migrate` for Rust services, `golang-migrate` for Go services.
- Every migration is forward-only in production. Rollback is a new migration.
- Expand/contract pattern:
  1. Add nullable column or new table.
  2. Dual-write from application.
  3. Backfill in batches with throttling.
  4. Switch reads.
  5. Remove old column in a later release.
- Migrations must be idempotent and safe to re-run.
- Destructive changes require an ADR and a backup verification.
- Long backfills run as separate jobs, not inside migrations.
- Migration lock: one migration at a time per database, enforced by advisory
  lock.
- CI runs migrations against a production-sized snapshot and measures lock time.

---

## 12. Backup and point-in-time recovery

| Database | Backup | Frequency | Retention | Restore test |
|----------|--------|-----------|-----------|--------------|
| All Postgres | Base backup (WAL-G/pgBastion) | Daily | 35 days | Monthly |
| All Postgres | WAL archiving | Continuous | 35 days | Monthly |
| Ledger | Logical export of journal | Daily | 7 years | Quarterly |
| ClickHouse | `BACKUP` to S3 | Daily | 90 days | Quarterly |
| Object storage | Versioning + cross-region replication | Continuous | Per bucket | Quarterly |

Recovery objectives:

- RPO for Postgres: 0 for synchronous commits; < 5 minutes for region loss.
- RTO for a single database: < 15 minutes with Patroni.
- RTO for a full region: < 4 hours with documented runbook and pre-warmed
  infrastructure.

Restore drills are mandatory before launch and quarterly thereafter. A backup
that has not been restored is considered unverified.

---

## 13. Data integrity checks

Nightly and on-demand reconciliation:

1. **Journal balance:** every batch nets to zero per asset.
2. **Balance projection:** `accounts.posted = sum(journal_entries.amount)` per
   account.
3. **Holds:** `accounts.held = sum(active holds.remaining)`.
4. **Available:** `accounts.available = posted - held` and `>= 0` for customers.
5. **Global zero-sum:** sum of all accounts per asset across shards = 0.
6. **Positions:** replay fills for the day and compare to `ledger.positions`.
7. **Trades:** compare `trading.trades` count and notional with settlement
   journals.
8. **Funding:** sum of payments per interval = rounding residue only.
9. **Wallet:** on-chain observed balance vs custody ledger vs customer
   liabilities.
10. **Idempotency:** no duplicate `(trade_id, participant_side)` settlements.

Findings are stored in `ops.reconciliation_findings` and block withdrawals for
affected accounts until resolved.

---

## 14. Access control

- Each service has a dedicated database role with least privilege:
  `trading_core_rw`, `account_core_rw`, `edge_ro`, `analytics_ro`, etc.
- No `SUPERUSER` in application roles.
- Row-level security is not used for financial tables; schema and role
  separation is the boundary.
- Direct human access to production databases requires break-glass approval,
  session recording, and is limited to read-only unless dual-approved.
- All DDL is applied by CI/CD, never manually.
