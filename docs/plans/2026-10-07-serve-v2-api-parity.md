# serve-v2-api-parity — implementation plan

- Date: 2026-10-07
- Backlog item: `serve-v2-api-parity` in `BACKLOG.md` (filed 2026-10-15)
- Worktree: none yet (this is the spec; execute in `.worktrees/wave-serve-v2-api-parity`)
- Status: **SUPERSEDED 2026-10-15 by `docs/plans/2026-10-15-serve-v2-full-support.md`.** Read that
  document instead; §0 of it lists every claim here that the spec no longer supports. The pins
  below are dead: the model-of-record file was in `/tmp` and is gone, the spec has since moved
  (118 paths / 142 operations / 285 schemas, sha `3fcd666b…`), and D2, D3, D4, and D6 are now
  settled. Kept unedited so the diff against its replacement is legible.
- Status at the time: **Rev-1.** Not yet run through `verify-fan`. Every `file:line` below was
  re-read in the primary checkout at `f77342d`; the V2 spec claims were re-derived from
  `/tmp/openapi-v2.json` (sha256 `1795168957…556e5b`, see §9). Treat the spec-shape claims as
  verified and the client-behaviour claims as **unverified** — KKCode is closed source.
- Model of record: `/tmp/openapi-v2.json`, 537.9 KB, OpenAPI 3.1.0, 117 paths / 141 operations /
  256 schemas, downloaded 2026-10-07 from opencode V2 (`opencode.ai/v2/docs/api` links
  `opencode.ai/v2/openapi.json`).

## Files touched
- `cmd/wopr/serve.go` — wiring only: pass the V2 server identity (pid, listener URLs, tmp dir).
- `internal/codingagent/serve/serve.go` — register the V2 routes and the `/api/` catch-all.
- `internal/codingagent/serve/serve_v2.go` — **new**: V2 router, error envelope, handshake,
  location/session/project/fs/prompt/interrupt handlers.
- `internal/codingagent/serve/serve_v2_dto.go` — **new**: V2 wire types (`Session.Info`,
  `Model.Info`, `Agent.Info`, `Provider.Info`, `FileSystem.Entry`, `ServerInfo`, error tags,
  `Success{location,data}` envelope).
- `internal/codingagent/serve/serve_v2_sse.go` — **new**: the V1-event → V2-frame encoder and
  `GET /api/event`.
- `internal/codingagent/serve/serve_v2_test.go` — **new**: hermetic route/envelope/SSE tests.
- `internal/codingagent/settings.go` — only if the auth decision (§7, D3) adopts a token:
  one field on `ServerSettings` (`settings.go:176-185`).
- `docs/site/docs/server.md`, `docs/site/docs/settings.md`, `docs/threat-model.md`,
  `docs/architecture.md`, `README.md`, `CHANGELOG.md` — S6 only.

## Gates to run
Per slice, on touched packages:
- `go build ./...`
- `go vet ./...`
- `go test -count=1 ./internal/codingagent/serve ./internal/codingagent` (and `./cmd/wopr` when touched)
- `go tool golangci-lint run ./internal/codingagent/... ./cmd/wopr/...`
- `go fix -diff ./...` empty
Before merge: `bash -lc 'make check'` (tmux is on PATH only for login shells).
S3 adds a **manual live probe** (curl against a real listener), because this package's suite is
historically blind to wire defects (BACKLOG serve-3/4 records five such escapes).

## Definition of done
- `/api/*` answers the V2 contract for the in-scope surface in §1, and every other `/api/*`
  path answers a valid V2 **error envelope** — never Go's default 404/405.
- A V2 client can: connect (health/status/info), list/create/get sessions, fetch history,
  browse and read files within the served root, submit a prompt, receive streamed deltas, and
  interrupt a run — all against a single `wopr serve` process.
- V1 routes are byte-unchanged and their tests pass unedited.
- No new dependency. `internal/codingagent/serve` still does not import `coding`.

## Explicitly out of scope
- PTY, persistent-PTY, worktrees, VCS, MCP, credential/OAuth/integration management, plugin
  RPC, shell-as-a-service, saved-permission management, session import/export, snapshot revert,
  durable inbox persistence, forms *engine*, the `session.next.*` durable event log, the
  `/api/experimental/*` namespace. All answered in-envelope; see §1 and the capability table in §5.
- Auth/TLS/CORS/rate-limiting/audit **unless** D3 adopts the optional token; pairing
  (`/api/pair`, `/auth/connect/{code}`) stays stubbed even then.
- Replacing or deleting V1 (see §2).
- Any change to `coding/`, `agent/`, `ai/`, `tui/`. If a slice seems to need one, the seam is
  wrong — the engine already exposes what is needed (§5).

---

## 1. Scope definition

Literal 141-operation parity is reimplementing opencode; that is not the goal. Parity here is
three tiers, stated so a verifier can check each:

**(a) Routes KKCode actually exercises.** Observed at connection: `GET /api/health`,
`GET /api/status`, `GET /api/info`, with an `Authorization` header. Beyond the handshake the
full call set is **unknown** (closed source). We implement the handshake exactly and the
likely loop broadly; the unknown is closed by capturing real traffic (§7, D1). `/api/status`
is in *no* spec we hold (not the local OpenAPI, not the generated SDK) — see §7 D2.

**(b) The V2 core session/event/fs/permission/form surface a chat client needs.** Concretely:
`/api/session*` (list/create/get/patch/delete/message/prompt/interrupt/inbox/active),
`/api/event`, `/api/fs/{list,read,find}`, `/api/location`, `/api/project`, `/api/agent`,
`/api/model`, `/api/provider`, `/api/permission/*`, `/api/form/*`.

**(c) Structurally-unsupported routes answered in the V2 error envelope.** Every remaining
`/api/*` path returns `{_tag:"ServiceUnavailableError", message:"…"}` with 503 rather than a
bare 404. This is what makes "we don't do PTY" an answer the client can parse instead of a
network-shaped accident. Recommended implementation: **one** `mux.HandleFunc("/api/", …)`
catch-all (§4), not 117 hand-written stubs.

**Out of scope, with the reason each is excluded:** PTY/worktree/VCS/MCP/credential/shell have
no wopr engine and no place inside a one-directory server; the durable inbox and `session.next`
log are a new persistence subsystem, not a transport; forms and saved permissions are product
decisions already deferred by `serve-7` (BACKLOG:443-468); import/export/generate are
experimental and off the chat path. All remain reachable and parseable via the catch-all.

## 2. Coexistence vs replacement — **coexist**

**Decision: add `/api/*` alongside V1; do not remove or re-point V1.**

Rationale: V1 is complete (serve-1…7), documented, and pinned to a different client
(`grapeot/opencode_ios_client` @ `2d0e7bd`); removing it breaks that client for no gain. The
paths do not collide — V1 owns `/global/*`, `/session*`, `/file*`, `/find/*`, `/project*`,
`/config/*`, `/provider`, `/agent`, `/question*`, `/permission`; V2 owns `/api/*` plus
`/auth/connect/{code}`. The mux supports both with two `HandleFunc` sets
(`serve.go:57-104` is the V1 set).

Cost, stated rather than hidden: two response shapes, two SSE encoders, two DTO sets in one
package. Mitigations: (i) both tiers call the same engine seams (`coding/host`, the session
readers, `resolveInRoot`), so only the wire layer is duplicated; (ii) V2 types live in one new
file (`serve_v2_dto.go`); (iii) the V2 SSE handler re-encodes the *same* bus events, so there
is one event producer (Host.forwardEvents), not two. A future V1 retirement is a doc decision
once no client uses it, and is explicitly not this plan.

## 3. Event model

V1 today: one SSE stream `/global/event`, frames `event: <name>\ndata: <json-object>\n\n`, 12
of 17 opencode-V1 names emitted (`serve_sse.go:32-45`, `:227-229`). The bus carries
`Event{Type, Session, Data}` and marshals `Data` once (`coding/host/bus.go:16-21`, `:52-72`).

V2 is **not** the same wire. `GET /api/event` (operation `event.subscribe`) returns
`text/event-stream` whose data is `{id, event, data}` where `data` is `V2EventEncoded` =
`{"type":"string","contentMediaType":"application/json"}` — i.e. **a JSON string containing
JSON**, not an inline object. `V2Event = string` in the generated SDK confirms it. The exact
SSE framing (`event:` lines vs `data:`-only vs raw JSON-lines) is not derivable from the
OpenAPI and must be confirmed against a live server or by capturing KKCode (§7, D4).

**Map what exists, what must change, what is new:**

| V1 bus event (source) | V2 event name | Work |
|---|---|---|
| `message.updated` | `message.updated` | reshape `properties{info}` to the V2 Message union |
| `message.part.updated` | `message.part.updated` | V2 `Part` differs from V1 `partDTO`; reshape |
| `message.part.delta` | `message.part.delta` | properties identical today |
| `session.status` | `session.status` | V2 wraps as `status:{type}` (V1 is flat `type`) |
| `session.error` | `session.error` | V2 error object is a tagged union; reshape |
| `todo.updated` | `todo.updated` | `Todo` requires `content` (+`status`,`priority` optional) |
| `session.updated` | `session.updated` | `info` becomes V2 `Session.Info` |
| `question.asked/replied/rejected` | `question.v2.asked/replied/rejected` | rename + reshape |
| `AgentSettledEvent`→`session.status idle` | add `session.idle` | **new** second frame on settle |
| `AgentStartEvent`→`session.status busy` | `session.status` | unchanged |
| — | `session.created`, `session.deleted`, `permission.v2.*` | **new**, only if the trigger exists |
| — | `session.next.*` (32 names), `file.edited`, `pty.*`, `worktree.*`, … | **new, not emitted** |

Design: **one bus, one new encoder.** `serve_v2_sse.go` subscribes exactly like
`globalEvent` (`serve_sse.go:143-223`) and looks up `(v1Name, v1Payload)` in a static table to
produce `(v2Name, v2Properties)`, then writes the V2 frame. Rejected alternative: teach
`eventMapper` to emit both — the bus stores only `(name, payloadBytes)`
(`bus.go:98-101`), so a second shape doubles every subscriber's queued bytes and couples the
two wire vocabularies in one producer. The 32 `session.next.*` durable-log events are the
`/api/experimental/session/{id}/log` stream, a different subsystem, and are not emitted.

**What must change in the producer:** almost nothing. The one addition is `session.idle`,
which is derivable in the V2 encoder from the existing `session.status{type:"idle"}` event — no
`coding/host` change. If a live capture shows KKCode requires `session.next.*` for streaming
(rather than `message.part.delta`), that is a scope escalation and stops the slice (see Risks).

## 4. Router and error envelope

**One catch-all.** `mux.HandleFunc("/api/", s.v2Unsupported)` returns
`503 {_tag:"ServiceUnavailableError", message:"<method> <path> is not supported by wopr serve"}`.
Go 1.22+ `ServeMux` prefers the more specific pattern, so registered V2 routes win and only
unhandled paths/methods fall through. This delivers scope tier (c) with one handler instead of
117 registrations that duplicate a spec the project does not own. `501` is deliberately **not**
used: the spec defines no `NotImplemented` error tag, and the existing V1 `501 {"error":…}`
(`serve_handlers.go:79-82`) is the wrong envelope for `/api/*`.

Error writer: `writeV2Error(w, status, tag, message)` → `{_tag, message, …fields}`,
`Content-Type: application/json`, HTML escaping off (mirror `writeJSON`, `serve_handlers.go:39-45`).
The full tag set is in the spec; the ones this plan emits are `UnauthorizedError`,
`InvalidRequestError`, `SessionNotFoundError`, `FileNotFoundError`,
`ServiceUnavailableError`, `UnknownError`.

Envelopes are **per-route**, taken from the spec: location-scoped reads are
`{location:{directory}, data}`; session-mutating routes are `{data}`; `/api/config` and
`/api/project` are bare arrays; `/api/info` is a bare `ServerInfo`. Do **not** invent a
universal wrapper.

## 5. Capability mapping (in-scope resources)

| V2 resource / route | wopr backing | Verdict |
|---|---|---|
| `/api/info` | `coding/version.Version`, `os.Getpid`, listener addr; `capabilities.persistentPty=false` | new, trivial |
| `/api/health` | mirrors `/global/health` | new, trivial; **in SDK, absent from local spec** |
| `/api/status` | none; **in no spec** | new, body undecided (D2) |
| `/api/event` | bus + V1 mapper (`serve_sse.go:272`) | exists; new V2 encoder (§3) |
| `/api/session` list/create/get/patch/delete | host registry + `sessionRecord*` + `buildSessionDTO` (`serve_handlers.go:95-332`) | exists; new DTO/envelope |
| `/api/session/{id}/message(+/{messageID})` | `sessionEntries`+`entriesToMessages` (`serve_handlers.go:768-1077`) | exists; new union DTO + cursor |
| `/api/session/active` | `Host.Sessions()` + `IsIdle()` (`serve_handlers.go:405-416`) | exists |
| `/api/session/{id}/prompt` | `Session.PromptContent` (`coding/host/host.go:233`) | exists; return an inbox-user DTO |
| `/api/session/{id}/interrupt` | `Session.Abort` (`host.go:312`) | exists; `{interrupted}` |
| `/api/session/{id}/inbox` | none (wopr has no durable inbox) | **new**; 200 `{data:[]}` until promoted |
| `/api/session/{id}/agent`, `/model` | `SetModel`/`applyModel` (`serve_prompt.go:250`) | exists; 204 |
| `/api/fs/list`, `read/*`, `find` | `resolveInRoot` (`serve_dto.go:159`), `os.OpenInRoot` (`serve_handlers.go:514,637`), walk (`:694`) | exists; new `FileSystem.Entry` + raw read |
| `/api/location` | `services.CWD()`, `projectID` (`serve_dto.go:150`) | exists |
| `/api/project` | `projectID` + `EvalSymlinks(CWD())` (`serve_handlers.go:338-349`) | exists |
| `/api/agent` | hardcoded list (`serve_handlers.go:394-402`) | exists; V2 requires `request`+`permissions` defaults |
| `/api/model`, `/model/default` | `AvailableModels` (`serve_handlers.go:358`) | exists; **heavy `Model.Info` DTO** (below) |
| `/api/provider` | `AvailableModels`, `AuthenticatedProviders` (`serve_handlers.go:352-391`) | exists |
| `/api/permission/request`, `/saved`, session permission | none (`serve-7` = all-open, BACKLOG:443-468) | **new**: honest empty 200 |
| `/api/form`, session form | none; nearest is `askuser` (`serve_question.go:21-26,113`) | **new**: D5 decides empty-stub vs askuser bridge |
| `/api/command`, `/api/skill`, `/api/reference`, `/api/config` | `internal/codingagent/skills.go`, `slash_commands.go`; `web` search | partial; S4 (or catch-all) |

**Flagged — wopr cannot back these at all:** PTY/persistent-PTY, worktrees, VCS, MCP servers
and resources, credentials/OAuth/integration, saved permissions and their engine, forms
engine, durable inbox, snapshot revert/stage/commit, session import/export/stats/log/instructions,
plugin RPC, installation update, models-dev refresh, file watcher, LSP. All are catch-all.

**DTO gaps that need deliberate placeholder values** (the spec marks these required, and a
strict generated decoder may reject extras because most schemas set
`additionalProperties:false`, so emit exactly the spec's fields, no wopr-only extras):
- `Session.Info` requires `cost`, `tokens`, `time{created,updated}`, `location{directory}`,
  `projectID`. wopr has no `reasoning` token count → emit the field (required) as the true
  value where known or `0`; `cost` is `Money.USD` = a bare number.
- `Model.Info` requires `capabilities`, `cost[]`, `limit`, `variants[]`, `status`, `enabled`,
  `time`. `variants` and `status` have no wopr source — emit `[]` and `"active"`. This is the
  single largest DTO in the slice; budget for it and test round-trip decode.
- `Agent.Info` requires `request` and `permissions` — emit an empty `Provider.Request` and `[]`.

## 6. Phased slices

Each slice is one feature, green, mergeable alone. `S1–S3` together are the minimal KKCode
connection + chat loop; S1 is first because the handshake gates everything else.

### S1 — V2 skeleton, handshake, error envelope  *(connection test)*
- **Goal:** KKCode's connection probe stops 404ing and every unknown `/api/*` parses.
- **Files:** `serve.go`, `serve_v2.go` (new), `serve_v2_dto.go` (new), `serve_v2_test.go` (new).
- **Capability:** new.
- **Behavior:** `GET /api/health` → 200 `{"healthy":true}`; `GET /api/status` → 200 (D2);
  `GET /api/info` → 200 `ServerInfo{version,pid,urls,paths:{tmp}}`; `POST /api/pair` and
  `GET /auth/connect/{code}` → 503 envelope; every other `/api/*` (any method) → 503
  `ServiceUnavailableError`; unknown method on a *known* route also falls to the catch-all.
- **Gates:** the four commands; tests assert the three handshake bodies over a real loopback
  listener and that `GET /api/nope` and `POST /api/pty` return the envelope, not Go 404/405.
- **DoD:** a `curl` to all three handshake routes shows 200 with the spec field names.

### S2 — V2 session read + lifecycle  *(history + session picker)*
- **Files:** `serve_v2.go`, `serve_v2_dto.go`, tests.
- **Capability:** exists (§5); new DTOs/envelopes.
- **Behavior:** `GET /api/location`; `GET /api/session` → `{data:[Session.Info],cursor:{previous,next}}`
  (`limit/order/directory` honored; `cursor` omitted-safe `null`s); `POST /api/session` →
  `{data:Session.Info}`; `GET /api/session/{id}`; `PATCH` → 204; `DELETE` → 204;
  `GET /api/session/active` → `{data:{<id>:{type:"running"}}}`; `GET /api/session/{id}/message`
  → `{data:[Session.Message.Info],cursor}`; `GET /api/session/{id}/message/{messageID}`;
  `GET /api/project` → bare array.
  Status codes copied from the spec: 400/401/404 as named.
- **DoD:** a V2 client can open the session list, open a session, and scroll its history; a
  disk-only session (server restarted) is listed and readable exactly as in V1.

### S3 — Prompt, interrupt, `/api/event`  *(chat loop)*
- **Files:** `serve_v2.go`, `serve_v2_sse.go` (new), `serve_v2_dto.go`, tests.
- **Capability:** existing prompt/abort/bus; new V2 frame encoder + `session.idle`.
- **Behavior:** `POST /api/session/{id}/prompt` → 200 `{data:Session.Inbox.User}` (404
  unknown, 409 busy, 400 bad directory/body, 413 oversized); `POST /api/session/{id}/interrupt`
  → 200 `{interrupted:bool}` (idle = `false`, no 404 for a known-but-idle session);
  `GET /api/session/{id}/inbox` → 200 `{data:[]}`; `GET /api/event` → SSE V2 frames per §3.
- **DoD (manual, mandatory):** against a live listener, `prompt` then a streamed turn whose
  `message.part.delta` payload the client can decode, then `interrupt`. **Do not merge on the
  unit suite alone** (BACKLOG serve-3/4: five wire defects passed the suite).

### S4 — Model/provider/agent/command/skill read surface  *(pickers)*
- **Files:** `serve_v2.go`, `serve_v2_dto.go`, tests.
- **Capability:** `AvailableModels`, `AuthenticatedProviders`, hardcoded agents, `skills.go`,
  `slash_commands.go`.
- **Behavior:** `GET /api/model`, `/api/model/default`, `/api/provider`, `/api/provider/{id}`,
  `/api/agent`, `/api/agent/{id}`, `/api/command`, `/api/skill`, `/api/reference`; all
  `{location,data}` or the spec's envelope; `Model.Info` emitted per §5.
- **DoD:** the model picker and agent picker decode every required field; `go test` decodes a
  representative payload per DTO (this package's existing `serve_dto_test.go` pattern).

### S5 — Permission / form / question surface  *(answer the model)*
- **Files:** `serve_v2.go`, tests.
- **Capability:** `askuser`/`questionStore` (`serve_question.go`) exists; permissions are
  all-open by decision; forms are new.
- **Behavior:** `GET /api/permission/request` and `/api/permission/saved` → 200
  `{location,data:[]}`; `DELETE /api/permission/saved/{id}` → 404/503 per D6;
  `GET /api/session/{id}/permission` → `{data:[]}`; `.../{requestID}/reply` → 404;
  `GET /api/form` and session form list → 200 empty; `.../reply` → 404; surface
  `question.v2.asked/replied/rejected` on the stream if D5 maps forms to `askuser`.
- **DoD:** the client's permission/form panels render "nothing pending" and do not error; a
  question asked by the agent is answerable end-to-end **iff** D5 chooses the bridge.

### S6 — Docs, settings, threat model
- **Files:** the docs list in Files touched; `settings.go` only if D3 ships a token.
- **DoD:** `server.md` gains a V2 section (endpoint table, the pinned spec sha256, "V2 is
  additive; V1 unchanged"); `threat-model.md` updates the remote-serve section for the V2
  surface and for auth if shipped; `make check` docs-drift green.

## 7. Owner decisions (must be answered before the named slice)

Presented as options with one recommendation each; the recommendation is not a substitute for
the decision.

- **D1 — capture KKCode's real traffic (blocks S2+).** The observed handshake is all we know;
  the full route set is unverified. Options: (a) run KKCode against a logging reverse proxy in
  front of a stubbed `/api/*` server and record every request; (b) obtain the app's API layer
  (closed source — likely impossible); (c) build against the spec and let live testing correct
  it. **Recommend (a)** — one tcpdump/mitmproxy session turns §1(a) from guesswork into a list.
- **D2 — `/api/status` is in no spec we hold.** Options: (a) answer `{"healthy":true}` like
  `/api/health`; (b) answer a minimal `{"status":"ok"}`; (c) 503 envelope. **Recommend (a)**
  until D1 shows the client's expected shape; anything non-404 unblocks the probe.
- **D3 — auth.** V2 documents `401 UnauthorizedError` on every route but defines no security
  scheme; KKCode sends `Authorization`. Options: (a) ignore the header, never emit 401 (today's
  posture, zero code); (b) optional `server.authToken`: when set, require
  `Authorization: Bearer <token>` on `/api/*` else 401, off by default (~30 lines + one settings
  field + docs); (c) implement pairing (`/api/pair` + `/auth/connect`) fully. **Recommend (b).**
  (c) is a subsystem; (a) keeps the threat-model gap open.
- **D4 — event envelope byte shape.** `data` is documented as a JSON *string*. Options:
  (a) emit the spec literally (double-encoded `data`); (b) emit an inline object (V1 style).
  **Recommend (a), verified against a live opencode or a KKCode capture before S3 merges.** The
  exact SSE framing (`event:` line or not) is part of the same capture.
- **D5 — forms ↔ `askuser`.** The V2 form model is a typed-field form engine
  (`Form.StringField`…`ExternalField`); wopr has a question with options/other. Options:
  (a) empty form lists + 503/404 replies (no bridge); (b) bridge `askuser` questions to a
  one-field form so the device can answer. **Recommend (a) for S5**, with (b) as its own slice
  once D1 shows whether KKCode drives questions via forms. Note the client may render questions
  from `question.v2.asked` instead, which S3 already emits.
- **D6 — status for structurally-unsupported routes.** Options: (a) 503
  `ServiceUnavailableError` (recommended, no spec `NotImplemented` tag exists); (b) 501 with a
  `ServiceUnavailableError` body; (c) explicit per-resource 200-empty for read-only resources
  the client may poll and 503 for the rest. **Recommend (a)**, with (c) only if D1 shows a 503
  is fatal at connect for a polled resource.

## 8. Risk register

- **Spec/client drift is the headline risk.** The local OpenAPI, the generated SDK
  (`packages/sdk/js/src/v2/gen/types.gen.ts`), and the prose docs already disagree: the spec has
  `/api/info`, no `/api/health`; the SDK has `/api/health`, no `/api/info`, and a different
  `/api/session/{id}/*` set (`event`, `history`, `wait`) than the spec (which has `inbox`,
  `form`). Neither has `/api/status`. **Mitigation:** pin the spec by sha256 in the repo docs,
  capture the actual server version with D1, and never generate code from the spec until the
  version is pinned.
- **`additionalProperties:false`.** The spec is strict; a strict generated client could reject
  extra fields. Mitigation: V2 DTOs emit only spec fields; no wopr-only extras.
- **Event topology.** If KKCode streams from `session.next.*` rather than `message.part.*`,
  S3's mapping is insufficient. Mitigation: D1/D4 capture before merge; treat as a scope
  escalation, not a silent workaround.
- **`message.part.updated` Part shape** differs between V1 `partDTO` and V2 `Part` (a `Part`
  in the spec is an 8-variant tagged union that `serve_dto.go:65-76` does not model). Reshaping
  is mechanical but is the most likely source of stream decode failures — test a representative
  payload.
- **The suite is not evidence in this package** (BACKLOG serve-3: seven green tests over live
  bugs; serve-4: five more). The live-curl gate in S3 is load-bearing, not optional.
- **V2 ID prefixes.** Routes declare `pattern:^ses` and messages `^msg_`; wopr IDs are UUIDs.
  The server never validates its own IDs, but a strict client might. Mitigation: if D1 shows
  prefix validation, mint `ses_`/`msg_` prefixed IDs at the V2 boundary; otherwise leave IDs
  opaque.

## 9. Verification

- Self-check before dispatch: re-read every cited `file:line` in the worktree; re-derive the
  spec counts from the pinned sha256.
- Run this plan through `verify-fan` (5 angles: scope, capability mapping, event model,
  V1-regression risk, falsifiable gates) before S1 dispatch. This document is **Rev-1**.
- Per-slice: the gate list, plus the S3 live probe. Before merge: `bash -lc 'make check'`.
- Pin the spec: record the sha256 `17951689576c124f66a8b0270d759429f46d2235293b1ec228e889994ac56e5b`
  and source URL in `docs/site/docs/server.md`; do **not** commit the 537 KB JSON unless the
  project decides to vendor specs (it does not today).

## 10. Cited seams (re-read at `f77342d`)

- V1 mux: `internal/codingagent/serve/serve.go:55-104`; `New` wiring `:35-52`.
- V1 SSE + mapper: `serve_sse.go:32-45` (names), `:143-223` (handler), `:227-229` (frame),
  `:280-322` (translate).
- Session DTO/read: `serve_handlers.go:95-207` (list), `:817-870` (buildSessionDTO),
  `:768-813` (messages), `:872-1077` (entriesToMessages); `serve_dto.go:17-96` (DTOs).
- FS confinement: `serve_dto.go:159-192` (`resolveInRoot`), `serve_handlers.go:514,637`
  (`os.OpenInRoot`), `:694` (walk).
- Questions: `serve_question.go:21-26` (`questionDTO`), `:113-132` (`ask`), `:154-208`.
- Engine: `coding/host/host.go:93-124` (`Start`), `:216-320` (`Prompt*`, `Abort`),
  `:133` (`Subscribe`); `coding/host/bus.go:16-21`, `:52-72` (`Publish`).
- Settings: `internal/codingagent/settings.go:176-185` (`ServerSettings`);
  `cmd/wopr/serve.go:149` (`serve.New` call).
- Hermetic harness: `serve_test.go:37-75` (`TestMain`).
- V2 spec: `/tmp/openapi-v2.json`, sha256
  `17951689576c124f66a8b0270d759429f46d2235293b1ec228e889994ac56e5b`.
