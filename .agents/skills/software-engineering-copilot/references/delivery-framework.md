# System Design Delivery Framework

Use this framework to guide a software project from an initially vague idea to an agreed, implementation-ready system design.

## Operating principle

Build the design in this order:

```text
Requirements → Core entities → Interfaces → Optional data flows
             → High-level design → Deep dives → Design sign-off
```

The first five stages establish a complete, simple system that satisfies the functional requirements. Deep dives then harden that system against the most important non-functional requirements, bottlenecks, and failure modes.

Do not introduce caches, queues, shards, workflow engines, or other distributed machinery before identifying the concrete requirement they solve. Record promising concerns for the deep-dive stage instead of interrupting the basic design.

Use the completion gates below instead of fixed time boxes. Requirements and interfaces should stay concise; high-level design and deep dives should receive most of the attention.

## Collaboration checkpoint

At each stage:

1. Summarize the current understanding.
2. Identify the single most important unresolved issue.
3. Recommend a default and explain why it fits.
4. Ask the user for the decision that materially affects the next stage.
5. Incorporate the decision into the design and decision log before continuing.

Do not ask the user to decide implementation details that can safely remain flexible. Group closely related questions, but do not bundle unrelated architectural decisions into one approval.

## Stage 1: Requirements

The goal is a small, prioritized definition of the system to build—not a catalog of every feature it might eventually support.

### 1.1 Functional requirements

Write functional requirements as observable capabilities:

> A user or client should be able to …

Identify the primary actors, then the few user journeys that define the product. Prefer roughly three core capabilities for the first design pass. Mark secondary features as deferred rather than silently expanding scope.

For each capability, clarify:

- Trigger and actor.
- Expected successful outcome.
- Important validation or authorization rule.
- State created or changed.
- Meaningful failure visible to the actor.

Questions should be targeted and decision-producing. Ask whether a capability exists, what outcome matters, or which behavior has priority—not broad prompts such as “What else?”

### 1.2 Non-functional requirements

Select only the qualities that materially shape this system. Make them workload- and operation-specific rather than saying every desirable property matters equally.

Consider:

- **Consistency and availability:** Which operations require current/strongly consistent state? Which may remain available with stale state during a partition or outage?
- **Scale:** Expected users, data volume, read/write ratio, fan-out, burstiness, and growth.
- **Latency:** Target percentiles for named operations, such as “search p95 under 500 ms.”
- **Durability:** Which data cannot be lost? What recovery-point and recovery-time objectives matter?
- **Fault tolerance:** Which component or regional failures must the system survive, and how should behavior degrade?
- **Security and privacy:** Authentication, authorization, tenant isolation, encryption, secrets, abuse controls, and auditability.
- **Compliance and residency:** Retention, deletion, legal, regulatory, and geographic constraints.
- **Environment:** Mobile/offline use, bandwidth, battery, device memory, browser limits, on-premises deployment, or an established project stack.
- **Operability and cost:** Observability, support burden, deployment constraints, and important cost ceilings.

Quantify targets when the number changes a design choice. Avoid generic requirements such as “highly scalable” or “low latency.”

### 1.3 Capacity estimation

Calculate only quantities that can change the architecture or validate a bottleneck. Useful estimates include:

- Average and peak request/write rate.
- Concurrent connections or active jobs.
- Storage growth and retention footprint.
- Network ingress/egress and large-object bandwidth.
- Fan-out per event.
- Working-set size for memory/cache decisions.
- Queue drain time at peak and normal worker capacity.

State assumptions and use ranges when precision is unavailable. End each calculation with the decision it informs, for example: “A single indexed database is sufficient,” “the connection tier must scale horizontally,” or “uploads should bypass the API servers.” Do not perform arithmetic merely to conclude that the system is large.

### Requirements completion gate

Proceed when:

- Core functional requirements are prioritized and testable.
- The non-functional requirements most likely to change the design are explicit.
- Important scope exclusions, constraints, and assumptions are recorded.
- Decision-relevant capacity estimates are complete—or deliberately deferred to a named deep dive.

## Stage 2: Core entities

Identify the durable business concepts exchanged through interfaces and persisted by the system.

Start with actors and nouns from the functional requirements. Give each concept one clear name and distinguish concepts that have different lifecycles or authority even if they look similar.

Capture only:

- Entity name and responsibility.
- Stable identifier and tenant/owner where relevant.
- Essential relationships.
- Lifecycle or important states.
- Which system is authoritative for it.

Do not design the complete schema yet. Fields and constraints should emerge while tracing interfaces and state changes. Avoid documenting obvious cosmetic fields unless they affect a requirement.

Useful questions:

- Who acts in the system, and can one person hold multiple roles?
- What resource is created, claimed, changed, queried, or deleted in each core journey?
- Which records need independent identity or lifecycle?
- Which aggregate or service owns each invariant?

### Core-entity completion gate

Proceed when every functional requirement can be described using the chosen vocabulary and no important resource is ambiguously owned.

## Stage 3: API or system interface

Define the contract between the system and each external actor before drawing internal components. Interfaces anchor the architecture to observable behavior.

For every functional requirement, define the command/query/event that satisfies it:

- Operation or endpoint name.
- Actor and authorization rule.
- Essential input and output shape.
- Important validation and error outcomes.
- Idempotency, pagination, or concurrency contract where relevant.
- Whether the response is synchronous, accepted for asynchronous work, or a stream/subscription.

### Protocol defaults

- **REST/HTTP:** Default for resource-oriented product APIs and ordinary client/server operations.
- **RPC/gRPC:** Internal, action-oriented service calls where typed contracts or transport efficiency provide a meaningful benefit.
- **GraphQL:** Diverse clients that need materially different projections and can justify the query-governance complexity.
- **WebSocket or SSE:** Add when a defined latency/direction requirement calls for server-driven updates; design the core command/query API first.
- **Events/queues:** Asynchronous integration, buffering, or decoupling; state the delivery and ordering contract.

Prefer resource-oriented names and keep versioning consistent with the project. Derive the current user/tenant from verified authentication context; do not trust identity supplied in a request body when the server already knows the caller.

### Interface completion gate

Proceed when each core functional requirement maps to an explicit interface and the contract is sufficient to trace the resulting reads, writes, events, and user-visible outcomes.

## Stage 4: Data flow — only when useful

Use a data-flow stage for pipelines, ingestion systems, workflows, search/indexing, media processing, event-driven systems, or other designs where the sequence is not obvious from individual APIs. Skip it for straightforward CRUD/request-response systems.

Describe the happy path as a short ordered sequence, for example:

```text
receive upload → store original → validate/scan → transform
               → publish metadata → notify client
```

For each transition, identify:

- Synchronous call, durable event, queue message, stream, or batch.
- State read or written and its authoritative owner.
- Ordering, idempotency, and retry expectations.
- Where the flow can pause, fail, or compensate.

Keep this at business-flow level. Component placement belongs in the high-level design.

### Data-flow completion gate

Proceed when the important transformations and state transitions can be followed end to end. If this adds no clarity, record that the stage was intentionally skipped.

## Stage 5: High-level design

Construct the smallest end-to-end architecture that satisfies the interfaces and functional requirements.

### Build one path at a time

For each core interface:

1. Start at the actor/client.
2. Trace the request, command, or event through the system.
3. Identify the component responsible for each business action.
4. Show the authoritative state read or changed.
5. Trace the response, status, or notification back to the actor.
6. Add only the fields, indexes, and constraints discovered by that path.

Use familiar components—client, API/service, database, object store, queue—until a requirement proves they are insufficient. Prefer the user’s established stack when it can express the design cleanly.

### Make arrows meaningful

For every important interaction, be able to state:

- What data crosses the boundary.
- Whether it is synchronous or asynchronous.
- What happens on timeout, retry, or duplicate delivery.
- Which component is authoritative and which copies are derived/cached.
- What state changes before success is reported.

### Keep the first design complete and simple

Do not pause the core design to fully solve every possible scale issue. Annotate a candidate hotspot or failure mode and revisit it during deep dives. A complete baseline that meets all functional requirements is more useful than a sophisticated fragment.

### High-level-design completion gate

Proceed when:

- Every core functional requirement has an end-to-end path.
- Component responsibilities and state ownership are clear.
- Important schemas/invariants have emerged near their owning stores.
- Success acknowledgment and basic failure behavior are defined.
- Known non-functional gaps are listed as deep-dive candidates.

## Stage 6: Deep dives

Use deep dives to make the baseline design satisfy its most important non-functional requirements and resolve its highest risks.

### Choose deep dives by evidence

Rank candidates by:

1. A quantified non-functional requirement the baseline misses.
2. A correctness or security invariant that can fail.
3. A bottleneck shown by workload estimates or access patterns.
4. A likely operational failure with significant impact.
5. A major unresolved decision that blocks implementation.

Prefer two or three consequential deep dives over shallow discussion of every component. Consult a pattern reference only when its triggering requirement or failure mode exists.

Typical mappings include:

- Server-driven low-latency changes → real-time updates.
- Multiple actors competing for the same state → contention.
- Cross-service steps and compensation → multi-step processes.
- Repeated/hot queries or global read latency → scaling reads.
- Sustained/bursty ingestion or hot partitions → scaling writes.
- Large binary transfer → large blobs.
- Work that outlives a request → long-running tasks.

### Deep-dive method

For each selected issue:

1. State the violated requirement, bottleneck, or failure mode.
2. Quantify the relevant load, latency, staleness, or risk if possible.
3. Present the simplest viable change.
4. Explain its data flow and state changes.
5. Cover correctness, degradation, recovery, security, and observability.
6. Name the tradeoff introduced.
7. Update the high-level design and decision record.

Do not list alternatives without a recommendation. Mention another option only when its tradeoff could reasonably change the user’s decision.

### Deep-dive completion gate

Proceed to sign-off when the design satisfies the agreed non-functional requirements, no unresolved issue materially changes implementation, and remaining risks have explicit mitigations or accepted tradeoffs.

## Stage 7: Design sign-off

Before implementation planning, walk the design against the requirements one final time.

### Functional trace

- Every core capability maps to an interface and an end-to-end path.
- Authorization, validation, state changes, and user-visible failures are covered.
- Asynchronous work exposes status, completion, and retry semantics.

### Non-functional trace

- Each prioritized non-functional requirement maps to a concrete design choice or an explicitly accepted limitation.
- Scale decisions are supported by estimates or measurable thresholds.
- Failure, recovery, consistency, security, and operability are coherent across components.

### Artifact update

Update `docs/system-design.md` with:

- Scope, actors, functional and non-functional requirements.
- Assumptions, constraints, and decision-relevant estimates.
- Core entities and ownership.
- Interfaces and optional data flows.
- High-level architecture and state transitions.
- Selected deep dives, failure handling, security, and observability.
- Known limitations and deferred work.

Update `docs/decisions.md` for each significant choice:

```text
Decision:
Context / requirement:
Chosen approach:
Why it fits:
Alternatives considered:
Tradeoffs and consequences:
Status: proposed | accepted | superseded
```

Only decisions agreed with the user should be marked accepted. Keep unresolved choices visible rather than burying assumptions in the architecture.

## Common delivery failures

- Starting with technology or diagrams before requirements are prioritized.
- Treating generic qualities such as “scalable” as usable non-functional requirements.
- Estimating numbers that do not change any decision.
- Designing a complete schema before the access patterns are known.
- Choosing an interface protocol because it is fashionable rather than necessary.
- Adding distributed components before completing the simplest functional path.
- Drawing arrows without explaining data, state, acknowledgment, and failure semantics.
- Deep-diving every possible issue instead of the few that dominate risk.
- Talking through alternatives without recommending one.
- Proceeding to implementation while a decision still materially changes schemas, boundaries, or tests.

## Final design-readiness checklist

- [ ] Scope and the few core user capabilities are explicit.
- [ ] Non-functional requirements are prioritized, contextual, and quantified where useful.
- [ ] Capacity estimates exist only where they inform decisions.
- [ ] Core entities, lifecycles, invariants, and authoritative owners are clear.
- [ ] Every functional requirement maps to an interface and end-to-end flow.
- [ ] The baseline architecture is complete before optimization machinery is added.
- [ ] Deep dives address the most consequential measured requirements and risks.
- [ ] Consistency, retries, idempotency, failure recovery, security, and observability are covered to the level the system requires.
- [ ] Significant choices and tradeoffs are recorded in `docs/decisions.md`.
- [ ] `docs/system-design.md` reflects the agreed design and no unresolved issue blocks an implementation plan.
