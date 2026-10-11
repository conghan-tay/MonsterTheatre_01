# Monster Theatre — Design Review

Updated 2026-10-08 for the simplified design (D022–D033), after a run through the design-readiness checklist. The first version of this review covered the earlier architecture with PostgreSQL snapshots, live audio streaming, and a data lake. Most of its findings no longer apply; the table at the end records what happened to each.

Scope: high-level issues in [the product brief](monster-theatre-product-brief.md), [decisions](decisions.md), and [system design](system-design.md). Implementation detail is out of scope.

## Open items

### Confirmed with you

1. **Late joiners can vote in an open round.** Decided 2026-10-07: yes. Anyone in the room can vote while a round is open, including someone who joined mid-round (D024).
2. **Director authentication.** Decided 2026-10-07: a simple built-in login for now, with Cognito as a possible later swap (D029).
3. **Can the director vote?** Decided 2026-10-07: yes. The director votes through their own connection as one extra voter (D028).
4. **Audience slots never release.** Decided 2026-10-07: accepted. Ten sessions per room, and closing a tab does not free a slot; a room filled by extra tabs or devices needs a new room (D028).

### Closed

5. **Vote and timer ordering after a worker outage.** Closed 2026-10-08 by the step 2 spike. The concern was real: workflow code cannot rely on seeing votes and the timer in the order Temporal recorded them. The remedy, a receipt-time stamp on each vote, is recorded as D033 (accepted).

   What was tested: a workflow with a 4-second window, with the worker killed and restarted around the deadline, three runs per case, on Go SDK v1.49.0 against the local dev server. Each way of deciding "was this vote in time?" was compared with the order in Temporal's history.

   | Way of deciding | Worker up | Outage across the deadline | Outage, both votes early |
   | --- | --- | --- | --- |
   | Selector, signal case registered first | Correct | Wrong: counts the late vote | Correct |
   | Selector, timer case registered first | Correct | Correct | Wrong: rejects an in-time vote |
   | Ask whether the timer is ready | Correct | Wrong: rejects the in-time vote | Wrong: rejects both |
   | Receipt-time stamp compared with the deadline | Correct | Correct | Correct |

   Why: with the worker down, history records `Signal(before) TimerFired Signal(after)` with no worker in between. When the worker returns it gets all three in one batch, and by the time workflow code runs, the timer is already ready and every signal is already buffered. Which one the code sees first then depends on how the code is written, not on the recorded order.

   The algorithm step 10 will use (stamp check, plus handling every buffered vote when the timer is delivered) was also run through all three scenarios and was correct in 9 of 9 runs. Raw output: [spikes/vote-order-spike-output.txt](spikes/vote-order-spike-output.txt).

6. **ElevenLabs model and output format.** Closed 2026-10-08. The user confirmed that the [Text to Dialogue convert endpoint](https://elevenlabs.io/docs/api-reference/text-to-dialogue/convert) with `eleven_v4_turbo` supports audio tags with the two chosen voices. Short gaps between MP3 clips at the premiere are accepted and need no check.

### Small design points

Items 7 to 10 are decided. Items 11 to 17 are settled while building; 14, 15, and 16 have been.

7. **Start the timer before publishing the deadline.** Decided 2026-10-08: the workflow starts the timer first, then publishes the deadline (system design, section 4).
8. **What happens when a publish fails.** Decided 2026-10-08: publishing retries briefly, then the workflow continues; a browser that sees a revision gap sends `resync` (system design, sections 4 and 5).
9. **Start that half-completes.** Decided 2026-10-08: when a browser connects and the workflow is not found, the server starts it with the same ID. **Reversed 2026-10-11** (D043): a connecting browser never starts a workflow. The room says `recovering` until the director presses Start again, which already repaired it.
10. **A workflow that ends without marking its row.** Decided 2026-10-08: when a browser connects and the workflow is closed, the server marks the row `failed` (system design, section 6).
11. **Recovery status.** The brief no longer requires a visible "recovering" state. Connected browsers just see updates pause. Decided 2026-10-11 for the server: a browser that connects while the workflow cannot be asked is sent a snapshot with phase `recovering` and stays connected (D041). What the page shows for it is step 14's.
12. **Waiting between rounds.** After each vote come tagging, speech, and the next generation, with nothing to listen to. The page should show the winning line and a "preparing the next line" state. Running tagging and speech in the background during the next round is a later improvement noted in the system design.
13. **Database cleanup.** S3 objects expire after seven days; nothing removes old database rows. Not urgent for a demo.
14. **Audio pass-through or buffer.** Decided 2026-10-11: piped. The server streams the object to the response and reads only the bytes a range request asks for (D040).
15. **Clip URLs.** Decided 2026-10-09: the `premiere` clip list carries URLs; the browser does not build them (D037).
16. **Audio caching.** Decided 2026-10-11: `Cache-Control: private, max-age=604800, immutable`, the same as portraits. Only a completed run's recordings are served, and they never change (system design, section 5).
17. **Migrations on Aurora DSQL.** Whether goose runs unmodified against DSQL is not confirmed. Check at deployment; the fallback is applying the same SQL files with a small script (D032).

## What happened to the earlier findings

| Earlier item | Outcome |
| --- | --- |
| A1 One process must be both killable and available | Resolved. Server and worker are separate processes (D026); no audio flows between them |
| A2 Late votes missing from the close transaction | Gone. Rejected votes are no longer recorded (D023) |
| A3 History order versus what workflow code sees | Confirmed by the spike and resolved; see item 5 |
| A4 Capacity count race | Gone. The cap was removed (D027) |
| A5 "No S3 URL" rule contradicted portrait delivery | Resolved. The server serves portraits and audio itself |
| A6 Muddled randomness mechanism | Resolved. The workflow picks the winner with its replay-safe random source |
| B1 Workflow-start intent had no owner | Resolved; see item 9 |
| B2 Staleness sweep underspecified | Gone with the sweep; the remnant is resolved in item 10 |
| B3 No way to publish retry status | Requirement dropped; see item 11 |
| B4 Director replay undefined | Resolved. Reopening the room link replays the premiere |
| B5 Audience slots never release | Accepted (D028) |
| B6 Retention covered only S3 | Reduced to item 13 |
| B7 Two deadlines | Resolved; see item 7 |
| B8 First-audio latency measured at an undefined point | Gone with live audio |
| B9 Contradictory in-round vote feedback | Resolved. The snapshot includes the browser's own vote |
| B10 Smaller gaps | Gone or resolved. The audio format question is closed (item 6) |
| Setup workflow weight | Resolved. Room creation is one plain request (D025) |
| Pacing between rounds | Still open as item 12 |

## Suggested order

1. Settle items 11 to 17 as each part is built.
