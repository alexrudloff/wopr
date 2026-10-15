# session-failover-registry-seam — register a faux routed model so session failover is testable

Spec for backlog item `session-failover-registry-seam` (BACKLOG.md:953). Worktree: `/home/kc/dev/wopr/.worktrees/wave-session-failover-registry-seam` (base `main` @ `3b1b73b`). Architect only: no product code in this document.

## Files touched

1. `coding/services.go` — **the seam (option A).** When `WOPR_TEST_FAUX=1`, register the test-faux provider's two model ids in the process registry so `routeModel` sees them through `HasModelDefinition` (the same dynamic-provider path llama.cpp uses). ~4 lines + one import (`internal/codingagent/llama`).
2. `ai/test_faux.go` — one scripted trigger `"Trigger: free tier limit"`: fail the first call with `free_tier_limit`, answer the next. Keyed on `options.SessionID` so it repeats under `-count=3`. Mirrors `"Trigger: retryable provider error"` (`:147-155`).
3. `coding/session_recovery_test.go` — `harnessOptions` gains `router string` (written to `agentDir/router.json` before `NewServices`); the harness assigns each instance a unique `SessionOptions.SessionID`.
4. `coding/session_router_failover_test.go` — **new.** `TestSessionFailoverAdvancesToTheNextRoutedModel`.

## Gates to run

```bash
go build ./...
go vet ./...
go test ./coding/ ./ai/ ./internal/codingagent/ -count=1          # touched packages
go test -race ./coding/ ./ai/ ./internal/codingagent/ -count=1
go test ./coding/ -run TestSessionFailoverAdvancesToTheNextRoutedModel -count=3   # repeatable trigger
go test ./... -count=1                                            # full suite: the seam activates under WOPR_TEST_FAUX=1 in other packages
go test ./tests/docs-drift/ -count=1                              # make check constituent
go tool golangci-lint run --allow-parallel-runners --build-tags=integration,live ./...
go fix -diff ./...                                                # must print nothing
```

`make check` stops at `test-prereqs` because tmux is not installed (`Makefile:110-118`, the `exit 1` at `:117`); its runnable constituents are the grouped tests (`./automation/ci/test-grouped.sh`), `go test ./tests/docs-drift/`, lint, and vet. Run those directly and report honestly. **Red-before-green runs first** (implementation order below).

## Definition of done

- `TestSessionFailoverAdvancesToTheNextRoutedModel` is **red** on the tree without item 1 (routeModel rejects `test-faux/faux-1`) and **green** with it, and passes `-count=3`.
- On green: the first `agent.RouteEvent` is `test-faux/faux-1` with `Fallback == false`; a later `agent.RouteEvent` is `test-faux/faux-2` with `Fallback == true`; the run's projection contains the second attempt's text `failover-ok`; the failed attempt was omitted (`len(entries("context_edit")) >= 1`); `Send` returns nil.
- `go build`, `go vet`, `go test ./... -count=1`, touched-package `-race`, `go test ./tests/docs-drift/ -count=1`, lint with the integration/live tags, and `go fix -diff` are all clean.
- No assertion pins an exact event sequence (repo policy, `coding/AGENTS.md`); assertions are on state/models/one fallback event.

## Explicitly out of scope

- Changing `routeModel`'s validation (option C′ rejected; see below). Real unknown models are still rejected.
- Router behavior, cooldown/classifier work (shipped in `wopr-failover`), or new tier logic.
- The `router-clock-seam` item (BACKLOG.md:938) and any cooldown assertion at the session call site.
- New dependencies, new exported APIs, docs/site bundle, CHANGELOG.

## Open questions

1. **Seam shape (decided).** Option A, registration in `NewServices` under `WOPR_TEST_FAUX=1`. C′ (env-gated exemption inside `routeModel`) is rejected as a test-only bypass in production validation.
2. **Blast radius.** Accepted and recorded below: under `WOPR_TEST_FAUX=1` the registration also flows through `getAllConfigured` into the model-list surface (`GetAll`, `RuntimeModels`, `ModelRuntime.GetModels`). Covered by the new full-suite gate; the fan should confirm no existing test regresses.
3. **`-count=3` repeatability.** Addressed by the session-scoped trigger key + per-harness session id below; the fan verifies the `-count=3` command.

---

## Why a production seam is unavoidable (the red-before-green constraint)

The wave requires the new session test to be **red before the seam and green after**, and the prompt fixes the red: "the test is unable to route a faux model at all (routeModel rejects it) before the seam." Any *test-side* registration (writing `models.json`, seeding `s.routeModels`, or calling `SetProvider` from the test) makes the test green on first run, so there is no red to demonstrate. A production change is therefore required. The item's name — `registry-seam` — points at making the registry recognise the faux provider.

### The three rejected/parked alternatives

**Option 1 — `models.json` written by the test (`test-faux` provider, two models), two tiers.** `routeModel` would accept it (`HasModelDefinition` true, `internal/codingagent/model_registry.go:636-646`) and `BuildModel` handles test-faux (`coding/model.go:161-166`). Zero production change — but the registration lives in the test, so it is green immediately and there is no red. Rejected on the gate, not on mechanics.

**Option 2 — seed the in-package cache `s.routeModels` after `NewSession`.** Fully test-only and green immediately; worse, it skips `BuildModel` and the definition check entirely. It tests neither the registry seam nor the real provider path. Rejected as cheating the intent.

**Option 3 — real local provider via `models.json` + `httptest` (e.g. `faux-router` with `baseUrl`).** Real provider path, hermetic, zero production change. But the test writes the registration itself, so again no red, and it does not exercise test-faux. Parked as a fallback *shape*, not chosen.

**Option 4 / C′ — weaken `routeModel` with a test-only bypass.** Rejected by the constraint "not a test-only bypass in production code."

### Chosen seam (option A): register the faux models in the registry

In `NewServices` (`coding/services.go:84-133`), right after `reg := icodingagent.NewModelRegistry(agentDir)` (`:114`):

```go
if os.Getenv("WOPR_TEST_FAUX") == "1" {
    reg.SetProvider("test-faux", llama.ProviderConfig{
        Models: []llama.ProviderModelConfig{{ID: "faux-1"}, {ID: "faux-2"}},
    })
}
```

Why this is the smallest correct seam:

- `HasModelDefinition` (`model_registry.go:636-646`) checks `r.dynamic[providerID].Models` first, so the two ids resolve as defined. This is exactly the path the built-in llama.cpp provider uses (`SetProvider`, `:668-681`; registered by `llama.Host`), i.e. the same code path a real dynamically-registered routed model uses — not a bypass.
- `routeModel` then flows unchanged into `BuildModel(spec, svcs)` (`session_router.go:440`), which returns the deterministic `TestFauxProvider` (`coding/model.go:161-166`); the env gate matches `BuildModel`'s own test-faux gate, so behaviour stays consistent whether the env is set or not.
- Real unknown models are untouched: `HasModelDefinition("acme","x")` is still false and `ai.LookupModelExact` still false, so `routeModel` still returns `unknown model: acme/x` (`session_router.go:435-438`).
- `modelRuntimeRequiresAuth("test-faux")` is already false (`coding/services.go:413-420`), so no credential is needed.

**`Files:` divergence (recorded).** The backlog item names `coding/session_router.go` (`routeModel`); option A edits `coding/services.go` and leaves `routeModel` untouched. Option A is the real dynamic-provider path (`SetProvider`, the same one llama.cpp uses); C′ — an env-gated exemption inside `routeModel` — is rejected because it is a test-only bypass in production validation.

### Blast radius (wider than `HasModelDefinition`)

The registration is not confined to `HasModelDefinition`/`Resolve`. Under `WOPR_TEST_FAUX=1`, `getAllConfigured` (`model_registry.go:824-865`; the dynamic loop at `:854-857`) feeds the model-list surface:

- `GetAll` (`model_registry.go:812`), also read for display names in the prompt panel (`internal/codingagent/prompt_panel.go:68`)
- `RuntimeModels` (`internal/codingagent/model_registry_runtime_models.go:17-26`), consumed by the model picker (`model_picker.go:23`) and by startup model selection (`cmd/wopr/startup_model.go:122` — benign, because `testFauxCLIModel` short-circuits `--model test-faux/*` first, `startup_model.go:149-151`)
- `ModelRuntime.GetModels` (`coding/services.go:211-216`, via `Registry().GetAll()`)

`RequestAuthRuntime` is **not** on this list: it is composed from a separate `ModelRegistry` built from `agentDir/models.json` + `ai.ListProviders()` (`internal/codingagent/request_auth_runtime.go:77-99,104-143`) and never consults the `Services` registry's in-process `dynamic` map, so `SetProvider` never reaches it.

So `test-faux/faux-1|faux-2` are advertised on the model-list surface while the env is set. `GetAvailable` still excludes them (no auth, so `hasConfiguredAuthLocked` is false, `:998-1014`; `GetAvailable` at `:874-898`). This is why the full-suite gate `go test ./... -count=1` in item 1 is required: the in-process setter is `internal/codingagent/serve/serve_test.go:151` (`t.Setenv`, then `NewServices` at `:155`) and `serve_dto_test.go:523,617`; the `./cmd/wopr/` path instead passes `WOPR_TEST_FAUX=1` to a spawned subprocess (`rpc_mode_test.go:196-199`, `exec.Command`), not `t.Setenv`. No `serve`/`cmd/wopr` test currently asserts a model list, so the surface change is expected to be inert there; the full-suite gate confirms it.

## Test design (hermetic)

**File/function:** `coding/session_router_failover_test.go`, `TestSessionFailoverAdvancesToTheNextRoutedModel`.

**Setup** — reuse `newRecoveryHarness` (`coding/session_recovery_test.go:88-131`) with two additions:

- `harnessOptions` gains `router string`; the harness writes it to `agentDir/router.json` (`os.WriteFile`, like `settings.json` at `:100`).
- The harness assigns a **unique session id per instance** so the trigger key is fresh under `-count=3`. Exact change in `coding/session_recovery_test.go`:

  ```go
  // package-level, next to harnessOptions
  var harnessSessionSeq atomic.Uint64

  // inside newRecoveryHarness, replacing the SessionOptions literal at :113
  session, err := NewSession(services, SessionOptions{
      Model: model, SkipBuiltinTools: true, Tools: opts.tools,
      SessionID: fmt.Sprintf("recovery-%d", harnessSessionSeq.Add(1)),
  })
  ```

  (adds `fmt` and `sync/atomic` imports; `SessionOptions.SessionID` is honoured at `coding/session.go:464-465`, the agent receives it at `agent/agent.go:868` via `agent.SetSessionID(inner.ID())` at `coding/session.go:494`, and it reaches the provider at `agent/agent_loop.go:404` `SessionID: a.opts.SessionID`.) None of the 8 existing `newRecoveryHarness` callers assert session id or path, so the change is inert for them.

- `t.Setenv("WOPR_TEST_FAUX", "1")`; the harness's own `WOPR_HOME`/`AgentDir` temp dirs. Credential env is already cleared process-wide by `coding/testmain_test.go:15-40`. No network, no `~/.wopr`.

**`router.json`** (Basic engine, no Jev endpoint, one tier, two candidates in order):

```json
{
  "enabled": true,
  "tiers": [
    {"name": "test", "cost": "free-local", "capability": 0.5,
     "models": [
       {"provider": "test-faux", "model": "faux-1"},
       {"provider": "test-faux", "model": "faux-2"}
     ]}
  ]
}
```

Determinism of "faux-1 first, faux-2 second": `Load` keeps config order and installs `auto = Enabled` (`config.go:268-323`, `router.go:204-227,247-251`); `decideBasic` builds `allCandidates` in config order (`router.go:766-772`), `basicOrder` stable-sorts by paid then capability — equal here — so the first candidate is `faux-1` and `basicDecision.chain` is `[faux-2]` (`basic.go:41-69,147-175`). The tier has no `probeUrl`, so `probe` returns true without network (`router.go:935-938`); `planOf` early-returns false for a non-subscription provider (`quota.go:67`), so `balanceQuota` is a no-op (`quota.go:149-157`).

**Drive:** `h.session.Send(ctx, "Trigger: free tier limit")`. `prepareRequest` calls `ensureRoute` while routing is active (`session_transcript.go:125-137`); `Send` sets the run boundary (`session_run_state.go:93-96`).

**Provider trigger** (item 2), next to the existing retryable trigger (`ai/test_faux.go:147-155`); session-scoped so `-count=3` gets a fresh key per iteration:

```go
if strings.Contains(lastText, "Trigger: free tier limit") {
    if _, loaded := testFauxRetryState.LoadOrStore("free-tier-limit:"+options.SessionID, true); !loaded {
        builder.fail(StopReasonError, errors.New("free_tier_limit"))
        return
    }
    builder.textDelta("failover-ok")
    builder.done(StopReasonStop, nil, "")
    return
}
```

`free_tier_limit` is recognised by `ai.IsProviderLimitError` (`ai/assistant_retry.go:20,49-55`). The failover itself does not depend on the classifier, but the gate names this error, so the trigger supplies it verbatim.

**Flow proved:** `ensureRoute` picks `test-faux/faux-1` and emits `RouteEvent{Fallback:false}` (`session_router.go:476-485,545-557`); the first stream fails; `handlePostAgentRun` calls `routeFailover` before any retry (`coding/session_recovery.go:259`); the message matches the active route (`session_router.go:570`; TestFauxProvider always reports `Provider="test-faux"`, `ModelID="faux-1"`, `ai/test_faux.go:120`, mapped to `agent.AssistantMessage.ModelID` by `agent/agent.go:1301` `ModelID: message.Model`); `providerFailure` sets `RateLimited` (`session_task.go:319-326`); `router.Next` returns `faux-2` (`router.go:1030-1146`); `routeModel` builds it (seam); `omitRecoveryAttempt` drops the failed attempt (`session_router.go:583-590`, `session_recovery.go:177-197`); the run resumes on `faux-2` and the second stream returns `failover-ok`.

**Assertions (state, not event sequence):**

- `Send` returns nil (also true in RED, so red-ness comes from the next three assertions; `Send`-nil still guards the failover path).
- Route events (collected from the harness's `events`): one with `Model == "faux-1"` and `Fallback == false`; one later with `Model == "faux-2"` and `Fallback == true`.
- `projectionContains(h, "failover-ok")`.
- `len(h.entries("context_edit")) >= 1` — the failed attempt was omitted, i.e. this is failover, not a same-model retry.

## Implementation order (red before green — pinned)

1. Write item 4 (the test), item 3 (`router` field + unique session id), and item 2 (the `ai/test_faux.go` trigger). Do **not** touch `coding/services.go` yet.
2. Capture **RED**: `go test ./coding/ -run TestSessionFailoverAdvancesToTheNextRoutedModel -count=1`. Before the seam, `routerHost.ModelInfo("test-faux","faux-1")` fails because `routeModel` returns `unknown model: test-faux/faux-1` (`session_router.go:435-438`), so `tierCandidates` drops every candidate (`router.go:890-893`) and `Decide` returns nil (`router.go:454-455`); `ensureRoute` stores `rs.err` with `rs.model == nil` and emits no `RouteEvent` (`session_router.go:474,483`). `cacheWarmingStreamFn` then refuses the request because `s.RouteError()` is set (`coding/session_cache_warming.go:32-35`), so the selected scripted model is never called. `routeFailover` returns false at its `rs.model == nil` guard (`session_router.go:567-568`), so no `RouteEvent{Fallback:true, Model:"faux-2"}` and no `failover-ok` appear — the assertions fail. Record the verbatim failure output.
3. Add the `coding/services.go` seam (item 1). Capture **GREEN** with the same command, plus `-count=3`.

Do not apply the seam before capturing red, and do not hand-revert; the red run is the evidence. (This also matches the `wopr-failover` note that an auto orchestrator already advances once it can route; the red is the routing, not the advance — the test still proves the advance.)

**`-race`:** the test itself is single-threaded; the concurrency is the Session's run goroutine drained by the harness (`session_recovery_test.go:118-125`). Run `go test -race ./coding/ ./ai/ ./internal/codingagent/ -count=1`.

## Verification notes for the fan

- Confirm `routeModel`'s definition check is unchanged (no test-only branch) and that `HasModelDefinition` is the seam.
- Confirm the registration is env-gated: with `WOPR_TEST_FAUX` unset, `routeModel("test-faux","faux-1")` still rejects and `BuildModel` still returns `test-faux is disabled`.
- Run the full suite (`go test ./... -count=1`). The in-process activation is in `internal/codingagent/serve` (`serve_test.go:151`, `serve_dto_test.go:523,617`); the `cmd/wopr` path spawns a subprocess (`rpc_mode_test.go:196-199`) and cannot observe the in-process registry.
- Confirm `go test ./coding/ -run TestSessionFailoverAdvancesToTheNextRoutedModel -count=3` passes (session-scoped trigger key).
- Confirm the test file contains no golden/snapshot/table test and pins no event sequence.
