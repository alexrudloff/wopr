# wopr agent guide

wopr is a terminal coding agent written in Go. It is its own product: there is
no upstream to match. Change behavior when it makes wopr better.

## What wopr optimizes for

1. **Route every prompt to the right model.** A classifier scores each task and
   the router picks a model across local hardware, subscriptions, and API
   providers under the active mode (auto, speed, quality, cost, uncensored)
   (`internal/codingagent/router`, `coding/session_router.go`, `docs/routing.md`).
2. **Carry fewer tokens per request.** The efficiency mechanisms in
   `internal/codingagent/efficiency` (Action Fusion, ObservationPack,
   Evidence-Preserving Reducer, Online Context Compact) are the reference for
   how context is spent (`docs/efficiency.md`).
3. **Write less code.** The minimal-code discipline in wopr's system prompt
   (`internal/codingagent/prompts`) applies to wopr's own code too: reuse what exists,
   prefer the standard library, add no abstraction or dependency that was not
   needed, and never cut validation, error handling, or security.

## Layout

| Path | Responsibility |
|---|---|
| `agent/` | Agent loop, messages, events, tool execution. |
| `ai/` | Providers, model catalog, authentication, streaming. |
| `coding/` | Session core, resource discovery. |
| `tui/` | Terminal components, rendering, input. |
| `cmd/wopr/` | The CLI and RPC surface. |
| `internal/` | Private implementation, including the router, efficiency mechanisms, and system prompt. |
| `docs/` | Documentation and the static site sources. |

Configuration lives under `~/.wopr` (`WOPR_HOME`); project configuration under
`.wopr/`. Environment variables use the `WOPR_` prefix.

## Working rules

- Fix the root cause. When a function is wrong, fix it once where every caller
  routes through.
- Do not add tests by default. Add one small behavioral test only when a
  regression would cost money, lose data, or weaken safety, or when fixing a real
  bug likely to recur. Never write snapshot/golden, change-detector,
  label/format, or exhaustive table tests.
- A change is done when `go build ./...`, `go vet ./...`, the tests of every
  package you touched, and `go tool golangci-lint run` on those packages pass.
  Run `make check` before committing.
- Tests must be hermetic: no real credentials, no network, no dependence on the
  developer's `~/.wopr`. Test setup clears provider credential variables
  (`ai.ProviderCredentialEnvVars`) and points `WOPR_HOME` at a temp directory.
- Credentials go only to the host they belong to. A provider without a
  configured base URL must fail, never fall back to another provider's endpoint.
- Production comments state current behavior and non-obvious invariants
  (protocol, concurrency, security). Do not narrate history or plans in code.
- Do not hard-wrap Markdown prose or comments to a terminal width.
- New features are named as wopr features, not after the project an idea came
  from. Copied or ported third-party code gets a line in `NOTICE`.

## Go style

- Build with Go 1.27.1; the module declares Go 1.27 as its language floor.
- Prefer modern standard-library APIs (`slices`, `maps`, `cmp`, `strings.Cut`,
  `for i := range n`, `min`, `max`) and keep `go fix -diff ./...` empty.
- Minimal, consumer-owned interfaces; concrete validated data; bounded
  resources; explicit ownership of errors and lifetimes.
- Performance claims need a benchmark with allocations. No blocking work on the
  TUI input and render loop.

## Commands

```bash
go build -o bin/wopr ./cmd/wopr   # build
make install                      # install ~/.local/bin/wopr
make lint                         # golangci-lint over the repository
make test                         # grouped Go tests (needs Go, git, tmux)
make check                        # the pre-commit gate
```

`wopr -p "<prompt>" </dev/null` runs one prompt non-interactively; close stdin
in scripts, or print mode waits for piped input.

## Licensing

Keep `LICENSE` and `NOTICE` intact. Copied or ported third-party code gets a line in `NOTICE`.
