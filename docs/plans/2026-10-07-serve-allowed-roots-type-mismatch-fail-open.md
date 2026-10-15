# serve-allowed-roots-type-mismatch-fail-open — a wrong-typed `server` / `server.allowedRoots` still fails open

Worktree root `_W_` = `/home/kc/dev/wopr/.worktrees/wave-serve-allowed-roots-type-mismatch-fail-open`.

## Files touched
- Production: `_W_/internal/codingagent/settings.go` — add one scoped pre-check helper and call it from `Settings.UnmarshalJSON` (line 266). No other production file changes; `cmd/wopr/serve.go` is already correct.
- Test: `_W_/internal/codingagent/settings_test.go` — add `TestUnmarshalJSONRejectsUndecodableServerGuardrail`.
- Test: `_W_/cmd/wopr/serve_test.go` — extend `TestGlobalServeAllowedRootsFailsClosedOnMalformedGlobalSettings` (line 88) with one wrong-typed subtest; add `TestRunServeCommandRefusesOnUndecodableServerGuardrail`.
- Spec: `_W_/docs/plans/2026-10-07-serve-allowed-roots-type-mismatch-fail-open.md` (this file).

## Gates
- `go test ./internal/codingagent/... -run 'TestSettingsDropsOnlyTheBadKey|TestUnmarshalJSONRejectsUndecodableServerGuardrail' -count=1`
- `go test ./cmd/wopr/... -run 'TestGlobalServeAllowedRootsFailsClosedOnMalformedGlobalSettings|TestRunServeCommandRefusesOnUndecodableServerGuardrail|TestRunServeCommandRefusesOnMalformedGlobalSettings' -count=1`
- `go build ./...`
- `go vet ./...`
- `go test ./internal/codingagent/... ./cmd/wopr/...`
- `go tool golangci-lint run ./internal/codingagent/... ./cmd/wopr/...`
- `bash -lc 'make check'`

## Definition of done
1. A global `settings.json` whose `server` key is present but not a JSON object, or whose `server.allowedRoots` is present but not an array of strings, makes `Settings.UnmarshalJSON` return an error. That error reaches `globalLoadErr` → `GlobalLoadError()` → `globalServeAllowedRoots()` (serve.go:163) → `runServeCommand` `failf` (serve.go:81-84), so `wopr serve` exits 1 instead of serving unrestricted.
2. `{"server":{"port":7777}}`, `{"server":null}`, an unrelated mistyped key, and a wrong-typed `server.port` that accompanies a valid `allowedRoots` all still load without error; the valid `allowedRoots` survives (`TestSettingsDropsOnlyTheBadKey` stays green).
3. All gates pass. No new dependencies, no new exported symbol, no new `Settings` field, `decodeTolerant` untouched.

## Explicitly out of scope (do NOT do here)
- Changing `decodeTolerant`'s tolerance for unrelated keys, or its array-element salvage behavior (`skills:["a",5,"b"]`).
- Changing `resolveServeRoot` (serve.go:175), the project layer, merged settings, or the allowlist read in `GetServerAllowedRoots` (settings.go:744).
- The sibling backlog item `serve-allowed-roots-wiring-test` (project-cannot-widen assertion) — do not claim or add it.
- Docs, `BACKLOG.md`, or the `serve-allowed-roots-fail-open` tests' existing semantics.

## Mechanism (chosen, smallest)

Add an unexported helper in `internal/codingagent/settings.go` and call it from `UnmarshalJSON` **before** `decodeTolerant` runs, while `raw` still holds the original JSON:

```go
// checkServerRaw rejects a server/allowedRoots value that decodeTolerant would
// otherwise silently drop, which would leave the serve allowlist guardrail off.
// Keys are matched case-insensitively to mirror encoding/json, which resolves
// the "server"/"allowedRoots" struct tags regardless of case; an exact lookup
// would let a case-variant key bypass the check. Scoped to the server key only;
// every other setting stays tolerant.
func checkServerRaw(raw map[string]any) error {
	for key, v := range raw {
		if !strings.EqualFold(key, "server") || v == nil { // absent or null: treated as absent
			continue
		}
		obj, ok := v.(map[string]any)
		if !ok {
			return errors.New("server must be a JSON object")
		}
		for innerKey, roots := range obj {
			if !strings.EqualFold(innerKey, "allowedRoots") || roots == nil {
				continue
			}
			items, ok := roots.([]any)
			if !ok {
				return errors.New("server.allowedRoots must be an array of strings")
			}
			for _, item := range items {
				if _, ok := item.(string); !ok {
					return errors.New("server.allowedRoots must be an array of strings")
				}
			}
		}
	}
	return nil
}
```

Key matching is case-insensitive (`strings.EqualFold`), because `encoding/json` matches struct keys case-insensitively: the `server` tag also matches `Server`, `SERVER`, `sErVeR`, and the `allowedRoots` tag also matches `AllowedRoots` and friends. An exact `raw["server"]` / `obj["allowedRoots"]` lookup does not see those variants, so the guard would be skipped while the decoder still resolves the variant into `Server.AllowedRoots` — the bypass this change closes. Iterating every matching key (rather than stopping at the first) also keeps the check fail-closed when both a good and a bad case-variant key are present.

Call site in `UnmarshalJSON` (settings.go:266): immediately after the `json.Unmarshal(data, &raw)` guard's early `return err` (settings.go:268-270) and before the `skills` rewrite (settings.go:271), i.e. at ~line 270. Keep it before `decodeTolerant` because `decodeTolerant` mutates `raw` in place, so the check must see the original JSON. `skills`/`retry` rewrites never touch `server`, so placement relative to them is immaterial.

Why this is enough: `errors.New` from `UnmarshalJSON` is returned verbatim by `json.Unmarshal` (verified) and is not a `*json.UnmarshalTypeError`, so it propagates out of `loadSettingsFile` (settings.go:1288) → `loadLayer` (settings.go:956, default branch records it and returns it) → `sm.globalLoadErr` (settings.go:945) → `GlobalLoadError()` (settings.go:1009). Nothing else is needed; `serve.go` already fails closed on that.

`allowedRoots` with an explicit JSON `null` is tolerated (decodes to a nil slice). `allowedRoots` with an empty array is tolerated (documented "empty means unrestricted"). Any non-string element — including a JSON `null` element such as `["/a",null]` — is rejected, matching the stated predicate (all elements must be strings); the decoder would otherwise turn a `null` element into an empty root. The whole-value-vs-element `null` asymmetry is deliberate.

## Behavior contract (verified against the current decoder in a scratch replica)

| global `settings.json` input | outcome after fix |
|---|---|
| `{"server":5}` | load **error** → serve refuses |
| `{"server":{"allowedRoots":"x"}}` | load **error** → serve refuses |
| `{"Server":{"allowedRoots":"x"}}` | load **error** → serve refuses (case-variant key) |
| `{"server":{"allowedRoots":[5]}}` | load **error** → serve refuses |
| `{"server":{"allowedRoots":{}}}` | load **error** → serve refuses |
| `{"server":{"allowedRoots":["/a",5]}}` | load **error** → serve refuses |
| `{"Server":{"AllowedRoots":["/a"]}}` | tolerated; `Server.AllowedRoots == ["/a"]` |
| `{"server":{"port":7777}}` | tolerated; `roots == nil, err == nil` |
| `{"server":{"port":"x","allowedRoots":["/a"]}}` | tolerated; `port` dropped, `allowedRoots==["/a"]` |
| `{"theme":"dark","skills":["a",5,"b"]}` | tolerated; unrelated key dropped only |
| `{"server":null}` | tolerated; `Server == nil`; unrestricted |
| `{"server":{"allowedRoots":[]}}` | tolerated; empty → unrestricted |
| `{"server":{"allowedRoots":null}}` | tolerated; nil slice → unrestricted |

The whole-list rows are the load-bearing ones, and they fail open through `deleteJSONPath`, not the sanitized pass. The `UnmarshalTypeError.Field` is the JSON-name path: `server.allowedRoots` for a string/object value, `server.allowedRoots.0`/`.1` for a bad element. `deleteJSONPath` walks that path and deletes the entire `allowedRoots` entry (a non-object at the leaf is deleted outright), then decoding retries against `{"server":{}}` — yielding `Server != nil`, `AllowedRoots` empty, and `GetServerAllowedRoots` returning `nil, nil`. A scratch replica of `decodeTolerant` confirmed `Server=&{Port:0 AllowedRoots:[]}` for `["/a",5]`, `[5]`, `"x"` and `{}`. Rejecting these is required, not over-strict.

The `server.port` wrong-type row was also verified: `typeErr.Field` is the JSON-name path (`server.port`), so `deleteJSONPath` removes only `port` and `allowedRoots` survives. Staying tolerant on `port` therefore does not silently drop the guardrail.

## Tests

1. `TestUnmarshalJSONRejectsUndecodableServerGuardrail` — `internal/codingagent/settings_test.go`.
   Subtests on `json.Unmarshal` into `Settings`:
   - strict (want error naming `server`): `{"server":5}`, `{"server":{"allowedRoots":"x"}}`, `{"server":{"allowedRoots":[5]}}`, `{"server":{"allowedRoots":{}}}`, `{"server":{"allowedRoots":["/a",5]}}`.
   - tolerant: `{"server":{"port":"x","allowedRoots":["/a"]}}` → no error and `slices.Equal(s.Server.AllowedRoots, []string{"/a"})` (guards the non-obvious `decodeTolerant` interaction); `{"server":null}` → no error, `s.Server == nil`.
   This is the behavior contract at the unit seam.

2. Extend `TestGlobalServeAllowedRootsFailsClosedOnMalformedGlobalSettings` — `cmd/wopr/serve_test.go:88`.
   Add one wrong-typed subtest mirroring the existing `malformed` pattern: write `{"server":{"allowedRoots":"x"}}` (object form), build the manager, assert `globalServeAllowedRoots(sm)` returns `err != nil`. One case suffices: the unit test above covers every wrong shape, and `TestRunServeCommandRefusesOnUndecodableServerGuardrail` proves propagation to the exit code, so further subtests would only re-assert the same code path. This one proves the security seam — a wrong-typed `allowedRoots` reaches `globalServeAllowedRoots` as an error.

3. `TestRunServeCommandRefusesOnUndecodableServerGuardrail` — `cmd/wopr/serve_test.go`.
   Mirror `TestRunServeCommandRefusesOnMalformedGlobalSettings` (:122) but write body `{"server":{"allowedRoots":"x"}}`; assert exit `code == 1` and stderr contains `"allowedRoots"` (or `"server"`). This is the literal item gate ("makes `wopr serve` refuse to start"). The existing `{`-body refusal test proves the `globalLoadErr → failf` wiring; this one proves the new error reaches it.

Must stay green, unmodified: `TestSettingsDropsOnlyTheBadKey` (`internal/codingagent/settings_explicit_values_test.go:63`) — the unrelated-array tolerance contract.

No run-command test for `{"server":{"port":"x"}}` is added: that path stays tolerant and would start serving, which is the intended behavior, not a refusal.

## Root cause and propagation (verified file:line)
- A first cut of `checkServerRaw` looked up `raw["server"]` / `obj["allowedRoots"]` exactly. `encoding/json` resolves struct tags case-insensitively, so `{"Server":{"allowedRoots":"x"}}`, `{"SERVER":{"allowedRoots":[5]}}`, and `{"server":{"AllowedRoots":"x"}}` skipped the exact-key check yet still decoded into `Server`, leaving the allowlist empty and `resolveServeRoot` serving unrestricted. An exact-key lookup is therefore a bypass, not a hardening; the check must match the same keys `encoding/json` resolves (`strings.EqualFold`).
- `Settings.UnmarshalJSON` (settings.go:266) → `decodeTolerant` (settings.go:308): a `*json.UnmarshalTypeError` path is deleted (`deleteJSONPath`) and decoding retries, so the four listed inputs parse "successfully" with an empty allowlist.
- `loadSettingsFile` (settings.go:1288) `json.Unmarshal`s into `Settings`; a custom error from `UnmarshalJSON` is returned verbatim.
- `Load` (settings.go:942) sets `sm.globalLoadErr = sm.loadLayer(...)`; `loadLayer` (settings.go:956) records and returns it, leaving the layer at its zero value.
- `GlobalLoadError()` (settings.go:1009) exposes it; `globalServeAllowedRoots` (serve.go:163) wraps it as `"global settings: %w"`; `runServeCommand` (serve.go:19) calls `failf` (serve.go:83) before `resolveServeRoot` (serve.go:85) and `srv.Serve`.
- `GetServerAllowedRoots` (settings.go:744) returns `nil, nil` on a nil `Server` or empty list; `resolveServeRoot` (serve.go:175) treats `len==0` as unrestricted.

## Alternatives considered
- **New `Settings` field / new `SettingsManager` field carrying the scoped error.** Confines the failure to the allowlist read (other commands keep their old tolerant behavior) but adds a field, clone/merge handling, and a new accessor — more surface than the bug warrants, and contrary to the item's "no new struct field" preference.
- **Re-parse the raw global file inside `globalServeAllowedRoots`.** Duplicates file reading at a second choke point; rejected.
- **Reuse `decodeTolerant`'s `decodes` probe for `allowedRoots`.** Does not work: probing `{"allowedRoots":...}` against `Settings` ignores an unknown top-level key, so it always "succeeds". The explicit type assertion above is the correct predicate.

## Open questions
1. **Blast radius (decision needed).** The chosen mechanism makes a bad `server`/`allowedRoots` fail the *entire* settings layer it appears in, not just the serve allowlist read. Because `checkServerRaw` lives in the shared `Settings.UnmarshalJSON`, this applies to both layers: a wrong-typed `server` in the global file fails the whole global layer, and a wrong-typed `server` in a trusted project `.wopr/settings.json` fails the whole project layer (`loadLayer(&sm.project, "project", ...)`, settings.go:948; project layer is only loaded when trusted). This is the same precedent `09bbd65` set for malformed JSON: `wopr serve` refuses (desired), other commands ignore the affected layer and warn via `reportSettingsErrors`, and `wopr config` refuses writes to that layer until the file is fixed. It is accepted here for the same reason as the global case — the check is scoped to the `server` key, so no unrelated setting is affected, and both layers already share one decode path (no extra plumbing). The only way to confine the failure to the serve allowlist read alone is the "new field" alternative above — more code. Recommendation: accept the precedent, proceed with the chosen mechanism.
2. **`{"server":null}`** is tolerated (treated as absent, unrestricted). Recommendation: keep — `null` is JSON's "unset" and matches Go's nil-pointer decode. Confirm if a stricter "explicit null ⇒ refuse" is wanted.
3. **`{"server":{"allowedRoots":[]}}`** is tolerated as unrestricted, per the field's documented "empty means unrestricted". Confirm that an explicit empty array should not be read as "deny all".
