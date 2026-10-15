# serve-server-key-near-miss-fail-open

Backlog: `BACKLOG.md:1220` (this worktree). Follow-up to the shipped
`serve-allowed-roots-unknown-key-fail-open` (`BACKLOG.md:1190`), whose owner decision — fail closed
on a near-miss, warn-and-continue on other unknown keys — is the precedent here, applied one level
up to the outer `server` key.

## Scope block (first ~25 lines)

- **Files touched (worktree-absolute):**
  - `/home/kc/dev/wopr/.worktrees/wave-serve-server-key-near-miss-fail-open/internal/codingagent/settings.go`
    (new `serverKeyIsServerNearMiss`; `checkServerRaw` restructured with a cross-key
    `validRootsSeen` + `serverNearMiss` and an outer near-miss refusal).
  - `/home/kc/dev/wopr/.worktrees/wave-serve-server-key-near-miss-fail-open/internal/codingagent/settings_test.go`
  - `/home/kc/dev/wopr/.worktrees/wave-serve-server-key-near-miss-fail-open/cmd/wopr/serve_test.go`
  - this plan doc (untracked).
- **No `cmd/wopr/serve.go` change.** The refusal rides the existing
  `GlobalLoadError` → `globalServeAllowedRoots` → `failf` seam (`serve.go:84-87`, `:166-171`), and the
  tolerate-and-warn path rides the existing `GlobalUnknownServerKeys` → `serve.go:81-83` warning. Both
  were added by the inner-key item; this slice reuses them.
- **Production sketch:**
  - `serverKeyIsServerNearMiss(key)` (§1): exact `EqualFold` excluded, then fold ASCII case in code and drop
    every other rune wherever it appears — non-ASCII runes are discarded unconditionally, never case-folded
    into ASCII — then test CONTAINMENT of the `server` stem, so a pad or stray letter before, inside, or
    after the stem is caught; no other top-level tag contains "server".
  - `checkServerRaw`: hoist `validRootsSeen`/`serverNearMiss`; per top-level key skip `nil`, record a
    near-miss non-`server` key (unspecified which when several exist — map iteration order), or run the
    unchanged inner loop. After the loop, if a near-miss
    outer key exists: refuse when no exact `server` contributed a valid `allowedRoots`, else append the
    key to `unknown` (warning). Inner near-miss/wrong-type errors unchanged.
- **Gates (implementer, verbatim):** red-proof (§6, temporary, revert) → `go build ./...` →
  `go vet ./...` → `go test ./cmd/wopr ./internal/codingagent -count=1` →
  `GOLANGCI_LINT_CACHE=$(mktemp -d) go tool golangci-lint run ./cmd/wopr/... ./internal/codingagent/...`
  → `bash -lc 'make check'` → `go fix -diff ./...` empty → `git diff --stat` shows only `settings.go`
  and the two test files.
- **Definition of done:** a global `settings.json` whose top-level `server` key is padded,
  stray-letter-prefixed, or stem-misspelled and holds a valid `allowedRoots` makes `wopr serve` exit 1 naming the key and
  `did you mean server?`; unrelated top-level keys stay tolerant; a valid exact `server` beside a
  near-miss tolerates and warns on the near-miss key; all gates exit 0; red-proof recorded then reverted.
- **Out of scope:** any `serve.go` change; the inner-key predicate; a generic warning channel /
  `SettingsError` changes; gating the serve path on `projectLoadErr`; name mutations that break the
  contiguous `server` stem — letters inserted inside it (`serxver`, `servxver`) or deleted/transposed
  (`srvr`, `serve`, `srever`); visible letter confusables/homoglyphs outside ASCII `a-z` (fullwidth
  `ｓｅｒｖｅｒ`, Cyrillic `ѕerver`); BACKLOG edit and commit (wave reconcile step).
- **Open questions:** residuals only — port-only near-miss refusal (§2c), which near-miss key is named
  when several exist (§2), stem-breaking mutations (`serxver`/`servxver`/`srvr`/`serve`/`srever`) and
  visible letter confusables/homoglyphs outside ASCII `a-z` evading the stem, project-layer
  warn-not-refuse (§4).

## 1. Outer near-miss predicate

```go
// serverKeyIsServerNearMiss reports whether a top-level settings key looks like a
// misspelling of "server", so a likely-typo'd server block fails closed instead of
// being dropped by encoding/json with the serve allowlist inside it. encoding/json
// folds case but does not normalize, so only the exact case-folded key reaches the
// Settings.Server field. It is the outer-key twin of serverKeyIsAllowedRootsNearMiss
// and is intentionally narrow on name, broad only on noise: ASCII letters are folded
// to lower case and every other rune — non-ASCII regardless of category (whitespace,
// control, format, combining, symbol, letter filler, or a case-folding letter such as
// KELVIN SIGN) — is discarded. The normalized key fails closed when it CONTAINS the
// "server" stem, so a pad or stray letter before, inside, or after the stem is caught.
// The container is deliberately containment rather than prefix: no other top-level
// settings tag contains "server", so the stem cannot collide with a legitimate key.
func serverKeyIsServerNearMiss(key string) bool {
	if strings.EqualFold(key, "server") { // exactly what encoding/json matches
		return false
	}
	k := strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		case r >= 'a' && r <= 'z':
			return r
		default:
			return -1
		}
	}, key)
	return strings.Contains(k, "server")
}
```

**Scope boundary.** This slice closes the padding/stray-noise class: any key whose normalized form (ASCII letters lowercased, all other runes discarded) CONTAINS the contiguous `server` stem. That includes leading/trailing/interior padding of any Unicode category, extra ASCII letters around the stem, and non-ASCII case-folding runes (KELVIN SIGN, İ). Explicitly OUT OF SCOPE, mirroring the inner precedent's `alowedRoots`/`allowedRots` residuals: name mutations that break the contiguous stem — letter insertions INSIDE the stem (`serxver`, `servxver`), deletions/transpositions (`srvr`, `serve`, `srever`), and non-ASCII letter confusables (fullwidth `ｓｅｒｖｅｒ`, Cyrillic `ѕerver`). Catching those needs edit distance / fuzzy matching, which "Why not edit distance" already rejects.

Boundary (testable):

| Outer key | predicate | Why |
|---|---|---|
| `server` / `Server` / `SERVER` | false | `EqualFold` exact; `encoding/json` populates `Server` |
| `"server "` / `" server"` | true | `EqualFold` false (no normalization); non-`a-z` padding removed → `"server"` |
| `"\u200bserver"` (U+200B, Cf) | true | not `a-z` → removed; category-independent |
| `serv\u200ber` (interior ZWSP, Cf) | true | non-`a-z` removed anywhere |
| `ser ver` (interior space) | true | non-`a-z` removed anywhere |
| `ser\u00a0ver` (interior NBSP) | true | non-`a-z` removed anywhere |
| `serv\ufeffer` (interior BOM, Cf) | true | non-`a-z` removed anywhere |
| `ser\u0301ver` (interior combining acute, Mn) | true | non-`a-z` removed anywhere |
| `serv\u200der` (interior ZWJ, Cf) | true | non-`a-z` removed anywhere |
| `\u0007server` (leading control, Cc) | true | non-`a-z` removed anywhere |
| `\u2800server` / `serv\u2800er` (BRAILLE PATTERN BLANK, So) | true | a symbol pad is not `a-z` → removed |
| `\u115fserver` / `serv\u115fer` (HANGUL CHOSEONG FILLER, Lo) | true | a letter-filler pad is not `a-z` → removed |
| `\u3164server` / `serv\u3164er` (HANGUL FILLER, Lo) | true | a letter-filler pad is not `a-z` → removed |
| `\uffa0server` (HALFWIDTH HANGUL FILLER, Lo) | true | a letter-filler pad is not `a-z` → removed |
| `serv\u0903er` (DEVANAGARI SIGN VISARGA, Mc) | true | a spacing-mark pad is not `a-z` → removed |
| `serv\u212Aer` (interior KELVIN SIGN U+212A) | true | non-ASCII rune dropped unconditionally; its `k` case-fold is never applied → `"server"` stem survives |
| `serv\u0130er` (interior LATIN CAPITAL LETTER I WITH DOT ABOVE U+0130) | true | non-ASCII rune dropped unconditionally; its `i` case-fold is never applied → `"server"` stem survives |
| `aserver` / `xserver` (extra ASCII letter before the stem) | true | `a-z` kept → `"aserver"`/`"xserver"` `Contains` `"server"` |
| `myserver` / `serverless` (a-z-only key containing the stem) | true | `Contains(k, "server")`; no legitimate top-level tag contains `server` |
| `servers` / `serverSettings` | true | `Contains(k, "server")` after keep-set |
| `serxver`, `servxver` (a letter inserted INSIDE the stem) | false | contiguity broken → no `server` substring; residual (Open Question 2) |
| `srvr`, `serve`, `srever` | false | no `server` substring remains (deletion/transposition inside the stem; residual, Open Question 2) |
| `ｓｅｒｖｅｒ` (fullwidth), `ѕerver` (Cyrillic) | false | visible letters outside `a-z` are dropped → no `server` stem; documented residual (Open Question 2) |
| `theme`, `defaultProvider`, `subagents`, `diagnostics` | false | no `server` substring |

Note: the rule is a positive keep-set plus containment, not a category drop-list. ASCII letters are
case-folded by hand (`A-Z` → `a-z`) inside the same `strings.Map` that drops every other rune, so the fold
can never map a non-ASCII rune into the keep-set and the order of operations is irrelevant; only ASCII
`a-z` survive. The surviving key fails closed when it **contains** the `server` stem. The keep-set is
category-independent and cannot be outflanked by an exotic invisible rune the way an enumerated drop-list
can; containment is position-independent and catches a stray letter or pad before, inside, or after the
stem, where a prefix test would miss a leading `aserver`/`xserver`.

Why not edit distance: Levenshtein(`server`,`servers`)=1, (`server`,`server `)=1 and (`server`,`aserver`)=1,
the same distances as unrelated short keys; any threshold that catches the typos also false-positives,
while the keep-only-`a-z`-then-containment rule is exact, category- and position-independent, one line,
and dependency-free. A category keep-set also cannot miss an invisible pad by construction, whereas an
enumerated drop-list leaves a new category of invisible rune open (as `So`/`Lo`/`Mc` pads did). The rule is
deliberately bounded by **contiguity of the stem**: it catches insertions, deletions and transpositions only
when the normalized key still contains the contiguous `server` substring. A letter inserted, deleted or moved
*inside* the stem (`serxver`, `srvr`, `serve`, `srever`) breaks contiguity and is an accepted residual
(Open Question 2) — it evades any small edit-distance threshold anyway, which is why fuzzy matching is
rejected wholesale rather than tuned.

## 2. What fails closed (boundary classification table)

Let `validRootsSeen` = some exact (case-folded) top-level `server` object carried an `allowedRoots` that
passed the existing array-of-strings validation (an empty array counts, matching the inner item's
present-and-valid rule). Let `serverNearMiss` = a non-null top-level key the predicate flags; with two or more such keys the
one recorded is unspecified (Go map iteration order), though any of them refuses when `validRootsSeen`
is false.

After the top-level loop: if `serverNearMiss == ""` nothing changes. Otherwise:

- `validRootsSeen` true → **tolerate**; the valid exact key wins (inner "valid wins" precedent), and the
  near-miss key is appended to `unknown` so serve prints the existing warning.
- `validRootsSeen` false → **refuse**, error naming the near-miss key (the item's gate).

| Fixture (global) | exact `server` valid? | Outcome |
|---|---|---|
| `{"server":{"allowedRoots":["/srv"]}}` | yes | accepted (unchanged) |
| `{"server ":{"allowedRoots":["/srv"]}}` | no | **REFUSE** (gate) |
| `{" server":{"allowedRoots":["/srv"]}}` | no | REFUSE |
| `{"\u200bserver":{"allowedRoots":["/srv"]}}` | no | REFUSE |
| `{"servers":{"allowedRoots":["/srv"]}}` | no | REFUSE (stem) |
| `{"aserver":{"allowedRoots":["/srv"]}}` | no | REFUSE (extra leading letter; `Contains`) |
| `{"server ":{"allowedRoot":"/srv"}}` | no | REFUSE (outer near-miss; the padded object's inner keys are never inspected) |
| `{"server ":{"port":8080}}` | no | REFUSE (§2c) |
| `{"server":{"allowedRoots":["/a"]},"server ":{"allowedRoots":["/b"]}}` | yes | tolerate + warn `"server "` |
| `{"server":null,"server ":{"allowedRoots":["/srv"]}}` | no | REFUSE (null exact is absent) |
| `{"server ":null}` | n/a (skipped) | tolerated (null = absent) |
| `{"theme":"dark"}` | no exact, no near-miss | accepted (unrelated key tolerant) |

- **(a) Valid exact + near-miss.** Tolerate. The allowlist the guard uses is the exact `server`'s; the
  padded object is dropped, which is exactly what the user sees warned. Mirrors the inner spec
  (`docs/plans/2026-10-07-serve-allowed-roots-unknown-key-fail-open.md:88-90`).
- **(b) Near-miss outer holding an unrelated inner key (e.g. `allowedRoot`), no valid roots.** Refuse.
  The padded outer key is itself the near-miss; its inner contents are never inspected (the outer loop
  `continue`s first), so the refusal does not depend on the inner typo.
- **(c) Near-miss outer with no `allowedRoots` (only `port`).** Refuse. This is a deliberate,
  precedent-consistent extension of the item's gate, not an accident: the shipped owner decision on
  `serve-allowed-roots-unknown-key-fail-open`
  (`docs/plans/2026-10-07-serve-allowed-roots-unknown-key-fail-open.md:88-89`) is fail-closed on a
  near-miss and warn-and-continue on other unknown keys; this slice applies that same decision one level
  up to the outer `server` key. It is fail-closed, not a security regression, and needs no inspection of
  the padded object's contents. Dropping a padded block silently ignores `port` too; refusing makes the
  typo visible. The narrower alternative — refuse only when the padded object itself holds an
  `allowedRoots`/near-miss value — was rejected as a second parser for a lesser concern.

## 3. Error message

Mirror the inner style (`"server.%s is not a known key; did you mean server.allowedRoots?"`):

```go
return nil, fmt.Errorf("%q is not a known key; did you mean server?", serverNearMiss)
```

Output with the standard wrapping: `wopr serve: global settings: "server " is not a known key; did you
mean server?`. The refusal-specific substring is `did you mean server?`. The warning text
(`unrecognized server setting "server " ignored`) cannot produce it, and the inner error ends
`server.allowedRoots?`, so `did you mean server?` distinguishes the outer path from both.

## 4. Layer behavior and warning-path interaction

`checkServerRaw` runs inside `Settings.UnmarshalJSON` (`settings.go:276`) once per layer.

- **Global:** `loadLayer` (`:1040-1052`) default branch records a `SettingsError` and returns the error;
  `Load` (`:1029`) stores it in `globalLoadErr`; `GlobalLoadError` (`:1093-1097`) returns it;
  `globalServeAllowedRoots` (`serve.go:166-171`) wraps it `global settings: …`; `runServeCommand`
  (`serve.go:84-87`) `failf`s before `resolveServeRoot`. **Serve refuses.** A padded outer key is also
  dropped by `encoding/json` regardless, so refusal is the only non-fail-open outcome.
- **Project:** the serve path's first manager (`serve.go:80`) uses cwd `"."`,
  `projectTrusted=false`, so it never reads a project layer. The second manager (`serve.go:99`) may load
  one; its error lands in `projectLoadErr`, which nothing on the serve path gates on. `reportSettingsErrors`
  (`config_command.go:430-434`, called at `serve.go:100`) prints it as a warning and serve continues; the
  whole project layer is dropped, not just `server` (same accepted behavior as the inner item/§3).
- **Interaction with `serverUnknownKeys`:** one line, no new field and no serve.go edit. On the
  valid-wins tolerate path the near-miss outer key is appended to the existing `unknown` slice, so
  `GlobalUnknownServerKeys` (`settings.go:1102-1106`) returns it and serve.go:81-83 prints
  `unrecognized server setting "server " ignored`. On the refuse path it becomes the error, not a
  warning. The field's doc comment ("settings.server keys") is stretched to include the outer key; the
  warning text reads correctly, and broadening it costs nothing.

## Production sketch (settings.go)

```go
func checkServerRaw(raw map[string]any) ([]string, error) {
	var unknown []string
	validRootsSeen := false
	serverNearMiss := ""
	for key, v := range raw {
		if v == nil { // absent or null: treated as absent
			continue
		}
		if serverKeyIsServerNearMiss(key) {
			if serverNearMiss == "" { // several near-miss keys: which is recorded is map-order dependent
				serverNearMiss = key
			}
			continue
		}
		if !strings.EqualFold(key, "server") {
			continue
		}
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("server must be a JSON object")
		}
		hasValidRoots := false
		nearMiss := ""
		for innerKey, roots := range obj {
			// ... unchanged inner loop (allowedRoots validation, port/hostname,
			// default near-miss + unknown) ...
		}
		if nearMiss != "" && !hasValidRoots {
			return nil, fmt.Errorf("server.%s is not a known key; did you mean server.allowedRoots?", nearMiss)
		}
		validRootsSeen = validRootsSeen || hasValidRoots
	}
	if serverNearMiss != "" {
		if !validRootsSeen {
			return nil, fmt.Errorf("%q is not a known key; did you mean server?", serverNearMiss)
		}
		unknown = append(unknown, serverNearMiss)
	}
	return unknown, nil
}
```

Unchanged: `UnmarshalJSON` wiring (`:276`, `:311`), `serverUnknownKeys` (`:108`), `clone` (`:463-474`),
`mergeSettings` (`:478-497`), `GlobalUnknownServerKeys` (`:1102-1106`), all of `serve.go`.

## 5. Tests

Unit — `internal/codingagent/settings_test.go`:

- `TestUnmarshalJSONRejectsNearMissServerKey` (outer-specific fixtures only):
  - reject with `did you mean server?` — the OUTER error; the padded object's inner keys are never
    inspected, so these do not exercise the inner-key path: `{"server ":{"allowedRoots":["/srv"]}}`,
    `"{\"\u200bserver\":{\"allowedRoots\":[\"/srv\"]}}"`, `{"servers":{"allowedRoots":["/srv"]}}`,
    `{"aserver":{"allowedRoots":["/srv"]}}` (extra leading ASCII letter),
    plus the newly demonstrated keep-set keys (JSON-escaped so the fixture file parses):
    `"{\"\\u2800server\":{\"allowedRoots\":[\"/srv\"]}}"` (BRAILLE PATTERN BLANK),
    `"{\"serv\\u115fer\":{\"allowedRoots\":[\"/srv\"]}}"` (HANGUL CHOSEONG FILLER), and the
    non-ASCII case-folding runes the manual fold drops unconditionally rather than folding into ASCII:
    `{"serv\u212Aer":{"allowedRoots":["/srv"]}}` (KELVIN SIGN U+212A) and
    `{"serv\u0130er":{"allowedRoots":["/srv"]}}` (LATIN CAPITAL LETTER I WITH DOT ABOVE U+0130);
    interior-padding keys are the same class: `{"serv\u200ber":{"allowedRoots":["/srv"]}}`,
    `{"ser ver":{"allowedRoots":["/srv"]}}`;
  - port-only near-miss (§2c) also rejects with `did you mean server?`: `{"server ":{"port":8080}}`;
  - unrelated top-level keys tolerated: `{"theme":"dark"}` and
    `{"defaultProvider":"x","server":{"allowedRoots":["/a"]}}` both `err == nil`, latter
    `Server.AllowedRoots == ["/a"]`;
  - valid wins: `{"server":{"allowedRoots":["/a"]},"server ":{"allowedRoots":["/b"]}}` → `err == nil`,
    `AllowedRoots == ["/a"]`, and the decoded `Settings.serverUnknownKeys` contains `"server "`.
- Extend `TestLoadWarnsOnUnknownServerKey` (`settings_test.go:390`) with the near-miss-beside-valid
  case instead of adding a function: global file
  `{"server":{"allowedRoots":["/a"]},"server ":{"allowedRoots":["/b"]}}` →
  `GlobalLoadError() == nil` and `GlobalUnknownServerKeys()` contains `"server "`.

Serve wiring — `cmd/wopr/serve_test.go`:

- **ADD** subtest `near-miss-outer-key` to `TestGlobalServeAllowedRootsFailsClosedOnMalformedGlobalSettings`
  (`:90`): global `{"server ":{"allowedRoots":["/srv"]}}` → `globalServeAllowedRoots` returns `err != nil`.
- **ADD** subtest `unrelated-top-level-key-tolerated`: global `{"theme":"dark"}` → `roots == nil, err == nil`
  (the item's "unrelated stays tolerant" gate at the seam).
- **ADD** `TestRunServeCommandRefusesOnNearMissServerKey`, mirroring
  `TestRunServeCommandRefusesOnNearMissAllowedRootsKey` (`:233`): hold a listener
  (`net.Listen("tcp","127.0.0.1:0")`, `defer Close`) for a free port; `WOPR_HOME → t.TempDir()`, clear
  `codingagent.ENV_AGENT_DIR`; write global `fmt.Sprintf("{\"server \":{\"allowedRoots\":[%q]}}", serveDir)`;
  capture stderr; call `runServeCommand([]string{"serve","--hostname","127.0.0.1","--port",strconv.Itoa(port),"--dir",serveDir})`;
  assert exit `1` and stderr contains `did you mean server?`. The held listener keeps the red path from
  binding the default/`--port` port and hanging; no `t.Parallel` (`t.Setenv` is process-global).

## 6. Red-proof recipe (temporary, must revert)

Make `serverKeyIsServerNearMiss` return `false` (or bypass its call), then:

```bash
go test ./cmd/wopr -run TestRunServeCommandRefusesOnNearMissServerKey -count=1
```

Expect **FAIL**. With the guard off, `checkServerRaw` skips `"server "`, `GlobalLoadError()` is nil,
`resolveServeRoot` treats the empty list as unrestricted, the held port makes `serve.New`'s `net.Listen`
fail, exit is still 1, but stderr lacks `did you mean server?`. Record the FAIL, revert, re-run green.
Re-run `go test ./internal/codingagent -run TestUnmarshalJSONRejectsNearMissServerKey -count=1` to show
the unit red too, then revert.

## Verified citations (this worktree)

- `internal/codingagent/settings.go`: `Settings` :34-151 (`Server` :104, `serverUnknownKeys` :108);
  `ServerSettings` :176-184; `UnmarshalJSON` :271-314 (calls `checkServerRaw` :276; assigns
  `serverUnknownKeys` :311); `checkServerRaw` :324-366 (outer match :327, inner near-miss error :361-363);
  `serverKeyIsAllowedRootsNearMiss` :378-386; `decodeTolerant` :391-443; `clone` :463-474;
  `mergeSettings` :478-497; `GetServerAllowedRoots` :828-841; `SettingsManager` :968-982;
  `SettingsError` :985-990; `loadLayer` :1040-1052; `GlobalLoadError` :1093-1097;
  `GlobalUnknownServerKeys` :1102-1106; `loadSettingsFile` :1381-1391.
- `cmd/wopr/serve.go`: global manager :80; warning loop :81-83; `globalServeAllowedRoots` call :84-87;
  second manager + `reportSettingsErrors` :99-100; `globalServeAllowedRoots` :166-171; `resolveServeRoot`
  :178-211.
- `cmd/wopr/profiling.go:29-32` `failf` returns 1; `cmd/wopr/config_command.go:430-434`
  `reportSettingsErrors` prints and continues.
- Existing tests: `settings_test.go` `TestUnmarshalJSONRejectsUndecodableServerGuardrail` :293,
  `TestUnmarshalJSONRejectsNearMissAllowedRootsKey` :347, `TestLoadWarnsOnUnknownServerKey` :390;
  `serve_test.go` `TestGlobalServeAllowedRootsFailsClosedOnMalformedGlobalSettings` :90 (subtests
  `unknown-server-keys-tolerated` :134), `TestRunServeCommandRefusesOnNearMissAllowedRootsKey` :233.
- Precedent: `docs/plans/2026-10-07-serve-allowed-roots-unknown-key-fail-open.md` (predicate :55-63,
  valid-wins :88-90, error :132, layer behavior :98-111).
- Added by this slice (line numbers as of the shipped implementation): `serverKeyIsServerNearMiss`
  in `internal/codingagent/settings.go`; the `near-miss-outer-key` and
  `unrelated-top-level-key-tolerated` subtests and `TestRunServeCommandRefusesOnNearMissServerKey` in
  `cmd/wopr/serve_test.go`. A grep for a top-level `"server "`/`servers`/`serverSettings` key in
  non-Markdown sources still returns nothing (only `BACKLOG.md`).

## Open questions (residuals only)

1. Port-only near-miss (§2c) refuses rather than tolerating. Deliberate, precedent-consistent extension of
   the item's literal gate — the shipped owner decision
   (`docs/plans/2026-10-07-serve-allowed-roots-unknown-key-fail-open.md:88-89`) applied one level up, not
   an accident; override only if a padded `port`-only block should warn instead.
2. Accepted residuals (SCOPE BOUNDARY). This slice closes the padding/stray-noise class: any key whose
   normalized form (ASCII letters lowercased, all other runes discarded) CONTAINS the contiguous `server`
   stem — leading/trailing/interior padding of any Unicode category, extra ASCII letters around the stem,
   and non-ASCII case-folding runes (KELVIN SIGN, İ). Explicitly OUT OF SCOPE, mirroring the inner
   precedent's `alowedRoots`/`allowedRots` residuals: name mutations that break the contiguous stem —
   letter insertions INSIDE the stem (`serxver`, `servxver`), deletions/transpositions (`srvr`, `serve`,
   `srever`), and non-ASCII letter confusables (fullwidth `ｓｅｒｖｅｒ`, Cyrillic `ѕerver`). Catching these
   needs edit distance / fuzzy matching, which "Why not edit distance" already rejects; the inner-key item
   shipped with the same boundary. Accept, or file a bounded fuzzy-match follow-up.
3. Project-layer near-miss prints via `reportSettingsErrors` but does not refuse (serve path does not gate
   `projectLoadErr`). Matches the inner item; accept or file a follow-up.
4. Two case-variant exact keys (`server` and `Server`) are evaluated per key by the inner check, an
   existing quirk unchanged here.
5. With two or more near-miss outer keys, which one the error names is unspecified (Go map iteration
   order); any near-miss still refuses when `validRootsSeen` is false. The tests use a single near-miss
   key, so they stay deterministic.
