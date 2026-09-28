# CLI integration

Run `wopr` with no options in a terminal to open the interactive terminal UI. Scripts and other programs use one of the three non-interactive modes instead: print, JSON or RPC.

Every mode uses the same agent loop, sessions, tools and resources. The mode decides how input reaches WOPR, what WOPR writes to standard output, and whether the process stays up for more commands. The options that select the model, tools, resources and session storage work the same way in every mode. See [command line](cli.md).

## Choose a mode

| Mode | Start it with | Output | Lifetime | Use it when |
|---|---|---|---|---|
| Interactive | `wopr` | Terminal UI | Until you exit | A person works with WOPR directly. |
| Print | `wopr -p "prompt"` | Final answer as text | One invocation | A script needs only the final answer. |
| JSON | `wopr --mode json "prompt"` | Events as JSON Lines | One invocation | A program needs the progress of one run. |
| RPC | `wopr --mode rpc` | Responses and events as JSON Lines | Until standard input closes | A program needs to send several commands. |

WOPR selects print mode on its own when standard input or standard output is not a terminal. A pipe or a redirect is therefore enough:

```bash
git diff | wopr "Write a commit message for this diff" > message.txt
```

## Print mode

Print mode runs the prompt, writes the text of the final assistant message to standard output, and exits:

```bash
wopr -p "Summarize the changes in this repository"
```

`--print` (`-p`) takes no value. WOPR builds the prompt from piped standard input, `@file` arguments and the first message argument, in that order. It sends each further message argument as a separate prompt after the first one finishes.

When standard output is not a terminal, WOPR keeps it for the answer and redirects anything that tools write there to standard error.

| Result | Exit status |
|---|---|
| The final assistant message completes | `0` |
| The final assistant message has stop reason `error` or `aborted` | `1`, with the error on standard error |
| WOPR cannot start the run, for example because no model is configured | `1` |
| `SIGINT`, `SIGTERM` or `SIGHUP` stops the run | `128` plus the signal number: `130`, `143` or `129` |

## JSON mode

JSON mode writes one JSON object per line. The first line is the session header. Agent and session events follow:

```bash
wopr --mode json "Review this repository" > events.jsonl
```

The output is a stream of events. It does not ask the model to answer in JSON.

All prompts come from the command line and standard input. The process streams the events of that run and exits. It does not accept further commands.

A failed or aborted assistant message appears in the events but does not change the exit status. Read the `message_end` events when success matters. WOPR exits with status `1` when the run itself fails.

A `message_update` event carries one delta, not the whole message so far. Build live output from the deltas, then replace it with the complete message in `message_end`.

`agent_end` can be followed by an automatic retry or by queued work. Wait for `agent_settled` to know that the run has no more work.

Standard output carries only JSON Lines. WOPR writes diagnostics and logs to standard error. See [JSON event stream mode](json.md) for event shapes.

## RPC mode

RPC mode keeps WOPR running while another program sends commands:

```bash
wopr --mode rpc --no-session
```

The client writes one JSON command per line to standard input. WOPR writes one JSON response or event per line to standard output.

Add an `id` to a command to match it with its response. The response repeats the `id`. Events have no `id`, because they describe the session and not one command.

A successful response to `prompt` means that WOPR accepted the prompt. It does not mean the run finished. Keep reading events until `agent_settled` when you need the result.

Commands can change the model, read the session state, manage sessions, and run shell commands. See [RPC commands](rpc-commands.md). `ui_notify` records are notices that a client can show or ignore. Features that need the terminal UI are not available in RPC mode.

Close standard input to stop WOPR. See [RPC mode](rpc.md) for framing and events.

Programs use the JSON Lines protocol directly. [RPC mode](rpc.md) includes a minimal Python client.

## Related

- [Command line](cli.md) lists the command-line options.
- [JSON event stream mode](json.md) describes the event records.
- [RPC mode](rpc.md) describes the RPC protocol.
- [RPC commands](rpc-commands.md) lists every RPC command.
- [Message types](message-types.md) defines the messages inside events.
