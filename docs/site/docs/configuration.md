# Configuration

WOPR reads configuration from two places: the agent directory, which applies to every project you open, and the `.wopr` directory inside a project. WOPR also loads instruction files such as `AGENTS.md` from the directories around your working directory.

WOPR keeps all of its state under `~/.wopr`.

Use `/settings` in an interactive session to change common preferences. When you edit a configuration file by hand, run `/reload` so the running session picks up the change. `/reload` rereads settings, keybindings, skills, prompt templates, themes and context files.

## Configuration root

WOPR chooses one configuration root at startup:

| Order | Source | Root |
|---|---|---|
| 1 | `WOPR_HOME` is set and not empty | `$WOPR_HOME` |
| 2 | `XDG_CONFIG_HOME` is set | `$XDG_CONFIG_HOME/wopr` |
| 3 | default | `~/.wopr` |

The root holds these directories:

| Path | Contents |
|---|---|
| `<root>/agent/` | The agent directory. See below. |
| `<root>/state/<namespace>/` | State that one WOPR capability owns. |
| `<root>/docs/` | The offline documentation that `wopr docs` writes. |

## Agent directory

The agent directory is `<root>/agent`, which is `~/.wopr/agent` by default. Set `WOPR_CODING_AGENT_DIR` to move only this directory.

This page writes the agent directory as `<agent-dir>`.

| Path | Purpose |
|---|---|
| `<agent-dir>/settings.json` | User [settings](settings.md), including resource declarations. |
| `<agent-dir>/keybindings.json` | Custom [keybindings](keybindings.md). |
| `<agent-dir>/models.json` | Custom endpoints and models. See [custom providers](custom-provider.md). |
| `<agent-dir>/auth.json` | Stored API keys and OAuth tokens. See [providers](providers.md). |
| `<agent-dir>/trust.json` | Saved project trust decisions. See [security](security.md). |
| `<agent-dir>/AGENTS.md` | Your instructions for every project. See [context files](#context-files). |
| `<agent-dir>/SYSTEM.md` | Replaces WOPR's default system prompt. |
| `<agent-dir>/APPEND_SYSTEM.md` | Adds text to the end of the system prompt. |
| `<agent-dir>/skills/` | User [skills](skills.md). |
| `<agent-dir>/prompts/` | User [prompt templates](prompt-templates.md). |
| `<agent-dir>/themes/` | User [themes](themes.md). |
| `<agent-dir>/sessions/` | Saved [sessions](sessions.md), one directory per project. |
| `<agent-dir>/bin/` | Helper binaries that WOPR downloads for its tools, such as `fd` and `rg`. |

WOPR also finds skills in `~/.agents/skills/`. See [skills](skills.md).

## Project directory

A project keeps its configuration in `.wopr` inside the working directory. WOPR reads `.wopr` only from the working directory itself. It does not look for `.wopr` in parent directories.

| Path | Purpose |
|---|---|
| `.wopr/settings.json` | Project settings. Keys here override the same keys in `<agent-dir>/settings.json`. |
| `.wopr/SYSTEM.md` | Replaces the system prompt for this project. |
| `.wopr/APPEND_SYSTEM.md` | Adds project text to the end of the system prompt. |
| `.wopr/skills/` | Project skills. |
| `.wopr/prompts/` | Project prompt templates. |
| `.wopr/themes/` | Project themes. |

### Project trust

A project directory can contain code that runs on your machine. WOPR therefore asks whether you trust a project before it reads `.wopr/settings.json`, `SYSTEM.md`, `APPEND_SYSTEM.md`, skills, prompt templates or themes from it. If you do not trust the project, WOPR ignores all of them.

One setting is an exception. WOPR reads `sessionDir` from the project settings before it asks, because it must locate the project's sessions first.

Use `--approve` (`-a`) to trust the project for one run, or `--no-approve` (`-na`) to ignore project files for one run. Use `/trust` to save a decision. See [security](security.md).

### System prompt files

WOPR looks for `SYSTEM.md` and for `APPEND_SYSTEM.md` separately. For each name, a file in a trusted project wins over the file in the agent directory. WOPR uses one file per name and does not combine the two.

The command line overrides both files. `--system-prompt` replaces the discovered `SYSTEM.md`. `--append-system-prompt` replaces the discovered `APPEND_SYSTEM.md`, and you can give it more than once. Each value can be text or a path to a file.

## Context files

Context files hold instructions for the model, such as build commands, code conventions and project rules. WOPR adds their content to the system prompt.

WOPR looks for context files in the agent directory, in the working directory, and in every parent directory up to the file system root. In each directory it takes the first file that exists from this list:

1. `AGENTS.override.md`
2. `AGENTS.md`
3. `AGENTS.MD`
4. `CLAUDE.md`
5. `CLAUDE.MD`

A directory contributes at most one file. `AGENTS.override.md` therefore replaces `AGENTS.md` or `CLAUDE.md` in its own directory only. It does not hide files in the agent directory or in other directories.

WOPR loads the files in this order:

1. the file in the agent directory;
2. the files from the file system root down to the working directory.

The file closest to your working directory comes last, so its instructions appear after the general ones.

In a Git worktree that sits inside its main checkout, WOPR skips the main checkout's context file when the worktree root has its own file with the same name. This prevents the same repository instructions from loading twice.

Context files do not need project trust. WOPR loads them even when you decline to trust the project.

The interactive startup banner lists the loaded files on its `[Context]` line. Use `--no-context-files` (`-nc`) to load none of them for one run.

After an in-process switch to a session from another project, WOPR keeps the context files of the project it started in.

## Diagnostics

`wopr status` prints the resolved configuration paths, Resource health, the providers with stored credentials, and the default model. `wopr version` prints the WOPR version, the build, the Go runtime, and the platform.

Set `WOPR_STARTUP_TRACE=1` to print the time of each startup step. See [environment variables](environment-variables.md) for the other diagnostic variables.

## Related

- [Settings](settings.md) lists every settings key.
- [Environment variables](environment-variables.md) lists the variables WOPR reads.
- [Security](security.md) explains project trust.
- [How WOPR works](how-wopr-works.md) shows where configuration enters the agent loop.
