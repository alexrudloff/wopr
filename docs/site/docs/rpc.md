# RPC mode

RPC mode runs WOPR as a headless JSON Lines process. A client writes commands to standard input and reads responses and events from standard output.

Use the [WOPR Go SDK](sdk.md) when your Go application does not need a process boundary. Use RPC mode for another language, an IDE, or a separate process.

## Start RPC mode

```bash
wopr --mode rpc [--model <provider/model>] [options]
```

Common options:

- `--provider <name>` selects a provider when the model argument does not include one.
- `--model <provider/model>` selects the model.
- `--name <name>` or `-n <name>` sets the initial Session name.
- `--no-session` disables Session persistence.
- `--session-dir <path>` selects a Session directory.

WOPR writes operational diagnostics to standard error. Treat standard output as protocol data only.

If you omit `--model` and no default model exists, RPC mode starts with the
`unknown` model record. Use `get_state`, `get_available_models`, and `set_model`
to select a model. A prompt fails preflight until the selected provider has
configured authentication.

## Framing

Each input or output record is one JSON object followed by LF (`\n`).

- Split records on LF only.
- Remove a trailing CR when you send CRLF.
- Do not split JSON strings on Unicode line separators.
- Keep each input command within the 16 MiB command-reader limit.

Every command can contain an `id`. The corresponding response repeats it:

```json
{"id":"req-1","type":"get_state"}
```

```json
{"id":"req-1","type":"response","command":"get_state","success":true,"data":{}}
```

A response confirms command acceptance or reports a command error. Later model or tool failures arrive as events.

## Lifecycle

Close standard input to shut down RPC mode. WOPR cancels active prompts, Bash
commands, Session transitions, model-selection handlers, and retries before it
closes the process.

Go programs can use `github.com/alexrudloff/wopr/coding/rpcclient`. It starts `wopr --mode rpc`, correlates responses,
exposes typed command methods, and delivers events to listeners. Other
languages use the JSONL protocol directly.

## Events

WOPR currently emits these model-loop events:

- `agent_start`;
- `agent_end` with `messages` and `willRetry`;
- `agent_settled`;
- `turn_start`;
- `turn_end` with the assistant message and tool results;
- `message_start`;
- `message_update`;
- `message_end`;
- `tool_execution_start`;
- `tool_execution_update`;
- `tool_execution_end`;
- `bash_execution_update`;
- `queue_update`;
- `thinking_level_changed`;
- `compaction_start` and `compaction_end`;
- automatic-retry and summarization-retry events;
- `entry_appended` after WOPR persists a Session entry outside a turn, such as a cache-warming usage entry;
- `session_info_changed`;
- `ui_notify` with `message` and an optional `notifyType` (`info`, `warning`, `error`), a fire-and-forget notice such as `/llama` progress;
- `error`.

`message_update` contains an `assistantMessageEvent`. Text and tool-call streams use matching start, delta, and end records. Deltas do not contain cumulative partial messages.

Example text stream:

```json
{"type":"message_start","message":{"role":"assistant","content":[]}}
{"type":"message_update","assistantMessageEvent":{"type":"text_start","contentIndex":0}}
{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Hello"}}
{"type":"message_update","assistantMessageEvent":{"type":"text_end","contentIndex":0,"content":"Hello"}}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Hello"}]}}
```

Read message and Session entry shapes in [Session file format](session-format.md).

## Minimal client

```python
import json
import subprocess

process = subprocess.Popen(
    ["wopr", "--mode", "rpc", "--model", "openai/gpt-5", "--no-session"],
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.PIPE,
    text=True,
    bufsize=1,
)

process.stdin.write(json.dumps({
    "id": "prompt-1",
    "type": "prompt",
    "message": "List the packages in this repository.",
}) + "\n")
process.stdin.flush()

for line in process.stdout:
    event = json.loads(line)
    print(event)
    if event.get("type") == "agent_settled":
        break

process.stdin.close()
```

Keep reading standard error separately. A full error pipe can block a child process.

## Reference

- [RPC commands](rpc-commands.md) lists every command and its response.
- [JSON event stream](json.md) describes the event records WOPR also writes in JSON mode.
- [Session file format](session-format.md) describes the entries that Session commands return.

## Source references

WOPR implementation:

- `cmd/wopr/rpc_mode.go` defines the command loop.
- `cmd/wopr/rpc_types.go` defines command and response types.
- `cmd/wopr/rpc_notify.go` defines the notify record.
- `cmd/wopr/rpc_events.go` defines event conversion.

