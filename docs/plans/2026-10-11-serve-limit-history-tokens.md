# serve-limit-history-tokens — cap the per-request context to a token budget

**Status:** spec (Rev-2). One slice, independently committable.
**Item:** `BACKLOG.md:1817` `serve-limit-history-tokens`.

## Scannable surface

**Files touched**
- `coding/services.go` — add `ServicesOptions.MaxHistoryTokens *int`, store on `Services`.
- `coding/session_efficiency.go` — add `(*Session).maxHistoryTokensFor(model *ai.Model)`.
- `coding/session_transcript.go` — replace the returned `Context` at the end of `prepareRequest`.
- `internal/codingagent/compaction/trim.go` — new `compaction.TrimMessages`.
- `cmd/wopr/serve.go` — `--max-history-tokens` flag + plumbing.
- `coding/session_history_limit_test.go` — the one gate test (new).

**Gates**
```
go build ./...
go vet ./...
go test -count=1 ./coding/... ./internal/codingagent/compaction/...
go tool golangci-lint run ./coding/... ./internal/codingagent/compaction/... ./cmd/wopr/...
make check          # before commit
```

**Definition of done:** `TestMaxHistoryTokensTrimsOldest` (package `coding`) passes and
fails if the trim is removed; a request over the budget is returned with the oldest
non-system messages dropped, no orphan tool result, and the leading system baseline kept;
default is 80% of the active model's effective window; `0` disables the cap; every named
gate green.

**Out of scope (do NOT do):** truncate `inner.entries` or the on-disk JSONL; mutate
`agent.messages`; change compaction, pruning policy, TUI, print, or RPC; add any other cap;
add config knobs the item did not ask for; snapshot/table tests.

**Open questions**
1. The 80% default applies wherever `ServicesOptions` is constructed (serve **and** TUI, print,
   host), not serve-only. Needs the owner's ratification on ship. See "Default scope" below.
2. Should a trimmed request emit a `SavingsEvent`? Not required by the item; left out.

---

## 1. The real seam

`ServerOptions` does not exist (`grep` finds only `BACKLOG.md`). The established serve
configuration seam is `coding.ServicesOptions` (`coding/services.go:67`), following
`LearnMaxModels`/`LearnTTLDays` (`coding/services.go:81-87`, stored `:148-149`, read
`coding/session_efficiency.go:56-61`). **Use `coding.ServicesOptions`.**

The item names `prepareNextTurn`/`prepareNextPrompt`. That seam is **wrong**:

- `prepareNextTurn` is `coding/session_transcript.go:149`; it returns
  `AgentLoopTurnUpdate{Context: s.projectedContext()}` (`:174`).
- The loop applies that to `r.context` (`agent/agent_loop.go:144` → `applyUpdate`
  `:289`, `:311`), but **the same iteration then calls `r.prepareRequest()`**
  (`agent/agent_loop.go:157`), which **overwrites `r.context` before the provider request**.
- Therefore a trim returned from `prepareNextTurn` is dead on arrival: it never reaches a
  request.

The seam that shapes every *conversation* provider request is `prepareRequest`
(`coding/session_transcript.go:125-143`), reached on every turn including the first of a run.
(Compaction summarization builds its own request separately — `compaction/branch_summarization.go`
`completeSummarization` — and is deliberately not touched.)

Do **not** trim inside `pruneRequestContext` (`coding/session_pruning.go:174`): its result
feeds router selection (`ensureRoute`, `coding/session_transcript.go:129`), request accounting
(`efficiencyRecordRequest` `:138`), and learner accounting (`learnRequest` `:139`). Trimming
there would change routing/learner inputs, and the resolver would read `activeModel()`
before `ensureRoute` stores the routed model (`:141`).

**Put the cap at the end of `prepareRequest`, replacing the returned `Context`, using the
already-resolved local `model`** (after `ensureRoute`, after the accounting calls).

## 2. Where the trim actually bounds memory (item premise is partially wrong)

- The **persistent** in-memory transcript is `icodingagent.Session.entries`
  (`internal/codingagent/session.go:335`), and the package states the invariant plainly:
  "no entry is ever rewritten or deleted" (`internal/codingagent/session.go:325-328`). It
  grows for the life of the session and is **not** bounded by this change. Truncating it is
  data loss and breaks branches, undo, and resume — **out of scope**.
- `agent.messages` is re-derived from the compacted projection at every compaction
  (`coding/session_recovery.go:48-80`), so it is bounded by compaction, not unbounded. This
  change does not touch it.
- The trim bounds the **per-request context**: the slice cloned per request
  (`agent/agent_loop.go:311`) and handed to the provider. This is real (it is wopr's
  goal #2, "carry fewer tokens per request") but it does **not** cap the retained transcript.
  State this honestly in the code comment; do not claim an RAM cap the change does not deliver.
- **Subagent turns (item's open question):** a subagent's result reaches the parent as a custom
  message in the parent projection, so it is counted by the parent's cap; the subagent's own
  request context is capped by its own session/turn. No new test.

Crucially, the cap does **not** interfere with compaction: the compaction trigger counts
`s.inner.BuildSessionProjection()` (`coding/session_transcript.go:152-153`), which is built
from the untrimmed `entries`, so compaction still fires at its threshold and still summarizes
the full history. The trim only hides the prefix from the model between compactions.

## 3. Config and the 0/default contradiction

The item says both "default 80% of the window" and "0 = no limit". Those conflict for a plain
`int`. Resolve with an explicit three-state pointer, the standard Go optional-config shape
(`ServicesOptions.ProjectTrusted` is already `*bool`, `coding/services.go:79`):

- `ServicesOptions.MaxHistoryTokens == nil` → **default**: 80% of the active model's effective
  window.
- `*MaxHistoryTokens == 0` → **disabled** (no cap).
- `*MaxHistoryTokens == N > 0` → cap at N tokens.

CLI (serve only): absent → nil; `--max-history-tokens 0` → disabled; `--max-history-tokens N`
(N>0) → cap at N; negative → usage error. This keeps the sibling fields' "≤0 means special"
spirit while giving the item's two behaviors a home.

**Default scope (open question 1):** because the resolver lives in `Session` and the field
defaults in `Services`, the 80% backstop applies to every `NewServices` caller, not just
`serve` (`cmd/wopr/main.go:454`, `cmd/wopr/print_mode_test.go`, host tests). It never drops
data (entries are intact; compaction still summarizes), so this is defensible, but if the
maintainer wants serve-only behavior the change must instead default `nil` to "off" and have
serve opt in. **Record this on ship; do not silently widen.**

## 4. Default derivation

Add to `coding/session_efficiency.go` next to `effectiveWindow` (`:478`). Take the resolved
model as an argument — the caller passes the post-routing local, so there is no
pre-route lookup:

```go
// maxHistoryTokensFor is the per-request context budget for model: the explicit
// override, or 80% of its effective window. 0 means no cap.
func (s *Session) maxHistoryTokensFor(model *ai.Model) int {
	if s.services != nil && s.services.maxHistoryTokens != nil {
		return *s.services.maxHistoryTokens
	}
	if model == nil {
		return 0
	}
	window := s.effectiveWindow(model) // session_efficiency.go:478; learner-adjusted
	if window <= 0 {
		return 0
	}
	return window * 4 / 5 // 80%, integer
}
```

Use `effectiveWindow` (not raw `Capabilities.ContextWindow`) so the cap tracks the same
learner-adjusted window compaction uses. Model with no window (`<= 0`) → no cap.

Apply at the end of `prepareRequest` (`coding/session_transcript.go:125-143`), **replacing**
the `Context` field of the returned update (do not add a second return — that would be
unreachable and fail `go vet`):

```go
return &agent.AgentRequestUpdate{
	Context:       compaction.TrimMessages(projected, s.maxHistoryTokensFor(model)),
	Model:         model,
	ThinkingLevel: &thinking,
}
```

`compaction` is already imported there (`coding/session_transcript.go:10`). Trim after the
accounting calls (`efficiencyRecordRequest`, `learnRequest`) so those keep seeing the untrimmed
projection, and use the post-`ensureRoute` `model`.

## 5. Trim algorithm (`compaction.TrimMessages`)

New file `internal/codingagent/compaction/trim.go`. Reuse the package's own estimator and its
tool-pairing invariant; do not duplicate either.

```go
// TrimMessages keeps the leading system messages and the newest messages whose
// estimated tokens fit maxTokens; it drops oldest non-system messages. The kept
// suffix always starts at a cut point, so no tool result is separated from its
// tool call. maxTokens <= 0 or an already-fitting list is returned unchanged.
func TrimMessages(messages []agent.AgentMessage, maxTokens int) []agent.AgentMessage
```

Algorithm (implement exactly; `isCutPointMessage` is `compaction/compaction.go:268`,
`EstimateTokens`/`EstimateMessagesTokens` are `compaction/estimate.go:13,22`):

1. If `maxTokens <= 0` or `EstimateMessagesTokens(messages) <= maxTokens`, return `messages`.
2. `sys := 0`; while `sys < len && messages[sys].System != nil` { `sys++` } (the instruction
   baseline is always leading in the projection). `prefixTokens := EstimateMessagesTokens(messages[:sys])`.
3. If `prefixTokens >= maxTokens`, return `messages[:sys]` (budget below the baseline; keep it).
4. Walk `i` from `len-1` down to `sys`, accumulating `EstimateTokens`; stop before the first
   message that would exceed `maxTokens-prefixTokens`. `start` = earliest index that fits
   (initialise `start = len(messages)`; if nothing fits it stays `len`).
5. Never start on a tool result: while `start < len(messages) && !isCutPointMessage(messages[start])`
   { `start++` }.
6. Backward fallback when step 4 found nothing (over-budget trailing turn). Scan for the last
   cut point before the end:
   ```go
   if start >= len(messages) {
       start = -1
       for j := len(messages) - 1; j > sys; j-- {
           if isCutPointMessage(messages[j]) { start = j; break }
       }
       if start < 0 { return messages } // no preceding cut point: never orphan a tool result
   }
   ```
   This keeps the final turn and its tool pair even when it alone exceeds the budget.
7. If `start <= sys`, return `messages` (nothing safely trimmable).
8. Return `append(slices.Clone(messages[:sys]), messages[start:]...)`.

The result honors the budget whenever the kept suffix fits; step 6 is the documented best-effort
fallback for a single over-budget turn. The gate test must size its messages so step 6 is not
what makes `tokens <= budget` hold.

## 6. CLI plumbing (`cmd/wopr/serve.go`)

Mirror the `--learn-*` flags (`cmd/wopr/serve.go:20,38-45,82-100,141-147`):

- Extend `serveUsage` (`:20`) with `[--max-history-tokens N]`.
- Add `"--max-history-tokens": &maxHistoryVal` to `stringFlags` (`:39-45`).
- Parse (`:82-100` pattern): `strconv.Atoi`; reject negative with a usage error
  ("requires a non-negative integer (0 disables)"); `0` → disabled, `N>0` → cap at N;
  absent → `nil`.
- Pass `MaxHistoryTokens: maxHistoryTokens` in the `coding.NewServices` call (`:141-147`).

No package helper: take the address of a local, the file's own precedent at
`cmd/wopr/serve.go:144` (`func() *bool { b := true; return &b }()`). Use the same two-line
local: `v := n; ... &v`.

## 7. The gate test

`coding/session_history_limit_test.go`, package `coding`, ONE test:
`TestMaxHistoryTokensTrimsOldest`. The file defines a local helper
`func intPtr(v int) *int { return &v }` (none exists in the package).

Fixture (shared by both sub-cases):

- Build services with an explicit budget:
  `NewServices(ServicesOptions{CWD: t.TempDir(), MaxHistoryTokens: intPtr(60)})`.
- `sess, _ := NewSession(svcs, SessionOptions{Model: fakeModel(), SystemPrompt: "you are a test"})`
  (fixtures at `coding/session_test.go:71-91`). The `SystemPrompt` makes the §5 step-2/3
  baseline branch live, so the "baseline kept" assertion is real.
- Append messages via `sess.inner.AppendMessage(...)` (pattern `coding/session_test.go:154-157`),
  then `sess.RefreshContext()`.

Sub-case A — budget holds (the item's gate): append several LARGE oldest messages
(`strings.Repeat("x", 4000)`, ~1000 tok each) followed by a SMALL recent tail (one short user
message, one short assistant tool-call, its short tool result) whose combined tokens fit 60.
Call `update := sess.prepareRequest(context.Background(), agent.PrepareRequestContext{})` and
take `update.Context`. Assert: `EstimateMessagesTokens(update.Context) <= 60`; the oldest large
content is absent; the first non-system message is not a tool result (pairing preserved); the
leading system message is present.

Sub-case B — over-budget tail (same test function, second scenario, fresh session): a recent
tail whose last turn alone exceeds 60. Assert the final assistant tool-call and its tool result
are both present (step 6 fallback) and the context does not begin with a tool result.

The test must fail if `TrimMessages` is removed; delete no other test.

Add one sentence to `BACKLOG.md:1817-1830` on ship: the apply point is the end of
`prepareRequest` (`Context` of the returned `AgentRequestUpdate`), not `prepareNextTurn` (a
`prepareNextTurn` return is overwritten by `prepareRequest`) and not `pruneRequestContext`
(whose result feeds routing/learner accounting); the cap bounds the request context, not
`inner.entries`; the 80% default applies to every `NewServices` caller and needs owner
ratification.
