# Plan: emit the user `message.updated` in opencode's two-phase form (parity only)

**Objective.** Make the SSE stream emit exactly two user `message.updated` frames per turn, matching opencode's two-phase form: at turn start a frame with no `summary` key, and at/after turn end a second frame for the same message id carrying `summary:{"diffs":[]}`. This is frame parity only. It is not a double-render fix and must not be described as one. The persisted transcript and the `GET /session/{id}/message` user shape are unchanged (both keep `summary`).

**Files touched** (all under `ROOT=/home/kc/dev/wopr/.worktrees/wave-serve-sse-user-message-two-phase`):
- `ROOT/internal/codingagent/serve/serve_dto.go`
- `ROOT/internal/codingagent/serve/serve_sse.go`
- `ROOT/internal/codingagent/serve/serve_test.go`

**Gates** (run from `ROOT`):
- `go build ./...`
- `go vet ./internal/codingagent/serve/`
- `go test ./internal/codingagent/serve/ -run 'TestSSEUserMessageTwoPhase|TestSSETurnLifecycleParity|TestPromptMessageIDHonoredEndToEnd|TestMessageInfoRoleShapes|TestSSEMessageUpdatedShapeMatchesOpencode|TestSSETurnSessionFramesMatchOpencode'`
- `go test ./internal/codingagent/serve/...`
- `make check`

**Definition of done** (falsifiable, from one prompted turn over SSE):
1. Exactly two `message.updated` frames have `info.role == "user"` (today: one).
2. Both carry the same `info.id`.
3. The first has no `summary` key in its `info` object.
4. The second has `info.summary` as an object whose `diffs` is `[]` and non-null.
5. `GET /session/{id}/message` user entries still emit `summary:{"diffs":[]}` (read path unchanged).
6. All gates above pass.

**Out of scope:**
- Any change to persisted entries, `entriesToMessages`, or the read-path user shape.
- Any claim, mention, or "fix" of the KKCode double-render — cause is UNESTABLISHED.
- Reproducing opencode's third end-frame (the capture shows start + two end frames; the DoD pins exactly two).
- New dependencies, refactors, or changes to other event types.

**Open questions:** none.

## Design

### 1. Suppress `summary` on the start frame only

`ROOT/internal/codingagent/serve/serve_dto.go`

- In `messageInfoDTO` (lines 75-89) add one unexported field:
  `omitSummary bool` — a stream-phase flag, never wire data. Zero value `false`, so every existing caller is unchanged.
- In `messageInfoDTO.MarshalJSON`, user branch (lines 110-132), change the anonymous struct field type from `Summary messageSummary` to a pointer `*messageSummary` tagged with `omitempty`, and build it conditionally:
  ```go
  var summary *messageSummary
  if !m.omitSummary {
      summary = &messageSummary{Diffs: []json.RawMessage{}}
  }
  ```
  Non-nil pointer emits `"summary":{"diffs":[]}`; nil is omitted by `omitempty`. The `plain` assistant branch is untouched.

Why this is safe: the read path (`serve_handlers.go:1060` `messageInfo` → `entriesToMessages`) never sets `omitSummary`, so it keeps emitting `summary` (DoD 5). Only the SSE start frame sets it, in the one place that must diverge.

### 2. Start frame carries no summary

`ROOT/internal/codingagent/serve/serve_sse.go`, `messageEnd` user branch (lines 552-570).

- Keep `m.turnUserID = entryID`.
- Record the created clock: add field `turnUserCreated int64` to `eventMapper` (struct at lines 341-386; document it beside `turnUserID` as "the user message's created time, recorded at its message_end and reused on the turn-end frame so both phases report one clock").
- Set `m.turnUserCreated = now` in this branch.
- Build the DTO, set `info.omitSummary = true`, then emit as today. The part frames after it are unchanged.

### 3. Turn-end frame in the settle burst

`ROOT/internal/codingagent/serve/serve_sse.go`, `translate` `agent.AgentSettledEvent` case (lines 435-443).

- Emit, guarded by `m.turnUserID != ""`, one `message.updated` built as
  `messageInfo(m.sessionID, m.cwd, m.root, m.turnUserID, "user", "", m.turnUserCreated, nil, m.providerID, m.modelID)` (leave `omitSummary` false), wrapped in `sseMessageUpdated{SessionID, Info}`.
- Position: **after `session.diff`, before `status("idle")`** — burst order becomes `session.updated`, `session.diff`, user `message.updated`, `session.status idle`, `session.idle`. Rationale: the capture's summary-bearing frame (`/tmp/oc-turn2.txt:35`) immediately follows `session.diff`, and putting it before the idle terminators guarantees any reader that stops on `session.idle` (as the tests do) deterministically includes it.
- Fires once per run: `emitAgentSettled` (`coding/session_run_state.go:135-141`) is reached unconditionally after `SendContent` returns (`coding/session.go:714-726`), including aborted/errored runs, so an aborted turn also gets its second frame. The type doc is `agent/agent.go:256-259`. Accepted; the DoD counts one start + one end per turn.
- Latent risk (accepted, no machinery): the `m.turnUserID != ""` guard is sticky per session (`serve_sse.go:382-385`); a settle that observed no new user message would re-emit the prior turn's id and clock. No Continue endpoint exists today, so there is no live path.

No lock is needed: the mapper stays single-goroutine per session.

### 4. Hermetic test

Add `TestSSEUserMessageTwoPhase` to `ROOT/internal/codingagent/serve/serve_test.go`. Drive a real in-process turn (`promptEnvFor` + `createPromptSession` + `openSSE` + `prompt_async` + `readSSEUntil` until `session.idle`), exactly like `TestSSETurnLifecycleParity`. No `/tmp`, no network, temp `WOPR_HOME`.

- Parse each `message.updated` `f.Data` with `json.Unmarshal` into `map[string]json.RawMessage`, take `info`, filter `info["role"] == "user"`; collect in order.
- Assert `len == 2`; `id`s equal; `info["summary"]` absent on the first; on the second, `info["summary"]` present and its `diffs` unmarshals to a slice of length 0 and is not `null`.
- Non-vacuity: reverting to the single-summary emission fails at `len == 2` and at "first has no summary"; reverting only the DTO flag fails the no-summary assertion on frame one; reverting only the settle emission fails the two-frame length.

### 5. Other tests whose expectations change

- `TestSSETurnLifecycleParity` (serve_test.go:978), section "1. Exactly one user message.updated" (1006-1028): expect count 2 (today 1); keep the id check; assert frame one has no `summary` key and frame two has `summary.diffs == []`. Parse the raw JSON — `sseMessageUpdated` has no `summary` field, so presence cannot be observed through it.
- `TestPromptMessageIDHonoredEndToEnd` (serve_test.go:3891), section "(a) Exactly one user message.updated" (3906-3925): its `readSSEUntil` stops on `session.idle` (3904) and the settle frame is emitted before idle, so it now sees 2 frames. Change `userUpdates != 1` to expect 2; keep the `msg_test1` id check (both frames carry it); assert the two-phase shape with the same raw-JSON helper §4 uses. Its second half (3963-3983) only reads the last user id and asserts no count — unaffected.
- Helper seam (one synchronous change): `assertUserInfoShape` (serve_test.go:3300) gains a third param — `assertUserInfoShape(t, info, wantSummary bool)`. When `wantSummary` is false it expects the user key set without `summary` and skips the diffs assertion; when true it is today's behavior. `assertInfoShape` (3266-3275) keeps its signature and its user branch continues to call `assertUserInfoShape(t, info, true)`, so the read path (`assertMessageShape` at 3329 and the `read` subtest at 3665) stays strict. The two SSE sites that now see the no-summary start frame call `assertUserInfoShape(t, info, false)` directly, not `assertInfoShape`:
  - `TestMessageInfoRoleShapes` `sse_user` (serve_test.go:3699): replace `assertInfoShape(t, info)` with `assertUserInfoShape(t, info, false)` (the frame it holds is the `messageEnd` start frame).
  - `TestSSEMessageUpdatedShapeMatchesOpencode` (serve_test.go:3606): branch on the decoded role — `user` → `assertUserInfoShape(t, info, false)` (the first `message.updated` is the start frame), otherwise `assertInfoShape(t, info)`.
- `TestSSETurnSessionFramesMatchOpencode` (serve_test.go:1181): unaffected — it filters `message.*` frames out (confirmed at 1209-1211).
- Full sweep for exact user `message.updated` counts: only the two `userUpdates` sites (1008 in `TestSSETurnLifecycleParity`, 3907 in `TestPromptMessageIDHonoredEndToEnd`) assert a count; every other `message.updated` reference filters by role/id or reads the last id without a count. No other site changes.
