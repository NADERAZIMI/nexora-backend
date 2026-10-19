# 12 — REST API and WebSocket

---

## 1. Conventions

### 1.1 Base URLs

| Surface | URL |
|---------|-----|
| REST | `https://api.nexora.exchange/v1` |
| Public WebSocket | `wss://stream.nexora.exchange/v1/public` |
| Private WebSocket | `wss://stream.nexora.exchange/v1/private` |
| Status | `https://status.nexora.exchange` |

### 1.2 Formatting

- JSON UTF-8, `Content-Type: application/json`.
- Monetary values, prices, and quantities are **decimal strings**, never JSON
  numbers. Example: `"price": "60000.10"`, `"qty": "0.500"`.
- Timestamps are ISO-8601 UTC in REST responses and Unix milliseconds in
  requests where numeric.
- Enums are uppercase snake case: `"LIMIT"`, `"GTC"`, `"BUY"`.
- Null fields are omitted, not sent as `null`, unless documented.
- Unknown response fields must be ignored by clients.

### 1.3 Required headers

| Header | Required | Description |
|--------|----------|-------------|
| `X-NEXORA-API-KEY` | Authenticated | API key ID (not secret) |
| `X-NEXORA-TIMESTAMP` | Authenticated | Unix milliseconds |
| `X-NEXORA-RECV-WINDOW` | Optional | Validity window ms, default 5000, max 60000 |
| `X-NEXORA-SIGNATURE` | Authenticated | Hex HMAC-SHA256 or base64 Ed25519 |
| `X-Request-ID` | Optional | Client correlation ID; generated if absent |
| `X-Idempotency-Key` | Mutating POST | Client idempotency key |
| `Idempotency-Key` | Alternative | Accepted alias |
| `Authorization` | UI sessions | `Bearer <JWT>` |
| `User-Agent` | Recommended | Client identification |
| `Accept-Language` | Optional | Error message localization |

### 1.4 Signature

Canonical string:

```
timestamp + "\n" +
method.toUpperCase() + "\n" +
path + "\n" +
canonical_query + "\n" +
sha256_hex(raw_body)
```

- `path` is the request path without query, e.g. `/v1/orders`.
- `canonical_query` is sorted by key, URL-encoded, joined with `&`; empty string
  if no query.
- `raw_body` is the exact bytes sent; empty string for GET/DELETE.
- HMAC keys: signature is HMAC-SHA256 with the API secret, hex-encoded lowercase.
- Ed25519 keys: signature is base64-encoded Ed25519 over the same canonical
  string. The algorithm is selected by the key record, not by a client header.
- The server rejects timestamps outside the receive window.
- Nonces are not required because the timestamp window plus idempotency keys
  prevent replay of mutating requests; the server also caches signatures for the
  window to reject exact replays.

### 1.5 Response envelope

Success responses return the resource directly, plus headers:

```
X-Request-ID: 01J...
X-RateLimit-Limit: 1200
X-RateLimit-Remaining: 1187
X-RateLimit-Reset: 1730000000
```

Errors:

```json
{
  "error": {
    "code": "RSK_INSUFFICIENT_MARGIN",
    "message": "Insufficient margin for this order.",
    "requestId": "01J8Z...",
    "retryable": false,
    "details": {
      "required": "612.40",
      "available": "540.10"
    }
  }
}
```

HTTP status mapping:

| Status | Meaning |
|--------|---------|
| 200 | Success |
| 201 | Created (order, withdrawal) |
| 204 | No content (cancel, delete) |
| 400 | Validation or risk rejection |
| 401 | Authentication failed |
| 403 | Permission or account restriction |
| 404 | Unknown resource |
| 409 | State conflict or duplicate in progress |
| 422 | Semantically invalid but well-formed |
| 429 | Rate limited |
| 500 | Internal error |
| 503 | Unavailable or backpressure |

---

## 2. Rate limits

Limits apply per API key, per account, and per IP, whichever is most
restrictive. Weights are assigned per endpoint.

| Tier | Requests/min | Order/s | Cancel/s | Burst |
|------|--------------|---------|----------|-------|
| Retail | 1,200 | 50 | 100 | 2x |
| Pro | 6,000 | 200 | 400 | 3x |
| Market maker | Custom | Custom | Custom | Custom |

Headers on every response:

```
X-RateLimit-Limit
X-RateLimit-Remaining
X-RateLimit-Reset
Retry-After (on 429)
```

On limit:

```json
{
  "error": {
    "code": "RATE_LIMIT_EXCEEDED",
    "message": "Too many requests.",
    "retryable": true,
    "details": { "retryAfterMs": "250" }
  }
}
```

Abuse (sustained over-limit, ban evasion) results in temporary or permanent key
suspension with an audit record.

---

## 3. Idempotency

### 3.1 Orders

- `clientOrderId` is the idempotency key for order placement.
- Unique per account for 7 days.
- Repeating the same request returns the original order and fills.
- Reusing with a different payload returns `ORD_IDEMPOTENCY_CONFLICT`.
- On timeout, clients MUST query `GET /v1/orders?clientOrderId=...` before
  retrying.

### 3.2 Withdrawals and transfers

- Require `X-Idempotency-Key` (UUID recommended).
- The key is bound to the request hash.
- Same key + same payload returns the original resource.
- Same key + different payload returns `VAL_IDEMPOTENCY_CONFLICT`.

### 3.3 Retention

| Operation | Retention |
|-----------|-----------|
| Orders | 7 days |
| Withdrawals | 30 days |
| Transfers | 7 days |
| Admin actions | 7 years (audit) |

---

## 4. Pagination

Cursor-based:

```
GET /v1/orders?limit=100&cursor=eyJpZCI6...
```

Response:

```json
{
  "items": [ ... ],
  "nextCursor": "eyJpZCI6...",
  "hasMore": true
}
```

- Default limit 50, max 500.
- Cursors are opaque, signed, and expire after 24 hours.
- Results are ordered newest first unless documented.
- `limit` above max returns 400, not a silent clamp.

---

## 5. REST endpoints

### 5.1 System and reference

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/time` | No | Server time in ms |
| GET | `/ping` | No | Liveness |
| GET | `/instruments` | No | List instruments |
| GET | `/instruments/{symbol}` | No | Instrument details |
| GET | `/assets` | No | Asset and network metadata |
| GET | `/fee-schedule` | Yes | Account fee tier |

`GET /instruments/BTCUSDT`:

```json
{
  "symbol": "BTCUSDT",
  "kind": "PERP",
  "status": "ACTIVE",
  "baseAsset": "BTC",
  "quoteAsset": "USDT",
  "tickSize": "0.10",
  "lotSize": "0.001",
  "minQty": "0.001",
  "maxQty": "1000",
  "minNotional": "5",
  "maxLeverage": 125,
  "makerFeeBps": 2,
  "takerFeeBps": 5,
  "fundingIntervalHours": 8,
  "marginTiers": [
    { "notionalFloor": "0", "notionalCap": "50000", "maxLeverage": 125, "mmrBps": 40 },
    { "notionalFloor": "50000", "notionalCap": "250000", "maxLeverage": 100, "mmrBps": 50 }
  ]
}
```

### 5.2 Market data

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/markets` | No | All tickers |
| GET | `/markets/{symbol}/ticker` | No | 24h ticker |
| GET | `/markets/{symbol}/orderbook` | No | L2 snapshot |
| GET | `/markets/{symbol}/trades` | No | Recent public trades |
| GET | `/markets/{symbol}/candles` | No | OHLCV |
| GET | `/markets/{symbol}/mark-price` | No | Mark and index price |
| GET | `/markets/{symbol}/funding-rate` | No | Current and predicted rate |
| GET | `/markets/{symbol}/funding-history` | No | Historical rates |

Order book query: `?limit=50` (max 500). Response:

```json
{
  "symbol": "BTCUSDT",
  "seq": 918273645,
  "asOf": "2026-10-19T12:00:00.123Z",
  "bids": [["60000.10", "1.250"], ["60000.00", "3.400"]],
  "asks": [["60000.20", "0.800"], ["60000.30", "2.100"]]
}
```

Candles: `?interval=1m&limit=500&startTime=...&endTime=...`. Intervals: `1s`,
`1m`, `5m`, `15m`, `1h`, `4h`, `1d`.

### 5.3 Account

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/account` | Yes | Profile and permissions |
| GET | `/account/balances` | Yes | Spot/futures balances |
| GET | `/account/positions` | Yes | Open positions |
| GET | `/account/positions/history` | Yes | Closed positions |
| GET | `/account/limits` | Yes | Rate and risk limits |
| GET | `/account/fee-rate` | Yes | Maker/taker fees |
| GET | `/account/pnl` | Yes | Realized/unrealized PnL |

Balances example:

```json
{
  "balances": [
    {
      "asset": "USDT",
      "wallet": "SPOT",
      "posted": "12500.000000",
      "held": "300.000000",
      "available": "12200.000000"
    },
    {
      "asset": "USDT",
      "wallet": "FUTURES",
      "posted": "5000.000000",
      "held": "600.000000",
      "available": "4400.000000"
    }
  ]
}
```

Positions example:

```json
{
  "positions": [
    {
      "symbol": "BTCUSDT",
      "positionSide": "LONG",
      "qty": "0.500",
      "entryPrice": "60000.00",
      "markPrice": "60500.00",
      "unrealizedPnl": "250.000000",
      "realizedPnl": "0.000000",
      "leverage": 10,
      "marginMode": "CROSS",
      "liquidationPrice": "54271.36",
      "bankruptcyPrice": "54000.00",
      "maintenanceMargin": "151.250000",
      "initialMargin": "3025.000000"
    }
  ]
}
```

### 5.4 Orders

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/orders` | Trade | Place order |
| GET | `/orders` | Read | List open/all orders |
| GET | `/orders/{orderId}` | Read | Get order |
| GET | `/orders/by-client-id/{clientOrderId}` | Read | Get by client ID |
| DELETE | `/orders/{orderId}` | Trade | Cancel order |
| DELETE | `/orders` | Trade | Cancel all (filters) |
| GET | `/orders/{orderId}/fills` | Read | Fills for an order |
| GET | `/fills` | Read | Recent account fills |

Place order request:

```json
{
  "symbol": "BTCUSDT",
  "clientOrderId": "c7f1e2d0-3b4a-4c1e-9f2a-1b2c3d4e5f60",
  "side": "BUY",
  "type": "LIMIT",
  "timeInForce": "GTC",
  "price": "60000.10",
  "qty": "0.500",
  "positionSide": "LONG",
  "reduceOnly": false,
  "postOnly": false,
  "marginMode": "CROSS",
  "leverage": 10
}
```

Response `201`:

```json
{
  "orderId": "4210987654321",
  "clientOrderId": "c7f1e2d0-3b4a-4c1e-9f2a-1b2c3d4e5f60",
  "symbol": "BTCUSDT",
  "status": "PARTIALLY_FILLED",
  "side": "BUY",
  "type": "LIMIT",
  "price": "60000.10",
  "qty": "0.500",
  "filledQty": "0.200",
  "remainingQty": "0.300",
  "avgFillPrice": "60000.10",
  "createdAt": "2026-10-19T12:00:00.123Z",
  "fills": [
    {
      "tradeId": "99887766",
      "price": "60000.10",
      "qty": "0.200",
      "fee": "6.000020",
      "feeAsset": "USDT",
      "isMaker": false
    }
  ]
}
```

Order statuses: `PENDING_TRIGGER`, `ACCEPTED`, `PARTIALLY_FILLED`, `FILLED`,
`CANCELED`, `REJECTED`, `EXPIRED`.

`positionSide` is required in hedge mode (`LONG` or `SHORT`). In one-way mode it
must be omitted or set to `BOTH`; sending `LONG`/`SHORT` returns
`VAL_POSITION_SIDE`. The example above assumes a hedge-mode account.

Cancel all query: `?symbol=BTCUSDT&side=BUY&positionSide=LONG`.

### 5.5 Positions and margin

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/positions/margin-mode` | Trade | Set cross/isolated |
| POST | `/positions/leverage` | Trade | Set leverage |
| POST | `/positions/margin` | Trade | Add/remove isolated margin |
| POST | `/positions/close` | Trade | Close position at market |
| POST | `/positions/position-mode` | Trade | One-way/hedge |

Set margin mode:

```json
{ "symbol": "BTCUSDT", "marginMode": "ISOLATED" }
```

Set leverage:

```json
{ "symbol": "BTCUSDT", "leverage": 20 }
```

Add isolated margin:

```json
{ "symbol": "BTCUSDT", "positionSide": "LONG", "amount": "500.00", "action": "ADD" }
```

### 5.6 Transfers

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/transfers` | Trade | Spot ↔ futures |
| GET | `/transfers` | Read | Transfer history |

```json
{
  "from": "SPOT",
  "to": "FUTURES",
  "asset": "USDT",
  "amount": "1000.00"
}
```

Requires `X-Idempotency-Key`. Rejected if it would breach margin for open
positions or orders.

### 5.7 Wallet

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/wallet/deposit-address` | Yes | Address for asset/network |
| GET | `/wallet/deposits` | Yes | Deposit history |
| GET | `/wallet/withdrawals` | Yes | Withdrawal history |
| POST | `/wallet/withdrawals` | Withdraw | Request withdrawal |
| DELETE | `/wallet/withdrawals/{id}` | Withdraw | Cancel before broadcast |
| GET | `/wallet/withdrawal-quote` | Yes | Fee and minimum |
| GET | `/wallet/networks` | No | Supported networks |

Withdrawal request:

```json
{
  "asset": "USDT",
  "network": "TRC20",
  "address": "T...",
  "amount": "1000.00",
  "memo": null,
  "twoFactorCode": "123456"
}
```

Requires `X-Idempotency-Key`. Returns `202 Accepted` for manual review or `201`
when automatically approved.

### 5.8 API keys

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/api-keys` | Session + 2FA | Create key |
| GET | `/api-keys` | Session | List keys |
| DELETE | `/api-keys/{keyId}` | Session + 2FA | Revoke key |

Creation returns the secret once:

```json
{
  "keyId": "01J...",
  "key": "nx_live_...",
  "secret": "nx_secret_...",
  "scopes": ["read", "trade"],
  "ipAllowlist": ["203.0.113.10"]
}
```

Secrets are never retrievable through the API again.

### 5.9 Streaming listen keys

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/stream/listen-key` | Yes | Create a private-stream listen key |
| PUT | `/stream/listen-key` | Yes | Extend the listen key TTL |
| DELETE | `/stream/listen-key` | Yes | Revoke the listen key |

Listen keys are scoped to the account, valid for 60 minutes, and extended by
30-minute intervals. They are not API secrets and cannot place orders.

### 5.10 Authentication

| Method | Path | Description |
|--------|------|-------------|
| POST | `/auth/register` | Create account |
| POST | `/auth/login` | Password + 2FA |
| POST | `/auth/refresh` | Rotate refresh token |
| POST | `/auth/logout` | Revoke session |
| POST | `/auth/2fa/totp` | Enroll/verify TOTP |
| POST | `/auth/2fa/webauthn` | Register/verify WebAuthn |
| POST | `/auth/password/reset` | Request/confirm reset |

Login returns a short-lived access token (15 min) and a rotating refresh token
(30 days, bound to device fingerprint).

---

## 6. WebSocket protocol

### 6.1 Connection lifecycle

1. Client connects with TLS and `Sec-WebSocket-Protocol: nexora.v1`.
2. Server sends `welcome` with connection ID, server time, and heartbeat
   interval.
3. Client subscribes to channels.
4. Server sends snapshots, then deltas.
5. Heartbeat: client sends `{"op":"ping"}` every 20 s; server responds
   `{"op":"pong","ts":...}`. Missed heartbeats close the connection after 60 s.
6. On reconnect, clients fetch a fresh snapshot and discard stale deltas.

### 6.2 Subscribe

```json
{
  "op": "subscribe",
  "id": "sub-1",
  "params": {
    "channels": ["book.BTCUSDT", "trades.BTCUSDT", "ticker.BTCUSDT"]
  }
}
```

Response:

```json
{
  "op": "subscribed",
  "id": "sub-1",
  "channels": ["book.BTCUSDT", "trades.BTCUSDT", "ticker.BTCUSDT"]
}
```

Unsubscribe uses the same shape with `"op":"unsubscribe"`.

### 6.3 Public channels

| Channel | Payload |
|---------|---------|
| `book.{symbol}` | Snapshot + L2 deltas |
| `trades.{symbol}` | Public trade prints |
| `ticker.{symbol}` | Best bid/ask, last, 24h stats |
| `mark.{symbol}` | Mark and index price |
| `funding.{symbol}` | Funding rate updates |
| `instrument.{symbol}` | Status/config changes |
| `candles.{symbol}.{interval}` | Closed candles (optional) |

Book snapshot:

```json
{
  "channel": "book.BTCUSDT",
  "type": "snapshot",
  "seq": 918273645,
  "ts": 1730000000123,
  "data": {
    "bids": [["60000.10", "1.250"], ["60000.00", "3.400"]],
    "asks": [["60000.20", "0.800"], ["60000.30", "2.100"]]
  }
}
```

Book delta:

```json
{
  "channel": "book.BTCUSDT",
  "type": "delta",
  "fromSeq": 918273645,
  "seq": 918273646,
  "ts": 1730000000140,
  "data": {
    "bids": [["60000.10", "0.750"]],
    "asks": [["60000.30", "0"]]
  }
}
```

Rules:

- `qty = "0"` removes the level.
- Levels are absolute totals, not incremental changes.
- `seq` is strictly increasing per symbol.
- For non-conflated streams, `fromSeq` MUST equal the client's `lastSeq`, and
  `seq` MUST equal `fromSeq + 1`. A mismatch means the client reconnects and
  resnapshots.
- Public book streams may be conflated: a conflated delta carries
  `fromSeq = lastSeq + 1` and a higher `seq`, and contains the **net change for
  every level modified since `fromSeq`**. The client applies it atomically; the
  gap check uses `fromSeq`, not `seq`.
- Private channels are never conflated and always require strict `+1`.
- A snapshot includes the `seq` it represents; deltas with lower `seq` are
  discarded.

### 6.4 Private authentication

Option A — listen key:

1. `POST /v1/stream/listen-key` (authenticated) returns a key valid for 60
   minutes.
2. Connect to `/v1/private?listenKey=...`.
3. `PUT /v1/stream/listen-key` every 30 minutes to extend.
4. `DELETE` on logout.

Option B — signed login message:

```json
{
  "op": "login",
  "args": {
    "apiKey": "nx_live_...",
    "timestamp": 1730000000123,
    "signature": "hex"
  }
}
```

The signature covers `timestamp + "\n" + "WS_LOGIN" + "\n" + apiKey`, using the
same HMAC-SHA256 secret as REST. The timestamp must be within the receive
window, and the login message is single-use per connection.

### 6.5 Private channels

| Channel | Payload |
|---------|---------|
| `orders` | Order accepted, canceled, expired, triggered |
| `fills` | Execution details per fill |
| `positions` | Position quantity, entry, PnL, margin |
| `balances` | Balance and hold changes |
| `margin` | Margin ratio, liquidation price, ADL indicator |
| `risk` | Liquidation warnings, ADL queue |
| `wallet` | Deposit/withdrawal status |

Order update:

```json
{
  "channel": "orders",
  "type": "update",
  "seq": 77123,
  "ts": 1730000000200,
  "data": {
    "orderId": "4210987654321",
    "clientOrderId": "c7f1e2d0-...",
    "symbol": "BTCUSDT",
    "status": "FILLED",
    "filledQty": "0.500",
    "avgFillPrice": "60000.10",
    "updatedAt": "2026-10-19T12:00:00.200Z"
  }
}
```

Fill:

```json
{
  "channel": "fills",
  "type": "fill",
  "seq": 77124,
  "ts": 1730000000201,
  "data": {
    "tradeId": "99887767",
    "orderId": "4210987654321",
    "symbol": "BTCUSDT",
    "side": "BUY",
    "price": "60000.10",
    "qty": "0.300",
    "fee": "9.000015",
    "feeAsset": "USDT",
    "isMaker": false,
    "settled": true
  }
}
```

Private events:

- Are never conflated.
- Are ordered per account by `seq`.
- Include `settled` to distinguish execution from ledger application.
- On gap, the client reconnects and calls the REST endpoints for state.

### 6.6 Subscription limits

| Limit | Value |
|-------|-------|
| Channels per connection | 200 |
| Connections per account | 10 |
| Connections per IP | 50 |
| Message rate | 10/s |
| Max frame size | 64 KB |

Exceeding limits returns an error and may close the connection.

### 6.7 WebSocket errors

```json
{
  "op": "error",
  "id": "sub-1",
  "error": {
    "code": "WS_UNKNOWN_CHANNEL",
    "message": "Unknown channel."
  }
}
```

Close codes:

| Code | Meaning |
|------|---------|
| 1000 | Normal |
| 1001 | Going away |
| 1008 | Policy violation |
| 1011 | Server error |
| 4000 | Authentication failed |
| 4001 | Subscription limit |
| 4002 | Sequence gap / resync required |
| 4003 | Rate limited |
| 4004 | Invalid message |

---

## 7. Error catalog (public)

| Code | HTTP | Retryable | Description |
|------|------|-----------|-------------|
| `AUTH_MISSING` | 401 | No | Missing credentials |
| `AUTH_INVALID_SIGNATURE` | 401 | No | Signature mismatch |
| `AUTH_TIMESTAMP` | 401 | No | Timestamp outside window |
| `AUTH_SCOPE` | 403 | No | Key lacks scope |
| `AUTH_IP_NOT_ALLOWED` | 403 | No | IP not allowlisted |
| `ACC_FROZEN` | 403 | No | Account frozen |
| `ACC_RESTRICTED` | 403 | No | Jurisdiction/product restriction |
| `VAL_SCHEMA` | 400 | No | Malformed request |
| `VAL_DECIMAL` | 400 | No | Invalid decimal |
| `VAL_SCALE` | 400 | No | Too many decimals |
| `VAL_PRICE_TICK` | 400 | No | Price not on tick |
| `VAL_QTY_LOT` | 400 | No | Quantity not on lot |
| `VAL_IDEMPOTENCY_CONFLICT` | 409 | No | Key reused with different payload |
| `MKT_UNKNOWN` | 404 | No | Unknown symbol |
| `MKT_HALTED` | 409 | Yes* | Symbol halted |
| `MKT_PRICE_BAND` | 400 | Yes* | Price outside band |
| `ORD_UNKNOWN` | 404 | No | Unknown order |
| `ORD_IDEMPOTENCY_CONFLICT` | 409 | No | Client order ID conflict |
| `ORD_POST_ONLY_WOULD_CROSS` | 400 | No | Post-only would take |
| `ORD_REDUCE_ONLY` | 400 | No | Reduce-only violation |
| `RSK_INSUFFICIENT_BALANCE` | 400 | Yes* | Insufficient balance |
| `RSK_INSUFFICIENT_MARGIN` | 400 | Yes* | Insufficient margin |
| `RSK_LEVERAGE_EXCEEDED` | 400 | No | Leverage too high |
| `RSK_POSITION_LIMIT` | 400 | No | Position limit |
| `RATE_LIMIT_EXCEEDED` | 429 | Yes | Rate limited |
| `SYS_BACKPRESSURE` | 503 | Yes | Overloaded |
| `SYS_SHARD_UNAVAILABLE` | 503 | Yes | Leader unavailable |
| `SYS_IN_PROGRESS` | 409 | Yes | Duplicate in flight |
| `SYS_MAINTENANCE` | 503 | Yes | Scheduled maintenance |

`*` retryable after the condition changes.

---

## 8. API versioning and deprecation

- Major version in the path (`/v1`). Breaking changes require `/v2`.
- Additive fields are non-breaking; clients must ignore unknown fields.
- Deprecation notice: at least 90 days for public APIs, 30 days for
  experimental endpoints.
- `Deprecation` and `Sunset` headers on deprecated endpoints.
- WebSocket channel versions are negotiated in the subscription payload; old
  versions remain available for the deprecation window.
- Changelog published with every release.

---

## 9. API security

- TLS 1.3 only; HSTS enabled.
- Request body size limit: 64 KB (REST), 64 KB (WS frame).
- JSON depth limit and duplicate-key rejection.
- No secrets in URLs; API keys and signatures in headers only.
- CORS restricted to first-party origins for browser sessions; API keys are not
  usable from browsers without explicit configuration.
- Error messages never reveal whether an email, account, or order belongs to
  another user.
- Sensitive endpoints (withdrawals, API key creation) require recent 2FA.
- All authentication failures are rate-limited and audit-logged.
- OpenAPI and AsyncAPI specifications are published and versioned with the
  service.
