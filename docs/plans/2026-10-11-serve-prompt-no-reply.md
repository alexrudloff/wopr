# serve-prompt-no-reply — persist a user message without an assistant turn

Backlog: `serve-prompt-no-reply` (`BACKLOG.md:1875`). Authority: opencode `v1.18.34`
(`/home/kc/dev/opencode-src` @ `aec0b9a`) is the specification for `wopr serve`; where they
disagree, opencode wins unless a divergence is recorded (`AGENTS.md`). Worktree:
`/home/kc/dev/wopr/.worktrees/wave-serve-prompt-no-reply`.

## Files touched

- `internal/codingagent/serve/serve_prompt.go` — `NoReply bool` on `promptBody` (`:24`); the
  noReply branch at the head of both handlers' `before` hooks (`:98` `promptAsync`, `:154`
  `sessionMessage`), before any model swap or `SetNextUserSystem` staging; new `errNoReply`
  sentinel beside `errRefused` (`:210`); `sessionMessage`'s `outcome` (`:138-145`) gains
  `noReplyEntryID`, and its `select` (`:178-193`) answers 200 with the user `{info, parts}`
  through the existing role-agnostic `assistantMessageDTO` (`:383-397`) — no new DTO. Both
  handler doc comments (`:47`, `:113`) name the new behaviour.
- `coding/session.go` — new `AppendUserMessage(content []ai.UserContentBlock, entryID, system
  string) (string, error)`, placed after `sendContent` (`:682-729`). The only production code
  change outside serve.
- `internal/codingagent/serve/serve_test.go` — the gate test
  `TestPromptNoReplyPersistsWithoutAnAssistantTurn`.
- `docs/site/docs/server.md` — one `noReply` clause on each prompt row (`:56`, `:57`).
- `docs/plans/2026-10-09-serve-v1-conformance-and-parity-tables.md` — flip the `noReply` row
  (`:53`) to done with the gate name.
- `BACKLOG.md` — mark `serve-prompt-no-reply` DONE (`:1875`); record the model divergence, the
  SSE divergence, and the SSE follow-up item.

## Gates to run

```
go build ./...
go vet ./coding/... ./internal/codingagent/serve/...
go test ./coding/... ./internal/codingagent/serve/...
go test ./internal/codingagent/serve/ -run TestPromptNoReplyPersistsWithoutAnAssistantTurn -v
go tool golangci-lint run ./coding/... ./internal/codingagent/serve/...
make check   # pre-commit; the tmux integration tier may not run here
```

Write the gate first and watch it fail (with `noReply` dropped, `promptBody` has no such field, so it
cannot compile against the new assertions — the red run is "unknown field"). A gate that cannot be
made red is not a gate; see the mutation list at the end.

## Definition of done

Both prompt endpoints accept `noReply`; a `noReply:true` prompt appends exactly one user entry to the
transcript and zero assistant entries (observed through `GET /session/{id}/message`), the faux
provider is never called, and the session model is unchanged. `POST /session/{id}/message` answers
200 with the user `{info, parts}`; `POST /session/{id}/prompt_async` answers 204 and emits no
SSE frame. `go build`, `go vet`, the touched packages' tests, `go tool golangci-lint run`, and
the gate test pass.

## Out of scope

- The SSE frames opencode emits for a noReply user message: `message.updated` plus one
  `message.part.updated` per part, published by `sessions.updateMessage` / `updatePart`
  (`packages/opencode/src/session/session.ts:631`, `:635-636`, reached from
  `packages/opencode/src/session/prompt.ts:1046-1047`). wopr's user frames are run-driven: the
  mapper reaches a user message only through a `MessageEndEvent`
  (`internal/codingagent/serve/serve_sse.go:436`, `:574`), which a noReply append never
  produces. An out-of-run frame needs its own race-free path. Follow-up item
  `serve-prompt-no-reply-sse`, not this slice (see D2).
- A `session.error` frame for a failed noReply append (opencode's `prompt_async` handler
  publishes one for any failed prompt,
  `packages/opencode/src/server/routes/instance/httpapi/handlers/session.ts:317-322`). Same
  out-of-run publish path as D2; see Q3.
- `parts: agent|subtask` (`serve-prompt-parts-agent-subtask`), `format`, `variant`, `tools`,
  `agent`. Same S3 family, separate slices.
- No change to the TUI / print / rpc prompt paths (`cmd/wopr/print_mode.go:306`,
  `coding/host/rpc.go:672`).
- No new type, interface, config key, or dependency.

## Open questions

1. **Settled, not open: the session model is unchanged.** The backlog gate for this item
   (`BACKLOG.md:1877-1880`) and the dispatch that commissioned this spec both require "the session
   model is unchanged". opencode does the opposite — `createUserMessage` calls
   `sessions.setAgentModel` (`packages/opencode/src/session/prompt.ts:679`, conditional at
   `:670-688`) *before* `prompt` returns early at `:1069` — so a noReply prompt that names a model
   does change the session model there. This slice follows the gate: D1 below. The implementer
   records the divergence in `BACKLOG.md` per `AGENTS.md` rather than "fixing" it toward opencode.
2. **Open — accept-time model validation on the noReply path (D1's asymmetry).** `promptAsync`
   resolves the model *before* the hook (`serve_prompt.go:86-94`), so a noReply prompt naming an
   unoffered model is still 400 and never accepted; `sessionMessage` resolves inside the hook, so
   on the noReply branch it is never resolved at all and an unoffered model is silently ignored.
   Recommendation: keep as-is. Refusing an unoffered model where it can be refused is the
   documented behaviour (`docs/site/docs/server.md:56`) and skipping the resolution would reopen
   the accepted-and-ignored bug `TestPromptAsyncAppliesTheClientModel` pins. Name the asymmetry in
   the `sessionMessage` doc comment so it reads as deliberate.
3. **Open — `session.error` for a failed noReply append.** `prompt_async` has already answered
   204, so an append failure is silent, exactly like every other `before`-hook failure today,
   while opencode publishes `session.error` for a failed `prompt_async`
   (`packages/opencode/src/server/routes/instance/httpapi/handlers/session.ts:317-322`).
   Recommendation: no frame this slice; it needs the same out-of-run publish path as D2 and
   belongs with it.

## Decisions

1. **D1 — `noReply` does not change the session model.** Divergence from opencode, ratified by
   the backlog gate for this item and by the dispatch that commissioned this spec. Reason: a
   noReply prompt runs no turn, so applying a per-prompt model that no turn will use mutates
   session state for a message that never reaches a provider. Under this decision the noReply
   branch runs before the model swap in both `before` hooks; a supplied `model` is not applied.
   Known asymmetry, accepted: `prompt_async` still 400s an unoffered model at accept time
   (`serve_prompt.go:86-94`, outside the hook), while `sessionMessage` does not validate a model
   on the noReply path at all. Record this in `BACKLOG.md` as a product divergence with the
   `prompt.ts` citation in Q1.
2. **D2 — no SSE frame for the noReply user message, this slice.** opencode publishes two frames
   for it: `message.updated` and one `message.part.updated` per part, from `updateMessage` /
   `updatePart` (`packages/opencode/src/session/session.ts:631`, `:635-636`, reached from
   `packages/opencode/src/session/prompt.ts:1046-1047`). wopr's user frames are run-driven:
   `eventMapper.translate` reaches a user message only through a `MessageEndEvent`
   (`serve_sse.go:436`), and `messageEnd` (`serve_sse.go:574-597`) needs both that event and the
   entry id the host forwarder resolves from the session's message→entry map
   (`coding/host/host.go:171-175`), which `refreshContext` rebuilds. A noReply append produces no
   event, so an out-of-run frame would have to hand the persisted entry id straight to the
   translator without racing that rebuild. That is its own slice; the transcript and `GET` are the
   gate here.
3. **D3 — route `noReply` through the host run slot, then abort the run from the `before` hook.**
   This is the seam (below). It reuses queueing, entry-id reservation, duplicate-id rejection, and
   the ask_user 409 for free, and it serializes the append against a running turn — which a bare
   `AppendMessage` cannot safely do (see "The seam").
4. **D4 — tracker/doc reconciliation is bookkeeping, not a product decision.** The `server.md`,
   parity-tables, and `BACKLOG.md` edits are this wave's obligation to keep its trackers truthful.

---

## The seam

**opencode persists the user message then returns, before the loop.** `prompt` runs
`const message = yield* createUserMessage(input)` and then `if (input.noReply === true) return message`
(`packages/opencode/src/session/prompt.ts:1057`, `:1069`); `createUserMessage` (from `:635`) ends with
`sessions.updateMessage(info)` and `sessions.updatePart(part)` (`:1046-1047`). Both HTTP endpoints
share it: `POST /session/:id/message`
(`packages/opencode/src/server/routes/instance/httpapi/handlers/session.ts:293-309`) returns the
message as JSON, and `POST /session/:id/prompt_async` (`:311-329`) forks the same `prompt` and
returns 204 (`HttpApiSchema.NoContent`). `PromptPayload` is `PromptInput` minus `sessionID`
(`packages/opencode/src/server/routes/instance/httpapi/groups/session.ts:70`), so `noReply` is a
field on both endpoints — it is not per-endpoint. The field itself is declared at
`packages/opencode/src/session/prompt.ts:1504`.

**Why wopr cannot simply append.** The transcript is the agent's in-memory messages **and** the
append-only session file; `AppendMessage` links each entry to the current leaf
(`internal/codingagent/session.go:595-626`, through `appendOnLeaf` under `leafAppendMu`,
`:629-646`). Appending a user message out-of-band while a turn is in flight would interleave it into
the run's message sequence (between a `tool_use` and its `tool_result`), which replays as an invalid
sequence — the exact hazard the custom-message persistence comment warns about
(`agent/agent.go:1112-1119`). wopr has no isolated store like opencode's, so the append must be
serialized against any run.

**The seam that serializes it is the existing run slot + `before` hook.** Both handlers already call
`PromptContent` / `PromptContentWait` with a `before` hook used for the model and the per-prompt
system text (`serve_prompt.go:98`, `:154`). `before` runs once the run slot is held — a queued prompt
included — and an error from it abandons the turn (`PromptContent`'s `runAndRelease` returns on any
`before` error, `coding/host/host.go:337-344`; `PromptContentWait`'s `withReservation` releases the
slot and returns the error unwrapped, `:387-391`, `:401-413`). The noReply path is: in `before`,
append the user message, then return a sentinel error that abandons the turn before
`SendContentWithPreflight` runs. No provider call, no agent loop, and no run-state transition
(`beginAgentRun` lives inside `sendContent` and `RunAgentPrompt`, neither of which this path
reaches, so `IsStreaming` stays false and `GET /session/status` never reports busy — matches
opencode, whose `noReply` never enters `loop`).

`promptAsync` (`serve_prompt.go:98-109`) — the branch goes first, before the model swap and
before the `SetNextUserSystem` staging, so a noReply prompt leaves neither behind:

```go
if err := sess.PromptContent(r.Context(), content, msgID, func() error {
	if body.NoReply {
		if _, err := sess.Inner().AppendUserMessage(content, msgID, body.System); err != nil {
			return err
		}
		return errNoReply // the append is the whole turn: abandon the run
	}
	if model != nil {
		if err := sess.Inner().SetModel(model); err != nil {
			return err
		}
	}
	sess.Inner().Agent().SetNextUserSystem(body.System)
	return nil
}); err != nil {
	s.writePromptError(w, err)
	return
}
```

`sessionMessage` (`serve_prompt.go:154-170`) — the same branch, but it keeps the persisted entry id
so the handler can answer with the user message:

```go
var noReplyEntryID string
msgs, runError := sess.PromptContentWait(r.Context(), content, msgID, func() error {
	if body.NoReply {
		id, err := sess.Inner().AppendUserMessage(content, msgID, body.System)
		if err != nil {
			return err
		}
		noReplyEntryID = id
		return errNoReply // the append is the whole turn: abandon the run
	}
	if body.Model != nil {
		errDTO, status = s.applyModel(sess, *body.Model)
		if errDTO != nil {
			return errRefused // refusal returns before the set: no pending value
		}
	}
	sess.Inner().Agent().SetNextUserSystem(body.System)
	return nil
})
```

`errNoReply` is a package-level sentinel beside `errRefused` (`serve_prompt.go:210`). Both hooks
return it after a successful append, so there is no second code path to keep in sync:

```go
var errNoReply = errors.New("serve: the prompt was persisted without a run")
```

`prompt_async` swallows it (`runAndRelease` returns on any `before` error,
`coding/host/host.go:340`); the 204 was already written, and no SSE frame is emitted (D2).
`sessionMessage` receives it from `PromptContentWait`, which returns `before`'s error unwrapped
(`coding/host/host.go:387-391`), and the handler intercepts it **before** the generic
`runError != nil` branch. `errors.Is` is false for a nil error, so the ordering is safe either
way:

```go
case got := <-settled:
	if got.errDTO != nil { s.writeJSON(w, got.status, got.errDTO); return }
	if errors.Is(got.runError, errNoReply) {
		if got.noReplyEntryID == "" {
			s.writeJSON(w, http.StatusInternalServerError, errorDTO{Error: "noReply message persisted without an entry id"})
			return
		}
		dto, found := s.assistantMessageDTO(sess, got.noReplyEntryID)
		if !found {
			s.writeJSON(w, http.StatusInternalServerError, errorDTO{Error: "noReply message is not readable from the transcript"})
			return
		}
		s.writeJSON(w, http.StatusOK, dto)
		return
	}
	if got.runError != nil { s.writePromptError(w, got.runError); return }
	... // existing assistant path unchanged
```

`assistantMessageDTO` (`serve_prompt.go:383-397`) already projects **any** entry by id through
`entriesToMessages`, so it serves the user entry unchanged: `Info.Role == "user"`, `Info.ID` is the
entry the append returned, and `Info.System` echoes `body.System` the way a normal turn's user
message does (asserted by `TestPromptSystemAppliesToTheTurn`'s read-back at
`serve_test.go:4321`, via the `lastUserMessage` helper at `:4104`). The name is now a slight misnomer; renaming it to
`messageDTOForEntry` is optional churn with one production caller (`:198`) and is explicitly
out of scope.

**`coding.Session.AppendUserMessage`.** It reuses `persistMessage` (`coding/session.go:559-575`) —
the documented single persistence site — and `refreshContext` (`coding/session_recovery.go:48-50`),
the existing "append to `Inner()` directly, then refresh" seam (`coding/session_ops.go:25-31`,
`coding/session_recovery.go:195`). Going through `persistMessage` rather than
`s.inner.AppendMessage` is what keeps the user-message side effects: it resets
`overflowRecoveryAttempted` (`coding/session.go:560-562`), exactly as a normal prompt's persistence
does, and it routes through `rememberMessageEntry` so the id is resolvable before the rebuild.

```go
// AppendUserMessage persists a user message without running the agent (serve's
// noReply). It normalizes the content the way sendContent does, persists under
// the client-minted entry id or a fresh one, then rebuilds the in-memory
// transcript from the projection so the next turn's provider context contains
// the message. No compaction, no run-lifecycle events, no run state: no turn
// starts, so the caller must already hold the session's run slot — s.mu is
// held here for the append only and refreshContext's callers hold it too.
// Returns the persisted entry id.
func (s *Session) AppendUserMessage(content []ai.UserContentBlock, entryID, system string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.closeDone:
		return "", errors.New("coding: session is closed")
	default:
	}
	content = icodingagent.NormalizePromptContent(content, s.services.SettingsManager().GetImageAutoResize(), s.agent.Model())
	msg := agent.AgentMessage{User: &agent.UserMessage{
		Role:      agent.RoleUser,
		Content:   append([]ai.UserContentBlock(nil), content...),
		Timestamp: time.Now().UnixMilli(),
		EntryID:   entryID,
		System:    system,
	}}
	if err := s.persistMessage(msg); err != nil {
		return "", err
	}
	id, ok := s.MessageEntryID(msg) // persistMessage recorded it; read before refreshContext rebuilds the map
	if !ok || id == "" {
		return "", fmt.Errorf("coding: noReply user message persisted without an entry id")
	}
	s.refreshContext()
	return id, nil
}
```

- `s.mu` is the same lock `sendContent` holds for a whole run (`coding/session.go:683`, `:697`).
  Because the `before` hook runs only once the slot is held, no run is in flight when this
  executes; the lock is belt-and-braces against other callers (e.g. a TUI refresh) and is the
  ownership boundary for the append. It cannot deadlock: the hook runs before
  `SendContentWithPreflight`, which is what takes the lock for a run.
- `refreshContext` is the same convention its other callers follow — `Compact`
  (`coding/session.go:1980-1988`), `appendBashLocked` (`coding/session.go:1450-1452`), and
  `omitRecoveryAttempt` (`coding/session_recovery.go:195`) all hold `s.mu` — so taking it here is
  consistent, not new.
- `NormalizePromptContent` mirrors `sendContent` (`coding/session.go:711`) so the persisted image is
  the resized one a normal turn persists, not raw base64.
- **No compaction check** (`checkPromptCompactionLocked`, `coding/session.go:736-744`): a noReply
  prompt fits no turn, so there is nothing to compact for. Divergence from `sendContent`,
  deliberate.
- `refreshContext` (`coding/session_recovery.go:48-50`, `:59-80`) rebuilds `entryIDsByMessage` from
  the projection and replaces the agent's messages, so the in-memory transcript and disk agree.
  `persistMessage` calls `rememberMessageEntry` first, which is why the id is readable before the
  rebuild — and `MessageEntryID` (`coding/session_recovery.go:159-161`) resolves it.
- `AppendMessage` honors `msg.User.EntryID` over a fresh id
  (`internal/codingagent/session.go:619-623`), so a client-minted `messageID` lands under that id;
  the duplicate and reservation checks that guard it (`resolveMessageID` `serve_prompt.go:266`,
  `reserveEntryID` `coding/host/host.go:256-270`) already ran because the call is inside the slot.

## The observable / gate

New `TestPromptNoReplyPersistsWithoutAnAssistantTurn` in `serve_test.go`, hermetic via
`promptEnvFor` (`:151`; `WOPR_TEST_FAUX=1`) and `createPromptSession` (`:180`). One session covers
both halves, so the async half builds on the sync half's transcript. Baseline first:
`before := sessionModelOf(t, baseURL, id)`.

**`POST /session/{id}/message` (sync).**
1. Post `{"parts":[{"type":"text","text":"reply with exactly: should-not-run"}],"noReply":true,
   "system":"no-reply-only"}` → expect **200**. Decode the body as `messageDTO` and assert
   `Info.Role == "user"` (opencode returns the user message,
   `packages/opencode/src/server/routes/instance/httpapi/handlers/session.ts:306`), `Info.ID`
   non-empty, and `Info.System == "no-reply-only"`.
2. `readTranscript(t, baseURL, id)` (`:712`): exactly **1 `user` entry, 0 `assistant` entries**.
   Count by role over the DTOs, never by slice length.
3. `sessionModelOf` (`:345`) still equals `before`.

**`POST /session/{id}/prompt_async` (async).**
4. Post the same body → expect **204**. The append runs on the host's run goroutine and no busy
   state is ever observable, so poll `readTranscript` with the deadline idiom
   `TestPromptMessageIDRejectsMalformedAndDuplicate` uses (`:4332`, poll loop `:4353-4363`) until the
   user count is **2**, `t.Fatal` on timeout.
5. Assert the assistant count is still **0** and `sessionModelOf` still equals `before`.

Use a recognized trigger (`reply with exactly: ...`) rather than literal text:
`classifyTestFauxRequest` (`ai/test_faux.go:267`) turns unrecognized text into an error response, so
a normal run on `"hi"` would fail rather than produce an assistant entry — which would mask the
"0 assistant" assertion. Do not write `auth.json` in the base test: the model assertion baselines the
session's default model, and `faux-2` is only needed for the D1 mutation below.

**Mutations that must turn the test red.**
- **Primary — the run is not actually skipped.** Delete `return errNoReply` from the
  `sessionMessage` hook (keeping the append). The hook then returns nil, the run starts on a
  recognized trigger, and step 5's assistant count becomes 1. On the sync half the handler falls
  through to `runError != nil` and answers 500, so step 1 fails too. This is the falsification that
  proves the turn is genuinely abandoned rather than merely hidden.
- Delete the whole `if body.NoReply` branch in `promptAsync`: step 4 sees an assistant entry and
  step 5 fails. This is the mutation that catches an implementation special-casing only the sync
  endpoint.
- Make `AppendUserMessage` return before persisting (a no-op append): 0 user entries, so step 2
  fails at 1 and step 4 never reaches a count of 2.
- **D1 — the model.** Add `sess.Inner().SetModel(model)` before the noReply branch in
  `sessionMessage`, write `auth.json` with a stored `test-faux` key so `faux-2` is offered (as
  `TestPromptAsyncAppliesTheClientModel` does, `serve_test.go:298`, setup `:301-306`), and send
  `"model":{"providerID":"test-faux","modelID":"faux-2"}` in the step-1 body: step 3 fails. Under D1
  that is the *bug*; if the divergence is ever ratified the other way, this mutation becomes the
  desired behavior and the model assertion inverts.

## Edge cases

- **`noReply` absent** — `body.NoReply` is false, both hooks keep their current path unchanged.
- **noReply while a turn runs** — the prompt queues behind the running turn (`PromptContent`,
  `coding/host/host.go:314-328`) and appends when it owns the slot; it does not interleave into the
  running turn. Divergence from opencode, which appends immediately against isolated storage.
- **queued noReply** — the append runs only when the queued prompt holds the slot, same guarantee
  `model` and `system` have; the running turn's transcript and pending per-prompt state are untouched.
- **client-minted `messageID`** — honored (`AppendMessage`, `internal/codingagent/session.go:621`);
  a duplicate is 400 from `resolveMessageID`/`reserveEntryID` before the append.
- **pending `ask_user`** — `promptAsync` still answers 409 before accepting (`serve_prompt.go:77`), so a
  noReply prompt during a blocked question is refused, unchanged.
- **images** — `NormalizePromptContent` resizes as a normal turn would; the raw body cap still applies
  (`maxBodyBytes`).
- **disk write failure** — `AppendUserMessage` returns the error; `sessionMessage` maps it through
  `writePromptError` (500). `prompt_async` has already answered 204, so the failure is silent to the
  client, exactly as every other `before`-hook failure is.
- **`refreshContext` on a user-only branch** — the projection must reconstruct a lone user message
  (it already does for abort-resume scenarios); the gate's step 2 is the check.
- **`system` + `noReply`** — `body.System` is stored on the user message (opencode does the same,
  `packages/opencode/src/session/prompt.ts:668`); it is not staged via `SetNextUserSystem`, so it
  cannot leak into the next run. Step 1 of the gate asserts the echo.

## Minimal-code check

No new type, interface, config key, or dependency. Reused: the run slot and its `before` hook
(queueing, entry-id reservation, duplicate rejection, ask_user 409), `persistMessage`
(`coding/session.go:559`), `refreshContext` (`coding/session_recovery.go:48`), and
`NormalizePromptContent` (`coding/session.go:711`). New mutable state: none. New named code:
`promptBody.NoReply` (a request field), `errNoReply` (a sentinel, no state),
`outcome.noReplyEntryID` (a value on the existing per-request struct), and
`coding.Session.AppendUserMessage` (one method). No agent-loop, host-signature, or SSE-mapper
change, and no new DTO — `assistantMessageDTO` already projects any entry by id.
