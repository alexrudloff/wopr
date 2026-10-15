# serve-kkcode-v1-compat — KKCode mobile-client compatibility delta

Backlog item: `BACKLOG.md:1269` (`serve-kkcode-v1-compat`), claimed 2026-10-07 on
`wave-serve-kkcode-v1-compat`. This is the V1-plus-few-routes delta, not the V2
HttpApi. The V2 tiered-parity plan `docs/plans/2026-10-07-serve-v2-api-parity.md`
is superseded for this client and stays future work.

## Files touched

- `internal/codingagent/serve/serve.go` — register four routes; repoint `GET /permission`.
- `internal/codingagent/serve/serve_handlers.go` — four new handlers; update the `notImplemented` route-count comment.
- `internal/codingagent/serve/serve_dto.go` — new `configDTO`, `apiCommandDTO`, `commandLocationDTO`, `commandLocationProjectDTO`.
- `internal/codingagent/serve/serve_test.go` — new hermetic tests; update `TestPermissionRoutesAnswer501OnEveryMatchingPath` and the "thirty routes" comment.
- `docs/site/docs/server.md` — endpoint table and route counts.

No other file. Do not touch the V1 session/config handlers beyond registering
`GET /experimental/session` on the existing `listSessions`.

## Gates

```
go build ./...
go vet ./...
go test ./internal/codingagent/serve/
go tool golangci-lint run ./internal/codingagent/serve/...
make check
```

Manual (not CI, defer to qa): KKCode connects to `wopr serve` through the logging
proxy with no 404 on connect.

## Definition of done

All five commands above pass. Each new route has a hermetic test pinning status
and body shape (no `~/.wopr` — `TestMain` already isolates). The item's "live
probe against a real listener" is satisfied by construction: `promptEnvFor`
(`serve_test.go:168-172`) calls `New`, which does `net.Listen("tcp", addr)`
(`serve.go:35-36`) and `srv.Serve(ln)`, and every test issues real `http.Get`
requests against `ln.Addr()` — a loopback TCP listener, not a response recorder.
No traffic leaves loopback. No handler emits 401.
`internal/codingagent/serve` still does not import `coding`. The endpoint table
and its counts match the registered routes.

## Out of scope

- Any V2 `/api/*` beyond `/api/health` and `/api/command`: no `/api/status`,
  no `/api/info`, no `/api/` catch-all. Those probes only fire when `/api/health`
  fails, which it no longer will.
- Authentication of any kind. wopr ignores `Authorization: Basic` and never emits
  401 (owner decision 2026-10-07). No middleware added.
- Built-in slash commands in `/api/command`'s `data`. Empty `[]` is the contract.
- Changing `/global/health` or its body. It keeps `version`.
- New dependencies, new abstractions, or a shared helper for the four handlers.

## Resolved decisions

1. **`GET /config` body.** Return `{"model": s.modelSpec}` (omitempty → `{}` when
   unset). `s.modelSpec` is the `provider/model` string the server starts sessions
   with (`cmd/wopr/serve.go:142-146`), and opencode's config `model` is the same
   string form, so it is honest and cheap. The test pins this shape.
2. **`directory` query on the three new non-session routes.** Ignore it on
   `/api/health`, `/config`, `/api/command` (they are root-independent and
   tolerance is the point of this slice). `/experimental/session` keeps
   `listSessions`' validation because the item mandates exact reuse.

## 1. Problem

KKCode's first connect call is `GET /api/health`; wopr answers 404 and the whole
connect aborts before reaching the V1 surface wopr already serves. A logging proxy
against real opencode 1.18.34 proved KKCode uses the V1 contract plus these routes.

## 2. Exact deltas (all verified against the capture in the backlog item)

| Method | Path | Now | Required |
|---|---|---|---|
| GET | `/api/health` | 404 | 200 `{"healthy":true}` — no `version` |
| GET | `/experimental/session` | 404 | 200, byte-for-byte the existing `GET /session` output |
| GET | `/config` | 404 | 200 JSON object (wopr's honest subset, min `{}`) |
| GET | `/api/command` | 404 | 200 `{location:{directory,project:{id,directory}},data:[]}` |
| GET | `/permission` | 501 `not implemented` | 200 `[]` |

## 3. Per-route behavior

### GET /api/health
Handler `apiHealth` (`serve_handlers.go`). Body `apiHealthResponse{Healthy bool
`json:"healthy"`}`; no version field. Separate type from the existing
`healthResponse` (`serve.go:12-15`) so `/global/health` keeps emitting `version`.
Do not reuse or mutate `healthResponse`. Ignores `directory`.

### GET /experimental/session
Register the **existing** `s.listSessions` handler (`serve_handlers.go:95`); do
not write a new handler and do not duplicate logic. It therefore inherits the
`directory` validation/400 (`directoryParam`, `serve_handlers.go:66-77`), the
`limit` default 50 / max 200 (`limitOf`), disk+live merge, sort and DTO shape of
`GET /session`. Exact match by construction.

### GET /config
Handler `getConfig`. Returns `configDTO` with at most `Model string
`json:"model,omitempty"`` (see Resolved decision 1). Never fabricate credentials,
provider secrets, or keys wopr does not hold. Ignores `directory`.
`GET /config/providers` (`serve.go:84`) is a more specific ServeMux pattern and
is unaffected.

### GET /api/command
Handler `apiCommand`. Exact body:

```json
{"location":{"directory":"<served root>","project":{"id":"global","directory":"<served root>"}},"data":[]}
```

`<served root>` is `filepath.EvalSymlinks(s.services.CWD())`, the same value
`GET /project` returns (`serve_handlers.go:338-349`). The project id is the
literal string `"global"` from the capture — **not** `projectID()`'s sha-based id
(`serve_dto.go:150`). `data` must marshal as `[]`, never `null` (initialize it).
Ignores `directory`.

### GET /permission
Repoint `serve.go:93` from `s.notImplemented` to a new handler `listPermissions`
that answers 200 `[]` (marshal as `[]`, never `null`). `serve-7` made all tools
open by default (`BACKLOG.md` serve-7), so there are never pending requests. The
one `POST /session/{id}/permissions/{permissionID}` registration (`serve.go:77`)
stays 501; the existing test exercises it as three wildcard 501 paths plus a
one-segment 404 (`serve_test.go:1312-1336`). The other four stubs (`serve.go:65-68`)
stay 501.

## 4. `directory` query risk (the item's verify item)

KKCode appends `?directory=<root>` to several V1 calls. Root is learned from
`GET /project`, which returns `EvalSymlinks(CWD())`; `directoryParam` compares
`EvalSymlinks(q)` to `EvalSymlinks(services.CWD())`, and `EvalSymlinks` is
idempotent, so a fresh connection matches.

| Route | directory handling | KKCode match? |
|---|---|---|
| `GET /agent` (`listAgents`, `serve_handlers.go:394`) | not read — query ignored | tolerant |
| `POST /session/{id}/prompt_async` (`promptAsync` → `sessionPreconditions` → `directoryParam`) | validated, 400 on mismatch | matches on a fresh connection |
| `GET /config` (new) | recommended ignore | tolerant |
| `GET /api/command` (new) | recommended ignore | tolerant |
| `GET /experimental/session` | inherits `listSessions` validation | matches on a fresh connection |

No existing handler needs changing for this; flag only if a real capture shows a
non-normalized path in the query.

## 5. Route registration (serve.go)

Add beside the existing blocks:

```go
mux.HandleFunc("GET /api/health", s.apiHealth)
mux.HandleFunc("GET /experimental/session", s.listSessions)
mux.HandleFunc("GET /config", s.getConfig)
mux.HandleFunc("GET /api/command", s.apiCommand)
```

and change `serve.go:93` to `mux.HandleFunc("GET /permission", s.listPermissions)`.

Update `serve_handlers.go:79` comment `// notImplemented is the shared stub for
the six 501 routes.` → `five`.

DTOs in `serve_dto.go`: `configDTO`; `apiCommandDTO{Location commandLocationDTO;
Data []any}`; `commandLocationDTO{Directory string; Project
commandLocationProjectDTO}`; `commandLocationProjectDTO{ID, Directory string}`.
No `commandDTO`: `data` is contractually always `[]` (no built-in slash commands,
Out of scope), so the element shape is never constructed. `Data` is initialized
`[]any{}` in the handler so it marshals `[]`, never `null`. No new imports needed
in `serve_handlers.go` (`net/http`, `filepath` already present).

## 6. Tests (hermetic, in serve_test.go)

Use `promptEnvFor(t)` (`serve_test.go:149`) and `getRaw` (`serve_test.go:2346`);
`TestMain` (`serve_test.go:38`) already clears credentials and points `WOPR_HOME`
at a temp dir.

1. `TestAPIHealthAnswersHealthyWithoutVersion` — 200; decode `{"healthy":true}`;
   assert the raw body contains no `version` key.
2. `TestExperimentalSessionMatchesSessionList` — seed one session first with the
   existing `createPromptSession(t, env.baseURL)` helper (`serve_test.go:178`), so
   the compare is non-trivial; then GET both `/session` and `/experimental/session`
   and assert equal status and equal decoded non-empty `[]sessionDTO`. Add one
   `?directory=<root>` call returning 200 and a mismatch returning 400 (proves
   exact reuse of `listSessions`).
3. `TestConfigAnswersAnObject` — 200; decode to `configDTO` and assert
   `model == "test-faux/faux-1"`, the value `promptEnvFor` passes to `New`
   (`serve_test.go:168`). A `{}` or `null` body must fail.
4. `TestAPICommandShape` — 200; assert `location.project.id == "global"`,
   `location.directory` and `location.project.directory` equal
   `EvalSymlinks(env.services.CWD())`, and `data` is a non-null empty array.
5. `TestPermissionListAnswersEmptyArray` — 200; body is exactly `[]` after
   `strings.TrimSpace` (as the existing empty-array tests do, `serve_test.go:1356`),
   which tolerates `writeJSON`'s `json.Encoder` trailing newline.
6. `TestNewRoutesIgnoreAForeignDirectory` — for `/api/health`, `/config`, and
   `/api/command`, GET with `?directory=/somewhere/else` and assert 200. Tolerance
   is the point: a 400 here breaks KKCode's first connect.
7. Update `TestPermissionRoutesAnswer501OnEveryMatchingPath`
   (`serve_test.go:1304`): move the `GET /permission` `200 []` assertion to test 5
   and keep this test for the one `POST /session/{id}/permissions/{permissionID}`
   registration (`serve.go:77`), which it exercises as three wildcard 501 paths
   plus the one-segment 404 (`serve_test.go:1312-1336`). Rename to
   `TestPermissionSubmitRoutesAnswer501OnEveryMatchingPath`.
8. Update the "takes all thirty routes" comment (`serve_test.go:1281`) to
   "thirty-four".

## 7. Docs (docs/site/docs/server.md)

- Change the `GET /permission` row (`server.md:63`) from `**501 stub**` to
  `200` with `[]`.
- Add rows for `GET /api/health`, `GET /experimental/session`, `GET /config`,
  `GET /api/command`, each stating status, body shape, and that `directory` is
  ignored where the recommendation holds.
- Update the counts at `server.md:37`: 30 → 34 routes, twenty-four → twenty-nine
  implemented, six → five 501 stubs.
- Qualify the blanket `directory` claim at `server.md:17` ("validates against the
  served root (400 on a mismatch)"): it holds for the session and file routes, but
  the three new root-independent routes (`/api/health`, `/config`, `/api/command`)
  ignore `directory`. Only that blanket sentence is now wrong; leave each route's
  own description alone.
- Amend the provenance sentence at `server.md:33`: the four new/changed rows come
  from the KKCode capture in the backlog item, not from the
  `grapeot/opencode_ios_client` commit the rest of the table was derived from.
- Do not hard-wrap the prose.

## 8. Invariants

- `internal/codingagent/serve` must not import `coding`; keep the seam in
  `coding/host`. The new handlers use `s.services` and `s.host` only.
- No route, existing or new, may emit 401.
- No credentials, provider secrets, or fabricated config keys in any body.
