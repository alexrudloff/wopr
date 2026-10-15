# docs-drift link-syntax ceiling

**Files touched (implementation):** `tests/docs-drift/docs_drift_test.go` (one regex line + its comment; no helper, no new import).
**Spec file (architect only):** `docs/plans/2026-10-06-docs-drift-link-syntax-ceiling.md`.

**Gates:**
- `go test ./tests/docs-drift/ -count=1`
- `go build ./...`
- `go vet ./...`
- `go tool golangci-lint run`
- Red-proof: insert the temp broken-link line below into `docs/site/docs/index.md`, `go test ./tests/docs-drift/ -count=1` must FAIL with 5 errors, remove the line, re-run must PASS.

**Definition of done:**
- Bundle stays green on the untouched regex inputs (no new false positives).
- Each of the five previously-unmatched broken forms — title, angle brackets, literal space, percent-encoding, upper-case `.MD` — produces one `TestInternalLinksResolve` error in the red-proof.
- No temp link left behind: `git diff -- docs/site/docs/index.md` empty; `git status --short` shows only `tests/docs-drift/docs_drift_test.go` modified.
- build, vet, lint pass.

**Out of scope:** no new test functions; no other files; no docs changes; no single-quote/parenthesized titles; no reference-style links; no escaped-character destinations; no external `http(s)` `.md` links; no case-insensitive filename matching beyond capturing the extension; no percent-decoding (see decision 2); `make check` not used (tmux absent).

**Open questions:** none.

---

## 1. The change (regex only; group 1 semantics unchanged)

Current, `docs_drift_test.go:160`:

```go
var docLink = regexp.MustCompile(`\]\(([A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*\.md)(?:#[^)]*)?\)`)
```

New:

```go
// docLink captures a Markdown inline-link destination ending in a .md
// reference. It accepts the CommonMark destinations the bundle may use: a
// destination in angle brackets, a title after the destination, literal
// spaces, percent-encoded characters, and an upper-case extension. The
// destination must end in .md so arbitrary ](...) text is not a link.
// Group 1 is the raw destination, without fragment or angle brackets.
var docLink = regexp.MustCompile(`\]\([ \t]*<?([A-Za-z0-9_.% -]+(?:/[A-Za-z0-9_.% -]+)*\.(?i:md))(?:#[^)]*)?>?(?:[ \t]+"[^"\n]*")?[ \t]*\)`)
```

Run `gofmt` on the file; the line is long, leave it one line (no hard wrap).

**Group 1 keeps the same meaning as today:** the destination path exactly as written, without the `#fragment` and — new, and the only structural change — without surrounding `<`/`>`, which are placed outside the capture. Extension case is preserved. Both consumers therefore need **no change**:

- `TestEveryPageIsReachableFromTheIndex`, consumer at `docs_drift_test.go:143-146` (function starts `:133`) — `files[match[1]]` still keys the exact basename.
- `TestInternalLinksResolve`, consumer at `docs_drift_test.go:165-179` (function starts `:162`; backlog cited `:162` for the function, not the loop) — `target := match[1]` unchanged.

Form-by-form coverage (all confirmed absent from the current bundle, so no new matches and no regression):

| Input | Captured group 1 | Why |
|---|---|---|
| `](page.md)` | `page.md` | unchanged |
| `](../../threat-model.md#frag)` | `../../threat-model.md` | unchanged |
| `](page.md "Title")` | `page.md` | `(?:[ \t]+"[^"\n]*")?` eats the title |
| `](<page.md>)` / `](<page.md#frag>)` | `page.md` | `<?` / `>?` sit outside the group |
| `](my file.md)` | `my file.md` | space added to the class |
| `](my%20file.md)` | `my%20file.md` | `%` added to the class |
| `](page.MD)` | `page.MD` | `(?i:md)` |

The fragment tail stays exactly the current `(?:#[^)]*)?` — fragment content is discarded, so it never needed narrowing, and narrowing it (to exclude whitespace or `>`) would silently stop the gate reporting broken links whose fragment carries a space or paren (`](missing.md#a b)`), a false green this item exists to prevent. `#[^)]*` already stops before the `)` of an angle-bracket destination (`](<page.md#frag>)`) and before a following `)`, so it composes with the new forms without change.

Not over-broadened: the destination keeps the current segment structure — `seg(?:/seg)*` with `seg = [A-Za-z0-9_.% -]+` — so a leading or doubled slash cannot match and the external forms are excluded: `](//example.com/x.md)`, `](/.md)`, and `](a//b.md)` all fail, as they did before (the old class was `[A-Za-z0-9_.-]`, which also started each segment with a non-slash). `](http://example.com/x.md)` is excluded because `:` is not in the class. The stem requires at least one character (`+`, not `*`), so `](.md)` is not a match. `](see the docs.md guide)` does not match: after the sole `.md` the remaining ` guide)` cannot satisfy the tail. The pattern still requires a literal `](` and a destination ending in `.md`. Two accepted, false-red-only ceilings: a stray `>` after a bare destination (`](foo.md>)`) matches because `<?` and `>?` are independently optional (RE2 cannot pair them without a second capture group and a consumer change), and a percent-encoded external URL (`](https%3A%2F%2Fexample.com/x.md)`) matches because `%` is needed for the `%20` form and we do not decode. Neither exists in the bundle and neither can hide a broken link — they can only make the gate over-report. Note the "same line" claim holds only for the destination/title tail: the fragment tail `#[^)]*` matches a newline (pre-existing).

## 2. Percent-encoding: verbatim, no decode (decision)

Do **not** percent-decode; `match[1]` is compared to bundle names exactly as written.

Justification: every bundle filename is lowercase and hyphenated (`docFiles` at `:28`, keyed by `entry.Name()` at `:43`), so a percent-encoded destination can only ever name a file the bundle does not ship. Matching it verbatim and failing is the correct gate behaviour: it surfaces the author's encoding mistake instead of hiding it. Decoding would require a helper, a `net/url` import, and a change to both consumers to buy support for a filename the convention forbids. Consequence: a future bundle file containing a literal space whose link uses `%20` would false-fail. Ceiling and upgrade path, left as a code comment: if a space-containing bundle filename is ever added, decode with `url.PathUnescape` at both consumers and revisit this regex.

The red-proof's `%20` link still proves the matcher captures percent-encoded destinations; it does not isolate the (absent) decode step, because no space-bearing file exists to resolve to. That limitation is stated rather than papered over.

## 3. Upper-case `.MD`: capture case-insensitively, compare exactly

`(?i:md)` makes the matcher fire on `.MD`/`.Md`; comparison stays exact-case. The bundle is all lowercase and `docFiles` keys by `entry.Name()`, so a `.MD` destination names no file on the case-sensitive host that ships the bundle. Flagging it is correct; normalizing extension case would convert a real 404 into a false green. No consumer change.

## 4. Red-proof procedure (add, observe red, remove)

The bundle contains none of these forms, so prove against a temporary link.

1. Append this line to the end of `docs/site/docs/index.md` (new line, preserve trailing newline):

   ```
   [redproof title](redproof-missing-title.md "Title") [redproof angle](<redproof-missing-angle.md>) [redproof space](redproof-missing space.md) [redproof encoded](redproof-missing%20encoded.md) [redproof upper](redproof-missing-upper.MD)
   ```

2. Run `go test ./tests/docs-drift/ -count=1`. Expected: **FAIL with 5 errors**, all from `TestInternalLinksResolve`, one per target, each shaped `index.md links <target>, which is not in the bundle`. Fewer than 5 errors means a widened form still escapes — the whole point of the five-in-one-line construction.
3. Remove the appended line entirely.
4. Re-run `go test ./tests/docs-drift/ -count=1`. Expected: **PASS**. Confirm `git diff -- docs/site/docs/index.md` is empty and `git status --short` lists only `tests/docs-drift/docs_drift_test.go`.

`TestEveryPageIsReachableFromTheIndex` is unaffected: none of the missing targets is a key in `files`, so no page is marked seen or unreachable by these links.

## 5. Scope fences

- Only `tests/docs-drift/docs_drift_test.go` changes; the diff is one `var docLink` line and the comment above it.
- No helper function, no new import, no consumer edits — group 1 is unchanged for both call sites.
- No new tests. The red-proof is a temporary edit, not a committed test.
- No docs, config, or other package changes.
