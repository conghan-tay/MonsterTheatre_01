# Monster Theatre — Design Review

Updated 2026-10-08 for the simplified design (D022–D031), after a run through the design-readiness checklist. The first version of this review covered the earlier architecture with PostgreSQL snapshots, live audio streaming, and a data lake. Most of its findings no longer apply; the table at the end records what happened to each.

Scope: high-level issues in [the product brief](monster-theatre-product-brief.md), [decisions](decisions.md), and [system design](system-design.md). Implementation detail is out of scope.

## Open items

### Confirmed with you

1. **Late joiners can vote in an open round.** Decided 2026-10-07: yes. Anyone in the room can vote while a round is open, including someone who joined mid-round (D024).
2. **Director authentication.** Decided 2026-10-07: a simple built-in login for now, with Cognito as a possible later swap (D029).
3. **Can the director vote?** Decided 2026-10-07: yes. The director votes through their own connection as one extra voter (D028).
4. **Audience slots never release.** Decided 2026-10-07: accepted. Ten sessions per room, and closing a tab does not free a slot; a room filled by extra tabs or devices needs a new room (D028).

### To verify with a spike

5. **Vote and timer ordering after a worker outage.** Temporal records signals and the timer in one ordered history. But when a worker returns after an outage, a vote before the deadline, the timer firing, and a vote after it may all reach the workflow code at once, and the Go SDK may not show their relative order. Test: stop the worker across a deadline, send votes on either side, restart, and check which were counted. This comes from knowledge of the Go SDK and is not verified. The stakes are lower than before, since a miscounted late vote only affects one round.

### Closed

6. **ElevenLabs model and output format.** Closed 2026-10-08. The user confirmed that the [Text to Dialogue convert endpoint](https://elevenlabs.io/docs/api-reference/text-to-dialogue/convert) with `eleven_v4_turbo` supports audio tags with the two chosen voices. Short gaps between MP3 clips at the premiere are accepted and need no check.

### Small design points

Items 7 to 10 are decided. Items 11 to 16 are settled while building.

7. **Start the timer before publishing the deadline.** Decided 2026-10-08: the workflow starts the timer first, then publishes the deadline (system design, section 4).
8. **What happens when a publish fails.** Decided 2026-10-08: publishing retries briefly, then the workflow continues; a browser that sees a revision gap sends `resync` (system design, sections 4 and 5).
9. **Start that half-completes.** Decided 2026-10-08: when a browser connects and the workflow is not found, the server starts it with the same ID (system design, section 6).
10. **A workflow that ends without marking its row.** Decided 2026-10-08: when a browser connects and the workflow is closed, the server marks the row `failed` (system design, section 6).
11. **Recovery status.** The brief no longer requires a visible "recovering" state. Connected browsers just see updates pause. If you want a message, the server can show one when no worker answers a query.
12. **Waiting between rounds.** After each vote come tagging, speech, and the next generation, with nothing to listen to. The page should show the winning line and a "preparing the next line" state. Running tagging and speech in the background during the next round is a later improvement noted in the system design.
13. **Database cleanup.** S3 objects expire after seven days; nothing removes old PostgreSQL rows. Not urgent for a demo.
14. **Audio pass-through or buffer.** Whether the server pipes the S3 body straight to the response or loads the clip first. Suggested: pipe it through.
15. **Clip URLs.** Whether the `premiere` clip list carries URLs or the browser builds them from the performance ID and turn number. Suggested: the browser builds them.
16. **Audio caching.** Whether clip responses are cacheable for replays. Suggested: private caching, since a recording never changes once saved.

## What happened to the earlier findings

| Earlier item | Outcome |
| --- | --- |
| A1 One process must be both killable and available | Resolved. Server and worker are separate processes (D026); no audio flows between them |
| A2 Late votes missing from the close transaction | Gone. Rejected votes are no longer recorded (D023) |
| A3 History order versus what workflow code sees | Still open as item 5 |
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

1. Run the spike in item 5 early; it is small.
2. Settle items 11 to 16 as each part is built.
