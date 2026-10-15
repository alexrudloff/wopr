# serve-allowed-roots-unknown-key-fail-open

Backlog: `BACKLOG.md:1190` (primary checkout). Worktree `wave-serve-allowed-roots-unknown-key-fail-open`.
Owner decision 2026-10-07: option (b) — fail closed on a near-miss `server` key when no valid
`allowedRoots` is present; **warn-and-continue** on unrelated unknown `server` keys.

## Scope block (first ~25 lines)

- **Files touched (worktree-absolute):**
  - `/home/kc/dev/wopr/.worktrees/wave-serve-allowed-roots-unknown-key-fail-open/internal/codingagent/settings.go`
    (predicate; `checkServerRaw` returns `([]string, error)`; `Settings` gains unexported
    `serverUnknownKeys`; `UnmarshalJSON` stores it; `clone` copies it; new
    `SettingsManager.GlobalUnknownServerKeys` accessor).
  - `/home/kc/dev/wopr/.worktrees/wave-serve-allowed-roots-unknown-key-fail-open/internal/codingagent/settings_test.go`
  - `/home/kc/dev/wopr/.worktrees/wave-serve-allowed-roots-unknown-key-fail-open/cmd/wopr/serve_test.go`
  - `/home/kc/dev/wopr/.worktrees/wave-serve-allowed-roots-unknown-key-fail-open/cmd/wopr/serve.go`
    (print the ignored-key warning right after the manager at :80).
  - this plan doc (untracked).
- **Production sketch:**
  - `serverKeyIsAllowedRootsNearMiss` (§1) — exact-match excluded, then stem test on the trimmed key.
  - `checkServerRaw` (:314-339): skip `nil` inner values; validate the strict `allowedRoots` array;
    treat `port`/`hostname` as known; otherwise record the first near-miss and append the key to
    `unknown`; return `(nil, fmt.Errorf("server.%s is not a known key; did you mean server.allowedRoots?", nearMiss))`
    only when a near-miss exists and no valid `allowedRoots` was found. Existing wrong-type errors kept.
  - `Settings.serverUnknownKeys []string` (unexported ⇒ JSON-ignored); `UnmarshalJSON` sets it on the
    decoded value; `clone` (:416-426) copies it manually (marshal round-trip drops it).
  - `GlobalUnknownServerKeys()` accessor; `cmd/wopr/serve.go` prints
    `wopr serve: warning: unrecognized server setting %q ignored` per key after :80 and continues.
- **Gates (implementer, verbatim):** red-proof (below, temporary, revert) → `go build ./...` →
  `go vet ./...` → `go test ./cmd/wopr ./internal/codingagent -count=1` →
  `GOLANGCI_LINT_CACHE=$(mktemp -d) go tool golangci-lint run ./cmd/wopr/... ./internal/codingagent/...`
  → `bash -lc 'make check'` → `go fix -diff ./...` empty → `git diff --stat` shows only `settings.go`,
  both test files, and `cmd/wopr/serve.go`.
- **Definition of done:** a global `settings.json` with a near-miss `server` key (incl. whitespace-padded
  `"allowedRoots "`) and no valid `allowedRoots` makes `wopr serve` exit 1 naming `allowedRoots`;
  unrelated unknown `server` keys yield `GlobalLoadError() == nil` plus an
  `GlobalUnknownServerKeys()` entry and a serve warning; a valid `allowedRoots` beside a near-miss stays
  accepted; all gates exit 0; red-proof recorded then reverted.
- **Out of scope:** a generic settings warning channel / `SettingsError` / diagnostics changes;
  `decodeTolerant` semantics for non-`server` keys; changing the project-layer whole-layer-drop
  behavior; BACKLOG/commit (wave reconcile step).
- **Open questions:** residuals only — empty-array `allowedRoots`, project-layer drop, total/prefix typos.

## 1. Near-miss predicate

```go
// serverKeyIsAllowedRootsNearMiss reports whether a server key looks like a
// misspelling of the allowedRoots stem, so a likely typo fails closed rather
// than silently disabling the serve allowlist. It is intentionally narrow: it
// catches keys on the "allow…root" stem, not every edit of allowedRoots.
// encoding/json folds case but does not trim, so an exactly-matching key is the
// only form that populates Server.AllowedRoots; a padded or mutated key is a
// near-miss. Padding includes whitespace and invisible format runes (a
// zero-width space pasted before the key), which TrimSpace alone would miss.
func serverKeyIsAllowedRootsNearMiss(key string) bool {
	if strings.EqualFold(key, "allowedRoots") { // exactly what encoding/json matches
		return false
	}
	k := strings.ToLower(strings.TrimFunc(key, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.Is(unicode.Cf, r)
	}))
	return strings.HasPrefix(k, "allow") && strings.Contains(k, "root")
}
```

The valid-key check in `checkServerRaw` stays strict — `strings.EqualFold(innerKey, "allowedRoots")`,
**no trim** — so a padded key is never treated as valid.

Boundary classification (testable):

| Key | Verdict | Why |
|---|---|---|
| `allowedRoots` / `AllowedRoots` / `ALLOWEDROOTS` | valid | `EqualFold` true; `encoding/json` populates |
| `"allowedRoots "` / `" allowedRoots"` | **flag** | `EqualFold` false (no trim) ⇒ does not populate; stem test true |
| `allowedRoot`, `allowedRootsX`, `allowRoots` | flag | stem `allow` + `root` |
| `allowedHosts`, `allowedOrigins`, `allowedRoutes` | tolerate | no `root` substring |

Why not edit distance: Levenshtein(`allowedHosts`,`allowedroots`) = 2, so any threshold ≤ 2
false-positives it; a threshold ≥ 3 stops catching the named typos. Prefix/containment is exact,
one line, dependency-free.

## 2. "No valid allowedRoots is present" condition

Present-and-valid = an inner key with strict `strings.EqualFold(key, "allowedRoots")` and a non-nil
value decoding as an array of strings (the existing loop's validation; now it also sets
`hasValidRoots = true`).

- Valid `allowedRoots` + a near-miss key → **no fail**; the valid key wins, the near-miss is routed to
  the ignored-key warning (§4).
- Wrong-typed `allowedRoots` → the existing `server.allowedRoots must be an array of strings` error
  fires first, unchanged.
- Empty array `"allowedRoots": []` → counts as present-and-valid ⇒ a near-miss beside it does **not**
  fail. Empty is the documented unrestricted value (`ServerSettings.AllowedRoots` comment;
  `GetServerAllowedRoots` :781 returns `nil, nil`). Residual — Open Question 1.
- A `null` inner value is skipped entirely (`roots == nil` ⇒ treated absent), so a null near-miss is
  neither a failure nor a warning — the same invariant the existing `v == nil` check encodes.

## 3. Layer behavior

`checkServerRaw` runs inside `Settings.UnmarshalJSON` (:266-306) once per layer.

- **Global:** `loadLayer` (:992-1004) default branch appends a `SettingsError` and returns the error;
  `Load` (:981) stores it in `globalLoadErr`; `GlobalLoadError()` (:1045-1049) returns it;
  `globalServeAllowedRoots` (`cmd/wopr/serve.go:163-168`) wraps it as `global settings: …`;
  `runServeCommand` (:81-84) `failf`s before `resolveServeRoot`. **Serve refuses.**
- **Project:** the first manager at `cmd/wopr/serve.go:80` uses cwd `"."`, `projectTrusted=false`,
  so it never reads a project layer on this path. The second manager (:96) may load one; its error
  lands in `projectLoadErr`, which **nothing on the serve path gates on** (it does gate
  `UpdateProject`, `settings.go:1101-1102`, which is not the serve path). **Serve does not refuse.**
  Side effect (accepted, mirrors the shipped wrong-type behavior): the whole project layer is
  dropped, not just `server`, because `loadLayer` does not assign `*layer` on error.

## 4. Warning seam for unrelated unknown `server` keys — DESIGN (to implement in Step 4)

Deliberately **not** routed through `SettingsError`. `SettingsError` is rendered by
`CollectSettingsDiagnostics` (`internal/codingagent/settings_diagnostics.go:24-29`) as
`"Invalid settings file <path>: <err>"` and by `status_command.go:114` under `Errors`, which would
mislabel an ignored key as invalid; and `Load` never clears `sm.errors`, so append-per-load would
accumulate duplicates. The ignored keys instead ride an unexported field and one explicit serve-time
print.

1. `Settings` gains `serverUnknownKeys []string` (unexported ⇒ not marshaled).
2. `checkServerRaw` returns `([]string, error)`:
   - server object value must be a map (existing error);
   - inner loop: skip a `nil` value entirely (`roots == nil` → treated absent);
   - `EqualFold(innerKey,"allowedRoots")` → existing array-of-strings validation, set
     `hasValidRoots = true`;
   - `EqualFold(innerKey,"port") || EqualFold(innerKey,"hostname")` → known;
   - default → if `nearMiss == "" && serverKeyIsAllowedRootsNearMiss(innerKey)` set `nearMiss`;
     append `innerKey` to `unknown`;
   - after the loop: if `nearMiss != "" && !hasValidRoots` return
     `(nil, fmt.Errorf("server.%s is not a known key; did you mean server.allowedRoots?", nearMiss))`;
     else return `(unknown, nil)`.
3. `UnmarshalJSON` (:266-306): `unknown, err := checkServerRaw(raw)`; after `decodeTolerant` succeeds,
   `d := Settings(decoded); d.serverUnknownKeys = unknown; *s = d`.
4. `clone` (:416-426): after the round-trip (which re-runs `UnmarshalJSON` and yields no unknowns),
   copy `out.serverUnknownKeys` from `s` (1 line). `mergeSettings`/`settingsJSONMap` (:430, :1383)
   still drop the field by design — it is read at the load/first-manager seam.
5. New accessor:
   ```go
   func (sm *SettingsManager) GlobalUnknownServerKeys() []string {
       sm.mu.RLock(); defer sm.mu.RUnlock()
       return append([]string(nil), sm.global.serverUnknownKeys...)
   }
   ```
6. `cmd/wopr/serve.go`: right after `globalSettings` is created at :80 (before
   `globalServeAllowedRoots` at :81), loop `globalSettings.GlobalUnknownServerKeys()` and print
   `wopr serve: warning: unrecognized server setting %q ignored` to stderr, then continue. No generic
   warning channel; do not touch `reportSettingsErrors` or diagnostics.
7. Tests: `TestLoadWarnsOnUnknownServerKey` asserts `GlobalLoadError() == nil` and
   `GlobalUnknownServerKeys()` contains `allowedHosts`; a new `unknown-server-keys-tolerated` subtest
   asserts `globalServeAllowedRoots` returns `nil, nil`.

## 5. Tests

Unit — `internal/codingagent/settings_test.go`, `TestUnmarshalJSONRejectsNearMissAllowedRootsKey`:
- rejects (error names `server`): `{"server":{"allowedRoot":"/srv"}}`, `{"server":{"allowedRoots ":[...]}}`,
  `{"server":{" allowedRoots":[...]}}`, and `{"server":{"<U+200B>allowedRoots":[...]}}` (leading zero-width
  space — padding `TrimSpace` misses, closed by the `unicode.Cf` trim). The padded cases are kept at the
  unit level only.
- tolerates a plausible unrelated key: `{"server":{"allowedHosts":["x"]}}` decodes, `Server.AllowedRoots` empty.
- valid wins: `{"server":{"allowedRoots":["/a"],"allowedRoot":["/b"]}}` → `AllowedRoots == ["/a"]`.
- empty-array valid: `{"server":{"allowedRoots":[],"allowedRoot":["/b"]}}` → no error.

Warning test — `internal/codingagent/settings_test.go`, `TestLoadWarnsOnUnknownServerKey`: global
`settings.json` `{"server":{"allowedHosts":["x"]}}` → `GlobalLoadError() == nil` and
`GlobalUnknownServerKeys()` contains `allowedHosts`.

Serve wiring — `cmd/wopr/serve_test.go`:
- **ADD** `unknown-server-keys-tolerated` to `TestGlobalServeAllowedRootsFailsClosedOnMalformedGlobalSettings`
  (:90): `{"server":{"allowedHosts":["x"]}}` → `roots == nil, err == nil`. (This subtest does not yet
  exist; an earlier draft wrongly said "keep".)
- **ADD** `TestRunServeCommandRefusesOnNearMissAllowedRootsKey`, mirroring
  `TestRunServeCommandRefusesOnUndecodableServerGuardrail` (`serve_test.go:178`): hold a listener
  (`net.Listen("tcp","127.0.0.1:0")`, `defer Close`) for a free port; write the plain near-miss fixture
  `{"server":{"allowedRoot":"/srv"}}`; call
  `runServeCommand([]string{"serve","--hostname","127.0.0.1","--port",strconv.Itoa(port),"--dir",tmpDir})`
  with an existing `tmpDir`; assert exit `1` and stderr contains the refusal-specific substring
  `did you mean server.allowedRoots`. The fixture is pinned to the plain key and the assertion to the
  refusal message so the test cannot pass with the guard off: a warning would read
  `unrecognized server setting "allowedRoot" ignored` (no "allowedRoots" plural), and only the
  near-miss error path emits `did you mean server.allowedRoots`. The held listener prevents the red
  path from binding the default 4096 and hanging.

Red-proof (temporary, **must revert**, record the FAIL): make `serverKeyIsAllowedRootsNearMiss`
return `false`, then
`go test ./cmd/wopr -run TestRunServeCommandRefusesOnNearMissAllowedRootsKey -count=1`. Expect FAIL —
with the guard off, `resolveServeRoot` treats the empty list as unrestricted, the held port makes
`serve.New`'s `net.Listen` fail, exit is still 1 but stderr lacks `did you mean server.allowedRoots`.
Revert, re-run green. Do not call `t.Parallel` (`t.Setenv`; process-global). Keep tests hermetic
(`WOPR_HOME` → `t.TempDir()`, clear `codingagent.ENV_AGENT_DIR`).

**Lint cache isolation:** the shared default golangci-lint cache (absolute package paths) serves
findings from sibling worktrees. Run lint with an isolated `GOLANGCI_LINT_CACHE`, matching
`Makefile:13-15` (`GOLANGCI_LINT_CACHE ?= $(WOPR_CACHE_HOME)/golangci-lint/$(notdir $(CURDIR))`), e.g.
`GOLANGCI_LINT_CACHE=$(mktemp -d) go tool golangci-lint run ./cmd/wopr/... ./internal/codingagent/...`.
`make check` already isolates via the Makefile default.

## Verified citations

- `internal/codingagent/settings.go`: `Settings` :33-146; `ServerSettings` :170-179;
  `UnmarshalJSON` :266-306 (calls `checkServerRaw` at :271); `checkServerRaw` :314-339 (sole caller
  :271); `decodeTolerant` :344-396; `clone` :416-426; `mergeSettings` :430-449;
  `GetServerAllowedRoots` :780-793 (empty → `nil, nil`); `SettingsManager` :930-934;
  `SettingsError` :937-942; `loadLayer` :992-1004; `GlobalLoadError` :1045-1049; `UpdateProject`
  gates `projectLoadErr` :1101-1102; `loadSettingsFile` :1324-1334; `settingsJSONMap` :1383-1393.
- `internal/codingagent/settings_diagnostics.go:22-32` `CollectSettingsDiagnostics` renders
  `Invalid …`; `cmd/wopr/status_command.go:114-116` appends to `Errors`.
- `cmd/wopr/serve.go`: manager :80, `globalServeAllowedRoots` call :81-84, `reportSettingsErrors` :97,
  `globalServeAllowedRoots` :163-168, empty-list short-circuit :180-182.
- `cmd/wopr/config_command.go:430-434` `reportSettingsErrors`; `cmd/wopr/profiling.go:29-32` `failf`.
- `Makefile:13-15` `GOLANGCI_LINT_CACHE`.
- Existing tests: `settings_test.go:293`; `serve_test.go:90`, `:135`, `:178`, `:221`.
- Absent (must be added): no `unknown-server-keys-tolerated`, `near-miss-roots`,
  `GlobalUnknownServerKeys`, or `serverUnknownKeys` exists in the worktree.

## Open questions (residuals only)

1. Empty-array `allowedRoots` beside a near-miss does not fail (empty = documented unrestricted).
   Confirm, or require fail-closed even then.
2. A project-layer near-miss drops the entire project layer (not just `server`), mirroring the shipped
   wrong-type behavior. Confirm acceptable, or file a follow-up.
3. Typos with no shared stem also fail open and are out of scope: `alowedRoots` (evades the `allow*`
   prefix), `allowedRots` (evades the `root` substring), and total-stem drops `roots` / `allowed`
   (share no stem, so neither the predicate nor the strict valid-key check sees them; with Design F
   they become warnings, not refusals). Accept, or widen to an inline bounded edit-distance predicate.
