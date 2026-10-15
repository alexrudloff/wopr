# serve-allowed-roots-wiring-test

Backlog: `BACKLOG.md:1134`. Branch `wave-serve-allowed-roots-wiring-test` off `main` (d57d826).
Test-only. No production edits in the final diff. No commit.

## Scope block (first ~25 lines)

- **Files touched:** `cmd/wopr/serve_test.go` (worktree-absolute:
  `/home/kc/dev/wopr/.worktrees/wave-serve-allowed-roots-wiring-test/cmd/wopr/serve_test.go`),
  plus this plan doc. No production file changes.
- **New tests (both in `cmd/wopr`, package `main`):**
  - `TestGlobalServeAllowedRootsReadsGlobalLayerOnly` — proves the project layer
    cannot widen the allowlist (load-bearing merged-read red-proof).
  - `TestRunServeCommandRefusesRootOutsideGlobalAllowlist` — proves the wiring:
    `runServeCommand` derives the allowlist from the global layer and calls
    `resolveServeRoot` (catches a dropped call). It does **not** prove
    "project cannot widen" — that is Test 1's job; no project fixture here.
- **Key assertions:**
  1. `globalServeAllowedRoots(sm)` returns only the global-layer `server.allowedRoots`
     even when a project `.wopr/settings.json` under the served root declares a
     broader list; with a `projectTrusted=true` manager, a guard asserts the
     merged `sm.Get()` *does* contain the project's broader entry, so the fixture
     would catch a `sm.Get()` read.
  2. `runServeCommand(["serve","--dir",projDir,...])` returns `1` and stderr
     contains `is outside every server.allowedRoots entry` (`cmd/wopr/serve.go:207`,
     wrapped at `cmd/wopr/serve.go:87`), with no project settings involved.
- **Required gates (implementer, verbatim):**
  1. Red-proof Test 1 (**temporary, must revert**): change `globalServeAllowedRoots`
     (`cmd/wopr/serve.go:163-168`) body to
     `return sm.Get().GetServerAllowedRoots()`;
     `go test ./cmd/wopr -run TestGlobalServeAllowedRootsReadsGlobalLayerOnly -count=1`;
     record the FAIL; revert.
  2. Red-proof Test 2 (**temporary, must revert**): change `cmd/wopr/serve.go:85`
     to pass `nil` as `resolveServeRoot`'s second arg;
     `go test ./cmd/wopr -run TestRunServeCommandRefusesRootOutsideGlobalAllowlist -count=1 -timeout 30s`;
     record the FAIL; revert.
  3. `go build ./...`
  4. `go vet ./...`
  5. `go test ./cmd/wopr ./internal/codingagent -count=1`
  6. `go tool golangci-lint run ./cmd/wopr/...`
  7. `bash -lc 'make check'`
  8. `git diff --stat` must show only `cmd/wopr/serve_test.go` (plus this untracked
     plan doc) — both red-proof probes reverted.
- **Definition of done:** both tests exist and pass; gates 1–2 each show the
  red-proof FAIL, then a green run after revert; gates 3–7 exit 0; gate 8 clean.
- **Out of scope:** any production change; table/golden/fuzz tests;
  `internal/codingagent` tests; admin behavior past the refusal seam.
- **Open questions:** none.

## Why the two tests, and the red-proofs

Both red-proofs are **temporary probes reverted before the diff is final**; the
committed slice stays test-only.

- Test 1's `trusted` subtest is the load-bearing merged-read red-proof: the
  project array replaces the global one (`mergeJSONObject`), so a `sm.Get()` read
  returns the broader list and the assertion fails.
- Test 2 red-proofs the dropped-call path: passing `nil` removes the allowlist
  check, so the function proceeds past `resolveServeRoot` and the assertion on
  the outside-allowlist message fails. The held port makes `serve.New`'s
  `net.Listen` (`internal/codingagent/serve/serve.go:36`) fail fast instead of
  hanging.

## Fixtures

All paths from `t.TempDir()` are absolute and exist, so `resolveServeRoot`'s
`EvalSymlinks`/`Stat` succeed. JSON-quote absolute paths with
`fmt.Sprintf("%q", p)`.

- `globalDir` — allowed by the global layer.
- `projDir` — the served root, outside `globalDir`; contains
  `projDir/.wopr/settings.json` with a broader `server.allowedRoots` that
  includes `projDir`.

### Test 1 — `TestGlobalServeAllowedRootsReadsGlobalLayerOnly`

`agentDir := t.TempDir()`; write `agentDir/settings.json` =
`{"server":{"allowedRoots":[<globalDir>]}}`.

Subtest `trusted` (**load-bearing**): `sm := codingagent.NewSettingsManagerWithProjectTrust(projDir, agentDir, true)`.
- Guard: `merged, _ := sm.Get().GetServerAllowedRoots()`; assert
  `slices.Contains(merged, projDir)` — the merged view really is broader.
- Assert `globalServeAllowedRoots(sm)` equals only `[globalDir]` (not the merged
  list). This is the assertion that fails under the `sm.Get()` red-proof.

Subtest `untrusted` (**fixture guard, not discriminating**): it mirrors
`runServeCommand`'s `projectTrusted=false` wiring at `cmd/wopr/serve.go:80`.
- `sm := codingagent.NewSettingsManagerWithProjectTrust(projDir, agentDir, false)`.
- Guard: `sm.Get().GetServerAllowedRoots()` equals `[globalDir]` (project ignored).
- `globalServeAllowedRoots(sm)` equals `[globalDir]`.

### Test 2 — `TestRunServeCommandRefusesRootOutsideGlobalAllowlist`

Mirror `TestRunServeCommandRefusesOnMalformedGlobalSettings` (`serve_test.go:133`).
**No project `settings.json` fixture** — `runServeCommand` never reads one on this
path (`cmd/wopr/serve.go:80` uses cwd `"."`, `projectTrusted=false`; refusal
happens at `resolveServeRoot`, `cmd/wopr/serve.go:85-88`).
- `tmp := t.TempDir()`; `t.Setenv("WOPR_HOME", tmp)`;
  `t.Setenv(codingagent.ENV_AGENT_DIR, "")`.
- `agentDir := filepath.Join(tmp, "agent")`, `MkdirAll`.
- `globalDir := t.TempDir()`, `projDir := t.TempDir()` (just an existing dir).
- `agentDir/settings.json` = `{"server":{"allowedRoots":[<globalDir>]}}`.
- Hold a listener once to get a free port and keep it open for the call duration
  (`net.Listen("tcp","127.0.0.1:0")`, `defer Close`).
- Capture `os.Stderr` with the existing pipe pattern.
- `code := runServeCommand([]string{"serve","--hostname","127.0.0.1","--port",strconv.Itoa(port),"--dir",projDir})`.
- Assert `code == 1`; assert stderr contains
  `is outside every server.allowedRoots entry`.
- Do **not** call `t.Parallel` (`t.Setenv`; `TestResolveServeRoot`/`t.Chdir` are
  process-global).

## Notes / verification already done

- `globalServeAllowedRoots` (`cmd/wopr/serve.go:163`) reads
  `sm.GetGlobalSettings().GetServerAllowedRoots()`, independent of
  `projectTrusted`; `GetGlobalSettings` at `internal/codingagent/settings.go:1035`.
- Project layer path is `<cwd>/.wopr/settings.json`
  (`internal/codingagent/settings.go:973`, `internal/codingagent/paths.go:13`),
  read only when `projectTrusted` (`settings.go:983-985`).
- `mergeJSONObject` (`internal/codingagent/settings.go:451`) replaces arrays
  wholesale, so a merged read returns the project list.
- `normalizeSettingsPath` (`internal/codingagent/settings.go:1271`) returns plain
  absolute paths unchanged, so expected slices compare directly.
- `failf` prints to stderr and returns 1 (`cmd/wopr/profiling.go:29`).
