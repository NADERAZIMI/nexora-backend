# 16 — Concurrency, Consistency and Scalability

---

## 1. Concurrency model

### 1.1 Thread-per-shard actors

| Component | Concurrency unit | Queue | Parallelism |
|-----------|------------------|-------|-------------|
| `trading-core` | Symbol shard | Bounded MPSC | One thread per shard |
| `account-core` | Account shard | Bounded MPSC | One thread per shard |
| `risk-engine` | Account partition | Bounded work queue | Worker pool |
| `edge-gateway` | Connection | Async tasks | Tokio/Go scheduler |
| `market-data` | Symbol stream | Ring buffer | Per-symbol task |
| `wallet-service` | Request | Async tasks | Worker pool |
| Settlement worker | Account partition | Kafka consumer | One partition per consumer |

Actors process one command at a time. No aggregate is mutated concurrently.

### 1.2 Why actors instead of locks

- Predictable latency: no lock contention or priority inversion.
- Simplicity: state transitions are sequential and testable.
- Determinism: command order defines state.
- Cache locality: a shard's working set stays on one core.
- Backpressure: queue depth is an explicit signal.

Locks are still used in Postgres for balance updates, but transactions are
short and scoped to a single account shard.

### 1.3 Queue discipline

- Bounded queues only; unbounded queues hide overload.
- Priority lanes: cancels/reduce-only > limit > market > post-only.
- Weighted fair queuing prevents starvation.
- Queue depth, age, and rejection rate are first-class metrics.
- When full: reject with `SYS_BACKPRESSURE`, not block indefinitely.

---

## 2. Consistency model per operation

| Operation | Consistency | Mechanism |
|-----------|-------------|-----------|
| Place order | Linearizable per symbol | Leader + Raft commit |
| Cancel order | Linearizable per symbol | Leader + Raft commit |
| Create hold | Linearizable per account | Shard leader + row lock |
| Settle trade | Linearizable per account leg | Row lock + journal |
| Cross-account settlement | Atomic per leg, eventual global | Clearing account + idempotency |
| Funding application | Exactly-once per position | Idempotency key |
| Deposit credit | Exactly-once per tx output | Unique constraint |
| Withdrawal | Linearizable per account + chain idempotency | Hold + signer dedupe |
| Balance query | Read-your-writes for owner | Leader or read barrier |
| Historical query | Eventually consistent | Projection with `as_of` |
| Market data | Ordered per symbol | Sequence numbers |
| Analytics | Eventually consistent | ClickHouse pipeline |

### 2.1 Read-your-writes

After placing an order, the client receives the committed order state in the
response. Subsequent reads by the same client can be served by:

- The leader (linearizable), or
- A replica that has applied at least the client's commit sequence (read
  barrier).

The edge attaches a `min_seq` from the response to subsequent reads; replicas
that have not reached it wait or redirect to the leader.

### 2.2 Monotonic reads

Session-affinity cookies or tokens pin a client to a replica version for
historical reads, preventing a client from seeing older data after newer data.
If the replica falls behind, the edge redirects to the leader.

---

## 3. Sharding

### 3.1 Symbol sharding

```
symbol_shard = rendezvous_hash(symbol_id, SYMBOL_SHARD_COUNT)
```

- Each shard is a Raft group of 3.
- A shard processes its symbols sequentially.
- Symbols are independent; no cross-symbol matching.
- Shard count is a power of two (16 at launch) to simplify routing.
- The mapping is versioned in `ops.shard_map` and cached by the edge.

Scaling:

1. Add a new shard and its Raft group.
2. Mark it `active` in the shard map.
3. Move symbols one at a time: halt symbol, snapshot, transfer, resume.
4. Update clients via instrument metadata (no client action required).

Moving a symbol is a maintenance operation with a short halt (< 30 s target).
Open orders are preserved in the snapshot; holds remain valid.

### 3.2 Account sharding

```
account_shard = rendezvous_hash(user_id, ACCOUNT_SHARD_COUNT)
```

- 16 logical shards at launch, mapped to 4 physical Postgres clusters.
- Each shard has a leader and two followers.
- All accounts for a user can be co-located by hashing `user_id` instead of
  `account_id`; this simplifies transfers and queries. Recommended:
  `shard = rendezvous_hash(user_id, N)` for user accounts; system accounts are
  replicated or assigned to a dedicated shard.
- Co-location ensures spot ↔ futures transfers are single-shard transactions.

Scaling:

1. Add a new logical shard.
2. Create schema and seed system accounts.
3. Dual-write new accounts to the new shard (no migration of existing users).
4. For rebalancing existing users, use a controlled migration with a freeze
   window, or rely on new-user growth.

Account migration is rare and expensive; the design favors adding shards for new
users over moving existing ones.

### 3.3 Hot-spot mitigation

| Hot spot | Mitigation |
|----------|------------|
| One symbol dominates volume | Dedicated shard; more CPU; separate market data fan-out |
| One market maker account | Dedicated account shard; batching; rate limits |
| One asset (USDT) in every trade | Clearing account per shard; batch postings |
| Popular instrument at open | Pre-warm caches; rate limits; auction |
| Viral market data channel | Fan-out tree; conflation; edge caching |

### 3.4 Shard limits

| Resource | Limit per shard | Action at limit |
|----------|-----------------|-----------------|
| Symbols | 64 | Move symbols to a new shard |
| Resting orders | 5 million | Reject new orders; alert |
| Accounts | 5 million | Add shard |
| Ledger writes | 5,000/s | Batch; add shard |
| WAL size | 100 GB | Snapshot and archive |
| Raft lag | 1 s | Stop acknowledging |

---

## 4. Capacity model

### 4.1 Assumptions

| Parameter | Launch | 12-month target | 3-year target |
|-----------|--------|-----------------|---------------|
| Symbols | 50 | 200 | 500 |
| Daily active users | 50k | 500k | 2M |
| Orders/s peak | 5k | 50k | 300k |
| Trades/s peak | 2k | 25k | 150k |
| WS connections | 100k | 500k | 1M |
| Book updates/s | 500k | 2.5M | 5M |
| Ledger transactions/s | 4k | 50k | 300k |

### 4.2 Sizing

**Trading core:**

- 30k orders/s per shard design target.
- 10 shards at 12-month target; 20 shards at 3-year target (2x headroom).
- Each shard: 8 vCPU, 32 GB RAM, local NVMe, 10 Gbps network.

**Account core:**

- 5k settlement transactions/s per shard (two legs per trade).
- 10 shards at 12-month target.
- Each shard: 16 vCPU, 64 GB RAM, NVMe, Postgres cluster.

**Event bus:**

- 300k messages/s peak; 12 partitions per high-volume topic.
- RF=3, 3 brokers minimum; 6 at 12-month target.
- Tiered storage for retention.

**WebSocket:**

- 50k connections per edge pod (Go, tuned).
- 250k messages/s per pod with conflation.
- 20 pods at 1M connections, plus headroom.
- Fan-out tree: market-data → regional fan-out → edge pods.

**Storage:**

- Orders: ~200 bytes/event × 300k/s × 86,400 ≈ 5 TB/day raw at 3-year peak.
- Trades: ~150 bytes × 150k/s × 86,400 ≈ 1.9 TB/day.
- Journal: ~300 bytes × 300k/s × 86,400 ≈ 7.8 TB/day.
- Compression (zstd) and partitioning reduce hot storage by 5–10x.
- ClickHouse retains 2 years; S3 retains 7–10 years.

These are upper-bound estimates for capacity planning, not current traffic.
Actual sizing is validated by load tests in B15.

### 4.3 Little's law

For a queue with arrival rate λ and latency W, the average number in system is
`L = λW`. At 30k orders/s and 1 ms matching latency, ~30 orders are in flight
per shard. Queue capacity of 10k provides ample burst absorption while keeping
worst-case latency bounded.

---

## 5. Backpressure and load shedding

### 5.1 Signals

- Ingress queue depth and age.
- WAL fsync latency.
- Raft commit latency.
- Settlement lag (event time vs. now).
- Database connection pool saturation.
- Kafka consumer lag.
- WebSocket send buffer depth.
- CPU throttling and GC pauses (Go).

### 5.2 Actions by level

| Level | Condition | Action |
|-------|-----------|--------|
| Green | Normal | None |
| Yellow | Queue > 60%, lag > 250 ms | Alert; reduce conflation interval |
| Orange | Queue > 80%, lag > 1 s | Reject new orders with 503; keep cancels |
| Red | Queue > 95%, lag > 5 s | Shed market orders; halt risk-increasing flow; page |
| Black | Invariant failure, WAL failure | Halt symbol/platform; preserve state |

### 5.3 Fairness

- Per-account fair queuing prevents one client from monopolizing a shard.
- Per-IP and per-key rate limits at the edge.
- Priority for risk-reducing actions.
- Market makers cannot exceed their configured share of ingress.

### 5.4 Retry storms

- Clients receive `Retry-After` with jitter guidance.
- The edge adds jitter to retries.
- Circuit breakers open after consecutive failures.
- Bulkheads isolate slow dependencies.
- Idempotency prevents duplicate effects during retries.

---

## 6. Caching

| Data | Cache | TTL | Invalidation |
|------|-------|-----|--------------|
| Instrument config | In-process | Until version change | Event + version check |
| API key metadata | Redis + in-process | 60 s | Revocation event |
| Session/JWT | Edge in-process | 15 min | Short TTL + revocation list |
| Fee schedule | In-process | 5 min | Event |
| Mark price | In-process | 1 s | Stream |
| Order book snapshot | Market-data | 100 ms | Sequence |
| Account snapshot | Risk-engine | Event-driven | Account events |
| Public tickers | Edge/CDN | 1 s | TTL |

Rules:

- Never cache balances for authorization decisions; read from account-core.
- Cache entries include a version/sequence; stale entries are rejected.
- Redis loss degrades performance, not correctness.
- Cache stampedes are prevented with single-flight and jitter.

---

## 7. Database scaling

### 7.1 Vertical first

- Provision for 2x peak CPU and IOPS.
- Use local NVMe for WAL and hot tables.
- Tune `shared_buffers`, `work_mem`, `effective_cache_size`, and autovacuum.
- Keep the working set in memory where possible.

### 7.2 Horizontal

- Partition large tables by time and hash.
- Shard accounts by user.
- Move historical partitions to cold storage.
- Use read replicas for reporting.
- Use connection pooling (PgBouncer) to cap connections.

### 7.3 Write batching

- Group commit for WAL.
- Batch settlement for the same account when possible.
- Batch funding payments per account.
- Use `COPY` for bulk imports, not individual inserts.

### 7.4 Avoiding hotspots

- Monotonic IDs can create index contention; use UUIDv7 or hash distribution.
- Avoid updating the same row for every trade (e.g. a global counter).
- Use per-shard sequences.
- Separate hot and cold columns to reduce bloat.

---

## 8. Event bus scaling

- Partition by `account_id` for account events; by `symbol_id` for market data.
- Consumer parallelism equals partition count.
- Rebalance is cooperative and incremental to avoid stop-the-world.
- Producer idempotence and transactions for outbox relay.
- Dead-letter topics per consumer with replay tooling.
- Lag is monitored per partition; a single hot partition triggers rekeying or
  more partitions (with a migration plan).

---

## 9. WebSocket scaling

### 9.1 Fan-out tree

```
Matching core
   -> market-data aggregators (per region)
      -> fan-out nodes (per AZ)
         -> edge pods (connections)
```

- Aggregators merge symbol streams and maintain snapshots.
- Fan-out nodes broadcast to edge pods over persistent connections.
- Edge pods only encode and send to clients.
- Adding edge pods does not increase core load.

### 9.2 Conflation

- Public channels may conflate to a maximum rate per client (e.g. 20 ms).
- Private channels are never conflated.
- Slow consumers are disconnected if their buffer exceeds the limit.
- Backpressure is per connection; one slow client cannot affect others.

### 9.3 Reconnect storm

- Clients receive a random reconnect delay.
- The edge rate-limits new connections per IP.
- Snapshots are served from a cache to avoid core load.
- Capacity is provisioned for 2x normal reconnect rate.

---

## 10. Multi-region

### 10.1 Topology

- **Edge:** active in multiple regions; DNS/anycast routes to the nearest healthy
  region.
- **Core:** single-writer per shard in the primary region to preserve
  linearizability.
- **DR:** warm standby in a secondary region with asynchronous replication of
  WAL, Postgres, and object storage.

### 10.2 Why not active-active core

Multi-region active-active for order books requires consensus across regions,
which adds 100+ ms latency and creates partition trade-offs. The business
requires low latency in one primary region; DR is a failover, not active-active.

### 10.3 Failover

1. Declare disaster and freeze writes in the primary (if reachable).
2. Verify the secondary has all committed WAL up to the last quorum commit.
3. Promote secondary cores and Postgres.
4. Rebuild derived state and reconcile.
5. Switch DNS/traffic.
6. Resume trading in reduce-only mode first, then full.

RPO: 0 for quorum-committed data in the primary region; < 5 minutes for a
regional disaster. RTO: < 4 hours.

---

## 11. Scalability checklist

- [ ] Every queue is bounded.
- [ ] Every service has a load-shedding policy.
- [ ] Every shard has a documented capacity limit.
- [ ] Every hot path has a p99 and p99.9 latency metric.
- [ ] Every cache has a version and invalidation path.
- [ ] Every consumer is idempotent and can replay.
- [ ] Every database query has an index and a plan review.
- [ ] Every external dependency has a timeout, retry, and circuit breaker.
- [ ] Every deploy is canaried and reversible.
- [ ] Every limit is configurable without a deploy.
