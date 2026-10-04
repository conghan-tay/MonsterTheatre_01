# Managing Long-Running Tasks — Condensed Design Playbook

Use this playbook when work outlives a normal request, needs specialized compute, or should survive server failure: video/image processing, report generation, imports/exports, bulk notifications, crawling, inference, and rate-limited third-party calls.

## Core architecture

Decouple acceptance from execution:

```text
client → API → job DB/outbox → durable queue → worker pool → result store
   ↑        job_id/status                      ↓
   └── polling, SSE/WebSocket, webhook, email/push notification
```

The API validates, records, and acknowledges work quickly. Workers execute independently and update durable job status. Scale web and worker fleets separately by their own resource profiles.

Use synchronous handling for quick, bounded operations. Use a simple queue for one background step. Use the multi-step-processes playbook when work has complex branches, dependencies, long waits, or compensation.

## API contract

### Submit

```http
POST /jobs
Idempotency-Key: <client-stable-key>

202 Accepted
{
  "job_id": "...",
  "status": "pending",
  "status_url": "/jobs/..."
}
```

- Authenticate, authorize, validate inputs, and check quotas before accepting.
- State exactly what `202` means: the job is durably recorded, not completed.
- Return the existing job for a repeated idempotency key.
- Store large inputs in object storage and queue references, not large payloads.

### Observe and control

- `GET /jobs/{id}` returns state, progress, timestamps, safe error details, and result reference.
- Optional cancellation marks intent; it may be best-effort once an irreversible step starts.
- Notify through polling, SSE/WebSocket, webhook, email, or push based on latency and client lifetime.
- Authorize every status/result lookup against job ownership/tenant.

## Durable acceptance

Avoid the gap where a database job row commits but queue publication fails.

Preferred transactional-outbox flow:

1. In one database transaction, insert the job and an outbox event.
2. Return success only after that transaction commits.
3. A relay publishes the outbox event to the queue and marks it sent.
4. Duplicate publication is harmless because workers deduplicate by job/event ID.

Alternatively, make the durable queue the acceptance source of truth and create status state idempotently. Do not claim that the API can keep accepting while the queue is down unless another durable handoff path exists.

## Job state model

```text
PENDING → RUNNING → SUCCEEDED
   ↓         ↓
CANCELLED  RETRY_WAIT → RUNNING
             ↓
           FAILED → DEAD_LETTER / MANUAL_REVIEW
```

Persist:

- Job ID, tenant/owner, type/schema version, priority, idempotency key.
- Input/result references, status, progress, attempt count.
- Creation/start/heartbeat/completion timestamps.
- Worker lease/receipt, next retry, last structured error.
- Parent/workflow ID and cancellation/deadline where relevant.

Status transitions should be conditional so stale workers cannot overwrite newer results.

## Queue choice

| Option | Best fit | Important tradeoff |
|---|---|---|
| Redis job library | Simple product queues and rich job features | Memory-first durability/operations must be understood |
| Managed queue such as SQS | Durable queue with low operational burden | Delivery semantics, visibility timeout, payload limits, per-message cost |
| RabbitMQ | Complex routing and controlled enterprise deployment | Cluster/disk operations |
| Kafka | Existing event platform, replay, high throughput, multiple consumers | Heavier queue semantics; ordering only within partitions |

Pick a technology the team can operate. Explain durability, ordering, retry, delay, leasing/visibility, DLQ, and payload limits rather than name-dropping several products.

## Worker model

Choose workers by job duration and resource profile:

- **Long-lived servers:** simplest debugging and no short execution cap; pay for idle capacity.
- **Serverless functions:** spiky, bounded-duration work; consider cold starts, execution/storage limits, and cost.
- **Containers/orchestrated workers:** varied hardware and autoscaling with more platform complexity.

Separate CPU-, memory-, GPU-, and I/O-heavy workloads so each pool scales and schedules independently.

Worker lifecycle:

1. Receive/lease message.
2. Load job and conditionally claim it.
3. Verify it is not already complete/cancelled.
4. Heartbeat/extend lease while processing.
5. Write output to the proper durable store.
6. Conditionally mark success.
7. Acknowledge the queue message only after durable success.

## Failure handling

### Worker crash and lost leases

Queues redeliver when a receipt/visibility lease expires. Set the initial timeout above normal processing time or extend it with heartbeats.

- Too long: recovery after a crash is slow.
- Too short: healthy jobs are redelivered during pauses or long phases.

Assume overlapping attempts can occur. A stale worker may finish after its lease expired, so use an attempt/lease token in conditional final writes or make outputs immutable per attempt and elect one result.

### Retry policy

- Classify transient, permanent, throttling, and bad-input failures.
- Retry transient errors with bounded exponential backoff and jitter.
- Honor provider retry-after hints and deadlines.
- After a small bounded number of failures, move to a DLQ/manual-review path.
- Monitor and alert on DLQ growth; provide audited replay tooling after the cause is fixed.

### Idempotency

Submission deduplication prevents impatient users from creating duplicate jobs; execution idempotency makes retries harmless.

- Use a client-supplied or deterministically derived idempotency key with a uniqueness constraint.
- Key external effects by stable job/step ID.
- Record completed outputs and return them on retry.
- For ambiguous irreversible effects, reconcile with the provider instead of blindly repeating.

## Backpressure and overload

Track queue depth **and oldest-message age**. Age maps directly to user delay and often scales workers better than CPU.

- Autoscale each worker pool from lag/age and resource saturation.
- Cap queue depth or admission rate according to completion-time SLO.
- Reject with a retryable busy response rather than accepting work that will expire before execution.
- Apply per-tenant quotas and fair scheduling.
- Coalesce duplicate/superseded jobs when only the latest state matters.
- Shed explicitly designated low-value work; never silently drop critical jobs.

## Mixed workloads and head-of-line blocking

Do not put five-second and five-hour jobs behind the same undifferentiated workers.

- Separate queues/pools by duration, priority, tenant class, or hardware need.
- Reserve capacity for latency-sensitive jobs.
- Chunk large jobs into independently retryable pieces when results can be merged.
- Keep priority bounded to prevent starvation.
- If duration is uncertain, set a time budget and reclassify/requeue safely rather than killing side effects midway.

## Progress, cancellation, and results

- Progress should be based on meaningful completed units, not elapsed-time guesses.
- Rate-limit progress writes/notifications; they can become a hotspot.
- Cancellation is a durable flag checked at safe points. Define whether partial outputs are deleted, retained, or compensated.
- Store files in object storage and return signed result URLs; keep metadata/status in the database.
- Define result retention and expiration, including cleanup of abandoned inputs and partial outputs.

## Job dependencies

For a short linear chain, one worker may durably enqueue the next step through an outbox before marking itself complete. Include a workflow/correlation ID and make each step independently retryable.

Use a workflow orchestrator when dependencies branch, join, wait on external events, require per-step compensation, or must remain visible/evolvable over a long lifetime.

## Observability

Monitor by job type and tenant:

- Submit/accept/reject rate.
- Queue depth, oldest age, dequeue latency, and redelivery rate.
- Running count, worker saturation, heartbeat/lease expiry.
- Duration percentiles, success/retry/failure/DLQ rate.
- Time spent in each state and stuck-job count.
- Idempotency hits, cancellation latency, and result-storage failures.

Carry job/correlation IDs through logs, queue messages, traces, and output metadata.

## Avoid these mistakes

- Holding an HTTP request open for work likely to exceed infrastructure/client timeouts.
- Enqueuing after committing the job row without an outbox/recovery mechanism.
- Acknowledging the message before output and job status are durable.
- Assuming queues provide exactly-once execution.
- Retrying non-idempotent effects without a stable key or reconciliation.
- Using CPU alone for autoscaling while queue age grows.
- Allowing infinite backlog or retry loops.
- Mixing radically different durations/resource types in one pool.
- Using a complex workflow engine for one background step—or hand-rolling chains once orchestration becomes complex.

## Design review checklist

- [ ] The operation cannot safely remain synchronous and the user contract is asynchronous.
- [ ] Acceptance is durable and atomic with eventual queue publication.
- [ ] Job states, conditional transitions, progress, cancellation, and retention are defined.
- [ ] Queue and worker choices match duration, throughput, ordering, and hardware needs.
- [ ] Lease/heartbeat/redelivery behavior and stale-worker fencing are covered.
- [ ] Submission and execution are idempotent; ambiguous effects reconcile.
- [ ] Retry limits, backoff, DLQ, repair, and replay are operationally usable.
- [ ] Backpressure uses queue age/depth, quotas, and bounded admission.
- [ ] Mixed workloads avoid head-of-line blocking and starvation.
- [ ] Results, large inputs, observability, and user notification are complete.
