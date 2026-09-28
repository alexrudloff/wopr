# Session file format

WOPR stores each Session as JSON Lines. Line 1 is a Session header. Each later line is one entry in a tree.

WOPR preserves unknown entry fields when it reads and writes a Session.

WOPR does not apply `context_edit` entries, which change the context an earlier entry contributes.

## File location

The default location is:

```text
~/.wopr/agent/sessions/--<project-path>--/<timestamp>_<uuid>.jsonl
```

WOPR uses `WOPR_CODING_AGENT_DIR` when you configure a different agent directory.

Use `/resume` to open or delete a Session. WOPR uses the `trash` command for deletion when it is available.

## Header

The first line has no `id` or `parentId`:

```json
{"type":"session","version":3,"id":"session-uuid","timestamp":"2026-01-15T14:00:00Z","cwd":"/work/project"}
```

A cloned Session can also contain `parentSession`.

The current Session schema version is 3. WOPR migrates supported older Session shapes when it loads them. Do not change the version field by hand.

## Entry tree

Every entry after the header contains:

```json
{
  "type": "message",
  "id": "a1b2c3d4",
  "parentId": null,
  "timestamp": "2026-01-15T14:00:01Z"
}
```

`parentId` points to the previous entry on that branch. A root entry uses `null`. A branch adds a new child to an earlier entry. The active leaf identifies the current branch.

Entries stay in append order in the file. Build the current conversation by walking from the active leaf to the root. Do not treat file order as one linear conversation.

## Entry types

### Message

A `message` entry contains one nested Agent message:

```json
{"type":"message","id":"a1b2c3d4","parentId":null,"timestamp":"2026-01-15T14:00:01Z","message":{"role":"user","content":[{"type":"text","text":"Hello"}],"timestamp":1768485601000}}
```

Supported message roles include:

- `user`;
- `assistant`;
- `toolResult`;
- `bashExecution`;
- `custom`;
- `branchSummary`;
- `compactionSummary`.

Content blocks use these field names:

- `text` with `text` and an optional `textSignature`;
- `image` with `data` (base64-encoded image bytes) and `mimeType`, such as `image/png`. There is no URL form;
- `thinking` with `thinking`, an optional `thinkingSignature`, and an optional `redacted` flag;
- `toolCall` with `id`, `name`, `arguments`, and an optional `thoughtSignature` and `namespace`.

For example, an assistant turn that calls the `read` tool stores:

```json
{"type":"toolCall","id":"call_1","name":"read","arguments":{"path":"a.txt"}}
```

Treat signatures as opaque provider data. Sessions written by older WOPR versions can contain a `tool_use` block with `input` in place of `arguments`. WOPR still reads that form and writes `toolCall`.

A completed assistant message can contain provider, model, API, usage, diagnostics, response identity, `stopReason`, and `errorMessage` fields. Treat fields that you do not use as opaque data.

A Bash entry uses the normal outer `message` shape. Its nested role is `bashExecution`:

```json
{"type":"message","id":"b2c3d4e5","parentId":"a1b2c3d4","timestamp":"2026-01-15T14:00:02Z","message":{"role":"bashExecution","command":"go test ./...","output":"ok","exitCode":0,"cancelled":false,"truncated":false}}
```

Optional Bash fields are `fullOutputPath` (the file that holds output too long to store inline), `timestamp` (Unix milliseconds), and `excludeFromContext` (set for a command run with `!!`, which the model does not see).

### Model change

```json
{"type":"model_change","id":"c3d4e5f6","parentId":"b2c3d4e5","timestamp":"2026-01-15T14:05:00Z","provider":"openai","modelId":"gpt-5"}
```

### Thinking-level change

```json
{"type":"thinking_level_change","id":"d4e5f6a7","parentId":"c3d4e5f6","timestamp":"2026-01-15T14:06:00Z","thinkingLevel":"high"}
```

### Compaction

```json
{"type":"compaction","id":"e5f6a7b8","parentId":"d4e5f6a7","timestamp":"2026-01-15T14:10:00Z","summary":"Earlier work summary","firstKeptEntryId":"c3d4e5f6","tokensBefore":50000}
```

Optional fields include `details`, `fromHook`, summary-generation `usage`, and `systemMessage`. `systemMessage` records the system prompt and tool declarations at the compaction boundary. It becomes the leading system message of the compacted context.

WOPR builds post-compaction context from the summary, `firstKeptEntryId`, and later entries. The current format does not define a `retainedTail` field.

### Branch summary

```json
{"type":"branch_summary","id":"f6a7b8c9","parentId":"a1b2c3d4","timestamp":"2026-01-15T14:15:00Z","fromId":"e5f6a7b8","summary":"Summary of the branch that was left"}
```

Optional fields include `details`, `fromHook`, and `usage`.

### Custom entry

A custom entry stores feature state, such as a bug report record. It does not become model context:

```json
{"type":"custom","id":"a7b8c9d0","parentId":"f6a7b8c9","timestamp":"2026-01-15T14:20:00Z","customType":"review-state","data":{"count":42}}
```

Each entry uses a namespaced `customType`. Treat data of an unknown `customType` as opaque.

### Custom message

A custom message can participate in model context:

```json
{"type":"custom_message","id":"b8c9d0e1","parentId":"a7b8c9d0","timestamp":"2026-01-15T14:25:00Z","customType":"review-note","content":"Review the changed API.","display":true}
```

`content` can be a string or content-block array. `details` is optional.

### Label

```json
{"type":"label","id":"c9d0e1f2","parentId":"b8c9d0e1","timestamp":"2026-01-15T14:30:00Z","targetId":"a1b2c3d4","label":"checkpoint"}
```

A `null` label clears the target label.

### Session information

```json
{"type":"session_info","id":"d0e1f2a3","parentId":"c9d0e1f2","timestamp":"2026-01-15T14:35:00Z","name":"Refactor authentication"}
```

WOPR shows the latest Session name in `/resume`.

## Context construction

WOPR builds model context from the active root-to-leaf branch:

1. It finds the active branch.
2. It applies the latest model and thinking-level entries.
3. It applies compaction at `firstKeptEntryId`.
4. It converts message, custom-message, branch-summary, and compaction entries to model messages.
5. It excludes custom state entries and Bash entries marked `excludeFromContext`.

Do not reconstruct model context by reading every `message` line in file order.

## Parse a file in Go

Use `json.RawMessage` so unknown fields survive your parser:

```go
file, err := os.Open("session.jsonl")
if err != nil {
    return err
}
defer file.Close()

scanner := bufio.NewScanner(file)
scanner.Buffer(make([]byte, 64*1024), 128*1024*1024)
for scanner.Scan() {
    var entry struct {
        Type     string          `json:"type"`
        ID       string          `json:"id"`
        ParentID *string         `json:"parentId"`
        Message  json.RawMessage `json:"message"`
    }
    if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
        return err
    }
    // Process known fields. Retain scanner.Bytes() for an exact copy.
}
return scanner.Err()
```

Set an explicit scanner limit. A Session line can contain a large tool result or image.

## Compatibility rules

- Preserve the header schema version.
- Preserve unknown fields and unknown entry types.
- Preserve `null` versus a missing `parentId`.
- Do not reorder entries.
- Do not rewrite timestamps or IDs.
- Do not edit a Session while WOPR is writing it.

## Source references

WOPR implementation:

- `internal/codingagent/session.go` defines the durable entry shapes.
- `internal/codingagent/session_manager.go` creates, opens, and lists Sessions.

