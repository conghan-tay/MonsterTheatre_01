# Monster Theatre — Product Brief

Status: Agreed concept; requirements baseline for the first milestone.

## Product concept

Monster Theatre is a small web experience in which an audience collaboratively chooses the dialogue for two characters. Each chosen line becomes expressive, streamed speech. After eight turns, a five-second countdown introduces an uninterrupted premiere of the performance the audience created.

The project demonstrates ElevenLabs real-time dialogue and audio tags, stateful stream processing with Apache Flink, and a queryable data lake on Amazon S3. Uploaded character images establish visual identity today and preserve inputs for a future AI avatar animation milestone.

Randomness in dialogue and delivery is part of the experience. Turn order, voting eligibility, committed results, and playback progression must remain predictable.

## Actors

- **Director:** Submits complete room setup once, receives and shares its audience link, starts the performance, and inspects saved artifacts through the S3 console.
- **Audience member:** Enters through the shared link, listens, and votes on dialogue choices.
- **Demo operator:** Inspects Flink processing and recovery, and queries structured performance data. The director may also fill this role.

## First-milestone scope

- Access is limited to the director and a small group of invited demo users.
- The first working milestone is an AWS-hosted demo reachable by invited users through the shared link. Temporal Cloud manages the automatic workflow; Go workers run on AWS.
- One active performance at a time, with up to ten audience members.
- One performance per room. Another performance requires a new room and audience link, including after a failed run.
- One setup submission creates and publishes the room with all scene context, both character contexts, and both portraits. The director then sees the shareable link and a Start button. Setup cannot be edited or revised; any change requires a new room.
- Scene context and characters A/B belong to the Room resource. Characters have no independent resource lifecycle or management APIs.
- Performance is the resource for one run, containing its eight turns, each turn's voting state, and synthesis-attempt summaries. Accepted votes are durable records owned by the performance, without independent CRUD APIs.
- After the initial Start, the flow proceeds automatically. There are no Stop or manual retry controls.
- Two uploaded character images, two character names, and two hardcoded voices. Optional short character briefs.
- A short scene topic, optional scene description, and optional genre.
- Eight alternating speaking turns: four per character, beginning with Character A.
- Three AI-generated dialogue choices and a ten-second voting window for every turn, including the introduction and ending.
- Static character portraits, with the speaking character highlighted.
- Streamed playback of each selected turn, followed by an uninterrupted full-performance premiere.
- Media artifacts and queryable structured events in S3. Manual inspection and subsequent replay through the S3 console/downloaded artifacts; no in-app performance library or file browser.

## Audience and director journey

1. The director completes a single setup form with scene context, both character names/briefs, and two portraits, then submits everything in one request to create and publish the room.
2. Once setup succeeds, the director sees the shareable audience link and a **Start** button, and shares the link. The room setup is already immutable; changes require a new room.
3. Each audience member clicks **Enter theatre**. This joins the room and activates audio playback in that browser. If playback remains blocked, show **Tap to hear audio**.
4. The director clicks **Start**. The automatic performance begins using the room's already fixed character and scene settings.
5. The AI generates three choices for the scheduled character and story beat, using the selected dialogue so far.
6. Once the choices are ready, the server opens a ten-second voting window.
7. Flink computes the result; the application commits one winner.
8. The AI adds audio tags to the winning text without rewriting its words. The application sends it to ElevenLabs and plays audio as chunks arrive.
9. When the turn finishes on the room playback timeline, the next turn begins with choice generation.
10. After all eight turns, prepare the full playback, count down **5, 4, 3, 2, 1**, and play the uninterrupted performance.
11. Mark the performance finished. Keep its audio, script, images, decisions, and structured events for inspection.

The final premiere uses the recorded audio from the individual turns. It does not regenerate speech. Because the audience has already heard the turns, the finale is an uninterrupted presentation of their work rather than a surprise reveal.

## Story and dialogue rules

- A **turn** is one character's complete spoken contribution.
- Character A always gives a short introduction.
- Speakers alternate. Middle turns use **REACT** or **QUESTION** beats.
- The final turn combines **EXPLAIN + PUNCHLINE**, delivered by one character. With eight alternating turns, Character B closes the first-milestone performance.
- One initial structure is:

  `A INTRO → B QUESTION → A REACT → B QUESTION → A QUESTION → B REACT → A REACT → B EXPLAIN + PUNCHLINE`

- Each round offers three actual dialogue lines with meaningfully different directions, rather than three delivery styles for identical words.
- Choice generation receives character briefs, scene context, selected dialogue, and remaining turns. Rejected choices are retained for inspection but do not become story history.
- The ending must respond to the established scene. The AI is reminded to resolve it as turns run out; consistent comedic quality is not an MVP guarantee.
- Audio-tag enhancement preserves the winning words. Store both the original and tagged versions.
- Bound input and generated-line lengths so the performance stays short. Exact limits and model prompts can be settled during design.
- Images serve as portraits in this milestone; automatic visual interpretation is deferred.

## Prioritized functional requirements

### 1. Create and publish a room in one submission, then start

**A director should be able to submit complete scene context, both characters, and both portraits in one request, receive a published room's audience link, and start its automatic performance.**

- **Trigger and outcome:** The director submits valid complete setup once; the backend saves the room and portraits and returns the shareable link. Present the link and Start button after successful creation. Audience members can enter the waiting room before the director starts.
- **Validation and authorization:** Require both character images and names and a scene topic, validate supported image formats and size limits, and bound text inputs. Only an authenticated director can create or start its room. Setup has no edit/revision operation or separate publication step. Audience links grant viewing and voting access only. Prevent duplicate creation on retried submissions, duplicate starts, and a second performance in the same room.
- **State:** Room owns immutable scene context and both character components, identified by slots A/B, with references to their private S3 portraits. Internal provisioning must complete before the room is shareable or startable. Start creates a performance referencing the already fixed room setup; saved artifacts include that context for inspection.
- **Visible failures:** Explain invalid uploads or inputs and failed setup. Do not show a shareable link or enabled Start for incomplete provisioning. A retried submission can recover its existing creation result after a lost response. Reject unauthorized actions and conflicting starts. Show an unrecoverable failed performance explicitly rather than treating it as successfully completed.

### 2. Choose and hear each turn

**An audience member should be able to choose one of three dialogue lines during each voting round and hear the winning line as expressive streamed audio.**

- **Trigger and outcome:** A voting round opens after valid choices are ready; eligible votes produce one committed winner, followed by tagged synthesis and playback.
- **Validation and authorization:** One vote per audience session per turn. Server receipt time determines eligibility. Reject invalid choices and votes received after the deadline. Retried submissions must not add votes.
- **State:** Performance owns turn choices, voting state, committed winners, and synthesis-attempt summaries. Save accepted votes in a separate durable ledger and retain original/tagged text, attempt events, and audio artifacts for inspection. Temporal carries execution context; database milestone snapshots expose published performance state. The winner becomes immutable before synthesis starts.
- **Visible failures:** Reject late votes clearly. Show **Counting votes…** if processing is behind. Preserve the winner on synthesis failure and retry automatically within bounded limits without reopening voting. Show recovery status; if recovery is exhausted, retain partial artifacts and mark the run failed.

Voting defaults:

- Most votes wins.
- Randomly resolve ties; randomly select a choice when there are no votes. Persist the selection and reason.
- Anonymous audience sessions are sufficient for the demo. Reconnection reuses the same identity; strong prevention of multiple identities is deferred.
- Late joiners see the current phase and participate in the next open round.
- AI choice generation uses bounded retries and a simple valid fallback. Generation time does not consume the voting window.
- Advance using the room playback timeline; a slow or disconnected listener cannot block the room.

### 3. Premiere, retain, replay, and inspect the performance

**The audience should be able to hear the full performance after the countdown, and the director should be able to replay and inspect the saved performance through S3 artifacts.**

- **Trigger and outcome:** All turns finish and the assembled playback is ready; the room plays the complete performance. The director later uses the S3 console to locate/download its artifacts.
- **Validation and authorization:** Require all completed turn recordings before the premiere countdown. S3 access is restricted to the operator; sharing a room does not grant bucket access.
- **State:** Save the complete audio and a manifest linking the performance, character assets, ordered turns, and timing. Mark completion explicitly.
- **Visible failures:** Show preparation or automatic-retry status if assembly/storage fails. Mark unrecoverable partial performances as failed/incomplete; never present missing audio as a successful full recording.

## Flink and S3 responsibilities

**Flink performs useful product work:** aggregate votes by performance and turn, deduplicate submissions, compute final tallies, and produce results for the application to commit. It also correlates turn lifecycle events to derive participation and latency metrics.

The authoritative deadline comes from the server, not audience device clocks. Finalization must include all eligible accepted votes before committing a winner; delayed internal processing must not silently discard them. Once committed, a result cannot change during playback.

Keep the replayable voting/event input durable so Flink can recover from checkpoints. Checkpointed processing does not imply exactly-once external speech generation or browser playback.

**S3 has two distinct purposes:**

- **Media storage:** Original character images, individual turn recordings, complete performance audio, script/decision details, and a manifest with speaker order and timing.
- **Data lake:** Structured vote, round, turn, synthesis, and playback events, plus derived metrics, in queryable Parquet files partitioned initially by date. Include schema versions, event IDs, and room/performance/turn IDs.

Keep media objects separate from lake records; lake records reference media rather than embedding audio. Query only finalized lake files. Flink's streaming file sink finalizes files through successful checkpoints, so lake freshness differs from live room state.

Example demo questions:

- Which turns had the most participation?
- How often did a round require a tie-break or no-vote fallback?
- What was first-audio latency by voice or story beat?
- How many duplicate or late submissions were rejected?

Choose the query tool during technical design. The product does not need an analytics UI.

## Non-functional requirements

These are first-milestone acceptance targets, to validate against actual provider behavior during implementation.

- **Workload:** One active room/performance and ten audience sessions; eight voting bursts of at most ten accepted votes each. No multi-region deployment requirement.
- **Latency:** Vote acknowledgement p95 under 300 ms; winner committed within one second of window closure under normal operation; first audible output p95 under two seconds after submitting/flushing a tagged turn. Measure choice-generation and tag-enhancement time separately.
- **Ordering and consistency:** One active turn and voting round per performance, one committed winner per round, strict speaker order, and no automatic return to a completed round. Client reconnects receive current published room/performance state from database snapshots while workflow workers recover. Published state reflects durable milestones rather than every internal workflow substep.
- **Durability and recovery:** Acknowledged votes and committed choices survive a worker restart. Keep completed turn audio for exact replay. Target Flink recovery within 60 seconds and finalized lake records within 60 seconds of performance completion under normal operation.
- **Degradation:** Pause voting-result progression while Flink cannot finalize a round and resume automatically after recovery. Already available audio can continue playing. On speech-provider failure, retain the chosen line and retry automatically within bounded limits. Expose recovery/failure status without manual performance controls. Dashboard-free lake inspection may lag behind live playback.
- **Security:** Director authentication, audience access through a room link, server-side provider keys, private encrypted S3 objects, and limits on uploads, text, room starts, and synthesis retries.
- **Retention:** Use synthetic demo content; retain performance assets and event data for seven days by default, with a manual preservation option for selected demo runs. Expiration includes associated media and structured records.
- **Environment:** Desktop browsers first. Activate the audio player during Enter theatre or director Start, retain a playback-blocked fallback, and do not depend on every browser finishing in lockstep.
- **Cost and operation:** Track generation usage, first-audio latency, vote-processing lag, checkpoint failures, and synthesis failures. Session-duration and daily-generation caps are not required for this limited-access demo.

## Learning and interview demonstration

| Learning goal | Demonstration |
| --- | --- |
| ElevenLabs real-time dialogue | Two hardcoded voices, speaker turns, and playback of arriving audio chunks |
| Expressive audio tags | Original winning text compared with its tagged synthesis input and audible delivery |
| Apache Flink | Keyed voting state, deadline handling, deduplication, aggregation, checkpoints, and recovery |
| S3 data lake | Structured Parquet data and historical queries, alongside separate media objects |
| Future AI avatars | Preserved images, individual turn audio, speaker identities, and performance timeline |
| Interview readiness | A short live performance, S3 artifact inspection, a lake query, and a controlled duplicate-vote or worker-restart demonstration |

Flink is included deliberately as a learning objective. This demo workload could use simpler voting logic, but the project provides a concrete way to demonstrate stateful processing and discuss its operational tradeoffs.

## Deferred scope and open design work

Deferred: variable turn counts, voice selection, microphone input, animation, automatic image interpretation, an in-app library/file browser, analytics dashboards, strong audience identity checks, and tuning dialogue randomness.

Future animation should begin as offline processing of saved assets, potentially with individual character shots. InfiniteTalk supports image-and-audio inputs, but arbitrary monster portraits and a combined two-character scene require separate validation. The MVP does not promise animation compatibility.

Technical design will settle the durable event source, vote-finalization contract, client synchronization, exact providers/model IDs, concrete length limits, automatic-retry budgets and recovery deadlines, audio assembly, lake query tooling, and deployment setup. These choices do not change the product concept.

## Reference documentation

- [ElevenLabs: Stream dialogue in real-time](https://elevenlabs.io/docs/eleven-api/guides/how-to/websockets/realtime-tdd) — multi-voice models, turns, buffering/flush, and keep-alives. The current guide supports multiple registered voices with `eleven_v4`; `eleven_v4_turbo` permits one per connection.
- [ElevenLabs: Audio tags](https://elevenlabs.io/docs/help-center/product/core-capabilities/text-to-speech/how-do-audio-tags-work-with-eleven-v3-and-v4) — expressive delivery instructions.
- [Apache Flink: Filesystem connector](https://nightlies.apache.org/flink/flink-docs-stable/docs/connectors/datastream/filesystem/) — Parquet output and checkpoint-based file finalization.
- [MDN: Autoplay guide](https://developer.mozilla.org/en-US/docs/Web/Media/Guides/Autoplay) — browser audio activation and playback-blocked handling.
- [InfiniteTalk: Official repository](https://github.com/MeiGen-AI/InfiniteTalk) — future image/audio animation exploration.
