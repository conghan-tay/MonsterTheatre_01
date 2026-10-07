# Monster Theatre — Design Review

Updated 2026-10-07 for the simplified design (D022–D027). The first version of this review covered the earlier architecture with PostgreSQL snapshots, live audio streaming, and a data lake. Most of its findings no longer apply; the table at the end records what happened to each.

Scope: high-level issues in [the product brief](monster-theatre-product-brief.md), [decisions](decisions.md), and [system design](system-design.md). Implementation detail is out of scope.

## Open items

### To confirm with you

1. **Late joiners can vote in an open round.** The workflow no longer keeps a list of eligible sessions, so anyone in the room can vote while a round is open, including someone who joined mid-round. The earlier design made them wait for the next round. This is simpler; confirm it is acceptable (recorded as proposed in D024).
2. **Director authentication.** Cognito is still the proposal. For a local prototype a single configured director login would be quicker; decide when to introduce Cognito.
3. **Can the director vote?** Not stated anywhere. Simplest answer: no, unless they also open the audience link.
4. **Audience slots never release.** Ten sessions per room, and closing a tab does not free a slot. A few extra tabs or devices can fill a room. Options: accept it, raise the cap, or expire sessions that have not been connected for some time.

### To verify with a spike

5. **Vote and timer ordering after a worker outage.** Temporal records signals and the timer in one ordered history. But when a worker returns after an outage, a vote before the deadline, the timer firing, and a vote after it may all reach the workflow code at once, and the Go SDK may not show their relative order. Test: stop the worker across a deadline, send votes on either side, restart, and check which were counted. This comes from knowledge of the Go SDK and is not verified. The stakes are lower than before, since a miscounted late vote only affects one round.
6. **ElevenLabs model and output format.** Confirm which model supports audio tags on the plain text-to-speech endpoint with the two chosen voices, and that MP3 output plays back to back in a browser without awkward gaps.

### Small design points to settle while building

7. **Start the timer before publishing the deadline.** If the workflow publishes "voting open" and then starts the 10-second timer, the real deadline is later than the one browsers show by however long the publish took. Starting the timer first keeps them aligned.
8. **What happens when a publish fails.** If Redis is unavailable, a publish activity that retries forever stalls the performance. Suggested: retry briefly, then continue. A browser that notices a gap in revision numbers asks for a fresh snapshot.
9. **Start that half-completes.** Start inserts the performance row and then starts the workflow. If the server stops between the two, the row says `running` with no workflow. A repeated Start repairs it; decide what the room page shows until then.
10. **A workflow that ends without marking its row.** A run ended by the timeout cannot update PostgreSQL. When a query reports the workflow closed but the row says `running`, the server should mark it failed.
11. **Recovery status.** The brief no longer requires a visible "recovering" state. Connected browsers just see updates pause. If you want a message, the server can show one when no worker answers a query.
12. **Waiting between rounds.** After each vote come tagging, speech, and the next generation, with nothing to listen to. The page should show the winning line and a "preparing the next line" state. Running tagging and speech in the background during the next round is a later improvement noted in the system design.
13. **Database cleanup.** S3 objects expire after seven days; nothing removes old PostgreSQL rows. Not urgent for a demo.

## What happened to the earlier findings

| Earlier item | Outcome |
| --- | --- |
| A1 One process must be both killable and available | Resolved. Server and worker are separate processes (D026); no audio flows between them |
| A2 Late votes missing from the close transaction | Gone. Rejected votes are no longer recorded (D023) |
| A3 History order versus what workflow code sees | Still open as item 5 |
| A4 Capacity count race | Gone. The cap was removed (D027) |
| A5 "No S3 URL" rule contradicted portrait delivery | Resolved. The server serves portraits and audio itself |
| A6 Muddled randomness mechanism | Resolved. The workflow picks the winner with its replay-safe random source |
| B1 Workflow-start intent had no owner | Reduced to item 9 |
| B2 Staleness sweep underspecified | Gone with the sweep; a remnant is item 10 |
| B3 No way to publish retry status | Requirement dropped; see item 11 |
| B4 Director replay undefined | Resolved. Reopening the room link replays the premiere |
| B5 Audience slots never release | Still open as item 4 |
| B6 Retention covered only S3 | Reduced to item 13 |
| B7 Two deadlines | Reduced to item 7 |
| B8 First-audio latency measured at an undefined point | Gone with live audio |
| B9 Contradictory in-round vote feedback | Resolved. The snapshot includes the browser's own vote |
| B10 Smaller gaps | Gone or resolved, except PCM, which is replaced by item 6 |
| Setup workflow weight | Resolved. Room creation is one plain request (D025) |
| Pacing between rounds | Still open as item 12 |

## Suggested order

1. Answer items 1 to 4.
2. Run the two spikes (items 5 and 6) early; both are small.
3. Settle items 7 to 13 as each part is built.
