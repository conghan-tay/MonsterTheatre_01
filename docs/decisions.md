# Monster Theatre — Decisions

Only explicit product-brief decisions or later user agreements are accepted. Proposed choices remain visible until resolved.

## Decisions in force

The log below is kept in full, including entries that were later replaced. These are the ones that describe the current design:

| Decision | In one line |
| --- | --- |
| D001 | Ten audience sessions, two characters, eight voted turns (amended) |
| D005 | One performance per room |
| D007 | Fully automatic after Start; no Stop or retry controls |
| D008 | First milestone is hosted on AWS |
| D009 | Temporal Cloud with Go workflows (amended) |
| D013 | Room owns immutable scene and character context |
| D015 | Performance is one resource with turns nested inside it (amended) |
| D017 | Language model is configurable behind an adapter |
| D018 | Votes are Temporal signals; the workflow owns duplicates and the deadline (amended) |
| D022 | Audio plays only at the premiere, as eight clips |
| D023 | No data lake or analytics |
| D024 | Redis Streams for live updates; workflow query on connect; no database snapshots |
| D025 | Room creation is one plain request |
| D026 | Server and worker are separate processes |
| D027 | No concurrency cap or staleness sweep |
| D028 | The director votes; audience slots are never released |
| D029 | Simple built-in director login |
| D030 | Session cookies for audience and director |
| D031 | WebSocket and Redis Streams kept as departures from the defaults |
| D032 | Aurora DSQL on AWS, plain PostgreSQL locally |
| D033 | Votes carry the server's receipt time; lateness is decided by that stamp |
| D034 | Share links are derived from a server secret, not stored |
| D035 | The WebSocket accepts only the web app's origin or the server's own address |
| D036 | Every update carries the whole state, not a change |
| D037 | The premiere's clip list carries each clip's URL |
| D038 | Choice generation has no stand-in lines: retry for a window, then fail the run |
| D039 | A finished room's snapshot comes from its row: the premiere as published, at revision 25, with its start time stored |
| D040 | Stored objects are read through a body that can seek, so the standard library answers range requests |
| D041 | A browser that cannot be given the state is sent a `recovering` snapshot and kept; it asks again with `resync` |
| D042 | A run found dead is detected by a refused query, marked failed, and announced to its whole room |
| D043 | A connecting browser never starts a workflow; only Start does |

Superseded or withdrawn: D002, D004, D006, D010, D011, D012, D014, D016, D019, D020, D021, and the data-lake half of D003.

## D001 — First-milestone product scope

**Decision:** A limited-access theatre with ten audience sessions per performance, two characters, and eight alternating voted turns.

**Amended 2026-10-06 ([D020](#d020--concurrent-performances-are-a-configurable-capacity-limit)):** this decision originally read "one active performance". That was scope framing — a statement that the demo need not handle many at once — but the system design hardened it into a deployment-wide mutex. Concurrency is now a configurable capacity limit rather than part of the product definition. The rest of this decision stands unchanged.

**Amended 2026-10-07:** the capacity limit was removed as well ([D027](#d027--no-concurrency-cap-or-staleness-sweep)), and turn audio is no longer streamed; the audience hears the performance only at the premiere ([D022](#d022--premiere-is-the-only-audio-playback)).

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

**Amended 2026-10-07:** the lake half of this decision is superseded by [D023](#d023--no-data-lake-or-analytics-in-the-first-milestone); there is no Parquet, Athena, or structured event data in this milestone. The media half stands in reduced form under [D022](#d022--premiere-is-the-only-audio-playback): the premiere reuses the saved turn recordings, played in order, with no assembled file or manifest. The original text is retained below for the record.

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

**Amended 2026-10-07:** still accepted. The references below to a room setup workflow and to milestone snapshots no longer apply ([D025](#d025--room-creation-is-one-plain-request), [D024](#d024--redis-streams-for-live-updates-and-workflow-query-on-connect)). There is one workflow, per performance, and the worker runs as its own process ([D026](#d026--server-and-worker-run-as-separate-processes)).

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

**Status:** superseded before acceptance, on 2026-10-07, by [D022](#d022--premiere-is-the-only-audio-playback). Nothing is streamed live and nothing is assembled, so the PCM format, the custom browser player, and WAV assembly are not needed. The original text is retained below for the record.

**Decision:** Propose PCM for live audio and turn recordings, with a WAV premiere assembled from completed elected attempts.

**Context / requirement:** Stream arriving chunks, identify exact turns, and replay the actual recordings without regenerating speech.

**Chosen approach:** Validate `eleven_v4`, two fixed voices, and 24 kHz mono 16-bit PCM account support; fan out chunks through an authorized application WebSocket; preserve ordered PCM and write valid WAV artifacts. On interrupted synthesis, propose automatically restarting the selected line in a fresh attempt.

**Why it fits:** Explicit sample boundaries and deterministic concatenation; simple capacity at eleven listeners. [ElevenLabs dialogue guide](https://elevenlabs.io/docs/eleven-api/guides/how-to/websockets/realtime-tdd), [Dialogue API](https://elevenlabs.io/docs/api-reference/text-to-dialogue/ttd-websocket).

**Alternatives considered:** Encoded streaming reduces bandwidth but complicates turn boundary buffering and exact assembly. Regenerating premiere speech is excluded by the brief.

**Tradeoffs and consequences:** Higher bandwidth, a custom browser PCM player, and possible repetition of partial lines after recovery. Real-provider/browser tests must confirm model/output access and the first-audio target.

**Status:** proposed.

## D013 — Room owns immutable scene and character context

**Amended 2026-10-07:** still accepted. There is no separate publication step any more; the room is immutable from the moment it is created ([D025](#d025--room-creation-is-one-plain-request)).

**Decision:** Characters A/B and scene context are components of Room, not independent character resources.

**Context / requirement:** Every room has exactly two characters; the director supplies all context once, and dialogue generation needs to retrieve it together.

**Chosen approach:** Room owns scene topic/description/genre and character slots A/B with names, briefs, fixed voices, and S3 portrait references. No global character IDs, separate lifecycles, or character CRUD APIs. GetRoom returns all static setup; generation adds selected performance dialogue, current speaker/beat, and remaining turns. Context is immutable when creation/publication succeeds; Start uses the already fixed setup.

**Why it fits:** Ownership and retrieval match the product's single-room setup. Portrait bytes remain media artifacts without creating independent character management.

**Alternatives considered:** Independently managed characters or setup editing until Start; neither is needed for this milestone.

**Tradeoffs and consequences:** Any context/portrait change requires a new room. Database embedding versus room-owned child tables does not change the resource boundary. Saved performance artifacts include the room context for standalone inspection.

**Status:** accepted — user discussion and explicit request to update the docs on 2026-10-04.

## D014 — Single create-and-publish request with Temporal setup compensation

**Status:** superseded on 2026-10-07 by [D025](#d025--room-creation-is-one-plain-request). The single-request experience for the director stands; the Temporal setup workflow, staging, and compensation were removed. The original text is retained below for the record.

**Decision:** One complete request creates and publishes immutable Room setup and returns its shareable link; the director then has Start.

**Context / requirement:** The director wants a single simple setup submission containing scene context, both characters, and both portraits, with no revision/edit stage. PostgreSQL and S3 writes can fail independently.

**Chosen approach:** Accept one authenticated multipart POST with JSON context, portraits A/B, and an idempotency key. Validate complete input and stage portrait bytes privately in S3. Durably hand off a stable creation identity and object references to a separate Temporal setup workflow. Its activities create an internal provisioning Room, copy portraits to deterministic room-owned keys, then atomically commit ready/published state and one audience-link token. Return the ready room ID and link from the creation request. Clean staging afterward. Remove separate room edit, portrait upload/replacement, and publication APIs.

**Why it fits:** Gives the director one form submission and a link/Start result while preserving recoverable cross-store provisioning. Temporal inputs contain references rather than raw image payloads.

**Alternatives considered:** Editable drafts plus separate upload/publication calls, or synchronous setup with best-effort cleanup; the agreed flow uses durable setup recovery and compensating actions.

**Tradeoffs and consequences:** Staging/copy adds S3 operations and internal provisioning state, not a user-editable draft. Reuse creation/workflow/object identities across retries; different input with the same key conflicts. Before readiness, definitive setup failure marks provisioning failed and compensates owned objects, with cleanup retries/reconciliation. Reconcile ambiguous publication before deletion. After readiness, lost HTTP responses, staging-cleanup failures, and later performance failures must not roll back the Room. A retried submission returns the original link; setup changes require a new room. Exact wait/retry/expiry budgets remain open design parameters.

**Status:** accepted — user approved the discussed setup flow and requested the documentation update on 2026-10-04.

## D015 — One Performance resource with nested components

**Amended 2026-10-07 ([D024](#d024--redis-streams-for-live-updates-and-workflow-query-on-connect)):** Performance is still the single resource for a run, with turns nested inside it. What changed is where it lives: in workflow state while running, and as one final script document on the performance row when finished. The PostgreSQL snapshot, the vote ledger, and synthesis-attempt summaries described below no longer exist.

**Decision:** Performance is the root resource for turns, their voting state, and synthesis-attempt summaries; accepted votes are durable records owned by the performance.

**Context / requirement:** The first milestone has eight fixed turns, exactly one voting round per turn, and no independent turn/round/attempt editing. Reduce resource/API complexity without weakening vote and playback invariants.

**Chosen approach:** Return an authorized nested view through GetPerformance. Identify turns within a performance by numbers 1–8, with voting fields and attempt summaries beneath each turn. Scope the voting command to performance/turn. Keep stable correlation and attempt identifiers in events/audio metadata. No separate Turn, VotingRound, AcceptedVote, or SynthesisAttempt CRUD/read APIs are needed. Keep accepted votes in a separate ledger rather than inside the snapshot. Store summaries/committed tallies in the shared Performance snapshot and enrich its authorized response with the caller's own receipt from the ledger; do not persist viewer-specific receipts or all voter records into that snapshot.

**Amended 2026-10-05 ([D018](#d018--workflow-owned-voting-by-temporal-signal)):** the ledger is no longer the authority that admits votes or enforces their uniqueness — the workflow is, and the ledger is the durable projection it writes at round close. The resource boundary described here is unchanged: accepted votes are still owned by the Performance, still stored outside its snapshot, and still surfaced only as the caller's own receipt.

**Why it fits:** These concepts share ownership and are consumed as one performance. The ledger preserves durable per-vote records and idempotent submissions without rewriting a shared snapshot per vote.

**Alternatives considered:** Independent resources/tables for every concept, or one mutable JSON document containing every vote. Resource ownership does not dictate one table for all underlying facts.

**Tradeoffs and consequences:** Nested snapshots need conditional milestone updates and bounded summaries. Actor-specific views must protect audience identities and provider details. Provider-attempt events and S3 artifacts remain independently inspectable even without standalone attempt APIs.

**Status:** accepted — user approved the proposed consolidation and requested the documentation update on 2026-10-04.

## D016 — Temporal working state with PostgreSQL milestone snapshots

**Status:** superseded on 2026-10-07 by [D024](#d024--redis-streams-for-live-updates-and-workflow-query-on-connect). The workflow is now the only holder of running state; PostgreSQL receives a row at Start and the final result at the end. The alternative this decision rejected, workflow-first live reads, is what was chosen, accepting that reads need an available worker. The original text is retained below for the record.

**Decision:** Use the hybrid model: Temporal owns internal execution/context; PostgreSQL stores published performance milestones, accepted votes, and immutable commitments.

**Context / requirement:** Reduce repeated context reads and per-step database writes while preserving current published state during workflow-worker recovery, durable voting, and historical inspection.

**Chosen approach:** Load immutable Room context once at startup and carry the schedule, selected dialogue history, generation/tagging results, timers, waits, retries, and audio references in recoverable workflow state. Persist milestone snapshots at Start, round opening/closure, winner commitment, playback anchors/turn completion, premiere readiness/timeline, and terminal outcome. Combine each applicable snapshot change and its durable events in one transaction. Read published state and own receipts through PostgreSQL rather than querying Temporal for every client request.

**Amended 2026-10-05 ([D018](#d018--workflow-owned-voting-by-temporal-signal)):** two points in this approach changed. Votes are no longer validated through PostgreSQL and no longer get a transaction each — they signal the workflow, and one transaction per round close persists that round's accepted votes and rejections together. Everything else stands: Room context is still loaded once, milestones are still the persistence boundary, and published-state reads still go to PostgreSQL without a workflow query. The availability property this decision valued — that reads survive worker unavailability — still holds for reads, but no longer for voting.

**Why it fits:** The user explicitly selected hybrid snapshots. Clients can read committed public state while workers recover; workflow orchestration need not re-read Room/history or persist every local assignment. [Temporal event history](https://docs.temporal.io/workflow-execution/event).

**Alternatives considered:** Workflow-first live reads with fewer SQL snapshots, which depend on available workflow workers for fresh queries; mirroring every internal state in PostgreSQL adds unnecessary application writes.

**Tradeoffs and consequences:** Published state can reflect the last durable product milestone while internal execution progresses or recovers. SQL commitment is authoritative; Temporal carries the committed result and cannot reroll it. Explicit provider-attempt/usage/latency/error events still need durable inspection storage. Audio chunks remain in bounded delivery buffers and S3, not SQL/history. Fewer application SQL calls do not eliminate Temporal history or replay-log persistence. Detailed budgets, schema encoding, and projection/event flushing remain open implementation choices.

**Status:** accepted — user selected hybrid snapshots and approved the documentation update on 2026-10-04.

## D017 — Configurable dialogue-generation provider behind an adapter

**Amended 2026-10-07:** still accepted. With no event log or lake ([D023](#d023--no-data-lake-or-analytics-in-the-first-milestone)), the model used is recorded per turn in the final script on the performance row.

**Decision:** Treat the model used for choice generation and audio-tag enhancement as deployment configuration behind a narrow application-side adapter, not as a fixed architectural dependency.

**Context / requirement:** Every turn makes two generation calls — three dialogue choices, then audio-tag enhancement of the winning line. These were named throughout the flows but had no component, no diagram presence, and no stated provider. The user wants to swap models without a code change.

**Chosen approach:** Define one internal interface covering both call shapes. Select provider endpoint and model identifier from configuration/secret storage at runtime, so a model can be substituted per environment or per demo without redeploying application logic. Keep both prompts and their bounded retry/fallback behaviour inside the application. Record the provider and model identifier actually used on each attempt in the durable generation and synthesis events, so saved performances remain interpretable after a model is swapped.

**Why it fits:** The calls are ordinary request/response text generation with no provider-specific feature dependency, so an adapter costs little and preserves the ability to compare models during tuning. Recording the model per attempt keeps the data lake honest across swaps.

**Alternatives considered:** Hardcoding one provider/model, which makes comparison awkward and buries a swap in application code. An agent framework or separate Python service, already rejected in the system design as unjustified for two stateless calls.

**Tradeoffs and consequences:** A configurable model means dialogue quality, latency, and token cost vary by configuration and are not fixed properties of the design; the bounded retry and valid-fallback rules must hold for any configured model. Prompt portability across providers is not guaranteed and is a tuning concern, not a design one. No specific latency target is set for either call at this stage.

**Amended 2026-10-10 ([D038](#d038--no-stand-in-lines-for-choice-generation)):** the "valid-fallback rule" mentioned below no longer exists for choice generation. What must hold for any configured model is the validation of its answers and the bounded retry. The first provider is Anthropic, with `claude-sonnet-5-5` as the default model (`LLM_MODEL`); both prompts are in `server/internal/providers/anthropic/prompts.go`. The model recorded per turn is the one that wrote that turn's choices.

**Status:** accepted — explicit user answer on 2026-10-04 that the model should be config-selectable; provider shortlist and prompts remain open.

## D018 — Workflow-owned voting by Temporal signal

**Amended 2026-10-07 ([D024](#d024--redis-streams-for-live-updates-and-workflow-query-on-connect)):** the core stands: a vote signals the workflow, and the workflow owns duplicates and the deadline. Four details changed. The vote travels over the browser's WebSocket instead of an HTTP `202` endpoint. The workflow picks the winner itself, using Temporal's replay-safe random source for ties, so there is no separate commit activity. Nothing is written to PostgreSQL per round, and rejected votes are ignored, not recorded. A browser reconnecting mid-round can now see its own vote, because the snapshot comes from a workflow query.

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

**Status:** superseded on 2026-10-07 by [D023](#d023--no-data-lake-or-analytics-in-the-first-milestone). There is no lake export. The original text is retained below for the record.

**Decision:** A performance's events are exported to the S3 lake by a workflow activity when the run reaches a terminal state, successful or failed, as one Parquet object per performance inside a date partition. There is no streaming sink and no derived-metrics layer.

**Context / requirement:** [D003](#d003--exact-recorded-premiere-and-separate-s3-medialake-content) requires queryable structured data in S3 and the brief lists four example demo questions. The streaming file sink that previously produced it was removed with Flink, so the lake needs another write mechanism.

**Chosen approach:** Events are rows in PostgreSQL, written in the same transaction as the milestone that produced them, so they are durable from the moment they occur. Export is a separate publication step: a single activity reads that performance's events and writes one Parquet object. It runs on the failure path as well as the success path, since failed runs retain their artifacts.

Athena and Glue remain the query surface, using partition projection rather than a crawler. The lake stores raw events; all four demo questions are answered by query, provided rejected submissions are recorded as events and the synthesis event carries first-audio timestamp, speaker, and story beat.

**Why it fits:** A single-object PUT is atomic, so "finalized" requires no checkpoint semantics and the sixty-second freshness target becomes trivially achievable and directly verifiable — the previous design could not promise it, because managed-runtime checkpoint intervals are not a freshness guarantee. One object per performance also makes seven-day expiry and manual preservation per-object operations, which resolves the mixed-retention problem that date-partitioned streaming output created.

**Alternatives considered:** Incremental export during the run, which adds coordination for data nobody queries mid-performance. Kinesis Firehose with Glue-based Parquet conversion, which reintroduces a streaming service and a buffering delay to replace a single PUT. Precomputed metric tables, unnecessary when every question is answerable from roughly two hundred event rows.

**Tradeoffs and consequences:** A run in progress is not queryable, which the brief already accepts. Export becomes a step that can fail and must be retried like any other activity; a run whose export never succeeds is complete but absent from the lake, so export failure needs to be visible to the operator. Schema versioning matters more without a schema registry in the pipeline, so each event carries its schema version as [D003](#d003--exact-recorded-premiere-and-separate-s3-medialake-content) requires.

**Status:** accepted — follows from the Flink removal agreed on 2026-10-05. Event schema, partition layout, and the preservation procedure remain open.

## D020 — Concurrent performances are a configurable capacity limit

**Status:** superseded on 2026-10-07 by [D027](#d027--no-concurrency-cap-or-staleness-sweep). Rooms remain independent, which was the point of this decision; the configurable cap and the staleness sweep were removed. The original text is retained below for the record.

**Decision:** Remove the deployment-wide single-active-performance constraint. One performance per room ([D005](#d005--one-performance-per-room)) remains the only invariant. The number of performances running at once is a configured maximum, evaluated by counting performances in non-terminal phases, with a default of three.

**Context / requirement:** [D001](#d001--first-milestone-product-scope) described "one active performance" as demo scope. The system design implemented it as an enforced global mutex: Start claimed a demo-wide slot held in a transactionally claimed guard row, released only when a run reached a terminal state. The user questioned whether one director's run should really block another's. It should not, and the deployment has several administrator-created director accounts.

**Chosen approach:** Start counts performances currently in non-terminal phases and compares that count with the configured maximum. There is no slot to claim and none to release.

Exceeding the limit returns `429`, matching the system design's existing definition of that code as bounded admission control, rather than `409`, which asserts that something about the requested resource conflicts. A Start rejected this way does **not** consume the room's one run: [D005](#d005--one-performance-per-room) spends a room on a *successful* Start only, and the room stays startable.

A staleness sweep closes the remaining liveness gap. A performance whose last durable milestone is older than a configured budget is automatically transitioned to failed through the existing terminal-failure path, retaining partial artifacts and exporting its events. This is an automatic sweep, not a control surface, so it does not reintroduce what [D007](#d007--automatic-performance-after-the-initial-start) removed.

**Why it fits:** Nothing about one room's run makes another room's run incorrect, so a global mutex had no business justification — it was protecting cost and audio fan-out capacity, which are quantities rather than the number one. Expressing that as a tunable limit keeps the protection and drops the false invariant.

Counting from state is also materially safer than the guard row it replaces. A lock must be released by whoever holds it; a count is derived fresh on every admission check, so there is nothing to leak. Under the previous design a run stuck in a non-terminal phase — a lost worker, a wedged workflow, a failure path that never fired — would have held the deployment-wide slot permanently, failing every subsequent Start in every room with no manual override available, since [D007](#d007--automatic-performance-after-the-initial-start) removed those. The staleness sweep handles the residual case where a stuck run still occupies capacity.

**Alternatives considered:** Keeping the global singleton and adding a reap rule, which fixes the liveness hole but leaves the unjustified cross-director coupling. A per-director limit of one, which matches intuition but adds a rule the workload does not need once a global cap exists. Removing all limits, which leaves concurrent provider spend and fan-out load unbounded.

**Tradeoffs and consequences:** Concurrency multiplies listener fan-out, concurrent speech connections, and generation spend, so the default is a capacity judgement rather than a product choice. The single application task described in the system design was sized for the eleven listeners of one run; three concurrent performances triples that, and the default should be confirmed against the audio deep dive's fan-out measurements rather than assumed. The limit is also deployment-wide rather than per-director, so a busy deployment can still return `429` to a director whose own rooms are idle; this is acceptable at the invited-account scale and is visible as a retryable condition.

**Status:** accepted — explicit user answers on 2026-10-06: replace the invariant with a configurable cap counted from state, no per-director cap, several invited director accounts. The default value remains open pending audio capacity work.

## D021 — Premiere plays over the room WebSocket on the server timeline

**Status:** superseded on 2026-10-07 by [D022](#d022--premiere-is-the-only-audio-playback). The premiere is now eight clips played by the browser's own audio element, fetched through the server. The rule that no browser receives a storage URL stands. The original text is retained below for the record.

**Decision:** The assembled premiere recording is delivered as ordered audio chunks over the same same-origin WebSocket that carries live turn audio, positioned by the server's room timeline. Audience sessions receive no object URL for playback.

**Context / requirement:** [D012](#d012--pcm-streaming-and-recorded-wav-assembly) settles what the premiere *is* — one recording assembled from the eight completed turn recordings, without regenerating speech. It did not settle how that recording reaches listeners. The system design said only "an authorized media-delivery mechanism" while also proposing short-lived per-object URLs for media, which implied a downloaded file played by a second, separate player.

**Chosen approach:** Reuse the existing connection, sequence identification, and snapshot-resync rules. The server drives position, so the five-second countdown leads into a premiere that every listener hears at the same moment. A client connecting or reconnecting during the premiere resumes at the timeline's current offset rather than restarting the performance.

Short-lived per-object authorized URLs remain, narrowed to what they were always for: portraits, and operator inspection or download of media, script, and manifest.

**Why it fits:** One player, one buffering model, and one notion of position in the browser, reusing everything already built for turn playback instead of adding a parallel path with its own failure modes. It also makes the countdown mean something: under the URL approach the countdown was synchronised but the thing it counted down to was not, since each browser would begin whenever its own buffer filled. Resume-at-offset is a direct consequence and is better than the alternative, where a late joiner would start a finished performance from the beginning.

A secondary benefit is that the security rule tightens rather than being qualified: no audience session receives an S3 URL at any point, so "a room link never grants bucket access" holds without exception.

**Alternatives considered:** A presigned URL to the assembled object played in a standard audio element. Simpler and it offloads bandwidth from the container, but it needs a second player, gives up synchronised start, restarts rather than resumes on reconnect, and hands audience sessions an object URL. The brief's tolerance for browsers not finishing in lockstep would have permitted it; synchronised delivery was preferred because the premiere is the moment the product is built around.

**Tradeoffs and consequences:** Each performance now sends its audio to every listener twice — once live, once at the premiere — so container egress roughly doubles and multiplies again by the concurrency maximum in [D020](#d020--concurrent-performances-are-a-configurable-capacity-limit). Server-driven delivery is also more exposed to application task replacement than a downloaded object would be: a task restart mid-premiere interrupts playback until clients reconnect and resume, where a browser playing a local file would have continued. The system design already accepts interrupted in-flight audio on the single-task deployment, so this is consistent rather than new, but it is a real cost of the choice.

**Status:** accepted — explicit user answer on 2026-10-06. Chunked delivery here assumes the PCM format in [D012](#d012--pcm-streaming-and-recorded-wav-assembly), which is still proposed. Buffering depth and the anchor behaviour for a resuming client remain part of the audio deep dive.

## D022 — Premiere is the only audio playback

**Decision:** No audio is played during the turns. Each winning line is turned into speech and saved to S3 as it is chosen, and the audience first hears the performance at the premiere, as the eight recordings played in order by the browser.

**Context / requirement:** The user found the design too complex to comprehend and prototype quickly, and removed the requirement to play each line after its vote. Live audio was the largest source of complexity: a realtime provider socket, PCM fan-out over WebSockets, a custom browser player, a room playback timeline, and interrupted-attempt handling.

**Chosen approach:** Text to speech is an ordinary request/response activity per turn that writes one audio file to S3. After turn eight the workflow publishes a start time five seconds ahead and the ordered clip list. Each browser counts down and plays the clips back to back with a standard audio element, fetching them through the server and highlighting the speaking portrait per clip. A browser arriving later plays from the beginning.

**Why it fits:** It removes every realtime audio concern while keeping the vote-then-hear payoff, and it makes the premiere a genuine reveal. It also dissolves the process-topology problem in the earlier design, because no audio has to travel from a worker to a browser.

**Alternatives considered:** Keeping live per-turn streaming, removed by the user. Assembling one recording on the server, which adds an activity and a format question for no benefit to a prototype; it can be added later.

**Tradeoffs and consequences:** The audience reads each winning line but does not hear it until the end. The ElevenLabs real-time streaming learning goal is dropped; audio tags and two voices remain. There is no single assembled recording in S3, and premiere playback is aligned only by a shared start time. Supersedes [D012](#d012--pcm-streaming-and-recorded-wav-assembly) and [D021](#d021--premiere-plays-over-the-room-websocket-on-the-server-timeline), and the media half of [D003](#d003--exact-recorded-premiere-and-separate-s3-medialake-content) in part.

**Status:** accepted — explicit user request and plan approval on 2026-10-07. Model confirmed by the user on 2026-10-08: the Text to Dialogue convert endpoint with `eleven_v4_turbo` supports audio tags with the chosen voices. MP3 clips played in sequence are accepted, including any short gaps between them.

## D023 — No data lake or analytics in the first milestone

**Decision:** Remove the S3 data lake, Parquet export, Athena, Glue, and the structured event log. S3 is used for object storage only.

**Context / requirement:** The user judged analytics not a priority for the prototype.

**Chosen approach:** Keep one final script document per performance on its PostgreSQL row, holding choices, vote counts, winners, how each was decided, and original and tagged text. Nothing else is recorded about a run.

**Why it fits:** The lake existed to answer four demo questions. Without that goal, the event schema, export step, retention design, and query tooling have no purpose.

**Alternatives considered:** Keeping an events table without the export, rejected as machinery with no consumer.

**Tradeoffs and consequences:** No per-vote history, rejection counts, or latency data after a run. The S3 data lake learning goal is dropped. Supersedes [D019](#d019--terminal-state-parquet-export-replaces-stream-processing) and the lake half of [D003](#d003--exact-recorded-premiere-and-separate-s3-medialake-content).

**Status:** accepted — explicit user request and plan approval on 2026-10-07.

## D024 — Redis Streams for live updates and workflow query on connect

**Decision:** The workflow publishes each state change to a Redis stream. The server reads the stream and forwards updates to every browser in the room over that browser's WebSocket. When a browser connects, the server gets the current state by querying the workflow. PostgreSQL holds no running state.

**Context / requirement:** The user's original sketch, restored: the workflow is the one place performance state lives, and the server is a forwarder.

**Chosen approach:** Publishing is a small activity, since workflow code cannot perform I/O. The workflow keeps a revision number, increased before every publish and included in both published updates and query results. On connect the server registers the connection, queries the workflow, sends that snapshot, then forwards only updates with a higher revision. Votes travel up the same WebSocket and the server signals the workflow. Finished performances are read from the performance row, because workflow history expires and a query needs a worker.

**Why it fits:** One authority for running state, no snapshot schema to keep in step with the workflow, and a connection model that is the same for first connect and reconnect.

**Alternatives considered:** PostgreSQL milestone snapshots ([D016](#d016--temporal-working-state-with-postgresql-milestone-snapshots)), which keep reads available while a worker is down at the cost of a second copy of state. PostgreSQL LISTEN/NOTIFY in place of Redis, which avoids one component; Redis Streams was the user's stated preference. Having the server poll the workflow, which needs no broker but adds delay and load.

**Tradeoffs and consequences:** A newly connecting browser cannot load state while no worker is available. An activity retry can publish an update twice; the revision rule makes that harmless. Redis is one more component to run. Because the workflow no longer needs a list of eligible sessions, anyone who joins while a round is open can vote in it, where the earlier design made them wait for the next round; the user confirmed this on 2026-10-07. Supersedes [D016](#d016--temporal-working-state-with-postgresql-milestone-snapshots); amends [D015](#d015--one-performance-resource-with-nested-components) and [D018](#d018--workflow-owned-voting-by-temporal-signal).

**Status:** accepted — explicit user request and plan approval on 2026-10-07, including the late-joiner voting detail.

## D025 — Room creation is one plain request

**Decision:** Creating a room is a single synchronous request: validate, upload both portraits to S3 under a new room ID, insert the room row, return the share link. There is no setup workflow.

**Context / requirement:** [D014](#d014--single-create-and-publish-request-with-temporal-setup-compensation) wrapped two small uploads and one row in a Temporal workflow with staging, copying, and compensation. That is disproportionate for a prototype.

**Chosen approach:** The room row is inserted last, so a room exists only if its portraits do. If the request fails partway, the director resubmits and gets a new room.

**Why it fits:** The director's experience is unchanged, and the only cost of a partial failure is two orphaned images that the seven-day lifecycle rule removes.

**Alternatives considered:** Keeping the setup workflow as a second Temporal showcase; the performance workflow already demonstrates durable execution.

**Tradeoffs and consequences:** A retried submission after a lost response creates a second room. Acceptable at this scale. Supersedes [D014](#d014--single-create-and-publish-request-with-temporal-setup-compensation).

**Status:** accepted — plan approval on 2026-10-07.

## D026 — Server and worker run as separate processes

**Decision:** The Go server (HTTP, WebSockets) and the Go Temporal worker are two processes built from one codebase.

**Context / requirement:** The headline demonstration is killing the worker mid-performance. In the earlier single-process design that would also have dropped every browser connection.

**Chosen approach:** The two never call each other. The server reaches the workflow through Temporal (start, signal, query); the workflow reaches the server through Redis ([D024](#d024--redis-streams-for-live-updates-and-workflow-query-on-connect)).

**Why it fits:** Browsers stay connected through a worker restart and simply see updates pause and resume. The split is cheap now that no audio flows from worker to browser ([D022](#d022--premiere-is-the-only-audio-playback)).

**Alternatives considered:** One process, which is simpler to run but makes the recovery demonstration misleading.

**Tradeoffs and consequences:** Two things to start locally and two services to deploy.

**Status:** accepted — plan approval on 2026-10-07.

## D027 — No concurrency cap or staleness sweep

**Decision:** Remove the configurable limit on concurrent performances and the automatic sweep that failed stale runs. One performance per room ([D005](#d005--one-performance-per-room)) remains.

**Context / requirement:** [D020](#d020--concurrent-performances-are-a-configurable-capacity-limit) bounded provider spend and audio fan-out. There is no fan-out any more, director accounts are administrator-created, and the sweep had no defined owner.

**Chosen approach:** Start checks only that the room has no performance yet. A workflow run timeout bounds a run that hangs.

**Why it fits:** Less to build, and nothing left for the cap to protect at invited-account scale.

**Alternatives considered:** Keeping a soft cap as a count at Start; easy to add back if cost becomes a concern.

**Tradeoffs and consequences:** Concurrent provider spend is unbounded in principle. A run ended by the timeout cannot mark its own row failed, so the server must treat a closed workflow with a `running` row as failed. Supersedes [D020](#d020--concurrent-performances-are-a-configurable-capacity-limit).

**Status:** accepted — plan approval on 2026-10-07.

## D028 — The director votes; audience slots are never released

**Decision:** The director can vote in every round. Audience slots, once taken, stay taken for the life of the room.

**Context / requirement:** Both were left open by the earlier design and raised in [the review](review.md).

**Chosen approach:** The director votes over their own WebSocket connection and is identified to the workflow by their director ID, so they are one voter, do not need an audience session, and do not use one of the ten audience slots. A round therefore has at most eleven votes. An audience session is never freed on disconnect or by a timer.

**Why it fits:** A director demonstrating alone can drive a whole performance. Never releasing slots keeps the rule that a reconnecting browser is always the same voter, with no cleanup logic.

**Alternatives considered:** Requiring the director to open the audience link to vote. Expiring sessions that have not connected for some time, or raising the cap.

**Tradeoffs and consequences:** Extra tabs, devices, or cleared cookies can fill a room before the intended audience arrives; the remedy is a new room.

**Status:** accepted — explicit user answers on 2026-10-07.

## D029 — Simple built-in director login for now

**Decision:** Directors log in with a username and password checked by the Go server, which sets a session cookie. Cognito is not used in the prototype.

**Context / requirement:** Only a handful of administrator-created directors exist, and the priority is a design that is quick to prototype and run locally.

**Chosen approach:** A `directors` table with hashed passwords (a slow password hash such as bcrypt or argon2), rows added by hand, no signup. Login creates a director session using the same random-secret cookie pattern as audience sessions. One piece of middleware turns the cookie into a verified director ID; no handler knows how the director was authenticated.

**Why it fits:** It works offline with nothing else running, tests can create a director and log in directly, and it reuses a mechanism the project already needs.

**Alternatives considered:** A Cognito user pool, which provides password reset, multi-factor, and managed credential storage, but needs a redirect login flow, token verification, and a real AWS user pool even for local runs and tests.

**Tradeoffs and consequences:** The project owns password hashing and the login endpoint, and there is no self-service password reset or multi-factor. Because authentication sits behind one middleware, moving to Cognito later replaces that middleware and the login page only.

**Status:** accepted — explicit user answer on 2026-10-07.

## D030 — Session cookies for audience and director

**Amended 2026-10-08 ([D034](#d034--share-links-are-derived-from-a-server-secret)):** still accepted for session cookies. The share-link part changed: no token or hash is stored for a room's link; it is derived from a server secret.

**Decision:** Audience members and directors are both identified by a session cookie holding a random secret. The database stores only a hash of it.

**Context / requirement:** The scheme was written up in [Audience session](audience-session.md) as a proposal, and [D029](#d029--simple-built-in-director-login-for-now) then built the director login on it. The design-readiness check on 2026-10-08 found it had never been accepted.

**Chosen approach:** 32 random bytes in an `HttpOnly`, `Secure`, `SameSite=Lax` cookie; SHA-256 of the secret stored on the session row. Audience cookies are named per room so one browser can attend several. Sessions expire after seven days. The share link stays valid for the life of the room. The server checks the `Origin` header on the WebSocket handshake, since that handshake is authenticated by cookie.

**Why it fits:** Browsers send cookies automatically on a same-origin WebSocket handshake but cannot attach custom headers to one, so a cookie serves HTTP and WebSocket alike. Storing only a hash means a database leak does not expose usable credentials.

**Alternatives considered:** A token held in page JavaScript, which is readable by any script on the page and cannot be sent on the WebSocket handshake without putting it in the URL.

**Tradeoffs and consequences:** A cleared cookie, private window, or second device is a new audience member and takes a new slot ([D028](#d028--the-director-votes-audience-slots-are-never-released)). The media expires after the same seven days, so an expired session loses access to nothing that still exists.

**Status:** accepted — approved with the design-readiness plan on 2026-10-08.

## D031 — WebSocket and Redis Streams kept as departures from the defaults

**Decision:** Keep one WebSocket per browser for updates and votes, and Redis Streams between worker and server, although the project's reference defaults point elsewhere for both.

**Context / requirement:** The design-readiness check on 2026-10-08 found two departures that had not been recorded in the required form.

**Chosen approach:**

Realtime transport:

- **Preferred default:** Server-Sent Events for updates, with votes as ordinary HTTP requests. The realtime pattern prefers this when client writes are occasional, and a browser sends at most eight votes per run.
- **Proposed alternative:** One WebSocket carrying updates down and votes up.
- **Reason to switch:** It matches the user's original sketch and is the transport the user wants to build and learn. One connection carries everything a room needs.
- **Tradeoff:** The browser must implement reconnection itself, where Server-Sent Events reconnect automatically, and a vote has no built-in request/response, so the server sends an explicit `vote_received`.

Worker-to-server transport:

- **Preferred default:** No Redis without a concrete need. PostgreSQL LISTEN/NOTIFY could carry the updates using a store the project already has.
- **Proposed alternative:** Redis Streams.
- **Reason to switch:** The user chose it explicitly ([D024](#d024--redis-streams-for-live-updates-and-workflow-query-on-connect)). A stream lets the server resume from its last-read position after a brief disconnect from Redis.
- **Tradeoff:** One more component to run locally and on AWS.

**Why it fits:** Both choices work at this scale and neither affects correctness, because snapshots come from the workflow and the revision rule tolerates duplicates and gaps.

**Alternatives considered:** The two defaults above. Either can be adopted later without touching the workflow: the transport sits entirely in the publish activity and the server's forwarding code.

**Tradeoffs and consequences:** As listed per transport.

**Status:** accepted — approved with the design-readiness plan on 2026-10-08.

## D032 — Aurora DSQL on AWS, plain PostgreSQL locally

**Decision:** The AWS deployment uses Aurora DSQL as its database. Local development and tests use a plain PostgreSQL container. One code path serves both.

**Context / requirement:** The design had named RDS PostgreSQL since the first draft without comparing it to anything. The user asked what the AWS Agent Toolkit recommends. Its database-selection skill, followed for this application (a new prototype asking for PostgreSQL, with no feature outside DSQL's supported surface), recommends Aurora DSQL, with Aurora PostgreSQL serverless as the alternative.

**Chosen approach:** Checked against current AWS documentation: foreign keys, unique constraints, and `jsonb` columns are supported; triggers, extensions, and LISTEN/NOTIFY are not, and the design uses none. Four rules keep one code path working on both databases:

- Every write transaction runs through a helper that retries on SQLSTATE `40001`, because DSQL has no row locks and reports conflicts at commit.
- The ten-session cap is a conditional update of a counter on the room row, in the same transaction as the session insert, replacing "lock the room, count, insert".
- Primary keys are UUIDs generated by the application.
- Migrations hold one schema statement each, never mixed with data changes; indexes on DSQL use `CREATE INDEX ASYNC`.

On AWS the server and worker connect through AWS's DSQL connector for pgx, which issues short-lived IAM tokens from their task roles, using a scoped database role.

**Why it fits:** The database is five small tables with a handful of writes per performance. DSQL costs nothing when idle, has no instance to size or patch, and needs no database password. A demo that sits unused most of the time benefits from all three.

**Alternatives considered:** Aurora PostgreSQL serverless: full PostgreSQL, identical to local behaviour, near-zero idle cost with auto-pause, but a wake-up delay after idle and a cluster to manage. RDS for PostgreSQL: a fixed instance at roughly fifteen dollars a month while running, with no advantage over either for this application.

**Tradeoffs and consequences:** The local and deployed databases behave differently under contention, so conflict retries are first exercised for real at deployment; the simultaneous-joins test is run against DSQL for that reason. Schema changes are more restricted: dropping or retyping a column means recreating the table. Whether goose runs unmodified against DSQL is not confirmed. The toolkit's `aurora-dsql` skill still describes foreign keys as an application-layer concern, while current AWS documentation says they are supported; this decision follows the documentation, to be confirmed when the cluster is created. Region availability must be checked once a region is chosen.

**Status:** accepted — explicit user choice on 2026-10-08.

## D033 — Votes carry the server's receipt time

**Decision:** The server stamps each vote with the time it received it and sends that with the signal. The workflow counts a vote if the stamp is at or before the round's deadline and the round has not yet closed. It does not use the order in which it happens to see votes and the timer.

**Context / requirement:** [D018](#d018--workflow-owned-voting-by-temporal-signal) relied on signals and the timer sharing one ordered history, "so whether a vote arrived before the deadline is settled by that order". The step 2 spike tested whether Go workflow code can observe that order after a worker outage. It cannot. With the worker down, Temporal records a vote, the timer firing, and a later vote in the right order, but delivers them to the returning worker in one batch. By the time workflow code runs, the timer is already ready and all the signals are already buffered. Three ordering-based approaches were tried and each misclassified votes in at least one outage scenario; results are in [the review](review.md), item 5.

**Chosen approach:**

- The workflow computes the deadline as its own current time plus the window when it starts the timer, and publishes that same deadline to browsers.
- The server records its clock when a vote arrives and includes it in the signal.
- The workflow accepts a vote whose stamp is not after the deadline.
- When the timer is delivered, the workflow handles every vote still buffered before closing the round. A vote that reaches the workflow after the round has closed is ignored regardless of its stamp.

This algorithm was run through the spike's three scenarios and was correct in every run.

**Why it fits:** It gives the same answer whether the worker was up or down, which is the property the recovery demonstration needs. It also makes the deadline browsers see and the deadline the workflow enforces the same value.

**Alternatives considered:** Relying on selector order or on whether the timer future is ready, all shown wrong by the spike. Reading event timestamps from history inside the workflow, which the Go SDK does not expose for signals. An activity that reads the workflow's history when the round closes and counts only the votes recorded before the timer fired: this uses Temporal's recorded order exactly and needs no clock comparison, but adds a network call and a failure mode to every round and was not tested in the spike; it remains the option to revisit if clock skew ever matters. Accepting misclassification during outages, which would make the worker-restart demonstration produce visibly wrong results.

**Tradeoffs and consequences:** Lateness now compares two clocks, the server's and Temporal's, which D018 set out to avoid. Both are synchronised to network time, so the exposure is a few milliseconds of skew at the edge of a ten-second window; a vote that close to the deadline could go either way. A vote stamped just before the deadline can still miss the round if it reaches the workflow after the round closes, which only happens within the network delay of one request. The vote signal gains one field.

**Amended 2026-10-09 (step 10):** the stamp must also be at or after the moment the round opened. Choice IDs are `c1` to `c3` on every turn, so a vote sent while the lines were still being written would otherwise have counted for lines nobody had seen. The rest stands, and is now exercised for real: an end-to-end test stops the worker across a deadline, casts one vote before it and one after, and checks that only the first counts.

**Status:** accepted — explicit user answer on 2026-10-08, after reviewing the step 2 spike.

## D034 — Share links are derived from a server secret

**Decision:** A room's share-link token is an HMAC-SHA256 of the room ID under a server secret. Nothing about the link is stored. The link is `{PUBLIC_BASE_URL}/join/{roomId}/{token}`.

**Context / requirement:** [D030](#d030--session-cookies-for-audience-and-director) stored only a hash of the link's token, like a session secret. Building room creation showed the flaw: the director's room page must show the link again after a reload, and a hash cannot be turned back into the link. The director would have had one chance to copy it.

**Chosen approach:** The server rebuilds the link whenever the director reads their room, and checks a presented link by recomputing the token and comparing in constant time. The secret, `SHARE_LINK_SECRET`, must be at least 32 characters and has no default: the server refuses to start without it, because a default in code would let anyone who can read the repository forge links. Local development generates one on first run and keeps it in a git-ignored file; the test stack uses a fresh one per run; AWS will hold it in Secrets Manager.

**Why it fits:** The link can always be shown again, and a leaked database contains nothing that opens a room.

**Alternatives considered:** Storing the token itself on the room row: simplest, but a database leak would expose every room's link. Showing the link once, as D030 implied: a lost link would make the room unusable.

**Tradeoffs and consequences:** One more secret to configure and protect. Changing it invalidates every existing link at once, and there is no way to revoke a single room's link. Anyone holding the secret can forge a link to any room whose ID they know. The `rooms` table has no share-link column, and joining presents the room ID together with the token.

**Status:** accepted — explicit user choice on 2026-10-08.

## D035 — WebSocket origin rule

**Decision:** The WebSocket handshake is accepted only when its `Origin` header is present and names either the web app's origin (`PUBLIC_BASE_URL`: scheme, host, and port) or the same host and port the request was addressed to. Anything else gets `403` before any session is looked up.

**Context / requirement:** The handshake is authenticated by cookie ([D030](#d030--session-cookies-for-audience-and-director)), and a browser attaches cookies to a WebSocket handshake whichever site's page opened it. The system design already said other sites must be refused; step 8 had to say exactly which origins are not "other". Three callers must pass: a browser coming through the Next.js development proxy, which rewrites `Host` to the Go server's address while `Origin` stays the web app's; a browser behind the AWS load balancer, where both are the public address; and the Go end-to-end client, which sends the Go server's own URL.

**Chosen approach:** One check in `server/internal/theater/origin.go`, run first in the handler. The first clause covers every real browser on the web app, by whichever route it arrives, including straight to the Go server's port in local development. The second covers a page served from the address the request reached, which is the test client. The WebSocket library is given the same allowed origin as a second check.

**Why it fits:** No new configuration: `PUBLIC_BASE_URL` already exists for share links. It leaves both local routes open (through the proxy, or directly to the port), so the theatre pages in step 14 can use either.

**Alternatives considered:** The library's default, which also accepts a missing `Origin`: harmless against browsers, which always send one, but there is no reason to accept it. A separate list of allowed origins in configuration: more to set and get wrong, with nothing yet that needs it. Comparing hosts only, ignoring scheme: would let a plain-HTTP page on the same host connect to an HTTPS deployment.

**Tradeoffs and consequences:** A non-browser client must set `Origin` itself. `PUBLIC_BASE_URL` must be exactly what the browser's address bar shows, without a default port written out, or every browser is refused. A second web origin (a preview deployment, say) would need the rule extended.

**Status:** accepted — approved in the step 8 plan on 2026-10-09.

## D036 — Updates carry the whole state

**Decision:** Every `update` message carries the complete public state of the performance as it stands at that revision, in exactly the shape a `snapshot` has. It is not a description of what changed.

**Context / requirement:** The system design said an update carries "one published change" and left its shape open. Step 9 had to fix the shape, because the workflow, the Redis stream, the server, the test client, and later the web pages all depend on it.

**Chosen approach:** The workflow keeps one public state value. Each publish replaces it, raises the revision, and sends the whole value. The `state` query returns the same value, so the server forwards a query answer as a snapshot without translating it. A browser needs one rule for both message types: take the state if its revision is higher than the one held.

**Why it fits:** A browser that misses an update is correct again at the next one, without needing a resync for anything but the missed moment. There is no merge logic in the browser to get wrong and no second shape to keep in step with the first. The state is small: at most three choices and eight short lines.

**Alternatives considered:** Deltas, each update carrying only the fields its phase introduces: smaller messages, but the browser must merge them, a missed update leaves it wrong until it resyncs, and snapshot and update become two shapes.

**Tradeoffs and consequences:** Updates repeat the lines chosen so far, so the last ones are a few kilobytes instead of a few hundred bytes. Per-browser data cannot go in an update, since every browser in the room gets the same bytes; a voter's own vote (step 10) therefore travels in the snapshot and in `vote_received`, not in updates.

**Status:** accepted — approved in the step 9 plan on 2026-10-09.

## D037 — The server builds clip URLs

**Decision:** Each clip in the `premiere` update is `{turn, speaker, url}`. The URL is a path on the server, `/api/v1/performances/{performanceId}/turns/{n}/audio`. The browser plays the URL it is given and never assembles one.

**Context / requirement:** Review item 15 left open whether the clip list carries URLs or the browser builds them from the performance ID and turn number, and suggested the browser. The clip list was first built in step 9 as `{turn, speaker}`; the user chose URLs before that step merged, so the contract was never released in the other shape.

**Chosen approach:** One function, `performance.AudioURL`, builds the path, and one constant beside it, `performance.AudioRoute`, is the route the server registers in step 12. Both are in the package the server and the worker share, and a test checks that every URL the function builds is matched by the route. The workflow calls the function when it builds the premiere, which keeps it deterministic: the URL depends only on the performance ID and the turn. The path is relative, like the portrait URLs a room returns, so it is correct behind the development proxy and the load balancer alike, and the worker does not need to know the public address.

**Why it fits:** Only the Go code knows the audio route. The web page has no path to keep in step with the server, and the route can change without a web change. It matches what rooms already do with `portraitUrl`.

**Alternatives considered:** The browser building the address: the smallest message and a worker that knows nothing about HTTP, but the same path written in two codebases. Putting the URL on each entry of `lines` and dropping `clips`: one list where there are two, but the user preferred a clip list that is complete on its own. A clip of `{turn, url}` with the speaker looked up in `lines`: less repetition, but the page must join two lists to play.

**Tradeoffs and consequences:** Worker code now produces a server route, held together by the shared function and its test. The URL is recorded in workflow history and in the Redis stream; a run lasts minutes, so a route change mid-run is not a concern. Turn and speaker appear in both `clips` and `lines`, accepted for a self-contained clip. The URLs are published from step 9 but answer `404` until step 12 builds the endpoint. They are not storage URLs: the bucket stays private and the server serves the audio.

**Status:** accepted — explicit user choice on 2026-10-09.

## D038 — No stand-in lines for choice generation

**Decision:** If the language model cannot produce a turn's three lines, the workflow keeps retrying for a bounded time (two minutes by default) and then fails the performance. It never substitutes lines of its own.

**Context / requirement:** The product brief said choice generation "uses bounded retries and a simple valid fallback", and D017, the system design, and the implementation plan repeated it as "falls back to valid lines". Before step 11 the user asked what those lines would be. Nothing had defined them. The only reading was canned text held in the worker, "valid" only in passing the format checks.

**Chosen approach:** Choice generation is retried with growing pauses (1, 2, 4, 8, then every 15 seconds) until `CHOICES_RETRY_WINDOW` has passed since the first attempt. Browsers stay on the "writing the next lines" state throughout. If the window runs out, the run takes the same failure path as a speech failure: the row is marked `failed` with reason `choices_failed`, and browsers are told. An error that retrying cannot fix, such as a rejected API key, fails the run at once. An answer that fails validation counts as a failed attempt. Tagging keeps its fallback, because there the fallback is the real line spoken plainly.

**Why it fits:** Canned lines know nothing of the scene, the characters, or what has been said. The audience would vote on filler; the winning filler would enter the dialogue history sent to the model for every later turn; and it would be voiced and played at the premiere as part of the show. A language-model outage is usually either brief, which a longer retry absorbs, or prolonged, in which case tagging would be failing too and the show is not worth finishing. Failing is also honest: the brief already requires that an unrecoverable failure be shown and not passed off as a completed performance.

**Alternatives considered:** The canned fallback as written: the run always finishes, but with a visibly broken turn in the recording. Failing after the original three attempts: simple, but a few seconds of trouble would end a performance, and D007 gives the director no way to retry. A template filled with the character's name: still filler.

**Tradeoffs and consequences:** A long outage ends the performance, two minutes later than it would have with three attempts, and the room cannot be run again (D005). The audience may wait up to two minutes on one turn. The brief's sentence and the S9 test were reworded; the failure path gained a second way in.

**Status:** accepted — explicit user choice on 2026-10-10.

## D039 — A finished room's snapshot is its last published state

**Decision:** A browser that connects to a room whose performance has completed is sent the `premiere` state exactly as it was published, at revision 25, rebuilt from the performance row. The premiere's start time is stored on the row for this. There is no `completed` phase on the WebSocket. A failed room's snapshot is `failed` with its reason, at revision 0.

**Context / requirement:** Until step 12 a finished room's snapshot only named a phase, `completed` or `failed`, at revision 0. The brief requires that reopening the room link shows the script and replays the premiere, so the snapshot needed the lines and the clips. Three things were open: its shape, its revision, and what a browser connecting during the countdown is told, since the start time was computed after the row was saved and stored nowhere.

**Chosen approach:** The workflow fixes the start time (its current time plus the countdown, in UTC, to the microsecond the database keeps) before the final save. The one write that marks the row `completed` stores it with the script and audio keys, and the premiere update then carries the same value. For a `completed` row the server builds the state from the row: `phase: premiere`, `turn: 8`, the lines and clips from the saved script (each clip's URL from `performance.AudioURL`), and the stored `startsAt`. Its revision is 25, a constant: every completed run has published three updates a turn and then the premiere, and the revision goes up with each publish whether or not it reached Redis. For a `failed` row the state is `phase: failed` with `failure` built from the stored reason, at revision 0. The column was added to the `performances` create-table migration instead of a new one, because only local test data existed.

**Why it fits:** Everyone holds the same state for a finished room whenever they connected: the same message, the same revision, and no second shape for pages to handle. A browser that connects during the countdown joins it, instead of being offered the recording ahead of everyone else (D022). A browser that reconnects after the run does not see its revision go from 25 back to 0. The pages (step 14) must handle a `premiere` whose start time has passed in any case, for the browsers that watched it, so replay asks nothing more of them.

**Alternatives considered:** A separate `completed` state without a start time, at revision 0, as first planned: no schema or workflow change, but a browser connecting during the countdown could play up to five seconds early, and revisions appear to step backwards on reconnecting. The same at a fixed revision 26: tidier, with the same early-play gap. Sending `premiere` until the start time and `completed` after, decided by the server's clock: independent of the browser's clock, but two shapes for one situation. Storing the last revision on the row, which would be exact for failed runs too: a second workflow change for a number nothing needs.

**Tradeoffs and consequences:** The final save comes out of the countdown; a database being retried can leave little or none. Whether a late browser counts down or offers the recording depends on its own clock, so one that runs slow can count down to a premiere that is over. The system design's "connects after the start time gets the completed snapshot" no longer describes the system and was rewritten. A failed snapshot has less than the failed update did: no turn, no lines, and no true revision, because a failed run saves no script; a `failed` update still on its way is let through and says the same. Rows written before the column existed have no start time; there were only local ones, and they were deleted.

**Status:** accepted — explicit user choices on 2026-10-11, in the step 12 plan.

## D040 — Stored objects are read through a body that can seek

**Decision:** `objectstore.Object.Body` is an `io.ReadSeekCloser`. The audio endpoint hands it to Go's `http.ServeContent`, which parses the `Range` header and writes the `206`, `416`, and `HEAD` replies. The object is streamed to the response, never held in memory.

**Context / requirement:** The audio endpoint must answer range requests, which Safari's audio element needs, and the system design said the range should be passed through to S3. `Store.Get` returned a plain reader and a size. Review item 14 also left open whether to pipe the object or buffer it.

**Chosen approach:** The filesystem store already returned a file, which can seek, so only the declared type changed. `ServeContent` seeks to the end to learn the size, seeks to the first byte asked for, and copies from there. The S3 store, built in step 15, must supply a body that makes those cheap: answer the seek to the end from the object's known size, and open its read at the offset last sought, as a ranged `GetObject`.

**Why it fits:** Range handling has many cases (open-ended and suffix ranges, several ranges at once, ranges past the end, `If-Range`), and the standard library's is correct and maintained. None of it is our code to test or get wrong. It also settles item 14: the response is piped.

**Alternatives considered:** A ranged read on the `Store` interface, with the handler parsing `Range` and writing the partial-content headers itself: maps to S3 in one call, but puts hand-written range parsing in this codebase. Reading the whole clip into memory and serving it from there: simplest, and a clip is only a few hundred kilobytes, but it is the buffering item 14 advised against.

**Tradeoffs and consequences:** The S3 store needs a small reader that turns a seek and a read into a ranged request, and a request against it costs one call for the size and one for the bytes. That reader does not exist yet, so range requests have only been exercised against the filesystem store.

**Status:** accepted — explicit user choice on 2026-10-11, in the step 12 plan.

## D041 — Recovering is a snapshot, and the browser stays

**Decision:** When a performance is running but its workflow cannot be asked for its state, the browser's snapshot is `{"phase": "recovering", "turn": 0, "performanceId": "…"}` at revision 0. The connection is kept and stays in the room. The browser asks again by sending `resync`.

**Context / requirement:** A snapshot of a running performance comes from a workflow query, and a query needs a worker. Until step 13 a failed query closed the socket with code `1013` and said nothing. The system design said the browser should be told the performance is recovering and retry; review item 11 left open whether there should be a message at all.

**Chosen approach:** `recovering` is a phase the server alone produces, like `waiting`. It is sent when the query is not answered within five seconds, or fails for any reason other than the workflow having closed (D042). The same snapshot function answers `resync`, so asking again is the retry, with no reconnection. Because the connection is already registered for the room, the first update published after the worker returns reaches it too, and every update carries the whole state (D036); `resync` then adds the browser's own vote. `resync` is passed on once a second per connection.

**Why it fits:** The first message is still always a snapshot. A retry costs one query and no handshake or session lookups. The browser cannot miss the moment the run resumes, because it is in the room when it does.

**Alternatives considered:** An `error` message and a `1013` close, with the browser reconnecting on a backoff: nothing held open and no new phase, but each retry is a new handshake and the browser is absent when updates resume. The same snapshot with the server itself retrying the query and pushing the answer: no retry logic in the page, at the cost of a timer per waiting connection and a departure from "the browser retries".

**Tradeoffs and consequences:** One more phase in the contract, which the pages must handle (step 14). A browser waits up to five seconds for `recovering`, the query's timeout; shortening it would call a busy worker "recovering". "Recovering" also covers causes that will not mend by themselves: Temporal unreachable, and a workflow that was never started (D043). Close code `1013` now means only that the database could not be read.

**Status:** accepted — explicit user choice on 2026-10-11, in the step 13 plan.

## D042 — A dead run is found by a refused query and announced to its room

**Decision:** The connect-time state query is sent on the condition that the workflow is open. If Temporal refuses it because the workflow has closed while the row still says `running`, the server marks the row `failed` with `internal_error` and sends the failed snapshot to every connection in the room.

**Context / requirement:** A run stopped by its 30-minute timeout, or terminated from outside, cannot mark its own row. The system design said that when a browser connects and the query fails, the server asks Temporal about the workflow and, finding it closed, marks the row failed. Building it showed the rule would not fire: with a worker up, a closed workflow still answers a query, with the state it stopped in. The end-to-end test saw exactly that, a terminated run answering `voting`.

**Chosen approach:** `QueryWorkflowWithOptions` with the reject condition "not open": one call that is answered, refused as closed, or fails. On a refusal the server calls `Fail`, which changes only a row still `running`, reads the row again, and answers from it. If the row is now `failed`, the same snapshot is queued for every other connection in the room, marked so that no browser drops it as older than what it holds.

**Why it fits:** One call where the design had two, and it catches the case the rule exists for. Reading the row again makes the race harmless: a run that completed between the row read and the query is answered as the premiere, and nothing is announced. Telling the room matches every other failure, where all browsers are told; otherwise the browsers already connected would wait on a round that will never close.

**Alternatives considered:** Describing the workflow only after a failed query, as designed: misses a closed run whenever a worker is up. Describing it before every query: correct, but two calls on every connection. Telling only the browser that connected, as designed: the smallest change, and leaves the rest of the room looking at a paused screen.

**Tradeoffs and consequences:** A snapshot can now arrive unasked. A dead run is noticed only when someone connects or resyncs; nothing sweeps for them (D027). The reason recorded is always `internal_error`, whatever ended the run. Verified by terminating a workflow, not by waiting out a real timeout.

**Status:** accepted — the room-wide announcement is an explicit user choice on 2026-10-11; the reject condition was proposed in the step 13 plan and approved with it.

## D043 — A connecting browser never starts a workflow

**Decision:** If the performance row says `running` and Temporal has no such workflow, the server does not start one when a browser connects. The browser is sent `recovering` (D041). The workflow is started only by the director pressing Start, which already starts it whenever the row says `running`.

**Context / requirement:** Review item 9, decided on 2026-10-08, said a connecting browser should start a missing workflow, to repair a Start that wrote the row and then failed to start the workflow. Planning step 13, the user asked what conditions leave a row without a workflow. There are two kinds. During Start: Temporal unreachable or erroring (the director sees `503`), the server dying between the two steps, or the director's browser dropping the request between them. Long after Start: a run that timed out without marking its row and whose workflow Temporal has since deleted at the end of its retention period; the server pointed at a different Temporal; or, locally, a deleted dev database.

**Chosen approach:** Not-found is treated like any other unanswered query. Nothing else changes: Start is safe to repeat and repairs the first kind.

**Why it fits:** The server cannot tell the two kinds apart. Starting on connect would repair the first and, for the second, begin a fresh performance in an old, dead room the moment someone opened its link, with nobody expecting it. Start is a deliberate act by the one person allowed to make it (D007).

**Alternatives considered:** Always starting, as item 9 said. Starting only when the row is younger than the run timeout and marking older ones failed: repairs the common case automatically, but a failed Start would still come to life from an audience member reloading, without the director pressing anything.

**Tradeoffs and consequences:** After a Start that answered `503` or was cut off, the room says `recovering` until the director presses Start again; a director who walks away leaves it so. A row whose workflow Temporal has forgotten says `recovering` for good and is never marked failed. The first row of the table in the system design, section 6, and review item 9 were rewritten.

**Status:** accepted — explicit user choice on 2026-10-11, reversing review item 9.
