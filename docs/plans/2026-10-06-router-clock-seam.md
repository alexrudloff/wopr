# router-clock-seam — make the failover cooldown assertable end-to-end

Spec for backlog item `router-clock-seam` (BACKLOG.md:1000). Worktree `/home/kc/dev/wopr/.worktrees/wave-router-clock-seam` (base `main` @ `e1f4ac2`). Architect only: no product code in this document.

## At a glance

- **Files touched:** `internal/codingagent/router/router.go` (add exported `SetClockForTest`, ~8 lines, no behavior change); `coding/session_router_failover_test.go` (append one test + `time`/`router` imports).
- **Seam:** `func (r *Router) SetClockForTest(now func() time.Time)` — nil restores `time.Now`; mutex-guarded; documented "call before the router serves a request". `New` still defaults `now: time.Now` (`router.go:217`).
- **Test:** harness with explicit `quotaBackoffMinutes: 15`; install fake clock on `h.session.router` before `Send`; `Send("Trigger: free tier limit")` → routed failover to `faux-2`; assert the post-failover `route.Load().decision` is `faux-2`; advance fake clock +61s; `Next(decision, router.Failure{})`; assert `faux-1` is **not** returned (correct: `nil`; bug 60s: `faux-1`).
- **Gates:** `go build ./...`; `go vet ./...`; `go test ./coding/ ./internal/codingagent/router/ -count=1`; `-race` same; new test `-count=3`; `go test ./... -count=1`; `go test ./tests/docs-drift/ -count=1`; `go tool golangci-lint run --allow-parallel-runners --build-tags=integration,live ./...`; `go fix -diff ./...` (empty). Plus the mutation-red check below.
- **Done:** seam present, default unchanged, `router_test.go` untouched and green; new test RED before the seam (compile error) and GREEN after; mutation (`Next`'s RateLimited cooldown, `router.go`, → `time.Minute`) makes it FAIL, revert makes it pass (verbatim output recorded); all gates clean; no format/sequence/golden/table assertions.
- **Out of scope:** any `Next` cooldown behavior change; `providerFailure`/`routeFailover`/`taskHost.Failover`/classifier edits; `down`-health accessors or `Targets()`/`Status()` changes; `Host`- or `Config`-based clock; docs/CHANGELOG/NOTICE.
- **Open questions:** (1) seam shape — decided `SetClockForTest`; BLOCKED alternative is WONTFIX, contract already pinned at `router_test.go:216-232`; (2) test file — recommend `session_router_failover_test.go` over the item's named `session_retry_classify_test.go`; (3) RED method — compile-red + mutation-red, since no behavioral red exists for a test-only seam; (4) `quotaBackoffMinutes` — explicit 15.

---

## Why a seam is unavoidable

The `coding` package cannot observe the cooldown: `Router.now` is unexported (`internal/codingagent/router/router.go:181`, default at `:217`), and the only clock injection is a direct in-package field assignment (`router_test.go:186,219`; `sticky_test.go:19`) reachable only from package `router`. `Router` exposes no down-state query; `Targets()` (`router.go:824`) is per-provider and format-coupled, and `faux-1`/`faux-2` share provider `test-faux`, so it cannot distinguish them (and AGENTS.md forbids label/format assertions). `Status()` (`:1175`) is text. The cooldown's only observable effect is which model `Next` offers, and the 60s-vs-`quotaBackoffMinutes` difference is only visible across time — so the test must control the clock.

The value is real but narrow: `router_test.go:216-232` already pins the in-package contract (opus still down at +10min vs both the 60s default and the 15min fallback), and `session_retry_classify_test.go:33-47` pins that `providerFailure` sets `RateLimited` for `free_tier_limit`. What is **not** pinned is the composition at the session call site: that `routeFailover`'s `providerFailure` result actually reaches `Next`'s `RateLimited` branch and sets a `quotaBackoffMinutes` cooldown rather than the generic 60s. A regression that dropped the classification on the way to `Next` would slip past both existing tests. The item is explicitly optional; see Open Q1.

## Alternatives evaluated

- **`SetClockForTest` method (chosen).** Smallest reachable seam: the `coding` test holds `*router.Router` (`Session.router`, `session.go:49`) and can call a method. Matches the existing exported-setter idiom (`SetMode` `router.go:277`, `Pin` `:320`, `Adopt` `:313`, `SetQuotaBalance` `quota.go:50`). 8 lines, no behavior change, no JSON surface. Cost: one exported API with no production caller.
- **Constructor option / `Config` field.** Rejected: `Session` builds its own router from `router.json` (`session_router.go:106-113`), so the test cannot inject a clock through `Config` without a larger plumbing change through `NewServices`/`NewSession`. Bigger blast radius for the same result.
- **`Host.Now()`.** Rejected: forces a new method on every `Host` implementation and changes an existing consumer interface; `New` already owns `now`, so the clock belongs on `Router`, not `Host`.
- **Reflection/unsafe.** Rejected: not a seam, brittle, bypasses the type system.
- **BLOCKED (WONTFIX).** Defensible: the cooldown contract and the classifier are each pinned in-package, so the item is optional. Left as the fallback if the fan rejects an exported test-only setter.

Seam:

```go
// SetClockForTest replaces the router's clock. Test-only. It exists so callers
// outside this package can control time in tests; production uses time.Now.
// A nil function restores time.Now. The mutex does not by itself make
// concurrent use safe: r.now is also read without r.mu (basic.go:170,
// brief.go:183, fit.go:56), so the contract is "call before the router serves
// a request" — the test installs it before Send, which is synchronous.
func (r *Router) SetClockForTest(now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	r.mu.Lock()
	r.now = now
	r.mu.Unlock()
}
```

Placement: `internal/codingagent/router/router.go`, immediately after `New` (ends `:227`), so the clock default and its setter read together. Invariant: `r.now` is also read without `r.mu` (e.g. `basic.go:170`, `brief.go:183`, `fit.go:56`), so the setter's lock does not by itself make concurrent calls safe — the contract is "call before serving requests", which holds here because the test installs it before `Send` and `Send` is synchronous.

## Test design

**File/function:** `coding/session_router_failover_test.go`, `TestSessionFailoverRestsTheFailedModelForQuotaBackoff` (append to the file that already holds `TestSessionFailoverAdvancesToTheNextRoutedModel`, `:15-61`). Rationale for diverging from the item's named `coding/session_retry_classify_test.go`: the new test is a session-router failover test using the same harness, trigger, and JSON as the sibling, and reuses that file's imports; the classify file is classifier-only. This divergence is load-bearing: the item's `Files:` line still names `coding/session_retry_classify_test.go`, and the tracker update must reconcile it to the shipped `coding/session_router_failover_test.go`. Flag the divergence in the commit/report.

```go
func TestSessionFailoverRestsTheFailedModelForQuotaBackoff(t *testing.T) {
	t.Setenv("WOPR_TEST_FAUX", "1")
	h := newRecoveryHarness(t, harnessOptions{router: `{
  "enabled": true,
  "quotaBackoffMinutes": 15,
  "tiers": [
    {"name": "test", "cost": "free-local", "capability": 0.5,
     "models": [
       {"provider": "test-faux", "model": "faux-1"},
       {"provider": "test-faux", "model": "faux-2"}
     ]}
  ]
}`})

	clock := time.Now()
	h.session.router.SetClockForTest(func() time.Time { return clock })

	if _, err := h.session.Send(context.Background(), "Trigger: free tier limit"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.settle(t)

	rs := h.session.route.Load()
	if rs == nil || rs.decision == nil || rs.decision.Model != "faux-2" {
		t.Fatalf("after failover route = %+v, want the fallback decision on faux-2", rs)
	}
	if !rs.decision.At.Equal(clock) {
		t.Fatalf("SetClockForTest not in effect: decision At=%v clock=%v", rs.decision.At, clock)
	}

	// 61s is past the generic 60s cooldown, well inside quotaBackoffMinutes (15m).
	clock = clock.Add(61 * time.Second)
	next := h.session.router.Next(rs.decision, router.Failure{Message: "cooldown probe"})
	if next != nil && next.Model == "faux-1" {
		t.Fatalf("faux-1 re-offered after 61s: rested 60s, not quotaBackoffMinutes (15m)")
	}
}
```

Imports: add `"time"` and `"github.com/alexrudloff/wopr/internal/codingagent/router"` to the file (it currently imports `context`, `testing`, `agent`).

**Why it is hermetic and deterministic.** `WOPR_TEST_FAUX=1` registers `test-faux/faux-1|faux-2` in the process registry (shipped `session-failover-registry-seam`, `b8e5894`), so `routeModel` accepts them and `BuildModel` returns the scripted faux provider. The harness sets `WOPR_HOME` to a temp dir and writes `router.json` (`session_recovery_test.go:97-114`); credentials are cleared process-wide by `coding/testmain_test.go`. No network, no `~/.wopr`.

**Why the failover happens.** `ensureRoute` (`session_router.go:458-487`) decides `faux-1` (config order: `allCandidates` walks `cfg.Tiers` in order, `router.go:766-772`; `basicOrder` stable-sorts equal-capability candidates, `basic.go:82-105`; `basicDecision` chains the rest, `basic.go:147-175`). The first stream fails with `free_tier_limit` (`ai/test_faux.go:156-158`); `handlePostAgentRun` calls `routeFailover` (`session_recovery.go:259`); the message matches the route (`session_router.go:570`); `providerFailure` sets `RateLimited` (`session_task.go:319-326`, classifier `ai.IsProviderLimitError`, `ai/assistant_retry.go:20,49`); `Next(rs.decision, failure)` marks `test-faux/faux-1` down for `QuotaBackoffMinutes` and returns `faux-2` (`router.go:1030-1049`); `routeFailover` stores the replacement (`session_router.go:586-588`).

**Why the probe distinguishes the contracts.** After failover, `rs.decision` is the `faux-2` decision and its `chain` is empty (`Next` sets `out.chain = remaining[pos+1:]`, `router.go:1137-1139`; the initial chain was `[faux-2]`). At +61s, `Next(faux2, Failure{Message:"cooldown probe"})` marks `faux-2` down for the generic 60s, finds the empty chain, and widens via `r.candidates(tokens, r.cfg.PaidLastResort, current)` (`router.go:1091`, which wraps `allCandidates`, `:766-786`); `tierCandidates` drops `isDown` models as it builds the pool (`:887`), so `pick`'s re-check at `:1068` is redundant on this path. The pool therefore excludes `faux-2` (just marked down) and any still-resting model, leaving only `faux-1` when it is not down. If `faux-1` rested `quotaBackoffMinutes` (15m) it is still down → `Next` returns nil. If it rested only 60s, `isDown` (`:914-926`) deletes the expired entry → `faux-1` is returned → the test fails. The `rs.decision.Model != "faux-2"` guard prevents a vacuous pass if failover never ran; the `At.Equal(clock)` guard prevents a vacuous pass if `SetClockForTest` were a no-op.

## Red-before-green and the mutation check

No behavioral red exists: the seam changes no behavior, so a test written against the unseamed tree cannot produce a behavioral failure — with no clock control, `faux-1` is down at +61s under both contracts, so the assertion would pass on buggy code too. Two honest checks, both required:

1. **Compile-red (red before green).** Append the test first, before adding `SetClockForTest`. `go test ./coding/ -run TestSessionFailoverRestsTheFailedModelForQuotaBackoff -count=1` fails to build with `h.session.router.SetClockForTest undefined`. Paste the verbatim output. Add the seam; the same command passes.
2. **Mutation-red (proves the assertion is load-bearing).** Temporarily change the RateLimited branch of `Next` in `router.go` from `cooldown = time.Duration(r.cfg.QuotaBackoffMinutes) * time.Minute` to `cooldown = time.Minute`; run the test; it must fail with `faux-1 re-offered after 61s`. Revert the line exactly; the test passes. Paste both outputs. The mutation is not left in the tree — the final `git diff` must show only the seam and the test.

If the wave requires a behavioral red strictly before the production change, this item cannot satisfy it; say so and take Open Q1's BLOCKED path.

## Gates

```bash
go build ./...
go vet ./...
go test ./coding/ ./internal/codingagent/router/ -count=1
go test -race ./coding/ ./internal/codingagent/router/ -count=1
go test ./coding/ -run TestSessionFailoverRestsTheFailedModelForQuotaBackoff -count=3
go test ./... -count=1
go test ./tests/docs-drift/ -count=1
go tool golangci-lint run --allow-parallel-runners --build-tags=integration,live ./...
go fix -diff ./...   # must print nothing
```

`make check` stops at `test-prereqs` because tmux is absent (`Makefile:110-118`); run its runnable constituents directly (grouped tests via `./automation/ci/test-grouped.sh`, `go test ./tests/docs-drift/`, lint, vet) and report honestly. `-race` matters because the fake clock is read on the session's run goroutine and written on the test goroutine; the write happens after `Send` returns, so the ordering is safe, and the run confirms it.

## Definition of done

- `SetClockForTest` exists as specified; `New` still sets `now: time.Now`; `router_test.go` is unchanged and green.
- The new test is RED before the seam (compile error) and GREEN after, and passes `-count=3`.
- The mutation check fails then passes on revert, with verbatim output recorded; the final tree contains no mutation.
- All gates above are clean; the test asserts state/models only (no `Status()`/`Targets()` text, no event sequence, no golden/table test).
- `go test ./... -count=1` is clean (the seam has no env-gated blast radius; this is confirmation, not a blast-radius requirement).

## Explicitly out of scope

- Changing cooldown behavior in `Next` (`router.go:1030-1049`) or `providerFailure`.
- Editing `routeFailover`, `taskHost.Failover`, the classifier, or `session_retry_classify_test.go`.
- New accessors for `down`/health, or changes to `Targets()`/`Status()`.
- A `Host`-based or constructor/`Config`-based clock.
- `session-failover-registry-seam` (already shipped), docs/site bundle, CHANGELOG, NOTICE.

## Open questions

1. **Seam shape (decided: `SetClockForTest`).** Follows the repo's exported-setter idiom, smallest reachable injection. The only honest alternative is BLOCKED/WONTFIX: the cooldown contract is already pinned at `router_test.go:216-232` and the item is optional. Recommend proceeding; if a reviewer rejects an exported test-only setter, close WONTFIX rather than widen plumbing.
2. **Test file (decided: `session_router_failover_test.go`).** More cohesive than the item's named `coding/session_retry_classify_test.go`; differs from the item's `Files:` line, which must be reconciled to the shipped file during tracker updates, and the divergence flagged.
3. **RED method (decided: compile-red + mutation-red).** No behavioral red exists for a behavior-neutral seam; this is the strongest available. Flag if the wave insists on behavioral-only.
4. **`quotaBackoffMinutes` value (decided: explicit 15).** 15 matches the default and proves 15m ≠ 60s. A non-default value (e.g. 30) would additionally prove the config value is read rather than the 15m fallback; the test stays correct either way, and 15 keeps the fixture aligned with the item.

## Verification notes for the fan

- Confirm `SetClockForTest` is the only production change and that `New`'s default is untouched (`router.go:217`).
- Confirm the test file adds no format/sequence assertion and that the `rs.decision.Model != "faux-2"` and `At.Equal(clock)` guards are present (without them the test can pass without proving the contract).
- Confirm the mutation was reverted: `git diff` shows only `router.go`'s `SetClockForTest` and `session_router_failover_test.go`.
- Confirm `go test ./coding/ -run TestSessionFailoverRestsTheFailedModelForQuotaBackoff -count=3` passes.
