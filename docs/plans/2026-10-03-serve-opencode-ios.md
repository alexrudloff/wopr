# `wopr serve`: an HTTP/SSE server the opencode iOS app can drive

Date: 2026-10-03
Status: rewritten after a 4-dispatch verification fan (verdict: 15 blockers — BLOCK).
Supersedes the 2026-10-03 first draft, whose central architectural claim was falsified.

## Goal

One new command, `wopr serve`, running a wopr session on a machine, driven from an
iPhone over the LAN or a VPN. Chat with live streaming, tool cards, file browsing,
diffs, todos, and the agent asking you questions. No app changes.

Effort is not a constraint on this work. Correctness is.

## What the verification fan changed

The first draft claimed the `--mode rpc` dispatcher could be reused verbatim as the
server engine, with no changes to `agent/`, `ai/`, `coding/`, or `tui/`. Four
independent read-only dispatches (runtime-seam, client-contract, test-oracle,
leanness) refuted it. Findings that reshaped this plan:

- **There is no dispatcher to reuse.** The 36-case switch is inlined in
  `runRPCMode`, a ~686-line body closing over ~20 locals and 87 `writeRPC` call
  sites, returning an `int` its only caller passes to `os.Exit`
  (`cmd/wopr/rpc_mode.go:163`, `cmd/wopr/main.go:683`). Every piece is unexported in
  `package main`, which cannot be imported.
- **`writeRPC` closes over `os.Stdout`.** No injectable writer, so the serialize-once
  fan-out the memory budget relied on had no seam to attach to.
- **RPC mode is one `coding.Session` per process,** reassigned in place, with no
  session id on any command, response, or event. Multi-session `/session` CRUD and
  per-session status cannot be expressed in it.
- **Abort and settle are process-global,** so the per-session concurrency rule had no
  implementation and no owner.
- **The 16 MiB RPC command cap the draft said to "reuse" does not exist.**
  `readJSONLLines` is `bufio.NewReader` + `ReadBytes('\n')`, unbounded
  (`cmd/wopr/rpc_mode.go:961`).
- **The OOM diagnosis was half wrong.** The storage half is confirmed and reproduced.
  The causal claim — replaying a session's events exhausts memory — is unsupported
  and contradicted on this host: `oom 0 oom_kill 0` in the unit's cgroup, zero heap
  or fatal errors in 723,994 log lines, and the resident set is page cache
  (anon 2.56 GB versus file 14.28 GB). We are replacing opencode, not debugging it.
  The new design therefore optimizes for bounded, predictable memory rather than
  for one specific past failure.
- **The client contract is larger than the draft's table:** 26 paths but 29
  operations, 17 handled SSE events (not 16), and an uncovered status-code,
  query-parameter, and required-field contract.
- **Two engine surfaces the client needs do not exist in wopr at all:** a todo
  source (`todo` appears once in non-test code, inside a prompt example) and a
  message-level revert (`coding/session_undo.go` exports nothing).
- **The draft's fix for the loader would have broken writes.** `raw` has 26 consumers
  across 7 files, two write-critical: `SessionEntry.MarshalJSON` returns it verbatim,
  and the full-file flush reads every `raw` under a held mutex
  (`internal/codingagent/session.go:254`, `:534`).
- **The draft pointed at the smaller of two greedy reads.** `summarizeSessionFile`
  retains `AllMessagesText` — every message's text of every session listed
  (`internal/codingagent/session_resume.go:289`, `:337`).

Confirmed accurate by the fan and carried forward unchanged: every `file:line` the
draft cited is exact; `message_update` genuinely carries a single delta, not a
snapshot (`cmd/wopr/rpc_events.go:158` deletes `partial`); the storage measurements
reproduce against the live database including the 14,671,844-byte maximum; and a
backward newline-boundary tail read is implementable, because every write marshals
with Go's encoder, which escapes newlines inside strings.

## Architecture

Three layers, each depending only on the one below.

```
cmd/wopr/serve.go              wiring and flags; no logic
internal/codingagent/serve/   HTTP + SSE transport, opencode DTOs, path allowlist
coding/host/                   multi-session supervisor and typed event bus
```

`coding/host` holds the seam that does not exist yet: a supervisor owning N
`*coding.Session` values, routing a command to one, and publishing typed events to
subscribers. It imports `coding` (legal — `coding` does not import it) and knows
nothing about HTTP or opencode.

`internal/codingagent/serve` imports `coding/host` and owns transport and the wire
contract only. It does **not** import `coding` directly: `coding/services.go:18`
already imports `internal/codingagent`, so anything there importing `coding` would
cycle. The draft's placement of the transport inside `internal/codingagent` with
direct engine access was not viable.

`coding.StartSession(svcs, opts)` is a free function over a shared `*Services`
(`coding/runtime.go:66`), and `Services` holds no session — only getters. Multi-session
is already supported at that layer; the supervisor is thin. Note the cost: one Session
owns one Model Runtime (`coding/AGENTS.md`), so N live sessions mean N runtimes.

**`--mode rpc` is re-pointed at `coding/host` in the same change.** This is what fixes
the "no reusable dispatcher" blocker by construction: one dispatcher, two clients.
Its wire protocol and its tests must pass unchanged — that is the acceptance
criterion, not a rewrite.

`InteractiveMode` keeps its direct path. It works, and routing the TUI through a
transport-shaped seam would churn 28 files for no gain. Out of scope, deliberately.

## Transport

Stdlib `net/http`. SSE is a `http.Flusher` loop. The vendored `gorilla/websocket` is
not used: the client speaks SSE only, with no websocket fallback anywhere in
`SSEClient.swift`.

The event bus serializes each event once and hands the same `[]byte` to every
subscriber, so fan-out cost is per-subscriber pointer copy, not per-subscriber
marshal.

## Client contract

Re-derived from the client source, not from opencode's documentation. Reference:
`grapeot/opencode_ios_client`, `OpenCodeClient/Services/APIClient.swift` and
`SSEClient.swift`. Restate if the app moves.

26 paths, 29 operations. Paths the app never calls are omitted; operations that share
a path with a different body or query are listed separately.

| Method | Path | Notes |
|---|---|---|
| GET | `/global/health` | `{healthy, version}`; the app's Test connection |
| GET | `/global/event` | SSE, `data: {json}`, `[DONE]` sentinel |
| GET | `/session` | query `directory`, `limit` |
| POST | `/session` | query `directory` |
| GET | `/session/{id}` | |
| PATCH | `/session/{id}` | two bodies: `{title}` rename, `{time:{archived:N}}` archive |
| DELETE | `/session/{id}` | 501 until implemented |
| GET | `/session/{id}/message` | query `limit`; returns `MessageWithParts[]`, not `Message[]` |
| POST | `/session/{id}/message` | structured output only: `system`, `format`, `agent`, `model` |
| POST | `/session/{id}/prompt_async` | query `directory`; **204** on success |
| POST | `/session/{id}/abort` | **200** on success |
| GET | `/session/status` | |
| GET | `/session/{id}/diff` | query `messageID` optional |
| GET | `/session/{id}/todo` | |
| POST | `/session/{id}/fork` | 501 until implemented |
| POST | `/session/{id}/revert` | 501 until implemented |
| GET | `/project`, `/project/current` | |
| GET | `/config/providers`, `/provider`, `/agent` | model and agent pickers |
| GET | `/file` | query `path`, `directory` |
| GET | `/file/content` | query `path`, `directory` |
| GET | `/file/status` | |
| GET | `/find/file` | query `query`, `limit` |
| GET | `/permission` | |
| POST | `/session/{id}/permissions/{id}` | |
| GET | `/question` | |
| POST | `/question/{id}/reply`, `/question/{id}/reject` | |

Status codes are contract, not detail: the app branches on 404 (session or host gone),
401 (auth), 204 (`prompt_async`), 200 (`abort`), and treats 501 as not-implemented
(`APIClient.swift:375-383`, `AppState+ClientCapabilities.swift:194`). 501 is a
supported answer, which is what makes `/revert`, `/fork`, and `DELETE` honest rather
than absent.

SSE events, all 17 the app switches on:
`server.connected`, `server.heartbeat`, `message.updated`, `message.part.updated`,
`message.part.delta`, `message.part.removed`, `message.removed`, `session.updated`,
`session.deleted`, `session.error`, `session.status`, `todo.updated`,
`permission.asked`, `permission.replied`, `question.asked`, `question.replied`,
`question.rejected`.

The client sends `Accept: application/json` and `Content-Type: application/json` on
REST and `Accept: text/event-stream` plus `Cache-Control: no-cache` on SSE. Answer all
four. It has no `Last-Event-ID` handling and no replay: reconnection is exponential
backoff only, so it recovers by re-fetching history. That is why replay is not built.

### Required wire fields

The app's decoders are tolerant of absent optionals and of unknown keys, but not of
absent requireds. These are required and must be emitted:

| Type | Required |
|---|---|
| `Message` | `id`, `sessionID`, `role`, `time` |
| `Part` | `id`, `messageID`, `sessionID`, `type` |
| `PartStateBridge` | `state` must be a valid object or string; all inner fields optional |
| `Session` | `id`, `slug`, `projectID`, `directory`, `title`, `version`, `time` |
| `SessionStatus` | `type` (`idle` \| `busy` \| `retry`) |
| `AgentInfo` | `name` |
| `Project` | `id`, `worktree` |
| `TodoItem` | `id`, `content`, `status`, `priority` (each has a decoder fallback) |

`Session.slug` and `Session.version` are non-optional in the client and have no wopr
equivalent. Both must be emitted; the plan originally said to derive both deterministically
from the session ID, and that is amended by decision: `slug` **is** the session ID (a
UUIDv7, already URL-safe, so `GET /session/{slug}` and `GET /session/{id}` are one request),
while `version` **emits the build constant** `coding/version.Version`. The stated purpose was
stability across reconnects, which the constant satisfies by construction — the derivation
was a means the plan did not need, and a hash of an ID presented as a version is a value
with no meaning in a field the client requires. Evidence and rejected alternatives:
`BACKLOG.md`, serve-3's `Session.version` entry. `session.tokens` and `session.cost` are
optional and cumulative; emit them or omit them, do not emit zeros that read as real totals.

Auth is optional in the client: `Authorization` is set only when both a username and a
password are stored (`APIClient.swift:75`). This plan serves without auth on a trusted
network. See Security for why that constrains slice 6.

## New engine surfaces

Two things the client needs have no origin in wopr today. Both are real product work,
not
plumbing, and both are in scope because the client's UI depends on them.

**Todos.** The app renders a todo panel and a completion badge on the tool part
(`TodoListPanel.swift`, `ToolPartView.swift:103`), reading either the session todo
list or `todos` on the part's own metadata. wopr has neither. Add a `todowrite` tool
whose result metadata carries the todo list — the same shape the client already reads
off a part — then serve `/todo` from the latest `todowrite` result and emit
`todo.updated` on change.

**Message-level revert.** `/revert` needs pre-message file state, which means a
snapshot store; wopr has none. Returning 501 is honest and the client handles it.
Snapshot-based revert is a separate plan with its own storage design, and it does not
belong inside a transport plan.

## Security

The client asks for no credentials, so this server exposes shell execution and
filesystem reads to anyone who can reach the port. Binding to a LAN is not a security
control. Three rules, all enforced in `internal/codingagent/serve`:

- **Path allowlist rooted at `projectDir`.** `/file`, `/file/content`, `/find/file`
  resolve symlinks (`filepath.EvalSymlinks`) and compare against the resolved root
  before any read. A symlink out of the project is a refusal, not a read. This is the
  control that makes auth-less LAN serving defensible.
- **A real request body cap.** `http.MaxBytesReader` on every body-carrying handler.
  16 MiB, justified rather than inherited: a 12 MP phone photo decodes and re-encodes
  to single-digit MB, base64 inflates by a third, and wopr's own largest pinned entry is
  17 MiB (`session_large_line_test.go:32`), so 16 MiB is below one transcript entry and
  far above one photo. The draft's claim to reuse a nonexistent cap is exactly the kind
  of false green this plan must not repeat.
- **No claim of sandboxing.** `docs/threat-model.md` records that wopr runs with the
  permissions of its user and is not a security boundary. A served session is a remote
  shell for anyone on the network. That sentence goes in the docs next to the bind
  instructions, not in a footnote.

Loopback is the default bind. A non-loopback bind prints the exposure warning on every
start: no auth, full tool access, LAN-reachable.

## Memory

The draft optimized against one unproven past failure. This optimizes for a property
instead: a served session's resident memory must be predictable and independent of
conversation length. Each rule names the mechanism it replaces.

- **Bounded per-connection buffer, sized in bytes.** The draft's "256 events or 1 MiB"
  is not a bound: one entry can exceed 1 MiB on an image-heavy session, so a
  byte-derived cap from the declared maximum event size is the only real cap. A
  subscriber that cannot drain within its budget is dropped with a terminal event, not
  queued. Backpressure that silently queues is how a server is killed by a phone that
  walks out of range.
- **Serialize once, fan out bytes.** The bus marshals once per event.
- **History is a tail read.** `/session/{id}/message` opens the append-only JSONL,
  seeks to EOF, walks back to a newline boundary, and decodes forward until the
  client's `limit` is satisfied. Roughly sixty lines, entirely inside
  `internal/codingagent/serve`.
- **The shared loader is not touched.** `raw` retention stays, because it is
  write-critical. The offset-index rewrite of `loadSessionFile` is deleted from this
  plan. It cannot regress the TUI because it never reaches the TUI. If it is ever
  wanted it belongs to a session-storage change, with the before/after memory
  measurement `coding/AGENTS.md` already requires for changes touching large sessions.
- **The session list is bounded too.** `/session` must not build `AllMessagesText` per
  listed session. Read the header line and derive the title from the entries the list
  actually shows. `summarizeSessionFile`'s retention is for the TUI's fuzzy search, not
  for a server list endpoint.
- **Blobs resolve only inside the served window.** `sessionblob.Resolve` inlines images;
  50 phone photos in a session must not become 50 decoded images to render the last 20
  messages.
- **One run per session, enforced by the host.** The host owns the rule, keyed on the
  session it owns. This replaces the draft's claim that `runGen` and `queueMu` already
  provided it: `runGen` is `InteractiveMode`'s TUI-only field
  (`internal/codingagent/interactive.go:97`) and there are two different `queueMu`s in
  two packages.
- **Evidence is a benchmark, not a residency threshold.** `coding/AGENTS.md` forbids
  change-detector shapes and asks for measurement. `BenchmarkServeFanout` with
  `-benchmem`, over a fixture of at least 8,000 entries, four connections, readers
  stalling long enough that an unbounded queue would exceed its declared bound by an
  order of magnitude. The assertion with teeth is that all four connections receive the
  terminal drop event; a memory assertion alone is the loose half.
- **Release is a real cost, so do not pretend otherwise.** Swapping or releasing a
  session runs `ReplaceInner`, which aborts compaction and branch summaries, interrupts
  tasks, reinstalls the cache warmer, and restores runtime state
  (`coding/session.go:901`). An LRU release discards queued work for that session. With
  the per-connection bound in place there is no aggregate pressure left to relieve, so
  `server.maxSessions` is cut.

## Configuration

A `server` section on `Settings` (`internal/codingagent/settings.go:33`), using the
existing global-plus-project merge, which is generic JSON recursion and needs no new
plumbing.

| Key | Default | Why it earns its place |
|---|---|---|
| `server.port` | `4096` | a phone profile is saved once; the port must be predictable |
| `server.hostname` | `127.0.0.1` | the only flag that cannot be defaulted |

`server.projectDir`, `server.maxSessions`, and `server.auth` are cut. `projectDir`
duplicates `Services.CWD()` and the server fixes one working directory anyway.
`maxSessions` rests on an LRU whose release cost is real work, not a reload.
`auth` is a key that decodes, merges, round-trips, and enforces nothing — one line when
auth lands.

Flags override settings. `wopr serve` with no flags is the whole workflow; no shell
alias.

## Slices

Six. Each ends green and is independently committable.

1. **Transport skeleton.** `coding/host` supervisor and event bus with an injectable
   sink and multi-session routing; `internal/codingagent/serve` listener; `wopr serve`
   reading `server.port` and `server.hostname`; `GET /global/health`;
   `http.Server{IdleTimeout}` for keep-alive reaping, with only the SSE heartbeat as new
   work. Gate: one test that a second concurrent session prompt is rejected per
   session, and one that health answers over a real listener.
2. **Engine unification.** Re-point `--mode rpc` at `coding/host`; delete the inlined
   switch and the `os.Stdout` closure. RPC's protocol and existing tests unchanged. Gate:
   `go test ./cmd/wopr` green with no test edits beyond additions — a changed test here
   means the protocol moved, which is the failure this slice exists to prevent.
3. **Session and read surface.** `/session` CRUD, `/project`, `/session/status`,
   `/agent`, `/config/providers`, `/provider`, `/session/{id}/todo`, `/session/{id}/diff`,
   `/find/file`, `/file`, `/file/status`. Every required field of every DTO in the table
   above. The bounded list read and the tail read land here, with the benchmark.
   `PATCH` archive, `DELETE` 501, `/fork` 501.
4. **Prompt path.** `POST /prompt_async` (204), `POST /message` structured, `/abort`,
   SSE `/global/event` with all 17 events and the per-connection byte bound. This is the
   slice that proves the seam; SSE is not started until a prompt streams correctly.
5. **Interactive surfaces.** `todowrite` tool and `/todo` plus `todo.updated`;
   `askuser.Asker` wired to `/question*` and the three question events;
   `GET /permission` and `POST /session/{id}/permissions/{id}` as **501** stubs — the
   approval substrate is assigned to `serve-7` and is `BLOCKED` on a human decision;
   `/file/content` behind the resolved-path allowlist. The phone becomes a first-class
   frontend: the agent can ask a question and there is a real answerer.
6. **Docs and threat model.** `docs/site/docs/server.md` with the endpoint table, the
   client's commit it was derived from, and the exposure warning;
   `docs/threat-model.md` remote section; a `server` row in
   `docs/site/docs/settings.md`; a package-map entry in `docs/architecture.md`; README.

Gates for every slice: `go build ./...`, `go vet ./...`,
`go tool golangci-lint run` on touched packages, `go test` on touched packages,
`go fix -diff ./...` empty. Before merge: `make check`. Manual gate before slice 6:
the app's Test connection against a laptop-hosted server, then one streamed
conversation, then a question answered from the phone.

## Out of scope

- Auth, TLS, CORS, mDNS, rate limiting, audit log. Auth is the first thing to add if
  this ever leaves a trusted network, and the allowlist is what makes deferring it
  defensible until then.
- The opencode web client, generated SDK, IDE plugins, ACP, `opencode attach`
  TUI-over-remote, `--attach` on print mode.
- SSE replay via `Last-Event-ID`. The client has no replay support, so nothing would
  consume it.
- Snapshot-based `/revert` and `/fork`. 501 until their own plan.
- More than one working directory per server process.
- Routing the TUI through the transport seam.

## Risks

- **Client drift.** The app is a third-party build with no SLA. Mitigation: the
  endpoint table states the commit it was derived from, and the DTOs stay tolerant so
  added optional fields do not break decoding.
- **Message topology.** opencode's parts address a message that also addresses the
  assistant reply; wopr's transcript is an ordered entry list. Slice 4 is where this
  either works or does not. Do not start SSE before a prompt streams correctly.
- **Two writers, one session.** The host owns a served session. A local TUI on the
  server does not attach to it, and the host's per-session run rule is what keeps a
  phone and a script from interleaving turns.
- **Package cycle pressure.** `internal/codingagent/serve` must not import `coding`.
  If a change seems to need it, the seam belongs in `coding/host`.
- **`todowrite` is a new agent capability.** It changes what the model can do in every
  mode, not only remote. Its own behavioral test, and a CHANGELOG line.

## Open questions

1. Which app build? Written against `grapeot/opencode_ios_client`. `OpenCode Mobile
   App`, `OpenCody`, and `anhtuan997/opencode-ios-client` also exist. If yours differs,
   re-run the endpoint diff against its `APIClient` first — it is a ten-minute grep and
   it is the difference between working and not.
2. Should `--mode rpc` gain the question path too, once `askuser` is wired in slice 5?
   It deliberately omits it today to keep request tokens down
   (`coding/session_ask.go:12`), which is the right default for a headless client and
   the wrong one for a phone.