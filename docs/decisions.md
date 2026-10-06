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

## D002 — Flink emits counts; the application selects and commits

**Status:** superseded by [D018](#d018--workflow-owned-voting-by-temporal-signal) on 2026-10-05 — Flink was removed from the design entirely. The parts of this decision that survive are the counts-versus-selection split and the single-point randomness rule, both of which D018 carries forward with the workflow in Flink's place. The original text is retained below for the record.

**Decision:** Flink performs deduplication and aggregation and emits **vote counts only**. The application performs any random tie-break or no-vote selection and commits one immutable winner.

**Context / requirement:** Demonstrate meaningful stateful stream processing while keeping all acknowledged eligible votes and deterministic progression across retries/recovery.

**Chosen approach:** Preserve durable replayable event input and include every accepted eligible vote before commitment.

Flink's output is the per-choice counts for a round, keyed by round and closure identity. It never selects a winner. The application applies the most-votes rule and, where the counts are tied or empty, performs the random selection — once, inside the winner-commit transaction, as a write that succeeds only if no result exists yet for that performance and turn. Re-delivered or replayed counts therefore return the already-persisted selection instead of producing a second roll. Committed rounds are never reopened for speech retries.

**Round closure is a single transaction.** The application derives the accepted-vote ID set from the ledger inside the same transaction that flips the round's state to closed, and emits the closure event in that transaction. Vote admission reads the round state in its own transaction and rejects anything that observes a closed round. Every accepted vote therefore either commits before closure reads the ledger and is in the set, or is rejected — there is no window in which a vote is acknowledged but omitted. The closure payload's accepted-ID set is complete by construction, and Flink's gate reduces to waiting until it has seen that set.

**Why it fits:** Meets the learning goal and separates computed voting data from committed performance state. Keeping randomness in the single commit transaction makes stream replay and activity retries safe without a distributed agreement protocol, and makes the completeness contract provable by construction rather than by argument.

**Alternatives considered:** Flink selecting the winner, which re-rolls the random choice on checkpoint replay and would let an idempotent upsert overwrite a committed result. Seeding the random choice deterministically from the closure boundary ID was a workable alternative; keeping selection in the application was preferred as simpler. Application-only aggregation could handle the demo workload but would omit the explicit Flink learning goal.

**Tradeoffs and consequences:** Additional processing/checkpoint operation; pause result progression if Flink cannot finalize. The number of hops between closure and commitment makes a sub-second commit unrealistic, so the latency target was relaxed to roughly two to three seconds with "Counting votes…" as the normal-path UI (see the product brief's non-functional requirements). Exactly-once speech or browser playback is not promised by checkpoints.

**Status (original):** accepted — responsibility split from product brief; counts-only contract, application-side selection, and single-transaction closure accepted on 2026-10-04. Input transport and replay retention remain open.

## D003 — Exact recorded premiere and separate S3 media/lake content

**Decision:** Reuse completed turn recordings for the premiere; keep media and queryable structured data separate in private S3 storage.

**Context / requirement:** Replay what the audience actually heard and demonstrate historical queries without an in-app library.

**Chosen approach:** Save portraits, turn/full audio, script/decisions, and a linking manifest. Write schema-versioned events to Parquet lake files, initially partitioned by date. Seven-day retention has a manual preservation exception.

**Why it fits:** Retains exact performance evidence and future animation inputs, and makes structured inspection independent of media transfer.

**Alternatives considered:** Regenerating full speech would change delivery and is excluded. An in-app file browser and analytics UI are deferred.

**Tradeoffs and consequences:** Assembly and storage must succeed before countdown; lake visibility lags live state; retention/preservation must cover referenced assets and records.

**Status:** accepted — source: product brief; assembly and query mechanisms remain open. The lake write mechanism and the retention/preservation procedure are settled by [D019](#d019--terminal-state-parquet-export-replaces-stream-processing); the derived-metrics layer named in the original approach was dropped there.

## D004 — Separate room, performance, round, and synthesis-attempt lifecycles

**Status:** superseded before acceptance — Room owns immutable setup (D013/D014); Performance owns nested turn, voting, and attempt components with hybrid state (D015/D016). Their distinct lifecycle states and correlation identifiers remain useful without independent CRUD resources.

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

**Chosen approach:** One stable workflow ID per performance, separate from the room setup workflow added in D014. Deterministic Go workflow code sequences activities and durable timers, carrying room context and dialogue history. Under D016, activities persist published milestone snapshots and accepted-vote/side-effect facts rather than mirroring every internal step. Use idempotency and conditional commits around provider/storage work. Pass media references through workflow history, not bytes.

**Why it fits:** Matches the user's preferred durable-workflow stack without operating a Temporal cluster.

**Alternatives considered:** AWS Step Functions for an AWS-only workflow control plane.

**Tradeoffs and consequences:** Adds a managed-service account and connectivity/credential requirements; workers still need AWS hosting. Activity retries do not imply exactly-once speech generation or playback. See [Temporal Cloud guide](https://docs.temporal.io/cloud) and [Go timers](https://docs.temporal.io/develop/go/workflows/timers).

**Status:** accepted — explicit user answer on 2026-10-04; region/cost and detailed policies remain open.

## D010 — PostgreSQL outbox, Kinesis, and managed Flink baseline

**Status:** superseded before acceptance, on 2026-10-05 — Flink and Kinesis were removed from the design (see [D018](#d018--workflow-owned-voting-by-temporal-signal) and [D019](#d019--terminal-state-parquet-export-replaces-stream-processing)). With no external stream consumer, the outbox and its relay were removed too: durable events are rows in PostgreSQL, exported to the lake in a batch at terminal state. PostgreSQL remains the business-state authority for everything other than in-round voting. The original text is retained below for the record.

**Decision:** Propose PostgreSQL as business authority with durable event publication to Kinesis and a managed Flink job.

**Context / requirement:** Acknowledged votes survive restart, Flink input is replayable, and committed winners cannot change after recovery.

**Chosen approach:** Atomically persist state/events/publication intent. Replay stable IDs into Kinesis; Flink deduplicates and checks a round closure's accepted-ID set before emitting a final derived JDBC result and checkpointed Parquet. Go commits the winner in PostgreSQL. Keep one application runtime and avoid extra result brokers.

**Why it fits:** Uses familiar state storage and AWS-managed streaming services; explicit completeness does not depend on batch arrival order.

**Alternatives considered:** Kafka/MSK is another replayable stream but introduces a separate broker platform. Database-only aggregation omits the Flink objective. Waiting for Parquet/checkpoints to select winners would conflict with live latency targets.

**Tradeoffs and consequences:** Managed Flink and Kinesis have a baseline operating cost even at demo volume. JDBC/Kinesis/runtime compatibility, receipt/closure ordering, retention, and latency need validation. [Kinesis ordering](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_PutRecords.html), [Flink JDBC](https://nightlies.apache.org/flink/flink-docs-stable/docs/connectors/table/jdbc/).

**Status:** proposed.

## D011 — Java for the Flink job

**Status:** superseded before acceptance, on 2026-10-05 — there is no Flink job, so there is no third language. The project is Go and TypeScript only, and the Java toolchain, connector-compatibility testing, and the Java-versus-PyFlink question are all moot. The original text is retained below for the record.

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

## D015 — One Performance resource with nested components

**Decision:** Performance is the root resource for turns, their voting state, and synthesis-attempt summaries; accepted votes are durable records owned by the performance.

**Context / requirement:** The first milestone has eight fixed turns, exactly one voting round per turn, and no independent turn/round/attempt editing. Reduce resource/API complexity without weakening vote and playback invariants.

**Chosen approach:** Return an authorized nested view through GetPerformance. Identify turns within a performance by numbers 1–8, with voting fields and attempt summaries beneath each turn. Scope the voting command to performance/turn. Keep stable correlation and attempt identifiers in events/audio metadata. No separate Turn, VotingRound, AcceptedVote, or SynthesisAttempt CRUD/read APIs are needed. Keep accepted votes in a separate ledger rather than inside the snapshot. Store summaries/committed tallies in the shared Performance snapshot and enrich its authorized response with the caller's own receipt from the ledger; do not persist viewer-specific receipts or all voter records into that snapshot.

**Amended 2026-10-05 ([D018](#d018--workflow-owned-voting-by-temporal-signal)):** the ledger is no longer the authority that admits votes or enforces their uniqueness — the workflow is, and the ledger is the durable projection it writes at round close. The resource boundary described here is unchanged: accepted votes are still owned by the Performance, still stored outside its snapshot, and still surfaced only as the caller's own receipt.

**Why it fits:** These concepts share ownership and are consumed as one performance. The ledger preserves durable per-vote records and idempotent submissions without rewriting a shared snapshot per vote.

**Alternatives considered:** Independent resources/tables for every concept, or one mutable JSON document containing every vote. Resource ownership does not dictate one table for all underlying facts.

**Tradeoffs and consequences:** Nested snapshots need conditional milestone updates and bounded summaries. Actor-specific views must protect audience identities and provider details. Provider-attempt events and S3 artifacts remain independently inspectable even without standalone attempt APIs.

**Status:** accepted — user approved the proposed consolidation and requested the documentation update on 2026-10-04.

## D016 — Temporal working state with PostgreSQL milestone snapshots

**Decision:** Use the hybrid model: Temporal owns internal execution/context; PostgreSQL stores published performance milestones, accepted votes, and immutable commitments.

**Context / requirement:** Reduce repeated context reads and per-step database writes while preserving current published state during workflow-worker recovery, durable voting, and historical inspection.

**Chosen approach:** Load immutable Room context once at startup and carry the schedule, selected dialogue history, generation/tagging results, timers, waits, retries, and audio references in recoverable workflow state. Persist milestone snapshots at Start, round opening/closure, winner commitment, playback anchors/turn completion, premiere readiness/timeline, and terminal outcome. Combine each applicable snapshot change and its durable events in one transaction. Read published state and own receipts through PostgreSQL rather than querying Temporal for every client request.

**Amended 2026-10-05 ([D018](#d018--workflow-owned-voting-by-temporal-signal)):** two points in this approach changed. Votes are no longer validated through PostgreSQL and no longer get a transaction each — they signal the workflow, and one transaction per round close persists that round's accepted votes and rejections together. Everything else stands: Room context is still loaded once, milestones are still the persistence boundary, and published-state reads still go to PostgreSQL without a workflow query. The availability property this decision valued — that reads survive worker unavailability — still holds for reads, but no longer for voting.

**Why it fits:** The user explicitly selected hybrid snapshots. Clients can read committed public state while workers recover; workflow orchestration need not re-read Room/history or persist every local assignment. [Temporal event history](https://docs.temporal.io/workflow-execution/event).

**Alternatives considered:** Workflow-first live reads with fewer SQL snapshots, which depend on available workflow workers for fresh queries; mirroring every internal state in PostgreSQL adds unnecessary application writes.

**Tradeoffs and consequences:** Published state can reflect the last durable product milestone while internal execution progresses or recovers. SQL commitment is authoritative; Temporal carries the committed result and cannot reroll it. Explicit provider-attempt/usage/latency/error events still need durable inspection storage. Audio chunks remain in bounded delivery buffers and S3, not SQL/history. Fewer application SQL calls do not eliminate Temporal history or replay-log persistence. Detailed budgets, schema encoding, and projection/event flushing remain open implementation choices.

**Status:** accepted — user selected hybrid snapshots and approved the documentation update on 2026-10-04.

## D017 — Configurable dialogue-generation provider behind an adapter

**Decision:** Treat the model used for choice generation and audio-tag enhancement as deployment configuration behind a narrow application-side adapter, not as a fixed architectural dependency.

**Context / requirement:** Every turn makes two generation calls — three dialogue choices, then audio-tag enhancement of the winning line. These were named throughout the flows but had no component, no diagram presence, and no stated provider. The user wants to swap models without a code change.

**Chosen approach:** Define one internal interface covering both call shapes. Select provider endpoint and model identifier from configuration/secret storage at runtime, so a model can be substituted per environment or per demo without redeploying application logic. Keep both prompts and their bounded retry/fallback behaviour inside the application. Record the provider and model identifier actually used on each attempt in the durable generation and synthesis events, so saved performances remain interpretable after a model is swapped.

**Why it fits:** The calls are ordinary request/response text generation with no provider-specific feature dependency, so an adapter costs little and preserves the ability to compare models during tuning. Recording the model per attempt keeps the data lake honest across swaps.

**Alternatives considered:** Hardcoding one provider/model, which makes comparison awkward and buries a swap in application code. An agent framework or separate Python service, already rejected in the system design as unjustified for two stateless calls.

**Tradeoffs and consequences:** A configurable model means dialogue quality, latency, and token cost vary by configuration and are not fixed properties of the design; the bounded retry and valid-fallback rules must hold for any configured model. Prompt portability across providers is not guaranteed and is a tuning concern, not a design one. No specific latency target is set for either call at this stage.

**Status:** accepted — explicit user answer on 2026-10-04 that the model should be config-selectable; provider shortlist and prompts remain open.

## D018 — Workflow-owned voting by Temporal signal

**Decision:** A vote is an HTTP request that signals the performance workflow. The workflow owns eligibility, choice validity, duplicate suppression, and the voting deadline, and counts the votes it received. The application still applies the most-votes rule and makes any tie-break or no-vote selection once, at commit. Apache Flink and Kinesis are removed from the design.

**Context / requirement:** The user removed Flink as a learning goal and asked whether it was still necessary. It was not. At eight rounds of at most ten votes, deduplication is a uniqueness check and a tally is a grouped count over ten rows. The closure-in-one-transaction rule accepted the previous day had already reduced Flink's live-path role to ceremony: the closing transaction computed the complete accepted-vote set, handed it to Flink, and Flink waited to see that same set arrive before counting it.

**Chosen approach:** The API authenticates the session credential and its room scope, then signals. It does not read round state from PostgreSQL first, and it does not query the workflow first.

The workflow holds the round's published choices, its eligible session set, and the set of sessions that have already voted, all in working state it already carries under D016. It records each signal as accepted, duplicate, invalid, or late. At the deadline its durable timer fires and one activity writes that round's accepted votes, its rejections, the counts, the selection and its reason, and the result event in a single transaction — one database round trip per round rather than one per vote.

Randomness stays where [D002](#d002--flink-emits-counts-the-application-selects-and-commits) put it: a tie or zero-vote round is resolved once, at commit, through a replay-safe side effect, and a recovered or repeated commit returns the persisted choice rather than selecting again.

**Why it fits:** Signals are persisted by the Temporal service before the call returns, so a vote is durable without a database write and survives worker restart — which is what the non-functional requirement actually asked for. Signals and the round timer are events in one totally ordered history, so "did this arrive before the deadline" is answered by that order instead of by comparing clocks across two transactions; the admission/closure race disappears rather than being mitigated. Duplicate suppression also becomes stronger, since the workflow processes signals serially and the voted-session set needs no concurrency control at all.

**Alternatives considered:** Keeping Flink for the learning objective, which the user removed. Reading the PostgreSQL round snapshot before signalling, which cannot be authoritative once the workflow owns round state and buys nothing once vote outcomes are asynchronous. Querying the workflow before signalling, which is strictly worse than not checking: query tasks are not persisted and are sync-matched only to a polling worker, failing with `FAILED_PRECONDITION` when none is available, so the check introduces a failure mode that a bare signal does not have — and it still cannot prevent a late vote, because the timer can fire between the query and the signal. Temporal Update instead of Signal, which would restore a synchronous validated receipt at the cost of a second round trip; the user judged the receipt unnecessary. [Sending messages to workflows](https://docs.temporal.io/sending-messages).

**Tradeoffs and consequences:** Vote acceptance now depends on the workflow service being reachable, which [D016](#d016--temporal-working-state-with-postgresql-milestone-snapshots) deliberately avoided. This is accepted: the workflow drives all progression, so when it is unreachable the performance is stopped regardless. Published-state reads are unaffected and still work while workers recover.

The vote endpoint returns `202 Accepted`. The only synchronous failures are authentication and signalling a run that has already finished; duplicate, late, invalid, and ineligible outcomes surface over the state channel instead. A client treats its own `202` as provisional and learns the authoritative outcome at round close. Malformed votes therefore reach workflow history, which is acceptable at an invite-only ten-person demo and produces the rejection records the demo questions need.

Removing Flink and Kinesis also removes the outbox and its relay, the Flink result table, the Java toolchain ([D011](#d011--java-for-the-flink-job)), managed-Flink VPC networking, and checkpoint tuning. The winner-commit latency target returns to under one second, since the workflow already holds the votes when the round closes.

**Status:** accepted — explicit user answers on 2026-10-05: remove Flink, vote by plain signal with no durable response, and elevate Temporal to the headline learning goal.

## D019 — Terminal-state Parquet export replaces stream processing

**Decision:** A performance's events are exported to the S3 lake by a workflow activity when the run reaches a terminal state, successful or failed, as one Parquet object per performance inside a date partition. There is no streaming sink and no derived-metrics layer.

**Context / requirement:** [D003](#d003--exact-recorded-premiere-and-separate-s3-medialake-content) requires queryable structured data in S3 and the brief lists four example demo questions. The streaming file sink that previously produced it was removed with Flink, so the lake needs another write mechanism.

**Chosen approach:** Events are rows in PostgreSQL, written in the same transaction as the milestone that produced them, so they are durable from the moment they occur. Export is a separate publication step: a single activity reads that performance's events and writes one Parquet object. It runs on the failure path as well as the success path, since failed runs retain their artifacts.

Athena and Glue remain the query surface, using partition projection rather than a crawler. The lake stores raw events; all four demo questions are answered by query, provided rejected submissions are recorded as events and the synthesis event carries first-audio timestamp, speaker, and story beat.

**Why it fits:** A single-object PUT is atomic, so "finalized" requires no checkpoint semantics and the sixty-second freshness target becomes trivially achievable and directly verifiable — the previous design could not promise it, because managed-runtime checkpoint intervals are not a freshness guarantee. One object per performance also makes seven-day expiry and manual preservation per-object operations, which resolves the mixed-retention problem that date-partitioned streaming output created.

**Alternatives considered:** Incremental export during the run, which adds coordination for data nobody queries mid-performance. Kinesis Firehose with Glue-based Parquet conversion, which reintroduces a streaming service and a buffering delay to replace a single PUT. Precomputed metric tables, unnecessary when every question is answerable from roughly two hundred event rows.

**Tradeoffs and consequences:** A run in progress is not queryable, which the brief already accepts. Export becomes a step that can fail and must be retried like any other activity; a run whose export never succeeds is complete but absent from the lake, so export failure needs to be visible to the operator. Schema versioning matters more without a schema registry in the pipeline, so each event carries its schema version as [D003](#d003--exact-recorded-premiere-and-separate-s3-medialake-content) requires.

**Status:** accepted — follows from the Flink removal agreed on 2026-10-05. Event schema, partition layout, and the preservation procedure remain open.
