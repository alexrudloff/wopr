# wopr backlog

Items are picked up cold by a session that has never seen this repository. Status is
load-bearing: `READY` is the only status that gets dispatched, and nothing is `DONE`
until its gates were verified rather than merely dispatched.

The plan of record for the current batch is
`docs/plans/2026-10-03-serve-opencode-ios.md`. Each item below is one of its slices,
and the gates are the repo's standard set from `AGENTS.md`: `go build ./...`,
`go vet ./...`, `go tool golangci-lint run` on the touched packages, `go test -count=1`
on the touched packages, `go fix -diff ./...` empty, and `make check` before merge.

## Standing constraints for every item in this batch

- Wrap toolchain commands in `bash -lc` — non-login shells have a sanitized PATH and
  `go`, `make`, and `tmux` all appear missing.
- Never modify `loadSessionFile` retention in
  `internal/codingagent/session_manager.go`. `raw` is write-critical:
  `SessionEntry.MarshalJSON` returns it verbatim and the full-file flush reads every
  `raw` under a held mutex.
- `internal/codingagent/serve` must not import `coding`. `coding/services.go:18`
  already imports `internal/codingagent`; that would be an import cycle. The
  multi-session supervisor belongs in `coding/host`.
- `runGen` is `InteractiveMode`'s TUI-only field, and there are two different
  `queueMu`s in two packages. Neither is a server-side concurrency primitive.
- `tmux` is 3.4, so four integration tests run a documented degraded fallback. That is
  pre-existing; do not "fix" it by raising a skip threshold.
- No auth and no TLS in this batch. The path allowlist on resolved paths is what makes
  auth-less LAN serving defensible, so it ships before or with the file endpoints, not
  after.

---

### serve-1 — Supervisor, event bus, and listener

- **Status:** DONE — shipped on `wave-serve-1`, merged into `main`
- **Why:** Nothing can be served until a process owns more than one session and can
  publish events to more than one subscriber. This is the seam the whole plan rests on.
- **Files:** `coding/host/` (new: multi-session supervisor, typed event bus with an
  injectable sink), `internal/codingagent/serve/` (new: listener), `cmd/wopr/serve.go`
  (new: flags and wiring), `internal/codingagent/settings.go` (a `server` section for
  `port` and `hostname` only).
- **Gates:** one test that a second concurrent session prompt is rejected per session,
  and one that `/global/health` answers over a real listener. — both met.
- **Notes:** `coding.StartSession(svcs, opts)` is already a free function over a shared
  `*Services` and `Services` holds no session, so multi-session needs a supervisor, not a
  refactor. `services.go:18` already imports `internal/codingagent`, which is what forces
  the layering. Only `server.port` and `server.hostname` earn a settings key — the plan
  cuts `projectDir`, `maxSessions`, and `auth`. `http.Server{IdleTimeout}` handles
  keep-alive reaping; only the SSE heartbeat is new work.

**What landed, for the next session**

- `coding/host`: `Host` with an id-keyed session registry, `Start`/`Get`, and the run rule
  — a per-session `sync.Mutex.TryLock` taken before `Send`, refusing with `ErrSessionBusy`.
  A blocking `Lock` instead would deadlock the caller, because `sendContent` holds
  `s.mu` for the whole run (`coding/session.go:653,667-684`).
- `coding/host/bus.go`: `Bus.Publish` marshals once and hands the same `[]byte` to every
  subscriber. **Symbols are exported with no in-slice caller on purpose** — the repo's
  `unused` linter rejects unexported dead code, so unexporting fails the lint gate. Do not
  "fix" this. `Subscription`'s field is `doneCh` and its accessor `Done()`; same names
  collide and fail the build.
- `internal/codingagent/serve`: `New(addr)` binds and returns the `*http.Server` and
  `net.Listener`; `GET /global/health` answers `{healthy, version}` with
  `IdleTimeout: 120s` and `ReadHeaderTimeout: 5s`. It imports `coding/version`, **not**
  `coding`. The `serve -> coding/host` edge is still unbuilt; it lands with the first
  route that needs a session, which is serve-3's `POST /session`.
- `cmd/wopr serve --port --hostname`, flags over settings, `server.port` default 4096 and
  `server.hostname` default 127.0.0.1, usage errors exit 2 with a usage line, bind failure
  exits 1, and a non-loopback bind prints the exposure warning.
- Spec of record for this slice: `docs/plans/2026-10-04-serve-1.md`.

**Left open deliberately, and where it goes**

- The SSE heartbeat. The plan and this item's Notes name it slice-1 work; it was deferred
  to serve-4, on the plan's own "do not start SSE before a prompt streams correctly".
  Recorded as open question 1 in the slice spec, with the counter-arguments, so a human
  can overrule it.
- The bus has **no test beyond `Close` idempotency** and no subscriber. Its blocking-send
  and shared-payload invariants are comments only. serve-4 replaces the blocking send with
  the byte bound and the terminal drop event, and should prefer `Session.Subscribe`
  (`coding/session.go:1648`) plus one `json.Marshal` over a second subscription mechanism.
- `Publish` cannot observe a `json.Marshal` failure, and `Host.Get` still has no caller.
  Both are serve-4's to resolve when there is a caller.

### serve-2 — Point `--mode rpc` at the same supervisor

- **Status:** DONE — shipped on `wave-serve-2`, merged into `main` as `f7bd732`
- **Depends on:** serve-1
- **Why:** The dispatch switch is inlined in `runRPCMode` and everything reusable is
  unexported in `package main`, so RPC and a server would otherwise drift into two
  divergent engines.
- **Files:** `cmd/wopr/rpc_mode.go` (delete the inlined switch and the `os.Stdout`
  closure), `cmd/wopr/rpc_types.go`, `cmd/wopr/rpc_events.go`, plus the new `coding/host`.
- **Gates:** `go test ./cmd/wopr -count=1` green with **no edits to existing tests**; a
  test you had to edit here means the wire protocol moved, which is the exact failure
  this item exists to prevent. — met.
- **Notes:** 87 `writeRPC` call sites and roughly 20 captured locals come with it. The
  `os.Stdout` closure is also the seam the serialize-once fan-out rule needs. The four
  globals that are process-scoped today — one session, one set of wait groups, one
  `settlePrompt`, one `streaming()` — become per-session state owned by the host.

**What landed, for the next session**

- `coding/host/rpc.go`: `ServeRPC(ctx, svcs, sess, RPCOptions) int`. The inlined 33-arm
  dispatch switch is gone and the 87 `writeRPC(` sites plus 17 `decodeRPC` sites moved
  **unrewritten**, because `writeRPC := opts.Write` is bound as a local on the first line
  of the body. That single line is why the move is 20 identifier rewrites instead of ~124.
- Per-session state is now per-`ServeRPC`-invocation state: the four globals are locals and
  closures of one call, not struct fields of an engine object. **Not** wired into
  `host.Host` — the registry and the run rule still belong to `host.Start`, and nothing in
  this slice calls it.
- `coding/host/rpc_events.go`: `rpc_events.go` moved whole with one export rename,
  `rpcAgentEvent` -> `WireAgentEvent`. `print_mode.go` imports the package as `codinghost`
  because `runPrintMode`'s second parameter is named `host`.
- `cmd/wopr/rpc_types.go` is 2 type aliases plus `writeJSONLine`. `rpcCommandCatalog` and
  its **six** methods stay in `package main` because `llama_test.go:23` constructs the
  unexported catalog by name; `routePrompt` is reachable only from `llama_test.go:35`, so it
  must survive any "test-only dead code" pruning.
- Spec of record: `docs/plans/2026-10-04-serve-2.md`. A 5-angle fan drove three revisions;
  its verdict history is the argument for reading the spec before touching this code.

**Two things the gates could not see, and serve-3 and serve-4 inherit**

- **`go test ./cmd/wopr` has no teeth for a wire change.** Zero test files assert on
  response text, so the slice's own acceptance criterion would have passed over a byte-level
  change. The one that existed: `encoding/json` embeds `<pkg>.<Type>` in a type error, so
  moving the wire types out of `package main` changed every malformed-command error to
  `host.RPCCommandEnvelope`. `unmarshalRPC` in `coding/host/rpc.go` restores the
  qualifier on the two paths whose errors reach the wire — bytes, not accuracy, marked with
  a `ponytail:` comment. **Any future move of a wire type repeats this trap.**
- Byte-identity was confirmed empirically, not assumed: old and new binaries driven through
  60+ hermetic invocations, three arms each (old, new, old-again) to separate regression from
  per-run noise. Response shapes, the whole streaming path, stderr, and exit codes are
  identical. Concurrent `bash` replies and `prompt`-then-`abort` interleaving are
  nondeterministic at base and unchanged here, so **no byte-exact golden test over those two
  paths is possible** without a quiesce.

**Left open deliberately**

- The `os.Stdout` closure at `cmd/wopr/rpc_mode.go:156-161` is **retained byte-identical**,
  which is not the item's literal wording. `writeJSONLine` still has callers in
  `print_mode.go`, so deleting it outright was impossible; the requirement that actually
  matters — the engine names no writer — is met. Ruling and reasoning in the spec.
- `Host.Get` and `Bus.Publish` still have no production caller, and `ServeRPC` takes
  `*coding.Services` rather than a `*Host`. The `serve -> coding/host` edge is still
  unbuilt; it lands with serve-3's `POST /session`, and that is the first slice whose
  DoD needs a byte-level wire check that the test suite cannot provide.
- serve-4's `serve`-side call shape (`h.svcs`, `s.sess`) needs exported accessors that do
  not exist yet. Recorded as an open question in the spec.
- Teardown **ordering** is preserved by the verbatim copy but is not asserted anywhere;
  idempotency of the double `Session.Close` is explicitly **not** claimed
  (`coding/session.go:933` guards only `close(s.closeDone)`).
- Spec drift worth knowing: the spec says `rpc_events.go` moves "all 392 lines"; `rpcUsage`
  and `tryDecodeJSON` went to `rpc.go` instead, because leaving them behind would make them
  unexported dead code in `package main` and fail the `unused` gate.


### serve-3 — Session and read surface

- **Status:** **SHIPPED** — merged `--no-ff` into `main` as
  `Merge wave-serve-3: session and read surface`. Seven commits on `f7bd732`; the merge also carries
  the follow-up that fixed the defect below. Worktree pruned after a successful merge.
- **Depends on:** serve-1
- **Why:** The phone cannot list, open, or browse anything until the read endpoints
  exist, and every required DTO field has to be emitted or decoding fails on the client.
- **Files:** `internal/codingagent/serve/` handlers and DTOs.
- **Gates:** per-DTO tests that decode a representative payload; the bounded list read
  and the tail read land here with `BenchmarkServeFanout -benchmem`.
- **Notes:** The client requires `Session.slug` and `Session.version` as non-optional
  and has no wopr equivalent — derive both deterministically from the session ID. It
  sends `directory`, `limit`, `path`, and `query` query parameters that must all be
  answered. `PATCH` has two distinct bodies, rename and archive. `session.tokens` and
  `session.cost` are optional and cumulative: emit them or omit them, never zeros that
  read as real totals. `/session/{id}/todo` needs the `todowrite` tool from serve-5 to
  have anything to return. `DELETE` and `/fork` answer 501, which the client explicitly
  treats as not-implemented. Do not use `summarizeSessionFile` for the list endpoint: it
  retains `AllMessagesText` for every session listed, which is a larger greedy read than
  the loader this plan already avoids.

### serve-3 — What blocked the merge, and how it closed

One defect, reproduced against a live listener, not inferred:

**`PATCH` rename on a session the server did not create returns 200 and changes nothing.**
A `.jsonl` written directly into the resolved session dir — the state every session is in
after a server restart — is listed by `GET /session` and answered by `GET /session/{id}`,
but renaming it returns `200` with `"title":""`, and the following `GET` still reports
`"title":""`. The rename is a silent no-op.

This is the exact case the slice's own A2 ruling was built on: `Host.Resume` was withdrawn
precisely so that renaming a session the phone did not create would cost an append rather
than a transcript read. The append path works for a session the host owns
(`TestPatchThenGetAgrees` covers cold and live); it does not reach a session that exists
only on disk.

**The suspicion in this record was wrong, and the correct answer was three defects, not one.**
`SessionManager.RenameSession` is not a no-op when unbound — it appended `NEW-TITLE` correctly every
time. The file changed; the **reader** could not see it. Reproducing over a live listener found:

1. **`sessionRecordAt` walked oldest-first** (`serve_read.go:207`) while the plan specified
   last-writer-wins in three places. A rename read back the original name. Branch B already scanned
   `slices.Backward` (`:338`), which is exactly why a live session renamed correctly and a disk one
   did not, from the same handler.
2. **The `Archived` type switch had no `nil` arm** (`:224-234`, `:352-362`), so the `null` that
   clears an archive matched nothing, the open `archived == nil` guard fell through to an *older*
   record, and a cleared session still reported archived. **Both** readers, not just the disk one.
3. **The two file-level appends wrote `"%s\n"` with no separator check**
   (`session_manager.go:339`, `:366`). On a file not ending in a newline the new record was glued
   onto the last one — 200, and permanent. On the rename path this read as `"title":""` before and
   after, which is how the *original* symptom in this record was really produced: the glued line
   fails to parse, so the reader found no `session_info` at all. `/import` reaches it with no crash,
   since `sessionblob.Relocate` rejoins on the separator it split on.

Fixed by `slices.Backward`, a dedicated `archivedSet` flag separating "consumed" from "latched a
value", and one `appendJSONLine` helper (both opens become `O_APPEND|O_RDWR`; the last-byte read
needs a readable descriptor). `TestPatchRenameAndArchiveOnFileBackedSession` drives all of it over a
real listener, and every requirement was **seen red**: B1 alone turns requirements 2, 3 and 4 red,
B1b alone turns 3, B2 alone turns 5 on both appends. Five cells were also re-measured over a live
listener on all three revert states.

### serve-3 — Carried debt, recorded rather than fixed

Three findings from the defect work that nothing else records. All are real; none is this slice's.

1. **`Session.AppendCustomEntry` has the same missing separator**, with **seven** callers:
   `coding/session_council.go:75`, `coding/session_router.go:214`, `coding/session_queue.go:109`,
   `coding/session_goal.go:346`, `coding/session_efficiency.go:428`,
   `internal/codingagent/sidebar_snapshot.go:112`, `serve/serve_handlers.go:298`. One-line fix
   inside `appendSessionLine` (`session.go:567-577`); wider blast radius than the two callsites
   serve-3 fixed, and no measurement harness exists for it yet.
2. **`sessionRecordAt` and `sessionRecordFromLive` are two implementations of one rule**, deriving
   `Name` and `Archived` from `[][]byte` and `[]SessionEntry` respectively. They now agree — B1 and
   B1b made that true, and it was false before — but unifying them means normalising both to raw
   bytes. Note that `serve_read.go`'s own §4.2 heading, "Why the two branches cannot disagree", was
   **false** until this commit, for a different reason in each branch.
3. **A header-only file with no trailing newline 404s** at `serve_read.go:182`.
   `reader.ReadBytes('\n')` returns the unterminated line with `io.EOF` and `:183-185` rejects any
   error. Three-line fix — accept `io.EOF` when the line is non-empty, the same shape as the
   `len(headerLine) == 0` guard at `:187`. Left alone because wopr does not produce the state for a
   session it creates (`writeDerivedSession` writes the header with its newline; `Create` writes
   nothing), `/import` reaches it and gets a consistent 404 rather than corruption, and fixing it
   here would leave that probe cell with no measured value — the harness is scratch.

### serve-3 — What landed, for the next session

- 19 routes over 15 handler functions plus the existing health handler, the wire DTOs, and
  the `serve -> coding/host` edge. Four exported symbols in `coding/host` (`Host.Services`,
  `Host.Sessions`, `Session.Inner`, package-level `AvailableModels` and `ModelProviderID`).
- The session directory is resolved **once**, in `cmd/wopr/serve.go`, and used by both the
  reader and `Host.StartOptions.SessionDir`. `resolveSessionDir` returns `""` for the
  default config, so the empty case falls back to `codingagent.NewSessionManager(cwd).
  SessionDir()`. Do not reintroduce a second resolution order.
- The id-to-path lookup is `os.ReadDir` plus a suffix match on a validated id. It was a
  `filepath.Glob` first: the session dir was interpolated into the pattern unescaped, and a
  `*` anywhere in a project path matched a second project's session directory. **The dir is
  an argument, never a pattern.**
- Both reads are bounded and measured: the list read holds ~52.8k allocations at 400 and at
  800 entries per session; `tailEntries` on the pinned 17 MiB entry went from 255-290 ms to
  under a millisecond.
- Live-probe confirmed: `GET /session` lists a session the server did not create, both 501
  stubs answer 501, a symlink to `/etc/passwd` is refused 403, an absolute path is refused
  403, `/file/status` returns a tracked filename containing a space whole, empty lists are
  `[]` and not `null`, and a `PATCH` response agrees with the `GET` after it for a session
  the host owns.
- The merge-blocking round landed `slices.Backward` on the disk reader, an `archivedSet` flag in
  both readers, and one `appendJSONLine` separator helper. Re-measured over a live listener:
  6/6 cells green on the merged binary, and 1/6, 5/6 and 4/6 on the three single-revert
  binaries — so each of the three defects is caught by a cell the other two do not share.

### serve-3 — The pattern the next session must not repeat

Five verification rounds, each finding real defects that every gate had passed:

1. `walkBack` aliased every read block into one buffer, so the tail read returned 0 entries
   for a 17 MiB entry and silently dropped the newest 113 messages at `limit=200`.
2. `listSessions` drained its worker channel with a non-blocking `select`, so `GET /session`
   returned `null` — 15 runs out of 15.
3. `projectID` took twelve raw digest **bytes** instead of hex, putting invalid UTF-8 into a
   field the client requires. `tokens` and `cost` shipped as zeros, which this item's own
   note forbids by name.
4. `stopTail` counted read **blocks** against `limit`, so `limit=20` returned 4 messages when
   entries exceeded a block — silent under-delivery the client cannot tell from end-of-history.
5. `workingTreePaths` split porcelain records on whitespace, truncating any tracked filename
   containing a space.

Seven tests reported green while their bug was live, and round 2 showed that reverting both
the per-block clone and the drain left the suite green. **A green suite in this package is
not evidence.** Every defect that mattered was found by running the binary, or by
deliberately breaking the code underneath each test.

Round 6, the one that blocked the merge, kept that true — and then added a sixth way to be fooled:

6. **A gate was red for four rounds and nobody read it.**
   `internal/testenv/TestWindowsTestsCreateSymlinksThroughSymlink` had been failing on
   `serve_read_test.go:338` since commit `c5a7b34` — a direct `os.Symlink` in a test that
   builds for Windows. **This slice introduced it**, and five earlier verification rounds
   never ran `make check` to completion; they ran its parts. One line through the repo's own
   `testenv.Symlink` helper. The lesson generalises past this defect: `make check` is a gate,
   and running a subset of it is not running the gate.

### serve-3 — Two scope reductions, recorded rather than shipped silently

- `GET /session/{id}/diff` answers **501**. The plan does not mark it not-implemented
  (plan:132) while it explicitly marks `DELETE`, `/fork` and `/revert`, and slice 3 names
  `/diff`. wopr has no snapshot substrate and building one is far outside this slice. The
  client treats 501 as not-implemented (plan:147-151), which is what makes this defensible.
- **`/permission*` is assigned to serve-5, as 501 stubs** (decided 2026-10-04 by a
  four-dispatch fan; alternatives and evidence below). `GET /permission` (plan:142) and
  `POST /session/{id}/permissions/{id}` (plan:143) fall between slices: the plan's slice
  list (plan:299-334) names neither, and neither does serve-5's Files line. serve-5 owns
  them **as `notImplemented` handlers answering 501**, and the real substrate becomes
  `serve-7`. Today both routes are **unregistered and answer 404**, with no catch-all on
  the mux (`internal/codingagent/serve/serve.go:41-76`); 404 is not an honest answer,
  because the client reads it as "host gone".

  Why 501 and not a real implementation: the client treats 501 as not-implemented
  (plan:147-151), which is the same treatment serve-3 gave `/diff` above. The substrate is
  *more* invasive than revert's snapshot store, not less — three things must be invented
  that wopr has no home for: a policy surface saying what requires approval (Settings
  carries only `ShellPath` and `CommandPrefix`, `internal/codingagent/settings.go:77-80`);
  **the first hook in the tree that blocks on a human**, which lands in the agent loop's
  *serial* prep phase (`agent/tool_execution.go:103-121`) and so stalls every tool in the
  batch, not just the one awaiting a thumb (all five existing hook users return
  synchronously); and a pending-request store holding an id the model never saw. Decisive
  asymmetry against `/revert`: the client has **no `Last-Event-ID` and no replay**
  (plan:162-163), so a permission asked while the phone was out of range is unrecoverable
  — that is a liveness protocol needing its own persistence story.

  Why serve-5 rather than serve-4: serve-4 owns the *mapping* of all 17 SSE events
  (plan:318-319) while serve-5 is explicitly handed "the three question events"
  (plan:321-322) — **production sits with the feature slice**, not the transport. In-tree
  precedent: serve-3 shipped `GET /session/{id}/todo` as an empty-array stub whose producer
  is serve-5 (`serve.go:55`, `serve_handlers.go:398-407`). serve-5's asker half needs zero
  new engine code (`askuser.Tool` and `Session.SetUserAsker` already exist), so the stubs
  keep its three gates honest; folding an undesigned policy engine in would make its "done"
  mean two different things.

  Alternatives rejected, with costs: **a new slice carrying real 200-returning permissions**
  — re-derives a second near-identical pending store (the shapes differ only in path
  prefix, plan:143 vs :144) and needs a plan section, gates, and a CHANGELOG line; **real
  permissions inside serve-5** — imports all three undesigned decisions above plus a
  batch-concurrency test its gate list does not cover; **serve-4** — strictly worse, it
  would carry an unspecified approval protocol plus two permission SSE events *and* the
  prompt path, raising risk on the slice that "proves the seam" to buy nothing; **leave it
  at 404** — cheapest of all and rejected, because an unregistered path is an accident, not
  an answer; **serve-6** — it is docs-only (`plan:325-328`), and an endpoint in
  `internal/codingagent/serve` is not a doc.

  Unverified, and it is the one thing that would flip this: whether the client's capability
  check distinguishes 501 from absent **for these two routes specifically**. No Swift
  source exists in this repo. `plan:368-371` already names the ten-minute check — grep
  `grapeot/opencode_ios_client` at commit `2d0e7bd`. If the client treats 501 on
  `GET /permission` as fatal, the answer becomes "always-empty 200 array" instead.

  Two mechanical costs, both landing in **serve-5's** commit, not serve-3's: the slice
  spec's DoD asserts `grep -c 'mux.HandleFunc'` prints `20` and two routes make it `22`;
  and the `notImplemented` stub's comment says "the four 501 routes"
  (`serve_handlers.go:73`), which becomes stale. Serve-3 did **not** add these routes —
  it merged with the count verified at `20`, and serve-3's `DoD:65` grep stays at `20`.

### serve-3 — Still open

- `Session.version` — **decided 2026-10-04: keep the build constant**
  (`coding/version.Version`). Closes open question 3 in the slice spec. Alternatives and
  evidence below.
- The tail read's byte cap wins silently on large entries — `limit=20` returns 15 when
  entries are 1 MiB. The spec sanctions "whichever bound is reached first"; whether the
  client should be told is unanswered.
- The spec's DoD:65 grep names `serve.go` for `writeJSON`, which now lives in
  `serve_handlers.go`. The property holds; the gate text is stale.
- `docs/plans/2026-10-04-serve-3.md` is 2112 lines and was revised seven times (§12 is the
  merge-blocking round). Read it before touching this code, as serve-2's was.

### serve-3 — `Session.version`, decided (open question 3 closed)

**Decision: keep `coding/version.Version` (the build constant).** The plan (plan:181-183)
says "derive both deterministically from the session ID"; the implementation ships the
constant instead. Decided 2026-10-04 by a four-dispatch fan. Three dispatches owned this
question: two returned a firm `DECIDED: keep the constant` on independent reasoning, and the
third — the client-contract angle — returned `UNVERIFIED` because it could not settle the
question from inside this repo, while naming the constant the **zero-risk option rather
than a proven-correct one**. That is the honest shape of the convergence: the code decides
this, the client contract only fails to object. The plan text must be amended to say "emit
the build constant", or the contradiction regenerates on the next read.

The reasoning that actually decides it: **the plan states a purpose, and names a means that
is not required to achieve it.** Its purpose is stability "across reconnects"
(plan:181-183); the constant is a `const` (`coding/version.go:6`), so it is stable across
reconnects by construction, as is any pure function of the session ID. The stated rationale
therefore does not discriminate between the two options — and the remaining ground is
honesty, which favours the constant.

Supporting findings, each with evidence:

- The plan **never says the client compares `Session.version`** against anything, and never
  states its **Swift type**. Exhaustive `grep -i version` over the plan returns exactly
  three hits: `:120`, `:175`, `:181`. Every Swift citation in the plan (`:149`, `:187`,
  `:199`) is about something else. So the contract's only stated requirement is *presence* —
  "tolerant of absent optionals and of unknown keys, but not of absent requireds"
  (plan:167-168) — and the constant satisfies it.
- `Session.version` and `/global/health`'s `version` (plan:120) are two fields in two
  different client structs, and the health one is `String?` while the Session one is
  non-optional. Emitting the same constant in both is what keeps the key meaning one thing
  across the API; emitting `header.Version` would put a build string and a format `int`
  under one JSON name.
- **`header.Version` is a bad candidate, and the slice spec's stated reason for dismissing
  it does not hold.** The spec says it "is 0 on a session written before the field existed"
  (`docs/plans/2026-10-04-serve-3.md:89`); no fixture in the repo omits the field, and git
  history is a single squashed `9396b05 Initial commit`, so the claim is unsubstantiated
  rather than refuted. What *is* verified kills the option anyway: the field is
  `Version int` with `omitempty` (`internal/codingagent/session.go:36`), it has **zero
  readers** anywhere in the tree, the loader validates only `Type == "session"` and never
  checks it (`session_manager.go:140-145`), and its doc comment — "Bumping this requires a
  migration step in `session_resume.go`" (`session.go:24`) — is **false**: that file
  contains zero occurrences of `ersion`. Promoting a write-only, unenforced internal
  counter onto the public wire would make it the only value wopr emits that wopr cannot
  itself interpret. It would also emit a JSON number where the plan describes a version
  string.

Alternatives, with costs: **derive from the session ID** (the plan's literal text) — costs a
derivation function and a per-session decision inside a 100-goroutine list fan-out
(`serve_dto.go:117`), and produces a value with **no meaning** in a field the client
requires: the same defect plan:183-184 forbids for zeros-as-totals. One line of code is the
only reason it is tempting. **`header.Version`** — the findings above. **Omit the field** —
highest downside for zero upside: `serve_dto.go:22` has no `omitempty`, so it is always
present today, and removing it puts the whole `GET /session` decode at risk rather than one
session.

Two things recorded because they are unpaid costs of the decision, both landing in serve-3's
fix-and-merge commit: `serve_dto_test.go:26` hardcodes the literal `"0.2.1"` in its
round-trip fixture, so the next version bump fails a green test — `AGENTS.md` forbids
change-detector shapes, and it should read `version.Version`; and nothing pins the constant
in that test beyond the literal, so the field is not actually asserted on.

Unverified, and it is what would reopen this: no Swift source exists in this repo, so
whether the client *reads* the field beyond decoding is unknown. If it compares
`Session.version` across sessions or invalidates on change, a constant silently disables
that — check `grapeot/opencode_ios_client` at commit `2d0e7bd` (plan:368-371). Note also
that the client *build* is itself unresolved: three candidate apps exist, and every citation
above is conditional on which one the plan was diffed against.

### serve-7 — Permission approval substrate

- **Status:** DONE — closed by decision 2026-10-06, nothing implemented. Policy is
  **all tools open by default** in served mode: no approval gate, no `permission.asked`,
  the two `GET/POST /permission*` routes stay 501. Reopen only if a concrete need for a
  thumb appears; the `Blocked on` policy question below is then the spec's first input.
- **Why:** `GET /permission` and `POST /session/{id}/permissions/{id}` are served as 501
  stubs by serve-5 (see serve-3's scope-reduction entry above). The endpoints are in the
  client contract (plan:142-143), the app renders a permission prompt, and wopr has no
  answer for any of it.
- **Blocked on:** what actually requires approval. wopr has no approval policy anywhere —
  Settings carries only `ShellPath` and `CommandPrefix` (`internal/codingagent/settings.go:77-80`).
  Until a human decides what needs a thumb and what does not, there is nothing to build
  toward, and inventing a policy is a product decision, not an implementation.
- **Files (when unblocked):** an approval queue in `coding/host`; the first *blocking* hook
  in the tree, which lands in the agent loop's serial prep phase
  (`agent/tool_execution.go:103-121`) and so stalls every tool in the batch rather than just
  the one awaiting a reply — all five existing hook users return synchronously; the
  `permission.asked` / `permission.replied` SSE events (plan:157), which have no origin in
  Go code and belong to serve-4's mapping; the two DTOs the plan never specifies.
- **Notes:** the blocker for a naive implementation is replay. The client has no
  `Last-Event-ID` and no replay (plan:162-163), so a permission asked while the phone was
  out of range is unrecoverable. That makes this a liveness protocol needing its own
  persistence story, and it is why deferring it was the cheap answer rather than a
  compromise. A CHANGELOG line is owed whenever it lands: it changes what the model can do
  in every mode, the same reason serve-5 carries one.

### serve-4 — Prompt path and SSE

- **Status:** DONE — shipped on `wave-serve-4` as four commits (prompt path, SSE, defect
  fixes, corrections)
- **Depends on:** serve-2, serve-3
- **Why:** The slice that proves the seam. Everything before it is plumbing; this is
  whether a phone can hold a real conversation with a session on another machine.
- **Files:** `internal/codingagent/serve/` prompt handlers, the SSE stream, the
  opencode event mapping.
  (`coding/host`'s per-session run rule shipped with **serve-1**, not here — this item
  double-claimed it. Do not rebuild it; `coding/host/host.go:97-104` already refuses a
  second concurrent prompt with `ErrSessionBusy`.)
- **Gates:** `prompt_async` answers 204, `abort` answers 200, and the SSE stream carries
  all 17 bus event types with the per-connection byte bound and the terminal drop event.
- **Notes:** Do not start SSE before a prompt streams correctly — message topology is the
  real risk here, since opencode's parts address a message that also addresses the
  assistant reply while wopr's transcript is an ordered entry list. Size the
  per-connection buffer in **bytes**, not events: one session entry can exceed 1 MiB
  (`internal/codingagent/session_large_line_test.go:32` pins a 17 MiB entry), so an
  event-count cap is not a bound. Serialize each event once and share the bytes.

#### serve-4 — The gate, restated because it was not meetable as written

`Gates:` above said the SSE stream "carries all 17 bus event types". **9 of the 17 have no
trigger in wopr**, so that sentence was never satisfiable and shipping against it would have
meant either a fake gate or placeholder emissions. Restated:

- **the mapping covers all 17 names; 8 have a live trigger in serve-4; the other 9 are
  justified by name.** No reservation table ships — a string table with no reader is dead code
  and `unused` is enabled (`.golangci.yml:18`); the §Q3 prose in
  `docs/plans/2026-10-04-serve-4.md` is the record.
- The nine: `todo.updated`, `question.asked`, `question.replied`, `question.rejected` and
  `permission.replied` are serve-5 and serve-7. `message.removed`, `message.part.removed` and
  `session.deleted` are a wopr concept with nothing that fires — `AppendContextEdit` emits no
  event, and `DELETE /session/{id}` is a 501 stub.
- The other two original gates are met literally: `prompt_async` answers 204, `abort` answers
  200, and the per-connection bound is in bytes with the terminal drop event.

#### serve-4 — What landed

- `POST /session/{id}/prompt_async` → 204, `POST /session/{id}/message` → 200 `{info, parts}`,
  `POST /session/{id}/abort` → 200, `GET /global/event` → SSE. 404 / 409 / 400 / 413 all
  specified and covered. `make check` green end to end, `go fix -diff` empty, `-race` green on
  `coding/host` and `serve` (the only race coverage those packages get — `make check`'s
  `test-race` pins `./internal/codingagent` only).
- **The byte bound is real and measured:** 33,498,094 queued bytes against a 33,554,432 bound
  with four stalled readers over 8,000 entries; an unbounded queue would have held ~500 MiB per
  stream. `Publish` marshals `ev.Data` once and queues it **beside `ev.Type`** — a subscriber
  holding only payload bytes has no event name and hands every frame to the client untyped.
  **This was a BLOCKER caught in the spec fan and it is the load-bearing detail of the design.**
- **Message topology, the item's named risk, works — on live data.** The transcript entry id does
  not exist while deltas stream, so deltas use a host-minted provisional message id and
  `MessageEndEvent` reconciles with the real one in a burst whose part ids match
  `GET /session/{id}/message` byte for byte. Verified by curl against a live listener, not only
  by test.
- **`appendSessionLine` separator fix shipped here, retiring serve-3 carried-debt item 1.**
  Applying the client's `model` field reaches that function, so serve-4 was in the blast radius
  whether it liked it or not; leaving a known data-loss path unfixed to protect a diff would
  have been the worse call.
- `partID` is one helper used by both the stream and the transcript, with the index enumerated
  over the **full block slice before any filtering** so it equals `ai.*Event.ContentIndex` even
  when a block yields no part. This retires a live serve-3 defect: `entriesToMessages` iterated
  indexless, so two text blocks in one message produced two parts with the same id.
- `messageDTO` became `{info, parts}` — opencode's `MessageWithParts`, which is what the client
  decodes. The flat half serve-3 shipped was speculative.
- `(*Session).MessageEntryID` exists because `TurnEndEvent.MessageEntryID` **never reaches
  `Session.Events()`**: `agent.emit` sends the pre-hook value, so the id was assigned in one
  place and thrown away.

#### serve-4 — Carried debt, recorded rather than fixed

1. **The `r.Context()` trap is closed and pinned; the abort reservation window is not.** The
   owned cancel is load-bearing for `prompt_async` and a test proves it (replacing
   `context.WithoutCancel` with the bare context turns the test red). But
   `TestAbortStopsARunningPrompt` stayed **green** when that cancel was deleted — by the time the
   test observes streaming, `beginAgentRun` has already stored its own. The window between
   reservation and `beginAgentRun` is not deterministically reachable from an HTTP test without
   a hook in `coding/`. The cancel is correct; it is simply not independently pinned.
2. **The byte-bounded queue has two unpinned properties.** (a) `pop` clearing the popped slot is
   what makes a *draining* connection release its memory — measured 31.0 MiB retained → 0.0 —
   but no committed test sees it, because a `HeapAlloc`-after-drain assertion is GC-timing
   dependent and would be flaky. (b) `Push` signalling under the mutex, which removes a stale
   wakeup token, has no test either: `TestDrainedBurstDoesNotFakeADeadline` is entirely
   single-goroutine. Both are correct today; both are undefended. Pin them when a deterministic
   oracle exists.
3. **`agent/agent.go:1296` (`agentAssistantMessage`) reads the stream builder's live message.**
   Pre-existing, and the SSE path does not reach it. The real hazard is one line wide:
   `snapshotAssistantEvent`'s `default:` arm returns events unsnapshotted, so any future
   `AssistantMessageEvent` type the mapper handles would arrive live. It is the same class as the
   `session_recovery.go` race this slice fixed. A `coding/` change, out of scope here.
4. **`ai/messages.go:222` — `cloneAssistantContent` asserts
   `cloneJSONValue(...).(map[string]any)` with no `, ok`.** A `ToolCall` with a nil `Arguments`
   map panics inside `snapshotAssistantEvent`. Unreachable from the builder today
   (`stream_builder.go:189` always sets `Arguments`), and pre-existing.
5. **`type:"retry"` is unreachable on the pull path.** `coding.Session` exposes only
   `SetAutoRetryEnabled` and `AbortRetry`, so `GET /session/status` cannot report it; `retry` is
   emitted on the SSE stream only. `SessionStatus.type` must be one of three values, so omitting
   one is valid, not a decode failure — but the two surfaces disagree.

#### serve-4 — A verification finding that was wrong, and how it was caught

A fan claimed `toolPartID` raced on the stream builder's live `*ai.AssistantMessage`. The
implementer rejected it on the grounds that `AssistantMessageEventStream.Push` calls
`snapshotAssistantEvent` on every event, and a second fan **confirmed the implementer, not the
finding**, two ways: by the code path (`ai/event_stream.go:44`, before the mutex, before
queueing) and empirically (a throwaway test mutated the producer's live object after the push
and the consumer still saw the original id). The first fan read `ai/stream_builder.go`, which
does push the live pointer, and stopped one layer short. **A verifier's rejection is as much a
hypothesis as its finding.**

#### serve-4 — What the next session should not repeat

Four rounds of verification, and the gates found real defects each time:

1. `POST /message` answered **500 "run produced no persisted assistant message"** for a busy
   session — `PromptContentWait` ran in a goroutine, so `ErrSessionBusy` arrived with an empty
   entry id and the empty-id branch fired before the error was read. A test existed for
   `prompt_async`'s 409 and none for `/message`'s.
2. `pop` advanced the slice header without clearing the slot, so a drained burst kept every
   payload: **31 MiB retained after draining 31 MiB**, against a bound whose whole purpose is that
   a draining connection costs nothing.
3. `Push` signalled after unlocking, leaving a stale token; every `Next` returned a spurious
   deadline, which the SSE handler maps to a heartbeat. **The 15s heartbeat pacing was dead** and a
   busy stream heartbeated after every drained burst.
4. The `appendSessionLine` fix shipped with **no test at all** — reverting it made nothing red
   anywhere, on a data-loss path `AGENTS.md` explicitly requires a test for.
5. `Host.Start` called the event forwarder **synchronously**, so `POST /session` hung for 60s.
   No unit test caught it; curl did, immediately.

The pattern: **every one of these passed the whole suite.** The curl-against-a-live-listener
step is what found #5, and the break-the-code-underneath-the-test step is what found #1 and #4.

### serve-5 — Todos, questions, and the file allowlist

- **Status:** DONE — shipped on `wave-serve-5` as `3b97e99`, merged into `main` as `42b2c5a`.
  Fix round from the verification fan closed one blocker (a rejected `todowrite` broadcast
  an empty `todo.updated`) and two warnings; all six gates green, `make check` passed.
- **Depends on:** serve-4
- **Why:** The client's UI genuinely consumes todos and the agent's ask-the-user tool,
  so without these the phone is a read-only viewer of a conversation it cannot answer.
- **Files:** a `todowrite` tool; `askuser.Asker` wiring; `/todo`, `/question*`,
  `/file/content` handlers and the resolved-path allowlist in `internal/codingagent/serve`.
- **Gates:** a behavioral test for `todowrite`, an ask/reply round trip through the
  question endpoints, and allowlist tests including a symlink that points outside the
  project root.
- **Notes:** The client reads todos from either the session todo list or `todos` on the
  part's own metadata (`ToolPartView.swift:103`), so a `todowrite` result carrying todos
  in its metadata satisfies both surfaces. `askuser.Asker` already exists and blocks on
  `ask(ctx, q)`; RPC omits it deliberately (`coding/session_ask.go:12`) because a headless
  client should not pay request tokens for a question nobody answers — a phone is the
  opposite case. This changes what the model can do in **every** mode, so it needs a
  CHANGELOG line. Snapshot-based revert is a separate plan; 501 until then.

### serve-6 — Docs and threat model

- **Status:** DONE — shipped on `wave-serve-6` as `8f63af8` plus a fix commit
  `64f7c16` (the diff verifier caught `POST /session` documented as 409 when the
  shipped command actually answers 500, and the six 501 stubs called "mutating"
  though two are GET), merged into `main` as `7da8dfa`. Seven docs files, no Go
  change. `docs-drift` green; `build`/`vet`/`lint`/`go fix`/grouped/race all green
  in the worktree and re-run on merged `main`. `make check` stops at `test-prereqs`
  (tmux absent, `Makefile:110-111`), so `test-integration` was not run.
- **Depends on:** serve-4, serve-5
- **Why:** An unauthenticated server that runs tools and reads files must say so where
  the person binding the port will actually read it.
- **Files:** `docs/site/docs/server.md` (new), `docs/threat-model.md`,
  `docs/site/docs/settings.md`, `docs/architecture.md` package map, `README.md`.
- **Gates:** `tests/docs-drift` via `make check`.
- **Notes:** `server.md` states the client commit the endpoint table was derived from,
  because the app is a third-party build with no SLA. The threat-model section says
  plainly that a served session is a remote shell for anyone who can reach the port, and
  that auth-less LAN serving rests on the path allowlist.

---

## Follow-ups surfaced by serve-5

### serve-allowlist-toctou — Close the /file/content resolve-to-open race

- **Status:** DONE — shipped on `wave-serve-allowlist-toctou` as `d8ca6a1`, merged into
  `main` as `b3890a1`.
- **Depends on:** serve-5
- **Why:** `fileContent` resolves the requested path through `resolveInRoot`
  (`Lstat` + `EvalSymlinks`) and then calls `os.Open` on the resulting string. A
  component inside the root swapped for a symlink pointing outside between the two is
  followed by the kernel, defeating the path allowlist that auth-less LAN serving rests
  on. It needs a writer with access to the served root, which is why serve-5 ships the
  static-symlink gate and this is a follow-up.
- **Files:** `internal/codingagent/serve/serve_handlers.go` (`fileContent`),
  `internal/codingagent/serve/serve_dto.go` (`resolveInRoot`).
- **Gates:** a test that swaps a path component for a symlink between resolution and
  read and asserts a 403, or a traversal that cannot follow a swapped link.
- **Notes:** A full fix is per-component `O_NOFOLLOW` (`openat`-style) traversal, or
  opening the file and re-checking the open descriptor against a freshly resolved path
  with `os.SameFile`. serve-5's DoD 8 covers the static symlink only.
- **Shipped (`b3890a1`):** the open runs through `os.OpenInRoot` on the already-canonical
  relative path, so the kernel walk is confined to the root and a swapped component is
  refused, not followed. The pre-open `os.Stat` is kept so a statically-present
  FIFO/device is answered 400 before any `openat`; open failures are reclassified by a
  fresh `resolveInRoot` (403/404, else 500). A `beforeOpen` test hook lets the gate swap a
  component in the resolve->open window; the test covers a leaf-file and an
  intermediate-directory swap and serves the outside content if the confinement is
  removed. Known residual: the pre-open `os.Stat` still follows a swapped symlink, so an
  outside file's existence (404) and size (413 body) can be observed — metadata only, no
  content. `listFiles` keeps the same class of gap; see `serve-file-list-race`.

### serve-ci-race-serve — `make check` does not race-test `internal/codingagent/serve`

- **Status:** DONE — shipped on `wave-serve-ci-race-serve` as `6cf2a46`, merged into
  `main` as `f48c353`.
- **Depends on:** serve-5
- **Why:** `automation/ci/test-race.sh:19` pins `pkg=./internal/codingagent` with a fixed
  `-run` filter, so `internal/codingagent/serve` gets no `-race` coverage from
  `make check`. serve-5 added a store written by a tool goroutine and read by HTTP
  handlers, which is exactly that gap; its spec added a separate `-race` line that
  nothing enforces.
- **Files:** `automation/ci/test-race.sh`; `Makefile` if the grouping lives there.
- **Gates:** `make check` runs the serve tests under `-race` and fails on a seeded race.
- **Notes:** Put it in the race script, not a doc, so the next serve slice inherits it.
- **Shipped (`f48c353`):** `automation/ci/test-race.sh` now runs
  `go test -race -count=1 ./internal/codingagent/serve` — whole package, no `-run` filter, so
  every current and future serve test inherits coverage — inserted between the existing TUI
  race block and the goroutineleak profile; line 2's summary was broadened to name serve.
  `make check`'s `test-race` step now compiles and runs the serve package under `-race`
  (`ok .../serve`). Proven to fail: an untracked seeded `TestSeedRace` in `package serve`
  produced `WARNING: DATA RACE` and a non-zero script exit; removed, the script is green.
  Re-verified `make test-race` green on `main` after the merge. `make check` itself still
  cannot complete here — `tmux` absent, it stops at `test-prereqs` (`Makefile:110-111`); every
  other constituent was run directly and passed (`go build ./...`, `go vet ./...`, `make lint`
  0 issues, the grouped unit suite, `go fix -diff` empty, docs-drift).
- **Surfaced here:** `coding-host-ci-race` (same gap for `coding/host`, which serve-4 only
  race-tested manually) and `serve-ci-race-doc-drift` (the TUI-only descriptions left elsewhere).

### serve-5-regressions — Pin the two serve-5 fix-round branches

- **Status:** DONE — shipped on `wave-serve-5-regressions` as `60044ef`, merged into
  `main` as `4394754`.
- **Depends on:** serve-5
- **Why:** Two fix-round branches are reasoned-correct but unpinned: a rejected
  `todowrite` must not broadcast an empty `todo.updated`, and the `ask` abort-vs-reply
  race must honour a reply that already answered 200. Reverting either guard would make
  nothing red today.
- **Files:** `internal/codingagent/serve/serve_sse.go` + `serve_test.go`;
  `internal/codingagent/serve/serve_question.go` + `serve_test.go`.
- **Gates:** one test per branch that fails when the guard is removed.
- **Notes:** The event case needs a `todowrite` fixture that `Parse` rejects, so a
  `Failed todowrite` trigger in `ai/test_faux.go` comes with it. The abort path needs a
  deterministic interleaving of a reply and an abort.
- **Shipped (`4394754`):** both guards pinned, production untouched. `serve_test.go`
  gains `TestRejectedTodowriteDoesNotBroadcastTodoUpdated` (a duplicate-id
  `"Run: failed todowrite"` fixture fails `Parse`, the mapper emits an error tool
  part, and no `todo.updated` may appear before the idle stop frame — a
  protocol-level control replaces the error-string coupling) and
  `TestAskHonoursAReplyThatClaimedTheEntryBeforeAbort` (single-shot deterministic
  `testing/synctest`: the entry is claimed, the context cancelled while the answers
  slot is empty so `ctx.Done` is the only ready select case, and the reply's answer
  must survive). Both proven red: removing the `todos != nil` guard fails on
  `todo.updated {todos:[]}`; removing the `<-pending.answers` fallback fails with
  `err=context canceled`. Gates green: build, vet, `-race` serve+ai, lint (isolated
  cache), `test-race.sh`, grouped fast+cli, `go fix -diff` empty, docs-drift.
  `test-integration` not run (tmux absent).

### serve-file-list-race — `GET /file` resolves then `ReadDir`s the string

- **Status:** DONE — shipped on `wave-serve-file-list-race` as `8f6f37b`, merged into
  `main` as `d415ec2`.
- **Depends on:** serve-5, serve-allowlist-toctou
- **Why:** `listFiles` (`serve_handlers.go:468-515`) resolves the requested directory
  through `resolveInRoot` and then calls `os.ReadDir(resolved)` on the string. A component
  swapped for an outside-pointing symlink between the two is followed, so a writer with
  access to the served root can list an outside directory's entry names. The
  serve-allowlist-toctou wave fixed only `/file/content`; this is the same race on the
  listing path — names rather than content, but the same allowlist.
- **Files:** `internal/codingagent/serve/serve_handlers.go` (`listFiles`).
- **Gates:** a test that swaps a directory component for an outside symlink between
  resolve and list, asserting 403 and no outside entry names; the `beforeOpen` hook
  pattern from serve-allowlist-toctou transfers.
- **Notes:** `os.OpenInRoot(root, rel).ReadDir(-1)` confines the walk the same way the
  content fix does; keep the pre-open `os.Stat` so a path that resolves to a file is still
  400.
- **Shipped (`d415ec2`):** `listFiles` is a nil-passing wrapper over a new
  `serveListFiles(w, r, beforeOpen)`; the walk goes through `os.OpenInRoot(root, rel)` and
  `ReadDir(-1)`, with open failures classified by a fresh `resolveInRoot` (403/404, else
  500). `(*os.File).ReadDir(-1)` returns directory order, so entries are sorted by name to
  reproduce `os.ReadDir`'s documented order and keep the `maxFileEntries` survivor set
  unchanged. The pre-open `os.Stat` and the `!info.IsDir()` → 400 branch are kept. The
  `beforeOpen` hook drives two swap subtests (requested directory and intermediate
  directory); the test serves the outside entries when the confinement is removed. Same
  residual as the content path — the pre-open `os.Stat` still follows a swapped symlink,
  so an outside directory's entry *names* (not contents) could be observed if it reaches
  the stat first; the confinement closes the listing itself.

### serve-file-list-directory-param — `/file` ignores the `directory` query

- **Status:** FOLDED into `session-directory` on 2026-10-06 — no longer a
  separate dispatchable item. Decided: reject a foreign `?directory=` with 400,
  matching the four sibling handlers (the API stays a validation against the
  process root; see `session-directory` decision 4). Delivered by
  `session-directory` slice 2 (`9062a52`): `serveListFiles` gains
  `directoryParam`; `server.md`'s `/file` row lists `directory`.
- **Depends on:** serve-file-list-race
- **Why:** `listFiles` reads only `path` and never calls `directoryParam`
  (`serve_handlers.go:64-77`), while every sibling handler including `serveFileContent`
  does. `/file?directory=<other>` lists the session CWD instead of validating or rejecting
  the foreign directory.
- **Files:** `internal/codingagent/serve/serve_handlers.go` (`listFiles`).
- **Gates:** delivered by `session-directory` — `serveListFiles` calls `directoryParam`
  and `/file?directory=<foreign>` answers 400 like the sibling handlers (plan slice 2).
- **Notes:** Left out of serve-file-list-race to keep that slice to the security fix.
  Semantics decided 2026-10-06 (reject with 400); delivered by `session-directory`.

### coding-host-ci-race — `make check` does not race-test `coding/host`

- **Status:** **DONE** — shipped on `wave-coding-host-ci-race` as `d25866e`,
  merged into `main` (`--no-ff`) as `c6d2af7`. 2026-10-06.
- **Depends on:** serve-ci-race-serve
- **Why:** `serve-ci-race-serve` added `internal/codingagent/serve` to
  `automation/ci/test-race.sh`, but `coding/host` — the multi-session supervisor and typed
  event bus from serve-1, with a concurrent session registry and a per-session
  `sync.Mutex.TryLock` — still gets no `-race` coverage from `make check`. serve-4 ran it under
  `-race` manually only.
- **Files:** `automation/ci/test-race.sh`; `coding/host` tests.
- **Gates:** the serve race block's twin for `coding/host`, plus the same seeded-race proof
  (a race in `coding/host` makes the script exit non-zero).
- **Notes:** Same shape as the serve fix — whole package, `-count=1`, no `-run` filter.

### serve-ci-race-doc-drift — reconcile the TUI-only `test-race` descriptions

- **Status:** DONE — shipped on `wave-serve-ci-race-doc-drift` as `9975d23`, merged into `main` as `0d55c16`
- **Depends on:** serve-ci-race-serve
- **Why:** `serve-ci-race-serve` broadened `automation/ci/test-race.sh:2` to name serve, but
  the explanatory block (`test-race.sh:3-16`), the Makefile comment and help
  (`Makefile:131-135`), and `automation/README.md:48` still describe `test-race` as the TUI
  concurrency gate only. They now under-describe what the target runs.
- **Files:** `automation/ci/test-race.sh`, `Makefile`, `automation/README.md`.
- **Gates:** the descriptions name both the TUI and the serve/backend packages; no behavior
  change (the script's commands stay as they are).
- **Notes:** Cosmetic. The `serve-ci-race-serve` spec deliberately deferred it to keep that
  slice to the gate fix.

### serve-ci-host-race-echo — `test-race.sh` host echo overstates the cross-race

- **Status:** DONE — shipped on `wave-serve-ci-host-race-echo` as `369fa93`,
  merged into `main` as `e1f4ac2` (`--no-ff`), 2026-10-06.
- **Depends on:** serve-ci-race-doc-drift
- **Why:** `automation/ci/test-race.sh:37`'s echo says "host: multi-session supervisor vs
  typed event bus", implying a supervisor↔bus cross-race that `coding/host`'s tests do not
  create — no test installs a `Translator`, so `forwardEvents` drops events. The command is
  correct (it does race the package); only the prose promises a race the tests never
  exercise.
- **Files:** `automation/ci/test-race.sh` (the `host` echo line, ~37).
- **Gates:** the echo names what the host tests actually race, or a host test is added that
  installs a `Translator` so the promised supervisor↔bus race is real; `tests/docs-drift` via
  `make check`.
- **Notes:** Surfaced by `serve-ci-race-doc-drift`, which was forbidden from touching script
  commands. Cosmetic unless the second gate option is taken, in which case it is a real
  coverage gap in the same class as `coding-host-ci-race`.
- **Landed:** Took Option A — corrected the prose, no test added. `automation/ci/test-race.sh:37`
  now reads `host: concurrent session prompts + racing subscriber close`, and the block-header
  comment at `:23-24` names the same two races so the file is internally consistent. No command
  changed (the `go test -race -count=1 ./coding/host` line is byte-identical). Chose A over B on
  `AGENTS.md`'s no-new-tests default and the `docs-drift-docs-json` precedent: the defect is
  cosmetic and no money/data/safety cost attaches. Option B's marginal value is smaller than the
  original note implied — no `coding/host` test installs a `Translator`, but the serve gate
  (`test-race.sh:35`) does exercise that forward path, because `serve_test.go:168` calls
  `serve.New` which sets `h.Translate` (`serve.go:44`) and then drives prompts. Spec
  `docs/plans/2026-10-06-serve-ci-host-race-echo.md`. Verified by a 2-angle spec fan
  (runtime-seam, leanness/scope) whose Evidence-section findings were corrected before
  dispatch, and a 2-angle diff fan (scope-fit, gate-integrity), both CLEAN. Gates:
  `bash -n`, `bash automation/ci/test-race.sh` (new echo printed, green), `go build ./...`,
  `go vet ./...`, `go tool golangci-lint run` (0 issues), `go fix -diff` (clean),
  `go test ./tests/docs-drift/ -count=1` (ok), `go test ./... -count=1` (936 passed, 58
  packages). `make check` still stops at `test-prereqs` (tmux absent); `test-integration`
  likewise untested (tmux).

---

## Follow-ups surfaced by serve-6

### serve-cli-doc — `cli.md` never documents the `wopr serve` verb

- **Status:** DONE (`wave-serve-cli-doc`, commit `8186386`, merged `d2842ec`)
- **Depends on:** serve-6
- **Why:** `wopr serve` is a live CLI verb with `--port`/`--hostname` flags
  (`cmd/wopr/serve.go`), but `docs/site/docs/cli.md`'s command table has no row for it
  and a grep for "serve" in that page is empty. A reader who wants the server finds it
  only via README or `server.md`; the CLI reference is incomplete.
- **Files:** `docs/site/docs/cli.md`.
- **Gates:** `tests/docs-drift` via `make check`; the new row names the real flags and
  default behavior.
- **Notes:** Surfaced by serve-6, which was scope-locked to its five named files plus the
  index/docs.json needed to publish `server.md`. `server.md` is the contract; `cli.md`
  should point at it rather than duplicate the endpoint table.
- **Landed:** `docs/site/docs/cli.md` gains a `wopr serve [--port N] [--hostname H]` row in
  the Generic subcommands table, naming both flags, `server.port` default `4096` and
  `server.hostname` default `127.0.0.1`, and linking `[server](server.md)` instead of
  duplicating the endpoint table. Spec `docs/plans/2026-10-06-serve-cli-doc.md`. Verified by
  a 2-angle spec fan and a 2-angle diff fan (runtime-seam, scope-fit), both CLEAN. Flags and
  defaults re-derived from `cmd/wopr/serve.go:35-70` and
  `internal/codingagent/settings.go:257-258`. Gates: `go build ./...`, `go vet ./...`,
  `go test ./tests/docs-drift/ -count=1` (6 passed), `go tool golangci-lint run` (0 issues),
  `go fix -diff` clean, `go test ./... -count=1` (936 passed, 58 packages). `make check`
  still stops at `test-prereqs` (tmux absent).

### docs-drift-docs-json — `development.md` claims docs-drift checks `docs.json`

- **Status:** DONE (`wave-docs-drift-docs-json`, commit `2625071`, merged `494fa4d`)
- **Depends on:** none
- **Why:** `docs/site/docs/development.md:76` says "`docs/site/docs/docs.json` classifies
  and orders every page, and `make docs-drift` checks it." Neither test under
  `tests/docs-drift` opens `docs.json`; `make docs-drift` is only
  `go test ./tests/docs-drift/`. The claim overstates the gate, and a page added to the
  bundle without a `docs.json` entry passes green.
- **Files:** either `docs/site/docs/development.md` (correct the claim) or
  `tests/docs-drift` (add the check the page promises).
- **Gates:** `tests/docs-drift` via `make check`.
- **Notes:** Surfaced by serve-6, which added the `server.md` provenance and navigation
  entries by hand because no gate would have caught their absence.
- **Landed:** Took option A — corrected the claim rather than adding a gate. Deleted
  "`, and `make docs-drift` checks it`" from `development.md:76`; the page now reads
  "`docs/site/docs/docs.json` classifies and orders every page." Chosen because the repo's
  own test rule (`development.md:68`, `AGENTS.md`) bars new tests without a
  money/data/safety cost, `docs.json` is not embedded (`embed.go:11` is `//go:embed *.md`)
  and no code reads it, the page-unreachable harm is already gated by
  `TestEveryPageIsReachableFromTheIndex`, and adding a check would have made stale the
  `Makefile:158-160` gate comment. Spec `docs/plans/2026-10-06-docs-drift-docs-json.md`.
  Verified by a 3-angle spec fan (leanness, runtime-seam, completeness) and a 2-angle
  diff fan (scope-fit, claim-accuracy), all CLEAN; claim-accuracy recomputed
  provenance == navigation == 42 `.md` pages. Gates: `go build ./...`, `go vet ./...`,
  `go test ./tests/docs-drift/ -count=1` (6 passed), `make lint` (0 issues),
  `go fix -diff ./...` clean, on both the wave branch and merged `main`. `make check`
  still stops at `test-prereqs` (tmux absent, `Makefile:110-111`).

### docs-drift-cross-dir-links — docs-drift cannot see links outside the bundle

- **Status:** DONE (`wave-docs-drift-cross-dir-links`, commit `40d7c8f`)
- **Depends on:** none
- **Why:** `TestInternalLinksResolve`'s `docLink` regex
  (`\]\(([A-Za-z0-9_.-]+\.md)`) matches a single path segment, so a link like
  `../../threat-model.md` is invisible to it. serve-6 added two such links from
  `server.md` to `docs/threat-model.md`; they are correct today, but a future rename or
  deletion of `docs/threat-model.md` would break them with docs-drift still green.
- **Files:** `tests/docs-drift/docs_drift_test.go`.
- **Gates:** `tests/docs-drift` via `make check`; the check resolves multi-segment
  relative links against the repo root, and a deliberately broken one fails.
- **Notes:** Surfaced by the serve-6 diff verifier. Low blast radius; the two links are
  manually verified now.
- **Landed:** `docLink` widened to capture multi-segment relative paths; `TestInternalLinksResolve`
  resolves a `target` containing `/` from the linking page's directory
  (`filepath.Join(contentDir, filepath.Dir(name), target)`) and rejects a missing file with
  `os.Stat`, naming the page. Red-proof: a broken `../../threat-model.md` in `server.md`
  fails the gate. Verified by a two-angle diff fan.

### docs-drift-link-syntax-ceiling — docs-drift still misses links in non-plain Markdown forms

- **Status:** DONE (`wave-docs-drift-link-syntax-ceiling`, commit `ac00a15`, merged `1d41da1` `--no-ff`)
- **Depends on:** none
- **Why:** after `docs-drift-cross-dir-links`, `docLink` still matches only the plain form
  `](path.md[#frag])`. CommonMark-valid destinations with a title (`](page.md "Title")`),
  spaces or percent-encoding (`](my file.md)`, `](my%20file.md)`), angle brackets
  (`](<page.md>)`), and uppercase `.MD` are not matched at all, so a broken link in any of
  those forms passes the gate silently. Pre-existing; the cross-dir fix only widened the
  path-segment alternation.
- **Files:** `tests/docs-drift/docs_drift_test.go`.
- **Gates:** `tests/docs-drift`; a deliberately broken titled/encoded link fails.
- **Notes:** Surfaced by the cross-dir wave's false-green verifier. No such link exists in the
  bundle today, so the fix can be verified against a temporary one.
- **Landed:** `docLink` widened to the segment-structured pattern
  `\]\([ \t]*<?([A-Za-z0-9_.% -]+(?:/[A-Za-z0-9_.% -]+)*\.(?i:md))(?:#[^)]*)?>?(?:[ \t]+"[^"\n]*")?[ \t]*\)`
  (`tests/docs-drift/docs_drift_test.go:163`): angle-bracketed destination, trailing
  title, literal spaces, `%`-encoding, and upper-case `.MD` now match; group 1 keeps its
  old meaning so neither consumer changed. Segment structure preserved the old rejection
  of `//host`, `/.md`, `a//b.md` after the first cut (flat class) over-matched. Red-proof:
  a temporary line in `index.md` with one broken link per form fails
  `TestInternalLinksResolve` five times; removed, green. Spec
  `docs/plans/2026-10-06-docs-drift-link-syntax-ceiling.md`. Verified by a 4-angle spec fan
  (completeness, spec-fidelity, convention-feasibility, runtime-seam) and a 3-angle diff
  fan (scope-fit, gate-integrity, adversarial regex) after the correction; two warnings
  (fragment tail regression, protocol-relative over-match) were fixed before commit, the
  remainder documented as accepted. Accepted ceilings (false-red-only, absent from the
  bundle): a stray `>` after a bare destination (`](foo.md>)`) matches because RE2 cannot
  pair the optional `<`/`>` without a second capture group, and a `%`-encoded external URL
  matches because `%` is needed for the `%20` form and we do not decode. No new test;
  both consumers key `match[1]` unchanged. Gates: `go build ./...`, `go vet ./...`,
  `go test ./tests/docs-drift/ -count=1` (6 passed), `go tool golangci-lint run`
  (0 issues, after `cache clean`), on the branch and re-run on merged `main`.
  `make check` still stops at `test-prereqs` (tmux absent, `Makefile:110-111`).

---

## Open questions blocking nothing yet

- `serve-4`'s manual gate needs the actual iOS app, a laptop-hosted server, and a
  phone. If those are unavailable when that slice lands, the slice ships `UNVERIFIED` on
  that one gate and says so — do not claim it passed.
- Whether `--mode rpc` should gain the question path once `askuser` is wired in
  `serve-5`, given that omitting it is correct for a headless client and wrong for a
  phone.

---

## Product features (outside the serve batch)

### wopr-failover — Fall over to the next model instead of parking a rate-limited run

- **Status:** DONE — shipped on `wave-failover` as `626fb9d`, merged into `main` as `3907f7a` (`--no-ff`)
- **Depends on:** —
- **Why:** A dispatched run on a capped model parks forever: the parent's `task`
  call never returns and the wave silently ends. Today the only remedy is the
  external `opencode-failover` tool (switch a session's model, then abort) plus the
  persona pins — both outside wopr. wopr's own router should own the ladder: on a
  `free_tier_limit`/rate-limit failure, advance to the next configured model in the
  same turn, ratchet past a rung that just refused, and never park a dispatched
  subagent.
- **Files:** `internal/codingagent/router` (model selection; `coding/session_router.go`),
  `ai/` provider streaming retry path, session retry handling; `docs/routing.md`.
- **Gates:** a behavioral test: a simulated 429 / `free_tier_limit` on the active
  model advances to the next and the turn completes without stranding.
- **Notes:** Recorded at the owner's request 2026-10-06, after personas pinned to a
  dead free rung parked every dispatched wave until a server restart.
  `~/.config/opencode/MODELS.md` holds the ladder shape (free-first, paid terminal).
- **Shipped 2026-10-06:** one classifier `ai.IsProviderLimitError` (the old
  `rateLimitPattern` tokens plus `free_tier_limit` in the non-retryable family) and
  one `providerFailure` choke point in `coding/session_task.go` feeding both
  `routeFailover` and `taskHost.Failover`; the subagent failover gate now advances
  read-only children after tool calls (`out.ToolCalls == 0 || req.Type !=
  TypeGeneral`). A routed model that hits `free_tier_limit` advances to the next
  configured candidate in the same turn and rests the failed rung for
  `quotaBackoffMinutes`. Spec: `docs/plans/2026-10-06-wopr-failover.md`.
- **Scope limit (accepted):** this is the routing-active case. With routing off or a
  pinned orchestrator there is no configured ladder and the error is terminal, not a
  hang; an always-on ladder would collide with pin semantics. The literal
  persona-pin incident is therefore not solvable inside wopr without an explicit
  product decision to override pins. See `router-clock-seam` and
  `session-failover-registry-seam` below.

### router-clock-seam — make the failover cooldown assertable end-to-end

- **Status:** DONE — shipped on `wave-router-clock-seam` as `198e2da`+`861cfa5`, merged into `main` as `57a2230` (`--no-ff`)
- **Depends on:** —
- **Why:** `Router.now` is unexported and the only clock seam is `newTestRouter`
  inside package `router`, so a `coding` test cannot observe the 60s-vs-
  `quotaBackoffMinutes` effect at the `taskHost.Failover`/`routeFailover` call sites.
  Surfaced by `wopr-failover`'s verifier fan (Open Q2).
- **Files:** `internal/codingagent/router/router.go` (added exported test-only
  `SetClockForTest`); `coding/session_router_failover_test.go` (the shipped test —
  moved from the originally named `coding/session_retry_classify_test.go`, which is
  classifier-only; see Shipped).
- **Gates:** a `coding` test asserting a `free_tier_limit` failure rests the failed
  spec for `quotaBackoffMinutes`, not 60s.
- **Notes:** optional; the shipped slice pins the classifier at `providerFailure`
  and the cooldown contract in `router_test.go`.
- **Shipped 2026-10-06:** `Router.SetClockForTest(func() time.Time)` (`router.go`)
  is a mutex-guarded test-only clock seam; `New` still defaults to `time.Now`, and no
  production cooldown behavior changed. `TestSessionFailoverRestsTheFailedModelForQuotaBackoff`
  (`coding/session_router_failover_test.go`) drives a `free_tier_limit` failover
  through the real session (`faux-1`→`faux-2`), asserts the post-failover decision is
  `faux-2` and `At` is the fake clock (guards against a vacuous pass), advances 61s,
  and asserts `faux-1` is not re-offered — i.e. it rested `quotaBackoffMinutes`
  (15m), not the generic 60s. RED: compile-red (`SetClockForTest undefined`) before
  the seam; mutation-red (`Next`'s RateLimited cooldown forced to `time.Minute`)
  fails with `faux-1 re-offered after 61s`, reverted. `make check` fully green
  (tmux is installed). Spec: `docs/plans/2026-10-06-router-clock-seam.md`.

### session-failover-registry-seam — register a faux routed model so session failover is testable

- **Status:** DONE — shipped on `wave-session-failover-registry-seam` as `b8e5894`
  (+ `51f25ed` spec-citation fix)
- **Depends on:** —
- **Why:** `routeModel` rejects `test-faux/*` as an unknown model and no test writes
  `router.json`, so session-level same-turn orchestrator failover has no hermetic
  test. Surfaced by the `wopr-failover` spec (Open Q4).
- **Files:** `coding/session_router.go` (`routeModel`), test harness.
- **Gates:** a hermetic session test where an auto-routed orchestrator hits
  `free_tier_limit` and continues on the next model in the same turn.
- **Shipped 2026-10-06:** `NewServices` registers `test-faux/faux-1`/`faux-2` in the
  process registry via `SetProvider` under `WOPR_TEST_FAUX=1` — the same
  dynamic-provider path llama.cpp uses — so `routeModel` accepts them without any
  change to its validation. `ai/test_faux.go` gains a session-scoped
  `"Trigger: free tier limit"` fixture; `coding/session_router_failover_test.go`'s
  `TestSessionFailoverAdvancesToTheNextRoutedModel` drives an auto-routed
  orchestrator that hits `free_tier_limit` on `faux-1` and advances to `faux-2` in
  the same turn (Fallback route event, failed attempt omitted, `failover-ok`). RED
  with the seam stashed, GREEN after; repeatable `-count=3`. Spec:
  `docs/plans/2026-10-06-session-failover-registry-seam.md`.
- **Scope limit (accepted):** the seam is env-gated; with `WOPR_TEST_FAUX` unset,
  `routeModel` still rejects `test-faux/*` and `BuildModel` still errors. Under the
  env, the registration also exposes `test-faux/faux-1|faux-2` on the model-list
  surface (`GetAll`/`RuntimeModels`); `GetAvailable` still excludes them (no auth).
  Full `go test ./... -count=1` verified green.

### council-build-failover-guard — keep build children out of mid-task failover

- **Status:** DONE — shipped on `wave-council-build-failover-guard` as `ebefcbd`,
  merged into `main` as `3b1b73b` (`--no-ff`).
- **Depends on:** —
- **Why:** `wopr-failover` relaxed the subagent failover gate for read-only children
  by exempting `TypeGeneral` writers, but `TypeBuild` also writes code (in its own
  worktree). It is safe today only because `councilBuildHost`'s `Failover` returns
  false unconditionally; if council failover is ever implemented, a build child with
  tool calls would be eligible to rerun over its own edits. Surfaced by the
  `wopr-failover` diff verifier.
- **Files:** `internal/codingagent/subagent/task.go` (gate),
  `coding/session_council_build.go`.
- **Gates:** a test that a `TypeBuild` route with tool calls does not fail over past a
  provider error.
- **Outcome:** added `writesFiles` capability predicate (`TypeGeneral`/`TypeBuild`)
  and routed both the failover gate and the escalation gate through it, so the
  exemption is by capability, not by the council host's current `Failover` return.
  The escalation gate (same file, same loop) was folded in — a provider error sets
  `trigger = "provider error: …"`, so a failover-blocked build child would otherwise
  escalate over its worktree edits. `coding/session_council_build.go` needed no edit.
  Test: `TestBuildWriterDoesNotAdvancePastProviderError` (red pre-fix
  `failovers=1 escalated=1`). No failover eligibility was broadened.

### session-directory — let a session work in a directory other than the process cwd

- **Status:** DONE — shipped on `wave-session-directory` as `9e2c944` + `9062a52` + `b79bd06` + `5a9c2ff`, merged `--no-ff` into `main`. Spec at `docs/plans/2026-10-06-session-directory.md`,
  verified 2026-10-06 by three independent verifier fans (final pass 0 blockers;
  the four residual warnings/nits corrected). The plan is the single source for
  the decisions, files, slices, and gates; this entry records status and
  dependencies only, to avoid the two drifting.
- **Depends on:** —
- **Why:** wopr pins every session to the process cwd. The CLI reads `os.Getwd()`
  with no override (`cmd/wopr/main.go:349`; no directory flag in
  `cmd/wopr/args.go`), and `wopr serve` builds one `host.Host` → one
  `coding.Services` from the startup cwd (`cmd/wopr/serve.go`), so the API's
  `directory` query only *validates* against that single root. opencode lets you
  name the directory to work in; wopr requires `cd` first (CLI) and cannot serve a
  second directory at all (server).
- **Plan:** 3 slices, all launch-time — `--dir` for the CLI and `wopr serve` plus
  the global-layer `server.allowedRoots` guardrail; the `/file` directory
  validation; docs. One root per process; the API `directory` query stays a
  validation against that root (400 on mismatch), not a per-request selector.
  Consequence: `serve-file-list-directory-param` folds into the plan's `/file`
  slice. See the plan for the decisions, files, and per-slice gates.
## Follow-ups surfaced by session-directory

### serve-allowed-roots-fail-open — a malformed global settings.json silently disables the allowlist

- **Status:** DONE — shipped on `wave-serve-allowed-roots-fail-open` as `09bbd65`.
- **Why:** `runServeCommand` reads `server.allowedRoots` from the global layer and
  warns on a settings parse error, then continues. A malformed global
  `settings.json` zeroes the layer, `GetServerAllowedRoots` returns `nil, nil`,
  and `resolveServeRoot` treats an empty list as unrestricted — the guardrail is
  configured but off. Not attacker-triggerable unless the launcher can write
  `~/.wopr/settings.json`, but it is a fail-open robustness gap in the control
  that is supposed to fail closed.
- **Files:** `cmd/wopr/serve.go` (the allowlist read), `internal/codingagent/settings.go`.
- **Gates:** a test that a configured-but-unparsable global settings layer makes
  `wopr serve` refuse to start rather than serve unrestricted.
- **What shipped:** `SettingsManager.GlobalLoadError()` exposes the recorded
  global-layer load error; `globalServeAllowedRoots` in `cmd/wopr/serve.go`
  returns that error instead of the zero layer, and `runServeCommand` exits via
  `failf` before `resolveServeRoot`. An absent or parsed layer with an empty
  allowlist still returns `nil, nil` (unrestricted). Tests:
  `TestGlobalServeAllowedRootsFailsClosedOnMalformedGlobalSettings` and
  `TestRunServeCommandRefusesOnMalformedGlobalSettings`.
- **Known residual:** the guard covers JSON parse errors only. A wrong-typed
  `server`/`allowedRoots` value is swallowed by the shared tolerant decoder
  (`Settings.UnmarshalJSON` → `decodeTolerant`), so it still fails open — split
  out as `serve-allowed-roots-type-mismatch-fail-open` below rather than widened
  into this item.

### serve-allowed-roots-wiring-test — no test covers the global-layer read and the resolveServeRoot call

- **Status:** DONE — shipped on `wave-serve-allowed-roots-wiring-test` as
  `06a850a`; merged into `main`. Added `TestGlobalServeAllowedRootsReadsGlobalLayerOnly`
  (trusted project `settings.json` cannot widen the list) and
  `TestRunServeCommandRefusesRootOutsideGlobalAllowlist` (wiring calls
  `resolveServeRoot` with the global-derived list), both red-proofed against a
  `sm.Get()` read and a dropped call. Test-only; no production change.
- **Why:** `TestResolveServeRoot` exercises the pure helper and
  `TestGetServerAllowedRoots` the getter, but nothing asserts that
  `runServeCommand` reads the allowlist from the global layer (projectTrusted
  false) and actually calls `resolveServeRoot`. A regression reading merged
  project settings, or dropping the call, stays green. The plan calls the
  global-only rule load-bearing.
- **Files:** `cmd/wopr/serve.go`.
- **Gates:** assert a project `settings.json` under the served root cannot widen
  the list, and that `runServeCommand` reads the allowlist from the global layer
  (`projectTrusted` false). The testable helper now exists:
  `globalServeAllowedRoots` (added by `serve-allowed-roots-fail-open`, commit
  `09bbd65`), so the remaining work is the global-only/project-cannot-widen
  assertion at that seam.

### serve-allowed-roots-type-mismatch-fail-open — a wrong-typed server.allowedRoots still fails open

- **Status:** DONE — shipped on `wave-serve-allowed-roots-type-mismatch-fail-open`
  as `c7e9f0b` (+ `3c9904d` case-insensitive bypass fix); merged into `main`.
- **What shipped:** `checkServerRaw` in `internal/codingagent/settings.go` runs in
  `Settings.UnmarshalJSON` before `decodeTolerant` and returns a scoped error when a
  `server` entry present in the raw JSON is not an object, or a `server.allowedRoots`
  entry is present but not an array of strings. Keys are matched with
  `strings.EqualFold` because `encoding/json` resolves struct keys case-insensitively
  (`{"Server":...}`, `{"server":{"AllowedRoots":"x"}}`); an exact-key lookup was a
  bypass. The error propagates through the existing `GlobalLoadError()` plumbing, so
  `wopr serve` refuses to start; `cmd/wopr/serve.go` needed no change.
  `decodeTolerant` and unrelated-key tolerance are untouched. Tests:
  `TestUnmarshalJSONRejectsUndecodableServerGuardrail` (unit, incl. case variants),
  the `wrong-typed-roots` subtest of
  `TestGlobalServeAllowedRootsFailsClosedOnMalformedGlobalSettings`, and
  `TestRunServeCommandRefusesOnUndecodableServerGuardrail`.
- **Why:** `Settings.UnmarshalJSON` routes through `decodeTolerant`
  (`internal/codingagent/settings.go:298-354`), which drops a field whose JSON type
  mismatches its key and retries. So `{"server":5}`,
  `{"server":{"allowedRoots":"x"}}`, `{"server":{"allowedRoots":[5]}}` and
  `{"server":{"allowedRoots":{}}}` all parse successfully: `GlobalLoadError()` is
  `nil`, `Server` is nil or `AllowedRoots` empty, `GetServerAllowedRoots` returns
  `nil, nil`, and `resolveServeRoot` treats the empty list as unrestricted. The
  `serve-allowed-roots-fail-open` guard only catches JSON parse errors, so this
  variant still silently disables the guardrail.
- **Files:** `internal/codingagent/settings.go` (the decoder and the allowlist
  read), `cmd/wopr/serve.go`. Likely needs a "dropped/undecodable key" signal out
  of `decodeTolerant` scoped to `server`, since changing the tolerant decoder's
  repo-wide semantics would affect every setting; design decision required.
- **Gates:** a test that a wrong-typed `server` or `server.allowedRoots` in the
  global layer makes `wopr serve` refuse to start rather than serve unrestricted,
  without changing the tolerant behavior of unrelated settings.

### serve-allowed-roots-unknown-key-fail-open — a misspelled server.allowedRoots key silently disables the allowlist

- **Status:** DONE — shipped on `wave-serve-allowed-roots-unknown-key-fail-open`, merged into
  `main`. Owner decision option (b): a `server` key on the `allow…root` stem that is not the
  exact (case-folded) `allowedRoots`, with no valid `allowedRoots` present, makes `wopr serve`
  refuse to start; any other unrecognized `server` key warns and continues. Surfaced by the
  `serve-allowed-roots-type-mismatch-fail-open` verification (security angle).
- **Why:** the tolerant decoder ignores unknown keys, so `{"server":{"allowedRoot":"/srv"}}`
  (typo) or `{"server":{"allowedRootsX":[...]}}` parses successfully with an empty
  allowlist, `GetServerAllowedRoots` returns `nil, nil`, and `wopr serve` treats the
  empty list as unrestricted — the configured guardrail is silently off. Same
  fail-open class as the wrong-typed variant, but for a key the decoder never
  matches; the type-mismatch fix deliberately did not widen to it.
- **Files:** `internal/codingagent/settings.go` (the allowlist read and/or the
  `server` pre-check).
- **Gates:** a global `settings.json` whose `server` object contains a key that is
  near-miss for `allowedRoots` and no valid `allowedRoots` makes `wopr serve` warn or
  refuse rather than serve unrestricted; unrelated unknown keys stay tolerant.
- **Note:** the design decision is implemented as option (b). `serverKeyIsAllowedRootsNearMiss`
  in `internal/codingagent/settings.go` flags keys on the `allow…root` stem that
  `encoding/json` would not match (including whitespace and invisible `unicode.Cf` padding such as
  a pasted U+200B); `checkServerRaw` fails closed only when a near-miss exists and no valid
  `allowedRoots` was found. Other unrecognized `server` keys ride an unexported
  `Settings.serverUnknownKeys` + `SettingsManager.GlobalUnknownServerKeys`, printed by
  `cmd/wopr/serve.go` as `unrecognized server setting %q ignored`.
- **Known residuals (accepted; spec Open Questions):** JSON `null` for a near-miss or
  `allowedRoots` is treated as absent; an explicit empty `allowedRoots: []` beside a near-miss
  does not fail (empty is the documented unrestricted value); typos with no shared stem
  (`alowedRoots`, `allowedRots`, `roots`, `allowed`) are not caught.

### serve-server-key-near-miss-fail-open — a misspelled outer `server` key still fails open

- **Status:** DONE — shipped on `wave-serve-server-key-near-miss-fail-open`, merged into `main`.
  A new outer-key near-miss predicate (`serverKeyIsServerNearMiss`) catches a top-level key that
  looks like `server` and, when no exact `server` supplied a valid `allowedRoots`, `wopr serve`
  refuses to start naming the key (`"server " is not a known key; did you mean server?`); a valid
  exact `server` wins and the near-miss key warns via the existing unknown-key path. Padded,
  invisible-rune-padded (any Unicode category including interior and non-ASCII case-folding runes),
  and stray-letter-extended outer keys all fail closed; a genuinely unrelated top-level key stays
  tolerant.
- **Known residuals (accepted; spec Open Questions):** name mutations that break the contiguous
  `server` stem (`srvr`, `serve`, `srever`, insertions like `serxver`) and non-ASCII letter
  confusables (`ｓｅｒｖｅｒ`, Cyrillic `ѕerver`) still fail open — catching them needs edit distance,
  which the shipped inner-key item also rejects. A near-miss outer key holding only `port` (no
  `allowedRoots`) also refuses, a deliberate fail-closed extension of the owner's option-(b)
  precedent.
- **Why:** `checkServerRaw` matches the outer key with `strings.EqualFold(key, "server")` and no
  trim, so `{"server ":{"allowedRoots":["/srv"]}}` (or an invisible-rune-padded `server`) is never
  inspected; `encoding/json` also will not match it, the whole `server` object is dropped by
  `decodeTolerant`, `GetServerAllowedRoots` returns `nil, nil`, and `wopr serve` serves
  unrestricted. Same fail-open class as the inner-key item, one level up. Pre-existing, not
  introduced by that item.
- **Files:** `internal/codingagent/settings.go` (the `server` key match in `checkServerRaw`).
- **Gates:** a global `settings.json` whose top-level `server` key is padded/misspelled and holds
  a valid `allowedRoots` makes `wopr serve` refuse rather than serve unrestricted; a genuinely
  unrelated top-level key stays tolerant.

### serve-unknown-server-key-schema-drift — the known-key set duplicates ServerSettings' JSON tags

- **Status:** DONE — shipped on `wave-serve-unknown-server-key-schema-drift`, merged into `main`.
  `checkServerRaw`'s known inner-key set is now derived at runtime from `ServerSettings`' fields via
  the new `isServerSettingKey` helper, which mirrors `encoding/json` field resolution (JSON tag name,
  or the Go field name for an untagged / `json:",opt"` field; unexported and `json:"-"` skipped), so
  a future `ServerSettings` field — tagged or not — can never land in `serverUnknownKeys` or trip the
  `allowedRoots` near-miss refusal. `allowedRoots` array validation and `port`/`hostname` behavior are
  unchanged. No test added (the fix enforces the coupling structurally; spec `docs/plans/2026-10-07-serve-unknown-server-key-schema-drift.md`).
- **Why:** `checkServerRaw` hard-codes the known `server` keys (`port`, `hostname`,
  `allowedRoots`). A future `ServerSettings` field added to the struct but not to that set is
  still applied by `decodeTolerant`, yet lands in the unknown-key list and prints a spurious
  "unrecognized server setting" warning — and, if its name matched the `allow…root` stem, would
  refuse a valid config. A drift hazard, not a current bug.
- **Files:** `internal/codingagent/settings.go`.
- **Gates:** a `ServerSettings` field addition cannot produce a false unknown-key warning
  (derive the known set from the struct tags, or add a test coupling the two).

---

## V2 contract coverage

### serve-v2-api-parity — full contract coverage of opencode V2 `/api/*`

- **Status:** READY — filed 2026-10-15. Spec: `docs/plans/2026-10-15-serve-v2-full-support.md`,
  which supersedes `docs/plans/2026-10-07-serve-v2-api-parity.md` (Rev-1; its pin was a file in
  `/tmp` and is gone).
- **Why:** wopr serves the opencode V1 contract. V2 (`/api/*`) is a **second, separate surface**, and the
  plan pins only one of the two: the published `https://opencode.ai/v2/openapi.json` (142 operations),
  while `opencode-src` @ v1.18.34's `packages/protocol/src/groups/` is a second V2 route set (58
  endpoints — 46 shared with the published spec, 12 protocol-only, 96 published-only). The two disagree,
  so no V2 client works against wopr today beyond the two V1-era routes wopr happens to serve under that
  prefix.
- **Scope (owner, 2026-10-15): full contract coverage, not literal parity.** Every `/api/*` path
  answers a spec-valid response — real where wopr has the engine (T1, 28 operations), a
  `503 ServiceUnavailableError` stub with a named reason for T2 (113), and one T3 namespace
  conflict, `GET /api/command` (1). The three tiers partition all 142 published-spec operations; wopr's
  `GET /api/health` is in no published spec and in no tier (it is one of the 12 protocol-only
  endpoints). No route answers Go's bare 404. Implementing PTY,
  worktrees, VCS, MCP, credentials, integrations, plugin RPC, pairing, a durable inbox, and the
  form engine is out of scope: no engine, and it would force the root `coding` package across the
  `internal/codingagent/serve` seam.
- **Spec pin:** `https://opencode.ai/v2/openapi.json`, sha256
  `3fcd666b597b746bad26f56a92fe782cc86338a0b4498f43b435a2f97e4fa05a`, 578,272 bytes, OpenAPI 3.1.0,
  118 paths / 142 operations / 285 schemas, fetched 2026-10-15. The spec moved inside eight days
  **(+1 path, +1 operation, +29 schemas — inferred from Rev-1's recorded counts, not re-derived; the
  prior pin file is gone)**, so the pin is a table and a hash, never a vendored file.
- **Carries six open decisions**, none blocking: **D9 — which V2 surface is the specification for
  `wopr serve`? — the most consequential, and the owner's to make (`GET /api/health` is already
  conformant under the protocol surface and unclassified under the published spec; 96 operations exist
  only under the published spec). Recommend specifying against **both**, with the 46 shared endpoints
  as the floor.** D1 — capture KKCode's real V2 traffic, now
  non-blocking because the spec is pinned and S1 classifies all 142 operations from it. D5 — forms
  stay a stub; recommend (b), rename to `question.v2.asked/replied/rejected` **in the V2 encoder
  only**, since the bus constants at `serve_sse.go:52-54` feed the V1 stream KKCode consumes. D7 —
  `GET /api/command` is served today with `location.project`, which V2's `Location.PublicRef`
  (`additionalProperties:false`) rejects; **recommend (b) over (a)** — keep `project` and record a
  divergence, since the KKCode capture (`serve-kkcode-v1-compat`) shows the reference server returns
  `project`. Dropping it would diverge from the client's actual target. D8 — V2 identifier format (`^ses`/`^msg_` vs wopr's UUIDs), low priority. D10 — T1 request members wopr cannot honor: ignore or reject (two query parameters, recommend ignore and log).
- **Files:** `internal/codingagent/serve/` (`serve_v2.go`, `serve_v2_dto.go`, `serve_v2_sse.go`,
  `serve_v2_test.go`, plus generated `testdata/opencode-v2-inventory.json` and
  `testdata/opencode-v2-schemas.json`), `cmd/wopr/serve.go` wiring, `docs/site/docs/server.md`,
  `docs/threat-model.md`, and `docs/site/docs/settings.md` **only if a V2 setting ships** — D3 dropped
  `server.authToken`, so as things stand no V2 content reaches that file.
- **Gates:** per slice the standard set, hermetic (temp `WOPR_HOME`, provider credentials cleared).
  S1's gate is **not** a test over the committed inventory: that is the change-detector category
  `AGENTS.md` bans, and the V1 conformance plan ruled against it in writing. S1 is done when the
  three tiers partition the spec's operation set exactly. S2 tests the catch-all and decodes
  `ServerInfo`. S3 and S4 carry a round-trip decode gate per V2 DTO against the pinned schemas; S4
  adds a manual live-curl probe. S5 decodes all read-surface DTOs (except `ServerInfo`, tested in
  S2). S6 runs `make check` docs-drift and a matrix test of the T2 catch-all handler (not all 113
  operations individually, which would need concrete path-param values).
- **Notes:** the two `/api/*` routes wopr already serves are V1-era KKCode handshake shims
  (`/api/health`, `/api/command`), and V2 has already taken over that prefix — the namespace is
  crowded, so new V2 work must not assume it is clean. S3 also depends on the unmerged
  `serve-delete-hard-delete`, which requires hard delete. Settled by owner 2026-10-07: ignore the
  client's `Authorization` header and never emit 401, unlike the spec, which documents 401 on all
  142 operations and defines no security scheme.

---

## Follow-ups surfaced by KKCode remote-client testing

### serve-kkcode-v1-compat — KKCode mobile client 404s on connect: missing `/api/health` and sibling routes

- **Status:** DONE — shipped on `wave-serve-kkcode-v1-compat`, merged into `main`; surfaced testing `wopr serve` against the KKCode iOS client
  (closed-source; UA `KKCode/29694823`). Captured by a logging reverse proxy in front of a real
  opencode 1.18.34 server.
- **Why:** KKCode's first connect call is `GET /api/health`, which wopr does not serve, so the whole
  connect aborts with a 404 before it reaches the V1 surface wopr already implements. A full
  connect → project → session → prompt flow uses exactly these routes: `/api/health`, `/global/health`,
  `/experimental/session`, `/project`, `/global/event` (SSE), `/file`, `POST /session`, `/api/command`,
  `/session/{id}/todo`, `/session/{id}/message`, `/agent`, `/config/providers`, `/config`, `/permission`,
  `/question`, `POST /session/{id}/prompt_async`. wopr already serves all but four routes and one
  status: `GET /api/health` → 404 (opencode answers `{"healthy":true}`); `GET /experimental/session`
  → 404 (same session list as `GET /session`); `GET /config` → 404 (opencode returns an object with
  keys `$schema,agent,command,instructions,mcp,mode,model,permission,plugin,provider,subagent_depth,
  username`); `GET /api/command` → 404 (opencode returns `{location:{directory,project},data:[{name,
  description,template}]}`, e.g. the built-in `init`); `GET /permission` → currently 501, should be
  `200 []`. `/api/status` and `/api/info` are fallback probes that only fire when `/api/health` fails
  (opencode serves them the web-UI HTML), so they are not required.
- **Files:** `internal/codingagent/serve/` (route registration, handlers, DTOs), `docs/site/docs/server.md`
  (endpoint table).
- **Gates:** hermetic tests pinning each new route's status and body shape; a live probe against a real
  listener. Manual: KKCode connects to `wopr serve` through the logging proxy with no 404.
- **Notes:** The V2 tiered-parity plan `docs/plans/2026-10-07-serve-v2-api-parity.md` is NOT needed for
  KKCode (superseded for this client by the capture above); keep it as future work for genuinely V2
  clients. Auth: wopr ignores the client's Basic `Authorization` header and never emits 401, by owner
  decision 2026-10-07.

### serve-test-faux-fallback-in-production — `wopr serve` reports a fake model when startup resolution yields none

- **Status:** DONE — shipped on `wave-serve-test-faux-fallback-in-production`, 2026-10-07.
  Confirmed reachable in production: a fresh `WOPR_HOME` with no authenticated provider and no saved default
  leaves `selectStartupModel` returning a nil model with a nil error. Fix: the `test-faux/faux-1` fallback in
  `cmd/wopr/serve.go` is deleted; `modelSpec` stays `""` when no model resolves, so `GET /config` reports
  `"model":""` (key present, never a fake provider) and `POST /session` answers the existing `409` via
  `codinghost.ErrNoModel` ("no model is configured for this server…"). Gate
  `TestRunServeCommandDoesNotReportFauxModelWithoutProvider` (`cmd/wopr/serve_test.go`) drives the real binary
  hermetically and asserts the key present/string/empty plus the 409; reproduced red pre-fix (`"test-faux/faux-1"`)
  and green post-fix. Spec was a planning artifact and is not committed; the decision is recorded here.
- **Note:** the item allowed "fail with a clear message or omit the field". Chosen: omit the fake while keeping the
  `model` JSON key with an empty string, because an absent object field is the repo's documented client-crash class
  (`internal/codingagent/serve/serve_dto.go`); the loud per-session `409` is the clear failure. No `omitempty`.
- **Why:** `cmd/wopr/serve.go:142-147` sets `modelSpec = "test-faux/faux-1"` (comment "fallback for tests")
  whenever `selectStartupModel` returns no model, which it can do with a nil error when no provider is
  authenticated and no default resolves (`cmd/wopr/startup_model.go:134-137`). In that state `GET /config`
  would return `{"model":"test-faux/faux-1"}` and sessions would start against a nonexistent provider rather
  than failing loudly. Confirm whether that state is reachable in production and either fail with a clear
  message or omit the field.
- **Files:** `cmd/wopr/serve.go`, possibly `cmd/wopr/startup_model.go`.
- **Gates:** a behavioral test for the no-model state; `make check`.

### serve-kkcode-dto-shape-parity — KKCode aborts with "cannot convert undefined value to object": wopr's V1 DTOs are skinnier than opencode's real ones

- **Status:** DONE — shipped on `wave-kkcode-dto-shape-parity`, merged into `main`; surfaced by driving KKCode against `wopr serve` through a logging proxy;
  the `/file` entry-shape fix (commit `78773a2`) cleared the first such crash, this was the next.
  `GET /config` now carries the 12 captured keys, `GET /config/providers` the structural
  `{providers:[…],default:{}}` (never a real `key`), sessions gain `agent`/`model{…}` (never null)/
  `path`/`project`/`summary`/`tokens{reasoning,total}`, `/agent` the 13-field item, and message info the
  full key set with a `path` object and `parts[].snapshot`; the `message.updated` stream tracks the same
  shape through one shared builder. Spec: `docs/plans/2026-10-07-serve-kkcode-dto-shape-parity.md`.
- **Why:** KKCode decodes opencode's **actual** response objects, and wopr's DTOs omit objects/arrays the
  client then calls `Object.keys`/spread on, which throws. Captured exact shapes from opencode 1.18.34 into
  `/tmp/opencode-shapes/*.json` (regenerate with `/tmp/capture_shapes.py`, reading `OPENCODE_SERVER_PASSWORD`
  from the running server's `/proc/<pid>/environ`; do not commit those files). Deltas:
  - `GET /config/providers` — wopr returns a **map** `{providerID:{name,api,models}}`; opencode returns
    `{providers:[{id,name,source,env:[],key,options:{},models:{}}], default:{providerID:modelID}}`.
    **Structural change** (breaks the old shape). Never emit a real provider `key` — use `""`.
  - `GET /config` — wopr returns `{model}`; opencode returns `$schema,agent{},command{},instructions[],
    mcp{},mode{},model,permission{},plugin[],provider{},subagent_depth,username`.
  - session object (list/`experimental`/get/create) — wopr omits `agent`, `model` (currently `null`),
    `path`, `project{id,worktree}`, `summary{additions,deletions,files}`; `tokens` misses `reasoning`/`total`.
    opencode: `{id,slug,projectID,directory,path,title,agent,model{id,providerID,variant},version,
    summary,cost,tokens{input,output,reasoning,cache{read,write}},time{created,updated},project}`.
  - `GET /agent` — wopr returns `{name}`; opencode items are `{name,description,mode,native,hidden,topP,
    temperature,color,permission[],variant,prompt,options{},steps}`.
  - message info (`GET /session/{id}/message`) — wopr returns `{id,sessionID,role,time{created}}`; opencode
    adds `agent,cost,finish,mode,modelID,parentID,path{cwd,root},providerID,tokens{total,input,output,
    reasoning,cache}`; parts may carry `snapshot`.
  - **Rule:** every object/array field the captured shape has must be present with the right JSON type even
    when wopr has no data — an absent (undefined) object is what throws, a `{}`/`[]`/zero is safe.
- **Files:** `internal/codingagent/serve/serve_dto.go`, `serve_handlers.go` (the affected handlers),
  `docs/site/docs/server.md` (endpoint/response notes).
- **Gates:** hermetic tests asserting each route's body decodes with the captured key set and correct JSON
  types; the existing suite; `make check`. Live: KKCode completes connect → folder → session → streamed
  prompt with no JS error.
- **Notes:** This is compatibility with opencode's real shapes, not the V2 `/api` surface. The old V1 client
  (`grapeot` @ 2d0e7bd) may not tolerate the `config/providers` structural change — flag it; KKCode is the
  target. `serve-test-faux-fallback-in-production` above is a prerequisite for a truthful `model` field.

### serve-session-model-from-change-entry — disk-only session `model` reports the server default, not the session's own last model

- **Status:** DONE — shipped on `wave-serve-session-model-from-change-entry`, merged into `main`. `sessionRecord` now carries `ModelID`/`ModelProvider`, read from the same `walkBack` tail `sessionRecordAt` already parses for the session list (no second file read), and `buildSessionDTO` reports it between the live `Model()` and the server `modelSpec` fallback. Gate `TestDiskSessionModelFromChangeEntry`: disk-only session with `model_change` `anthropic/claude-x` reports that model, and one with no `model_change` still reports the server default `test-faux/faux-1`; independently reproduced red pre-fix / green post-fix. Accepted boundaries: an empty-provider `model_change` is reported verbatim (`providerID:""`), and a `model_change` beyond the 32 KiB `metaProbeBytes` tail bound is not seen. Surfaced 2026-10-07 by the `serve-kkcode-dto-shape-parity` wave.
- **Why:** `buildSessionDTO` has no model in `sessionRecord` (`serve_read.go`), so a session with no live
  counterpart reports the server's current `modelSpec` as `session.model`. A session last used with another
  model, or created by another server run, is misreported. The transcript already records it in the newest
  `model_change` entry (`internal/codingagent/session.go:64`, `ModelChangeEntry{Provider, ModelID}`).
- **Files:** `internal/codingagent/serve/serve_handlers.go` (`buildSessionDTO`), possibly `serve_read.go`.
- **Gates:** a test that a disk-only session whose `model_change` names another model reports that model;
  `make check`. It must read from the transcript tail already parsed for the session list, not add a second
  full-file read per session.

### serve-sse-message-lifecycle-parity — KKCode renders the reply twice and never settles

- **Status:** DONE — shipped on `wave-serve-sse-message-lifecycle-parity`, merged into `main`; surfaced 2026-10-07 driving KKCode against `wopr serve`. The session mints the assistant entry id at message start (stream and transcript share one id), the stream announces the user message and its text part, and the assistant `message.updated` brackets the parts. The payload-envelope fix
  (`4eb51e1`) produced the first reply and the abort-JSON fix (`47d5ef8`) fixed Stop, but a turn still
  double-renders and the client never returns to idle.
- **Why:** comparing one turn's `/global/event` frames from opencode 1.18.34 (`/tmp/oc-turn.txt`) and wopr
  (`/tmp/wopr-sse2.txt`; regenerate against a running server, do not commit):
  - wopr emits **no** `message.updated`/`message.part.updated` for the **user** message; opencode emits
    `message.updated{info.role:"user"}` plus the user text part before the assistant run. The client builds
    the user turn from these.
  - opencode keeps part ids **stable** (`prt_…`) from creation through `message.part.delta` and the final
    `message.part.updated`; wopr streams deltas under a provisional `msg_<session>-<n>-text-0` id and then
    publishes the final part under `<entryid>-text-0`. The same text arrives under two ids, so the client
    renders the reply twice. This is `eventMapper.provisionalID` (serve-4's reconciliation); the client does
    not reconcile.
  - opencode emits `message.updated{role:"assistant"}` (stable id) **before** the parts and again at the
    end; wopr emits it once, at the end.
  - opencode emits `step-start`/`step-finish` parts; wopr emits neither (may be optional).
  - Both now emit `session.status{status:{type}}` and `session.idle`; the client still does not settle,
    likely a consequence of the id mismatch above.
- **Files:** `internal/codingagent/serve/serve_sse.go` (mapper and part construction); possibly
  `serve_dto.go`/`serve_read.go` for id minting.
- **Gates:** a hermetic test that replays one turn and asserts the frame sequence — user `message.updated`
  plus part, a stable part id across delta/updated, an assistant `message.updated`, terminal `session.idle`;
  the existing suite; `make check`. Live: KKCode shows the reply once and the spinner settles.
- **Notes:** the likely fix is to mint stable message/part ids at message start (as opencode does) instead
  of the provisional-then-reconcile scheme, without changing the persisted transcript ids `GET
  /session/{id}/message` returns. Preserve serve-4's invariant that the stream and the transcript agree on
  part ids for a completed message.

### serve-stream-parentid-parity — streamed `message.updated` reports an empty `info.parentID`

- **Status:** DONE — shipped on `wave-serve-stream-parentid-parity`. The per-session SSE mapper remembers the turn's
  user message entry id (`eventMapper.turnUserID`, set from the host-resolved id at the user `message_end`) and emits
  it as `info.parentID` on both the assistant start announce and the end burst, matching opencode 1.18.34's
  turn-threading (every assistant message in a turn parents on the initiating user message). The new gate in
  `TestSSETurnLifecycleParity` (step 3b) asserts the assistant `message.updated` parentID equals the user
  `message.updated` id. The read API still reports the raw persisted parent, so it diverges from the stream —
  logged as `serve-transcript-parentid-skips-metadata`.
- **Why:** the SSE mapper passes `""` for the assistant message's parent on both the start announce and the
  end burst (`internal/codingagent/serve/serve_sse.go`), while `entriesToMessages` and opencode 1.18.34 report
  the real parent entry id (`/tmp/oc-turn.txt:155`). A client that threads turns by `parentID` cannot attach
  the streamed reply to its user message. Pre-existing; outside the id-stability item's definition of done.
- **Files:** `internal/codingagent/serve/serve_sse.go` (mapper parent plumbing), possibly
  `coding/host/host.go` if the parent entry id must be resolved alongside `entryID`.
- **Gates:** an assertion in `TestSSETurnLifecycleParity` that the assistant `message.updated` parentID equals
  the user `message.updated` id; the serve suite; `make check`.

### serve-transcript-parentid-skips-metadata — transcript read API reports the interposed online-context custom entry as an assistant message's parentID

- **Status:** DONE — shipped on `wave-serve-transcript-parentid-skips-metadata`. `entriesToMessages` now resolves an assistant's `parentID` to the nearest ancestor whose `Base.Type == "message"` by walking a full id→entry map built from the entries it is given (never slice order), and `assistantMessageDTO` passes the full transcript instead of `entries[start:]`. `GET /session/{id}/message` and the POST `/message` response now report the initiating user message id, matching the stream. Gates: `TestEntriesToMessagesResolvesParentPastMetadata` (deterministic, custom-entry-interposed fixture) and the parentID assertion in `TestMessageAnswers200WithTheFinishedAssistantMessage`; both independently proven red pre-fix and green post-fix. Caveat: `GET /session/{id}/message` is tail-limited (`maxLimit` 200), so a window that opens after the initiating user entry reports `parentID: ""` for a parent outside the window. The multi-step divergence is surfaced as `serve-transcript-parentid-multistep` below.
- **Why:** `entriesToMessages` reads the persisted entry's raw `Base.ParentID`
  (`internal/codingagent/serve/serve_handlers.go:1119-1121`). Online Context Compact appends a
  `harness-online-context-state-v1` custom entry per provider request
  (`coding/session_efficiency.go:efficiencyRecordRequest` → `AppendCustomEntry` → `appendOnLeaf`), from
  `coding/session_transcript.go:prepareRequest`, and it lands between the persisted user message and the assistant
  message. So `GET /session/{id}/message` reports an assistant `parentID` naming a custom entry that is skipped as a
  message, and a client threading the fetched transcript cannot attach the reply. `serve-stream-parentid-parity`
  fixed the stream to report the initiating user message id, so the read API and the stream now disagree.
- **Files:** `internal/codingagent/serve/serve_handlers.go` (`entriesToMessages` parent resolution),
  `internal/codingagent/serve/serve_prompt.go` (`assistantMessageDTO` passes `entries[start:]`, so a slice-order
  "nearest preceding message" tracker returns `""`).
- **Gates:** resolve the nearest `message` ancestor via a full id→entry map (not slice order) and assert an
  assistant's transcript parentID equals the user message id; the serve suite; `make check`.

### serve-transcript-parentid-multistep — transcript read API reports a toolResult message as a later assistant message's parentID

- **Status:** DONE — shipped on `wave-serve-transcript-parentid-multistep`. `nearestMessageAncestor` gained a `wantUser` flag; `entriesToMessages` passes `assistant != nil`, so assistant rows resolve `parentID` to the nearest **user-role** message ancestor while user/toolResult rows keep the nearest-message walk. A later assistant of a multi-step tool turn now reports the turn's initiating user message, matching `eventMapper.turnUserID`. Keep-minimal decision: user/toolResult rows are untouched, so no new values appear for rows the stream does not model. Gate: `TestEntriesToMessagesAssistantParentsOnTurnUserInMultiStepToolTurn` (`u1 → c1 → a1 → t1(toolResult) → c2 → a2`) asserts `a1` and `a2` both parent on `u1`; independently reproduced red pre-fix (`a2` was `t1`) and green post-fix. Serve suite + `make check` green. Residual (out of scope, pre-existing): a later **user** row still reports the prior assistant rather than the stream's `""`; surfaced as `serve-transcript-parentid-userturn` below.
- **Why:** `nearestMessageAncestor` resolves to the nearest `Base.Type == "message"` ancestor. In a tool-using turn the
  persisted chain is `user → custom → A1 → T1 (toolResult message) → custom → A2`, so `A2`'s transcript `parentID`
  is `T1`, while the stream (`eventMapper.turnUserID`) reports the turn's initiating user message id for **every**
  assistant message. The named gate for `serve-transcript-parentid-skips-metadata` covers only the single-assistant
  interposed case, where nearest-message and user coincide. Resolving to the nearest **user-role** message ancestor
  would restore read/stream parity across multi-step turns.
- **Files:** `internal/codingagent/serve/serve_handlers.go` (`nearestMessageAncestor`).
- **Gates:** an assistant transcript `parentID` equals the turn's user message id for a multi-step `A1 → T1 → A2`
  fixture; the serve suite; `make check`.

### serve-transcript-parentid-userturn — transcript read API reports the previous assistant as a later user message's parentID

- **Status:** DONE — shipped on `wave-serve-transcript-parentid-userturn`. `entriesToMessages` now leaves a user-role row's transcript `parentID` at `""` (guard on `me.Message.User == nil`, skipping the nearest-message walk), matching the stream's user `message.updated` (`serve_sse.go` `messageEnd` emits parentID `""`). Assistant/toolResult/system/custom resolution is byte-identical to before: every non-user row still calls `nearestMessageAncestor(byID, e, assistant != nil)`. Gate: `TestEntriesToMessagesUserTurnParentIsEmpty` (fixture `u1 → a1 → u2`, `u2` raw parent `a1`) asserts `u2` parentID `""` and the neighbouring `a1` parentID `u1`; independently reproduced red pre-fix (`u2` was `a1`) in a throwaway checkout by three verification angles and green post-fix. Serve suite 88 passed, `golangci-lint` 0 issues, `go fix -diff` empty, `make check` all green in the worktree. Spec: `docs/plans/2026-10-07-serve-transcript-parentid-userturn.md` (planning artifact, not committed).
- **Why:** the stream emits a user `message.updated` with `parentID: ""` (`internal/codingagent/serve/serve_sse.go`),
  and captured opencode 1.18.34 frames agree (user messages carry no parentID). But `entriesToMessages` computes a
  user row's `parentID` with the nearest-message walk, so a later user turn (`… → A_n → U2`) reports `A_n`. Read and
  stream disagree for user rows; unpinned. Deliberately left out of scope by `serve-transcript-parentid-multistep`
  to keep its blast radius to assistant rows.
- **Files:** `internal/codingagent/serve/serve_handlers.go` (`entriesToMessages` parent resolution for user rows).
- **Gates:** an assertion that a second-user-turn transcript `parentID` equals `""`, matching the stream; the serve
  suite; `make check`.

### serve-tool-delta-test-vacuous-equality-loop — `TestToolCallDeltaReachesTheStream`'s cross-source check never runs

- **Status:** DONE — shipped on `wave-serve-tool-delta-test-vacuous-equality-loop` (test-only). The `readSSEUntil`
  predicate in `TestToolCallDeltaReachesTheStream` now stops only once a tool `arguments` delta **and** a tool
  `message.part.updated` have arrived; a new `toolUpdatedPartID` helper drives it. The post-loop check gained a
  `toolPartSeen` guard (`t.Fatalf` when no part matches the delta's call id) so the equality `t.Errorf` runs and can
  fail. Proven non-vacuous by a throwaway mutation: `serve_sse.go:474` `stream.ToolCall.ID` → `+"mut"` turns the test
  red at the guard; restored. Serve suite 89 passed, `make check` all green (build, vet, lint, test-race, integration,
  go-fix, docs-drift). Spec: `docs/plans/2026-10-07-serve-tool-delta-test-vacuous-equality-loop.md` (planning artifact,
  not committed).
- **Why:** the test stopped at the first tool `arguments` delta, but the matching tool `message.part.updated` is
  emitted later, so the loop that claimed to prove the delta and the tool part share an id
  (`internal/codingagent/serve/serve_test.go`) never executed. Pre-existing; the earlier diff only changed the prefix
  assertion. Delivered the "extend the read and keep a check that can fail" option, not deletion.
- **Files:** `internal/codingagent/serve/serve_test.go`.
- **Gates:** the serve suite; `make check`.

### serve-user-message-nested-model — KKCode crashes "Cannot read properties of undefined (reading 'providerID')"

- **Status:** DONE — shipped on `wave-serve-user-message-nested-model`, merged into `main` (`--no-ff`). `messageInfoDTO`
  (`internal/codingagent/serve/serve_dto.go`) gained a value-receiver `MarshalJSON` that branches on `role`: a
  `"user"` message emits opencode's nested shape (`id,sessionID,role,time,summary:{diffs:[]},agent,model:{providerID,modelID}`)
  and none of the flat fields, while every other role emits the unchanged flat shape through a methodless `plain` alias.
  The value receiver is required because the SSE bus and `TestMessageDTODecode` marshal non-addressable values; the GET
  path would have tolerated a pointer receiver. The existing `assertInfoShape` oracle now dispatches on role to a new
  shared `assertUserInfoShape`, so `TestMessageInfoShapeMatchesOpencode` and `TestSSEMessageUpdatedShapeMatchesOpencode`
  stay green on the user row. Gate `TestMessageInfoRoleShapes` covers both callers (`entriesToMessages` read path and
  `eventMapper.messageEnd` SSE `message.updated`); independently reproduced red pre-fix (13 flat keys, no `model`) /
  green post-fix. Accepted boundary: the inner `json.Marshal` re-enables HTML escaping, so a GET `path.cwd`/`root`
  containing `&<>` emits `\u00XX` where it previously emitted raw (decoded values identical; SSE already escaped).
  Spec: `docs/plans/2026-10-07-serve-user-message-nested-model.md` (planning artifact, not committed). Live KKCode
  acceptance is deferred to the owner post-merge (non-hermetic, not part of the green gates). Surfaced 2026-10-07
  driving KKCode against `wopr serve` after the SSE lifecycle fixes.
- **Why:** opencode's **user** message info is a different shape from the assistant's. Verified live on opencode
  1.18.34: `{id,sessionID,role:"user",time:{created},summary:{diffs:[]},agent,model:{providerID,modelID}}` — a
  **nested** `model` and **no** flat `providerID`/`modelID`. wopr emits the assistant shape for every role: flat
  `providerID`/`modelID` plus `cost/tokens/mode/path/parentID/finish`, and no `model`. The client reads
  `userMessage.model.providerID`, `model` is undefined, and it throws. The assistant message keeps flat
  `providerID`/`modelID` (opencode assistant info verified: `agent,cost,finish,id,mode,modelID,parentID,path,
  providerID,role,sessionID,time,tokens`).
- **Files:** `internal/codingagent/serve/serve_handlers.go` (`messageInfoDTO`), `internal/codingagent/serve/serve_sse.go`
  (the user `message.updated` info), `internal/codingagent/serve/serve_dto.go` if the info builder lives there.
- **Gates:** a test that a user-role message info equals the opencode shape — `model:{providerID,modelID}` present,
  no flat `providerID`/`modelID`, `summary` present — while the assistant info is unchanged; the serve suite;
  `make check`. Live: KKCode opens a session and prompts without throwing.
- **Notes:** the same info builder feeds both `GET /session/{id}/message` and the SSE `message.updated`, so branch on
  `role` once, in the one place both callers route through.

### serve-delete-session-archives — `DELETE /session/{id}` answers 501; archive on delete and hide archived from `/experimental/session`

- **Status:** DONE — shipped on `wave-serve-delete-session-archives`, merged into `main` (`--no-ff`). `DELETE /session/{id}`
  now archives: it stamps the existing `session_archive` entry with `time.Now().UnixMilli()` through a new single writer
  `writeArchive` (shared with `PATCH {time:{archived}}` — no second metadata format), then answers `200 application/json`
  body `true`; `404` for an unknown id; it never removes a transcript file. `GET /experimental/session` now calls
  `listSessionsFiltered(..., false)`, which skips sessions with `Archived != nil` **before** the `limit` cap, while
  `GET /session` stays inclusive (`listSessionsFiltered(..., true)`) — opencode parity. Gate `TestDeleteSessionArchives`
  asserts DELETE→200/`true`, `time.archived` present on `GET /session/{id}`, the id absent from `/experimental/session`
  but present in `/session`, and `404` for an unknown id; independently reproduced red pre-fix (501) / green post-fix,
  and mutation-checked (filter regression and a non-stamping DELETE both turn it red). Spec:
  `docs/plans/2026-10-08-serve-delete-session-archives.md` (planning artifact, not committed). Surfaced 2026-10-07 in the
  KKCode proxy log (two `DELETE /session/{id}` → `501`); owner decision 2026-10-08: **archive on delete** rather than
  hard-delete, so a mistaken delete is recoverable; KKCode has an archive.
- **Why:** opencode implements `DELETE /session/{id}` (`200 application/json true`) as a **hard delete**. wopr answers
  `501`, so the client's delete silently fails. wopr already has archiving: `PATCH /session/{id}` with
  `{time:{archived:<ms>}}` sets it (`serve_handlers.go` `patchSession`), `sessionRecord.Archived` reads it back
  (`serve_read.go`), and the session DTO emits `time.archived` (`serve_dto.go`). wopr has no host-level delete primitive
  and must not remove transcript files. Decision: `DELETE` archives (sets `time.archived`) instead of deleting — a safe,
  deliberate divergence from opencode. Separately, opencode's `GET /experimental/session` **excludes** archived sessions
  while `GET /session` includes them (verified live on 1.18.34: an archived session is present in `/session`, absent in
  `/experimental/session`). KKCode calls `/experimental/session`, so wopr must filter archived there or an
  archived/deleted session never leaves the client's list. wopr's `/experimental/session` is currently an alias of
  `/session` with no filter (`serve.go:69`).
- **Files:** `internal/codingagent/serve/serve.go` (`DELETE /session/{id}` route → new handler; the experimental route),
  `internal/codingagent/serve/serve_handlers.go` (reuse the `patchSession` archive write; `listSessions` gains an
  archived filter for the experimental route only).
- **Gates:** a test that `DELETE /session/{id}` returns 200 `true`, and the session is then absent from
  `GET /experimental/session` but still present in `GET /session` and `GET /session/{id}`; 404 for an unknown id;
  `make check`.
- **Notes:** reuse the existing archive write (no second metadata format). Keep `/session` inclusive, matching opencode.
  The DTO `time.archived` must carry the archive timestamp.

### serve-sse-turn-frame-parity — KKCode still double-renders and never idles: assistant part is created after its delta, and the message never completes

- **Status:** DONE — shipped on `wave-serve-sse-turn-frame-parity`, merged into `main` (`--no-ff`). The mapper now emits
  opencode's per-part frame order: a `message.part.updated` create (empty text, `time:{start}`) precedes each
  text/reasoning/tool delta, and the block's end emits the final `message.part.updated` with `time:{start,end}`; the final
  assistant `message.updated` carries `time:{created,completed}` (was `{created}` only), so the client sees the run finish
  instead of staying busy. `partDTO.Time` and `messageTime.Completed` are `omitempty` pointers set only on the stream, so
  the persisted transcript and POST/GET message JSON are byte-unchanged. Gate `TestSSETurnFrameParity` asserts the literal
  part id across create→delta→final in that order, `time:{start}` on create and `{start,end}` on final, and the final
  assistant `message.updated` with `time.completed` set (the placeholder has none); proven non-vacuous under four
  mutations (delta-first, dropped part time, dropped completion, drifted id). Spec:
  `docs/plans/2026-10-08-serve-sse-turn-frame-parity.md` (planning artifact, not committed). Out of scope by decision:
  `step-start`/`step-finish` (wopr has no snapshot hash or step boundary), `session.diff` (no diff source), and the
  placeholder `finish` key. Surfaced 2026-10-08 driving KKCode after `serve-sse-message-lifecycle-parity`: one prompt shows
  the reply twice and the client never returns to idle for the next command.
- **Why:** comparing a full turn's `/global/event` frames from opencode 1.18.34 (`/tmp/oc-turn2.txt`) and wopr
  (`/tmp/wopr-turn2.txt`; regenerate against a running server, do not commit), the per-turn frame lifecycle still
  diverges:
  - **Delta before part.** opencode emits `message.part.updated` for the assistant text part **created** first
    (empty `text`, `time:{start}`), then `message.part.delta`, then a final `message.part.updated` (`time:{start,end}`).
    wopr emits the **delta first** (`serve_sse.go`), before any part for that id exists, then the part. A client that
    materialises a part on the delta and a second on the update renders it twice.
  - **No part time.** opencode text/reasoning parts carry `time:{start,end}`; wopr's carry none.
  - **No message completion.** opencode's final assistant `message.updated` has `time:{created, completed}`; wopr's has
    `time:{created}` only, so the client never sees the assistant message finish — the likely reason it stays busy.
  - **No step parts.** opencode brackets the assistant run with `step-start` and `step-finish` parts (`step-finish`
    carries `cost`/`tokens`/`reason`/`snapshot`); wopr emits neither.
  - Minor: opencode's assistant placeholder has no `finish` key (wopr sends `"pending"`), and opencode emits
    `session.updated`/`session.diff` frames wopr omits (likely optional).
- **Files:** `internal/codingagent/serve/serve_sse.go` (the mapper and part construction), maybe `serve_dto.go`.
- **Gates:** a hermetic replay test asserting, for one assistant text part, the order
  `message.part.updated(create) → message.part.delta → message.part.updated(final)`, a stable part id across all
  three, `time:{start}` on create and `{start,end}` on final, and a final assistant `message.updated` whose
  `time.completed` is set. The serve suite; `make check`. Live: KKCode shows the reply once and returns to idle.
- **Notes:** match opencode's order/fields for the core message/part frames; `step-start`/`step-finish` and
  `session.updated`/`session.diff` are secondary — include only if cheap and honest. Do not change the persisted
  transcript shape.

### serve-sse-end-burst-duplicate-part — the assistant end burst re-emits already-streamed parts with no `time`, wiping the part's `end`

- **Status:** DONE — shipped 2026-10-08 on `wave-serve-sse-end-burst-duplicate-part`. `messageEnd` now skips a part the stream already published, so the burst no longer overwrites a streamed part's `{start,end}` with a timeless frame; a never-streamed part is still announced, created then stamped. `m.parts` records every published part (via `m.part`), so text, reasoning and tool parts are all suppressed. New `TestMessageEndDoesNotRepublishStreamedPart`; `TestStreamPartIDsMatchTranscriptPartIDs` reads streamed frames.
- **Why:** wopr's assistant `MessageEndEvent` emits the final `message.updated` (now with `time.completed`) and then
  **re-publishes every content part** (`internal/codingagent/serve/serve_sse.go` `messageEnd`, the loop over `blocks`).
  Every part is already emitted during streaming — text/reasoning via `createPart`/`finishPart` (with
  `time:{start,end}`), tools via `toolStart`/`toolSnapshot`/`ToolExecutionEndEvent`. opencode emits **no** such
  republish (`/tmp/oc-turn2.txt`). The re-published text part is rebuilt with no time, so the trailing
  `message.part.updated` carries `time:None` (`/tmp/wopr-turn3.txt`, the frame after the final assistant
  `message.updated`) and overwrites the part's `{start,end}` from the streaming frame — which can leave the client
  seeing an unterminated part and never settling. It is also an extra part frame opencode never sends, a plausible
  second copy in the double render.
- **Files:** `internal/codingagent/serve/serve_sse.go` (`messageEnd`).
- **Gates:** a hermetic replay test asserting that, after the final assistant `message.updated`, no
  `message.part.updated` is emitted for a part that was already streamed, and that a streamed part's `time.end` is
  never overwritten with a missing time; the serve suite; `make check`. Live: KKCode renders once and idles.
- **Notes:** keep the user-message announce branch unchanged. Only suppress the assistant part republish for parts the
  stream already sent; if a part genuinely never streamed, it may still be emitted (with `time` via `finishPart`).

### serve-sse-session-frames — wopr never emits `session.updated`/`session.diff`, and announces the user message once instead of opencode's twice

- **Status:** DONE (narrowed) — shipped on `wave-serve-sse-session-frames`, merged `--no-ff` into `main`; session
  frames only, because the item's narrow clause fired (the double-render was not explained by the user-message
  sequence). Surfaced 2026-10-08 driving KKCode: it never enters a running state (no Stop), so the user
  resends and wopr answers `409 busy`; each sent message also renders twice.
- **Why:** in one opencode 1.18.34 turn (`/tmp/oc-turn2.txt`) the frame census is `session.updated` ×5, `session.diff`
  ×2, `message.updated` ×6 (the user message updated at turn start with no `summary`, again mid-turn, and again at the
  end with `summary:{diffs:[]}`), plus `step-start`/`step-finish` parts. wopr's turn emits **no** `session.updated`,
  **no** `session.diff`, and the user `message.updated` once. KKCode is opencode-shaped: it likely derives the
  session's running/updated state and title/token metadata from `session.updated`, and reconciles the message it
  optimistically rendered from the user `message.updated` sequence — so their absence leaves it thinking the session is
  idle (no Stop button) and doubling the message. Abort itself is verified working (wopr stops the stream promptly;
  `POST /session/{id}/abort` → 200 `true`), so "can't stop" is the missing running state, not the abort handler.
- **Files:** `internal/codingagent/serve/serve_sse.go` (mapper), `internal/codingagent/serve/serve_handlers.go` /
  `serve_dto.go` (`buildSessionDTO` for the `session.updated` info).
- **Gates:** a hermetic replay test comparing wopr's turn against `/tmp/oc-turn2.txt`: `session.updated` emitted on
  session start and at turn end (info = the session DTO, `time.updated` moving), `session.diff` after the assistant
  run, and the user `message.updated` emitted twice (start without `summary`, end with `summary:{diffs:[]}`); the serve
  suite; `make check`. Live: KKCode shows the run state (Stop) and renders each message once.
- **Notes:** only emit frames wopr can produce honestly. If the double is not explained by the user-message sequence,
  the spec fan must say so and the item narrows to the session frames.
- **Landed (2026-10-08, narrowed):** the mapper now emits `session.updated` at turn start (before `session.status
  busy`) and at turn end (`AgentSettledEvent`, before `session.status idle`/`session.idle`), info = the real
  `buildSessionDTO(rec, live)` with `time.updated` moving; and `session.diff` `{sessionID,diff:[]}` at turn end. The
  terminal seam fires on every settle including abort. Hermetic test `TestSSETurnSessionFramesMatchOpencode` embeds
  the opencode census and never reads `/tmp`. No persisted-transcript change; no `coding` import into `serve`.
- **Double-render — UNESTABLISHED and deliberately not fixed.** The spec fan and the work fan both found the
  user-message sequence does **not** explain the double render: wopr emits one user `message.updated` and still
  doubles, while opencode emits three for one id and renders once — the multiplicity runs opposite the symptom, so
  it cannot be the cause; neither candidate alternative is proven. Per the item's clause the item narrowed and no
  cause was invented. The two-phase user parity and the double-render diagnosis are surfaced as the two items below.

### serve-sse-user-message-two-phase — emit the user `message.updated` in opencode's two-phase form (parity only)

- **Status:** DONE — shipped 2026-10-08 on `wave-serve-sse-user-message-two-phase`, merged `--no-ff` into `main`.
  Previously READY (parity, low priority), deferred out of `serve-sse-session-frames` by that item's narrow clause.
- **Why:** opencode emits the user message.updated at turn start with no `summary` and again (twice) at/after turn end
  with `summary:{diffs:[]}` (`/tmp/oc-turn2.txt:7,35,83`); wopr emitted it once, always with `summary`. This is frame
  parity only — it is **not** a double-render fix; the double-render cause is UNESTABLISHED (see
  `serve-sse-session-frames`). Do not reintroduce a fix claim.
- **Files:** `internal/codingagent/serve/serve_sse.go`, `serve_dto.go`.
- **Gates:** a hermetic test asserting the two-phase shape; the serve suite; `make check`. — all met.

**What landed, for the next session**

- `messageInfoDTO` gained an unexported `omitSummary` flag (`serve_dto.go`), read only by the user branch of
  `MarshalJSON`, where `Summary` is now `*messageSummary` with `omitempty`: nil omits the key, non-nil emits
  `{"diffs":[]}`. The zero value leaves every existing caller — above all the `GET /session/{id}/message` read path
  through `entriesToMessages`/`messageInfo` — emitting `summary` exactly as before. The persisted transcript shape is
  untouched.
- `eventMapper` gained `turnUserCreated`, recorded at the user `message_end`. `messageEnd`'s user branch now sets
  `omitSummary=true` — the turn-start frame, no `summary`. `translate`'s `AgentSettledEvent` case emits a second user
  `message.updated` for the same id and clock after `session.diff`, before `session.status idle`/`session.idle` — the
  `summary:{"diffs":[]}` settle frame. **Exactly two per turn**; an abort/error settle gets the second too
  (`emitAgentSettled` is reached unconditionally).
- Hermetic `TestSSEUserMessageTwoPhase` (never reads `/tmp`), plus updates to `TestSSETurnLifecycleParity` and
  `TestPromptMessageIDHonoredEndToEnd` — both previously pinned exactly one user frame — and the
  `assertUserInfoShape`/`assertUserSummaryEmpty` helpers (`assertUserSummaryEmpty` rejects `diffs:null`, not just a
  len-0 slice). Non-vacuity proven red on: the settle emission, the DTO no-summary flag, the shared clock, and the
  null-diffs rejection.
- **Deliberate divergence, recorded:** opencode's capture shows **three** user frames (start + two end); this item's
  DoD and slice pin **two** (start no-summary, settle with summary). The third is out of scope. The settle frame sits
  after `session.diff` and before the idle terminators rather than at opencode's post-`session.idle` position, so a
  reader that stops on `session.idle` sees it deterministically.
- **Latent, accepted:** the `m.turnUserID != ""` settle guard is sticky per session; with no Continue endpoint there
  is no live path where a settle re-emits the prior turn's id and clock. No machinery added for it.
- **No double-render claim.** Frame parity only; the KKCode double-render was resolved separately by
  `serve-prompt-message-id` (`b9db06d`).
- Six gates green in the worktree and re-run on merged `main`: `go build ./...`, `go vet ./...`,
  `go fix -diff ./...` empty, the serve suite, `golangci-lint` (0 issues), and `bash -lc 'make check'`.
- Spec of record: `docs/plans/2026-10-08-serve-sse-user-message-two-phase.md`. A 4-angle spec fan found one blocker
  (a sixth test, `TestPromptMessageIDHonoredEndToEnd`, also pinned one user frame) and one helper-seam ambiguity,
  both fixed before implementation; a 4-angle diff fan found one major (the `diffs:null` false green above), fixed
  before commit.

### kkcode-double-render-cause — diagnose the remaining KKCode message double-render

- **Status:** DONE — resolved 2026-10-08 by `serve-prompt-message-id` (merged `b9db06d`); closed as a tracker
  correction, no code of its own.
- **Why:** driving KKCode, each sent message rendered twice. Cause established from the request-body capture
  (`/tmp/wopr-proxy.log`): KKCode sends `"messageID":"msg_optimistic_…"` in the prompt body and renders that bubble
  optimistically, reconciling it against the SSE `message.updated` id. wopr ignored `messageID` and minted its own
  entry id, so the echo never matched and the client showed two. Fixed by honoring the client id end-to-end.
- **Files:** fixed in `internal/codingagent/serve/serve_prompt.go` (`resolveMessageID`).
- **Gates:** covered by `serve-prompt-message-id`'s tests (client id round-trips through SSE and
  `GET /session/{id}/message`).

### serve-prompt-async-queue-when-busy — a second `prompt_async` while running returns `409`; opencode returns `204`

- **Status:** DONE — shipped 2026-10-08 on `wave-serve-prompt-async-queue-when-busy`. A busy `prompt_async` now
  returns `204` and queues the prompt to run as the next user turn (FIFO, after the current run settles);
  `host.Session` gained a host-owned queue (`queueMu`/`queued`) whose accept-vs-release handoff is serialized under
  one mutex, so a queued prompt cannot be appended to a free slot and strand. `questionStore.hasPending` keeps the
  `409` while an `ask_user` question is pending (the `serve-5` DoD 11 invariant); `/message` keeps its `409`. Gate
  `TestPromptAsyncQueuesWhileTheSessionIsBusy` (both calls `204`; order userA/assistantA/userB/assistantB, natural
  `TUI_LIVE_STREAM_PAUSE` settle). Surfaced 2026-10-08 in the KKCode proxy log (`prompt_async` → `409` twice) and
  verified live: opencode's `POST /session/{id}/prompt_async` on a busy session returns `204` (accepts/queues),
  wopr returned `409` `application/json`.
- **Decision:** a host-scoped queue, not the agent follow-up queue: that queue is single-run-scoped, its final drain
  decision is unsynchronized with an enqueue (a tail enqueue strands), and it never drains on abort
  (`runPostAgentRunsWith` is `ctx.Err()==nil`-gated). The host queue stores `[]ai.UserContentBlock` verbatim.
  Known gap: serve's queued prompts are not surfaced in `PendingMessageCount`/`queue_update`.
- **Surfaced:** `serve-prompt-async-abort-handoff-race` (READY, edge case).
- **Why:** the client allows sending while running (it has no queue UI) and wopr's `409 busy` surfaces as an error.
  wopr already models queued input (`HasPendingMessages`/`PendingMessageCount`, and the failure table's "queued input
  after compaction"), but `promptAsync` calls `PromptContent`, which returns `ErrSessionBusy` instead of queueing.
- **Files:** `internal/codingagent/serve/serve_prompt.go` (`promptAsync`), `coding/host/host.go`
  (`PromptContent`/queue path).
- **Gates:** a test that two `prompt_async` calls in a row both return `204`, and the second run's user+assistant
  messages appear after the first settles (in order); the serve suite; `make check`. Live: KKCode sends while running
  without an error.
- **Notes:** match opencode's queued (not replaced, not dropped) semantics; confirm whether opencode steers the
  running turn or runs the queued message next before implementing.

### serve-prompt-message-id — wopr ignores the client's `messageID`, so KKCode's optimistic user message never reconciles and renders twice

- **Status:** DONE — shipped 2026-10-08 on `wave-serve-prompt-message-id`. Both prompt endpoints now accept
  `messageID`; when present and `^msg`-valid it becomes the user transcript entry id end-to-end (the SSE user
  `message.updated`/parts and `GET /session/{id}/message` all report it), so KKCode's optimistic bubble reconciles
  and renders once. Absent the field, wopr mints a 16-hex server id exactly as before. The id rides the prompt
  through the host queue, so a prompt accepted while busy keeps it. Gate `TestPromptMessageIDHonoredEndToEnd` (the
  named assertion: SSE id == client id, text part `msg_test1-text-0`, GET id == client id; a no-field prompt still
  mints and SSE == GET), plus `TestPromptMessageIDHonoredOnMessage`,
  `TestPromptMessageIDRejectsMalformedAndDuplicate`, `TestQueuedPromptMessageIDPreserved`; independently reproduced
  red pre-implementation / green post.
- **Decision (deliberate divergence):** malformed (`^msg` fails) → `400 {"error":"invalid messageID"}` matches
  opencode's schema (`z.isStartsWith("msg")`). A **duplicate** id → `400 {"error":"messageID already exists"}` is a
  wopr divergence: opencode accepts and upserts in its id-keyed store, but wopr's transcript is an append-only entry
  tree with no upsert, so accepting would write two entries under one id. Ratified by the orchestrator; recorded as
  divergence, not parity.
- **Surfaced:** `serve-prompt-message-id-duplicate-race` (READY) — the duplicate check runs at handler time, so a
  same-id resend arriving before the first prompt persists is accepted twice. Deferred with that follow-up.
- **Why:** opencode's `POST /session/{id}/prompt_async` (and `/message`) body accepts an optional `messageID`
  (`pattern ^msg`; see `/tmp/ocapi.json`). Verified live on opencode 1.18.34: a prompt sent with
  `messageID:"msg_…"` produces a user message whose id **is** that value, round-tripping through
  `GET /session/{id}/message`. KKCode is opencode-shaped: it renders the sent message optimistically with a
  client-generated id and reconciles it when the SSE `message.updated` carries the same id. wopr ignores the body's
  `messageID` and mints its own entry id, so the echo never matches the optimistic bubble and the client shows two.
- **Files (as built):** `internal/codingagent/serve/serve_prompt.go` (`promptBody.MessageID`, `resolveMessageID`,
  both handlers), `agent/agent.go` (`UserMessage.EntryID`, `SendContent`), `coding/session.go`,
  `coding/session_ops.go` (`EntryByID` delegate), `coding/host/host.go` (`queuedPrompt` carries the id),
  `coding/host/rpc.go` + `internal/codingagent/interactive_turn.go` (pass `""`),
  `internal/codingagent/session.go` (`AppendMessage` honors the user id, mirroring the assistant branch).
- **Gates:** a test that a prompt carrying `messageID:"msg_…"` yields a user message whose id equals it in both the
  SSE `message.updated` and `GET /session/{id}/message`, while a prompt without one still mints a server id; the serve
  suite; `make check`. Live: KKCode renders the sent message once.
- **Notes:** opencode's pattern is `^msg`; malformed → 400 schema parity, duplicate → 400 wopr divergence (see
  Decision). **Confirmed 2026-10-08**:
  KKCode's prompt body is `{"messageID":"msg_optimistic_muzwmw43_1","parts":[{"type":"text","text":"Hi"}],
  "agent":"explore","model":{"providerID":"opencode-go","modelID":"deepseek-v4.1-flash"}}` (captured in
  `/tmp/wopr-proxy.log`), so the client does send `messageID` and wopr drops it. The body also carries `agent` and
  `model`; honoring `agent` is a separate concern (log it if it diverges).

### serve-prompt-async-drops-model — `prompt_async` decodes the client's `model` and never applies it, so KKCode's model choice is ignored

- **Status:** DONE — shipped 2026-10-10 on `main`, direct on the owner's request rather than on a wave. `prompt_async`
  now honors the model the client names per prompt, the way `POST /session/{id}/message` always has. The reference is
  **resolved before the 204** (an unoffered model is `400 {"error":"model not found: …"}` rather than a silent
  accept-and-ignore) and **swapped once the run holds the slot**, so a prompt queued behind a running turn does not
  change that turn's model. `applyModel` split into `resolveModel` (pure) plus the swap; `host.Session.PromptContent`
  gained the `before func() error` that `PromptContentWait` already had, and `queuedPrompt` carries it, so the swap
  happens when the queued run starts. Gate `TestPromptAsyncAppliesTheClientModel`, independently reproduced red both
  ways.
- **Why:** `promptBody.Model` was read by `sessionMessage` and dropped by `promptAsync`, which called
  `PromptContent(ctx, content, msgID)` with no model. KKCode prompts over `prompt_async`, so every turn ran on the
  server's startup model while the phone showed the one it picked. Caught live in `/tmp/wopr-proxy.log`:
  `prompt_async … "model":{"providerID":"opencode-go","modelID":"space-bunny"}` → `204`, answered by deepseek-v4.1-flash.
- **Gate:** a `400` for an unoffered model; a queued `faux-2` prompt accepted while `faux-1` is still running leaves the
  session on `faux-1` at accept time (red if the swap moves to accept time) and lands on `faux-2` once that turn settles
  (red if the model handling is dropped at all).
- **Accepted boundary:** a model that becomes unresolvable between the 204 and the queued run starting abandons the turn
  silently — the response is long gone. Unreachable in practice: the reference is resolved from the same
  `AvailableModels` a moment earlier.
- **Still open:** `prompt_async` still drops the body's `agent` field, so KKCode's agent choice is not honored (see the
  note under `serve-prompt-message-id`).

### serve-missing-route-tests — eight routes lack behavioral tests

- **Status:** DONE — shipped 2026-10-11 on `wave-serve-missing-route-tests`. Five tests added to `internal/codingagent/serve/serve_test.go`, no import changes, no new helper: `TestProjectCurrentReturnsServedRootProject`, `TestProviderReturnsSortedAuthenticatedNames`, `TestFindFileReturnsWalkedFileNodes`, `TestFileStatusAnswersNonNilArray`, `TestStubbedSessionRoutesAnswer501`. Each was observed RED on its own route's `mux.HandleFunc` removal (404) and GREEN after; the stub test was mutated three times (`serve.go` fork/revert/diff lines). Gate: package tests (108 pass), `go build`/`go vet`, `golangci-lint` 0 issues; `make check` still cannot complete in this environment (tmux absent), `make lint` passes.
- **Permissions:** `POST /session/{id}/permissions/{permissionID}` already had behavioral coverage (`TestPermissionSubmitRoutesAnswer501OnEveryMatchingPath`, `serve_test.go:2121`), so no test was added — the item's "eight" is seven uncovered plus one covered.
- **Open question — resolved:** the four 501 stubs stay pinned at 501 today; the `501` literal is the one line to change when a stub is implemented (`TestStubbedSessionRoutesAnswer501` carries that comment; the permissions test's literal is at `serve_test.go:2131`). No `!= 404` shadow assertion — it is dead beside the `== 501` check.
- **Why:** `TestNewRegistersEveryRouteWithoutPanicking` (the only route-wide test) covers only `/permission` and `/question`. The following mounted routes have no behavioral test and would silently regress if removed from `serve.go`:
  - `GET /project/current`
  - `GET /provider`
  - `GET /find/file`
  - `GET /file/status`
  - `POST /session/{id}/fork` (501)
  - `POST /session/{id}/revert` (501)
  - `GET /session/{id}/diff` (501)
  - `POST /session/{id}/permissions/{permissionID}` (501)
- **Gate:** add a behavioral test per route (or a single parameterized test) asserting the route exists, returns the correct status, and the response shape matches the contract. Each must name the mutation (route removal) that turns it red.
- **Open question:** are the four 501s (`fork`, `revert`, `diff`, `permissions`) permanently stubbed, or will they become implemented? The test should assert the current 501 but stay green when implemented.

### serve-prune-learner-map — bound the learner model-outcome map

- **Status:** DONE — shipped 2026-10-11 (wave `wave-serve-prune-learner-map`).
- **Shipped:** TTL (default 90d) and max-entries (default 500) eviction on `Learner.models`, both
  overridable via `wopr serve --learn-max-models N --learn-ttl DAYS` → `coding.ServicesOptions`
  → `efficiency.LoadLearner` options. Eviction is least-recently-**updated** (keyed on a new
  per-entry `UpdatedAt`), which is the item's "no outcome update for N days" read; it is not
  read-order LRU. Tests: `TestLearnerCapEvictsOldest`, `TestLearnerTTLDropsStale`
  (`internal/codingagent/efficiency/`). Backward compatible: `learnedVersion` unchanged, legacy
  entries are stamped on load, no data loss.
- **Corrections to this item's text:** the path was `internal/codingagent/efficiency/learn.go`, not
  `internal/codingagent/learn.go`; `ServerOptions` does not exist, so the seam is
  `coding.ServicesOptions`; the gate is `./internal/codingagent/...` (the tests live in the
  `efficiency` subpackage).
- **Why:** `internal/codingagent/learn.go:175-189` (`Learner.models` map) accumulates every unique
  `provider/model` spec ever seen, with no TTL, max-entries, or decay. The router uses this history
  to score models, but stale entries never drop. Over months of a serve deployment encountering
  hundreds of models, the map grows linearly.
- **Fix:** add a bounded eviction policy — **both**:
  - **TTL**: drop entries with no outcome update for N days (configurable, default 90 days).
  - **Max entries**: cap the map at N entries (configurable, default 500); evict LRU when exceeded.
  Both configs exposed via `wopr serve --learn-max-models` / `--learn-ttl` and `ServerOptions`.
  Gate: unit test that inserts N+1 entries, asserts the oldest is dropped; `go test -count=1 ./internal/codingagent/`.
- **Open question:** should the router's score decay over time (exponential decay) so stale entries
  self-deprecate before hitting the cap? Not required for MVP; decay can be a follow-up.

### serve-limit-history-tokens — cap in-memory transcript per session

- **Status:** DONE — shipped on `wave-serve-limit-history-tokens` as `9655b10`, 2026-10-11. Filed
  2026-10-10. Audit HIGH #2: agent's in-memory message history grows until compaction, with no
  entry-count or byte cap between compactions. Subagent turns add to the same session. A 10k-turn
  session can hold 100–500 MiB in RAM.
- **What landed:** `coding.ServicesOptions.MaxHistoryTokens *int` (there is no `ServerOptions` type)
  plumbed from `wopr serve --max-history-tokens`: `nil` → 80% of the model's effective window,
  `0` → disabled, `N>0` → cap N. New `compaction.TrimMessages` keeps the leading system baseline and
  the newest messages that fit, starting the kept suffix at a cut point so no tool result is
  orphaned. Applied at the END of `prepareRequest` (`coding/session_transcript.go:142`), after the
  routing/learner accounting calls, so the `Context` handed to the provider is bounded. Gate
  `TestMaxHistoryTokensTrimsOldest` (`coding`) fails if the trim is removed.
- **Corrections to this item's text:** the path is `coding/session_transcript.go`, not `session.go`;
  `prepareNextPrompt` does not exist; the seam is `prepareRequest`, not `prepareNextTurn` (a
  `prepareNextTurn` return is overwritten by `prepareRequest` in the same loop iteration —
  `agent/agent_loop.go:144` then `:157`), and not `pruneRequestContext` (its result feeds
  `ensureRoute`/`efficiencyRecordRequest`/`learnRequest`, which must see the untrimmed projection).
- **Scope correction, recorded:** this bounds the **per-request context**, not the retained
  transcript. `inner.entries` (`internal/codingagent/session.go:335`) is append-only by invariant
  (`:325-328`) and is NOT capped; `agent.messages` is already compaction-bounded. The item's
  100–500 MiB RAM premise is therefore not fully addressed — see `serve-transcript-ram-cap`.
- **Open items on ship:** the 80% default (`MaxHistoryTokens == nil`) applies to **every**
  `NewServices` caller — serve, TUI, print, host — not serve-only. It never drops data (compaction
  still counts the untrimmed projection) but the owner should ratify the broader behavior. Gate note:
  `make check`'s `test-integration` tier needs `tmux`, absent here, so it did not run; `make test-race`
  does not race-test the `./coding` package itself.

### serve-transcript-ram-cap — actually bound the retained in-memory transcript

- **Status:** READY — surfaced 2026-10-11 by `serve-limit-history-tokens`. That item's stated
  motivation (a 10k-turn session holding 100–500 MiB in RAM) is not solved by capping the per-request
  context: `inner.entries` (`internal/codingagent/session.go:335`) grows for the life of a session and
  is append-only by design (`:325-328`). Bounding it needs a mechanism that preserves
  branches/undo/resume (e.g. spill cold entries to disk and page them back, or evict only from a
  settled point), not truncation. Not started; needs a design decision first.

### serve-prompt-system — per-prompt system prompt override

- **Status:** DONE — shipped on `wave-serve-prompt-system` as `2ce42a8`, 2026-10-11. Filed 2026-10-10.
  `PromptInput.system` is an optional string that adds to the session's system prompt for one turn. The
  hook point is `createUserMessage` in `prompt.ts` (which already reads `input.system`).
- **What landed:** both `POST /session/{id}/message` and `/prompt_async` accept `system`; the user message
  each produces persists it and returns it on `GET /session/{id}/message` and both SSE user frames; the
  provider request for that turn appends the text last (`agent/agent_loop.go:400`). Gate:
  `TestPromptSystemAppliesToTheTurn` (`internal/codingagent/serve`), which covers both endpoints, the
  persisted DTO round-trip, the appended provider request, a no-bleed second turn, and the two SSE frames.
- **Corrections to this item's text:** opencode does not *override* — it **appends** the per-prompt text
  as the last system element (`join("\n", [base, ...user.system])`, `session/llm/request.ts:56-66`), and
  the append is fixed for the whole turn (`lastUser`). wopr matches: `projectSystemPrompt` folds
  `base + "\n" + sys` into the head. The gate asserts the **provider request** that carried the text, not
  "the model's response reflects it" — a scripted provider has no prompt-dependent response.
- **Recorded product divergence (D1):** an empty `system: ""` is treated as absent (no append, key
  omitted), where opencode stores `Some("")` and echoes `"system":""`. Model behavior is identical —
  opencode appends only a truthy `user.system` (`request.ts:62`) — but wopr keeps `UserMessage.System` a
  plain `string` rather than introduce a `*string` solely to echo an empty override. Recorded here per
  `AGENTS.md`'s divergence rule.

### serve-prompt-no-reply — persist user message without an assistant turn

- **Status:** IN-PROGRESS — claimed 2026-10-11 on `wave-serve-prompt-no-reply`; filed 2026-10-10. `PromptInput.noReply` is an optional boolean. When true, the session
  appends the user message and returns immediately without running the agent loop. The gate: a prompt with
  `noReply:true` produces a user message in the transcript and no assistant message; the session model is
  unchanged. Gate: assert the transcript has +1 user entry, 0 assistant entries.

### serve-prompt-parts-agent-subtask — support `agent` and `subtask` part types in prompt parts

- **Status:** READY — filed 2026-10-10. `PromptInput.parts` accepts `agent` and `subtask` part types
  (in addition to `text` and `file`). The `agent` part spawns a subagent task; `subtask` is a nested
  sub-task. The gate: a prompt with an `agent` part spawns a subagent task that appears in the transcript;
  the parent turn continues after the subtask completes.

### serve-variant-wire-alias — emit real variant from models.dev reasoning options

- **Status:** READY — filed 2026-10-10. opencode derives variants from models.dev `reasoning_options`
  (`provider/transform.ts:1717`): one variant per effort value, or a thinking toggle, or a token budget.
  `variant: "high"` is the *same model at high reasoning effort*. wopr already has this as `ai.ThinkingLevel`
  (`coding/session.go:897`, clamped per model). Missing: the wire alias. `sessionModel.Variant` is a hardcoded
  `"default"` and `providerModelDTO.Variants` an empty map. The slice emits the real variant — the shapes
  sidecar's "captured `default`" no longer justifies it. Gate: a prompt with `variant: "high"` causes the
  run to use the high-effort reasoning level, and `GET /session/{id}` reports the variant.

### serve-prompt-tools-permissions — map deprecated `tools` to session permissions

- **Status:** READY — filed 2026-10-10. `tools` is a per-prompt `{toolName: bool}` that opencode converts
  into session permission rules (`prompt.ts:1060-1067`): `{permission: t, action: enabled ? "allow" :
  "deny", pattern: "*"}`, written to the session. wopr has no permission subsystem (three non-test files,
  all in serve, all stubs). This is a subsystem item, not a field item. Nothing sends `tools` today. Filed

### serve-prompt-format-json-schema — structured output via `format`

- **Status:** READY — filed 2026-10-10. `format` is an optional `SessionV1.Format` (JSON schema). Needs a
  structured-output tool; wopr's own system prompt already promises the capability. Filed as
  `serve-prompt-format-json-schema`.

### serve-auto-evict-idle-sessions — auto-archive and evict sessions idle > N hours



### serve-delete-hard-delete — `DELETE /session/{id}` archives; opencode removes the session and its children

- **Status:** READY — filed 2026-10-09. Supersedes the archive-on-delete owner decision of 2026-10-08
  (`serve-delete-session-archives`) and removes one of the three ratified divergences named in `AGENTS.md`, under the
  rule that opencode is the specification for `wopr serve`.
- **Why:** opencode's `DELETE /session/{id}` is a hard delete. `packages/opencode/src/session/session.ts:606-627`
  resolves the session first (unknown id errors), cancels the session's background jobs, **recursively removes every
  child session**, publishes `SessionV1.Event.Deleted`, removes the session's events, and **logs and swallows any
  error** — the route still answers `200` with a JSON `true`. wopr instead stamps `time.archived`
  (`writeArchive`, `internal/codingagent/serve/serve_handlers.go`) and never removes a transcript file, so a phone
  that deletes a session keeps seeing it. The 2026-10-08 decision was made because wopr had no host delete primitive.
  That is still true — there is no `Host.Remove`, and nothing in `coding/host/` removes a transcript — so this is new
  work, not a handler swap.
- **What opencode specifies, precisely** (`session.ts:606-627`, route at `routes/instance/httpapi/groups/session.ts`):
  1. resolve the session; unknown id is a failure before anything is touched;
  2. cancel background jobs for that session;
  3. **cascade** — `children(sessionID)`, then `remove(child.id)` per child, recursively;
  4. publish `Deleted` with the session info;
  5. remove the session's events;
  6. any thrown error is logged, not propagated — the route still returns `true`;
  7. response is `200 application/json` with body `true`, unchanged from what wopr already sends.
- **Files:** `coding/host/` — a new host delete primitive (cancel the run, drop the live session, remove the
  transcript and its sidecars); `internal/codingagent/serve/serve_handlers.go` — `deleteSession` routes to it and
  publishes the delete event; `internal/codingagent/serve/serve_sse.go` — a `session.deleted` event, which does not
  exist today (the vocabulary at `serve_sse.go:46-50` has `session.updated`, `session.diff`, `session.error`,
  `session.status`, `session.idle`, and no delete).
- **Open questions, for the implementer to settle before coding — not owner decisions:**
  - **Delete and archive both refuse while a run is in flight** — owner decision 2026-09. `409 {"error":"session is
    busy"}`, matching the convention already used by `prompt_async` and `/message` (`serve_prompt.go:77,343`). The busy
    signal is `s.host.Sessions()` → `Inner().IsIdle()`, the same one `sessionStatus` reads
    (`serve_handlers.go:552`). This **removes** the cancel-and-drain machinery: a delete can no longer race a turn
    writing to a transcript it is about to unlink, which was the worst half-delete source. opencode cancels background
    jobs instead; refusing is a deliberate divergence, and a safer one — nothing is destroyed while work is in flight.
  - **Archive needs no decision.** `PATCH /session/{id}` with `time.archived` is a separate opencode feature
    (`Session.setArchived`) and keeps working untouched; only the shared running-session refusal above is new. What
    the old item got wrong was implying the two might fold together. They do not.
  - **Half-deleted trees: make the state unreachable by ordering, not by storing trash.** With the busy refusal in
    place, three sources remain — (i) the recursive cascade unlinks the parent and a child's `os.Remove` returns
    `EACCES`/`EBUSY` (open handle, permissions, NFS); (ii) the process dies between two unlinks; (iii) `ENOSPC`/`EIO`
    part-way down the tree. The symptom is concrete: **`serve_read.go` lists sessions by walking session files under
    `~/.wopr/agent/sessions/<mangled-cwd>/` and never follows `parentID`**, so an orphaned child does not fail the
    reader — it reappears in `GET /session` as an ordinary session whose `parentID` points at nothing. A silent ghost.
    opencode tolerates this by logging and swallowing. **wopr orders instead: read the whole subtree from disk first
    (reading it as you walk loses the siblings once the first is gone), then unlink children-first, parent-last,
    stopping at the first error.** The invariant that buys is *a session file exists ⟹ its parent exists, or it was a
    root*, because a parent is only unlinked once every child is gone. A failed unlink or a crash then leaves the
    **parent in place** — the subtree stays discoverable and the delete is retryable, which is the same guarantee as
    any other failed filesystem call. No trash directory: it would need its own lifecycle (what prunes it, what lists
    it, how it is recovered) to solve a state that ordering removes. Gate on the ordering, not on the error path.
- **Gates:** a behavioral test that deleting an idle session removes it from `GET /session` and
  `GET /experimental/session` and that its transcript file is gone from disk; that a session with a child removes the
  child; that **deleting a session with a run in flight is `409 {"error":"session is busy"}` and deletes nothing**;
  that deleting an unknown id is `404`; and that **a child whose unlink fails leaves its parent listed and intact**
  — the ordering assertion, not an error-code assertion. Every gate must name the mutation that turns it red. Plus
  `go build ./...`, `go vet ./...`, `go test -count=1 ./coding/host/ ./internal/codingagent/serve/`,
  `golangci-lint` on both, `go fix -diff ./...` empty, `bash -lc 'make check'`.
- **Risks:** this is the first wopr code path that destroys user data. A regression loses transcripts. That is the
  "lose data" bar `AGENTS.md` sets for tests — it means this slice gets the fuller gate set. Two things keep it
  recoverable, and both are already specified above: nothing is destroyed under a live run, and children-first ordering
  means every failure leaves the parent listed and the delete retryable.
- **Deferred in the same area:** `POST /session/{id}/share` and `DELETE /session/{id}/share` are unmounted in wopr and
  filed under the absent-endpoint inventory. opencode's `Session.share` is a link-generating operation with its own
  lifecycle; it is not implied by this item.

### serve-prompt-async-abort-handoff-race — a late abort can cancel the just-handed-off queued prompt

- **Status:** DONE — shipped 2026-10-08 on `wave-serve-prompt-async-abort-handoff-race`. A late abort no longer cancels
  the queued run. `releaseSlot` now hands a queued run the slot with a bare `context.WithCancel(context.Background())`
  that is **not** registered in `s.cancel`, so an abort delivered in the handoff window (A settled, B handed off, B not
  yet in `beginAgentRun`) does not find B's cancel. `Abort` now discriminates: a non-nil host cancel (a reserved run
  outstanding) → `cancel()` and return, never reaching the session-wide abort; otherwise it returns unless
  `s.sess.IsStreaming()`, so with no begun run it cancels nothing, and only a genuinely active handed-off run is
  cancelled via `RequestAbort`. `s.cancel` therefore aborts only the reserved run; a handed-off run is abortable only
  from `beginAgentRun` onward. A test-only `handoffBarrier func()` field (nil in production) fires inside `releaseSlot`
  between context creation and the goroutine spawn so the test executes `Abort()` synchronously in the window.
- **Why:** `releaseSlot` installed the next run's cancel (`ownRun`) and spawned it; an `abort` delivered after run A
  settles but before the queued run B reaches `beginAgentRun` read B's fresh cancel and cancelled B, whose turn was
  then lost although it was already answered `204`. Pinned semantics (verified live on opencode 1.18.34): abort cancels
  only the currently-active run; the queue is not cleared and a queued-but-not-yet-started run keeps running.
- **Files:** `coding/host/host.go` (`releaseSlot`, `Abort`, `Session.cancel`/`ownRun` comments, `handoffBarrier` seam);
  `coding/host/host_test.go` (`TestAbortInHandoffWindowDoesNotCancelQueuedPrompt`).
- **Gates:** `TestAbortInHandoffWindowDoesNotCancelQueuedPrompt` queues B behind a blocking A, fires `Abort()` in the
  handoff window via the seam, and asserts B reaches the provider and its user+assistant messages appear; independently
  reproduced red by reverting only `releaseSlot`'s registration line (`waitArrived` times out) / green post-fix, stable
  under `-race`. Spec fan (5 angles: 2 blockers fixed in Rev 2) + focused re-fan (2 angles) + diff fan (3 angles:
  test-integrity and scope CLEAN; the correctness angle's nil-cancel residual was closed by the `IsStreaming` guard and
  re-verified CLEAN). Gates: `go build`, `go vet`, `go fix -diff` empty, host `-race` 5 passed, host+serve 106 passed,
  `golangci-lint ./coding/host/` 0 issues, `make check` all green.
- **Accepted residual (out of scope):** an abort issued while a handed-off run is active can still land on a successor
  if that run settles and the next queued run begins within the `RequestAbort` call; closing it needs a generation-scoped
  coding abort, and abort-while-active semantics are already covered by serve's `TestAbortStopsARunningPrompt`.

### serve-prompt-message-id-duplicate-race — a same-`messageID` resend before the first prompt persists is accepted twice

- **Status:** DONE — shipped 2026-10-08 on `wave-serve-prompt-message-id-duplicate-race`.
- **Why:** `resolveMessageID` (`internal/codingagent/serve/serve_prompt.go`) rejects a duplicate client id by
  checking the persisted entry map (`codinghost.Session.Inner().EntryByID`). A prompt accepted while the session is
  busy is only queued (`host.Session.PromptContent`), and even a running prompt's user message is absent from `byID`
  until the `OnMessagePersist` hook runs on the run goroutine — not at accept time. So two requests carrying the same
  id that arrive during that window both pass the check and both `204`, then both `AppendMessage` under one id
  (`internal/codingagent/session.go` has no collision check), producing two entries with the same id. Client ids are
  per-bubble unique (`msg_optimistic_*`), so this needs a resend racing itself; the item's gate does not cover it.
- **Files:** `internal/codingagent/serve/serve_prompt.go` (`resolveMessageID`), `coding/host/host.go`
  (`PromptContent`/`PromptContentWait` — the atomic seam if a host-side in-flight-id set is chosen).
- **Gates:** a test that sends the same `messageID` twice across the busy window and pins the chosen semantics
  (reject the second, or make it idempotent); the serve and host suites; `make check`.
- **Landed:** Rejection is now atomic. `host.Session` gained `idsMu` + `inFlightIDs`; `reserveEntryID` checks the
  persisted `EntryByID` map and the in-flight set in one critical section and claims the id, so two same-id requests
  cannot both pass before the first persists. `PromptContent` reserves before the queue/run decision (the id survives
  the busy window); `PromptContentWait` reserves before `run.TryLock` and releases via one `defer`; `runAndRelease`
  releases after `SendContentWithPreflight` returns (persistence done). serve keeps its persisted `EntryByID`
  pre-check as a fast path; `writePromptError` maps the new `ErrDuplicateMessageID` to `400 {"error":"messageID
  already exists"}`. `idsMu` is a leaf lock (no path takes `run`/`queueMu` while holding it), avoiding the
  `queueMu→run`/`run→queueMu` inversion.
- **Gate:** `TestPromptMessageIDDuplicateRejectedWhileBusy` (serve) holds A via `TUI_LIVE_STREAM_PAUSE`, queues B with
  `messageID:"msg_race"`, then sends C with the same id before B persists → C is `400` and the transcript holds exactly
  one `msg_race` user entry (B's text). Independently reproduced red by neutralizing `reserveEntryID` (C 204s) then
  green; `-race` clean; all gates green.
- **Observed (opencode divergence, recorded):** probed opencode 1.18.34 live at `http://100.109.237.16:4096` — it
  does NOT refuse a duplicate `messageID`: both `/prompt_async` calls return `204` and the second request's parts are
  merged into the one id-keyed user message (upsert), with the run not repeated; the same holds on the synchronous
  `/message`. wopr keeps its ratified **reject** semantics (append-only transcript, no upsert); matching opencode's
  merge is out of scope.
- **Ratified behavior changes:** a same-id `/message` while its own run is active now answers `400` (duplicate) rather
  than `409` (busy) — a duplicate id is the more specific error; a busy `/message` with a fresh id briefly claims then
  releases the id, so two concurrent same-id busy requests may answer `400` instead of `409` (both rejections of a run
  that never happened). No existing test pinned either case. The host `reserveEntryID` `EntryByID` branch is untested
  (serve's pre-check masks it); the in-flight branch is covered by the gate.
