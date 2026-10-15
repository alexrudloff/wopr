# Spec — `serve-missing-route-tests` (behavioral tests for seven uncovered serve routes)

Date 2026-10-11 · worktree `wave-serve-missing-route-tests` · backlog `BACKLOG.md` §`serve-missing-route-tests`.

## Scannable summary

1. **Files touched:** `internal/codingagent/serve/serve_test.go` only, **with no import changes** (the stdlib import block is untouched). No other file, no new helper. `serve.go`, handlers and DTOs are read-only for this slice.
2. **New tests (4 focused + 1 parameterized) and the route each guards:**
   - `TestProjectCurrentReturnsServedRootProject` → `GET /project/current` (serve.go:89)
   - `TestProviderReturnsSortedAuthenticatedNames` → `GET /provider` (serve.go:99)
   - `TestFindFileReturnsWalkedFileNodes` → `GET /find/file` (serve.go:115)
   - `TestFileStatusReturnsWorkingTreePaths` → `GET /file/status` (serve.go:116)
   - `TestStubbedSessionRoutesAnswer501` (loop over 3 mount paths) → `POST /session/{id}/fork` (serve.go:74), `POST /session/{id}/revert` (serve.go:75), `GET /session/{id}/diff` (serve.go:76)
   - **`POST /session/{id}/permissions/{permissionID}` needs NO new test** — already covered by `TestPermissionSubmitRoutesAnswer501OnEveryMatchingPath` (serve_test.go:2121). Do not duplicate, do not modify (see §Permissions route).
3. **Expected status + shape assertion per route** (DTOs in `serve_dto.go`):
   - `/project/current` → `200`; body decodes into one `projectDTO` (proves a JSON object, not an array/scalar) and `sandboxes` is a non-nil slice (the `projectDTO` contract says it is always present, never `null`).
   - `/provider` → `200`; body decodes to `[]string`, non-nil, `slices.IsSorted` true. Empty is the hermetic result (see §Notes).
   - `/find/file` → `200`; body decodes to `[]fileNodeDTO`, non-nil; after seeding `hello.txt` in CWD, one node has `name=="hello.txt"`, `path=="hello.txt"`, `type=="file"`, `ignored==false`, `absolute!=""`.
   - `/file/status` → `200`; body decodes to a non-nil `[]fileStatusDTO`. On a plain `promptEnvFor` tempdir this is the empty (but non-null) array — the empty case is the assertion, no git fixture needed (see §Notes).
   - `fork`/`revert`/`diff` → `501`; body decodes to `errorDTO` with `error=="not implemented"`.
4. **Red mutation (route removal):** delete the route's `mux.HandleFunc(...)` line in `serve.go`. There is no catch-all, so ServeMux answers `404`; the exact-status assertion fails (`expected 200/501, got 404`). For the stub loop this means **remove each of the three registrations in turn (serve.go:74, :75, :76) and confirm the test goes red for each**, so all three detections are proven. Restore each before the next. A same-path/other-method collision would yield `405`, which the exact-status assertion also rejects.
5. **Gate commands:**
   ```
   go build ./...
   go vet ./...
   go test ./internal/codingagent/serve/
   go tool golangci-lint run ./internal/codingagent/serve/...
   make check
   ```
6. **Definition of done:** the five tests pass; each was observed RED under its own route-removal mutation (all three stub registrations individually) and GREEN after restore; `make check` exits 0. **OUT OF SCOPE:** the permissions route (already covered, left unmodified); implementing `fork`/`revert`/`diff`; any edit to `serve.go`, handlers, DTOs or `testdata`; a generic route-removal guard; router docs; new test helpers; any import change.
7. **Open question — resolved:** "assert the current 501 but stay green when implemented" — see §Resolved question. Recommendation: pin the 501 body now, with a comment that the 501 literal is the one line updated when the stub is implemented. The route-exists guarantee comes from the exact-status assertion succeeding today, not from a separate `!= 404` line.

## Reuse (no new infrastructure)

All in `serve_test.go`: `promptEnvFor(t)` (fresh tempdir CWD, returns `baseURL`, `services`), `createPromptSession(t, baseURL)`, `postJSON(t, baseURL, path, body)`, `getRaw(t, baseURL, path)`, `getStatus(t, baseURL, path)`. Decode into the package-local DTOs (`projectDTO`, `fileNodeDTO`, `fileStatusDTO`, `errorDTO`) — tests are `package serve`, so unexported symbols are reachable if wanted. No new import; `slices`, `encoding/json`, `os` (for `os.WriteFile`) are already imported. Do not add a parallel HTTP helper.

## Per-test detail

### `TestProjectCurrentReturnsServedRootProject`
`promptEnvFor(t)`; `getRaw(t, env.baseURL, "/project/current")`. Assert `200`; unmarshal into a `projectDTO` (a decode failure proves the body is not the expected object shape); assert `sandboxes != nil`. That is the whole test: it pins this route's status and object shape. `id`/`worktree`/`time`/`vcs` are deliberately **not** asserted — they exercise the shared `project()` builder (serve_handlers.go:429), not this route, and pinning them here duplicates builder coverage.

### `TestProviderReturnsSortedAuthenticatedNames`
`promptEnvFor(t)`; `getRaw(t, env.baseURL, "/provider")`. Assert `200`; unmarshal into `[]string`; fail if the slice is nil (body must be `[]`, not `null`); assert `slices.IsSorted`. Do **not** assert a specific provider name — hermetic emptiness is the contract here.

### `TestFindFileReturnsWalkedFileNodes`
`promptEnvFor(t)`; write `hello.txt` into `env.services.CWD()` (`os.WriteFile`); `getRaw(t, env.baseURL, "/find/file")`. Assert `200`; unmarshal into `[]fileNodeDTO`; locate the `hello.txt` node and assert `name`/`path`/`type`/`ignored`/`absolute` per §3. **The seed is deliberate:** an empty root returns `[]`, which makes the "response shape matches the contract" assertion vacuous — there is no element to check. One `os.WriteFile` (no new import) is the minimum that makes the shape assertion real.

### `TestFileStatusReturnsWorkingTreePaths`
`promptEnvFor(t)`; `getRaw(t, env.baseURL, "/file/status")`. Assert `200`; unmarshal into `[]fileStatusDTO`; assert the slice is non-nil. No git fixture: `workingTreePaths` returns an empty non-nil slice on a non-repo tempdir (serve_read.go:499-505), so the plain `promptEnvFor` CWD already exercises this route's success path and its array shape. **Deliberate asymmetry with `/find/file`:** a non-empty `fileStatusDTO` element would require a git fixture and a new `os/exec` import, and the porcelain parser is already owned by `TestWorkingTreePaths` (serve_read_test.go:216-243). So this test stays empty-but-typed; the mutation gate (route removal → 404) is already served by the status assertion.

### `TestStubbedSessionRoutesAnswer501`
`promptEnvFor(t)`; `id := createPromptSession(t, env.baseURL)`; loop over `POST /session/{id}/fork`, `POST /session/{id}/revert`, and `GET /session/{id}/diff` (GET via `getRaw`, POST via `postJSON(t, …, "{}")`). For each: assert `status == 501`; unmarshal into `errorDTO`; assert `error == "not implemented"`. Add a comment on the `501` literal: *this is the current contract for this stub; update this line when the route is implemented.* No separate `!= 404` assertion — see §Resolved question.

## Resolved question — 501 now vs. stays green when implemented

A hard 501 assertion cannot both pin today's observable contract and survive a future implementation; the two demands are irreconcilable as a single assertion. Resolution: pin the current contract now (`status == 501` + `errorDTO.Error == "not implemented"`) and mark the `501` literal as the single line to update when the stub is implemented. The route-removal mutation is still detected because the exact-status assertion fails on the `404` ServeMux returns in the stub's absence. This is what the item asks for in practice: today the route exists and answers 501; the day it is implemented, one literal changes.

Rejected alternatives:
- **Also assert `status != http.StatusNotFound`** — dead as written: the `status == 501` check already fails on the only 404 path, so the extra line can never be the failing assertion, and it does not deliver "stays green when implemented" because the `== 501` literal reds first. Cut.
- **Only `!= 404`, no 501 check** — satisfies "stays green" literally, but a regression 501→500 passes; loses the contract pin. Worse.
- **`status == 501 || status == 200`** — a peephole that forbids other legitimate statuses (e.g. 400 for a bad id) and is a change-detector. Rejected.

## Permissions route — open question addressed

`POST /session/{id}/permissions/{permissionID}` is already covered by `TestPermissionSubmitRoutesAnswer501OnEveryMatchingPath` (serve_test.go:2121), which asserts `501` across three session/id shapes plus `404` for the one-segment form, and encodes `error == "not implemented"`. That test **hard-pins `501` and therefore reds if the permissions stub is later implemented** — the same tension as the other three stubs. This spec leaves that test **unmodified (explicitly out of scope)** and records the tension here so the tracker carries it: when the permissions route is implemented, the `501` literal in `serve_test.go:2131` is the line to update, alongside the new `501` literals added by `TestStubbedSessionRoutesAnswer501`. No additional permissions test is written.

## Notes / risks the implementer must not get wrong

- **Provider emptiness is hermetic, not luck.** `TestMain` (serve_test.go:54-57) unsets every `ai.ProviderCredentialEnvVars()` value and `agentDir` is a fresh tempdir, so `AuthenticatedProviders` returns empty and `/provider` yields `[]`. Do not point at a real `~/.wopr`.
- **No import changes.** The file already imports `slices`, `encoding/json`, `os`, `net/http`, `filepath`. If a proposed test needs a new import, the test is doing too much — cut it back.
- **Each test uses its own `promptEnvFor(t)`** so the `hello.txt` seed in `TestFindFileReturnsWalkedFileNodes` cannot leak into `TestFileStatusReturnsWorkingTreePaths` (which asserts the empty non-nil array).
- **No catch-all route exists**, which is exactly why a removed route returns 404 and the mutation works. If a catch-all is ever added, every test here silently stops detecting removal — the exact-status (200/501) assertions are the backstop. Recorded, not defended against.

## Gate per test (mutation run)
```
# e.g. for /find/file
#   1. remove `mux.HandleFunc("GET /find/file", s.findFiles)` (serve.go:115)
#   2. run:
go test ./internal/codingagent/serve/ -run TestFindFileReturnsWalkedFileNodes -count=1
#   expect: FAIL (expected 200, got 404)
#   3. restore the line; re-run: PASS
```
For `TestStubbedSessionRoutesAnswer501`, repeat with serve.go:74, then :75, then :76 removed in turn — each removal must red the test before declaring the slice done.
