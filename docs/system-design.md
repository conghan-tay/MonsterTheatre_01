# Monster Theatre — System Design

Status: Simplified design, agreed with the user on 2026-10-07 and 2026-10-08 (D022–D034). It replaces the earlier draft built around PostgreSQL milestone snapshots, live audio streaming, and an S3 data lake. It passed the design-readiness checklist on 2026-10-08; the remaining build-time points are in [the review](review.md).

Source of truth: [Product brief](monster-theatre-product-brief.md). Significant choices are recorded in [Decisions](decisions.md).

## 1. What the system does

1. A director creates a room (scene, two characters, two portraits) and gets a share link.
2. Audience members open the link and enter the theatre.
3. The director starts the performance. For each of eight turns the system generates three lines, the audience votes for ten seconds, and the winning line is shown, tagged, turned into speech, and saved.
4. After turn eight, everyone counts down from five and hears the eight recordings played in order.

### Invariants

- One performance per room. A new run needs a new room (D005).
- Room setup is immutable once created (D013).
- After Start everything is automatic; there are no Stop or retry controls (D007).
- Eight alternating turns, A first, B last.
- One vote per voter per turn; the first vote counts. Voters are the audience sessions (up to ten) and the director, who votes through their own connection under their director ID (D028).
- Audience slots are never released; closing a tab does not free one (D028).
- One winner per turn, chosen once and never changed.
- No audio is played before the premiere (D022).
- S3 is private. Browsers get portraits and audio through the server.

### Requirements and how the design meets them

| Target from the brief | How the design meets it |
| --- | --- |
| Vote acknowledged within 300 ms (p95) | The server replies `vote_received` as soon as Temporal accepts the signal: one network call, no database write |
| Winner shown within one second of the window closing | The workflow already holds the votes when the timer fires; it picks the winner in memory and publishes once |
| One active round, one winner, strict order | A single workflow per performance runs the turns in a loop; nothing else can advance a turn |
| Votes and winners survive a worker restart | Signals and workflow state are stored by Temporal, not by the worker |
| Progression resumes within 60 seconds of a worker restart | Holds when the worker was between activities or waiting on the timer. If it died mid-activity, Temporal waits for that activity's timeout first; see the retry table in section 4 |
| Reconnecting client receives current state | Snapshot from a workflow query, then updates by revision (section 6). Accepted limit: no snapshot while no worker is running |
| Director authentication, link-only audience access, private storage, input limits | Section 7 |
| Media expires after seven days | S3 lifecycle rule. Database rows are not cleaned up in the prototype |
| Failures and retries visible to the operator | Section 8 |
| Desktop browsers, audio unlocked on Enter theatre | Frontend behaviour; the click that joins the room also unlocks audio for the premiere |

### Capacity

One performance involves at most 11 WebSocket connections, 25 published updates, 88 vote signals, 16 language-model calls, 8 speech calls, and 8 audio files of a few hundred kilobytes each, with only a handful of performances at once. One server instance and one worker instance are enough, the connection map can live in server memory, and no scaling machinery is needed.

## 2. Components

| Component | Role |
| --- | --- |
| Next.js frontend | Director setup form, audience theatre, voting, premiere player |
| Go server | One process with two modules. **Room service:** create rooms, audience sessions, Start. **Theater:** WebSocket connections, vote signalling, forwarding updates, serving portraits and audio |
| Go worker | A separate process from the same codebase. Runs the performance workflow and its activities (D026) |
| Temporal Cloud | Stores and drives the performance workflow |
| Redis Streams | The worker publishes performance updates; the server reads them and forwards to browsers (D024). ElastiCache on AWS, plain Redis locally |
| PostgreSQL | Rooms, audience sessions, and one row per performance. Aurora DSQL on AWS, a plain PostgreSQL container locally (D032) |
| S3 | Portraits and the eight turn recordings |
| Language model | Choice generation and audio-tag enhancement, behind a configurable adapter (D017) |
| ElevenLabs | Speech for each winning line: one request per turn to the Text to Dialogue convert endpoint with `eleven_v4_turbo`, the speaker's voice, and the tagged text |
| Director login | Built into the Go server: administrator-created accounts with hashed passwords, a login form, and a session cookie (D029). Handlers receive a verified director ID from one piece of middleware, so the mechanism can be swapped later |

```mermaid
flowchart LR
    Browser[Director and audience browsers] -->|HTTPS| ALB[Load balancer]
    ALB -->|Pages| Web[Next.js]
    ALB -->|API and one WebSocket per browser| Server["Go server (Room service and Theater)"]
    Server -->|Rooms, sessions, performance row| DB[("PostgreSQL (Aurora DSQL on AWS)")]
    Server -->|Portraits in, portraits and audio out| S3[(S3)]
    Server -->|Start workflow, signal votes, query state| Temporal[Temporal Cloud]
    Temporal <-->|Workflow and activity tasks| Worker[Go worker]
    Worker -->|Publish updates| Redis[(Redis Streams)]
    Redis -->|Read updates| Server
    Worker -->|Read room, save final result| DB
    Worker -->|Save turn audio| S3
    Worker <-->|Choices and tags| LLM[Language model]
    Worker <-->|Text to speech| TTS[ElevenLabs]
```

The server and the worker never talk to each other directly. The server reaches the workflow through Temporal (start, signal, query), and the workflow reaches the server through Redis.

The browser sees one origin. On AWS the load balancer routes `/api/*` and the WebSocket to the Go server and everything else to Next.js. Locally the Next.js app does that routing itself, proxying `/api/*` to the Go server, so cookies need no cross-origin handling in either place. Pages fetch from the API in the browser; Next.js does no data fetching of its own.

For prototyping, everything runs locally: the Temporal dev server, Redis, and PostgreSQL in containers, with a real S3 bucket or a local stand-in. On AWS the three application processes run as containers on ECS Fargate, and the database is Aurora DSQL.

## 3. Stored data

### PostgreSQL

| Table | Holds | Written when |
| --- | --- | --- |
| `directors` | Director ID, username, password hash. Rows are added by an administrator; there is no signup | By hand |
| `director_sessions` | Session ID, director, credential hash, expiry. Same pattern as audience sessions | At login |
| `rooms` | Owner (director ID), scene topic/description/genre, character A and B (name, brief, voice ID, portrait key), and `audience_count`. Nothing about the share link is stored (D034) | Once, at creation; `audience_count` goes up by one per new audience session |
| `audience_sessions` | Session ID, room, credential hash, timestamps. See [Audience session](audience-session.md) | When someone enters; `last_seen_at` on reconnect |
| `performances` | Performance ID, room (unique, so one per room), status (`running`, `completed`, `failed`), and on completion the final script and the eight audio keys, each a `jsonb` column (Aurora DSQL has no stored array type) | A row at Start; one update at the end |

The final script is one JSON document: for each turn the speaker, beat, three choices, vote counts, winner, how it was decided (majority, tie, no votes), the tagged text, and the model used.

There are no snapshot, event, or vote tables. While a performance is running, its state exists only in the workflow.

### Working on both databases

The same code runs against local PostgreSQL and Aurora DSQL (D032). Four rules make that possible:

- **Retry on conflict.** Aurora DSQL has no row locks; a transaction that conflicts with another fails at commit with SQLSTATE `40001`. Every write transaction runs through one helper that retries on that error with backoff. On local PostgreSQL the helper never has anything to retry.
- **Session cap by counter.** Admitting an audience member is one transaction: `UPDATE rooms SET audience_count = audience_count + 1 WHERE id = $1 AND audience_count < 10`, then the session insert. If the update changes no row, the room is full. Locally the row lock makes simultaneous joins queue; on DSQL they conflict and one retries.
- **Application-generated UUID keys.** No sequences are used.
- **Plain migrations.** One schema statement per migration, never mixed with data changes, and no column drops or type changes without recreating the table. On DSQL, indexes are created with `CREATE INDEX ASYNC`.

Nothing in the schema needs triggers, extensions, or LISTEN/NOTIFY, which DSQL does not support. The final script is a `jsonb` column well under DSQL's 1 MiB limit.

### S3

| Object | Key | Written when |
| --- | --- | --- |
| Portrait A and B | `rooms/{roomId}/portrait-a`, `portrait-b` | Room creation |
| Turn recording | `performances/{performanceId}/turn-{n}.mp3` | After each turn's speech is generated |

A lifecycle rule expires objects after seven days.

### Temporal workflow state

Room context, current turn and phase, the current round's choices and deadline, who has voted for what, the winners so far, the tagged lines and audio keys so far, and a revision number that increases with every published update.

## 4. Performance workflow

One workflow per performance, with ID `performance/{performanceId}`.

```mermaid
flowchart TD
    Start([Start]) --> Fetch[Activity: fetch scene and character context]
    Fetch --> Loop{"Turns remaining?"}
    Loop -->|Yes| Turn[Publish: generating, turn number and speaker]
    Turn --> Gen[Activity: generate three choices]
    Gen --> Timer[Start the 10 s timer]
    Timer --> Open[Publish: voting, choices and deadline]
    Open --> Wait[Collect vote signals until the timer fires]
    Wait --> Pick[Pick winner in workflow]
    Pick --> Won[Publish: preparing_line, winner and counts]
    Won --> Tag[Activity: enhance winning line with audio tags]
    Tag --> Speak[Activity: text to speech, save audio to S3]
    Speak --> Loop
    Loop -->|No| Save[Activity: save script and audio keys, mark completed]
    Save --> Prem[Publish: premiere, start time 5 s ahead]
    Prem --> End([End])
```

Notes on each part:

- **Publishing is an activity.** Workflow code must be deterministic and cannot talk to Redis itself, so every "Publish" box is a small activity that appends one message to the stream. Before each publish the workflow increases its revision number and includes it in the message.
- **Timer before publish.** The workflow computes the deadline (its current time plus the window), starts the 10-second timer, and then publishes that deadline, so browsers show the same deadline the workflow enforces.
- **Collecting votes.** A vote arrives as a signal carrying a voter ID (an audience session ID or the director's ID), turn number, and choice ID. The signal also carries the time the server received the vote. The workflow records the first vote from each voter for the current turn and ignores repeats, unknown choices, wrong turn numbers, and late votes.
- **Deciding lateness.** A vote is in time if the server's receipt time is at or before the deadline. The workflow does not rely on whether it saw the vote before or after the timer: the step 2 spike showed that after a worker outage those arrive together and their order is lost (D033). When the timer is delivered, the workflow first handles every vote still buffered, then closes the round. A vote that reaches the workflow after the round has closed is ignored whatever its stamp says.
- **Picking the winner.** Done in workflow code: most votes wins; a tie or an empty round is settled with Temporal's replay-safe random source, so a replay after a crash reaches the same answer.
- **Fallbacks.** Choice generation retries, then falls back to simple valid lines. Tag enhancement retries, then falls back to the untagged line. Text to speech retries; if it still fails the run fails.
- **Failure.** If the workflow cannot continue, it runs one last activity to mark the performance row `failed` and publishes a `failed` update. A workflow run timeout bounds a run that hangs.
- **A failed publish does not stop the run.** Publishing retries briefly and then the workflow carries on. A browser that receives an update whose revision skips a number knows it missed one and asks for a fresh snapshot.
- **Query handler.** The workflow answers a `state` query with its current turn, phase, choices, deadline, votes, winners so far, and revision. The answer has the same shape as a published update's state, so the server passes it on as the snapshot unchanged.
- **Settings.** The vote window and the premiere countdown are the worker's settings (`VOTE_WINDOW`, `PREMIERE_COUNTDOWN`). The first activity returns them with the room context, so the values a run used are recorded in its history and a replay sees the same ones.

### What is published, and when

Every publish moves the performance into a new phase and increases the revision by one. These are the only publish points:

| When | Phase in the update | What the update carries | What the browser shows |
| --- | --- | --- | --- |
| A turn begins | `generating` | Turn number, speaker, story beat | "Writing the next lines…" |
| Three choices are ready | `voting` | The choices and the deadline | The three choices and a countdown |
| The timer has fired and the winner is picked | `preparing_line` | The winner, vote counts, and how it was decided | The winning line while it is tagged and voiced |
| All eight turns are saved | `premiere` | Start time five seconds ahead and the ordered clip list, each clip with its URL | Countdown, then playback |
| The run cannot continue | `failed` | A short reason | A failure message |

Before Start there is one more phase, `waiting`, which is never published: with no performance there is no workflow to publish it. It appears only in the snapshot the server builds for a room that has not started, at revision 0, so the first published update is always newer.

A normal run publishes three updates per turn and one for the premiere: revisions 1 to 25. Tagging, text to speech, and saving to the database publish nothing of their own; the browser stays in `preparing_line` until the next turn's `generating` arrives (or `premiere` after turn eight).

### Retries and timeouts

Proposed starting values, to tune while building:

| Activity | Timeout per attempt | Attempts | Then |
| --- | --- | --- | --- |
| Generate choices | 30 s | 3 | Fallback lines |
| Enhance tags | 20 s | 2 | Untagged line |
| Text to speech | 60 s | 3 | Run fails |
| Publish update | 5 s | 3 | Continue without it |
| Database read or write | 10 s | Until the run timeout | — |
| Whole workflow run | 30 min | — | Run ends |

If a worker dies in the middle of an activity, Temporal waits for that activity's timeout before trying it again on another worker. Worst-case resumption is therefore the longest timeout above, not the worker's restart time.

A possible later improvement: since nothing is played until the premiere, tagging and speech for a turn could run in the background while the next turn's voting proceeds, shortening the wait between rounds. Start with the simple sequential loop.

## 5. Interfaces

### HTTP

| Operation | Who | Behaviour |
| --- | --- | --- |
| `POST /api/v1/director/login` | Anyone | Username and password as JSON; on success sets the director session cookie. Wrong password and unknown username get the same `401`. `POST /api/v1/director/logout` deletes the session and clears the cookie |
| `GET /api/v1/director/me` | Director | The logged-in director's ID and username; `401` without a live session. The web pages use it to know whether someone is logged in |
| `POST /api/v1/rooms` | Director | One multipart request with scene/character JSON and two portraits. Validate, upload portraits to S3 under a new room ID, insert the room row, return the room ID and share link. Invalid input gets `422` naming each field; an oversize portrait gets `413`. On failure the director resubmits (D025) |
| `GET /api/v1/rooms` | Director | The director's own rooms, newest first, so a director who closed the tab can find a room again |
| `GET /api/v1/rooms/{roomId}` | Director or audience session of that room | Scene and characters. The director's view adds the share link and `audienceCount`; the audience's view has neither. `401` with no credentials, `403` for a director who does not own the room |
| `POST /api/v1/rooms/{roomId}/performance` | Director | The body is an empty JSON object. Insert the performance row, then start the workflow. Returns `{performanceId, roomId, status}`: `201` the first time, `200` for a repeated Start, which returns the existing performance. `503` if the workflow could not be started; pressing again repairs it |
| `POST /api/v1/audience-sessions` | Anyone with the share link | Takes the link's room ID and token. `201` and a cookie for a new session; `200` with `restored: true` for a browser that already has one; `403` for any invalid link; `409` when the room has ten. See [Audience session](audience-session.md) |
| `GET /api/v1/rooms/{roomId}/portraits/{slot}` | Director or audience session | Portrait bytes, read from S3 by the server |
| `GET /api/v1/performances/{id}/turns/{n}/audio` | Director or audience session | Turn recording as `audio/mpeg`, read from S3 by the server. Supports range requests. Available once the performance is completed |
| `GET /api/v1/rooms/{roomId}/ws` | Director or audience session of that room | Upgrades to the WebSocket described below. Refusals are ordinary JSON errors sent before the upgrade: `403` `origin_not_allowed` for a page on another origin, then the same `401`, `403`, and `404` as reading the room |

Start is safe to retry: the unique room constraint stops a second row, and starting a workflow whose ID already exists is treated as success. While the row says `running`, every Start asks Temporal to start the workflow; a running one is left alone and a finished one is never run again. A row that is `completed` or `failed` is returned as it is.

Every error response has the same shape, `{"error": {"code": "...", "message": "..."}}`, where the code is stable and the message is for people.

Error outcomes: `401` not logged in or no session; `403` not your room; `404` unknown room or performance; `409` room already has ten sessions, or a conflicting Start; `413` upload too large; `422` invalid fields or image; `429` too many login attempts.

Validation bounds (unchanged): portraits JPEG, PNG, or WebP up to 5 MiB; names up to 80 characters; briefs up to 400; scene topic up to 200; description up to 1000; genre up to 80; generated lines up to 300.

### WebSocket

Each browser opens its own WebSocket to the server, authenticated by its session cookie (audience) or login (director). It carries state down and votes up. No audio travels on it.

Server to browser:

| Message | Contents |
| --- | --- |
| `snapshot` | Full current state and its revision. Always the first message after connecting |
| `update` | One published change with its revision. It carries the whole state as it now stands, in the same shape as a snapshot, not only what changed (D036) |
| `vote_received` | Confirms the server passed this browser's vote to the workflow |
| `error` | A vote could not be delivered (for example, the performance has ended) |

Every server message has the same envelope. A snapshot for a room that has not started:

```json
{"type": "snapshot", "revision": 0, "state": {"phase": "waiting", "turn": 0}}
```

Browser to server:

| Message | Contents |
| --- | --- |
| `vote` | Turn number and choice ID. The voter (audience session or director) comes from the connection, never from the message |
| `resync` | Asks for a fresh `snapshot`. Sent when the browser sees a gap in revision numbers |

The state of a running performance. A field is absent in the phases that do not have it:

```json
{"type": "update", "revision": 6, "state": {
  "phase": "preparing_line", "turn": 2, "performanceId": "…", "speaker": "B", "beat": "QUESTION",
  "choices": [{"id": "c1", "text": "…"}, {"id": "c2", "text": "…"}, {"id": "c3", "text": "…"}],
  "winner": {"choiceId": "c2", "text": "…", "counts": {"c1": 0, "c2": 0, "c3": 0}, "decidedBy": "no_votes"},
  "lines": [{"turn": 1, "speaker": "A", "text": "…"}, {"turn": 2, "speaker": "B", "text": "…"}]}}
```

| Field | Present in | Meaning |
| --- | --- | --- |
| `phase`, `turn` | Always | `turn` is 0 before the first turn |
| `performanceId` | Once started | Needed to fetch the recordings |
| `speaker`, `beat` | `generating`, `voting`, `preparing_line` | `A` or `B`, and the turn's story beat |
| `choices` | `voting`, `preparing_line` | The three lines, with IDs `c1` to `c3` |
| `deadline` | `voting` | When the round closes |
| `winner` | `preparing_line` | The chosen line, each choice's count, and `decidedBy`: `majority`, `tie`, or `no_votes` |
| `lines` | From the first winner on | The winning lines so far, in order |
| `premiere` | `premiere` | `startsAt` and the ordered `clips`. Each clip is `{turn, speaker, url}`, where `url` is a path on the server such as `/api/v1/performances/{id}/turns/1/audio` (D037) |

The winning line with its audio tags is not sent to browsers. It goes to the speech service and into the final script.

**Heartbeat.** The server pings each connection every 25 seconds and drops one that has not answered within 10. Browsers answer pings by themselves. This keeps a quiet connection alive through the load balancer's idle timeout and clears dead ones from the server's map.

**Closing.** The server closes a connection with code `1001` when it shuts down, with `1008` when a browser has fallen so far behind that its queue of unsent messages is full, and with `1013` when it could not get a snapshot from the workflow. In each case the browser reconnects and gets a fresh snapshot.

## 6. Flows

### Connecting and staying up to date

```mermaid
sequenceDiagram
    autonumber
    participant B as Browser
    participant S as Go server
    participant DB as PostgreSQL
    participant T as Temporal
    participant R as Redis Streams
    participant W as Worker

    Note over S,R: The server reads the stream continuously from startup
    B->>S: Open WebSocket with session cookie
    S->>DB: Check session, read performance row
    alt Performance completed or failed
        S-->>B: snapshot from the performance row
    else Performance running
        Note over S: Register this connection for the room and hold new updates
        S->>T: Query workflow state
        T->>W: Run query
        W-->>T: State at revision 12
        T-->>S: State at revision 12
        S-->>B: snapshot at revision 12
        Note over S: Release held updates above revision 12
    else Not started
        S-->>B: snapshot: waiting for the director
    end
    W->>R: Publish update, revision 13
    R-->>S: Update, revision 13
    S-->>B: update, revision 13, sent to every connection in the room
```

The browser replaces its state with each `snapshot` and applies an `update` only if its revision is higher than what it already has. That one rule covers first connection, reconnection, and duplicate delivery.

If the performance row says `running` but the query fails, the server asks Temporal about the workflow and acts on the answer:

| Temporal says | Meaning | Server does |
| --- | --- | --- |
| Workflow not found | Start wrote the row but never started the workflow | Starts it with the same ID, which is safe to repeat |
| Workflow closed | The run ended without updating its row, for example by timeout | Marks the row `failed` and sends that snapshot |
| Workflow running | No worker is available to answer | Tells the browser the performance is recovering; the browser retries |

The server keeps an in-memory map of room to open connections. It reads each update from Redis once and writes it to every connection for that room. The server reads the stream from "now" at startup; history is not needed because snapshots come from the workflow.

All performances share one stream, `performance-updates`. Each entry holds the room ID, the revision, and the message exactly as browsers receive it. The stream is trimmed to about a thousand entries. The reader notes the stream's last entry at startup and from then on always reads on from the last entry it has seen, including after a Redis outage; asking only for "what is new" on each read would lose an update that arrived between two reads.

Built so far (step 9): the first and third branches of the diagram, and the second while a worker is running. Until steps 12 and 13, a room whose performance has finished gets a snapshot that only names the phase (`completed` or `failed`), and a query that fails closes the connection with code `1013` instead of following the table above.

### Voting

1. The browser sends `vote` on its WebSocket.
2. The server notes the time, signals the workflow with the voter ID, turn, choice, and that receipt time, then replies `vote_received`.
3. The workflow keeps the vote if it is that voter's first for the current turn, the receipt time is not after the deadline, and the round has not yet closed.
4. When the timer fires, the workflow picks the winner and publishes it. Every browser receives the winner and the counts.

A browser that reconnects during a round gets its own vote back in the snapshot, because the workflow state includes who voted and the server sends each browser only its own entry.

### Premiere

1. After the eighth recording is saved, the workflow saves the final script and audio keys to the performance row and marks it `completed`.
2. It publishes `premiere` with a start time five seconds ahead and the ordered clip list (turn, speaker, URL).
3. Each browser counts down to the start time, then plays the eight clips in order with a normal audio element, fetching each from the URL the clip carries, and highlights the portrait of the clip's speaker. The browser never builds an audio address itself.
4. A browser that connects after the start time gets the completed snapshot and plays from the beginning.

Playback is not synchronised beyond the shared start time, which the brief accepts.

Two delivery rules keep the premiere continuous and working across browsers:

- **Preloading.** On receiving the `premiere` update, the browser starts fetching all eight clips during the five-second countdown, so each clip is ready before the previous one ends. If a clip is still loading when its turn comes, playback waits for it; nothing is skipped.
- **Range requests.** The audio endpoint answers partial-content requests: it honours the `Range` header, replies `206` with `Accept-Ranges: bytes`, and passes the range through to S3. Safari's audio element expects this.

### Worker restart

- The workflow resumes from its history when a worker is back: completed activities are not repeated, and the timer keeps its original deadline.
- Votes signalled while the worker is down are stored by Temporal. On return the workflow counts those whose receipt time is not after the deadline, and rejects the rest (D033).
- Browsers stay connected to the server throughout and see updates pause, then continue.
- A browser that connects while the worker is down cannot get a snapshot, because a query needs a worker. The server tells it the performance is recovering and the browser retries.

## 7. Security

- **Sessions.** Audience and director sessions use the same scheme (D030): a random secret in an `HttpOnly`, `Secure`, `SameSite=Lax` cookie, with only its hash stored. Sessions expire after seven days.
- **Share link.** `{PUBLIC_BASE_URL}/join/{roomId}/{token}`, where the token is an HMAC-SHA256 of the room ID under `SHARE_LINK_SECRET` (D034). Nothing is stored: the link is rebuilt whenever the director's page needs it and checked by recomputing. It is valid for the life of the room and grants an audience session for that room and nothing else. The server refuses to start without the secret, and changing the secret invalidates every existing link.
- **Portraits.** The type is detected from the file's own bytes, not from what the uploader claims; animated images and anything over 4096 by 4096 pixels are refused. Portraits are served with `nosniff`.
- **Ownership.** Every director operation checks that the room belongs to the logged-in director.
- **WebSocket.** The handshake is authenticated by cookie, so the server also checks the `Origin` header and refuses connections from other sites (D035). The header must be present and name either the web app's origin (`PUBLIC_BASE_URL`: scheme, host, and port) or the host and port the request itself was addressed to. The check runs before any session lookup.
- **Login.** Passwords are stored with argon2id. Five failed attempts for a username within 15 minutes lock that username until the 15 minutes have passed, and the lock applies even to the correct password. The count is held in server memory. Accepted cost: someone who knows a director's username can lock that director out for 15 minutes.
- **Cookie `Secure` flag.** On by default and controlled by `COOKIE_SECURE`. Local development and the test stack turn it off because they serve plain HTTP.
- **Secrets.** Language-model and ElevenLabs keys are held only by the worker; the Temporal credentials by server and worker. Nothing is sent to browsers.
- **Database.** On AWS the server and worker connect to Aurora DSQL with short-lived IAM tokens issued to their task roles, through a scoped database role. There is no database password to store.
- **Storage.** The S3 bucket is private and encrypted. Browsers reach portraits and audio only through the server's authorised endpoints.
- **Input limits.** The validation bounds in section 5 apply to every upload and text field.

## 8. Observability

- Server and worker write structured logs that carry the room ID and performance ID on every line.
- The Temporal UI is the timeline of a run: each activity, retry, signal, timer, and failure is visible there, and it is where the worker-restart demonstration is shown.
- There is no metrics system in the prototype.

## 9. Limits accepted for the prototype

- No state reads for new connections while the worker is down.
- No history of individual votes after a run; only counts are kept in the final script.
- No single assembled recording; the premiere is eight clips.
- No limit on how many performances run at once (D027).
- A room creation that fails halfway can leave orphaned portraits in S3 until the lifecycle rule removes them.
- An activity may publish the same update twice after a retry; the revision rule makes that harmless.
- The local and deployed databases behave differently under contention. Conflict retries only run for real on Aurora DSQL, so they are first exercised at deployment.

## 10. Next step

Follow [the implementation plan](implementation-plan.md). Its second step, straight after project setup, is the vote and timer ordering spike (item 5 in [the review](review.md)).
