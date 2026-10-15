# serve-allowed-roots-fail-open — fail closed when the global settings layer cannot be parsed

## Files touched
- `_abs_/internal/codingagent/settings.go` — add accessor `GlobalLoadError()`.
- `_abs_/cmd/wopr/serve.go` — add helper `globalServeAllowedRoots`; call it in `runServeCommand`.
- `_abs_/cmd/wopr/serve_test.go` — two tests: helper seam + `runServeCommand` refusal.
- `_abs_/docs/plans/2026-10-07-serve-allowed-roots-fail-open.md` — this spec.

(`_abs_` = `/home/kc/dev/wopr/.worktrees/wave-serve-allowed-roots-fail-open`.)

## Helper
`cmd/wopr/serve.go`, above `resolveServeRoot` (line 163):
`func globalServeAllowedRoots(sm *codingagent.SettingsManager) ([]string, error)` —
returns `sm.GlobalLoadError()` wrapped as `"global settings: %w"` if non-nil, else
`sm.GetGlobalSettings().GetServerAllowedRoots()`. `runServeCommand` lines 80–84 call it and
`failf` on error (before `resolveServeRoot` at :85 and `srv.Serve` at :152).

## Accessor
`internal/codingagent/settings.go`: `func (sm *SettingsManager) GlobalLoadError() error` —
RLock, returns `sm.globalLoadErr` (set at settings.go:945; `nil` for absent/parsed per
:961–963, the parse/read error otherwise per :964–967). Do NOT use `DrainErrors` here:
it consumes diagnostics, drops Scope/Path, and draining for control flow is fragile.
Decision: zero-byte `settings.json` is unparsable by `json.Unmarshal` and therefore fails
closed — "unparsable ⇒ closed"; the zero-byte question is rejected, not open.

## Tests
File `cmd/wopr/serve_test.go`. New imports: `io`, `net`, `strconv`, `strings`,
`github.com/alexrudloff/wopr/internal/codingagent` (`os`, `path/filepath`, `testing` already present).

1. `TestGlobalServeAllowedRootsFailsClosedOnMalformedGlobalSettings` — helper seam.
   Each case: `agentDir := t.TempDir()`; optionally write `agentDir/settings.json`; build
   `NewSettingsManagerWithProjectTrust(t.TempDir(), agentDir, false)`; call the helper.
   Assertions: (a) file absent → `roots == nil && err == nil`; (b) file `{"server":{"port":7777}}`
   (parsed, empty allowlist) → `roots == nil && err == nil`; (c) file `{` → `err != nil`.
   (The invalid-`file://` case was dropped — already covered at settings_test.go:30–34.)
2. `TestRunServeCommandRefusesOnMalformedGlobalSettings` — literal gate. Occupy a port with
   `net.Listen("tcp","127.0.0.1:0")`, `defer ln.Close()`, read `port`; `t.Setenv("WOPR_HOME", tmp)`;
   write malformed `tmp/agent/settings.json` (`{`) (`AgentDir()` = `$WOPR_HOME/agent`, paths.go:23–28,46–48);
   capture stderr via `os.Stderr = w` from an `os.Pipe()`, restore in `t.Cleanup` and before reading;
   call `runServeCommand([]string{"serve","--hostname","127.0.0.1","--port",strconv.Itoa(port)})`.
   Assert `code == 1` AND `strings.Contains(stderr, "global settings:")`. Both fail if the guard
   is removed. Hang-safe: on a regressed build the port is occupied, so `serve.New`'s `net.Listen`
   returns immediately (serve/serve.go:35–39) with a different stderr (`address already in use`);
   the blocking `srv.Serve` is unreachable and the substrings differ.

## Gates
- `go test ./cmd/wopr -run 'TestGlobalServeAllowedRootsFailsClosedOnMalformedGlobalSettings|TestRunServeCommandRefusesOnMalformedGlobalSettings' -count=1`
- `go test ./cmd/wopr/... ./internal/codingagent/...`
- `go build ./...`  · `go vet ./...`
- `go tool golangci-lint run ./cmd/wopr/... ./internal/codingagent/...`
- `make check`

## Definition of done
(i) A malformed global settings file makes `globalServeAllowedRoots` return an error and makes
`runServeCommand` exit 1 with the global-settings parse error on stderr, so `wopr serve` refuses
rather than serving unrestricted. (ii) An absent or parsed-empty global layer still returns
`nil, nil` (unrestricted). (iii) All gates pass. No new dependencies; nothing beyond the helper
and accessor. (iv) `runServeCommand` calls the helper; this item owns the refusal test, the
sibling owns the global-only read assertion — no overlap.

## Out of scope (do NOT do here)
- `serve-allowed-roots-wiring-test` — sibling: asserts `runServeCommand` reads the allowlist from the
  global layer (projectTrusted=false) and that a project `settings.json` cannot widen the list.
  It does NOT own refusal; this item does. Do not add that assertion here.
- Changing project-layer or merged-settings fail semantics; `resolveServeRoot` internals.
- A `DrainErrors`-based fix, or printing the drained `SettingsError` scope/path at startup.

## Root cause (verified)
- `NewSettingsManagerWithProjectTrust` (settings.go:915) calls `sm.Load()` (:917).
- `Load` sets `sm.globalLoadErr = sm.loadLayer(...)` (:945); `loadLayer` (:956) records the error
  and returns it on the default branch (:964–967) but leaves the layer at its prior (zero) value.
- `GetServerAllowedRoots` (:744) returns `nil, nil` when `s.Server == nil` (:745).
- `runServeCommand` (:80–84) reads that zero layer → `nil, err==nil`; `resolveServeRoot` treats
  `len==0` as unrestricted (:168); `reportSettingsErrors` warns only later (:97).
  Net: configured guardrail silently off.

## Open questions
1. Accessor name `GlobalLoadError` (recommended; reads as the `error` value) vs `GlobalLoadErr`
   (matches the field). Pick one and keep it.
2. Whether the fail-closed message should also name the settings file path; `failf` currently prints
   the wrapped `loadSettingsFile` JSON error, which omits the path. Non-blocking.
