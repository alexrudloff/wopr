# Shape sidecar — serve-kkcode-dto-shape-parity

Companion to `2026-10-07-serve-kkcode-dto-shape-parity.md`. This file holds the
per-route key/type tables extracted from `/tmp/opencode-shapes/TARGET_SHAPES.md`
and the raw captures, plus what wopr emits for each key and the Go type that
produces it.

Types are JSON types: `obj`, `arr`, `str`, `num`, `bool`, `null`. The
load-bearing rule (BACKLOG 1330-1331): **every obj/arr field the captured shape
has must be present with the right JSON type even when wopr has no data.**
`{}` / `[]` / `0` / `""` is safe; absent (undefined) is not. Scalars may be
absent or null. A Go nil map marshals `null`, not `{}`, so every obj field is
initialized.

Legend for the "wopr value" column:
- `""` / `0` / `false` = deliberate honest placeholder, no wopr source.
- `live` = from the live session; `disk` = from the session record / server spec.

---

## 1. `GET /config` — `config.json`

| key | type | wopr value | Go |
|---|---|---|---|
| `$schema` | str | `""` | `Schema string` |
| `command` | obj | `{}` | `Command map[string]any` |
| `plugin` | arr | `[]` | `Plugin []any` |
| `model` | str | `s.modelSpec` | `Model string` |
| `subagent_depth` | num | `0` | `SubagentDepth int` |
| `username` | str | `""` | `Username string` |
| `mode` | obj | `{}` | `Mode map[string]any` |
| `agent` | obj | `{}` | `Agent map[string]any` |
| `provider` | obj | `{}` | `Provider map[string]any` |
| `mcp` | obj | `{}` | `MCP map[string]any` |
| `instructions` | arr | `[]` | `Instructions []any` |
| `permission` | obj | `{}` | `Permission map[string]any` |

Captured scalar values (informational): `model` = `"opencode-go/deepseek-v4.1-flash"`,
`subagent_depth` = `2`, `username` = `"kc"`, `plugin` = a list of plugin file URLs,
`instructions` = a list of instruction file paths. wopr has no equivalent for any
of these except `model`. `directory` is ignored on this route.

## 2. `GET /config/providers` — `config_providers.json`

Top level:

| key | type | wopr value | Go |
|---|---|---|---|
| `providers` | arr of provider obj | sorted by `id`; one per provider in `AvailableModels` (plus the live session's current model) | `Providers []providerDTO` |
| `default` | obj (map providerID -> modelID) | parsed from `s.modelSpec`; `{}` when `modelSpec` has no `/` | `Default map[string]string` |

`provider` object:

| key | type | wopr value | Go |
|---|---|---|---|
| `id` | str | provider id (`codinghost.ModelProviderID`) | `ID string` |
| `name` | str | provider id (no display name in the `ai.Provider` interface) | `Name string` |
| `source` | str | constant `"api"` | `Source string` |
| `env` | arr of str | `[]` | `Env []string` |
| `key` | str | **always `""`** — never a real credential | `Key string` |
| `options` | obj | `{}` | `Options map[string]any` |
| `models` | obj (map modelID -> model obj) | one entry per model of that provider | `Models map[string]providerModelDTO` |

`model` object — the full captured key set; wopr fills `id`/`providerID`/`name`
and every other field is an empty composite or zero scalar, never fabricated
(Rev-2, resolves the old Open Question 1):

| key | type | wopr value | Go |
|---|---|---|---|
| `id` | str | `m.ID` | `ID string` |
| `providerID` | str | provider id | `ProviderID string` |
| `api` | obj | `{}` | `API map[string]any` |
| `name` | str | `m.DisplayName` | `Name string` |
| `family` | str | `""` | `Family string` |
| `capabilities` | obj | `{}` | `Capabilities map[string]any` |
| `cost` | obj | `{}` | `Cost map[string]any` |
| `limit` | obj | `{}` | `Limit map[string]any` |
| `status` | str | `""` | `Status string` |
| `options` | obj | `{}` | `Options map[string]any` |
| `headers` | obj | `{}` | `Headers map[string]any` |
| `release_date` | str | `""` | `ReleaseDate string` |
| `variants` | obj | `{}` | `Variants map[string]any` |

Captured `default` is a map with one entry per provider that has a default
(e.g. `{"opencode-go":"gpt-5.6-luna", ...}`). `source` was `"api"` in all three
captured providers; `env` held real env-var names, `key` a redacted real key.

## 3. session object — `session_list.json` (list / experimental / get / create / PATCH / `session.updated` SSE)

| key | type | wopr value | Go |
|---|---|---|---|
| `id` | str | `rec.ID` | `ID string` |
| `slug` | str | `rec.ID` | `Slug string` |
| `projectID` | str | `projectID(root)` | `ProjectID string` |
| `directory` | str | `rec.Header.CWD`, else root | `Directory string` |
| `path` | str | `""` | `Path string` |
| `title` | str | `rec.Name` | `Title string` |
| `agent` | str | `""` (no per-session agent in wopr) | `Agent string` |
| `model` | obj | live `Model()` when live; else parsed `s.modelSpec` | `Model sessionModel` |
| `model.id` | str | model id | `ID string` |
| `model.providerID` | str | provider id | `ProviderID string` |
| `model.variant` | str | `"default"` constant | `Variant string` |
| `version` | str | `version.Version` | `Version string` |
| `summary` | obj | zeros | `Summary sessionSummary` |
| `summary.additions` | num | `0` | `Additions int` |
| `summary.deletions` | num | `0` | `Deletions int` |
| `summary.files` | num | `0` | `Files int` |
| `cost` | num | live accounting cost; `0` when unknown | `Cost float64` |
| `tokens` | obj | live accounting; zeros when no live session | `Tokens sessionTokens` |
| `tokens.input` | num | `acc.Tokens.Input` | `Input int` |
| `tokens.output` | num | `acc.Tokens.Output` | `Output int` |
| `tokens.reasoning` | num | `0` (session accounting does not track reasoning; see spec follow-up) | `Reasoning int` |
| `tokens.total` | num | `acc.Tokens.Total` | `Total int` |
| `tokens.cache` | obj | `{read,write}` | `Cache tokenCache` |
| `tokens.cache.read` | num | `acc.Tokens.CacheRead` | `Read int` |
| `tokens.cache.write` | num | `acc.Tokens.CacheWrite` | `Write int` |
| `time` | obj | `{created,updated}` | `Time sessionTime` |
| `time.created` | num | header timestamp | `Created int64` |
| `time.updated` | num | mtime, else created | `Updated int64` |
| `time.archived` | num | omitempty, unchanged | `Archived *int64` |
| `project` | obj | `{id,worktree}` | `Project projectDTO` |
| `project.id` | str | `projectID(root)` | `ID string` |
| `project.worktree` | str | resolved root | `Worktree string` |

Captured `model.variant` is `"default"`. Captured `tokens` has **no** `total` key;
this spec adds it because BACKLOG 1322 names `reasoning`/`total` and it is a safe
scalar. `session_list[1]` (a child session) additionally carries `parentID str`
and `permission arr`; `session_list[0]` (top level) does not, so they are
conditional and **out of scope**.

## 4. `GET /agent` item — `agent.json` (14 items)

| key | type | wopr value | Go |
|---|---|---|---|
| `name` | str | one of explore/general/propose/build | `Name string` |
| `description` | str | `""` | `Description string` |
| `mode` | str | `"primary"` | `Mode string` |
| `native` | bool | `false` | `Native bool` |
| `hidden` | null | `null` | `Hidden *string` |
| `topP` | null | `null` | `TopP *float64` |
| `temperature` | null | `null` | `Temperature *float64` |
| `color` | null | `null` | `Color *string` |
| `permission` | arr of obj | `[]` | `Permission []any` |
| `variant` | null | `null` | `Variant *string` |
| `prompt` | null | `null` | `Prompt *string` |
| `options` | obj | `{}` | `Options map[string]any` |
| `steps` | null | `null` | `Steps *int` |

Captured names: build, architect, compaction, explore, general, implementer,
mechanic, orchestrator, plan, qa, summary, title, verifier, wave-runner. Captured
`permission` is an array of `{permission,pattern,action}` objects. `model` is a
key on some captured agents but is not in the enumerated delta.

## 5. message info — `message.json` (`GET /session/{id}/message`, `POST /session/{id}/message`, `message.updated` SSE)

| key | type | wopr value | Go |
|---|---|---|---|
| `id` | str | entry id | `ID string` |
| `sessionID` | str | session id | `SessionID string` |
| `role` | str | user/assistant/toolResult/system | `Role string` |
| `parentID` | str | `*e.Base.ParentID`, else `""` | `ParentID string` |
| `mode` | str | `""` | `Mode string` |
| `agent` | str | `""` | `Agent string` |
| `path` | obj | `{cwd,root}` | `Path messagePath` |
| `path.cwd` | str | session CWD, else root | `CWD string` |
| `path.root` | str | resolved server root | `Root string` |
| `cost` | num | assistant `Usage.Cost.Total`, else `0` | `Cost float64` |
| `tokens` | obj | assistant `Usage`, else zeros | `Tokens messageTokens` |
| `tokens.total` | num | `Usage.TotalTokens` | `Total int` |
| `tokens.input` | num | `Usage.Input` | `Input int` |
| `tokens.output` | num | `Usage.Output` | `Output int` |
| `tokens.reasoning` | num | `*Usage.Reasoning`, else `0` | `Reasoning int` |
| `tokens.cache` | obj | `{write,read}` | `Cache tokenCache` |
| `modelID` | str | assistant `ModelID`, else session model id | `ModelID string` |
| `providerID` | str | assistant `Provider`, else session provider id | `ProviderID string` |
| `time` | obj | `{created}` (`completed` is scalar and may be absent, so it is dropped) | `Time messageTime` |
| `time.created` | num | entry timestamp | `Created int64` |
| `finish` | str | mapped from assistant `StopReason` | `Finish string` |

`finish` mapping: `toolUse -> "tool-calls"`; otherwise the raw `StopReason`
string (`"stop"`, `"length"`, `"error"`, `"aborted"`, `"deferred"`); empty when no
assistant message. The captured in-progress message had `finish` absent.

### part object

| key | type | wopr value | Go |
|---|---|---|---|
| `id` | str | `partID(...)` | `ID string` |
| `sessionID` | str | session id | `SessionID string` |
| `messageID` | str | entry id | `MessageID string` |
| `type` | str | text/reasoning/tool/file | `Type string` |
| `text` | str | omitempty | `Text string` |
| `tool` | str | omitempty | `Tool string` |
| `callID` | str | omitempty | `CallID string` |
| `mime` | str | omitempty | `Mime string` |
| `url` | str | omitempty | `URL string` |
| `state` | obj | omitempty, tool only | `State json.RawMessage` |
| `snapshot` | str | **always `""`** (BACKLOG 1329) | `Snapshot string` |

Captured part types: reasoning, step-start, step-finish, text, tool. Only
`step-start` carries `snapshot`. wopr emits no step-* parts; `snapshot` is added
always-present so a strict (non-optional) part decoder finds it, following the
`fileNodeDTO.Ignored` precedent. Captured `reasoning`/`text` parts also carry a
`time obj`; wopr does not emit part-level `time` and the enumerated delta does not
require it.
