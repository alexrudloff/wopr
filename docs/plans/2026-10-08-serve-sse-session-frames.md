# serve-sse-session-frames — emit turn-level `session.updated` / `session.diff`

Backlog: `BACKLOG.md:1597` (`serve-sse-session-frames`, claimed on this worktree).
Worktree: `/home/kc/dev/wopr/.worktrees/wave-serve-sse-session-frames`. Sidecar frame tables: `docs/plans/2026-10-08-serve-sse-session-frames-frames.md`.

**Scope: NARROW.** The verify fan confirmed the item clause fires (see §4): the user-message sequence does not explain the double-render, so the item narrows to the session frames. This spec delivers `session.updated` ×2 (turn start, turn end) + `session.diff` ×1 (turn end) and nothing else.

## Files touched
- `internal/codingagent/serve/serve_sse.go` — mapper: `eventSessionDiff` const + `sseSessionDiff` type, `eventMapper.sessionInfo` closure, `sessionUpdated()` helper (2 uses, fallible), `AgentStartEvent` / `AgentSettledEvent` cases, doc-comment recount.
- `internal/codingagent/serve/serve_test.go` — new `TestSSETurnSessionFramesMatchOpencode`. **No existing-test edits.**
- `docs/plans/2026-10-08-serve-sse-session-frames.md` (this spec) + `-frames.md` (sidecar).

## Gates
`bash -lc 'go build ./...'`; `bash -lc 'go vet ./...'`; `go test -count=1 ./internal/codingagent/serve/...`; `bash -lc 'go tool golangci-lint run ./internal/codingagent/serve/...'`; `bash -lc 'make check'`; the new hermetic test `go test -count=1 -run TestSSETurnSessionFramesMatchOpencode ./internal/codingagent/serve/`.

## Definition of done
All gates green. One turn emits, honestly and in order: `session.updated` at turn start and `session.updated` + `session.diff` `{sessionID,diff:[]}` at turn end (**settle**) — the info is `buildSessionDTO(rec, live)` with `time.updated` moving. The terminal seam fires on **every** settle, including abort (an aborted turn has no assistant run; `diff:[]` is honest there). Persisted transcript shape unchanged (`GET /session/{id}/message` byte-identical). New hermetic test asserts the sequence without reading `/tmp`.

## Explicitly out of scope
The two-phase user `message.updated` (start no-summary / end summary) — dropped; if ever wanted it is a **new backlog item** justified as parity work (§4), not this item. Also: `step-start`/`step-finish` parts (no snapshot hash, no step boundary — do not invent one); any persisted transcript or `loadSessionFile`/`getSessionRecord` retention change; importing `coding` into `serve` (cycle); placeholder/invented frames; the `409 busy` queue item; `session.deleted`/`permission.*`/`message.removed`; live KKCode verification (QA).

## Open questions
1. `time.updated` strict movement rests on the transcript mtime advancing between turn start and turn end. If a future harness pre-writes the file before `AgentStartEvent`, relax the new test's assertion to `>=`.
2. When `getSessionRecord` fails at a turn boundary, skip the frame (honest) rather than emit a stale DTO. Confirm acceptable.

---

## 1. What the mapper emits today vs. target

`eventMapper` (`serve_sse.go:329`) is built by `(*Server).newEventMapper` (375) and driven by `forwardEvents` (`coding/host/host.go:153`). Today `AgentStartEvent` → `[session.status busy]` (405) and `AgentSettledEvent` → `[session.status idle, session.idle]` (407). It emits no **turn** `session.updated` and no `session.diff` anywhere.

Note the scoping: `session.updated` is not absent from the codebase — `patchSession` already publishes it on `PATCH /session/{id}` (`serve_handlers.go:376-380`). What is missing is the turn-lifecycle emission this item targets. The mapper never emits it and never emits `session.diff`.

Target turn (full opencode census and current/target tables in the sidecar):

| seam | emitted events (order) |
|---|---|
| `AgentStartEvent` | `session.updated`, `session.status busy` |
| assistant frames | unchanged (prior waves) |
| `AgentSettledEvent` | `session.updated`, `session.diff`, `session.status idle`, `session.idle` |

**Turn-start order (accepted).** wopr's seam puts `AgentStartEvent` before the user `MessageEndEvent`, so `session.updated`/`session.status busy` precede the user `message.updated`. opencode's literal order is user `message.updated` → `session.updated` → busy. This is wopr's existing seam order and is accepted; **do not force a reorder.** The frame the client needs (a session DTO with a moving `time.updated` before the run state) is present either way.

**Doc-comment note.** The const block (`serve_sse.go:36-50`) defines **13** event-name consts today; adding `eventSessionDiff` makes **14**. The comment at `serve_sse.go:27` claims "Twelve of the seventeen names the client switches on have a live trigger" and lists five with none. **The captured client event list (`docs/plans/2026-10-03-serve-opencode-ios.md:153-158`) names neither `session.diff` nor `session.idle`**, so adding `session.diff` does not move the *client-name* count: the "twelve of the seventeen / other five have none" sentence stays correct and unchanged. The comment is extended instead to name `session.diff` as an opencode-parity emission outside that list, as `session.idle` already is. Do not claim "thirteen of the seventeen" — that would assert `session.diff` is a client name the capture does not list.

## 2. Seam: the mapper needs the session DTO

`eventMapper` has no `*Server`. Give it a closure built in `newEventMapper` (a `*Server` method), so the stream and `GET /session` cannot disagree:

```go
// sessionInfo returns the session's current DTO, or false when the record
// cannot be read. Built by newEventMapper from the server's own read surface.
sessionInfo func() (sessionDTO, bool)
```

In `newEventMapper` (after the existing `Get`), set:

```go
mapper.sessionInfo = func() (sessionDTO, bool) {
    live, _ := s.host.Get(sessionID)
    rec, ok := getSessionRecord(s.sessionDir, sessionID, live)
    if !ok {
        return sessionDTO{}, false
    }
    return s.buildSessionDTO(rec, live), true
}
```

`buildSessionDTO` (`serve_handlers.go:987`) and `getSessionRecord` (`serve_read.go:322`) are read-only and in-package; no new import, no `coding` import. One fallible helper, two callers:

```go
func (m *eventMapper) sessionUpdated() (codinghost.Event, bool) {
    if m.sessionInfo == nil { return codinghost.Event{}, false }
    info, ok := m.sessionInfo()
    if !ok { return codinghost.Event{}, false }
    return m.event(eventSessionUpdated, sseSessionUpdated{SessionID: m.sessionID, Info: info}), true
}
```

`session.diff` is built inline at its single call site (no helper):

```go
case agent.AgentStartEvent:
    events := m.one(m.status("busy"))
    if ev, ok := m.sessionUpdated(); ok { events = append([]codinghost.Event{ev}, events...) }
    return events, nil
case agent.AgentSettledEvent:
    var events []codinghost.Event
    if ev, ok := m.sessionUpdated(); ok { events = append(events, ev) }
    events = append(events,
        m.event(eventSessionDiff, sseSessionDiff{SessionID: m.sessionID, Diff: []json.RawMessage{}}),
        m.status("idle"), m.idle())
    return events, nil
```

**Cost note:** each boundary does one bounded `sessionRecordAt` read (stat + ≤ `metaProbeBytes`=32 KiB). Two per turn, on the per-session forwarder goroutine. Acceptable; no cache (YAGNI).

Add `eventSessionDiff = "session.diff"` beside `eventSessionUpdated` (`serve_sse.go:42`) and:

```go
// sseSessionDiff answers session.diff. wopr records no diff, so Diff is the
// empty array the client requires, never null — mirrors messageSummary.Diffs.
type sseSessionDiff struct {
    SessionID string            `json:"sessionID"`
    Diff      []json.RawMessage `json:"diff"`
}
```

## 3. Tests

**New hermetic test** in `serve_test.go` (not `serve_kkcode_replay_test.go`: the latter replays an HTTP *request* sequence; this needs the SSE helpers and sits beside `TestSSETurnLifecycleParity`). Name: `TestSSETurnSessionFramesMatchOpencode`.

**Hermetic by design.** The backlog Gate says "comparing wopr's turn against `/tmp/oc-turn2.txt`". This test is deliberately hermetic: it embeds the expected sequence as an in-test fixture and **never reads `/tmp`**. The divergence is intentional (repo rule: tests are hermetic, no developer filesystem); the fixture is the transcribed census.

Setup reuses `promptServer`, `createPromptSession`, `openSSE`, `postJSON`, `readSSEUntil`, `summarize`. **One `readSSEUntil` call only** — a second call creates a second `bufio.Reader` over the same body and loses buffered frames. Stop at `session.idle` (the session frames are emitted before it).

Assertions, on frames filtered to drop `server.*` and `message.part.*`:
1. The first contract frame is `session.updated`; it precedes the first `session.status busy`.
2. Exactly two `session.updated`; each `info.id == sessionID`, `info.time.updated > 0`; the second's `time.updated >` the first's (moving).
3. Exactly one `session.diff`; properties `{sessionID, diff}` with `diff` an **empty array** (not null); it precedes `session.status idle`.
4. `session.status idle` precedes `session.idle`.
5. The normalized contract subsequence is `session.updated, session.status, session.updated, session.diff, session.status, session.idle`.

Non-vacuity (state in a comment): dropping the `session.updated` emission fails (1)/(2); emitting `diff:null` fails (3); emitting `session.diff` after idle fails (3).

No existing test changes: they stop at `session.idle`, and the new frames appear before idle but are ignored by their name/id filters. Verified against `TestSSETurnLifecycleParity`, `TestSSETurnFrameParity`, `TestMessageEndDoesNotRepublishStreamedPart`, `TestSSEMessageUpdatedShapeMatchesOpencode`, `TestMessageInfoRoleShapes`, and the byte-bound drop test.

## 4. CAUSALITY ASSESSMENT — does the user-message sequence explain the double-render?

**Verdict: UNESTABLISHED. The user-message sequence does not plausibly explain the double-render, and neither alternative is supported by the captures.**

Evidence, from frames alone:
- `wopr-turn4.txt` is current wopr: it emits exactly **one** user `message.updated` (already with `summary:{diffs:[]}`), yet the symptom is that each sent message renders twice. One update cannot produce two renders by update multiplicity.
- `oc-turn2.txt` emits **three** user `message.updated` for the same id and renders once. A client that keys by id tolerates repeated updates; multiplicity is not the duplicate's cause.

No alternative cause is established here either — the two candidates (client optimistic render vs. echoed update under a mismatched key; assistant-side part republish) are not proven by these captures, and the republish is already removed by `serve-sse-end-burst-duplicate-part`.

**Consequence.** Per the item clause ("if the double is not explained by the user-message sequence … the item narrows to the session frames"), this item ships the session frames only. The double-render cause remains **UNESTABLISHED** and is **not** invented here. The two-phase user `message.updated` parity, if ever wanted, is a **new backlog item** explicitly justified as parity work — not this item.

## 5. Deliverable

| Subject | Status |
|---|---|
| `session.updated` ×2 (turn start, turn end) | **in scope** |
| `session.diff` ×1 (turn end) | **in scope** |
| two-phase user `message.updated` | **dropped** (new backlog item if wanted) |
| `step-start`/`step-finish` | out of scope |

## 6. Risks / notes
- **Abort.** `AgentSettledEvent` fires on abort too (`coding/session.go` `sendContent` emits it after the run regardless of error), so the terminal seam — `session.updated`, `session.diff`, `session.status idle`, `session.idle` — fires on every settle, abort included. `diff:[]` is honest for an aborted turn (wopr recorded no diff).
- `sessionRecordAt` at `AgentSettledEvent` may see a partially written file; it reads complete records only and returns false on a bad header. If it returns false the frame is skipped (honest) — a systematic failure is caught by the new test.
- `time.updated` movement relies on mtime advancing (open question 1).
- `eventMapper` is per-session and single-goroutine, so the new field needs no lock (existing invariant, `serve_sse.go:326-328`).
- Do not re-do the part-time / delta-create-first / assistant completion work — prior waves own it.
