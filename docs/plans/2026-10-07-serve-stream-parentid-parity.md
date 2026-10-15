# Plan: serve-stream-parentid-parity — streamed `message.updated` reports the initiating user message as `parentID`

## Files touched
- `internal/codingagent/serve/serve_sse.go` (map the turn's user message id; use it as the assistant parent)
- `internal/codingagent/serve/serve_test.go` (the named gate assertion)
- REVERT (already modified in the worktree, must go back to HEAD):
  `agent/agent.go`, `coding/session.go`

## Gates to run
```bash
git checkout -- agent/agent.go coding/session.go   # revert the dead-end seam
go build -buildvcs=false ./...
go vet ./...
go test -race -count=1 -run TestSSETurnLifecycleParity ./internal/codingagent/serve
go test -race -count=1 ./internal/codingagent/serve
go test -count=1 ./agent/... ./coding/...
go tool golangci-lint run --allow-parallel-runners ./agent/... ./coding/... ./internal/codingagent/serve/...
go fix -diff ./...          # must print nothing
make check                  # pre-commit gate
```
The named assertion is in `TestSSETurnLifecycleParity`
(`internal/codingagent/serve/serve_test.go:894`): every assistant
`message.updated` `info.parentID` equals the user `message.updated` id.

## Definition of done
- The assistant start announce (`messageStart`) and the assistant end burst
  (`messageEnd`) both emit `info.parentID` equal to the entry id of the user
  message that initiated the turn — the id already emitted as the user
  `message.updated`, which matches opencode 1.18.34 (`/tmp/oc-turn.txt:155`).
- `TestSSETurnLifecycleParity` fails before the change (parentID `""`) and passes after.
- All gates above green; `go fix -diff ./...` empty.
- No new exported symbol, interface, or dependency; one unexported mapper field.

## Explicitly out of scope
- The transcript read API (`entriesToMessages`, `assistantMessageDTO`,
  `GET /session/{id}/message`) — it still reports the raw persisted parent, the
  interposed `harness-online-context-state-v1` custom entry. Reconciling it is a
  follow-up (design B below), not this item.
- The user-message branch of `messageEnd`: it still emits `parentID: ""`.
- Any change to `agent/agent.go`, `coding/session.go`, or persisted session tree.
- Any doc beyond this spec, any commit, any push.

## Open questions
None.

## Why design (A), the mapper-local turn parent

**Factual blocker (verified).** With `OnlineContextCompact` on by default
(`internal/codingagent/efficiency/config.go:79`), every provider request appends a
`custom` entry of type `harness-online-context-state-v1`
(`internal/codingagent/efficiency/onlinecompact.go:20,98` →
`coding/session_efficiency.go:244,428` → `AppendCustomEntry` →
`appendOnLeaf`, `internal/codingagent/session.go:628,634`). `prepareRequest`
runs it (`coding/session_transcript.go:138`) after the user message is persisted
and before the assistant stream starts (`agent/agent_loop.go:88-94` replay the
user `MessageEnd`, then `loop()` → `prepareRequest` → assistant start). The
custom entry therefore sits between them, and the assistant entry's
`Base.ParentID` is the custom entry id. So the spec's leaf seam produces the
custom entry, the gate wants the user id, and the same contradiction applies to
`entriesToMessages` (`serve_handlers.go:1119-1121`), which reads the raw
`e.Base.ParentID`.

**Resolution.** The item's real requirement — "a client that threads turns by
`parentID` … attach the streamed reply to its user message" — is opencode's
semantic: an assistant `message.updated` carries the id of the user message that
initiated the turn, and every assistant message in a multi-step turn shares it
(`/tmp/oc-turn.txt:155`, confirmed: assistant `msg_…f80001…` has
`parentID:"msg_…f59001…"`, and the later user `message.updated` is
`msg_…f59001…`). That is the target; "stream equals the raw persisted parent" was
a means, and it is the side that must yield. Design (A) implements the semantic
directly, at the only place the turn boundary is observable: the per-session
`eventMapper`, which sees the user `MessageEndEvent` (with its host-resolved
entry id) before any assistant `MessageStartEvent` on the same single goroutine.

**Minimality.** (A) needs one unexported `string` field and four changed lines
(field, one assignment, two argument swaps), all inside the item's named file,
`serve_sse.go`. It needs no session/agent
change at all, so the applied `agent/agent.go` and `coding/session.go` edits are
dead weight and revert.

**Rejected (B) — metadata-skipping parent in both projections.** It would make
the stream and `entriesToMessages` agree on the user id, but it changes shared
read-API behavior beyond this item, and `entriesToMessages` receives a sub-slice
(`assistantMessageDTO` passes `entries[start:]`, `serve_prompt.go:296-313`), so a
slice-order "nearest preceding message" tracker returns `""`; it needs a full
id→entry map plus a ParentID-chain resolver threaded into `entriesToMessages`.
Larger, and it edits files the item does not name. Follow-up, not this slice.

**Rejected (C) — change the persisted parent at write time.** It mutates the
stored session tree and branch navigation. Out of scope.

**Rejected — host seam.** `codinghost.Translator` is
`func(ev agent.AgentEvent, entryID string)` and the host resolves `entryID` only
for `MessageEndEvent` (`coding/host/host.go:165-168`); a `MessageStartEvent` has
no entry to resolve, so routing the parent through the host would widen the
translator signature and still miss the start frame. The mapper is strictly better.

## Exact edits

### 1. `internal/codingagent/serve/serve_sse.go` — one field on `eventMapper`
After `assistantID` (`serve_sse.go:343`), add:

```go
	// turnUserID is the entry id of the user message that initiated the current
	// turn, remembered when that message's MessageEndEvent is mapped. opencode
	// parent-chains every assistant message of a turn to that user message, so
	// each assistant message.updated reports it as parentID. It is deliberately
	// NOT the raw persisted parent: the per-request
	// harness-online-context-state-v1 custom entry is interposed between the two,
	// so entriesToMessages (the transcript read API) reports a different value.
	// It is per-session, not per-run: a run that emits no new user message_end
	// (no Continue endpoint exists today) leaves the previous turn's value, and
	// an assistant frame then reports whatever turn last set it.
	turnUserID string
```

### 2. `serve_sse.go` — remember it in `messageEnd`'s user branch
In the user branch (`messageEnd`, ~`serve_sse.go:475-479`), after the
`entryID == ""` guard and before the `events := …` construction, add:

```go
		m.turnUserID = entryID
```

### 3. `serve_sse.go` — use it for both assistant frames
- `messageStart` (`serve_sse.go:424`): replace the argument
  `ev.Message.Assistant.ParentID` with `m.turnUserID`.
- assistant branch of `messageEnd` (`serve_sse.go:504`): replace the argument
  `ev.Message.Assistant.ParentID` with `m.turnUserID`.

`messageUpdate` emits no `message.updated` and is untouched. The user branch's
own `messageInfo(..., "user", "", ...)` is untouched.

### 4. `internal/codingagent/serve/serve_test.go` — the gate assertion
Add after step 3 (the assistant-id loop, where `userID` and `assistantID` are in
scope):

```go
	// 3b. Every assistant message.updated names the user message as its parent,
	// so a client can thread the reply onto the prompt it answered.
	for _, f := range frames {
		if f.Name != eventMessageUpdated {
			continue
		}
		var payload sseMessageUpdated
		if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
			continue
		}
		if payload.Info.Role == "assistant" && payload.Info.ParentID != userID {
			t.Errorf("assistant message.updated parentID: expected %q, got %q",
				userID, payload.Info.ParentID)
		}
	}
```

Do **not** re-add the previously "recommended" step-7 transcript-walk assertion
(streamed parent == transcript parent). That assertion encoded the contradictory
DoD and must not be reintroduced.

### 5. Revert the dead-end seam already applied in the worktree
- `agent/agent.go` — revert (remove the `ParentID string` field on
  `AssistantMessage`). `git checkout -- agent/agent.go`.
- `coding/session.go` — revert (`tagAssistantEntryID` back to assigning only
  `EntryID`). `git checkout -- coding/session.go`.
- `internal/codingagent/serve/serve_sse.go` — **keep modified**, but the two
  argument expressions become `m.turnUserID` per step 3; add the field and the
  user-branch assignment per steps 1–2. Net: the file is changed, the other two
  are clean against HEAD.

## Residual divergence accepted (stated honestly)

- **The transcript read API diverges.** `entriesToMessages`
  (`serve_handlers.go:1119-1121`) and `assistantMessageDTO`
  (`serve_prompt.go:296-313`) still return the raw persisted parent — the
  `harness-online-context-state-v1` custom entry — for an assistant message.
  Because custom entries are skipped as messages (`serve_handlers.go:1076`), a
  client threading from `GET /session/{id}/message` gets a `parentID` with no
  corresponding message. That is a real, pre-existing read-path defect; the item
  scopes to the stream, and this spec does not fix it. Log it as a follow-up
  (design B).
- **The user message's own `parentID`** stays `""` in the stream. Out of scope.
- **`Continue`/no-new-user-message runs** emit no user `MessageEndEvent`. The
  mapper is per-session, so `turnUserID` retains the previous turn's user id
  rather than becoming `""`; an assistant frame then reports that earlier id. No
  `Continue` endpoint exists in serve today, so this is unreachable; the ceiling
  is stated in the field comment rather than widened into scope.

## Risks and edge cases

- **Ordering is the load-bearing fact**: `agent_loop.go:88-94` emits the user
  `MessageStart`+`MessageEnd` before `loop()` starts, and the mapper is one
  goroutine per session (`serve_sse.go:327-344`), so `turnUserID` is set before
  any assistant `messageStart`. No lock is needed, same as `assistantID`.
- **Multi-step turns** share the initiating user id for every assistant message,
  matching opencode. A steering message between assistant messages updates
  `turnUserID`, so the next assistant reply parents on that user message.
- **Struct literals** that build `eventMapper` with field names
  (`serve_test.go:1172`) are unaffected by the new field.
- **Key presence** is unchanged: `messageInfo` always sets `parentID`
  (`serve_handlers.go:1024`), so the schema check at `serve_test.go:2709` still
  sees the key.
