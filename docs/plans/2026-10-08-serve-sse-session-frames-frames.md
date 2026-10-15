# Sidecar — serve-sse-session-frames frame evidence

Companion to `docs/plans/2026-10-08-serve-sse-session-frames.md`. Reference only; not a gate.
Sources: `/tmp/oc-turn2.txt` (opencode 1.18.34, one text turn, filtered to the target session), `/tmp/wopr-turn4.txt` (wopr current). Do not commit either capture.

## A. opencode contract — one turn, in order

| # | frame | notes |
|---|---|---|
| 1 | `session.updated` | session start; `time.updated` moves |
| 2 | `message.updated` user | `time.created` set; **no `summary`** |
| 3 | `message.part.updated` user text | the prompt |
| 4 | `session.updated` | `time.updated` moves |
| 5 | `session.status` busy | |
| 6 | `message.updated` assistant | created; no finish/completed |
| 7 | `session.updated` | adds `summary:{additions,deletions,files}` |
| 8 | `session.diff` | `{sessionID, diff: []}` |
| 9 | `message.updated` user | **with `summary:{diffs:[]}`** (mid) |
| 10 | `session.status` busy | |
| 11 | `session.updated` | title settled ("Greeting") |
| 12 | `message.part.updated` step-start | has `snapshot` |
| 13 | `message.part.updated` text create "" | `time:{start}` |
| 14 | `message.part.delta` "Hi" | |
| 15 | `message.part.updated` text final | `time:{start,end}` |
| 16 | `message.part.updated` step-finish | tokens/cost/snapshot |
| 17 | `message.updated` assistant | cost/tokens + `finish:"stop"` |
| 18 | `message.updated` assistant | `time.completed` |
| 19 | `session.status` busy | |
| 20 | `session.updated` | cost/tokens populated; `time.updated` moves |
| 21 | `session.diff` | `{sessionID, diff: []}` |
| 22 | `session.status` idle | |
| 23 | `session.idle` | |
| 24 | `message.updated` user | **with `summary:{diffs:[]}`** (after idle) |

Census: `session.updated` ×5, `session.diff` ×2, `message.updated` ×6 (user ×3: start no-summary, mid+end with summary; assistant ×3).

## B. wopr current (`/tmp/wopr-turn4.txt`)

`server.connected`; `session.status` busy; `message.updated` user **once, already with `summary:{diffs:[]}`**; user text part; `message.updated` assistant (finish pending); text create; delta; text final; `message.updated` assistant (completed); `session.status` idle; `session.idle`.

Emits **no turn-level** `session.updated`, **no** `session.diff`. (`session.updated` exists only on `PATCH /session/{id}`.)

## C. wopr target after this change

| # | frame | seam |
|---|---|---|
| 1 | `session.updated` | `AgentStartEvent` |
| 2 | `session.status` busy | `AgentStartEvent` |
| 3 | `message.updated` user | user `MessageEndEvent` (unchanged) |
| 4 | user text part | user `MessageEndEvent` |
| 5 | `message.updated` assistant (placeholder) | `MessageStartEvent` |
| 6 | text create / delta / final | stream (unchanged) |
| 7 | `message.updated` assistant (completed) | assistant `MessageEndEvent` |
| 8 | `session.updated` | `AgentSettledEvent` |
| 9 | `session.diff` `{diff:[]}` | `AgentSettledEvent` |
| 10 | `session.status` idle | `AgentSettledEvent` |
| 11 | `session.idle` | `AgentSettledEvent` |

Accepted divergences from opencode: no step-start/step-finish (out of scope); one `session.updated` per boundary rather than five; `session.status busy` precedes the user message at turn start (opencode has it after the user part) — wopr's existing seam, accepted, not reordered; no mid/end user update (two-phase dropped, §4/§5 of the spec).
