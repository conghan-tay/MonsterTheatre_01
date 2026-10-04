# Real-Time Updates — Condensed Design Playbook

Use this playbook when clients must learn about server-side changes with bounded delay: chat, collaborative editing, live dashboards, auctions, tracking, presence, or streaming AI output.

## Core model: solve two hops

A real-time design is incomplete until it explains both:

1. **Server → client:** how an update reaches the device.
2. **Event source → connection server:** how the server holding that client connection learns about the update.

Start by quantifying:

- Required latency: milliseconds, sub-second, or merely a few seconds?
- Direction: server-to-client only, or frequent traffic both ways?
- Event rate and payload size per connection.
- Concurrent connections and fan-out per event.
- Ordering scope: global, per user, per room, or per document?
- Delivery expectation: best effort, resumable, or no missed events?
- Connection lifetime and client environment: browser, mobile, proxies, NAT, flaky networks.
- State held per connection and the cost of rebuilding it.

## Choose the client connection

| Mechanism | Choose it when | Main cost / limitation |
|---|---|---|
| Simple polling | Seconds of delay are acceptable; updates are secondary; the observation window is short | Repeated empty reads, interval-bound latency, database load |
| Long polling | Updates are infrequent but should arrive soon; standard HTTP is valuable | One response per update, reconnect latency, long-request timeouts and connection limits |
| Server-Sent Events (SSE) | Updates are one-way and may be frequent: token streams, dashboards, notifications | No client→server channel; proxies may buffer; long-lived HTTP operations need tuning |
| WebSocket | Reads and writes are both frequent and low latency matters: chat, games, collaboration | Stateful connections, harder load balancing/deployments, explicit recovery required |
| WebRTC | Peers exchange audio/video or high-volume peer data directly | Signaling plus STUN/TURN, NAT/firewall complexity, slower setup |

### Escalation rule

Use the simplest mechanism that meets the latency requirement:

1. Poll if a 2–5 second delay is acceptable.
2. Use long polling for infrequent near-real-time events.
3. Prefer SSE for a one-way stream; ordinary HTTP can carry occasional client writes.
4. Use WebSockets for sustained, high-frequency, bidirectional traffic.
5. Use WebRTC only when peer-to-peer media/data materially reduces latency or server bandwidth.

### Protocol-specific notes

**Polling**

- Query incrementally with a cursor, sequence, or `updated_after`, not the entire state.
- Set the interval from the staleness SLO and calculate aggregate QPS: one million clients polling every ten seconds means roughly 100,000 reads/second.
- HTTP keep-alive reduces connection setup overhead.

**Long polling**

- Hold a request until data arrives or a bounded timeout expires; the client immediately reconnects.
- Align client, proxy, load balancer, and server timeouts. A 15–30 second request window is a practical starting point.
- It performs poorly during bursts because each response must complete before the next request exists.

**SSE**

- Send `text/event-stream`; disable unwanted proxy buffering and caching.
- Give every event an ID. On reconnect, resume after the client's last received ID.
- Use heartbeats/comments to keep intermediaries from treating an idle stream as dead.

**WebSocket**

- Authenticate during the HTTP upgrade, then authorize individual actions and subscriptions.
- Terminate connections in a dedicated gateway tier so the rest of the application remains stateless.
- Use a WebSocket-aware load balancer; least-connections is often better than round-robin.
- Detect half-open connections with ping/pong heartbeats.
- Drain during deployment: stop new connections, ask existing clients to reconnect, and retain resumable event history.

**WebRTC**

- A signaling service introduces peers and exchanges connection candidates.
- STUN tries to establish a direct path; TURN relays traffic when direct traversal fails.
- The signaling path is itself a real-time system, commonly implemented with WebSockets or SSE.

## Route events to the right connection server

### 1. Pull from durable state

Clients or endpoint servers poll a database/log for events after a cursor.

Use when latency is relaxed and simplicity matters. The durable store decouples producers from consumers, but frequent polling creates read amplification.

### 2. Route by consistent ownership

Hash a stable entity such as `user_id`, `room_id`, or `document_id` to the connection server that owns its state.

Use when each entity has expensive in-memory state that should live on one server. Keep membership in a coordination system and use consistent hashing/virtual nodes so scaling moves only a fraction of connections.

During rebalance:

1. Publish old and new ownership.
2. Route events to both owners during the transition.
3. Gradually reconnect or transfer affected clients.
4. Retire the old assignment after the handoff is confirmed.

Costs: coordination, routing metadata, failure recovery, and state loss/rebuild when an owner dies.

### 3. Publish/subscribe

Producers publish to topics; lightweight endpoint servers subscribe and forward events over local client connections.

Use when endpoint servers should be interchangeable and events fan out broadly. Partition topics by the ordering/routing key and scale the broker cluster. Account for broker availability, many-to-many broker↔endpoint connections, and subscription lifecycle.

For most systems, a pub/sub backbone plus stateless connection gateways is the best starting architecture. Prefer consistent ownership only when connection-associated state is substantial.

## Reference architecture

```text
event producer → durable DB/log → pub/sub partitions
                                  ↓
client ↔ connection gateway pool ↔ subscription/topic mapping
             ↓
       status/metrics store
```

- The database or log is the recovery source of truth; pub/sub is the low-latency distribution path.
- Partition related events by the entity whose order matters.
- Connection gateways keep only ephemeral connection/subscription state.
- Persist recent events or checkpoints so reconnection does not depend on gateway memory.

## Delivery, ordering, and recovery

- Assign a monotonic sequence within the required ordering scope.
- Include `event_id`, entity key, sequence, schema version, and creation time.
- Treat delivery as at-least-once unless a weaker guarantee is explicitly acceptable; deduplicate by event ID.
- On reconnect, send the last acknowledged sequence and replay later events from durable storage.
- Detect gaps. Re-fetch a snapshot when replay history has expired or a gap cannot be filled.
- Funnel strongly ordered events for one entity through one partition/owner. Avoid claiming global order unless it is truly required.
- Bound per-connection buffers. Disconnect, degrade, or switch slow clients to snapshot recovery rather than allowing unbounded memory growth.
- Use exponential backoff with jitter for reconnect storms; cap retries and honor server retry hints.

## Fan-out and overload

- Do not perform a database write per recipient for a massive broadcast unless read requirements justify fan-out-on-write.
- Cache the shared event once, then distribute through regional or hierarchical broadcast layers.
- Batch/coalesce high-frequency state such as cursor positions or counters; transmit the latest value when intermediate states have no value.
- Apply per-tenant and per-connection quotas.
- Prioritize critical events over presence, typing, or cosmetic updates.
- Track connection count, reconnect rate, fan-out width, broker lag, per-client buffer size, delivery latency, and dropped/coalesced events.

## Common failure scenarios

| Failure | Response |
|---|---|
| Zombie connection | Heartbeat timeout, clean local mappings, client reconnect with jitter |
| Gateway crash | Reconnect elsewhere and resume from last acknowledged event |
| Missed pub/sub message | Replay from durable log/DB; pub/sub alone is not the recovery record |
| Duplicate event | Idempotent client handler keyed by event ID |
| Out-of-order event | Per-entity sequence; buffer briefly or refresh snapshot on gaps |
| Slow consumer | Bounded queue, coalescing, lower fidelity, or disconnect and resync |
| Deploy/rebalance storm | Connection draining, staged rollout, retry jitter, dual routing during ownership changes |
| Celebrity fan-out | Cache once, partition/hierarchically broadcast, avoid per-user synchronous work |

## Avoid these mistakes

- Choosing WebSockets before establishing that polling or SSE misses the SLO.
- Explaining only the browser connection and omitting server-side event routing.
- Treating an in-memory gateway as the durable source of truth.
- Forgetting proxy/load-balancer idle timeouts, buffering, or WebSocket support.
- Promising exactly-once network delivery instead of idempotent effects and replay.
- Ignoring reconnection, deployments, slow consumers, or fan-out hotspots.

## Design review checklist

- [ ] Latency, direction, event frequency, connection count, and fan-out are quantified.
- [ ] The connection protocol is justified against a simpler option.
- [ ] Event source → gateway routing is explicit.
- [ ] Ordering and delivery scopes are narrowly defined.
- [ ] Reconnect, resume, deduplication, and gap recovery are designed.
- [ ] Gateway state, broker state, and durable source-of-truth state are distinguished.
- [ ] Backpressure, hot broadcasts, deploy draining, and regional failure are covered.
- [ ] Authentication renewal, authorization, quotas, and observability are covered.
