# Automation

Every script WOPR runs outside of Go code lives here. The root `Makefile` is the
command interface: run `make help` for the everyday targets and
Call a script directly only when
no target covers what you need, and add a target when you find yourself doing
that twice.

| Folder | Holds | Run from |
|---|---|---|
| `dev/` | machine setup and local developer tools | `make setup`, `make doctor`, by hand |
| `ci/` | gates and test runners that CI also calls | `make check`, `make test`, workflows |
| `gen/` | generators and vendoring | `make knowledge-graph` |
| `release/` | release signing and module-tag checks | release and security workflows |
| `make/` | make fragments the root `Makefile` includes (CI shards) | `make ci-*` |

Shell scripts find the repository root from their own location. The Go tools
(`go run ./automation/...`) find it by walking up to the `go.mod` of
`github.com/alexrudloff/wopr` (`internal/repo`), so both work from any
directory inside the checkout.

## Sandboxes, devcontainers, and hosted agents

The automation writes only where the environment allows. Set these when the defaults are not writable or not reachable:

| Variable | Default | Used for |
|---|---|---|
| `TMPDIR` | `/tmp` | every temporary file and directory, and the default `RESULTS` path. Scripts pass an explicit `mktemp` template because macOS ignores `TMPDIR` without one. |
| `XDG_CACHE_HOME` | `~/.cache` | `WOPR_DEV_HOME` (the pinned toolchain and `env.mk`) |
| `WOPR_DEV_HOME` | `$XDG_CACHE_HOME/wopr-dev` | toolchains and machine settings from `make setup` |
| `SSL_CERT_FILE` | system trust | Go TLS behind an intercepting proxy or in a macOS sandbox; `make doctor` prints this fix when Go cannot verify the module proxy |


## dev

| Script | Purpose |
|---|---|
| `setup.sh` | Installs the Go toolchain pinned by `go.mod`, checks that the module proxy is reachable (without one, builds use the module cache), and builds `bin/wopr`. `--check` reports without changing anything, including a leaked `GOROOT`/`GOBIN` and Go TLS failures. Behind `make setup` and `make doctor`. |
| `toolchains.lock` | Pinned toolchain archives and SHA-256 digests for `setup.sh`. |
| `login-copilot.sh` | Interactive helper for signing wopr in to GitHub Copilot. |
| `stage-clipboard-png.sh` | Puts a PNG on the clipboard for manual paste tests. |

## ci

| Script | Purpose |
|---|---|
| `test-grouped.sh` | The `make test` scheduler: runs Go packages in groups sized for the machine. |
| `test-race.sh` | Race and goroutine-leak gate for the TUI concurrency model. |
| `integration-tests.sh` | The tmux integration tier, split by category. |
| `hygiene/`, `check-scratch-paths.sh` | Rejects private coordination paths (lane reports, `.env` files, handoff folders) and private infrastructure or operator paths in tracked files; `-ref REF` scans the committed tree at `REF` instead, for publication review. |

## gen

| Script | Purpose |
|---|---|
| `knowledgegraph/` | Renders `docs/knowledge-graph/wopr.graph.json` into its published forms (both docs pages, JSON-LD, Mermaid) and copies the install troubleshooting page into the agent docs bundle. `-check` fails on stale outputs; its test runs that check in `go test ./...`. |

## release

| Script | Purpose |
|---|---|
| `signkey/` | Go tool for the release Ed25519 key: `gen` a key pair, `sign` and `verify` `SHA256SUMS`. See `docs/project/RELEASING.md`. |
| `module-tags.sh` | Fails a release tag whose `go.mod` would break `go install ...@vVERSION`. |


## Evals and profiling

The eval harness is not here: it is Go, in `internal/evals` with its command line in `cmd/wopr-eval`, and its tasks, budgets, and results live in `evals/` with their own README. `make evals`, `make evals-live`, `make perf-check`, `make profile`, and `make pgo` drive it.
