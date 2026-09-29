# wopr

**SHALL WE PLAY A GAME?**

WOPR, the War Operation Plan Response: an opinionated coding agent with built-in
model routing, for the terminal. One Go binary.

## Why wopr

wopr is for people with a mix of models: a couple running locally, and
subscriptions to more than one cloud provider. It is a lightweight agent loop
like Pi's with a fullscreen interface like opencode's, NVIDIA's token-efficiency
research built in, and no plugin architecture. If you want it to work
differently, change the source.

- **Model routing, built in.** Connect your local models, subscriptions, and
  API keys, put them in order, and wopr picks the model for each prompt and
  each subagent. **Basic** routing uses fixed rules: your order, each model's
  window, measured speed, and cost. **Jev** routing has a TypeSafe Jev endpoint
  score every prompt, with Basic taking over whenever Jev can't answer. Modes
  set what to optimize: **auto**, **speed**, **quality**, **cost** (local and
  subscriptions first, never pay-per-token), **uncensored**, or **private**
  (only connections you mark private, for everything, with no fallback). Off
  by default; turn it on in `/setup`.
- **Global Thermonuclear War.** `ctrl+x g` puts your top model at maximum
  thinking and turns on the war council: every other model you set up answers
  each prompt in parallel at its own maximum thinking, and the top model
  builds from the best of their proposals. Slow and expensive on purpose.
- **Token efficiency, on by default.** The mechanisms below cut what each
  request carries, and wopr tunes them per model from how each of your models
  actually behaves.
- **Quota aware.** With more than one subscription, wopr balances work between
  similarly ranked models on different plans before either runs out.
- **Context sized per model.** Compaction fits each model's window, including
  the window you set yourself, and wopr compacts to fit a smaller routed model
  when that model is the better pick.
- **Subagents that are checked.** Read-only investigations go to routed
  subagents, in the foreground or background. wopr verifies every line they
  quote against the files and escalates once when an answer can't be trusted.
  `/goal` keeps working until an independent auditor agrees it's done.
- **Writes less code.** An always-on minimal-code discipline (reuse, stdlib,
  platform, one line, then the minimum). `/review`, `/audit`, and `/debt`
  report bugs, over-engineering, and `simplify:` debt.
- **One Go binary.** Fullscreen terminal UI with a sidebar for context,
  routing, live agents, subscription usage, and speed per model; command
  palette on `ctrl+p`; opencode-compatible themes; MCP servers; web fetch and
  search with no setup (your search key, else the model provider's own
  search, else DuckDuckGo). Signed releases and `/upgrade`.

## Efficiency

All on by default. Turn any of them off in `efficiency.json`; details in
[docs/efficiency.md](docs/efficiency.md).

| Mechanism | What it does |
|---|---|
| Action fusion | `edit` and `write` can run a command (tests, build) in the same step, saving a round trip |
| Observation pack | Tool results over 10 KB leave the context after two sends; `obs_recall` pages them back |
| Evidence reducer | Long build and test logs are summarized by your cheapest model; a summary is kept only if every quote in it is exact |
| Online compaction | Compacts at plan-step boundaries when that costs less than carrying the context forward |
| Tool-output half-life | On small-window models, older tool results shrink to excerpts |
| Stall nudge | When the model repeats itself or stops making progress, it is told to change approach |
| Test-rerun cap | A test run that already passed isn't repeated until a file changes |
| apply_patch | GPT and Codex models get the patch format they were trained on |
| Quota balance | Spreads work across subscriptions before one gets tight |
| Per-model learning | Tunes the cutoffs above for each of your models from real sessions |

## Compared with Pi and opencode

[Pi](https://github.com/earendil-works/pi) is a deliberately minimal agent you
extend yourself. [opencode](https://opencode.ai) is a full-featured agent with
a polished terminal UI. wopr takes opencode's interface, keeps Pi's small
core, and adds the parts neither ships out of the box:

| | Pi | opencode | wopr |
|---|---|---|---|
| Picks a model per prompt across local, subscription, and API providers | no | no | yes |
| Token efficiency beyond compaction, on by default | no (SoL-Pi is an opt-in extension) | pruning of old tool output | ten mechanisms, tuned per model |
| Subscription quota balancing | no | no | yes |
| Subagents with quote verification | via extensions | subagents, unverified | yes |
| Compacts to fit a smaller routed model | no | no | yes |
| Fullscreen TUI | no (inline) | yes | yes |
| Runtime | TypeScript (Node) | TypeScript (Bun) | one Go binary |

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
[GitHub Releases](https://github.com/alexrudloff/wopr/releases). `/upgrade`
(or `wopr update`) installs the latest release after verifying its signed
`SHA256SUMS`.

```bash
wopr                  # start in the current directory
wopr -p "explain main.go" </dev/null   # one-shot
```

## Configure

Everything lives under `~/.wopr/agent/` (override with `WOPR_HOME`):

| File | Purpose |
|---|---|
| `models.json` | Your own endpoints (local and LAN servers) |
| `router.json` | Your models in order, routing (off, Basic, or Jev), the Jev endpoint, and policy |
| `router-speed.json` | Learned per-model speeds (written by wopr) |
| `efficiency.json` | Turns efficiency mechanisms off (all on by default) |
| `efficiency-learned.json` | Per-model efficiency values learned from your sessions (written by wopr; delete to reset) |

Log in to subscriptions from inside wopr with `/login openai-codex` and
`/login anthropic`. `/router status` shows every model, its learned speed, the efficiency mechanisms, and why the last prompt went where it did.

## License

MIT. See [LICENSE](LICENSE); third-party notices are in [NOTICE](NOTICE).

## Acknowledgments

wopr started from [PiG](https://github.com/MichaelKinsy/PiG), a Go port of the
[Pi](https://github.com/earendil-works/pi) coding agent. Its efficiency
mechanisms come from NVIDIA's [SoL-Pi](https://github.com/NVlabs/SoL-Pi), and its
minimal-code discipline from [Ponytail](https://ponytail.dev/). The terminal
interface is heavily inspired by [opencode](https://opencode.ai). Thanks :)
