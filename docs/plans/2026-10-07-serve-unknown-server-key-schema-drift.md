# serve-unknown-server-key-schema-drift — implementation plan

- Date: 2026-10-07
- Worktree: `/home/kc/dev/wopr/.worktrees/wave-serve-unknown-server-key-schema-drift`
- Backlog item: `BACKLOG.md` → `serve-unknown-server-key-schema-drift`
- Status: Rev-2, folded accepted fan findings (untagged-field resolution, cut the doc-comment edit, reconciled the red-proof claim). Every `file:line` re-read from the worktree.

## Files touched
- `internal/codingagent/settings.go` — the only file. No test file. No other package.

## Gates to run
- `go build ./...`
- `go vet ./...`
- `go test ./internal/codingagent`
- `go tool golangci-lint run ./internal/codingagent/...`
- `bash -lc 'make check'`  (tmux is on PATH only for login shells)

## Definition of done
- `ServerSettings`' JSON-tag/field-name resolution is the single source of truth for
  `checkServerRaw`'s "known, tolerate" inner-key set: any new `ServerSettings` field
  — tagged or not, exported — can never land in `serverUnknownKeys`, and can never be
  refused by the `allow…root` near-miss guard.
- `allowedRoots` keeps its array-of-strings validation unchanged; `port`/`hostname`
  behavior is unchanged (now covered by the derived set). Case-folding still mirrors
  encoding/json via `strings.EqualFold`.
- All existing `internal/codingagent` tests pass unchanged.

## Explicitly out of scope
- `serverKeyIsServerNearMiss` (outer-key near-miss) and `serverKeyIsAllowedRootsNearMiss`
  stem logic — untouched.
- `decodeTolerant` semantics — untouched.
- `GlobalUnknownServerKeys` and the `cmd/wopr/serve.go` warning loop — untouched.
- Docs beyond this plan; any new dependency (the fix uses only stdlib `reflect`).

## Open questions
- none.

---

## Decision: derive the known set from the struct tags (option a)

Chosen seam: the inner `switch` in `checkServerRaw` (`internal/codingagent/settings.go:351-370`).
Replace the explicit `case strings.EqualFold(innerKey, "port"), strings.EqualFold(innerKey, "hostname")`
(line 363) with `case isServerSettingKey(innerKey)`, where that helper reflects over
`ServerSettings`' fields — JSON tags, and Go field names for untagged fields — the way
`encoding/json` resolves them.

Why (a) over the test-only coupling (b):

- The gate is "a `ServerSettings` field addition **cannot** silently produce a false
  unknown-key warning." Deriving makes the bad state structurally unreachable; a
  test only makes it *caught*, and only when the suite runs. wopr's first working rule
  is "fix the root cause"; the root cause here is a known-set duplicated away from the
  struct that defines it.
- Deriving also closes the second hazard named in the backlog: a future field whose
  name matches the `allow…root` stem. A known name is matched before `default`'s
  near-miss branch, so it never reaches `serverKeyIsAllowedRootsNearMiss` and never
  triggers the fail-closed refusal. A test-only guard leaves that refusal live in
  production code.
- Cost is negligible: one `reflect.TypeOf(ServerSettings{})` per inner key, only during a
  settings load (effectively once at process start). No request path, no hot loop.
- It removes code: the explicit `port`/`hostname` case is deleted because both tags are
  already in the derived set.

## Exact edits

1. Imports (`settings.go:3-20`): add `"reflect"`, gofmt order (`"path/filepath"` …
   `"runtime"`).

2. `checkServerRaw` inner switch (`settings.go:351-370`) — leave the `allowedRoots`
   case (352-362) and the `default` near-miss/unknown body (365-369) byte-for-byte.
   Replace only line 363's case:

```go
case isServerSettingKey(innerKey):
    // Known key declared by ServerSettings; tolerated, not warned about.
```

3. Insert the helper immediately after `checkServerRaw`'s closing brace (after
   `settings.go:384`, before the `serverKeyIsServerNearMiss` doc comment at 386):

```go
// isServerSettingKey reports whether key names a ServerSettings field, matched
// case-insensitively the way encoding/json resolves a field: a tagged field by
// its JSON name, an untagged or `json:",opt"` field by its Go field name, and
// unexported or `json:"-"` fields not at all. The set is derived, so adding a
// ServerSettings field cannot leave it in checkServerRaw's unknown-key set or
// trip the allowedRoots near-miss guard.
// ponytail: top-level fields only; ServerSettings does not embed, so promoted
// tags are not walked — extend this if an embedded struct is ever added.
func isServerSettingKey(key string) bool {
	t := reflect.TypeOf(ServerSettings{})
	for i := range t.NumField() {
		f := t.Field(i)
		if f.PkgPath != "" { // unexported: encoding/json ignores it
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" { // json:"-": never decoded
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" { // no tag, or json:",opt": encoding/json uses the Go name
			name = f.Name
		}
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}
```

The `allowedRoots` case stays first, so its type validation is untouched and a future
field whose name folds to `allowedRoots` is handled there, not by the derived set.

## Why no test

The fix enforces the coupling by construction, and wopr's AGENTS.md says not to add
tests by default: this regression costs no money, loses no data, and weakens no safety.
A reflection-over-tags test is not red against today's tree as it stands — `port` and
`hostname` are already accepted by the explicit case — but it *can* be red-proven by
temporarily adding a `ServerSettings` field, as the alternative below shows. The derive
fix is still preferred because it makes the drift structurally impossible rather than
merely caught by a test that only runs when the suite runs, and because a test
re-introduces a second maintenance surface for the tag list.

### Alternative considered (test-only coupling, option b) and its red-proof

Leave `checkServerRaw` untouched; add a test that reflects over `ServerSettings` fields
and, for each non-`allowedRoots` field's resolved name (JSON name, Go name when
untagged), feeds `{"server":{"<name>":<valid value>}}` through `json.Unmarshal` into
`Settings`, asserting the name is absent from `serverUnknownKeys` (and, for a
stem-matching name, that no error is returned).

Red-proof: with the current explicit `port`/`hostname` switch, temporarily add
a tagged field (e.g. `Extra string` carrying tag `json:"extra"`) to `ServerSettings`
and run the test — the derived tag `extra` lands in `serverUnknownKeys`, so the test fails. Revert. A probe named after the
stem (e.g. `allowedRootsExtra`) additionally shows the refusal path firing. This proves
the test can fail on a field addition.

Rejected because it detects the drift instead of eliminating it and leaves the duplicated
schema live; pick (a) alone unless the human wants belt-and-suspenders.

## Verified seams (re-read in this worktree)

- `ServerSettings` struct + tags: `settings.go:176-184` (`port`, `hostname`, `allowedRoots`).
- `checkServerRaw`: `settings.go:324-384`; inner switch `351-370`; `allowedRoots` case
  `352-362`; port/hostname case `363`; `default` near-miss + unknown `365-369`; refusal
  `372-374`.
- `serverKeyIsAllowedRootsNearMiss`: `settings.go:425-433`.
- `decodeTolerant`: `settings.go:438`.
- Call site: `(*Settings).UnmarshalJSON` calls `checkServerRaw` at `settings.go:276`.
- Surfacing: `serverUnknownKeys` field `settings.go:105-108`, set `311`, clone `519`;
  `GlobalUnknownServerKeys` `settings.go:1149-1152`; warn loop `cmd/wopr/serve.go:81-82`.
- Tests that must stay green: `TestUnmarshalJSONRejectsUndecodableServerGuardrail`
  (`settings_test.go:293`), `TestUnmarshalJSONRejectsNearMissAllowedRootsKey` (`347`),
  `TestUnmarshalJSONRejectsNearMissServerKey` (`393`), `TestLoadWarnsOnUnknownServerKey`
  (`456`). The last still passes: `allowedHosts` and `"server "` are not struct tags.
