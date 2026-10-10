# Monster Theatre — Product Brief

Status: Agreed concept; requirements baseline for the first milestone. Simplified on 2026-10-07: audio plays only at the premiere, and the data lake and analytics were removed from this milestone (see [the decisions log](decisions.md), D022–D031).

## Product concept

Monster Theatre is a small web experience in which an audience collaboratively chooses the dialogue for two characters. Each chosen line is turned into expressive speech behind the scenes. After eight turns, a five-second countdown introduces the premiere: the first time the audience hears the performance they created.

The project demonstrates durable workflow execution with Temporal and expressive speech with ElevenLabs audio tags. Uploaded character images establish visual identity today and preserve inputs for a future AI avatar animation milestone.

Randomness in dialogue and delivery is part of the experience. Turn order, one vote per person, committed results, and progression must remain predictable.

## Actors

- **Director:** Submits complete room setup once, receives and shares its audience link, starts the performance, and can vote alongside the audience. Several director accounts exist; each owns its own rooms and runs them independently of the others.
- **Audience member:** Enters through the shared link, votes on dialogue choices, and hears the premiere.
- **Demo operator:** Inspects workflow execution and recovery in Temporal, and saved files in S3. The director may also fill this role.

## First-milestone scope

- Access is limited to a small set of administrator-created director accounts and the invited demo users they share links with.
- The first working milestone is an AWS-hosted demo reachable by invited users through the shared link. Temporal Cloud manages the automatic workflow; Go workers run on AWS. Prototyping happens locally first.
- Up to ten audience members per performance. Rooms run independently: a performance in one room never blocks a performance in another.
- One performance per room. Another performance requires a new room and audience link, including after a failed run.
- One setup submission creates the room with all scene context, both character contexts, and both portraits. The director then sees the shareable link and a Start button. Setup cannot be edited; any change requires a new room.
- Scene context and characters A/B belong to the Room. Characters have no independent lifecycle or management APIs.
- Performance is one run of eight turns. While it runs, its state lives in the Temporal workflow; when it finishes, the final script and audio references are saved.
- After the initial Start, the flow proceeds automatically. There are no Stop or manual retry controls.
- Two uploaded character images, two character names, and two hardcoded voices. Optional short character briefs.
- A short scene topic, optional scene description, and optional genre.
- Eight alternating speaking turns: four per character, beginning with Character A.
- Three AI-generated dialogue choices and a ten-second voting window for every turn, including the introduction and ending.
- Static character portraits, with the speaking character highlighted during the premiere.
- No audio during the turns. The only playback is the full performance at the premiere.
- Portraits and turn recordings are stored in S3. There is no data lake, analytics, in-app library, or file browser.

## Audience and director journey

1. The director completes a single setup form with scene context, both character names/briefs, and two portraits, then submits everything in one request to create the room.
2. Once setup succeeds, the director sees the shareable audience link and a **Start** button, and shares the link. Setup is immutable; changes require a new room.
3. Each audience member clicks **Enter theatre**. This joins the room and activates audio playback in that browser for later. If playback is blocked when the premiere starts, show **Tap to hear audio**.
4. The director clicks **Start**. The automatic performance begins using the room's fixed character and scene settings.
5. The AI generates three choices for the scheduled character and story beat, using the selected dialogue so far.
6. Once the choices are ready, a ten-second voting window opens.
7. When the window closes, the votes are counted and one winner is chosen. Everyone sees the winning line as text.
8. Behind the scenes, the AI adds audio tags to the winning text without rewriting its words, and the tagged line is turned into speech and saved. Nothing is played yet.
9. The next turn begins with choice generation.
10. After all eight turns, count down **5, 4, 3, 2, 1**, and play the eight recordings in order as one performance.
11. Mark the performance finished. Keep its audio, script, and images.

Reopening the room link after the performance has finished shows the script and replays the premiere from the start.

## Story and dialogue rules

- A **turn** is one character's complete spoken contribution.
- Character A always gives a short introduction.
- Speakers alternate. Middle turns use **REACT** or **QUESTION** beats.
- The final turn combines **EXPLAIN + PUNCHLINE**, delivered by one character. With eight alternating turns, Character B closes the performance.
- One initial structure is:

  `A INTRO → B QUESTION → A REACT → B QUESTION → A QUESTION → B REACT → A REACT → B EXPLAIN + PUNCHLINE`

- Each round offers three actual dialogue lines with meaningfully different directions, rather than three delivery styles for identical words.
- Choice generation receives character briefs, scene context, selected dialogue, and remaining turns. Rejected choices do not become story history.
- The ending must respond to the established scene. The AI is reminded to resolve it as turns run out; consistent comedic quality is not an MVP guarantee.
- Audio-tag enhancement preserves the winning words. Keep both the original and tagged versions in the saved script.
- Bound input and generated-line lengths so the performance stays short.
- Images serve as portraits in this milestone; automatic visual interpretation is deferred.

## Prioritized functional requirements

### 1. Create a room in one submission, then start

**A director should be able to submit complete scene context, both characters, and both portraits in one request, receive the room's audience link, and start its automatic performance.**

- **Trigger and outcome:** The director submits valid complete setup once; the backend saves the portraits and the room and returns the shareable link. Audience members can enter the waiting room before the director starts.
- **Validation and authorization:** Require both character images and names and a scene topic, validate supported image formats and size limits, and bound text inputs. Only an authenticated director can create or start its room. Audience links grant viewing and voting access only. Prevent duplicate starts and a second performance in the same room.
- **State:** Room owns immutable scene context and both characters, identified by slots A/B, with references to their private S3 portraits. Start creates the room's one performance.
- **Visible failures:** Explain invalid uploads or inputs and failed setup; the director resubmits. Reject unauthorized actions and conflicting starts. Show an unrecoverable failed performance explicitly rather than treating it as completed.

### 2. Choose each turn

**An audience member should be able to choose one of three dialogue lines during each voting round and see the winning line.**

- **Trigger and outcome:** A voting round opens after valid choices are ready; votes produce one winner, shown to everyone as text. The line is then tagged and turned into speech for the premiere.
- **Validation and authorization:** One vote per audience session per turn; the first vote counts. Votes for unknown choices and votes arriving after the window closes are ignored. Whether a vote arrived in time is decided on the server, not by device clocks.
- **State:** The performance workflow holds the choices, votes, and winners while the run is in progress. The winner cannot change once chosen.
- **Visible failures:** Retry failed generation, tagging, or speech automatically within bounded limits without reopening voting. If retries are exhausted, mark the run failed.

Voting defaults:

- Most votes wins.
- Randomly resolve ties; randomly select a choice when there are no votes. The selection is made once and cannot be re-rolled by recovery.
- Anonymous audience sessions are sufficient for the demo. Reconnection reuses the same identity; strong prevention of multiple identities is deferred.
- Late joiners see the current phase and can vote in any round that is still open.
- AI choice generation is retried for a bounded time. If it still fails, the performance fails visibly; the audience is never offered stand-in lines the AI did not write. Generation time does not consume the voting window.

### 3. Premiere and replay

**The audience should be able to hear the full performance after the countdown, and anyone with access to the room should be able to replay it afterwards.**

- **Trigger and outcome:** All eight recordings are saved; the room counts down and plays them in order.
- **Validation and authorization:** Require all eight recordings before the countdown. S3 is private; browsers receive portraits and audio through the application, never a storage URL.
- **State:** Save the final script (choices, winners, original and tagged text) and the eight recording references. Mark completion explicitly.
- **Visible failures:** Never present a run with missing audio as a successful performance; mark it failed.

## Non-functional requirements

These are first-milestone targets, to validate during implementation.

- **Workload:** Ten audience sessions per performance and eight voting rounds. A handful of performances at once at most. No multi-region requirement.
- **Latency:** Vote acknowledged within 300 ms (p95); winner shown within one second of the window closing under normal operation. No target is set for generation, tagging, or speech time; they happen between rounds.
- **Ordering and consistency:** One active turn and voting round per performance, one winner per round, strict speaker order, and no return to a completed round. A reconnecting client receives the current state.
- **Durability and recovery:** Votes already received and winners already chosen survive a worker restart. Target resumption of progression within 60 seconds of a worker restart.
- **Degradation:** While the worker is down, the performance pauses and a newly connecting browser cannot load the current state until it returns. Already-connected browsers stay connected and resume when updates do.
- **Security:** Director authentication, audience access through a room link, server-side provider keys, private encrypted S3 objects, and limits on uploads and text.
- **Retention:** Use synthetic demo content; expire stored media after seven days by default.
- **Operation:** Failures and retries are visible to the operator through application logs and the Temporal UI.
- **Environment:** Desktop browsers first. Activate audio during Enter theatre or director Start, and keep a playback-blocked fallback.

## Learning and interview demonstration

| Learning goal | Demonstration |
| --- | --- |
| Temporal durable execution | A workflow that owns the eight-turn loop, voting by signal, a durable timer for the voting window, activity retries across provider calls, and recovery from a killed worker |
| Expressive audio tags | Original winning text compared with its tagged version and its audible delivery at the premiere |
| Realtime updates | Workflow state published through Redis Streams and pushed to browsers over WebSockets |
| Future AI avatars | Preserved images, individual turn audio, and speaker identities |
| Interview readiness | A short live performance and a controlled worker-restart demonstration |

Durable execution is the headline engineering objective. Killing the worker mid-performance makes recovery visible in the workflow history rather than something to assert.

Earlier versions of this design included Apache Flink in the voting path, an S3 data lake with Athena queries, and live audio streamed to listeners after every vote. Each was removed because it added more moving parts than a first prototype could justify; the reasoning is recorded in [the decisions log](decisions.md).

## Deferred scope

Deferred: per-turn audio playback and live audio streaming, a data lake and analytics, a single assembled recording, variable turn counts, voice selection, microphone input, animation, automatic image interpretation, an in-app library/file browser, and strong audience identity checks.

Future animation should begin as offline processing of saved assets. The MVP does not promise animation compatibility.

## Reference documentation

- [ElevenLabs: Audio tags](https://elevenlabs.io/docs/help-center/product/core-capabilities/text-to-speech/how-do-audio-tags-work-with-eleven-v3-and-v4) — expressive delivery instructions.
- [Temporal: Sending messages to workflows](https://docs.temporal.io/sending-messages) — signals, queries, and updates.
- [MDN: Autoplay guide](https://developer.mozilla.org/en-US/docs/Web/Media/Guides/Autoplay) — browser audio activation and playback-blocked handling.
- [InfiniteTalk: Official repository](https://github.com/MeiGen-AI/InfiniteTalk) — future image/audio animation exploration.
