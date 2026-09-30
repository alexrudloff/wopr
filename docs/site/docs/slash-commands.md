# Slash commands

The dispatcher is exhaustive: every built-in command is listed here. Prompt templates and skills add their own slash commands.

## Core

| Command | Description |
|---|---|
| `/settings` | Open the settings menu. |
| `/model [<name>]` | Choose the orchestrator's and the subagents' models or modes; with a name, set the orchestrator's. A mode for the orchestrator is the subagents' mode too; Subagents splits them. |
| `/thinking [level]` | Set the thinking level, or open the selector without a level. |
| `/login` | Configure provider authentication. |
| `/logout` | Remove stored provider authentication. |
| `/setup` | Set up models: sign in, add API keys and OpenAI-compatible endpoints, and measure them; turn model routing on with a Jev endpoint and put your models in order. Runs from the home screen. |
| `/new` | Start a new session in the same cwd. |
| `/resume` | Resume a different session. |
| `/fork` | Fork from a previous user message. |
| `/clone` | Duplicate session at the current position. |
| `/tree` | Navigate session tree. |
| `/undo` | Undo the last file change a tool made. `/undo <path>` undoes the latest change to that file; `/undo prompt` undoes every change since your last prompt. See [undoing file changes](sessions.md#undoing-file-changes). |
| `/compact` | Manually compact the context. |
| `/reload` | Reload keybindings, skills, prompts, themes, and context files. |
| `/reload --explain` | Same plus resource counts. |
| `/export [path]` | Export session (default HTML; specify `.jsonl`). |
| `/import <path>` | Import and resume a JSONL session. |
| `/share` | Publish the Session as a secret GitHub gist. |
| `/bug [description]` | Write a bug report archive to the current directory and print a prefilled WOPR issue link to attach it to. Nothing is uploaded. |
| `/copy` | Copy the last agent message to clipboard. |
| `/name <text>` | Set the session display name. |
| `/session` | Show session info and stats. |
| `/upgrade` | Upgrade WOPR to the latest release after verifying its signature, then restart into it (reopening this session) or later. |
| `/changelog` | Show changelog entries. |
| `/hotkeys` | List keyboard shortcuts. |
| `/quit` | Exit WOPR. Asks first while background agents run. |
| `/agents [<id>\|stop [id]]` | List this session's subagents (enter opens one's transcript, ctrl+k stops it), open one by id, or stop one. See [Routing and subagents](routing.md). |
| `/gtw [on\|off]` | Toggle Global Thermonuclear War on the current session (same as `ctrl+x g`); `on` or `off` sets it. See [war council](routing.md#war-council). |
| `/goal [<objective>\|stop\|resume]` | Work toward an objective across turns until an independent audit confirms it; with no argument, show its status. `--turns`, `--cost`, and `--time` set its caps. See [Routing and subagents](routing.md#goals). |
| `/trust` | Set the trust decision for the current project. |
| `/llama` | Manage a local llama.cpp server. |
| `/router [status\|on\|off\|pin <tier>\|unpin\|why\|classify <prompt>]` | Show and control model routing: `on` is auto (the router picks the orchestrator and routes subagents), `off` runs everything on your model (both need routing turned on in `/setup`); status shows both halves, tiers, learned speeds, and why the last prompt went where it did. See [Routing and subagents](routing.md). |
| `/mcp [status\|tools <server>\|restart [server]]` | Show MCP servers with their state and tool counts, list one server's tools, or stop servers so they start again on next use. See [MCP servers](mcp.md). |

## Review prompts

| Command | Description |
|---|---|
| `/review` | Review the current changes: correctness bugs first, then over-engineering and simplification opportunities. |
| `/audit` | Audit the whole repository for over-engineering. |
| `/debt` | List every `simplify:` comment as a debt ledger. |

There is no `/exit` or `/clear` command. To
leave, use `/quit`. To clear the editor, press the `app.clear` key (`ctrl+c` by
default). `/hotkeys` lists the current bindings.
