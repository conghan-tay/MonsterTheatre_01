# Multi-Step Processes — Condensed Design Playbook

Use this playbook when a business operation spans multiple durable steps, services, callbacks, human waits, or compensating actions: order fulfillment, payments, onboarding, ride matching, approvals, and AI-agent pipelines.

## Recognition signals

- “If step X fails, undo Y.”
- Work may run for minutes, days, or months.
- A webhook or human action resumes the process later.
- The initiating server can crash or deploy mid-process.
- Steps cross transactional boundaries or independently owned services.
- Operators need an audit trail, retries, manual recovery, or workflow visibility.
- The process has branching, parallelism, timers, or evolving definitions.

Do not introduce a workflow for ordinary CRUD, a single transaction, one background job, or a latency-sensitive synchronous request.

## Choose the smallest mechanism

| Process shape | Use |
|---|---|
| All writes fit one database transaction | Local transaction |
| One independent slow/retryable step | Durable queue + idempotent worker |
| Several local commits need undo actions | Saga |
| Mid-complexity reactions owned by independent services | Event-driven choreography |
| Complex, centrally governed sequence | Workflow orchestration |
| Long-lived, code-heavy flow with waits/callbacks | Durable execution engine such as Temporal |
| Cloud-native state machine and low operational burden | Managed workflow service such as Step Functions |
| Scheduled batch/ETL DAG | Batch orchestrator such as Airflow |

Avoid two-phase commit across slow or external services: participants hold resources while waiting, coordinator failure stalls progress, and third parties usually cannot participate. A saga accepts temporary inconsistency and repairs it with compensation.

## Model the workflow before choosing technology

For every step, define:

- Input/output and durable identifier.
- Timeout and retry policy.
- Whether retry is safe; idempotency key.
- Permanent vs transient failures.
- Compensation and whether compensation can itself fail.
- State visible to users/operators.
- Events/signals that unblock the step.
- Ownership and required ordering.

A compact state model might be:

```text
PENDING → PAYMENT_COMPLETE → INVENTORY_RESERVED → SHIPPED → COMPLETE
              ↓ failure              ↓ failure
         REFUND_PENDING       RELEASE_PENDING
              ↓                    ↓
           COMPENSATED / MANUAL_REVIEW
```

Make intermediate states explicit. “Pending” is a product state, not merely an implementation detail.

## Saga design

A saga is a sequence of independently committed local transactions with compensations.

```text
charge payment       ↔ refund payment
reserve inventory    ↔ release inventory
create shipment      ↔ cancel shipment
send confirmation    ↔ send correction (often not truly reversible)
```

Rules:

- Compensate in reverse dependency order.
- Compensation restores a valid business state; it need not recreate the exact prior world.
- Some effects are irreversible. Delay them until commitment or define a forward recovery action.
- Forward steps and compensations both need retries, idempotency, alerting, and a manual escape hatch.
- Record business intent and each step result durably before relying on it after a crash.

### Choreography

Services consume durable events, perform local work, and emit the next event.

Benefits:

- Loose runtime coupling and independent scaling.
- Durable log provides replay and an audit trail.
- Adding an independent reaction is straightforward.

Costs:

- The end-to-end flow is implicit across event contracts.
- Debugging causality and changing the middle of the flow require strong tooling and coordination.
- Consumers must tolerate duplicates and preserve per-workflow ordering, usually by partitioning on `workflow_id`.

Use choreography for a moderate number of reactions across independently owned domains. Keep correlation IDs and causation IDs on every event.

### Orchestration

A coordinator owns the sequence and commands workers.

Benefits:

- One visible definition of the process.
- Central handling for timeouts, retries, branches, compensations, and operator controls.
- Easier to answer “where is this order and why is it stuck?”

Costs:

- Coordinator/engine is critical infrastructure.
- Workflow code or definitions need compatibility across long-lived executions.
- Excessively large workflows can become centralized business-logic bottlenecks.

Use orchestration once choreography’s implicit control flow becomes harder to operate than a central coordinator.

## Durable execution model

In engines such as Temporal:

- A **workflow** contains deterministic control flow.
- An **activity** performs nondeterministic I/O and external side effects.
- The engine stores an append-only execution history.
- After a worker crash, workflow code replays; completed activity results come from history rather than running again.
- A signal/event wakes a workflow waiting on a human, webhook, or other external action without holding a thread.

Obligations:

- Workflow code must make the same decisions for the same recorded history. Do not directly read the network, database, wall clock, or randomness from workflow code unless the SDK provides a replay-safe primitive.
- Activities must be idempotent because a crash can occur after the effect succeeds but before success is recorded.
- Keep large payloads in object/database storage and pass references through workflow history.
- Use durable timers rather than sleeping a worker process.
- Periodically continue as a new execution/snapshot when histories grow without bound.

Declarative state machines store the current state server-side rather than rebuilding it through code replay. They are easier to visualize and operate, but less expressive. Pick one model and explain its recovery and versioning semantics.

## Correctness patterns

### Idempotent effects

Derive a stable key from workflow and step, for example `order-123:charge:v1`. Pass it to downstream providers when supported and keep a local effect record.

```text
NOT_STARTED → IN_PROGRESS → COMPLETED(result)
```

- A retry seeing `COMPLETED` returns the recorded result.
- `IN_PROGRESS` is ambiguous: reconcile with the external provider instead of blindly repeating an irreversible effect.
- Network delivery is at-least-once; “exactly once” means the business effect is deduplicated.

### Atomic state + event publication

Do not commit business state and publish the next event as unrelated operations. Use a transactional outbox:

1. Update local business state and insert an outbox row in one database transaction.
2. A relay publishes outbox rows to the broker.
3. Mark published rows; tolerate duplicate publication.
4. Consumers deduplicate by event ID.

### Timeouts and retries

- Give each step an execution timeout and a schedule-to-start/queue timeout.
- Retry transient errors with exponential backoff and jitter.
- Do not retry validation failures or exhausted business conditions.
- Bound attempts, then compensate, pause, or send to manual review.
- Keep a whole-workflow deadline distinct from per-step timeouts.

### External events

- Address a workflow by stable ID and signal it through the engine/API.
- Authenticate and deduplicate signals.
- Handle signal-before-wait races by recording the signal durably; do not rely on an in-memory waiter.
- Use durable timers for reminders and expiry paths.

## Workflow evolution

Long-lived executions may resume under new code.

Options:

- **Version pinning:** old executions finish on old definitions; new executions use the new version.
- **Replay-safe patching:** record a version marker so old history follows the old branch and new executions follow the new one.
- **Migration/restart:** transform durable state or start a successor workflow when a mandatory change must affect running instances.

Test new workflow code by replaying representative production histories. Keep event schemas backward/forward compatible during rolling changes.

## Operational model

Persist and expose:

- Workflow ID, type/version, current business state, and timestamps.
- Step attempts, inputs by reference, outputs by reference, and last error.
- Next retry/timer, compensation state, and manual-review reason.
- Correlation/causation IDs across logs and events.

Monitor start-to-finish latency, time per state, retry rate, stuck workflows, compensation failures, worker/queue lag, history size, signal delay, and manual-review volume.

Operator actions should be explicit and audited: retry step, skip only when safe, compensate, terminate, or migrate.

## Avoid these mistakes

- Keeping progress only in application memory.
- Saying “the queue retries it” without making the effect idempotent.
- Treating compensation as an infallible rollback.
- Publishing the next event separately from the local state commit.
- Using choreography for a process whose overall path no one can reconstruct.
- Putting nondeterministic I/O in replayed workflow code.
- Deploying incompatible workflow code without versioning or history replay tests.
- Passing large documents through workflow history instead of references.
- Using a workflow engine for one simple background job.

## Design review checklist

- [ ] The process cannot be reduced to one local transaction or one queue job.
- [ ] Every step, state, retry, timeout, and compensation is defined.
- [ ] Progress survives crashes and can resume on another worker.
- [ ] Effects and compensations are idempotent; ambiguous outcomes reconcile.
- [ ] State changes and emitted events use an outbox or equivalent atomic mechanism.
- [ ] Event ordering, correlation, deduplication, and schema evolution are explicit.
- [ ] External waits use durable signals/timers.
- [ ] Running workflows remain compatible across deployments.
- [ ] History/payload growth is bounded.
- [ ] Operators can see, repair, compensate, and audit stuck executions.
