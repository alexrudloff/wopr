# serve-5 — reference evidence

Bulk material for `2026-10-04-serve-5.md`. Every `file:line` here was verified by a
read or a grep in `/home/kc/dev/wopr/.worktrees/wave-serve-5` on 2026-10-04. Line
numbers drift; the symbol name is the durable citation.

## 1. Route count, measured

```
$ grep -c 'mux.HandleFunc' internal/codingagent/serve/serve.go
24
```

`BACKLOG.md:356-360` states 20 → 22. Both numbers are stale. serve-5 adds six
routes, so the real transition is **24 → 30**. `BACKLOG.md:360` also says
"serve-3 ... merged with the count verified at `20`" — serve-3's own DoD grep is
stale for the same reason and is not this slice's business.

The four existing `notImplemented` routes, verified: `DELETE /session/{id}`
(`serve.go:61`), `POST /session/{id}/fork` (`:62`), `POST /session/{id}/revert`
(`:63`), `GET /session/{id}/diff` (`:64`). The comment at `serve_handlers.go:73`
— "notImplemented is the shared stub for the four 501 routes." — becomes false at
six and must be corrected in this slice.

## 2. BLOCKER-shaped finding — `POST /session/{id}/permissions/{id}` panics at startup

Go's `ServeMux` rejects a pattern that reuses one wildcard name. The check is
`seenNames` in `net/http/pattern.go`:

```go
// /home/kc/sdk/go1.27.1/src/net/http/pattern.go:130
seenNames := map[string]bool{} // remember wildcard names to catch dups
...
// :176-179
if seenNames[name] {
    return nil, fmt.Errorf("duplicate wildcard name %q", name)
}
seenNames[name] = true
```

`mux.HandleFunc` panics on that error (`net/http/server.go:2955-2958` for the
package-level func, `:2927-2932` for the method), so registering the plan's path
verbatim (`plan:143`) takes down `serve.New` and therefore **every route in this
slice**. Executed, not reasoned:

```
$ go test -run TestPanicBothSpellings -v
    pattern "POST /session/{id}/permissions/{id}" PANICS: parsing "POST /session/{id}/permissions/{id}": at offset 31: duplicate wildcard name "id"
    wire status=200 id="ses_1" permissionID="per_9"
    pattern "POST /session/{id}/permissions/{permissionID}" ok
--- PASS
```

The panic is reached only when `use121` is false — the Go 1.21 mux under
`GODEBUG=httpmuxgo121=1` has no wildcard parser at all and stores the pattern
literally (`net/http/servemux121.go`, `handle`). Measured:

```
$ GODEBUG=httpmuxgo121=1 go test -run TestPanicBothSpellings -v
    wire status=404 id="" permissionID=""
    pattern "POST /session/{id}/permissions/{id}" ok
    wire status=404 id="" permissionID=""
    pattern "POST /session/{id}/permissions/{permissionID}" ok
--- PASS
```

Both spellings register and both 404 under 1.21 behaviour, so the correction is
safe either way — and the plan's spelling is broken under both settings.

Fix: `POST /session/{id}/permissions/{permissionID}`. The **wire path is
unchanged** — only the pattern's second wildcard name differs — and
`r.PathValue("id")` / `r.PathValue("permissionID")` resolve as shown above.
Consequence for the gate: because a request cannot distinguish the two
spellings, a test over the wire cannot pin the name. It can only prove
`serve.New` did not panic.

## 3. The tool-result metadata path survives JSON

The todo list is read out of the transcript, so the round trip has to work:

| Step | Cite |
|---|---|
| a tool returns `Details` | `agent/agent.go:178` (`AgentToolResult.Details`) |
| it is copied onto the tool-result message | `agent/tool_execution.go:420` |
| the message is written to disk | `ai/messages.go:150` (`ToolResultMessage.Details`, `json:"details,omitempty"`) via `internal/codingagent/session.go:59-62` (`MessageEntry.Message`) |
| it comes back off disk | `agent/message_json.go:183` into `agent/message_json.go:215` (`agentMessageWire.Details any`) |

`Details` is `any`, so after a round trip it is `map[string]any` / `[]any`, not
the tool's own struct. Every reader must go through `json.Marshal` +
`json.Unmarshal` into a typed value, not a type assertion. The live path
(`ToolExecutionEndEvent.Result` at `agent/agent.go:305-307`, whose
`AgentToolResult.Details` is one hop away at `:178`) yields the **typed** value,
so the same marshal/unmarshal decode serves both paths.

Nothing in `internal/codingagent/serve` read `.Details` before this slice. The
three transcript-side constructions of a tool part's `state` are
`serve_handlers.go:779-783`, `:812` and `:841`, and each builds
`{"status":…,"output":…}` from the tool result's `Content` alone. That is why the
`metadata.todos` half of the work is a separate edit in `serve_handlers.go` and
not a consequence of the SSE fix.

## 4. Two different "read this session" paths already exist

`sessionMessages` (`serve_handlers.go:557-593`) is the precedent and `/todo`
copies its precedence exactly, so the two handlers cannot disagree:

- `rec.HasFile` → `tailEntries(rec.Path, limit)` (`serve_read.go:420`),
  byte-bounded by `maxTailBytes = 16 << 20` (`serve_dto.go:127`).
- else `live.Inner().Entries()`, truncated to the last `limit`.

`/todo` passes `limit = maxLimit` (200, `serve_handlers.go:27`) — the widest
existing bound — and takes the **last** matching `todowrite` result. A
whole-file scan is the alternative and is wrong here: this is an unauthenticated
LAN endpoint and the plan requires resident memory independent of conversation
length (`plan:235-237`).

## 5. The mapper's tool part drops metadata today

```go
// internal/codingagent/serve/serve_sse.go:392-396
func (m *eventMapper) toolSnapshot(callID, name, status, output string) codinghost.Event {
	part := toolPart(m.sessionID, m.provisionalID(), 0, callID, name)
	part.State = json.RawMessage(`{"status":"` + status + `","output":` + strconv.Quote(output) + `}`)
	return m.part(part)
}
```

It builds `state` from `status` and `output` only, so `Details` never reaches the
wire. The backlog Notes (`BACKLOG.md:611-613`) and `plan:197-201` both require
todos on the part's own metadata as well as on the session list. Hence §Q1 and
the signature change in the spec.

Three further spots build a tool part's `state` the same way, and the spec does
**not** list them — it would have been the false green §Q1's second paragraph is
about:

```go
// internal/codingagent/serve/serve_handlers.go:779-783  (matched tool result)
state := map[string]any{"status": "completed", "output": output.String()}

// :812  (unmatched tool result, new assistant message)
State: json.RawMessage(fmt.Sprintf(`{"status":"completed","output":%q}`, output.String())),

// :841  (no pending tools at all) — identical to :812
```

`/session/{id}/message` is a **recovery** surface, not a convenience one: the
client has no replay and recovers by re-fetching history (`plan:162-163`).

The backlog's own citation for the second surface is `ToolPartView.swift:103`.
There are zero `.swift` files in this tree, so that is inherited from the
backlog rather than checked here — recorded so no gate claims to have verified
it.

## 6. SSE names, before and after

Trigger-less today, per the comment at `serve_sse.go:21-27` and serve-4 §Q3:

| Name | After serve-5 | Trigger |
|---|---|---|
| `todo.updated` | **mapped** | `ToolExecutionEndEvent` with `ToolName == "todowrite"` |
| `question.asked` | **mapped** | the `askuser.Asker` closure serve installs |
| `question.replied` | **mapped** | `POST /question/{id}/reply` |
| `question.rejected` | **mapped** | `POST /question/{id}/reject` |
| `permission.asked` | unmapped | no approval policy, no blocking hook — serve-7, BLOCKED |
| `permission.replied` | unmapped | same |
| `message.removed` | unmapped | `omitRecoveryAttempt` emits no agent event (`coding/session_recovery.go`) |
| `message.part.removed` | unmapped | nothing in wopr removes a part; a failed tool is `state.status = "error"` |
| `session.deleted` | unmapped | `DELETE /session/{id}` is a 501 stub (`serve.go:61`) |

9 → 5 unmapped, 8 → 12 mapped of 17. The `serve_sse.go:21-27` comment lists all
nine by name and must be rewritten in this slice.

## 7. Asker reachability from serve

serve may not import `coding` (`plan:359-361`). It does not need to:
`sess.Inner()` (`coding/host/host.go:210-212`) returns `*coding.Session`, and Go
lets serve call methods on a value whose type it never names. The existing
precedents are `sess.Inner().SetSessionName(title)` (`serve_handlers.go:279`) and
`sess.Inner().SetModel(models[i])` (`serve_prompt.go:261`).

serve imports `internal/codingagent/askuser` for `Asker`/`Question`/`Answer`.
No cycle: `askuser` imports only `agent`, `ai`, `internal/text`
(`askuser.go:6-16`).

`s.host.Start(...)` has exactly one production caller — `createSession` at
`serve_handlers.go:205` — so installing the asker there covers every session the
server owns.

## 8. `todowrite` reaches every mode without a config knob

`CreateCodingTools` (`tools.go:134`) is the **shared** set: subagents build their
own from it (`coding/session_council_build.go:289`, `coding/session_task.go:339`,
`coding/session_writers.go:228`). `CreateAllTools` (`tools.go:163`) is the main
session's registry and already hosts the opt-in `PowerShellTool`. `todowrite`
belongs in `CreateAllTools` — that is what keeps it out of subagents, exactly as
`askuser` is kept out of them (`askuser.go:1-4`).

Two consumers then differ, and both must be accounted for:

- **serve**: `Host.Start` passes no `ActiveBuiltinTools`
  (`coding/host/host.go:101-104`), so `SelectBuiltinTools(builtin, nil, nil)`
  leaves anything **not** in `optInBuiltinToolNames` (`tools.go:88`) active.
  `todowrite` is live on serve with no further change.
- **TUI / print / json**: `cmd/wopr/main.go:565` sets `agentToolNames` to a
  hardcoded eight names, and the `default:` arm, opening at `:602`, turns it
  into `activeBuiltin`, which `BuiltinToolActive` (`tools.go:98-108`) treats as
  explicit. A tool missing from that list is **inactive by default** on those
  modes. This is why `main.go` is in the file list (§Q6).

`BuiltinToolNames()` (`tools.go:92`) is read by `cmd/wopr/main.go:564` for the
`--tools` filter, so adding the name to `builtinToolNames` is what makes
`--tools todowrite` work.

## 9. Response-size precedent, and what 8 MiB is not

`maxBodyBytes = 16 << 20` (`serve_dto.go:130`) is the **request** cap and the
wrong number for a response.

`maxInlinedImageBytes = 8 << 20` (`serve_read.go:465`) supplies the *number* for
`/file/content`, but not by the route the first draft claimed. It is a
**cumulative budget over one tail read**, not a per-blob cap:

```go
// internal/codingagent/serve/serve_read.go:437
budget := maxInlinedImageBytes

for _, rec := range records {
	resolved := sessionblob.Resolve(path, rec)
	budget -= len(resolved)          // :442-443
	if budget < 0 { … continue }
```

So it bounds one transcript's total inlined payload across every record. A
single file bounded by the same number is a different question, answered on its
own terms: 8 MiB is under wopr's largest pinned transcript entry (17 MiB,
`session_large_line_test.go:33`) and far over one decoded phone photo. §Q5
justifies it that way and drops the borrowed-value framing.

## 9b. Measured: what `json.Encoder.Encode` actually corrupts

`writeJSON` (`serve_handlers.go:33-39`) uses `json.NewEncoder(w).Encode`, not
`json.Marshal`. Executed on this tree's toolchain:

```
invalid utf8: in=61 ff fe 62 out=61 ef bf bd ef bf bd 62 equal=false
nul:          in=61 00 62    out=61 00 62          equal=true
```

The corruption claim holds for invalid UTF-8 → U+FFFD, and **fails for NUL**,
which round-trips byte-exact. So the writeJSON rationale justifies the
invalid-UTF-8 half of the 415 predicate only; §Q5 gives the NUL half its own
stated reason rather than leaving it resting on a false premise.

## 10. CHANGELOG shape

`CHANGELOG.md:3` is `## [0.2.1] - 2026-09-30`, cut by `9a38b33 "Version 0.2.1"`,
with `## [0.1.5]` below it and **no `Unreleased` section**. serve-3 and serve-4
added nothing, so serve-5 is the first slice to face this.

Two constraints on the line:

1. **Where.** Writing into the dated, shipped `0.2.1` heading would amend a
   released version. serve-5 opens `## [Unreleased]` above it, matching the
   bold-lead style of the existing entries (`CHANGELOG.md:5-11`).
2. **What it may claim.** `plan:362-363` and `BACKLOG.md:616-617` require a line
   because `todowrite` is a new agent capability in every mode. The mode this
   slice exists to serve cannot contradict it: `wopr serve` parses only
   `--port` / `--hostname` (`cmd/wopr/serve.go:52-56`, `:95-138`), `runServe`
   reads no `settings.DefaultTools` and no exclusion, and `host.StartOptions`
   (`coding/host/host.go:86-89`) carries only `Model` and `SessionDir` — so there
   is no field for `Host.Start` to pass an exclusion through. The line says
   "active by default" and does **not** promise `--exclude-tools todowrite`
   under serve.

## 11. The test-faux seam is a string table, and it needs two new rows

`serve_test.go`'s only provider seam is `promptServer` (`:126-152`):
`WOPR_TEST_FAUX=1`, `coding.NewServices`, `New(…, "test-faux/faux-1")`. There is
no provider-injection hook, so a round trip can only be driven through
`ai/test_faux.go`, whose `classifyTestFauxRequest` (`:235`) is a hardcoded
`strings.Contains(lastText, "Run: …")` table. Its tool fixtures
(`:314-471`) name exactly bash, read, write, edit, grep, find and ls — **no
`todowrite`, no `ask_user`**.

Consequence: the round-trip gates are unsatisfiable until two fixtures are added,
in the file's existing shape:

```go
// Run: write todos
if strings.Contains(lastText, "Run: write todos") {
	return "tool", "", []testFauxToolCall{{
		Name: "todowrite",
		Args: map[string]any{"todos": []map[string]any{ /* … */ }},
	}}
}
```

and the same for `ask_user`, plus a tool-result follow-up row per fixture so the
turn settles (`:478-530`). Both go in `ai/test_faux.go`, so `./ai/...` joins the
golangci-lint and `-race` gate lines — otherwise that file ships unlinted and
untested.

## 12. `ask_user` cannot block twice in one batch

`askuser.Tool.ExecutionMode()` returns `agent.ToolModeSequential`
(`askuser.go:68`). The loop runs a whole batch sequentially when the config says
so **or** any call in it targets a sequential tool
(`agent/tool_execution.go:52-53`, `:58-63`); the default is parallel
(`agent/agent.go:615`, `cmp.Or(opts.ToolExecution, ToolModeParallel)`). So two
`ask_user` calls in one batch run one after another, and at most one is blocked
at a time. This — not the per-session run rule — is why §Q2 can bound the store
without a cap constant.

The per-session bound comes from the run slot, held by `PromptContent`
(`coding/host/host.go:233-243`) and `PromptContentWait` (`:255-267`) for the
whole run. `Session.Prompt` (`:216-223`) holds it too but **serve never calls
it**; the first draft cited it, which was wrong.
