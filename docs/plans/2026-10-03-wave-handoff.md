# Wave handoff — `wopr serve` (opencode iOS client)

Hand this entire file to a fresh session as its opening prompt. Every fact below was
established by reading code or running gates; none of it is guesswork. Do not re-derive
it, and do not "simplify" the environment section — each item has already cost a
session a wrong turn.

## What you are doing

Execute the plan at `/home/kc/dev/wopr/docs/plans/2026-10-03-serve-opencode-ios.md`
using the **`wave` skill, from Step 0**. Load it and follow its eight steps in order.

The goal: a `wopr serve` command that runs a wopr session on a machine and lets the
opencode iOS app drive it over the LAN or a VPN — chat with live streaming, tool
cards, files, diffs, todos, and the agent asking questions. The plan is the spec of
record; it was rewritten after a four-dispatch verification fan returned 15 blockers
on an earlier draft whose central architectural claim was falsified.

Six slices, in the plan's order. Slice 1 is `coding/host`: a multi-session supervisor
and typed event bus with an injectable sink, plus the `internal/codingagent/serve`
listener and `GET /global/health`.

## Environment — read this before running anything

**Non-login shells have a sanitized PATH on this machine.** `go`, `make`, and `tmux`
will appear to be missing. Wrap every toolchain command:

```bash
bash -lc 'cd /home/kc/dev/wopr && make check'
```

This is not a workaround; it is how the commands must be invoked here.

- Go 1.27.1, installed at `~/sdk/go1.27.1`, symlinked to `~/.local/bin/go`, which
  `~/.bashrc:122` already puts on PATH. No shell config was edited. Checksum was
  verified against go.dev's published SHA256 before extracting.
- `go.mod` declares `go 1.27.0` / `toolchain go1.27.1`. `GOTOOLCHAIN=auto` with no
  override, so no silent toolchain substitution.
- tmux is 3.4 at `~/bin/tmux`. It **lacks `extended-keys-format`** (needs 3.5+), so
  four integration tests run an explicit degraded fallback and some TUI coverage is
  weaker than the test names imply. They pass. This is pre-existing, not yours.
- `make test` does not pass `-count=1`, so a fast green may be a cache replay. For
  evidence you intend to rely on, add `-count=1` yourself.

## Prerequisite: commit the groundwork FIRST

The primary checkout at `/home/kc/dev/wopr` has **uncommitted** changes that every
wave slice depends on. `git worktree add` branches from `HEAD`, so a worktree will
**not** contain them, and `make check` will fail there on `go-fix-clean` — every
slice blocked, for a reason that has nothing to do with your work.

Commit these on `main` before Step 0, in two commits so the gate fix and the docs stay
separately revertable:

**Commit 1 — restore the gate.** `.gitignore` plus the three `go fix` files:

| File | Change |
|---|---|
| `.gitignore` | `/.tokensave/` — ignores an untracked 90 MB local tool database |
| `cmd/wopr/print_mode.go` | reverse index loop → `slices.Backward` |
| `coding/session_safetybackup.go` | 3× `strings.Split` → `strings.SplitSeq` |
| `tui/stack_test.go` | composite-literal embedded-field elision |

**Commit 2 — the plan.** `docs/plans/2026-10-03-serve-opencode-ios.md` (the spec of
record) and `docs/plans/2026-10-03-wave-handoff.md` (this file).

The three code changes in commit 1 are `go fix ./...` modernizers, reviewed for
semantic equivalence. `make check` was red before them — nobody could run it, because
Go was not installed on this machine. Confirm the tree is green before branching:

```bash
bash -lc 'cd /home/kc/dev/wopr && make check'   # must exit 0
```

## There is no backlog

The wave skill's Step 1 needs a tracker and wopr has none — no `BACKLOG.md`, no
`ROADMAP*.md`, no `docs/backlog/`. Establish one first, using the **`backlog` skill**
so the convention is the project's rather than invented: one item per plan slice,
each with a status marker, the plan section it comes from, and its gates. Step 1's
claim step needs it to exist.

## Model constraint: tier 1 and tier 2 only

Per `~/.config/opencode/MODELS.md`. Four personas are in range and already
registered — use these:

| Persona | Model | Role |
|---|---|---|
| `architect` | `opencode-go/space-bunny-free` (tier 1) | drafts the slice spec (Step 2) |
| `verifier` | `opencode-go/space-bunny-free` (tier 1) | the whole verification fan (Steps 3 and 6) |
| `implementer` | `opencode/nemotron-3-ultra-free` (tier 2) | slice implementation (Step 4) |
| `qa` | `opencode/nemotron-3-ultra-free` (tier 2) | end-to-end and real-app verification |

**Two exclusions.** `mechanic` is pinned to `opencode/nemotron-3.5-lightning-free`,
which is tier 4 — do not dispatch it; route bounded edits to `implementer` instead.
`explore` and `general` declare no model in `~/.config/opencode/agents/`, so there is
no evidence they are tier 1 or 2; leave them alone, because an unverifiable model does
not satisfy a model constraint.

**Fan diversity comes from the angle, not the model.** `verifier`'s persona carries the
angle list — `runtime-seam`, `spec-fidelity`, `completeness`, `scope-fit`,
`convention`, `leanness`, `test-oracle` — and takes exactly one per dispatch, named in
the prompt. Dispatch it three or four times in a single message, one angle each, and do
not create persona variants: an agent file written mid-session does not register, and
the angle belongs in the prompt where it is per-dispatch rather than per-project.

Two of the angles earn their place here specifically. `test-oracle` is what catches a
gate exiting 0 without running the suite — the failure this repository already
produced, since `make check` had been silently unrunnable for its entire life because
no Go toolchain was installed. `runtime-seam` is what caught the earlier draft's
falsified central claim, when four dispatches found 15 blockers in a plan that had
already been through two rewrites.

`verifier` runs on the same weights as `architect`. Convergence between a `verifier`
finding and an `architect` judgement is therefore weaker evidence than convergence
between a `verifier` finding and an `implementer` report. Say which personas actually
ran in the fan report rather than implying a diversity you did not have.

## Traps the verification fan already found

These are verified. Do not rediscover them, and do not build the plan's superseded
ideas.

- **Do not modify `loadSessionFile` retention.** `raw` is write-critical:
  `SessionEntry.MarshalJSON` returns it verbatim and the full-file flush reads every
  `raw` under a held mutex. The offset-index idea was cut for this reason. The tail
  read lives entirely in `internal/codingagent/serve`.
- **`internal/codingagent/serve` must not import `coding`.** `coding/services.go:18`
  already imports `internal/codingagent`; that would cycle. The supervisor lives in
  `coding/host`, which can import its parent package.
- **Multi-session is already supported at the `coding` layer.**
  `coding.StartSession(svcs, opts)` (`coding/runtime.go:66`) is a free function over
  a shared `*Services`, and `Services` holds no session. "One session per process" is
  an artifact of how `cmd/wopr` drives RPC mode, not a limitation to engineer around.
- **The 16 MiB command cap the old draft said to "reuse" never existed.**
  `readJSONLLines` is unbounded `bufio.ReadBytes`. The 16 MiB `http.MaxBytesReader` in
  the plan is new work, justified from a 12 MP photo versus wopr's 17 MiB pinned
  session entry (`session_large_line_test.go:32`).
- **`runGen` is the TUI's field** (`internal/codingagent/interactive.go:97`) and there
  are two different `queueMu`s in two packages. The per-session run rule belongs to the
  new host, keyed on the session it owns.
- **The client's todo and question features are real.** `TodoListPanel` and a
  completion badge at `ToolPartView.swift:103` read todos; `askuser.Asker` exists and
  RPC deliberately omits it. Both are slice 5 work.
- **`/revert` and `/fork` return 501.** The client treats 501 as not-implemented, so
  that is an honest answer. Snapshot-based revert is a separate plan.

## Client contract source

The plan's 26-path / 29-operation table and 17 SSE events were derived from the app
source, not from opencode's docs. The clone is under `/tmp` and may be gone:

```bash
git clone --depth 1 https://github.com/grapeot/opencode_ios_client /tmp/opencode/iosclient
```

The endpoint diff is a ten-minute grep of `OpenCodeClient/Services/APIClient.swift`
and `SSEClient.swift`. If the phone runs a different client build — `OpenCode Mobile
App`, `OpenCody`, `anhtuan997/opencode-ios-client` all exist — re-run that diff first.
It is the difference between working and not.

## Gates

`make check` is the pre-commit gate and is currently green. Per-slice, from the plan:
`go build ./...`, `go vet ./...`, `go tool golangci-lint run` on touched packages,
`go test -count=1` on touched packages, and `go fix -diff ./...` empty — that last one
is an `AGENTS.md` rule and it is what the groundwork commit fixed.

Slice 2's acceptance criterion is special: re-pointing `--mode rpc` at `coding/host`
must leave RPC's wire protocol and existing tests **unchanged**. A test you had to
edit there means the protocol moved, which is the exact failure that slice exists to
prevent.

## Working rules

- `AGENTS.md` governs: hermetic tests, no real credentials, no network, minimal code,
  comments that state current behavior, and `go fix -diff ./...` empty.
- No auth, no TLS, no replay. Path allowlist on resolved paths is mandatory before the
  file endpoints ship — it is what makes auth-less LAN serving defensible, and
  `docs/threat-model.md` must say plainly that a served session is a remote shell.
- Commit only green slices. Write the resume point after each one; wopr has no resume
  file convention yet, so use the `resume-point` skill to establish one and record
  where it lives.

## Report back

Per slice: what changed with `file:line`, the verbatim gate output, and any deviation
from the plan with its reason. If you must deviate from the plan's architecture, stop
and report rather than improvising — the last architectural claim in this plan was
falsified by verification, which is why it now specifies the seam explicitly.
