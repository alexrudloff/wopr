# WOPR documentation

WOPR is an opinionated coding agent with built-in model routing, for the terminal, written in Go. It routes each prompt to a model chosen for the active mode (see [routing](routing.md)), cuts the tokens each request carries, and keeps the code it writes minimal.

WOPR is under active development. Install it on macOS or Linux with `curl -fsSL https://raw.githubusercontent.com/alexrudloff/wopr/main/install.sh | sh`, on any supported platform with `go install github.com/alexrudloff/wopr/cmd/wopr@latest`, or download an archive from [GitHub Releases](https://github.com/alexrudloff/wopr/releases). Windows support is a preview.

## Start by task

Read the one row that matches the task, then follow only its direct links.

| I want to... | Start here | Then read |
|---|---|---|
| install WOPR and run a first session | [Quickstart](quickstart.md) | [Providers](providers.md) |
| let WOPR set up a project for me | [Quickstart](quickstart.md#let-wopr-set-up-a-project) | [Concepts](concepts.md) |
| understand the small mental model | [Concepts](concepts.md) | [Configuration](configuration.md) |
| configure models and credentials | [Providers](providers.md) | [Models](models.md) |
| run models locally with llama.cpp | [Run local models](llama-cpp.md) | [Routing and subagents](routing.md) |
| let WOPR pick models, or delegate to subagents | [Routing and subagents](routing.md) | [Models](models.md) |
| connect MCP servers (GitHub, Sentry, databases, ...) | [MCP servers](mcp.md) | [Security](security.md#project-trust) |
| let the agent read web pages or search the web | [Web tools](web.md) | [Settings](settings.md) |
| look up a CLI verb, option, or path | [Command line](cli.md) | [Configuration](configuration.md) |
| look up a slash command | [Slash commands](slash-commands.md) | [Keybindings](keybindings.md) |
| change how WOPR runs shell commands | [Shell commands](shell-aliases.md) | [Settings](settings.md) |
| build WOPR on Android (experimental) | [Termux](termux.md) | [Terminal setup](terminal-setup.md) |
| run WOPR in CI or a container | [Environment variables](environment-variables.md) | [Isolate WOPR](containerization.md) |
| change a setting | [Settings](settings.md) | [Security](security.md#project-trust) |
| resume, branch, or export work | [Sessions](sessions.md) | [Compaction](compaction.md) |
| change or look up a key | [Keybindings](keybindings.md) | [Terminal setup](terminal-setup.md) |
| write reusable prompts | [Prompt templates](prompt-templates.md) | [Skills](skills.md) |
| change colors | [Themes](themes.md) | [Settings](settings.md) |
| embed WOPR in a Go program | [SDK](sdk.md) | [RPC mode](rpc.md) |
| add a provider or model endpoint | [Custom providers](custom-provider.md) | [Models](models.md) |
| map entities, paths, and the command that inspects each | [Knowledge graph](knowledge-graph.md) | [Concepts](concepts.md) |
| compare WOPR's benchmark results | [Benchmarks](evals.md) | [Routing and subagents](routing.md) |
| fix an install, trust, or display problem | [Install and troubleshooting](install-troubleshooting.md) | [Quickstart](quickstart.md) |

## Start here

- [Quickstart](quickstart.md) explains how to build and start WOPR.
- [Using WOPR](usage.md) covers the normal command-line and interactive surfaces.
- [WOPR concepts](concepts.md) explains Resources, settings, and trust.
- [Security](security.md) explains trust and process boundaries.
- [Providers](providers.md) explains model authentication and configuration.

## Customize and extend WOPR

Skills, prompt templates, and themes customize WOPR without code. A Go program can embed WOPR and add its own tools through the Go SDK.

Read:

- [Skills](skills.md)
- [Prompt templates](prompt-templates.md)
- [Themes](themes.md)
- [Go SDK](sdk.md)

## Project facts

| Item | Value |
|---|---|
| Executable | `wopr` |
| Module | `github.com/alexrudloff/wopr` |
| User configuration root | `~/.wopr` or `WOPR_HOME` |
| Agent directory | `~/.wopr/agent` or `WOPR_CODING_AGENT_DIR` |
| License | MIT |

## Conventions

- `~` means the user's home directory.
- Shell examples assume bash/zsh and `wopr` on `PATH`.
- Tables are normative reference; prose explains rationale and boundaries.

## Local copy

Every `wopr` binary embeds these pages and writes them to `~/.wopr/docs` (see [`wopr docs`](cli.md#wopr-docs)). WOPR checks the bundle marker at startup: a binary with different pages replaces the synchronized copy. Do not edit the materialized copy; it is overwritten on the next sync.

## Guidance for coding agents

The system prompt points here instead of carrying this advice in every request. Read it when a user asks you to configure WOPR or to extend it.

Configuring WOPR: add skills, prompt templates, and themes under `~/.wopr/agent` or a trusted project's `.wopr/` directory, or list their paths in settings. Use `wopr config` to enable or disable discovered resources. Read [Concepts](concepts.md) before advising.

Extending WOPR: WOPR has no plugin or extension system. Add skills, prompt templates, themes, or MCP servers, or embed WOPR in a Go program with the [SDK](sdk.md) to add tools.
