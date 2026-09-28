# wopr

**SHALL WE PLAY A GAME?**

WOPR, the War Operation Plan Response: an opinionated coding agent with built-in
model routing, for the terminal. One Go binary.

## What it does

- **Routes every prompt, when you want it to.** Off by default: you pick the
  model. Turn routing on in `/setup`: **Basic** picks a model from your order
  and each model's window, speed, and cost with fixed rules; **Jev** has a
  TypeSafe Jev endpoint score each task (Basic takes over whenever Jev can't
  answer). Either way the router picks across your local models,
  subscriptions, and API providers. The mode sets what it optimizes: **auto** (balanced),
  **speed**, **quality**, **cost**, or **uncensored**. Pick a model to pin it
  as the orchestrator; subagents keep routing. See
  [docs/routing.md](docs/routing.md).
- **Delegates to subagents.** The orchestrator hands read-only investigations
  to routed subagents, in the foreground or the background, verifies their
  quotes against the files, and keeps a work queue that survives resume.
  `/goal` keeps working until an independent auditor agrees it's done.
- **Cuts context.** Large tool results leave the context but stay recallable,
  old output is pruned when the cache is cold anyway, noisy command output is
  compacted, and compaction happens when it pays for itself. See
  [docs/efficiency.md](docs/efficiency.md).
- **Writes less code.** An always-on minimal-code discipline (reuse, stdlib,
  platform, one line, then the minimum). `/review`, `/audit`, and `/debt`
  report bugs, over-engineering, and `simplify:` debt.
- **Fullscreen terminal UI.** Sidebar with context, routing, live agents,
  usage, speed per model, and the queue; command palette on `ctrl+p`; themes
  compatible with opencode theme files. MCP servers, web fetch and search,
  and hashline anchored edits for small local models.

## Install

```bash
# macOS / Linux: latest release to ~/.local/bin, SHA-256 verified
curl -fsSL https://raw.githubusercontent.com/alexrudloff/wopr/main/install.sh | sh

# or with Go
go install github.com/alexrudloff/wopr/cmd/wopr@latest

# or from a checkout
make install          # builds and installs ~/.local/bin/wopr
```

Archives for macOS, Linux, and Windows are on
[GitHub Releases](https://github.com/alexrudloff/wopr/releases). `wopr update`
installs the latest release after verifying its signed `SHA256SUMS`.

```bash
wopr                  # start in the current directory
wopr -p "explain main.go" </dev/null   # one-shot
```

## Configure

Everything lives under `~/.wopr/agent/` (override with `WOPR_HOME`):

| File | Purpose |
|---|---|
| `models.json` | Your own endpoints (local and LAN servers) |
| `router.json` | Your models in order; the Jev endpoint that turns routing on; tiers, capabilities, and policy |
| `router-speed.json` | Learned per-model speeds (written by wopr) |
| `efficiency.json` | Which efficiency mechanisms are on |

Log in to subscriptions from inside wopr with `/login openai-codex` and
`/login anthropic`. `/router status` shows every model, its capability, its
learned speed, and why the last prompt went where it did.

## License

MIT. See [LICENSE](LICENSE); third-party notices are in [NOTICE](NOTICE).

## Acknowledgments

wopr started from [PiG](https://github.com/MichaelKinsy/PiG), a Go port of the
[Pi](https://github.com/earendil-works/pi) coding agent. Its efficiency
mechanisms come from NVIDIA's [SoL-Pi](https://github.com/NVlabs/SoL-Pi), and its
minimal-code discipline from [Ponytail](https://ponytail.dev/). The terminal
interface is heavily inspired by [opencode](https://opencode.ai). Thank you.
