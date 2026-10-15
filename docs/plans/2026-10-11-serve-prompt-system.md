# serve-prompt-system — per-prompt `system` field on the prompt endpoints

Backlog: `serve-prompt-system` (`BACKLOG.md:1854`). Authority: opencode `v1.18.34`
(`/home/kc/dev/opencode-src` @ `aec0b9a`) is the specification for `wopr serve`; where they
disagree, opencode wins (`AGENTS.md`). Worktree: `/home/kc/dev/wopr/.worktrees/wave-serve-prompt-system`.

## Files touched

- `internal/codingagent/serve/serve_prompt.go` — decode `system`; in both handlers' run-slot `before` hook call `SetNextUserSystem` unconditionally **after** a successful model resolution; narrow the `ponytail:` comment (`:16-23`) to drop only the `system` clause.
- `agent/agent.go` — `UserMessage.System`; `Agent.nextUserSystem` + `SetNextUserSystem`; `SendContent` attaches it to the user message and clears it.
- `agent/agent_loop.go` — inline the suffix projection at the provider boundary in `streamAssistantResponse`, reusing `projectSystemPrompt`; `lastUserSystem` defined beside its only caller.
- `agent/message_json.go` — persist and round-trip `UserMessage.System`: user marshal struct (`system,omitempty`), `agentMessageWire`, user unmarshal case.
- `internal/codingagent/serve/serve_dto.go` — `messageInfoDTO.System` (`omitempty`) and the user branch of `MarshalJSON`.
- `internal/codingagent/serve/serve_handlers.go` — `entriesToMessages` copies the user entry's `System`.
- `internal/codingagent/serve/serve_sse.go` — the two user frames carry `System` (forced by the transcript/stream no-drift invariant, `serve_handlers.go:1057-1059`).
- `ai/test_faux.go` — the gate's provider-request capture: a mutex-guarded package var + `LastTestFauxSystemPrompt()` accessor recording the last request's effective system prompt.
- `internal/codingagent/serve/serve_test.go` — the gate test.
- `docs/site/docs/server.md` — document the `system` prompt-body field (`:56-57`) and the user message info field (`:54`).
- `docs/plans/2026-10-09-serve-v1-conformance-and-parity-tables.md` — flip the `system` row (`:52`) to done with the gate name.
- `BACKLOG.md` — mark `serve-prompt-system` done; record the D1 divergence as a product decision with its reason.

## Gates to run

```
go build ./...
go vet ./agent/... ./coding/... ./ai/... ./internal/codingagent/serve/...
go test ./agent/... ./coding/... ./ai/... ./internal/codingagent/serve/...
go test ./internal/codingagent/serve/ -run TestPromptSystemAppliesToTheTurn -v
go tool golangci-lint run ./agent/... ./coding/... ./ai/... ./internal/codingagent/serve/...
make check   # pre-commit; the tmux integration tier may not run here
```

## Definition of done

Both `POST /session/{id}/message` and `/prompt_async` accept `system`; the user message each
produces persists `system` and returns it on `GET /session/{id}/message`; the provider request
for that turn's system prompt is the base prompt **plus** that text, joined with `"\n"` and
appended last; a later turn without `system` carries neither. The gate covers both endpoints
independently — their `before` closures are separate (`serve_prompt.go:94`, `:147`), so each needs
its own step — and reads the two SSE user frames to cover the stream path.
`go build`, `go vet`, the touched packages' tests, `go tool golangci-lint run`, and the gate test pass.

## Out of scope

- `noReply` (`serve-prompt-no-reply`) and `parts: agent|subtask`
  (`serve-prompt-parts-agent-subtask`). Same S3 family, separate slices.
- `format` / structured-output retry, `variant`, `tools`, `agent` — untouched.
- No new type, interface, config key, or dependency.
- No change to the TUI/print/rpc prompt paths (their user messages carry no `system`).

## Open questions

None.

## Decisions

1. **D1 — `system: ""` is treated as absent.** wopr treats an empty `system` string as absent: it
   appends nothing and, with `omitempty`, omits the key. This is a **recorded product divergence**
   from opencode, added to `BACKLOG.md` under `serve-prompt-system` by this slice. opencode stores
   `Some("")` and echoes `"system":""` through `SessionV1.User.system`
   (`packages/schema/src/v1/session.ts:352`), but its append is `input.user.system ? [...] : []`
   (`packages/opencode/src/session/llm/request.ts:62`), which never appends a falsy value — model
   behavior is identical, only the echoed JSON differs for the empty case. Reason: wopr keeps
   `UserMessage.System` a plain `string` rather than introducing a `*string` only to echo an empty
   override.
2. **D2 — SSE user frames carry `system`.** The stream's two user frames (`serve_sse.go:453` settle,
   `:577` start) include the per-prompt value. This is opencode parity, and it is forced by the
   transcript/stream no-drift invariant (`serve_handlers.go:1057-1059`): the frames share
   `messageInfo`, so the stream and `GET` must report the same user info. Gate coverage: the gate
   reads both user frames off `/global/event` (below). `serve_sse.go` stays in scope.
3. **D3 — tracker/doc reconciliation is bookkeeping, not a product decision.** The
   `docs/site/docs/server.md`, parity-tables, and `BACKLOG.md` edits are this wave's obligation to
   keep its trackers truthful; they are in scope but decide no product behavior.

---

## The seam

**opencode does not override — it appends.** `prepare` builds the request system prompt as
`join("\n", [agent.prompt or provider default, ...input.system, ...(user.system ? [user.system] : [])])`
(`packages/opencode/src/session/llm/request.ts:56-66`). `input.user.system` (the per-prompt value)
is the **last** element, after the base env/instructions/mcp/skills. `createUserMessage` stores it
on the user message info (`prompt.ts:635-670`, `system: input.system` at `:668`;
`SessionV1.User.system` at `packages/schema/src/v1/session.ts:352`), and `lastUser` is fixed for the run
(`prompt.ts:1096`, `:1273`) so the append holds for every step of that turn and cannot leak into the
next, whose `lastUser` carries no `system`. The backlog's word "override" is **wrong**; this slice
appends, matching opencode.

**Where it goes.** The single provider-facing build site is `streamAssistantResponse`
(`agent/agent_loop.go:379`; the provider-facing build is at `:390`). The existing per-turn
projection sits right there: `if a.forcedSystemPrompt != nil { llmMsgs = projectSystemPrompt(...) }`
(`:394-396`). Add the suffix projection next to it, after it, so the per-prompt text lands last:

```go
if sys := lastUserSystem(contextMsgs); sys != "" {
	prompt := sys
	if base := ai.GetCurrentSystemPrompt(llmMsgs); base != "" {
		prompt = base + "\n" + sys
	}
	llmMsgs = projectSystemPrompt(llmMsgs, prompt)
}
```

`projectSystemPrompt` (`agent/system_state.go:96-109`) already does the whole job: it takes
`ai.GetCurrentSystemMessage(messages)` as the head, folds `prompt` into that head's `Content` via
`ai.SystemText`, clears `Sections`, and drops every other system message. The head's tool state is
not lost: `GetCurrentSystemMessage` recomputes it from `GetCurrentTools(messages)`, so the net
active tool set — any prior `ToolsRemoved` already applied — rides the projected head as
`ToolsAdded`. Feeding it `base + "\n" + sys` appends the per-prompt text instead of replacing the
base, and the empty-base guard avoids a leading `"\n"` when there is no base prompt. No new
projection function is added.

`contextMsgs` is `r.context`. `lastUserSystem` (defined in `agent/agent_loop.go`, beside its only
caller) returns the newest user message's `System`. Within a serve turn the newest user message is
the turn's prompt: the serve surface never calls `Steer`/`FollowUp` (the serve Session methods live
at `coding/host/host.go:240-466`; steering exists only on the separate RPC surface,
`coding/host/rpc.go:273`), and the inner tool loop appends only assistant/toolResult messages. This
is **not** a general invariant: `loop()` does append steering user messages
(`agent/agent_loop.go:130`, `:154-156`, `:172`), so if steering is ever surfaced through serve this
must become an explicit per-turn field rather than "newest user message".

Providers render the initial system message through `ai.GetCurrentSystemPrompt(messages[:min(1, len(messages))])`
(`ai/openai.go:1217`, `ai/google.go:559`, `ai/mistral.go:265`); Anthropic renders it as
`GetCurrentSystemPrompt([]Message{*initialSystem})` on the initial system message
(`ai/anthropic.go:1097-1098`), not `messages[:1]`. After `projectSystemPrompt` folds the base and
suffix into the head, that text is what every provider sends last.

**How the string reaches the user message.** The run-slot `before` hook is already the per-prompt,
queue-safe seam (`coding/host/host.go:314`, `:379`); both handlers pass one for `model`
(`serve_prompt.go:94`, `:147`). Add `promptBody.System string \`json:"system,omitempty"\`` (`:24`)
and, in each `before`, call `sess.Inner().Agent().SetNextUserSystem(body.System)` **after** a
successful model resolution, unconditionally (a prompt without `system` clears any pending value).
A model refusal must return **before** the set, so a refused prompt leaves no pending value. Both
current closures return early when the client sends no model, which would skip the set; rewrite
them so the model swap is conditional and the set is not:

`prompt_async` (`serve_prompt.go:94-99`):

```go
if err := sess.PromptContent(r.Context(), content, msgID, func() error {
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

`sessionMessage` (`serve_prompt.go:147-156`):

```go
msgs, runError := sess.PromptContentWait(r.Context(), content, msgID, func() error {
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

`coding.Session.Agent()` is exported and returns
the run agent (`coding/session.go:915`), so no new delegation method is needed; unlike `SetModel`
(`coding/session.go:860`), setting the next user system writes no audit entry and needs no
delegating wrapper. `Agent.SetNextUserSystem` stores `nextUserSystem`; `Agent.SendContent`
(`agent/agent.go:727`, the user message built at `:737`) sets `UserMessage.System` and clears the
field.

Residual narrow case: if a `before` hook succeeds (value set) but the run never reaches
`SendContent` — an `Abort` landing between the hook and the send — the pending value survives until
the next `SendContent` on any path consumes it. It is narrow because the hook and the send are
adjacent on the run goroutine, and the refusal ordering removes the realistic case (model refusal).

**Persistence + DTO.** `AgentMessage.MarshalJSON`/`UnmarshalJSON` emit a fixed user shape and drop
unknown fields (`agent/message_json.go:53`, `:140`), so `UserMessage.System` must be added to the
user marshal struct (`System string \`json:"system,omitempty"\``) and `agentMessageWire`, and the
`RoleUser` unmarshal case must set it, or it will not survive the persist/reload round-trip. The
`omitempty` tag is required so every existing non-serve user message on disk does not gain a
`"system":""` key. `UserMessage` is copied by value in `AgentMessage.Clone` (`message_json.go:19`),
so `System` survives projection for free. `messageInfoDTO` (`serve_dto.go:75`) gains
`System string \`json:"system,omitempty"\`` and the user branch of `MarshalJSON` (`:116-141`) emits
it; `entriesToMessages` (`serve_handlers.go:1142`) sets `msg.Info.System = me.Message.User.System`
for user rows. The two SSE user frames get the same value with a new `eventMapper.turnUserSystem` field,
recorded at the user `message_end` (`serve_sse.go:573-574`) and emitted on both frames. It is not
redundant with the DTO field: the settle frame is emitted from `AgentSettledEvent`
(`serve_sse.go:450-455`), which carries no user message, so the mapper must retain the value the
start frame read. Both frames route through the shared `messageInfo` constructor
(`serve_handlers.go:1060`); setting `info.System` on each keeps them agreeing with
`entriesToMessages`/GET, the no-drift invariant at `serve_handlers.go:1057-1059`.

## The observable / gate

New `TestPromptSystemAppliesToTheTurn` in `serve_test.go`, hermetic via `promptEnvFor` (temp CWD and
agentDir, `WOPR_TEST_FAUX=1`; it does **not** set `WOPR_HOME` and does **not** clear credential vars —
test-faux is credential-free), `createPromptSession`. It covers both endpoints. The backlog listed
"the model's response reflects it" as the observable; **the gate instead asserts the provider request
that carried the system prompt**, which is the behavior under test and needs no model cooperation. It
drives the faux provider with a recognized trigger (`reply with exactly: ...`), not literal text like
`"hi"`, which `classifyTestFauxRequest` turns into an error response.

Observable (b) is read through `ai.LastTestFauxSystemPrompt()` — a package-level, mutex-guarded capture
in `ai/test_faux.go`. Exactly:

```go
var (
	testFauxCaptureMu  sync.Mutex
	testFauxLastSystem string
)

// recordTestFauxSystemPrompt runs at the top of TestFauxProvider.Stream, before
// the goroutine, from the messages it already computes.
func recordTestFauxSystemPrompt(prompt string) {
	testFauxCaptureMu.Lock()
	testFauxLastSystem = prompt
	testFauxCaptureMu.Unlock()
}

// LastTestFauxSystemPrompt returns the effective system prompt of the last
// test-faux request, or "" before the first one.
func LastTestFauxSystemPrompt() string {
	testFauxCaptureMu.Lock()
	defer testFauxCaptureMu.Unlock()
	return testFauxLastSystem
}
```

`Stream` calls `recordTestFauxSystemPrompt(GetCurrentSystemPrompt(messages))` on the
`messages := transcript.Messages()` it already computes (`ai/test_faux.go:121`). The capture is
inherently gated because `TestFauxProvider` is only reachable under `WOPR_TEST_FAUX=1`; do **not**
add a second `os.Getenv("WOPR_TEST_FAUX")` check around it. No `coding/model.go` change and no
`Model().Provider.(*ai.TestFauxProvider)` unwrap: `BuildModel` wraps every provider in the unexported
`*coding.providerAttributionProvider` (`coding/model.go:69`, type at `:486-506`), so that assertion
can never succeed from `package serve`.

`POST /session/{id}/message`:
1. Send `{"parts":[{"type":"text","text":"reply with exactly: poet-ok"}],"system":"You are a poet"}`;
   expect 200.
2. `GET /session/{id}/message`; the user entry's `info.system == "You are a poet"` — observable
   **(a)**: opencode's own `SessionV1.User.system` DTO field, so this doubles as wire conformance.
3. Read `ai.LastTestFauxSystemPrompt()` — observable **(b)**: assert it `HasSuffix("You are a poet")`
   and is longer than the override, i.e. the base prompt is present (append, not replace).
4. Second `POST /session/{id}/message` with the same text and **no** `system`; assert the new user
   entry has no `system` and the captured prompt does **not** contain `"You are a poet"` — proves
   exactly-one-turn and no bleed.
5. SSE coverage (D2): subscribe to `/global/event` before a `/message` turn that carries `system`,
   collect frames to `session.idle`, and assert both `message.updated` user frames for that turn (the
   `message_end` start frame and the `AgentSettledEvent` settle frame, same entry id) carry
   `"system":"You are a poet"`. Use `openSSE`/`readSSE`.

`POST /session/{id}/prompt_async` (its own `before` closure, `serve_prompt.go:94`):
6. Send the same body with `system`; expect 204, wait for idle, then `GET /session/{id}/message` shows
   the override on the new user entry, and `ai.LastTestFauxSystemPrompt()` appended it.

**(a)** is the primary observable because it is the exact DTO field opencode exposes and needs no new
plumbing; **(b)** is included because (a) alone would pass even if the string never reached the model.

**Mutations that must turn the test red.** Delete the `sess.Inner().Agent().SetNextUserSystem(body.System)`
call from a handler's `before` hook: steps 2 and 3 fail (the DTO field is absent, the captured prompt
lacks the suffix), so the test is red. Step 4's no-bleed assertion would *pass* under this mutation —
nothing is ever appended — but the test is red regardless via 2–3, which is exactly why it must assert
all of them. A second, narrower mutation for the append semantics: make the projection replace the base
instead of appending — step 3's length/`HasSuffix` check fails while step 2 still passes. A third: drop
`info.System` from the settle frame — step 5 fails while GET stays green.

## Edge cases

- **`system` absent** — `nextUserSystem` is `""`, no append, DTO omits the key. Unchanged behavior.
- **`system: ""`** — treated as absent (D1). No append, key omitted.
- **queued prompt** — `before` runs only when the queued prompt owns the slot; the running turn's
  user message and `nextUserSystem` are untouched (same guarantee `model` has; host.go `:314-343`).
- **multiple turns** — the append reads the *newest* user message, so turn N+1 with no `system`
  appends nothing; the previous override cannot bleed.
- **prepareRequest/compaction** — `prepareRequest` (`coding/session_transcript.go:125`; distinct from
  `projectedContext` at `:119`) re-projects `r.context` before each request; the user message's
  `System` survives projection because `AgentMessage.Clone` copies it (`message_json.go:19`). A
  compaction retry re-reads the same user message, so the append holds for the whole turn, matching
  opencode's fixed `lastUser`.
- **model refusal** — `SetNextUserSystem` is called only after a successful model resolution, so a
  bad-model refusal returns before it and leaves no pending value. Residual narrow case: a hook that succeeds but whose run aborts before
  `SendContent` leaves the value pending until the next `SendContent` overwrites it (see The seam).

## Minimal-code check

No new type or abstraction. Reused: the `before` hook (queue safety), `UserMessage` + its JSON
round-trip, the existing `projectSystemPrompt`, `messageInfoDTO`/`messageInfo`, and the
`GetCurrentSystemPrompt` renderers. New mutable state is small and named: `Agent.nextUserSystem`
(set and consumed on the same run goroutine by the adjacent `before` hook and `SendContent` — the
same ownership as the `model` swap in the identical `before` hook), `eventMapper.turnUserSystem`
(the mapper already holds per-turn `turnUserID`/`turnUserCreated` scalars for the settle frame), and
the `ai` test-faux capture var + mutex. The suffix path is `projectSystemPrompt`, not
`SetSystemPrompt`: `SetSystemPrompt` (`agent/agent.go:653-656`) mutates `a.opts.SystemPrompt` and so
persists across turns, whereas the projection here is request-scoped and cannot leak.
