# Dealing with Contention — Condensed Design Playbook

Use this playbook when concurrent actors can claim, modify, or decide based on the same scarce or strongly consistent state: seats, inventory, bids, balances, rate-limit counters, driver assignment, or uniqueness.

## First identify the invariant

Write the rule that must remain true, for example:

- A seat has at most one owner.
- Inventory never falls below zero.
- A bid is accepted only if it exceeds the current bid.
- A transfer does not overdraw an account.
- At least one engineer remains on call.

Then identify the smallest authoritative item that represents the contested resource. Protect the seat row, not merely a venue-level seat counter. A database can enforce only conflicts visible in the data it atomically checks.

## Decision ladder

Choose the first technique that fits:

1. **Unique constraint or atomic operation** — the invariant is directly representable by the store.
2. **Conditional write** — the decision is a predicate on the row/item being written.
3. **Pessimistic lock** — application code must read, decide, then write; conflicts are common.
4. **Optimistic concurrency control (OCC)** — the same read-decide-write shape, but conflicts are uncommon.
5. **Serializable transaction or materialized guard row** — the invariant spans rows that do not otherwise collide.
6. **Distributed lease/lock** — exclusivity must outlive one database transaction or span a wait/external call.
7. **Single-writer queue** — one extremely hot resource cannot sustain concurrent writes even with retries/locks.

Keep the invariant in one authoritative datastore whenever possible. If one atomic business operation truly spans services or shards, use the multi-step-processes playbook instead.

## Technique guide

### Unique constraints and conditional writes

Use a single atomic statement whenever the rule can be expressed in the write:

```sql
UPDATE seats
SET status = 'sold', user_id = :buyer
WHERE seat_id = :seat
  AND status = 'available';
```

- One affected row means success; zero means the condition lost.
- A zero-row update is not necessarily a database error. The application must inspect the result.
- Gate dependent writes on that result or wrap them in one transaction.
- Add unique/check constraints as the final integrity boundary where applicable.

For multiple dependent writes:

```sql
BEGIN;

WITH claim AS (
  UPDATE inventory
  SET available = available - 1
  WHERE sku = :sku AND available > 0
  RETURNING sku
)
INSERT INTO reservations (sku, order_id)
SELECT sku, :order_id FROM claim;

COMMIT;
```

The follow-up insert happens only if the claim succeeded.

### Pessimistic locking

Use `SELECT ... FOR UPDATE` when the application must inspect several values or perform logic that cannot be reduced to a write predicate, especially when collisions are frequent.

```sql
BEGIN;
SELECT * FROM seats
WHERE event_id = :event AND section = :section
ORDER BY seat_id
FOR UPDATE;

-- Choose and update the smallest valid seat set.
COMMIT;
```

Rules:

- Lock the smallest row set for the shortest time.
- Never hold a transaction lock while calling a payment API, waiting for a user, or doing slow I/O.
- Acquire multiple locks in one globally consistent order, such as ascending primary key.
- Treat deadlock and lock-timeout errors as retryable with bounded exponential backoff and jitter.
- Under heavy contention, locking can be cheaper and more predictable than repeated OCC retries.

### Optimistic concurrency control

Use a dedicated monotonically increasing version when conflicts are rare:

```sql
UPDATE auctions
SET high_bid = :new_bid,
    winner_id = :bidder,
    version = version + 1
WHERE auction_id = :id
  AND version = :version_seen
  AND high_bid < :new_bid;
```

- Zero affected rows means stale state or a failed business condition; re-read before deciding whether to retry.
- Keep retries bounded. Return a conflict when user input must be reconsidered.
- Prefer a dedicated version over timestamps with coarse precision.
- Avoid the ABA problem: a business value may change A→B→A and appear unchanged. A monotonic version does not.
- A one-way business value such as an ever-increasing bid can sometimes serve as the version.

### Serializable isolation and guard rows

Row-level techniques miss **write skew**, where transactions update different rows after reading overlapping state. Example: two on-call engineers simultaneously see the other active and both step down.

Options:

- Run the transaction at `SERIALIZABLE` and retry serialization failures.
- Materialize the invariant onto one guard row/cell and lock or conditionally update it.

Serializable isolation tracks more dependencies and may abort more work. Use it for genuine cross-row invariants, not as a blanket substitute for simpler guards.

### Distributed leases and reservations

Use a lease when exclusivity spans user think-time, a network call, or multiple transactions. Model it as data with an owner/token and expiration.

```sql
UPDATE seats
SET reserved_by = :user,
    reservation_token = :token,
    reserved_until = NOW() + INTERVAL '10 minutes'
WHERE seat_id = :seat
  AND (reserved_until IS NULL OR reserved_until < NOW());
```

Choices:

- **Database row:** strongest alignment with authoritative state; no new system; potentially slower/hotter.
- **Redis `SET NX` with TTL:** fast and convenient for soft holds; plan for Redis failure and lease-expiry races.
- **etcd/ZooKeeper lease:** stronger coordination semantics; higher operational cost.

Lease safety rules:

- Acquire atomically; never implement `GET` then `SET`.
- Release only when the stored owner/token matches the caller.
- An expired holder may continue running. For irreversible writes, validate the lease in the authoritative store or use an increasing fencing token so stale holders are rejected.
- Make the final commit conditional on the reservation token and current state.
- Treat TTL reservations primarily as a UX and traffic-shaping mechanism unless backed by an authoritative conditional write.

## Hot-resource strategy

Sharding does not solve a hot key: every writer still targets the same item.

1. Question whether the invariant can be relaxed or the resource divided.
2. Coalesce redundant updates or make noncritical counters eventually consistent.
3. If strict order is necessary, route all operations for that resource to one partition and process sequentially.
4. Buffer bursts with a durable queue, bound its depth, and expose overload rather than accepting infinite delay.
5. Partition distinct resources so different keys still scale independently.

A single writer removes conflicts but caps throughput for that resource. Provide failover and idempotent replay.

## Retry and side-effect rules

- Retry only recognized transient conflicts: deadlock, serialization failure, conditional-write loss when retry remains meaningful, or lease acquisition timeout.
- Use bounded exponential backoff with jitter; unlimited immediate retries amplify contention.
- Keep external side effects outside locked transactions.
- Attach an idempotency key to charges, emails, and other retryable effects.
- Return a clear business result (`sold_out`, `bid_too_low`, `reservation_expired`) rather than a generic server error.
- Measure conflict rate before switching between OCC and pessimistic locking.

## Common scenarios

| Scenario | Good starting point |
|---|---|
| Claim one seat / decrement stock | Conditional write plus authoritative constraint |
| Select four adjacent seats | Short pessimistic lock while selecting/claiming |
| Auction bid | OCC using a version or monotonic current bid |
| User checkout hold | Expiring reservation plus conditional final sale |
| Cross-row invariant | Serializable transaction or one guard row |
| Bank transfer in one database | Ordered row locks or OCC inside one transaction |
| Transfer across services/shards | Saga/workflow, idempotency, and compensation |
| Extremely hot strongly ordered item | Per-resource single-writer queue |

## Avoid these mistakes

- Performing read/check/write as separate unprotected operations.
- Protecting an aggregate counter but not the specific resource being claimed.
- Running dependent writes after a conditional update without checking affected rows.
- Holding database locks across user interaction or third-party calls.
- Using a plain Redis TTL lock as proof that two actors can never overlap.
- Using OCC under high contention without retry limits.
- Assuming repeatable-read isolation prevents all write skew.
- Adding a distributed lock when one database transaction already enforces the invariant.

## Design review checklist

- [ ] The business invariant and contention scope are explicit.
- [ ] The contested resource has one authoritative addressable record.
- [ ] The simplest fitting technique from the decision ladder is used.
- [ ] Conditional-write results and transaction boundaries are checked.
- [ ] Lock order, duration, timeout, and retry behavior are defined.
- [ ] OCC uses a monotonic version and handles ABA/retry exhaustion.
- [ ] Lease expiry, stale holders, fencing/final validation, and cleanup are covered.
- [ ] External effects are idempotent and outside long-held locks.
- [ ] Hot-key behavior and overload limits are addressed.
- [ ] Conflict, abort, retry, lock-wait, and oversell/double-claim metrics exist.
