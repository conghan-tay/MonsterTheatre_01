# Scaling Reads — Condensed Design Playbook

Use this playbook when read traffic, dataset size, repeated computation, or global latency overwhelms the primary data path. Common cases include public content, feeds, catalogs, URL redirects, profiles, and analytics views.

## Quantify before adding infrastructure

For each high-volume read path, establish:

- Requests/second, peak-to-average ratio, and read/write ratio.
- Query shape, rows scanned/returned, payload size, and fan-out.
- Latency SLO and acceptable staleness.
- Access skew: how much traffic targets the hottest keys?
- Public/shared vs tenant/user-specific data.
- Dataset and working-set size, growth, and geographic distribution.
- Consistency requirements: read-your-writes, monotonic reads, or eventual consistency.

Measure database CPU, I/O, connections, query latency, buffer-cache hit rate, and rows examined. “Faster reads” and “less primary-database load” are different goals; state which one is constrained.

## Escalation ladder

Apply these in order until the measured/SLO problem is solved:

1. Fix query and data-access inefficiency.
2. Improve indexes and pagination.
3. Scale the database vertically.
4. Precompute or denormalize expensive read models.
5. Add read replicas when the dataset fits but primary read throughput does not.
6. Add application caching for repeated hot reads.
7. Add CDN/edge caching for shared globally accessed content.
8. Shard only when one dataset/server is the limiting factor and the partitioning tradeoff is justified.

## Optimize the database first

### Queries and indexes

- Inspect query plans; fix full scans, accidental N+1 reads, unnecessary columns, and unbounded joins.
- Index columns used in high-volume filters, joins, and ordering.
- Design composite indexes from the actual predicate and sort order; leftmost column order matters.
- Prefer keyset/cursor pagination over large offsets.
- Keep index count intentional: every index consumes storage and adds write work, but under-indexing a hot read path is usually worse.
- Cap result sizes and avoid loading entire objects when a projection suffices.

### Vertical scaling

More RAM expands the hot working set; faster SSDs reduce random I/O; more CPU/cores increase query concurrency. This is often the fastest low-complexity step and buys time before distributing state.

### Denormalized read models

Use denormalization or materialized views when reads repeatedly perform the same joins or aggregation.

- Store the fields needed by the read path together.
- Define how updates propagate and how much staleness is acceptable.
- Rebuild read models from an authoritative source when possible.
- Separate user-facing read models from write-normalized source tables.

This trades write complexity and storage for lower read latency.

## Read replicas

One leader accepts writes; followers serve eligible reads.

Use when:

- The data fits on one node but read QPS exceeds one primary.
- Slightly stale results are acceptable for most endpoints.
- Replicas also improve failover/read availability.

Design choices:

- **Asynchronous replication:** lower write latency and more scalable; reads may lag.
- **Synchronous replication:** fresher/safer reads; writes wait for replicas and availability may drop.

Mitigations for replica lag:

- Route a user to the leader for a bounded period after their write.
- Return a commit position/version and read from a replica only after it has caught up.
- Keep strongly consistent inventory, permissions, and financial decisions on the leader/authoritative path.
- Monitor replay lag in both time and bytes; stop routing to badly lagging replicas.

Replicas multiply read throughput but do not reduce per-node dataset size.

## Application cache

Cache-aside is the common baseline:

1. Read cache.
2. On miss, fetch from the authoritative store.
3. Populate cache with a TTL.
4. Return the value.

Choose keys from the read contract, including tenant, locale, permissions scope, representation/version, and relevant query parameters.

### Invalidation options

| Strategy | Good for | Tradeoff |
|---|---|---|
| TTL | Bounded-staleness data | Stale until expiry; expiry can stampede |
| Delete/update on write | Entity data requiring faster visibility | Failure/race handling on the write path |
| Async invalidation event | Lower write latency, many consumers | Temporary staleness; delivery/retry required |
| Tagged invalidation | Many entries depend on one entity | Tag index complexity |
| Versioned keys | Entity reads with atomic version changes | Extra version lookup and stale-key cleanup |

Use the staleness SLO to choose TTL rather than picking an arbitrary number. Combine active invalidation with TTL as a safety net for important data.

### Cache failure policy

- Decide whether cache failure falls back to the database, returns stale data, or sheds load.
- A full unrestricted fallback can turn a cache outage into a database outage.
- Use timeouts, circuit breakers, concurrency limits, and stale-if-error for eligible data.
- Apply TTL jitter so many keys do not expire simultaneously.

## CDN and edge caching

Use for content reused across users: public posts, catalog pages, thumbnails, static assets, public API responses.

- Keep private/user-specific data out unless the cache key and authorization model make isolation explicit.
- Set `Cache-Control`, `Vary`, surrogate keys/tags, and versioned URLs deliberately.
- Use signed URLs/cookies for protected media when appropriate.
- Purges are not instantaneous everywhere; design critical removals with short TTLs, version changes, or a small deny/deletion filter at serving time.

CDNs reduce both origin load and geographic latency. They provide little benefit for data requested by only one user.

## Sharding for reads

Shard when the dataset or aggregate throughput no longer fits one database even after optimization/caching.

- Hash partitioning spreads load; range/geographic partitioning supports locality but risks skew.
- Co-locate data read together.
- Calculate the number of shards touched by each important query.
- Avoid scatter-gather on high-volume endpoints.
- Functional sharding separates domains with independent access patterns.

Sharding adds routing, resharding, cross-shard joins, distributed transactions, and operational complexity. It is usually a later step for read scaling.

## Hot keys and stampedes

### One key overwhelms the cache

- Add local/in-process caching in front of the shared cache.
- Coalesce concurrent fetches per key on each application server.
- Replicate/fan out a hot cache entry across multiple keys/nodes when network or serialization on one node is the limit.
- Push widely shared content to a CDN.

Fan-out improves throughput but increases memory and invalidation work.

### Many requests rebuild an expired value

- Single-flight/request coalescing allows one rebuild per process or distributed scope.
- Serve stale while one background refresh runs.
- Refresh probabilistically before expiry to spread rebuild work.
- Proactively refresh the few critical always-hot entries.
- Add TTL jitter and retain a stale fallback.

### Immediate visibility is required

For an entity, increment a database version in the same transaction as the update and include it in the cache key, such as `event:123:v43`. Old versions become unreachable and expire naturally. This is less useful for search results and broad aggregations; use explicit invalidation or rebuild those read models.

## Common scenarios

| Scenario | Starting design |
|---|---|
| URL redirect | Aggressive Redis/edge cache; durable mapping store on miss |
| Public product/event page | CDN + application cache; separate live inventory path |
| Feed | Precomputed recent feed/read model + pagination + cache |
| Video metadata/thumbnails | CDN for media; cache metadata; batch eventually consistent counters |
| User sees their recent write | Leader read or replication-position-aware routing |
| Critical inventory/financial decision | Authoritative strongly consistent read; do not trust stale cache |

## Avoid these mistakes

- Adding replicas/caches before checking the query plan and indexes.
- Caching everything without a staleness or invalidation contract.
- Serving authorization-sensitive or scarce-resource state from a stale shared cache.
- Sending read-after-write traffic to an arbitrarily lagging replica.
- Letting cache failure create an unlimited database fallback storm.
- Ignoring hot-key load because the overall cache hit rate looks healthy.
- Sharding while hot endpoints still scatter to every shard.
- Using read-scaling machinery when the real problem is writes or real-time propagation.

## Design review checklist

- [ ] Hot read paths, QPS, latency, staleness, and consistency are quantified.
- [ ] Query plans, indexes, pagination, and payload size are addressed first.
- [ ] Read models/denormalization have an update and rebuild strategy.
- [ ] Replica lag and read-your-writes behavior are explicit.
- [ ] Cache keys include isolation and representation dimensions.
- [ ] TTL, invalidation, stampede protection, and cache-outage policy are defined.
- [ ] CDN use matches content sharing and privacy constraints.
- [ ] Hot keys, scatter-gather, and regional latency/failure are covered.
- [ ] Database, cache, and edge metrics can identify the actual bottleneck.
