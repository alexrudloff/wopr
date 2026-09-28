# How WOPR works

WOPR sends model requests, runs tools, assembles context and stores sessions. This page shows how those parts fit together.

A session is WOPR's record of one conversation. It holds messages, tool calls and their results, model changes, compactions and other events. The entries form a tree. Each path from the root to an entry is a branch, and the branch that ends at the current entry is the active branch. The active branch supplies the history for the next model request.

## Agent loop

When you submit a message, WOPR adds it to the active branch. WOPR then builds a model request from the system prompt, the messages on the active branch, the definitions of the active tools, and the model settings. It sends the request to the selected provider.

The provider streams back an assistant message. The message can contain text, thinking and tool calls. WOPR records the message, runs each tool call, and records each result. That is one turn.

WOPR starts another turn when the last turn produced tool results or when a steering message is waiting. Otherwise it checks the follow-up queue. When both queues are empty, the run ends.

You can send more input while the agent runs:

| Input | When WOPR delivers it |
|---|---|
| Steering message | After the current turn, before the next model request. |
| Follow-up message | After the agent has no more work, before the run ends. |

`Enter` steers and `Alt+Enter` queues a follow-up. On Windows and WSL, `Ctrl+Q` queues a follow-up. Press `Escape` to stop the run. WOPR then puts the queued messages back into the editor. See [keybindings](keybindings.md).

Before each further turn of a run, WOPR checks the context size. When the context passes its limit, WOPR compacts older history into a summary and then continues the run. See [compaction](compaction.md).

## Context

The active branch supplies the conversation history. WOPR turns each session entry into a model message: a user, assistant or tool-result message. Shell commands you run with `!` (but not `!!`), custom messages, branch summaries and compaction summaries become user-role text. See [message types](message-types.md).

The system prompt has these parts:

- WOPR's base instructions, or your `SYSTEM.md`;
- your `APPEND_SYSTEM.md`, when one exists;
- the content of the [context files](configuration.md#context-files), such as `AGENTS.md`;
- the name and description of each available skill.

WOPR loads the full instructions of a skill only when the model reads them.

Editor input passes through prompt templates and skill commands before it becomes a user message. Files you attach with `@`, images and pasted text become part of that message.

## Sessions

WOPR saves each session as a JSON Lines file under `~/.wopr/agent/sessions/`. Every entry has an ID and names its parent entry.

When you continue from an earlier entry, WOPR starts a new branch in the same file. `/fork` and `/clone` copy history into a new session file.

WOPR rebuilds the model context from the active branch. A compaction entry replaces older messages in later requests. The replaced entries stay in the file. See [sessions](sessions.md) and [session file format](session-format.md).

## Interfaces

Every interface uses the same agent loop and the same sessions:

| Interface | How to start it | What it does |
|---|---|---|
| Interactive | `wopr` | Shows the session in a terminal UI. |
| Print | `wopr -p "prompt"` | Runs the prompt and writes the final answer. |
| JSON | `wopr --mode json "prompt"` | Runs the prompt and writes every event as JSON Lines. |
| RPC | `wopr --mode rpc` | Reads JSON Lines commands on standard input and writes responses and events. |
| Go SDK | `github.com/alexrudloff/wopr/coding` | Runs sessions inside a Go program. |

WOPR's SDK is a Go module. See [CLI integration](cli-integration.md) and [Go SDK](sdk.md).

## Tools

WOPR enables four built-in tools by default: `read`, `bash`, `edit` and `write`. `grep`, `find` and `ls` are available but off by default. On Windows, WOPR also offers a `powershell` tool. Use `--tools`, `--exclude-tools` or the `defaultTools` setting to change the active set.

WOPR sends every active tool definition with each model request. A Go program that embeds WOPR can add its own tools through the [Go SDK](sdk.md).

## Resources

Skills provide instructions and supporting files. Prompt templates provide reusable message text. Themes set terminal colors.

## Trust and permissions

WOPR asks whether you trust a project before it loads the project's settings and resources. After that decision, WOPR loads the context files, which do not need trust. See [configuration](configuration.md).

Tools run with the operating-system permissions of the `wopr` process. See [security](security.md).

## Related

- [Configuration](configuration.md) lists the files WOPR reads.
- [Concepts](concepts.md) explains Resources, settings and trust.
- [Message types](message-types.md) defines the messages in a session.
