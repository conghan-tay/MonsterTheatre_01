# Scaling Writes — Condensed Design Playbook

Use this playbook when sustained throughput, bursts, hot partitions, or fan-in/fan-out overwhelm the write path. Typical systems include telemetry, counters, location updates, search indexing, notifications, feeds, flash sales, and live interaction.

## Characterize the write workload

- Sustained and peak writes/second; burst duration and peak multiplier.
- Payload bytes/second and durability/acknowledgment requirement.
- Insert/append, update, increment, or read-modify-write shape.
- Required ordering and atomicity scope.
- Acceptable processing delay and data loss.
- Key distribution and hottest-key rate.
- Index, replication, and downstream fan-out per logical write.
- Whether individual events must be stored or aggregates are sufficient.

Do back-of-the-envelope math before proposing shards. A queue helps a short burst; it does not fix a steady-state sink that is permanently slower than its input.

## Escalation ladder

1. Remove avoidable work and scale the current database vertically.
2. Choose a write-appropriate data model/store.
3. Partition independent writes across shards.
4. Buffer short bursts with a durable queue.
5. Shed or coalesce low-value writes during overload.
6. Batch writes to amortize fixed cost.
7. Aggregate hierarchically when raw fan-in/fan-out is intractable.
8. Split hot aggregatable keys when one key exceeds one shard’s capacity.

## Optimize the single-node path

- Confirm whether disk I/O, CPU, locks, connections, or network is saturated.
- Batch commits where the durability contract permits it.
- Remove unnecessary indexes, triggers, synchronous derived work, and oversized payloads.
- Keep transactions short and avoid hot read-modify-write rows.
- Use append-only/log-structured storage for insert-heavy workloads.
- Use time-series storage for timestamped measurements and retention/downsampling.
- Use column-oriented analytics stores for batched analytical ingestion, not transactional point updates.

Fewer indexes improve writes but degrade reads. Make the tradeoff per workload rather than globally.

## Partitioning

### Horizontal sharding

Choose a partition key that:

- Distributes write volume evenly.
- Has high enough cardinality.
- Is stable over time.
- Co-locates data commonly read or transacted together.
- Avoids frequent scatter-gather reads.

Hashing a stable identifier such as `user_id` or `post_id` is a common baseline. Low-cardinality or skewed keys such as country can overload one shard.

For every important operation, answer:

- Which shard owns it?
- How does the client/router discover that shard?
- How many shards must the read/write touch?
- What happens when ownership changes?

### Vertical partitioning

Separate fields or tables with different workloads, for example:

- Immutable post content: read-optimized relational storage.
- Rapid counters: specialized counter/in-memory store.
- Append-only events: log or time-series store.

This prevents high-frequency metric writes and analytical scans from contending with core transactional data.

### Resharding without downtime

1. Publish a versioned old→new ownership map.
2. Backfill historical data with checkpoints.
3. During migration, dual-write or log writes for replay.
4. Read from the new owner first with a controlled fallback to old.
5. Validate counts/checksums and lag.
6. Cut reads over, stop old writes, then retire old data after a safety window.

Dual writes can diverge on partial failure. Prefer a durable source log/change stream when available; make writes idempotent and continuously reconcile.

## Queues for bursts

The acceptance tier validates a request, durably enqueues it, and returns an accepted/job status. Consumers write at the downstream system’s sustainable rate.

Use when:

- Bursts are temporary.
- The product tolerates delayed visibility.
- Per-key ordering can be preserved through queue partitioning.

Define:

- What “accepted” guarantees: in memory, replicated queue, or committed business state.
- Maximum queue depth/age and the corresponding user experience.
- Partition key and consumer concurrency.
- Retry, idempotency, poison-message/DLQ, and replay behavior.
- Backpressure when enqueue rate remains above drain rate.

Scale workers using queue lag/oldest-message age, not CPU alone. Bound backlog; do not accept work that will miss its usefulness deadline.

## Load shedding and coalescing

When overloaded, preserve high-value work instead of allowing everything to fail.

- Drop replaceable stale updates: a new driver location supersedes one from seconds ago.
- Coalesce multiple updates to the latest state per entity.
- Sample low-value analytics while preserving critical events.
- Prioritize clicks/purchases over impressions/background metrics.
- Apply tenant/user quotas and rate limits.
- Return explicit throttling with retry guidance for work that cannot be silently dropped.

Document which data may be dropped and the maximum allowed loss. Never shed financial, authorization, or other correctness-critical writes merely for convenience.

## Batching

Batch at the earliest safe layer:

- **Client/producer:** fewer network calls; risk of losing acknowledged in-memory batches.
- **Queue consumer/batcher:** durable input and efficient bulk database writes.
- **Database/WAL:** amortized flush cost; changes the durability/latency tradeoff.

Choose batch size/window from throughput and latency SLOs. Batching helps only when multiple compatible writes arrive within the window. Measure compression ratio: input events divided by output writes.

For counters, aggregate deltas by key in the batch, then write one delta per key. Make batch commits idempotent using a batch/event ID or committed source offset.

## Hierarchical aggregation

Use when millions of producers need a shared eventually consistent view and one root cannot receive/redistribute every raw update.

```text
producers → partitioned local aggregators → merge/root → broadcast nodes → clients
```

- Partition incoming events by aggregation key.
- Aggregate over bounded windows at local nodes.
- Merge compact partial results centrally or through multiple levels.
- Fan the merged result out through broadcast nodes.

Each level reduces volume but adds latency and partial-failure complexity. Define window boundaries, late-event handling, deduplication, and whether aggregates are exact or approximate.

## Hot keys

Normal sharding cannot split one extremely popular key.

For aggregatable values such as likes or views:

- Split every key into a small fixed number of subkeys, or split only detected hot keys.
- Randomly/hash writers across `key:0..k-1`.
- Readers sum all active subkeys or consume a periodically materialized total.
- Keep split metadata discoverable and versioned so readers and writers agree.

Costs: read amplification, extra storage, more complex invalidation, and temporary inconsistency. Do not use this for state that must remain one atomic value.

If a hot resource requires strict total order, use a per-resource single-writer partition/queue and accept its throughput ceiling.

## Correctness and operability

- Use stable event/idempotency IDs; assume at-least-once delivery.
- Preserve order only within the smallest necessary key scope.
- Store large payloads externally and queue references.
- Separate accepted, persisted, processed, and visible timestamps.
- Reconcile queue/log offsets with sink state.
- Monitor write rate/bytes, p95/p99 commit latency, error/throttle rate, shard skew, hottest keys, queue age/depth, batch efficiency, replication lag, and dropped/coalesced volume.

## Avoid these mistakes

- Sharding before showing that one tuned node is insufficient.
- Picking a partition key without analyzing skew and read fan-out.
- Claiming a queue increases sustainable sink throughput.
- Returning success before work is durable without stating that weaker guarantee.
- Allowing an unbounded backlog.
- Batching rare keys where no writes combine.
- Dual-writing during resharding without idempotency/reconciliation.
- Splitting a value that must be updated atomically.
- Ignoring how write optimizations amplify or slow reads.

## Design review checklist

- [ ] Sustained/peak throughput, bytes, burst duration, and write amplification are quantified.
- [ ] Durability, acknowledgment, ordering, atomicity, and delay/loss tolerances are explicit.
- [ ] Single-node bottlenecks and database tradeoffs are validated first.
- [ ] Partition key, shard routing, skew, and scatter-gather behavior are justified.
- [ ] Resharding has a versioned, idempotent, observable migration plan.
- [ ] Queues have backlog limits, backpressure, retry, DLQ, and replay semantics.
- [ ] Load shedding/coalescing preserves critical writes and is measurable.
- [ ] Batch windows and aggregation levels meet latency requirements.
- [ ] Hot-key strategy matches whether the value is aggregatable or strictly ordered.
