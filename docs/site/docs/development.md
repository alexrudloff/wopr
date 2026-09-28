# Development

WOPR is a terminal coding agent written in Go. Read `AGENTS.md` before changing behavior: it states the project's priorities (spend the least that still gets the job right, carry fewer tokens per request, write less code) and its working rules.

## Requirements

If WOPR is already available, use the repository Skill for guided setup:

```bash
wopr --skill ./.agents/skills/setup-wopr
```

The Skill checks only the tools required for the selected task. It asks before
installing software.

The qualified verification toolchain is:

- Go 1.27.1;
- macOS 13 or later for native macOS builds;
- Git;
- tmux 3.7 on Unix.

The development and CI tooling is Go and shell; it needs no Python. Python 3 is needed only for `make evals-live`: the sample repositories of the live eval tasks are Python, so their checks run `python3`. Unit tests that exercise Python files skip when `python3` is not on `PATH`.

Use the versions pinned by the repository (`go.mod`).

## Build

```bash
go build -o bin/wopr ./cmd/wopr
./bin/wopr --version
```

Do not use `go install ./cmd/wopr` in a development checkout. Keep test and candidate binaries under the repository `bin/` directory or another explicit temporary path.

## Primary checks

Run the pre-commit gate before every commit:

```bash
make check
```

`make check` builds every package, runs `go vet`, golangci-lint, the grouped unit tests, the race and goroutine-leak gate, the tmux integration tier, the `go fix` cleanliness check, and the documentation drift gate. Focused targets for the inner loop:

```bash
make dev             # build, vet, and the fast test group
make build
make vet
make lint
make lint-changed    # lint changed packages against LINT_BASE
make test
make test-integration
make docs-drift
```

Run `make help` for the full list, including the eval, benchmark, and profiling targets.

## Change workflow

For a behavior change:

1. fix the root cause once, where every caller routes through;
2. run the tests of every package you touched;
3. update the user docs in the same change (see below);
4. run `make check`.

Do not add tests by default. Add one small behavioral test only when a regression would cost money, lose data, or weaken safety, or when you fix a real bug that is likely to recur. Do not write snapshot or golden tests, change-detector tests, tests of labels or formatting, or exhaustive table tests.

Tests are hermetic: no real credentials, no network, and no dependence on the developer's `~/.wopr`. Do not weaken a test or skip a gate to make a defect green.

Every prompt and tool result costs tokens. When a change adds text that reaches the model (system prompt, tool descriptions, tool output), keep it as short as it can be and still be correct; see `docs/efficiency.md`.

## User documentation

User documentation is authored as Markdown under `docs/site/docs/` and read on GitHub; pages link each other with relative `.md` links. `docs/site/docs/docs.json` classifies and orders every page, and `make docs-drift` checks it. The installer is `install.sh` at the repository root, and releases are cut as described in `docs/project/RELEASING.md`.

The same pages ship in the executable: `docs/site/docs/embed.go` embeds them, and `internal/woprdocs` writes them to `~/.wopr/docs` for `wopr docs` and the coding agent. Keep every page reachable by links from `index.md`; the offline copy has no site navigation.

See the repository `AGENTS.md` and `docs/project/RELEASING.md`.
