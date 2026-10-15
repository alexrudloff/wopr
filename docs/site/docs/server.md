# WOPR server

`wopr serve` exposes the session in the working directory over a local HTTP and SSE API that speaks the opencode client contract. The API is unauthenticated and has no TLS. This page is the contract it serves and the warning that comes with it.

## Start the server

```text
wopr serve [--port N] [--hostname H] [--dir PATH]
```

`wopr serve` binds one address and serves the launch directory (`--dir`, default the current directory). The flags are optional:

- `--dir` sets the directory to serve; it defaults to the current directory.
- `--port` overrides `server.port`, whose default is `4096`.
- `--hostname` overrides `server.hostname`, whose default is `127.0.0.1`.

A client's `directory` query validates against the served root (400 on a mismatch) on the session and file routes; it does not select a directory. The root-independent routes `GET /api/health`, `GET /config`, and `GET /api/command` ignore `directory`, so a stale or unnormalized value on those cannot break a connect. `server.allowedRoots` restricts which directories `wopr serve` may serve — a global-only launcher guardrail against misconfiguration, not a network boundary. See [settings](settings.md#server).

The process prints `wopr serve listening on http://<addr>` once it is up. Set the values in [settings](settings.md#server) to make the choice persistent.

## No authentication

> A served session is a remote shell for anyone who can reach the port.

The server has no authentication and no authorization. Every route that runs a tool or reads a file is reachable by anyone who can open the socket, and the process runs with your user's permissions. When the bind address is not loopback, the process prints:

> wopr serve: no authentication. Anyone who can reach <addr> can run tools and read files as this user. Binding to a LAN is not a security control.

The only confinement on file access is the resolved project root: `GET /file` and `GET /file/content` reject a path that leaves the root, including one that escapes through a symlink. That is a path check, not network isolation. See the [remote serve boundary](../../threat-model.md#remote-serve-boundary) in the threat model.

## Client

The endpoint table below was derived from `grapeot/opencode_ios_client` at commit `2d0e7bd`, except the `GET /api/health`, `GET /experimental/session`, `GET /config`, `GET /config/providers`, `GET /agent`, the session routes, the message routes, `GET /api/command`, and `GET /permission` rows, which come from logging-proxy captures of KKCode against opencode 1.18.34 recorded in the `serve-kkcode-v1-compat` and `serve-kkcode-dto-shape-parity` backlog items. It is a third-party build with no SLA; if the app moves, this table can drift, and the DTOs stay tolerant of added optional fields.

## Endpoints

The server registers 34 routes. Thirty are implemented; four answer `501` with `{"error":"not implemented"}` for an operation the client can request but the server does not implement yet. A `501` is a supported answer the client treats as not implemented.

| Method | Path | Status | Behavior |
|---|---|---|---|
| `GET` | /global/health | 200 | `{healthy: true, version}`. The client's Test connection call. |
| `GET` | /api/health | 200 | `{healthy: true}`, no `version`. KKCode's first connect probe. `directory` ignored. |
| `GET` | /global/event | SSE | One event stream for every session: `server.connected` first, then `server.heartbeat` every 15 seconds, and a terminal `session.error` with `data: [DONE]` when the byte budget drops. Answers 503 past 16 concurrent streams. |
| `GET` | /session | 200 | `sessionDTO[]` merged from disk and live sessions. Each entry carries `{id, slug, projectID, directory, path, title, agent, model:{id,providerID,variant}, version, summary:{additions,deletions,files}, cost, tokens:{input,output,reasoning,total,cache:{read,write}}, time, project:{id,worktree}}`; `model`, `summary`, `tokens`, and `project` are objects even when unknown. Query `directory`; `limit` default 50, max 200; 400 on a directory mismatch. |
| `GET` | /experimental/session | 200 | Same `sessionDTO[]` keys as `GET /session`, but archived sessions are omitted; `directory` validated, `limit` default 50, max 200. |
| `POST` | /session | 200 | Starts a host session, installs the `ask_user` asker, and returns the expanded `sessionDTO`; 500 when the server has no usable model. No `directory` parameter. |
| `GET` | /session/{id} | 200 | The expanded `sessionDTO`; 404 unknown. |
| `PATCH` | /session/{id} | 200 | Body is exactly one of `{title}` or `{time:{archived}}`; renames or archives, returns the same expanded `sessionDTO`, and publishes `session.updated`; 400, 404, or 500. |
| `DELETE` | /session/{id} | 200 | Archives the session (the same `session_archive` write as `PATCH {time:{archived}}`) and returns `true`; transcript files are never removed; 404 unknown. |
| `POST` | /session/{id}/fork | **501 stub** | Not implemented. |
| `POST` | /session/{id}/revert | **501 stub** | Not implemented. |
| `GET` | /session/{id}/diff | **501 stub** | Not implemented. |
| `GET` | /session/{id}/todo | 200 | `todos[]` from the newest `todowrite` result in the transcript, `[]` when there is none; 404 unknown. |
| `GET` | /session/{id}/message | 200 | `MessageWithParts[]`; each item's `info` carries `{id, sessionID, role, parentID, mode, agent, path:{cwd,root}, cost, tokens:{total,input,output,reasoning,cache:{read,write}}, modelID, providerID, time:{created}, finish}` and each part carries `snapshot` (`""` when wopr has none); a user message whose prompt set `system` also carries `system`, omitted when empty; `limit` default 50, max 200; 404 unknown. |
| `GET` | /session/status | 200 | `sessionStatusDTO[]` (`idle` or `busy`) for live sessions. |
| `POST` | /session/{id}/prompt_async | 204 | Reserves the run slot and returns no body; a prompt while a run is in flight is queued to run next, also 204; 404 when the session was not started here, 409 only while an `ask_user` question is pending, 400 bad directory or body or a model this server does not offer, 413 over 16 MiB. A body `model` swaps the session onto that model once the run holds the slot, so a queued prompt does not change the model of the turn it is queued behind. A body `system` appends to that turn's system prompt and is echoed on the user message; an empty `system` is treated as absent. Query `directory`. |
| `POST` | /session/{id}/message | 200 | Runs to completion and returns the expanded `{info, parts}` (same info keys as `GET /session/{id}/message`); unlike `prompt_async` it blocks for the run slot rather than queueing, so a busy session is 409; 400 for a bad model, bad directory, or malformed body, 404 unknown, 413 over 16 MiB. A body `system` appends to that turn's system prompt and is echoed on the user message; an empty `system` is treated as absent. Query `directory`. |
| `POST` | /session/{id}/abort | 200 | Cancels a run; 200 whether it is running or idle, 400 on a directory mismatch, 404 when the session was not started here. Query `directory`. |
| `POST` | /session/{id}/permissions/{permissionID} | **501 stub** | Not implemented. The wildcard is `permissionID` because `{id}` is already used in the pattern. |
| `GET` | /project | 200 | `[projectDTO{ID, Worktree}]` for the server root. |
| `GET` | /project/current | 200 | `projectDTO`. |
| `GET` | /config | 200 | The captured 12-key object: `{$schema, command, plugin, model, subagent_depth, username, mode, agent, provider, mcp, instructions, permission}`; `model` is the server's spec verbatim, obj/arr fields are `{}`/`[]` when wopr has no data. No credentials or secrets. `directory` ignored. |
| `GET` | /config/providers | 200 | `{providers: [{id, name, source, env, key, options, models}], default: {}}`; `providers` is sorted by `id` and always an array, `default` maps provider ID to the parsed model ID (or `{}`), and `key` is always `""` — never a real credential. Each model value carries the full nested key set (`{id, providerID, api, name, family, capabilities, cost, limit, status, options, headers, release_date, variants}`) so no nested object is `undefined`. |
| `GET` | /api/command | 200 | `{location: {directory, project: {id: "global", directory}}, data: []}`; both directories are the resolved served root, and `data` is always `[]` (no built-in slash commands). `directory` ignored. |
| `GET` | /provider | 200 | `string[]` of authenticated provider names. |
| `GET` | /agent | 200 | Static `agentInfoDTO[]` for `explore`, `general`, `propose`, `build`; each item carries the captured 13 keys `{name, description, mode, native, hidden, topP, temperature, color, permission, variant, prompt, options, steps}` (obj/arr fields `{}`/`[]`, null scalars `null`) and deliberately no `model` key. |
| `GET` | /permission | 200 | `[]`. Every tool is open by default, so there are never pending requests. |
| `GET` | /question | 200 | Pending `questionDTO[]`, `[]` when there is none. Never 404; that status means the host is gone. |
| `POST` | /question/{id}/reply | 200 | Body is exactly one of `choice` or `other`; 404 gone, 400 invalid, 413 over 16 MiB; publishes `question.replied`. |
| `POST` | /question/{id}/reject | 200 | No body read; 404 gone; publishes `question.rejected`. |
| `GET` | /file | 200 | `fileNodeDTO[]` directory listing inside the project root; 403 outside the root, 404 missing, 400 not a directory. Query `path`, `directory`. |
| `GET` | /file/content | 200 | `{"content": …}` for a text file inside the root; 403 outside the root, including through a symlink, 404 missing, 400 directory or non-regular file, 413 over 8 MiB, 415 non-text, 500 read failure. Query `path`, `directory`. |
| `GET` | /find/file | 200 | `fileNodeDTO[]` walk of the root, skipping dot-entries and `node_modules`; `query`, `limit` default 50, max 100. |
| `GET` | /file/status | 200 | `fileStatusDTO[]` for the working tree, at most 500 entries. |

## Events

The stream on `GET /global/event` carries 12 of the 17 event names the client switches on.

| Event | When |
|---|---|
| `server.connected` | First frame on each `GET /global/event` connection. |
| `server.heartbeat` | Every 15 seconds while the stream is idle. |
| `message.updated` | An assistant message ends, under its transcript ID. |
| `message.part.updated` | A part starts or ends; tool snapshots and the reconciliation burst. |
| `message.part.delta` | Streamed text, reasoning, or tool-argument deltas. |
| `session.updated` | `PATCH /session/{id}` renames or archives a session. |
| `session.error` | A run, assistant, compaction, or auto-retry failure, and the terminal byte-bound drop. |
| `session.status` | `busy`, `idle`, or `retry` around a run. |
| `todo.updated` | A successful `todowrite` result. |
| `question.asked` | `ask_user` blocks; the server publishes a pending question. |
| `question.replied` | `POST /question/{id}/reply` is accepted. |
| `question.rejected` | `POST /question/{id}/reject`, or a run aborted while a question is pending. |

Five names have no live trigger and are deliberately not emitted:

- `permission.asked`, `permission.replied` — no approval policy or blocking hook exists.
- `session.deleted` — `DELETE /session/{id}` archives rather than deletes, so nothing is removed and the event has no trigger.
- `message.removed`, `message.part.removed` — nothing removes a message or a part; a failed tool sets `state.status` to `error` on a part that stays.

## Limits

| Bound | Value |
|---|---|
| Request body | 16 MiB |
| `GET /file/content` file | 8 MiB |
| `GET /file` directory listing | 500 entries |
| `GET /file/status` | 500 entries |
| `GET /find/file` walk | 20000 entries |
| Concurrent `GET /global/event` streams | 16 (further connections answer 503) |
| Heartbeat on an idle stream | 15 seconds |

## What is not served

The server is not a hardened network service. Accepted: no authentication, no TLS, no CORS policy, no rate limiting, no audit log, and no SSE replay, so a dropped stream does not resume with `Last-Event-ID`. One server process serves one working directory. These are recorded in the [threat model](../../threat-model.md#security-assumptions).
