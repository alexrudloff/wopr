# wopr-failover — advance the model on a quota/rate-limit failure instead of stranding the run

Spec for backlog item `wopr-failover`. Worktree: `/home/kc/dev/wopr/.worktrees/wave-failover` (base `main` @ `8e871c1`).

**Scope decision (Q1 resolved): routing-active only.** The ladder is `router.Next`'s chain from the configured `router.json` tiers plus the paid last resort, and it engages for the orchestrator when `routingActive()` (`session_router.go:313`) and for subagents when `subagentRoutingActive()` (`:319`) — i.e. only when a `*router.Decision` handle exists. When routing is off or the orchestrator is pinned there is **no ladder**: a provider limit error is a bounded terminal failure. An implicit always-on ladder would collide with `objectiveGate`'s no-silent-fallback rule (`session_router.go:343-360`) and needs configuration surface outside this item's `Files`; it is not built here.

## Files touched

1. `ai/assistant_retry.go` — add `providerRateLimitPattern` (the exact tokens currently at `coding/session_router.go:560`, moved unchanged) and one exported classifier `IsProviderLimitError`; add `free_tier_limit` to the non-retryable family. One definition (Design A). `IsRetryableAssistantError` (`:94-102`) logic is unchanged.
2. `ai/assistant_retry_test.go` — one small classifier test (the regression string, not a table).
3. `coding/session_router.go` — delete `rateLimitPattern` (`:560`) and the now-unused `regexp` import (`:10`); `routeFailover` builds its `router.Failure` through the new `providerFailure` choke point (`:578-583`).
4. `coding/session_task.go` — add `providerFailure(message, contextTokens, overflow)` and use it in `taskHost.Failover` (`:323`).
5. `coding/session_retry_classify_test.go` — one test on `providerFailure`: the call-site classifier wiring.
6. `internal/codingagent/subagent/task.go` — relax the failover gate at `:428` for read-only children (Design D).
7. `internal/codingagent/subagent/subagent_test.go` — two tests: a routed subagent advances on failover (red before #6); a no-ladder child terminates bounded.
8. `docs/routing.md` — `## Failover` (`:308`) updated; keep terse.

Not touched: `ai/provider_retry.go` (inert by default, correct to stay inert here), `internal/codingagent/router/router.go` (its `Next`/`markDown` ratchet is already correct), `docs/site/docs/routing.md` (the shipped bundle, a different file), `CHANGELOG.md` (no unreleased-line convention — only the `serve-5` wave added one; every other entry is a release commit).

## Gates to run

```bash
go build ./...
go vet ./...
go test ./coding/ ./internal/codingagent/router/ ./internal/codingagent/subagent/ ./ai/ -count=1
go test -race ./coding/ ./internal/codingagent/subagent/ ./ai/ -count=1   # touched packages with concurrency
go test ./tests/docs-drift/ -count=1
make lint   # == go tool golangci-lint run --allow-parallel-runners --build-tags=integration,live ./... (Makefile:93)
make check  # stops at test-prereqs: tmux is not installed (Makefile:110-111); run its constituents directly and report honestly
```

`go fix -diff ./...` must print nothing. Lint honestly: `make lint` and `lint-changed` (`Makefile:93,102`) add `--build-tags=integration,live`; a bare `go tool golangci-lint run` skips those tags and lints fewer files, so run it with the tags for parity. If golangci-lint reports phantom issues from `../wave-serve-*`, re-run with `GOLANGCI_LINT_CACHE=$(mktemp -d)`. `go test ./tests/docs-drift/` guards the shipped `docs/site/docs` bundle; this slice does not edit that bundle, so the gate should stay green and confirms nothing there broke.

## Definition of done

1. `ai.IsProviderLimitError` returns true for the canonical set the code must match — `free_tier_limit`, `FreeUsageLimitError`, `GoUsageLimitError`, `Monthly usage limit reached`, `insufficient_quota`, `quota exceeded`, `429`, `rate limit`, `too many requests`, `overloaded`, `capacity`, `529` — and false for `connection refused` and `` (empty). The test asserts the regression string plus at most one or two representatives; it is not a table over the whole set.
2. Both failover call sites classify through one `providerFailure` choke point whose `RateLimited` is `ai.IsProviderLimitError(message)`. The `coding` test asserts `providerFailure("free_tier_limit", …).RateLimited` true and `providerFailure("connection refused", …).RateLimited` false. `TestRateLimitRestsTheModel` (`router_test.go:216`) already pins that a `RateLimited` failure marks the failed spec down for `QuotaBackoffMinutes` (`router.go:1036-1040,1047-1049`), returns a different model, and is down at +10m; no new router test is added.
3. A routed read-only subagent (`subagentRoutingActive`, a `*router.Decision` handle) that completes a tool call then gets a `free_tier_limit` provider error calls `Host.Failover`, runs its next attempt on the replacement route, and returns an accepted result; the attempt log records `outcome:"failover"` for the failed attempt, not `"escalated"`.
4. **Non-park / bounded terminal failure.** When there is no ladder — routing off or the orchestrator pinned (`routeFailover` returns false at `session_router.go:566`), a single configured model, the chain exhausted (`session_router.go:599-600`), a child with no `Handle` (`session_task.go:319-321`), or no failover target — a provider limit error terminates the run/child with a bounded failure. It never loops and never hangs. This guarantee holds for every no-ladder case; test #4 pins only the subagent no-target path, while the orchestrator no-ladder (`routeFailover` false) and chain-exhausted paths are documented rather than tested because they are already terminal (pre-green) — no new behavior to prove.
5. `coding`, `subagent`, `ai`, and `router` tests pass `-race`; `go fix -diff ./...` is empty; `go test ./tests/docs-drift/ -count=1` passes.
6. `docs/routing.md` `## Failover` names the free-tier/quota family, states that such errors advance to the next configured candidate in the same turn and rest the failed model for `quotaBackoffMinutes`, and states the honest limit: with routing off or a single model there is no ladder and the error is terminal, not a hang.

## Out of scope

- Editing `~/.config/opencode/MODELS.md`, opencode persona pins, `opencode-failover`, or anything outside the worktree.
- New dependencies, router refactors, or changing routing decisions unrelated to failure advance.
- `ai/provider_retry.go` opt-in behaviour; mid-stream HTTP retry at the transport.
- Building an implicit failover ladder when routing is off or the orchestrator is pinned (Q1 decision above).
- The shipped `docs/site/docs` bundle and `CHANGELOG.md`.

## Open questions (all non-blocking)

1. **Q2 — Call-site cooldown seam.** The 60s-vs-`quotaBackoffMinutes` consequence of the classifier lives in `router.Next` and is not observable from the `coding` package: `Router.now` is unexported (`router.go:181`) and defaults to `time.Now` (`:217`), and the only time seam is `newTestRouter` inside the `router` package (`router_test.go:64-70`). This slice therefore pins the classifier at the choke point (DoD 2) and the cooldown contract with the existing `router` test; a future slice wanting an end-to-end `taskHost.Failover` cooldown assertion would need a small exported clock seam on `Router`.
2. **Q3 — Mid-task failover for writer (`TypeGeneral`) children.** Keep writers at `ToolCalls == 0`; a writer that may already have changed files is never rerun past a provider error (comment and guard at `task.go:452-454`). Chosen default; revisit only on evidence of stranded writers.
3. **Q4 — Session-level same-turn test.** `routeModel` rejects `test-faux/*` as an unknown model (`session_router.go:436-439`) and no test writes `router.json`, so the subagent test is the behavioral proof; a session-level test needs a registry/model-definition seam — a separate slice.

## Root-cause analysis (verified)

There are two divergent classifiers for "this failure is a quota/rate limit", and they disagree on the free-tier strings.

- `ai/assistant_retry.go:12-24` `nonRetryableProviderLimitErrorPattern` — decided *same-model retryability*. Contains `GoUsageLimitError`, `FreeUsageLimitError`, `Monthly usage limit reached`, `available balance`, `insufficient_quota`, `out of budget`, `quota exceeded`, `billing`.
- `ai/assistant_retry.go:26-72` `retryableProviderErrorPattern` — transient retryable (`429`, `rate.?limit`, `overloaded`, 5xx, network).
- `coding/session_router.go:560` `rateLimitPattern` — decided *rest the model and advance*.

`free_tier_limit` (underscore, lowercase) matches **none** of the three patterns. Consequences, each cited:

- **Rest duration / ratchet broken.** `routeFailover` sets `RateLimited` from `rateLimitPattern` (`session_router.go:581`); the subagent path does the same (`session_task.go:323`). `router.Next` uses `RateLimited` only to choose the cooldown: 60s default vs `QuotaBackoffMinutes` (default 15m) at `router.go:1036-1040`, then `markDown` (`:1047-1049`). So a free-tier refusal rests the rung for only 60s and the ratchet can re-pick it a minute later. This is the confirmed ratchet defect.
- **Subagent does not advance after tool work.** `task.go:428` gates failover on `out.ToolCalls == 0`. A rate limit that arrives after the child has made any read call sets `ProviderError` (via `finalText`, `runner.go:332-346`) but skips failover; `EscalationTrigger` then returns `"provider error: …"` (`task.go:557-560`) and the attempt loop breaks. When an escalation target exists the child escalates to it — pre-fix this is `fake/strong` and the log records `"escalated"`, per test #3 — and only with no escalation target does it terminate as a bounded failed result. Either way it does not advance to a failover route. This is the concrete "does not advance" bug and the most likely cause of a stranded dispatch in the outer harness.
- **Orchestrator under auto already advances, but on the wrong cooldown.** `handlePostAgentRun` tries `routeFailover` *before* the retry/classifier branch (`session_recovery.go:259`) and `routeFailover` does not consult the classifier — it calls `router.Next` unconditionally once routing is active and the failed message matches the active model (`session_router.go:565-601`). So a `free_tier_limit` on an auto-routed orchestrator does advance today; only the 60s-vs-15m cooldown is wrong. Any test that asserts "auto orchestrator advances" is therefore **already green before the fix** and is not a red proof.
- **`Next`'s ratchet topology is already correct.** `Next` marks the failed spec down, and its `pick` skips `d.Spec()` and `isDown` keys (`router.go:1066-1085`); `isDown` is consulted across calls (`:914-926`). No change needed there; the fix is feeding `RateLimited` correctly.
- **Paid terminal.** `Next` exhausts `d.chain`, then widens via `r.candidates(tokens, r.cfg.PaidLastResort, current)` (`router.go:1086-1103`). `PaidLastResort` defaults true (`config.go:220`, written by `/setup`), so a configured ladder always has a paid landing. If `paidLastResort` is false, or only one model fits, `Next` returns nil — a terminal error, not a park.
- **`ai/provider_retry.go` is not the seam.** `retryTransport.RoundTrip` is inert unless `retry.provider.maxRetries > 0` (default 0, `:20-22,155-157`), and it retries only the RoundTrip boundary (headers), never a mid-stream error (`:147-150`). It also re-sends the same request, which cannot move to another model. Leave it.

**Most likely root cause:** the free-tier/quota error family is split across two classifiers and `free_tier_limit` falls through both, so (a) the refused rung rests only 60s and (b) a subagent that rate-limits after a tool call fails instead of advancing. Fix the vocabulary once in `ai`, feed it to both call sites, and relax the subagent gate for read-only children.

## Design

**A. One classifier (`ai/assistant_retry.go`).** Add `providerRateLimitPattern` = exactly the alternation branches currently in `coding/session_router.go:560`, moved unchanged and built with `buildProviderErrorPattern` (case-insensitive). `buildProviderErrorPattern` joins tokens with `|` (`assistant_retry.go:8-10`), so the numeric branches must be passed already anchored — `\b429\b` and `\b529\b`, not bare `429`/`529` — or they become substring matches and widen past the current `\b429\b`/`\b529\b`. The token list is (`\b429\b`, `rate[ _-]?limit`, `quota`, `usage limit`, `too many requests`, `overloaded`, `capacity`, `\b529\b`). Define `IsProviderLimitError(message string) bool` as: false when `message == ""`; otherwise `nonRetryableProviderLimitErrorPattern.MatchString(message) || providerRateLimitPattern.MatchString(message)`. Add `free_tier_limit` to `nonRetryableProviderLimitErrorPattern` (`:12-24`) so a quota error is explicitly never retried same-model (today it is non-retryable only by failing to match either pattern). Do **not** add `429`/`rate limit` to the non-retryable family: that would make transient rate limits non-retryable same-model and change `IsRetryableAssistantError` semantics. State that behavioral reason; do not cite `assistant_retry_test.go:39-41`, whose `"429 quota exceeded"` already matches `quota exceeded` and therefore passes either way.

**B. Single choke point.** Add `providerFailure(message string, contextTokens int, overflow bool) router.Failure` in `coding/session_task.go`; it sets `RateLimited: ai.IsProviderLimitError(message)`. Use it in `routeFailover` (replace `session_router.go:578-583`, keeping `ai.IsContextOverflow(...)` for `Overflow`) and in `taskHost.Failover` (`session_task.go:323`, `overflow=false`). Delete `rateLimitPattern` and the `regexp` import. `coding` already imports `ai`. Every failover decision now asks the same classifier, and the classification has one testable home.

**C. Same-turn advance.** Unchanged mechanism; document it. `runPostAgentRunsWith` (`session_recovery.go:228-246`) calls `handlePostAgentRun`; a true `routeFailover` returns `continueRun=true`, the loop resumes with `s.agent.Continue(ctx)` (`:221-223`), and `ensureRoute` returns the stored replacement (`session_router.go:459-465`). `routeFailover` emits a `RouteEvent` with `Fallback=true` (`emitRoute`, `:546-558,597`) and omits the failed attempt (`omitRecoveryAttempt`, `:591`). When the ladder is exhausted, `Next` returns nil, `routeFailover` returns false, and the run ends on the terminal error — never a loop.

**D. Subagent advance (`task.go:428`).** Change the gate to:
`out.ProviderError != "" && ctx.Err() == nil && failovers < maxFailovers && (out.ToolCalls == 0 || req.Type != TypeGeneral)`.
Invariants: a writer (`TypeGeneral`) child that may already have changed files is never rerun past a provider error (mirrors `task.go:454`); a read-only/explore child advances even after tool calls. On no failover target the loop falls through and returns a bounded failed result — never a loop, never a hang. Cap stays `maxFailovers = 3` (`task.go:242`).

**E. Ladder inputs.** The ladder is `router.Next`'s chain — the ranked candidates from the configured `router.json` tiers (cheapest first, `config.go:161`) widened to `candidates(..., PaidLastResort, ...)`. It is **not** the `~/.config/opencode/MODELS.md` ladder; wopr does not read that file. `paidLastResort` (default true) provides the terminal; `quotaBackoffMinutes` (default 15) is the ratchet rest. Per the Q1 decision, this ladder exists only while routing is active and a `*router.Decision` handle is present; routing off, a pinned orchestrator, or a single configured model means no ladder, and the quota error is terminal (not a hang).

## Test design (hermetic: no network, no real creds, no `~/.wopr`)

1. **Classifier (red: `IsProviderLimitError` does not exist).** `ai/assistant_retry_test.go`: assert true for `free_tier_limit` and one or two representatives (`FreeUsageLimitError`, `GoUsageLimitError`), false for `connection refused` and `""`. Not a table over the whole DoD 1 set.
2. **Classifier choke-point expression (red: `providerFailure` does not exist).** `coding/session_retry_classify_test.go`: assert `providerFailure("free_tier_limit", 100, false).RateLimited` is true and `providerFailure("connection refused", 100, false).RateLimited` is false, plus `Message`/`ContextTokens`/`Overflow` pass through. This pins the classifier expression at the choke point only. It does **not** observe call-site wiring: `providerFailure` is a pure function called directly by the test, so an implementer can add it and leave a call site on `rateLimitPattern` and this test still passes. Call-site wiring is a **verification obligation**, not a unit test — (i) deleting `rateLimitPattern` and the `regexp` import makes any leftover reference at a retained site fail to compile, and (ii) a verification-time diff check confirms both `session_router.go` and `session_task.go` route through `providerFailure`. No unit test can observe the wiring because `RateLimited`'s only external effect is the `router.Next` cooldown and `Router.now` is unexported (`router.go:181`); see Open Q2.
3. **Subagent advance.** `internal/codingagent/subagent/subagent_test.go`, extending `fakeHost` with a one-shot failover target in `Failover` (`:180`) returning `fake/failover`; the `scripted` provider scripts by call order (`:117-145`). Replies: `toolCall("grep")`, then `aitest.Response{StopReason:"error", ErrorMessage:"free_tier_limit"}`, then `answer(goodAnswer)`. Assert final `d.Spec == "fake/failover"`, `d.State == StatusDone`, `d.ToolCalls == 1`, `host.escalated == 0`, and `records[0].Outcome == "failover"` with `records[0].EscalatedTo == "fake/failover"`. **Red assertion:** pre-#6 the failover branch is skipped (`out.ToolCalls != 0`), so the result is **not** failed — it escalates to `fake/strong` and `records[0].Outcome == "escalated"`. The genuinely red assertions are `Outcome == "failover"` and final spec `fake/failover`.
4. **No-ladder bounded failure (guard).** Same package: a `fakeHost` whose `Failover` and `Escalate` both return false. Script a tool call then a provider error; assert `d.State == StatusFailed`, the attempt count is bounded (≤ `maxAttempts+maxFailovers`), and `runTask` returns without looping or hanging. Pins Design D's terminal-failure invariant.

The evidence rules in `ai/AGENTS.md` ("add a test only when a regression would cost money, leak a credential, or lose data") and `coding/AGENTS.md` ("do not pin exact event sequences") are satisfied: these assert state/models, not event sequences, and the regression is a stranded paid dispatch.

## Doc change (`docs/routing.md` `## Failover`)

Replace the paragraph at `:308-315` with the same shape plus: the free-tier/quota family is recognized by one classifier; such an error advances to the next configured candidate **in the same turn** and rests the failed model for `quotaBackoffMinutes` so it is not immediately re-picked; the ladder is the configured tier chain plus the paid last resort; with routing off or a single model there is no ladder and the error is terminal (not a hang).
