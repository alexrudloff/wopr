# serve-sse-message-lifecycle-parity — one stable id per assistant turn

Backlog: `BACKLOG.md:1358` (`serve-sse-message-lifecycle-parity`, CLAIMED on this worktree).
Goal: make `GET /global/event` stream one user turn the way opencode 1.18.34 does, so KKCode renders the reply once and settles to idle.

## Files touched
`agent/agent.go` (add `EntryID` to `AssistantMessage`); `coding/session.go` (mint/tag it in `OnEvent`); `internal/codingagent/session.go` (`AppendMessage` honors it); `coding/host/host.go` (resolve the user entry id); `internal/codingagent/serve/serve_sse.go` (mapper); `internal/codingagent/serve/serve_test.go` (update 3, add 1); this spec.

## Gates
`go build ./...`; `go vet ./...`; `go test ./agent/... ./coding/... ./internal/codingagent/...`; `go tool golangci-lint run ./agent/... ./coding/... ./internal/codingagent/...`; `bash -lc 'make check'`.

## Definition of done
New hermetic test over `GET /global/event` asserts: a user `message.updated{role:"user"}` plus its text part; the assistant text part id is **identical** across `message.part.delta` and `message.part.updated`; an assistant `message.updated`; a terminal `session.idle`.
For a completed assistant message, streamed part ids are literally equal to the `GET /session/{id}/message` transcript ids (`<entryID>-text-0`, `<entryID>-tool-<callID>`); persistence uses the streamed id by construction. All gates green.

## Explicitly out of scope
Transcript id scheme / `entriesToMessages`; `step-start`/`step-finish` parts; making `session.idle` settle the client by itself (downstream of the id fix — no new frame is invented); user image/file parts in the stream; V1 (`grapeot`) client compatibility.

## Open questions
1. Add `step-start`/`step-finish` as a later parity follow-up? They are stream-only and would break literal stream==transcript id equality, so they need their own invariant.
2. Mirror user `ai.ImageContent` as a `type:"file"` part later?

---

## Evidence (read-only captures)

- `/tmp/oc-turn.txt` (opencode 1.18.34): assistant part ids (`prt_…`) are stable across delta and update; user message is announced with `message.updated{role:"user"}` plus a full-text `message.part.updated`; assistant `message.updated` appears before the parts and again at the end.
- `/tmp/wopr-sse2.txt`: `message.part.delta` under `msg_<session>-<n>-text-0`, then `message.updated` id `<entryid>` and `message.part.updated` id `<entryid>-text-0` — the same text under two ids, which is the double render.

## Chosen option: D — mint at message start in `coding/session.go`'s `OnEvent`, persist under that id

`Agent.OnEvent` is documented (`agent/agent.go:555-560`) as running synchronously for every event *before* the event is persisted or delivered, and as allowed to mutate the pointed-to message in place such that "agent state, persistence and EventCh observe" the change. `Agent.emit` calls `OnEvent(ev)` and then, for `MessageEndEvent`, `persistMessage(ev.Message)` on the same goroutine (`agent/agent.go:1156-1172`). So a mutation made in `OnEvent` at message start reaches both persistence and the SSE stream, and is ordered before the persist for the same message. This is the sanctioned seam.

1. **Carrier.** Add an `EntryID string` field, JSON tag `-`, to `agent.AssistantMessage` (`agent/agent.go:40-64`). It is copied by `cloneAssistantMessage` (`agent/agent.go:1341`, `cp := *msg`) and `AgentMessage.Clone` (`agent/message_json.go:13`), and is never serialized: `MarshalJSON` (func at `agent/message_json.go:53`; assistant case `:77-109`) and `UnmarshalJSON` (func at `agent/message_json.go:140`; assistant case `:163-175`) build explicit shapes, so the durable entry id stays on the session entry base, not the message body.
2. **Mint + tag in `coding/session.go:420` `OnEvent`.** Add a `pendingAssistantID string` field beside `assistantEnds` (`coding/session.go:160-172`). The handler at `coding/session.go:420-428` today holds only a `TurnEndEvent` assertion and `observeRouteTiming`, so the switch is added from scratch:
   - `MessageStartEvent` with `Assistant != nil`: `id, _ := icodingagent.GenerateEntryID()`; set `Assistant.EntryID = id`; store `pendingAssistantID = id`.
   - `MessageEndEvent` with `Assistant != nil`: set `Assistant.EntryID = pendingAssistantID` (persistence reads it next), then clear.
   No `MessageUpdateEvent` case: `agent.emitAssistantUpdate` clones a fresh `AssistantMessage` per update (`agent/agent.go:1280-1285`), so an id set there reaches neither persistence nor the mapper — the mapper reads the id from `MessageStartEvent` instead (see Mapper changes).
   No lock is needed: `OnEvent` and `OnMessagePersist` are both invoked from `emit` on the agent loop goroutine.
3. **Persist under it.** `internal/codingagent/session.go:597` `AppendMessage` already builds `SessionEntryBase{ID: generateEntryID()}` via `appendOnLeaf` (`internal/codingagent/session.go:621-635`; fresh id literal at `:624-626`). Override `base.ID` when `msg.Assistant != nil && msg.Assistant.EntryID != ""`. Since `coding/session.go:561` calls `in.AppendMessage(msg)` as the single persistence site, the persisted entry id is the streamed id. No change to `coding/session.go` `persistMessage`.
4. **Mapper** (`serve_sse.go`) uses that one id for the whole assistant message (details below).
5. **Host** resolves the entry id for user `MessageEndEvent` too (`coding/host/host.go:165-168`), so the user message can be streamed under its transcript id.

### Why not A (agent mints and carries)

Option A threads a generator and per-run id state through `consumeStream` (`agent/agent.go:1205-1268`; `endAssistantMessage` `:1270-1278`), the failure path (`agent/agent.go:1144-1147`), `requestError` (`agent/agent_loop.go:435-446`) and `appendMessage` (`agent/agent_loop.go:321-327`), and needs a second entry-id generator in package `agent` (it cannot import `internal/codingagent`, which imports `agent`). Option D mints at one choke point that already sees every message lifecycle event, and keeps entry-id policy where the transcript lives. Rejected as more surface for the same result.

### Why not B (reservation in serve/host forwarder)

The host forwarder reads a buffered `EventCh` (`coding/session.go:319`, `coding/host/host.go:163`), so a reservation made when it reads `MessageStart` is not guaranteed to precede the agent's synchronous persist at `MessageEnd`; the id would be decided twice. Option D's `OnEvent` runs on the agent goroutine before the persist, so the ordering is structural, not lucky. Rejected.

### Why not C (rename the final burst to the provisional id)

The transcript id scheme is `<entryID>-…` from `entriesToMessages` (`serve_handlers.go:1067-1270`) and may not change. A provisional id can never equal the persisted random entry id without pre-minting, so C does not satisfy the constraints. Rejected.

---

## Target frame sequence for one text turn

| # | frame | id |
|---|---|---|
| 1 | `session.status{busy}` | — |
| 2 | `message.updated{role:"user"}` | `<userEntryID>` |
| 3 | `message.part.updated{type:"text"}` | `<userEntryID>-text-0` |
| 4 | `message.updated{role:"assistant"}` | `<assistantEntryID>` |
| 5 | `message.part.delta{field:"text"}` | `<assistantEntryID>-text-0` |
| 6 | `message.part.updated{type:"text"}` (TextEnd and the end burst) | `<assistantEntryID>-text-0` |
| 7 | `message.updated{role:"assistant"}` (end re-announce) | `<assistantEntryID>` |
| 8 | `session.status{idle}`, `session.idle` | — |

Open order note: opencode's captured multi-turn stream interleaves sessions, so the user frame is not strictly first; for one turn the user `MessageEnd` is emitted by the runLoop replay (`agent/agent_loop.go:90-95`) before `consumeStream`, so user-before-assistant is the natural order. The test asserts presence and id equality, not a strict total order beyond user-before-assistant and idle-last.

## Mapper changes (`internal/codingagent/serve/serve_sse.go`)

- Delete the provisional counter: `eventMapper.next`, `eventMapper.current` (`serve_sse.go:338-343`) and `provisionalID` (`serve_sse.go:546-549`). Add `assistantID string` — the last assistant entry id, used by tool frames.
- `translate` (`serve_sse.go:371-413`):
  - `MessageStartEvent` for an assistant message: record `m.assistantID = ev.Message.Assistant.EntryID` and emit one `message.updated{role:"assistant"}` via `messageInfo(...)`. Other roles produce nothing.
  - `MessageUpdateEvent`, `MessageEndEvent`, tool events: unchanged dispatch.
- `messageUpdate` (`serve_sse.go:419-450`): `messageID := m.assistantID` directly — a `MessageUpdateEvent` carries a fresh clone with no `EntryID` (`agent/agent.go:1280-1285`), so there is no per-update id to read. The mapper records it once at `MessageStartEvent` (above), the same id persistence uses. Every delta/part uses it. All `partID(messageID, …)` calls are unchanged in form, so `<id>-text-0` / `<id>-tool-<callID>` match `entriesToMessages`.
- `toolStart` / `toolSnapshot` (`serve_sse.go:500-518`): replace `m.provisionalID()` with `m.assistantID`. Tool parts thereby live on the assistant entry id the transcript uses, removing the tool double-render too.
- `messageEnd` (`serve_sse.go:461-495`): branch on role.
  - user: emit `message.updated{role:"user"}` (`messageInfo(..., nil, …)`) plus one `textPart(entryID, sessionID, i, text)` per `ai.TextContent` index, using the host-resolved `entryID`. Skip if `entryID == ""`.
  - assistant: `id := ev.Message.Assistant.EntryID; if id == "" { id = entryID }`; emit `message.updated{role:"assistant"}` and the content parts under `id` exactly as today (`serve_sse.go:466-488`), plus the existing error frame. The end burst stays because the parts loop (`serve_sse.go:475-488`) is the only publish a message that errored before any `TextEndEvent` gets — not because the transcript-equality test reads it (that test stops on `session.idle`, after persistence).
- Rewrite the `messageEnd` doc comment (`serve_sse.go:452-460`): it states `message.updated` is emitted for assistant messages only and a user or toolResult end produces nothing, which this change falsifies — a user end now emits `message.updated{role:"user"}` plus its text parts. Production comments state current behavior (AGENTS.md).

The net effect: the same part id is used for `message.part.delta`, the intermediate `message.part.updated`, and the end burst, and it equals the transcript id.

## Host change (`coding/host/host.go:165-168`)

Broaden the resolver condition from `end.Message.Assistant != nil` to `end.Message.Assistant != nil || end.Message.User != nil`. `MessageEntryID` (`coding/session_recovery.go:159`) already indexes user messages by pointer via `messageIdentity` (`coding/session_recovery.go:17-29`) and `rememberMessageEntry` (`:32`), and the persisted user pointer is the same pointer the `MessageEndEvent` carries, so user resolution is O(1) and exact.

## Persistence change (`internal/codingagent/session.go:597-618`)

Inside `AppendMessage`'s `appendOnLeaf` build callback, when `msg.Assistant != nil && msg.Assistant.EntryID != ""`, set `base.ID = msg.Assistant.EntryID` before constructing `MessageEntry`. `AppendEntry` (`internal/codingagent/session.go:484`) uses `base.ID` verbatim, so the entry, `byID`, `LeafID`, and every transcript read agree on the streamed id. `json:"-"` keeps the carrier out of the persisted body.

## step-start / step-finish: not emitted

They are stream-only parts with no transcript counterpart. Emitting them would make "for a completed message, the streamed part ids and the transcript part ids are literally equal" false, and they are not the settling mechanism (both servers already emit `session.status`/`session.idle`). Deferred to follow-up open question 1.

---

## Tests

### New: `TestSSETurnLifecycleParity` in `internal/codingagent/serve/serve_test.go`

Hermetic, using `promptServer(t)` / `createPromptSession` / `openSSE` / `readSSEUntil` (all already set `WOPR_TEST_FAUX=1` and temp `WOPR_HOME`). Prompt `reply with exactly: parity-ok`. Read until a `session.idle` frame. Then assert on the collected frames:

1. Exactly one `message.updated` with `info.role == "user"`; its `info.id` is non-empty.
2. A `message.part.updated` whose `part.messageID` equals that user id, `part.id == userID+"-text-0"`, and `part.text == "reply with exactly: parity-ok"` (the user part carries the prompt text verbatim, as `entriesToMessages` does).
3. Exactly one assistant `message.updated` (or two, if the end re-announce is kept — assert at least one and that all assistant `message.updated` share one id, `assistantID`).
4. Collect every `message.part.delta` whose `messageID == assistantID`; there is at least one; every delta's `partID` is equal.
5. A `message.part.updated` with `part.id == <that delta partID>`, `type=="text"`, and `text=="parity-ok"` — the stable-id assertion.
6. The final frame set contains `session.idle`.
7. Cross-check id equality with the transcript: `readTranscript(t, baseURL, id)` contains a message `info.id == assistantID` whose `parts[0].id == delta partID` (serve-4 invariant, in-test).

Add small local helpers (parse `message.updated` role+id, collect delta part ids) rather than changing shared helpers beyond what the updates below require. Add one shared helper that extracts the assistant `message.updated` id from frames: both this test (step 3) and `TestToolCallDeltaReachesTheStream` need it, and reusing it keeps the two id sources pinned to one parse.

### Existing tests that must change, and why

- `TestStreamPartIDsMatchTranscriptPartIDs` (`serve_test.go:842`). Two defects in its stop condition. First, `burstFrom` (`serve_test.go:1234`) resets on *every* `message.updated` with no role filter, so with the user frame now first it would stop on the user part and silently test the user message. Second, `burstFrom` keys parts to that `message.updated`, so a role-filtered `len(parts) > 0` stop fires on the first assistant `message.part.updated` — the `TextEndEvent` frame (`serve_sse.go:442-443`) emitted *before* `MessageEnd` persistence (`agent/agent.go:1276-1277` emits `MessageEndEvent`; `agent/agent.go:1166-1167` persists it). The subsequent `readTranscript` (`serve_test.go:878-888`) can then race and read a transcript with no assistant message. **Required fix: the stop condition is the `session.idle` frame** (`serve_sse.go:383-384`), which `coding/session_run_state.go:135-141` emits only after the run and its `message_end` persist have settled. Then assert. Make `burstFrom` consider only `info.role == "assistant"` for the id and parts extraction; the assertion (streamed ids == transcript ids) is unchanged and now also covers the streamed delta id if the test asserts against the delta.
- `TestToolCallDeltaReachesTheStream` (`serve_test.go:900`, prefix assertion at `:927`). It asserts the tool delta part id starts with `msg_<session>-`. Now it starts with `<assistantEntryID>-`. Fix: capture the assistant `message.updated` id from the frames using the shared helper above and assert that prefix; the delta-vs-tool-part id equality assertion (`:945`) is unchanged.
- `TestToolCallDeltaAgreesWithThePartItCompletes` (`serve_test.go:974`, wants at `:999` and `:1015`). It builds an `eventMapper{sessionID:"sess-guard"}` directly and expects `msg_sess-guard-0-tool-…`. Fix: set `assistantID: "sess-guard"` on the mapper (the message clone carries no id now) and expect `sess-guard-tool-…` / `sess-guard-tool-call-9`.
- `TestSSEMessageUpdatedShapeMatchesOpencode` (`serve_test.go:2748`) — no change required: it stops at the first `message.updated` (now the user) and `assertInfoShape` (`:2507`) accepts a user `info` (all keys present, provider/model fall back to the server spec). Flagged here so a verifier does not mistake it for an edit.

No other test asserts provisional-id shapes (grep for `msg_`, `provisional`, `burstFrom` in `serve_test.go` is confined to the three above).

## Risks / edge cases

- **Start/end pairing.** `pendingAssistantID` is overwritten on each assistant `MessageStartEvent` and cleared on each assistant `MessageEndEvent`; start/end are strictly paired and synchronous on the agent goroutine, so a stale id cannot leak to the next message.
- **Replay.** `runLoop` replays only `a.messages[runStart:]` (`agent/agent_loop.go:80-97`), which for `Send`/`SendMessages` is the new prompt (a user message); no assistant replay mints a spurious id.
- **Subagents.** `internal/codingagent/subagent/runner.go:201` sets its own `OnEvent` and does not persist via this path; an unset `EntryID` is inert (`json:"-"`, no append override).
- **Direct mapper unit tests.** An `eventMapper` built without an event field must set `assistantID` (the mapper no longer reads the message `EntryID`); otherwise ids are empty. Covered by the test updates.
- **Image-only user prompts** currently produce `message.updated{user}` with no parts; accepted and listed out of scope.

## Rollback

Single logical change across six files; reverting the commit restores the provisional scheme. No data migration: `EntryID` is `json:"-"`, no existing session file changes shape, and no transcript id format changes.
