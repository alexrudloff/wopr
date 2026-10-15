# session-directory Implementation Plan

## Orchestrator brief

- **Goal:** let a `wopr` or `wopr serve` process work in a directory chosen at
  launch (`--dir`), instead of being pinned to `os.Getwd()`; gate a served root
  by an operator-configured allowlist; keep the API's `directory` query a
  *validation*, not a per-request selector.
- **Files touched:**
  - `cmd/wopr/args.go` — `CLIFlags.Dir`; add `--dir` to `stringFlags`
    (`args.go:155`); help text (`args.go:299`, the `wopr serve` line).
  - `cmd/wopr/help.txt` — a `--dir <path>` row in the embedded Options table
    (lines 13–54). The previous revision only reached the `woprCommands` block
    (`args.go:294-303`), so `wopr --help` still hid the flag.
  - `cmd/wopr/main.go` — resolve the directory once, thread it as `cwd`.
  - `cmd/wopr/serve.go` — `--dir` parse, the five usage/help strings, the
    `resolveServeRoot`/allowlist seam.
  - `internal/codingagent/settings.go` — `ServerSettings.AllowedRoots` +
    `GetServerAllowedRoots() ([]string, error)`.
  - `internal/codingagent/serve/serve_handlers.go` — `serveListFiles` gains the
    `directoryParam` check (slice 2).
  - Audited `internal/codingagent` `os.Getwd()` sites that mean "the session
    directory" (slice 1, step 3): `interactive_commands.go:116`,
    `undo_command.go:38`, `slash_bug.go:155`, `slash_session_handlers.go:240`,
    `session_export.go:88`.
  - Docs: `docs/site/docs/cli.md`, `docs/site/docs/server.md`,
    `docs/site/docs/settings.md`, `docs/threat-model.md`, `CHANGELOG.md`.
  - Tests: `cmd/wopr/serve_test.go` (new) and additions under `cmd/wopr/` for the
    CLI/startup cases; `internal/codingagent/settings_test.go` for the getter.
- **Gates (each slice):** `go build ./...`, `go vet ./...`, touched-package tests
  under `-count=1`, `go tool golangci-lint run`, and `make check` (tmux is present
  only in a login shell; run `bash -lc 'make check'` or its constituents).
  Slice 1 adds `go test ./cmd/wopr/ -count=1` and
  `go test ./internal/codingagent/ -count=1`. Slice 2 adds
  `go test ./internal/codingagent/serve/ -count=1`. Docs slice adds
  `go test ./tests/docs-drift/ -count=1`.
- **Done when:** `wopr --dir X` runs a session whose cwd is X (project settings
  discovery, session store, and tools all follow X); `wopr serve --dir X` serves
  X and refuses a root outside a non-empty `server.allowedRoots`;
  `/file?directory=<foreign>` answers 400; docs updated; every gate green.
- **Out of scope:** more than one served root per process; per-request directory
  selection; any `Services` refactor; `os.Chdir`; snapshot/`revert`; auth/TLS.
- **Resolved questions:** see *Resolved questions* at the end (Q1–Q3 are assumed
  defaults the owner may override).

## Decisions (locked 2026-10-06)

1. Directory selection is available from **both** the CLI and `wopr serve`.
2. An operator-configured **allowlist of roots** (`server.allowedRoots`) gates
   which directory `wopr serve` may serve. It constrains the *launcher* — a
   wrapper, a service, or a non-owner caller — against misconfiguration. It is
   **not** a network boundary: under a launch-time root no client can point the
   server anywhere, and the server's actual file confinement is `resolveInRoot` /
   `os.OpenInRoot`, with the tool routes already unconfined
   (`docs/threat-model.md:43`). It is read from the **global** settings layer only
   (see slice 1, step 4), so a served project cannot widen its own guardrail.
3. **One root per process**: a process serves the directory it was started with;
   a second directory is a second process. No `Services` refactor.
4. The root is chosen at **launch** (`--dir X`; default the invocation cwd); the
   API's `directory` query stays a validation against the process root (400 on
   mismatch), so `serve-file-list-directory-param` resolves to the sibling 400.

## Design

- **One seam, no `os.Chdir`.** `coding.Services.cwd` is already the canonical
  session directory: tools are built from it (`coding/session.go:301`,
  `tools.CreateAllTools(svcs.CWD(), …)`), the bash tool runs in it
  (`tools/bash.go:28,61` → `bash_operations.go:115` `cmd.Dir = cwd`), the session
  store is anchored to it (`coding/session.go:440`), and settings discovery uses
  it (`NewSettingsManager(cwd, …)`). So the change is to *choose* that value from
  a flag rather than from `os.Getwd()`. Leaving the process cwd untouched avoids
  silently reinterpreting relative paths the user typed on the command line.
- **`--dir <path>`** on both parsers (`cmd/wopr/args.go` for the main CLI, the
  custom parser in `cmd/wopr/serve.go`). No short form — `-C` is dropped. A
  relative value resolves against the invocation cwd (`filepath.Abs`), then
  `filepath.EvalSymlinks`, then must be an existing directory; a bad value is a
  startup error, not a fallback (Q2 default).
- **Allowlist semantics (`server.allowedRoots`).** Each entry is expanded with
  `normalizeSettingsPath` (`settings.go:1204`, returns `(string, error)`), made
  absolute (`filepath.Abs`), then `filepath.EvalSymlinks`'d. The serve root is put
  through the same pipeline. Containment is a **per-path-component** check:
  `rel, err := filepath.Rel(entry, root)` and accept iff `err == nil` and
  (`rel == "."` or (`rel != ".."` and
  `!strings.HasPrefix(rel, ".."+string(os.PathSeparator))`)). Never a raw
  string-prefix match. An entry is invalid if `normalizeSettingsPath` errors, or
  if after expansion it does not resolve to an existing directory; an invalid
  entry fails closed at startup with an error naming it (an unresolvable `~`
  yields the literal path, which then fails `EvalSymlinks`). The getter expands and
  reports the `file://` error; `resolveServeRoot` does the abs/symlink/existing-dir
  and containment checks. Empty list = unrestricted, today's behaviour (Q1).
- **`directoryParam` citation.** The guard is `serve_handlers.go:66-77`; it keeps
  returning 400 on a mismatch, and slice 2 applies it to `/file` too.

## Slice 1 — `--dir` for the CLI and `wopr serve`, plus the allowlist

**Deliverable:** `wopr --dir X …` starts with X as the session cwd; `wopr serve
--dir X` serves X and refuses a root outside a non-empty `server.allowedRoots`.

The first draft's CLI and serve slices are **merged**: the serve slice called a
`resolveWorkingDir` helper that only the CLI slice added, so it could not compile
or commit alone. One slice makes both independently committable.

1. `cmd/wopr/args.go`: add `Dir string` to `CLIFlags`; add `"--dir"` to
   `stringFlags` (`args.go:155`); update the `wopr serve` line in `woprCommands`
   (`args.go:299`) to advertise `[--dir PATH]`.
2. `cmd/wopr/help.txt`: add a `--dir <path>` row to the embedded Options table
   (lines 13–54) — e.g. `--dir <path>  Working directory for the session
   (default: current directory)` — so `wopr --help` lists it.
3. `cmd/wopr/main.go`: add `resolveWorkingDir(flagValue string) (string, error)`
   (empty value → the invocation cwd, resolved with `filepath.Abs`; non-empty →
   resolve against the invocation cwd, `filepath.EvalSymlinks`, require an
   existing dir). Replace `cwd := initialCWD` (`main.go:350`) with
   `cwd, err := resolveWorkingDir(flags.Dir)` and route the `initialCWD` uses that
   mean "the project" through the resolved directory: `main.go:371`, `:392`,
   `:432`. `--dir` is resolved at `main.go:350`, *after* the `--version` early
   exit (`main.go:285`) and the `--export` early exit (`main.go:304`):
   `wopr --dir /nonexistent --version` prints the version and exits 0, and
   `--export` is unaffected; a bad `--dir` fails only when a session starts. Delete
   the now-false comment "Working directory comes from os.Getwd() (no CLI
   override)" (`main.go:348-349`).
   **Do NOT replace the `initialCWD` bases that resolve relative CLI paths against
   the invocation cwd (Q2):** `resolveCLIResourceFlags(flags, initialCWD)`
   (`main.go:437`, resolves `--skill`/`--prompt-template`/`--theme`) and
   `prepareInitialMessage(initialCWD, …)` (`main.go:693`, resolves `@file`) keep
   `initialCWD`. `collectStartupThemePaths(initialCWD, …)` (`main.go:365`) is also
   intentionally unchanged: it calls `opencodeThemePaths(cwd, false)` and returns
   before reading `cwd` (`configured_resources.go:35`), so the argument is inert.
4. **Audit the `os.Getwd()` sites (required, not an open question).**
   Classification rule: a site that means "the session/project directory" routes
   through the resolved cwd / the session's `CWD()`; a site that means "the
   process/UI context" stays. Sites: `interactive_commands.go:116` (tui file
   provider — session dir), `undo_command.go:38` (relative undo paths — session
   dir), `slash_bug.go:155` (bug-report output — decide), `slash_session_handlers.go:240`
   (trust command — session dir), `session_export.go:88` (export path — decide),
   `interactive.go:749` (empty-CWD fallback — leave), plus `cmd/wopr/auth_commands.go:87`,
   `cmd/wopr/status_command.go:95`, `cmd/wopr/config_command.go:39`,
   `coding/services.go:87` (NewServices fallback — leave),
   `automation/internal/repo/repo.go:20` (automation process context — leave).
   Each change must be justified per site; when in doubt, leave it.
5. `cmd/wopr/serve.go`: parse `--dir` into the local `stringFlags` map
   (`serve.go:35`); add `--dir PATH` to all five usage/help strings (`serve.go:26`,
   `:45`, `:55`, `:59`, `:68`); add
   `func resolveServeRoot(dirFlag string, allowedRoots []string) (string, error)`
   that resolves `--dir` via `resolveWorkingDir`, and when `allowedRoots` is
   non-empty requires the resolved root to be contained in an entry (the
   per-path-component `filepath.Rel` check in Design). An entry that does not
   resolve to an existing directory, or a root outside every entry, is an error
   naming the entry. Read the list from the **global layer only**:
   `globalSettings.GetGlobalSettings().GetServerAllowedRoots()`. The manager built at
   `serve.go:80` is constructed with `projectTrusted=false` (already global-only);
   the merged manager is `settings` at `serve.go:88`. `GetGlobalSettings()` states
   the intent and keeps a trusted served project from ever widening this guardrail
   if that manager is later given project settings. Replace
   `cwd, err := os.Getwd()` (`serve.go:75`) with the resolved root.
   `runServeCommand` blocks in `srv.Serve` (`serve.go:144`), so `resolveServeRoot`
   is the testable seam for the root/allowlist logic. Update the non-loopback
   warning (`serve.go:100`) if it should name the served root.
6. `internal/codingagent/settings.go`: add an `AllowedRoots []string` field with
   the `allowedRoots,omitempty` JSON tag to `ServerSettings` (`settings.go:171`)
   and `func (s Settings) GetServerAllowedRoots() ([]string, error)` (expand `~`
   / `file://` per entry via `normalizeSettingsPath`, returning an error on the
   first invalid URL — mirroring `GetShellPath` `settings.go:464` / `GetSessionDir`
   `settings.go:1124`).

**Gates:** `go test ./cmd/wopr/ -count=1` and
`go test ./internal/codingagent/ -count=1`, with:

- `TestResolveWorkingDir` — **seam:** the new `resolveWorkingDir` helper. It is the
  real resolver, so the test drives `filepath.Abs`/`EvalSymlinks`/the existing-dir
  check against real temp dirs: abs, missing → error, not-a-dir → error, symlink
  resolved.
- `TestDirFlagIsSessionCWD` — **seam: subprocess.** `main()` cannot be called
  in-process (it exits through `exitProcess`, `profiling.go:16`), so reuse
  `woprBinary(t)` (`build_binary_test.go:26`) and the `--mode rpc` harness
   (`startRPCProcessAt`, `rpc_mode_test.go:55`, which already prepends `--mode rpc`).
   Run the built binary with `cmd.Dir` set to a *different* directory and
   `--dir X --model test-faux/faux-1` under `WOPR_TEST_FAUX=1`; send
  `{"type":"bash","command":"pwd"}` and assert the tool cwd is X, and (with
  persistence on) that the `sessions/--…--/` store is anchored under X. This
  reaches `main` → `coding.NewServices` → the tool builder → the session store and
  asserts an observed side effect, not a value the test itself constructed.
- `TestResumeStoredCWDSupersedesDirFlag` — **seam: the same subprocess harness.**
  Run `wopr --dir X --session <stored>` where `<stored>` records cwd Y (the
  existing `writeStartupSession` fixture, `startup_session_test.go:62`), then
  `pwd` → Y. This drives `resolveStartupSessionSelection` on the real startup path
  and locks Q3.
- `TestResolveServeRoot` — **seam:** the new `resolveServeRoot` helper in
  `cmd/wopr`. Table: root inside an entry; root outside every entry → error; empty
  list → unrestricted; symlinked entry; an entry that is not a directory → error.
  The root/allowlist logic lives in `cmd/wopr`, not
  `internal/codingagent/serve` — `serve.New`
  (`internal/codingagent/serve/serve.go:35`) takes no cwd or allowlist — so its
  test lives in `cmd/wopr/serve_test.go`.
- `TestGetServerAllowedRoots` — expansion, and the invalid-URL error path.

The existing foreign-directory 400 test (`internal/codingagent/serve/serve_test.go:252`) stays green. `go
build`, `go vet`, lint.

**Commit alone:** yes.

## Slice 2 — `/file` honours the directory validation

**Deliverable:** `GET /file?directory=<foreign>` → 400, matching its four
siblings. This is tracker item `serve-file-list-directory-param`.

1. `internal/codingagent/serve/serve_handlers.go`: at the top of `serveListFiles`
   (`:476` — signature; the doc comment starts `:472`) add the `directoryParam`
   guard already used by `serveFileContent` (`:589`) and `listSessions` (`:97`).
2. `docs/site/docs/server.md:64`: change the `/file` row's "Query `path` only" to
   list `directory`.

**Gates:** `go test ./internal/codingagent/serve/ -count=1` with
`TestListFilesAnswers400ForAForeignDirectory` (mirror of
`TestPromptAsyncAnswers400ForAForeignDirectory`, `serve_test.go:252`), plus
docs-drift. `go build`, `go vet`, lint.

**Commit alone:** yes.

## Slice 3 — Docs and threat model

**Deliverable:** the flag, the setting, and the guardrail are documented where the
person binding the port reads them.

1. `docs/site/docs/cli.md`: a `--dir` row for `wopr` and for `wopr serve`.
2. `docs/site/docs/settings.md`: `server.allowedRoots`, its empty-list meaning,
   that it is read from the **global** settings layer only, and the narrow
   rationale — a launcher guardrail against misconfiguration, not a network
   boundary (no client can point a launch-time root anywhere).
3. `docs/site/docs/server.md`: the served root is the launch directory; the
   `directory` query validates against it; repeat the one-line allowlist
   rationale.
4. `docs/threat-model.md`: **one clause** in the remote-serve boundary naming the
   launch-time root. Do not reuse "allowlist" here for `server.allowedRoots` — that
   word already names the resolved-path confinement (`docs/threat-model.md:43`,
   `resolveInRoot`/`os.OpenInRoot`). The rationale for the setting belongs in
   `settings.md`/`server.md`.
5. `CHANGELOG.md`.

**Gates:** `go test ./tests/docs-drift/ -count=1` and `make check`.

**Commit alone:** yes (trailing — documents slices 1–2).

## Resolved questions (defaults assumed; owner may override)

1. **Empty `server.allowedRoots`** → *unrestricted* (backwards compatible). Sane
   default; the list is an opt-in tightening.
2. **Relative CLI paths** (`@file`, `--skill`, `--prompt-template`, `--theme`) →
   resolve against the *invocation* cwd, not `--dir`; slice 1, step 3 leaves those
   bases at `initialCWD`. **Assumed.** The alternative — resolve inside `--dir`
   (`cd X && wopr …`) — is the one product fork to flag.
3. **`--dir` with `--resume`** → the resumed session's stored cwd wins
   (`main.go:435` sets `cwd = startupSession.runtimeCWD`); `--dir` seeds discovery
   and fresh sessions only. **Assumed**, and locked by
   `TestResumeStoredCWDSupersedesDirFlag`.
