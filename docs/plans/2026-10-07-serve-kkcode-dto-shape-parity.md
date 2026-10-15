# serve-kkcode-dto-shape-parity — align wopr's V1 DTOs to opencode's captured shapes

Status: **Rev-3** spec (architect), 2026-10-07. Backlog item: `serve-kkcode-dto-shape-parity`
(BACKLOG.md 1308-1339). Evidence: `/tmp/opencode-shapes/` captures of opencode 1.18.34,
summarized in the sidecar `2026-10-07-serve-kkcode-dto-shape-parity-shapes.md`. Rev-2 applied the
spec verify-fan findings (three blockers — helper definition, `New` wiring order, shared info
builder — and eight minors); Rev-3 fixes two seam re-check warnings: one `path.cwd` source across
poll and stream, and exact `ai.Usage` subfield mapping in §6a.

**Files touched** (all under `internal/codingagent/serve/` unless noted): `serve_dto.go` (DTO
structs), `serve_handlers.go` (handlers, `buildSessionDTO`, package funcs `entriesToMessages`,
`messageInfo`, `finishFromStopReason`, new method `sessionModelParts`), `serve_sse.go` (mapper
carries cwd/root/provider/model), `serve_prompt.go` (call site), `serve.go` (`New` wiring),
`serve_dto_test.go`, `serve_test.go`; `docs/site/docs/server.md`.

**Gates:** `go build ./...`; `go vet ./...`; `go test -count=1 ./internal/codingagent/serve/...`;
`go tool golangci-lint run ./internal/codingagent/serve/...`; `go fix -diff ./...` (must print
nothing); `make check`.

**Definition of done:** all gates green; every changed route's body, decoded from real response
bytes, carries the captured obj/arr key set with the right JSON kind (sidecar tables) — kind/key
assertions, not golden dumps; `/config/providers` `key` is `""` always, `providers` is an array
with `default` an object, and each model value carries the full captured nested key set; no
`coding` import is added to `serve`; live KKCode flow completes with no JS error.

**Out of scope:** V2 `/api`; backward compat with `grapeot` @ `2d0e7bd`; fixing
`serve-test-faux-fallback-in-production`; real per-session `agent`/`mode`/`variant`; disk-session
`model_change` truth; session `parentID`/`permission`; part-level `time`; agent `model` object
(rejected — see §5); other routes.

**Open questions** (formerly #1 is now resolved in §11; see §11 for detail): (1) `session.agent` /
`message.mode` / `message.agent` `""` (chosen) vs `"build"`? (2) `session.path` `""` (chosen) vs
`rec.Path`? (3) `/config` `username` `""` (chosen) vs `os/user` name? (4) `source` constant
`"api"` vs local distinction? (5) session `tokens.reasoning` is `0` (accounting lacks it) —
accept or extend `SessionTokenStats`?

---

## 1. Problem

KKCode decodes opencode's real response objects and calls `Object.keys`/spread on fields
wopr omits. An absent obj/arr field is `undefined` in JS and the call throws
`cannot convert undefined value to object`. wopr's V1 DTOs are skinnier than opencode's. The
fix is to emit opencode's captured key set with the right JSON types, using `{}`/`[]`/`0`/`""`
where wopr has no data. Scalar fields may be absent or null; obj/arr fields must be present.

The same rule applies to the SSE stream, not only the polled routes: `message.updated` carries
`messageInfoDTO`, and `session.updated` carries `sessionDTO`, so both must track the new shape.

## 2. `GET /config`

Replace `configDTO{Model string json:"model,omitempty"}` with the full 12-key object
(sidecar section 1). All obj/arr fields are initialized to empty composite literals so they
marshal `{}`/`[]`, never `null`. `getConfig` becomes:

```go
func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, configDTO{
		Command:      map[string]any{},
		Plugin:       []any{},
		Model:        s.modelSpec,
		Mode:         map[string]any{},
		Agent:        map[string]any{},
		Provider:     map[string]any{},
		MCP:          map[string]any{},
		Instructions: []any{},
		Permission:   map[string]any{},
	})
}
```

`$schema`, `username`, `subagent_depth` stay zero values (present, correct type). `directory`
stays ignored.

## 3. `GET /config/providers` — structural change

Replace the map response with `providersResponseDTO{Providers []providerDTO, Default map[string]string}`.
`providerDTO` gains `ID/Source/Env/Key/Options` and keeps `Models`; `providerModelDTO` becomes the
full captured nested object (sidecar section 2). Handler:

1. `current` from the first live session's `Inner().Model()` (unchanged).
2. `models, err := codinghost.AvailableModels(s.services, current)`; keep the 500 on error.
3. Group by `codinghost.ModelProviderID(m)`. Each provider starts as
   `providerDTO{ID: pid, Name: pid, Source: "api", Env: []string{}, Options: map[string]any{}, Models: map[string]providerModelDTO{}}`.
   Each model value is built in full so no nested obj is `undefined`:
   ```go
   providerModelDTO{
       ID: m.ID, ProviderID: pid, Name: m.DisplayName,
       API: map[string]any{}, Capabilities: map[string]any{}, Cost: map[string]any{},
       Limit: map[string]any{}, Options: map[string]any{}, Headers: map[string]any{},
       Variants: map[string]any{},
       // Family, Status, ReleaseDate stay "" (scalars; absent-equivalent).
   }
   ```
4. Sort provider ids with `slices.Sort`. **The slice is allocated non-nil before the append:**
   `providers := make([]providerDTO, 0, len(ids))` then `append` per id, so an empty result
   marshals `[]`, never `null`.
5. `Default` from the new `sessionModelParts` helper (§4a): `{providerID: modelID}` when
   `modelSpec` splits on `/`; `map[string]string{}` otherwise (never null).

`Name` is the provider id: the `ai.Provider` interface (`ai/types.go:543`) has `ID()` but no
display name. Never emit a real key. `p, m, ok := s.sessionModelParts()` with `if ok { def[p] = m }`.

**Old-client note.** `grapeot` @ `2d0e7bd` decodes this route as a map `{providerID: {name,api,models}}`.
The array shape breaks it. KKCode is the target; this must not block the change (BACKLOG 1337-1339).

## 4. Session DTO

`sessionDTO` (backlog: `{id,slug,projectID,directory,path,title,agent,model{id,providerID,variant},version,summary,cost,tokens{input,output,reasoning,cache{read,write}},time{created,updated},project}`)
gains `Path`, `Agent` (both `string`), `Model sessionModel`, `Summary sessionSummary`, and
`Project projectDTO`; `Cost` becomes a value `float64` and `Tokens` becomes a value
`sessionTokens` (both always present, no `omitempty`). `sessionTokens` gains
`Reasoning int` and `Total int`; the sidecar has the full type table.

`buildSessionDTO` (`serve_handlers.go:856`):
- `root, _ := filepath.EvalSymlinks(s.services.CWD())`.
- `Project = projectDTO{ID: projectID(root), Worktree: root}` (reuse the existing `ProjectID` computation).
- `Path = ""`, `Agent = ""`, `Summary = sessionSummary{}`.
- Model: when `live != nil` and `live.Inner().Model() != nil`, use
  `{ID: m.ID, ProviderID: codinghost.ModelProviderID(m), Variant: "default"}`; else use
  `sessionModelParts` (§4a) with the same variant. Never null.
- Tokens/cost: always emit `sessionTokens`; when live, fill `Input/Output/Total` and
  `Cache{Read,Write}` from `live.Inner().Inner().Accounting().Tokens`, `Reasoning: 0`, and
  `Cost = acc.Tokens.Cost`. When not live, zeros.

The session DTO also flows through `PATCH` and the `session.updated` SSE frame unchanged.

### 4a. New helper `sessionModelParts` (blocker 1)

```go
// sessionModelParts splits the server's startup model spec "provider/model" on the
// first "/". ok is false when modelSpec has no slash (including empty); the returned
// ids are then "", "". It is the single source for the provider/model pair used by
// /config/providers default, the session model, and the message model fallback.
func (s *Server) sessionModelParts() (providerID, modelID string, ok bool) {
	providerID, modelID, ok = strings.Cut(s.modelSpec, "/")
	return providerID, modelID, ok
}
```

Defined once in `serve_handlers.go`. Call sites: §3 step 5 (`/config/providers` default), §4
`buildSessionDTO` (session model), §6 `entriesToMessages` call sites (message fallback), and the
§7 mapper's captured fallback pair (read once at construction — the same message-model fallback).
This is the single message-model split point; do not inline `strings.Cut` at each site.

**Cost/tokens presence vs the serve-1 rule.** serve-1 (`BACKLOG.md:174`) says not to emit zeros
that read as real totals. This item's rule (BACKLOG 1330-1331) and the captured session both
require `cost` and `tokens` keys to exist. Presence wins for the V1 DTOs: a disk-only session
emits zeros, which is the honest "wopr does not know". This supersedes the omission for these
routes and is called out so it is deliberate, not a regression.

## 5. `GET /agent`

Replace `agentInfoDTO{Name}` with the 13-field struct in sidecar section 4. `listAgents` keeps
its four names (explore/general/propose/build), each built as
`agentInfoDTO{Name: name, Mode: "primary", Permission: []any{}, Options: map[string]any{}}`.
The null scalars are pointer fields left nil (marshals `null`); the obj/arr fields are
non-nil empties. `model` is deliberately **not** added (rejected finding 13): opencode omits it on
7 of its 14 captured agents, so the client already tolerates its absence, and the enumerated agent
delta does not include it.

## 6. Message info

`messageInfoDTO` (sidecar section 5) gains `ParentID`, `Mode`, `Agent`, `Path messagePath`,
`Cost float64`, `Tokens messageTokens`, `ModelID`, `ProviderID`, `Finish`. All obj fields are
always present. `messageTime` stays `{Created int64}` — `completed` is a scalar, absence is
allowed, and a permanently-nil `omitempty` field is dead relative to the shape requirement, so it
is **dropped** (minor 6). `partDTO` gains `Snapshot string json:"snapshot"` (always present,
`""`), following the `fileNodeDTO.Ignored` precedent at `serve_dto.go:99-101`.

### 6a. Shared builder `messageInfo` (blocker 3)

One package-level function builds the info for both the transcript and the SSE stream, so the two
cannot drift:

```go
// messageInfo builds messageInfoDTO for one message. assistant is the streamed/persisted
// assistant message, or nil for a user/toolResult/system entry (cost and tokens are then
// zero, finish is "", and the model falls back to fallbackProvider/fallbackModel).
// created is the message's created time in ms. It is called by entriesToMessages and by
// eventMapper.messageEnd.
func messageInfo(sessionID, cwd, root, entryID, role, parentID string, created int64,
	assistant *agent.AssistantMessage, fallbackProvider, fallbackModel string) messageInfoDTO
```

Semantics: `Path = messagePath{CWD: cwd, Root: root}`, `Mode`/`Agent` = `""`,
`Time = messageTime{Created: created}`. When `assistant != nil`, map the real `ai.Usage` subfields
(do not invent field names — `ai.Usage` has no `Total`, no `Cache`, and `Reasoning` is a `*int`):
- `tokens.total` <- `assistant.Usage.TotalTokens`
- `tokens.input` <- `assistant.Usage.Input`
- `tokens.output` <- `assistant.Usage.Output`
- `tokens.reasoning` <- `0`, unless `assistant.Usage.Reasoning != nil`, then `*assistant.Usage.Reasoning`
- `tokens.cache.read` <- `assistant.Usage.CacheRead`; `tokens.cache.write` <- `assistant.Usage.CacheWrite`
- `cost` <- `assistant.Usage.Cost.Total` (a `float64`)
- `providerID`/`modelID` <- `assistant.Provider`/`assistant.ModelID`, or the two fallbacks when empty
- `finish` <- `finishFromStopReason(assistant.StopReason)`

A nil `assistant.Usage` leaves `cost`/`tokens` at their zero values. When `assistant == nil`:
`Cost`/`Tokens` zero, `Finish ""`, model = the two fallbacks. `finishFromStopReason(ai.StopReason)`
maps `StopReasonToolUse -> "tool-calls"` and otherwise returns the raw string (`""` when empty).
This signature is the Rev-2 suggested one extended with `sessionID` (required by
`messageInfoDTO.SessionID`) and `created` (required by `messageTime`); both callers supply them.

### 6b. `entriesToMessages` is a package function (minor 9)

```go
// entriesToMessages projects transcript entries into messageDTOs. It reads no Server
// state: cwd/root and the model fallback are passed by the caller.
func entriesToMessages(entries []icodingagent.SessionEntry, sessionID, cwd, root,
	fallbackProvider, fallbackModel string) []messageDTO
```

It never reads `s` (the Rev-2 suggested four-arg signature extended with the two fallback strings
so `messageInfo` stays pure; see §11). Per message:
- `parentID` = `""` when `e.Base.ParentID == nil`, else `*e.Base.ParentID`.
- `created` = `parseRFC3339NanoToMillis(e.Base.Timestamp)`.
- `assistant` = `me.Message.Assistant` for assistant entries, else nil.
- Build the info with `messageInfo(...)`, including for the synthesized toolResult-as-assistant
  messages (role `"assistant"`, assistant nil, parentID from the entry).

Call sites to update (all in package `serve`):
- `sessionMessages` (`serve_handlers.go:824`): compute `root, _ := filepath.EvalSymlinks(s.services.CWD())`,
  `cwd := rec.Header.CWD`, `if cwd == "" { cwd = root }`, and `p, m, _ := s.sessionModelParts()`;
  call `entriesToMessages(entries, id, cwd, root, p, m)`.
- `assistantMessageDTO` (`serve_prompt.go:304`): `cwd := sess.Inner().CWD()` (`coding.Session.CWD()`,
  `coding/session.go:798`), fallback to the resolved root when empty, `p, m, _ := s.sessionModelParts()`.
- `readTranscriptFrom` test helper (`serve_test.go:595`): pass explicit cwd/root and `"", ""`.

**`path.cwd` source (Rev-3).** The session's own CWD is the single source on both surfaces; the
resolved server root is only the empty-value fallback. For a session the server owns,
`rec.Header.CWD` (poll) and `live.Inner().CWD()` (stream) are the same value, so the two cannot
report different `path.cwd`; §7 states the stream-side resolution.

## 7. SSE tracking

`SSE message.updated` must carry the same info shape, so the mapper needs cwd/root. In `New`,
**move the `s := &Server{...}` literal above the Translate assignment** (blocker 2): lines 50-58
become

```go
s := &Server{
	services:   h.Services(),
	host:       h,
	sessionDir: sessionDir,
	modelSpec:  modelSpec,
	ln:         ln,
}
h.Translate = s.newEventMapper
```

Make `newEventMapper` a `*Server` method (`serve_sse.go:272`; 271 is its doc comment). It resolves
cwd and root once, at construction:
- `root, _ := filepath.EvalSymlinks(s.services.CWD())`.
- the session's own cwd: `if ls, ok := s.host.Get(sessionID); ok { cwd = ls.Inner().CWD() }`
  (`coding.Session.CWD()`, `coding/session.go:798`). The host registers the session at
  `coding/host/host.go:120` before it starts the forwarder that calls `Translate` at `:122`/`:156`,
  so the lookup succeeds. If `Get` returns false or `CWD()` is `""`, fall back to `root`.

The `eventMapper` stores `cwd`, `root` and `providerID`, `modelID` (from `s.sessionModelParts()`,
used only when a streamed assistant message omits them). In `messageEnd` (`serve_sse.go:370`) build
the info through the shared builder:
`messageInfo(m.sessionID, m.cwd, m.root, entryID, "assistant", "", now, ev.Message.Assistant, m.providerID, m.modelID)`.
`ParentID` is `""` on the stream path (the entry's parent is not carried by the event).
`session.updated` already uses `sessionDTO`, so it tracks section 4 for free.

## 8. `docs/site/docs/server.md`

- Row `GET /config`: replace "`{model}` when a model is configured" with the 12-key object.
- Row `GET /config/providers`: replace "Map from provider ID to `{name, api, models}`" with the
  `{providers:[{id,name,source,env,key,options,models}], default:{}}` shape; note `key` is always `""`
  and each model value carries the full nested key set.
- Row `GET /agent`: note the full 13-field item shape (no `model`).
- Row `GET /session`, `/experimental/session`, `GET /session/{id}`: note the expanded session keys.
- Row `PATCH /session/{id}`: its 200 body is the same expanded `sessionDTO` (it flows through
  `buildSessionDTO`), so note the new keys (minor 11).
- Row `GET /session/{id}/message`: note the expanded message `info` keys and `parts[].snapshot`.
- Row `POST /session/{id}/message`: its 200 body is the same expanded `{info, parts}` shape (minor 11).
- The line at 33 that lists capture-derived rows should mention this capture too.

## 9. Model truthfulness decision (prerequisite item)

`serve-test-faux-fallback-in-production` (READY) owns making the model truthful. This spec does
**not** mask the placeholder:
- `GET /config.model` = `s.modelSpec` verbatim (unchanged).
- Session/`message` model = live `Inner().Model()` when a live session exists; else parsed
  `s.modelSpec`; else `{"id":"","providerID":"","variant":"default"}`.
- When `s.modelSpec` is the `test-faux/faux-1` fallback, `GET /config` reports
  `"test-faux/faux-1"` and sessions report `{id:"faux-1",providerID:"test-faux",variant:"default"}`
  — the fake is visible, not hidden. Making the no-model state fail or omit is that item's job.

Disk-only sessions have no model in `sessionRecord` (only name/archived/header are read by
`sessionRecordAt`, `serve_read.go:166`), so the parsed `modelSpec` is the server's current
default, not necessarily the model the session last used. Reading `model_change` entries from
the transcript is a follow-up (section 12).

## 10. Tests

All hermetic: temp `WOPR_HOME`/`AgentDir` via the existing harness, `WOPR_TEST_FAUX=1`, no
credentials/network. Each test decodes **actual response bytes** into `map[string]json.RawMessage`
(nested) and asserts the captured keys exist with the right JSON kind — no golden/snapshot dumps,
no `reflect.DeepEqual` on whole structs. Create a session first where a populated provider list
is needed (with no live session, `AvailableModels(_, nil)` returns no providers because
`test-faux` is never authenticated and `GetAvailable` excludes it).

New test names (all in `serve_test.go`):
1. `TestSessionShapeMatchesOpencode` — `GET /session`, `GET /session/{id}`, `POST /session`, `GET
   /experimental/session` (alias), and `PATCH /session/{id}` (both reachable bodies flow through
   `buildSessionDTO`): assert every session key/kind from sidecar section 3, including `model` as
   an object with non-null `id/providerID/variant`, `summary`/`project` objects, and `tokens` with
   `reasoning`/`total`/`cache`. Drive one disk-only session (restart server) and one live session
   to cover both model sources.
2. `TestConfigProvidersShapeMatchesOpencode` — create a session, then `GET /config/providers`:
   assert top-level `providers` is an array and `default` an object; for each provider assert
   `id/name/source/key` strings, `env` array, `options`/`models` objects; assert `key == ""`; and
   assert each `models` value has the full nested key set (sidecar section 2): string
   `id/providerID/name/family/status/release_date` and object `api/capabilities/cost/limit/options/headers/variants`.
3. `TestConfigShapeMatchesOpencode` — `GET /config`: assert all 12 keys with sidecar section 1
   kinds; assert `model == "test-faux/faux-1"` here (this folds in and replaces
   `TestConfigAnswersAnObject`, which is deleted), and `command/mode/agent/provider/mcp/permission`
   objects, `plugin/instructions` arrays (never null).
4. `TestAgentShapeMatchesOpencode` — `GET /agent`: assert a non-empty array; each item has
   `name/description/mode` strings, `native` bool, `permission` array, `options` object, the scalar
   keys `hidden/topP/temperature/color/variant/prompt/steps` present as their kinds, and **no**
   `model` key.
5. `TestMessageInfoShapeMatchesOpencode` — run a prompt, then `GET /session/{id}/message` and
   `POST /session/{id}/message`: for each item assert `info` has every section 5 key with the
   right kind, including `path` object with string `cwd`/`root`, `tokens` object with numeric
   `total/input/output/reasoning` and `cache` object, `time` object with numeric `created`,
   `parentID/mode/agent/finish` strings, and `parts` an array whose items carry string
   `id/sessionID/messageID/type` plus a `snapshot` key.
6. `TestSSEMessageUpdatedShapeMatchesOpencode` — open `/global/event`, run a prompt, capture the
   `message.updated` frame, and assert its `info` carries the same section 5 key set (proves §7).

**Mandatory test replacements (minor 7):** delete the DTO round-trip tests
`TestSessionDTODecode`, `TestAgentInfoDTODecode`, `TestProviderDTODecode` from
`serve_dto_test.go`; their struct literals go stale and the route shape tests above supersede
them. Keep `TestRequestBodyFieldsAreBuiltins`. Delete `TestConfigAnswersAnObject` (folded into
test 3). Update the `readTranscriptFrom` helper for the new `entriesToMessages` signature.

## 11. Decisions and why

- **`sessionModelParts` is the single split point (blocker 1).** One method, three consumers; no
  inlined `strings.Cut`.
- **`New` creates `s` before wiring `Translate` (blocker 2).** The method value `s.newEventMapper`
  requires `s`.
- **One `messageInfo` builder (blocker 3).** Both `entriesToMessages` and `eventMapper.messageEnd`
  call it, so `Path`/`Tokens`/`Cost`/`Finish` cannot drift between poll and stream; `path.cwd` is
  the session's own CWD on both surfaces (§6b/§7) with the resolved server root as the shared
  empty-value fallback.
- **`entriesToMessages` takes the model fallback explicitly.** It is a package function that reads
  no `Server` state (minor 9), yet a message's model must fall back to the server's spec (§9), so
  the two fallback strings are threaded through rather than read from `s`. This is the internal
  contradiction the Rev-2 review asked to check: a purely four-arg function cannot populate the
  fallback. The fix extends the signature by two strings; the alternative (a `*Server` wrapper that
  reads `s.modelSpec`) re-couples the function to the zero `Server{}` used by `readTranscriptFrom`,
  so it was rejected.
- **Nested provider-model objects are emitted (minor 5).** The captured model object has six obj
  fields; a strict client could `Object.keys` them just like the top-level fields. Empty `{}` / `""`
  placeholders cost one small struct and remove the largest residual crash risk. No real values are
  fabricated.
- **Empty composites, never nil maps.** `map[string]any(nil)` marshals `null`; the providers slice
  is `make([]providerDTO, 0, len(ids))`; every obj field is initialized. This is the point of the item.
- **`snapshot` and `tokens.total` kept (rejected finding 14).** BACKLOG 1329 and 1322 name both.
- **`messageTime.Completed` dropped (minor 6).** Scalar, always nil, never reaches the wire.
- **agent `model` omitted (rejected finding 13).** 7 of 14 captured agents have no `model`.
- **SSE mapper refactor stays (rejected finding 15).** Per-session cwd/root requires it.
- **No `coding` import.** Everything routes through `codinghost` (`AvailableModels`,
  `ModelProviderID`) and `icodingagent`; the seam stays in `coding/host`.

## 12. Follow-ups (do not build here)

- `serve-test-faux-fallback-in-production` — make `/config.model` truthful when startup resolves no model.
- Read the newest `model_change` entry (`internal/codingagent/session.go:64`) for disk-only
  sessions so `session.model` reflects the session's own last model, not the server default.
- Consider adding `Reasoning` and per-message reasoning to `SessionTokenStats` if session-level
  `tokens.reasoning` proves to matter to the client.
- A truthful `source` (local vs api) if the client distinguishes providers.
