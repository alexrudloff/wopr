# Quickstart

WOPR is under active development. Install it on macOS or Linux with `curl -fsSL https://raw.githubusercontent.com/alexrudloff/wopr/main/install.sh | sh`, on any supported platform with `go install github.com/alexrudloff/wopr/cmd/wopr@latest`, or download an archive from [GitHub Releases](https://github.com/alexrudloff/wopr/releases). Windows support is a preview. To build from source instead, follow the steps below.

## Requirements

Build the `wopr` executable with Go 1.27.1. Go 1.27 release binaries for
macOS require macOS 13 or later.

The complete verification suite also uses:

- Git;
- tmux on Unix.

You do not need every verification tool to run a previously built executable.

## Build from source

Clone the repository and build WOPR:

```bash
git clone https://github.com/alexrudloff/wopr.git
cd wopr
go build -o bin/wopr ./cmd/wopr
./bin/wopr --version
```

## Install a release

On macOS or Linux, the installer downloads the latest release from
[GitHub Releases](https://github.com/alexrudloff/wopr/releases), verifies its
SHA-256 against the release's `SHA256SUMS`, and installs `wopr` to
`~/.local/bin`:

```bash
curl -fsSL https://raw.githubusercontent.com/alexrudloff/wopr/main/install.sh | sh
```

Set `WOPR_VERSION=0.1.0` to install a specific release and `WOPR_INSTALL_DIR`
to install elsewhere. On Windows, download the `.zip` archive from the release
page. `/upgrade` in WOPR (or `wopr update` in a shell) replaces a release
binary with the latest release after it verifies the release's Ed25519-signed
`SHA256SUMS` and the archive's SHA-256. WOPR tells you at startup when a newer
release is out.

## Install with Go

Go 1.27 or newer installs the command without cloning the repository:

```bash
go install github.com/alexrudloff/wopr/cmd/wopr@latest
```

Use an exact version for a reproducible installation:

```bash
go install github.com/alexrudloff/wopr/cmd/wopr@v0.1.0
```

Go writes the `wopr` executable to `GOBIN`. When `GOBIN` is empty, Go uses the
`bin` directory below the first path in `GOPATH`: `$(go env GOPATH)/bin`, which
is `~/go/bin` by default (`%USERPROFILE%\go\bin` on Windows). With an older Go
1.21 or later and the default `GOTOOLCHAIN=auto`, Go downloads a new enough
toolchain automatically. Add that directory to `PATH`, then inspect the
installed module identity:

```bash
wopr version
go version -m "$(command -v wopr)"
```

Update a Go installation by running `go install` again with a newer exact
version or with `@latest`. `wopr update` also works: it replaces the executable
with the signed release binary.

Each release tags the root module (for example `v0.1.0`).

## Start WOPR

Run WOPR in the project you want it to inspect:

```bash
cd /path/to/project
/path/to/wopr/bin/wopr
```

WOPR stores user state below `~/.wopr`. Use `WOPR_HOME` only when you need an isolated configuration root.

## First run

The first time WOPR starts with nothing set up (no `router.json` and no default
model, even if an API key is in the environment), it greets you; Connect opens
setup's menus. Connect a subscription (ChatGPT, Claude, Copilot,
and the other logins `/login` offers), an API key (checked with a request that
costs nothing, then stored in `auth.json`), or an OpenAI-compatible endpoint
(llama.cpp, vLLM, LiteLLM, Ollama); its model list opens next, to check the
models to use. Done goes to the home screen, on your first model. Pick
another with `/model`, which lists the models you set up, grouped by
connection; Tab on an empty prompt cycles them. `/model <name>` and
`--model` can still pick any available model without adding it.

Run `/setup` from the home screen, or `wopr setup`, any time. Its main screen
lists your **connections** (each endpoint, API key, and subscription) with
their models, the ways to connect another, **Model routing** (off, on ·
Basic, or on · Jev), and **War council**, the models that propose in Global
Thermonuclear War (see [war council](routing.md#war-council)), and **Web
search**, where a Brave or Tavily key or a SearXNG instance can be added (see
[web tools](web.md); search works without one).

- **Adding a model**: a connection's "Models from …" list is the provider's
  own current list (Anthropic's, Google's, or the model list of an
  OpenAI-compatible API or endpoint), so new models appear as soon as the
  provider offers them; when a provider can't be asked, setup says so and shows
  the models wopr knows. Enter or space checks or unchecks a model and Next
  saves (Cancel discards); checked
  means you use it, so unchecking one stops using it. The models are saved,
  their speed is measured in the background, and you are back where you came
  from, with no questions.
- **Measuring** runs by itself after a model is added: a few tiny requests
  check that it calls tools, whether it streams, which thinking levels it
  takes, and how fast it answers (routing, when on, picks the fastest model
  that is strong enough). A model paid per token shows the cost (a fraction of a
  cent). Results appear on the model's row and in the sidebar while it runs.
- **A connection** lists its models and its own settings: an endpoint's name,
  address, and key, or removing it; replacing or removing an API key (a key
  from an environment variable is changed in your shell); signing in again or
  out of a subscription.
- **A model** has its name, context window, measured speed (with Measure
  again), what it costs (fixed by its connection; an endpoint can be free on
  this machine or your network, or per token), and Remove this model.
- **Model routing** is off by default: wopr runs the model you pick. Its
  Routing choice is Off, Basic (fixed rules on your order and each model's
  window, speed, and cost), or Jev (enter a Jev endpoint and model; Save
  checks that Jev answers). With routing on, it picks a model for each
  prompt and subagent, the modes (auto, cost, speed, quality, uncensored, private) appear in
  `/model` and Tab, and this screen holds **Put your models in order**,
  strongest first: new models join the bottom; space picks one up, ↑/↓
  carry it, and space drops it (esc puts it back; alt+↑/↓ move it
  directly). Enter on a model opens its settings: use for routing,
  abliterated, and strengths (frontend, backend, tests, docs, …). Suggestions from how models have been
  doing, such as a model escalated often this week, appear above the list.
  Save keeps changes, Turn off turns routing off. See
  [Routing and subagents](routing.md).

Every editing screen has Save and Cancel; nothing is written until Save. Setup
edits `models.json`, `router.json`, and `router-speed.json` under
`~/.wopr/agent` in place: everything it doesn't change, comments included,
stays as it is, and each file is copied to `<file>.<timestamp>.bak` the first
time a setup run changes it.

## Authenticate a provider

List login targets available in the current binary:

```bash
wopr login --list
```

Start an available OAuth login:

```bash
wopr login github-copilot
```

For an API-key provider, set the provider's documented environment variable:

```bash
export OPENAI_API_KEY=...
wopr --model openai/gpt-4.1
```

Use `wopr --help` and `wopr login --help` as the authority for your binary.

## Start a first session

Type a request and press Enter:

```text
Summarize this repository and tell me how to run its checks.
```

WOPR gives the model its normal built-in coding tools unless the command line or settings narrow them.

## Let WOPR set up a project

WOPR includes the reference documentation it needs to configure models, skills,
prompt templates, and themes. Ask the running agent a direct setup question, and
tell it to read the matching local documentation before it changes files:

```text
Read the local WOPR documentation. Set up this repository's .wopr directory:
the skills, prompt templates, and project settings it needs. Explain each
choice.
```

The expected result is files under the project's `.wopr/` directory. WOPR loads
them after you trust the project; see [Security](security.md#project-trust).

## Use print mode

Run one prompt without the interactive TUI:

```bash
wopr -p "Summarize this repository"
```

Select a model explicitly when needed:

```bash
wopr --model github-copilot/gpt-5-mini -p "Find the test entry points"
```

## Verify a source checkout

Run the primary gate:

```bash
make check
```

Use the toolchain versions declared by the repository. See [Development](development.md) for the focused targets.

## Troubleshooting

| Symptom | Likely cause | Action |
|---|---|---|
| `wopr` not found | binary directory absent from `PATH` | add the install directory |
| no selectable models | no provider credential | run `wopr login <provider>` or set its API key env var |

See [Install and troubleshooting](install-troubleshooting.md) for macOS
Gatekeeper, Windows SmartScreen, proxies, and binary verification.

## Offline docs

Every binary carries these pages and writes them to `~/.wopr/docs`:

```bash
wopr docs list
wopr docs show quickstart
wopr docs path
```

## Next steps

- [Using WOPR](usage.md)
- [WOPR concepts](concepts.md)
- [Skills](skills.md)
