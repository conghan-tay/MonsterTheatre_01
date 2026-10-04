# Monster Theatre — Decisions

Only explicit product-brief decisions or later user agreements are accepted. Proposed choices remain visible until resolved.

## D001 — First-milestone product scope

**Decision:** A limited-access theatre with one active performance, ten audience sessions, two characters, and eight alternating voted turns.

**Context / requirement:** Deliver a small live demo with predictable ordering and manageable operational scope.

**Chosen approach:** Keep the three core journeys and deferred features defined in [the product brief](monster-theatre-product-brief.md). Use static uploaded portraits and fixed voices; introduce A first and let B deliver the closing EXPLAIN + PUNCHLINE.

**Why it fits:** Demonstrates the complete collaborative performance without adding animation, libraries, or variable schedules.

**Alternatives considered:** Variable turn counts, voice selection, animation, and stronger audience identity are explicitly deferred.

**Tradeoffs and consequences:** Limited customization; anonymous sessions do not strongly prevent multiple identities.

**Status:** accepted — source: agreed product brief.

## D002 — Flink computes results; the application commits them

**Decision:** Flink performs deduplication, aggregation, and final tallies; the application commits one immutable selection.

**Context / requirement:** Demonstrate meaningful stateful stream processing while keeping all acknowledged eligible votes and deterministic progression across retries/recovery.

**Chosen approach:** Preserve durable replayable event input, include every accepted eligible vote before commitment, persist majority/tie/no-vote selection and reason, and never reopen a committed round for speech retries. Input transport and completeness proof remain unresolved.

**Why it fits:** Meets the learning goal and separates computed voting data from committed performance state.

**Alternatives considered:** Application-only aggregation could handle the demo workload but would omit the explicit Flink learning goal.

**Tradeoffs and consequences:** Additional processing/checkpoint operation; pause result progression if Flink cannot finalize. Exactly-once speech or browser playback is not promised by checkpoints.

**Status:** accepted — responsibility split from product brief; mechanism pending later design.

## D003 — Exact recorded premiere and separate S3 media/lake content

**Decision:** Reuse completed turn recordings for the premiere; keep media and queryable structured data separate in private S3 storage.

**Context / requirement:** Replay what the audience actually heard and demonstrate historical queries without an in-app library.

**Chosen approach:** Save portraits, turn/full audio, script/decisions, and a linking manifest. Write schema-versioned events and derived metrics to finalized Parquet lake files, initially partitioned by date. Seven-day retention has a manual preservation exception.

**Why it fits:** Retains exact performance evidence and future animation inputs, and makes structured inspection independent of media transfer.

**Alternatives considered:** Regenerating full speech would change delivery and is excluded. An in-app file browser and analytics UI are deferred.

**Tradeoffs and consequences:** Assembly and storage must succeed before countdown; lake visibility lags live state; retention/preservation must cover referenced assets and records.

**Status:** accepted — source: product brief; assembly, query, and retention mechanisms remain open.

## D004 — Separate room, performance, round, and synthesis-attempt lifecycles

**Decision:** Give these concepts separate durable identities and authorities.

**Context / requirement:** A shareable room has immutable setup; Start creates its run; voting commits once; failed speech can retry without changing dialogue.

**Chosen approach:** Use the proposed core-entity model in [system design](system-design.md). Room owns scene and character components, fixed through create-and-publish (D013/D014). Treat choices and committed selection as round-owned data, metrics as derived data, and browser connection state as separate from audience identity.

**Why it fits:** Expresses the product's retry, reconnect, snapshot, and replay rules without conflating their lifecycles.

**Alternatives considered:** One mutable room record for all phases would obscure frozen setup and historical attempt/result identity.

**Tradeoffs and consequences:** More identifiers and lifecycle transitions; exact schemas/storage remain deferred until interfaces and access patterns are traced.

**Status:** proposed.

## D005 — One performance per room

**Decision:** Require a new room for every performance.

**Context / requirement:** The brief separates room creation from performance creation but does not specify repeat-run behavior.

**Chosen approach:** A room has zero or one performance. A successful Start permanently consumes its one run; failure or completion never restores Start eligibility. A subsequent run needs a new room and audience link. Retried Start requests can return the original performance without creating another.

**Why it fits:** Matches the user's explicit preference and keeps room links, setup, sessions, and artifacts scoped to one run.

**Alternatives considered:** Reuse a published room/link for repeated runs; the user declined this recommendation.

**Tradeoffs and consequences:** Repeat demos require another setup/publication and audience join. No between-run editing, membership reset, or cross-run portrait sharing is needed within a room.

**Status:** accepted — explicit user answer on 2026-10-04.

## D006 — Immediate Stop playback semantics

**Status:** superseded before acceptance — the user removed Stop controls; see D007. No Stop behavior or endpoint is part of the current design.

## D007 — Automatic performance after the initial Start

**Decision:** Remove Stop and manual retry controls; run the entire performance flow automatically after Start.

**Context / requirement:** The user explicitly requested no Stop controls and an automatic flow on 2026-10-04.

**Chosen approach:** Retain the initial director Start from the product journey. Automatically generate choices, open/close voting, commit results, enhance tags, synthesize/stream/record, advance turns, prepare the premiere, count down, and complete playback. Replace manual recovery actions with bounded automatic retries and visible recovery status.

**Why it fits:** Requires audience participation in voting without requiring director intervention throughout the performance.

**Alternatives considered:** Stop interruption and director-operated retry controls from the initial brief; removed by the user's correction.

**Tradeoffs and consequences:** The director cannot interrupt a launched run through product controls. Proposed failure policy: essential stages that exhaust recovery become terminally failed with retained partial artifacts; another run needs another room. Exact retry budgets and recovery deadlines remain unresolved.

**Status:** accepted — explicit automatic-flow/no-Stop requirement. Retaining Start follows the existing journey; detailed recovery policy remains proposed pending technical design.

## D008 — AWS-hosted first milestone

**Decision:** Deliver the first working milestone as an AWS-hosted demo for invited users.

**Context / requirement:** Invited audience members need to follow the shared room link remotely.

**Chosen approach:** Host the application on AWS and use real speech/S3 services; keep a local development/testing environment as supporting tooling.

**Why it fits:** Exercises the intended audience journey in the first milestone.

**Alternatives considered:** Local-only first milestone with hosting deferred.

**Tradeoffs and consequences:** Requires HTTPS hosting, director authentication, secret management, AWS networking, and a recurring-cost estimate. Specific resources/region remain proposed.

**Status:** accepted — explicit user answer on 2026-10-04.

## D009 — Temporal Cloud with Go workflows

**Decision:** Use Temporal Cloud to orchestrate the automatic performance, with Go workers on AWS.

**Context / requirement:** Eight repeated generation/vote/speech stages, external result waits, playback timers, retries, and restart recovery must proceed without director intervention.

**Chosen approach:** One stable workflow ID per performance, separate from the room setup workflow added in D014. Deterministic Go workflow code sequences activities and durable timers. Activities use PostgreSQL-backed idempotency and conditional commits around provider/storage work. Pass media references through workflow history, not bytes.

**Why it fits:** Matches the user's preferred durable-workflow stack without operating a Temporal cluster.

**Alternatives considered:** AWS Step Functions for an AWS-only workflow control plane.

**Tradeoffs and consequences:** Adds a managed-service account and connectivity/credential requirements; workers still need AWS hosting. Activity retries do not imply exactly-once speech generation or playback. See [Temporal Cloud guide](https://docs.temporal.io/cloud) and [Go timers](https://docs.temporal.io/develop/go/workflows/timers).

**Status:** accepted — explicit user answer on 2026-10-04; region/cost and detailed policies remain open.

## D010 — PostgreSQL outbox, Kinesis, and managed Flink baseline

**Decision:** Propose PostgreSQL as business authority with durable event publication to Kinesis and a managed Flink job.

**Context / requirement:** Acknowledged votes survive restart, Flink input is replayable, and committed winners cannot change after recovery.

**Chosen approach:** Atomically persist state/events/publication intent. Replay stable IDs into Kinesis; Flink deduplicates and checks a round closure's accepted-ID set before emitting a final derived JDBC result and checkpointed Parquet. Go commits the winner in PostgreSQL. Keep one application runtime and avoid extra result brokers.

**Why it fits:** Uses familiar state storage and AWS-managed streaming services; explicit completeness does not depend on batch arrival order.

**Alternatives considered:** Kafka/MSK is another replayable stream but introduces a separate broker platform. Database-only aggregation omits the Flink objective. Waiting for Parquet/checkpoints to select winners would conflict with live latency targets.

**Tradeoffs and consequences:** Managed Flink and Kinesis have a baseline operating cost even at demo volume. JDBC/Kinesis/runtime compatibility, receipt/closure ordering, retention, and latency need validation. [Kinesis ordering](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_PutRecords.html), [Flink JDBC](https://nightlies.apache.org/flink/flink-docs-stable/docs/connectors/table/jdbc/).

**Status:** proposed.

## D011 — Java for the Flink job

**Decision:** Propose Java DataStream for Flink, keeping Go for APIs/workflows and TypeScript for Next.js.

**Context / requirement:** Keyed state, completeness gating, connectors, low latency, and checkpointed Parquet need a supported Flink job API.

**Chosen approach:** A small Java job with pinned compatible connector/runtime versions, to be selected after compatibility verification.

**Why it fits:** Direct Flink APIs; avoids introducing a custom Go-to-Flink bridge. AWS notes Python connector limitations and recommends Java/Scala for low latency. [AWS Flink APIs](https://docs.aws.amazon.com/managed-flink/latest/java/what-is.html).

**Alternatives considered:** PyFlink fits the user's Python familiarity but requires API/connector validation for this job. Go is not a native Flink job language.

**Tradeoffs and consequences:** Introduces a Java toolchain; exact versions cannot be assumed from the newest upstream stable release because managed-runtime and connector support differ.

**Status:** proposed.

## D012 — PCM streaming and recorded WAV assembly

**Decision:** Propose PCM for live audio and turn recordings, with a WAV premiere assembled from completed elected attempts.

**Context / requirement:** Stream arriving chunks, identify exact turns, and replay the actual recordings without regenerating speech.

**Chosen approach:** Validate `eleven_v4`, two fixed voices, and 24 kHz mono 16-bit PCM account support; fan out chunks through an authorized application WebSocket; preserve ordered PCM and write valid WAV artifacts. On interrupted synthesis, propose automatically restarting the selected line in a fresh attempt.

**Why it fits:** Explicit sample boundaries and deterministic concatenation; simple capacity at eleven listeners. [ElevenLabs dialogue guide](https://elevenlabs.io/docs/eleven-api/guides/how-to/websockets/realtime-tdd), [Dialogue API](https://elevenlabs.io/docs/api-reference/text-to-dialogue/ttd-websocket).

**Alternatives considered:** Encoded streaming reduces bandwidth but complicates turn boundary buffering and exact assembly. Regenerating premiere speech is excluded by the brief.

**Tradeoffs and consequences:** Higher bandwidth, a custom browser PCM player, and possible repetition of partial lines after recovery. Real-provider/browser tests must confirm model/output access and the first-audio target.

**Status:** proposed.

## D013 — Room owns immutable scene and character context

**Decision:** Characters A/B and scene context are components of Room, not independent character resources.

**Context / requirement:** Every room has exactly two characters; the director supplies all context once, and dialogue generation needs to retrieve it together.

**Chosen approach:** Room owns scene topic/description/genre and character slots A/B with names, briefs, fixed voices, and S3 portrait references. No global character IDs, separate lifecycles, or character CRUD APIs. GetRoom returns all static setup; generation adds selected performance dialogue, current speaker/beat, and remaining turns. Context is immutable when creation/publication succeeds; Start uses the already fixed setup.

**Why it fits:** Ownership and retrieval match the product's single-room setup. Portrait bytes remain media artifacts without creating independent character management.

**Alternatives considered:** Independently managed characters or setup editing until Start; neither is needed for this milestone.

**Tradeoffs and consequences:** Any context/portrait change requires a new room. Database embedding versus room-owned child tables does not change the resource boundary. Saved performance artifacts include the room context for standalone inspection.

**Status:** accepted — user discussion and explicit request to update the docs on 2026-10-04.

## D014 — Single create-and-publish request with Temporal setup compensation

**Decision:** One complete request creates and publishes immutable Room setup and returns its shareable link; the director then has Start.

**Context / requirement:** The director wants a single simple setup submission containing scene context, both characters, and both portraits, with no revision/edit stage. PostgreSQL and S3 writes can fail independently.

**Chosen approach:** Accept one authenticated multipart POST with JSON context, portraits A/B, and an idempotency key. Validate complete input and stage portrait bytes privately in S3. Durably hand off a stable creation identity and object references to a separate Temporal setup workflow. Its activities create an internal provisioning Room, copy portraits to deterministic room-owned keys, then atomically commit ready/published state and one audience-link token. Return the ready room ID and link from the creation request. Clean staging afterward. Remove separate room edit, portrait upload/replacement, and publication APIs.

**Why it fits:** Gives the director one form submission and a link/Start result while preserving recoverable cross-store provisioning. Temporal inputs contain references rather than raw image payloads.

**Alternatives considered:** Editable drafts plus separate upload/publication calls, or synchronous setup with best-effort cleanup; the agreed flow uses durable setup recovery and compensating actions.

**Tradeoffs and consequences:** Staging/copy adds S3 operations and internal provisioning state, not a user-editable draft. Reuse creation/workflow/object identities across retries; different input with the same key conflicts. Before readiness, definitive setup failure marks provisioning failed and compensates owned objects, with cleanup retries/reconciliation. Reconcile ambiguous publication before deletion. After readiness, lost HTTP responses, staging-cleanup failures, and later performance failures must not roll back the Room. A retried submission returns the original link; setup changes require a new room. Exact wait/retry/expiry budgets remain open design parameters.

**Status:** accepted — user approved the discussed setup flow and requested the documentation update on 2026-10-04.
