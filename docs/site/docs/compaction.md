# Compaction

A model has a fixed context window. A long session eventually fills it.
Compaction replaces the older part of the conversation with a summary, so the
session continues instead of failing.

## When WOPR compacts

WOPR compacts when the context in use passes the window minus a reserve, or 200000 tokens on a larger window:

```
contextTokens > min(contextWindow - reserveTokens, maxContextTokens)
```

The reserve leaves room for the next prompt and its answer. Without it, WOPR would
compact only after a request was already too large to send. The ceiling keeps a 1M-token window from growing a conversation that every turn re-reads in full.

The model running the conversation writes the summary; it has already seen everything it summarizes. When the conversation is larger than that model's window, it summarizes the newest messages that fit.

The sizes follow the model in use (the routed one while routing is on): the
reserve is the model's max output, at most 16384 and at most a quarter of its
window, and the kept tail is a fifth of the window, at most 20000. A model
that states no window counts as 32768. Model routing uses the same reserve to
decide which models a conversation fits, and can compact at a new prompt to
fit a better model (see routing).

## Settings

| Key | Default | Meaning |
|---|---|---|
| `compaction.enabled` | `true` | Compact automatically |
| `compaction.reserveTokens` | sized to the model | Tokens held back from the window |
| `compaction.keepRecentTokens` | sized to the model | Recent conversation kept in full |
| `compaction.maxContextTokens` | `200000` | Compact past this even on a larger window; `0` lets the window decide |

Set them in [settings](settings.md):

```json
{
  "compaction": {
    "enabled": true,
    "reserveTokens": 16384,
    "keepRecentTokens": 20000
  }
}
```

Raise `reserveTokens` if requests still fail as too large. Raise
`keepRecentTokens` to keep more recent detail, which compacts more often. Set
values are clamped per model so a compaction always fits: at most a quarter
of the window is reserved, and the kept tail leaves room for the summary and
the reserve.

## What is kept

WOPR keeps the most recent `keepRecentTokens` of conversation in full and
summarizes what comes before it.

WOPR cuts only where a cut is valid. A tool result must follow its tool call, so
WOPR never cuts between them. Valid cut points are user messages, assistant
messages, shell executions, branch summaries, custom messages and earlier
compactions.

## Compacting by hand

Use `/compact` to compact now, before the window fills. This is useful when you
finish one task and start another in the same session: the summary keeps the
outcome and drops the intermediate steps.

Add a note to say what matters: `/compact keep the API decisions; next we write the migration`. The summary is written with the note in mind, and the note is kept in it word for word, so the agent reads it when it continues.

## Context pruning

Before a compaction is needed, WOPR removes what the model no longer needs.
Each change is a `context_edit` entry in the session file, so the original stays
on disk.

Three strategies run without a model call:

- **Duplicate calls.** When `read`, `grep`, `find`, `ls`, `obs_recall`,
  `web_fetch` or `web_search` runs again with the same arguments, and no `edit`
  or `write` changed the file in between, the older result becomes a one-line
  note. The newest result stays.
- **Failed-call inputs.** Two assistant messages after a tool call fails, its
  large arguments (such as the old and new text of a failed `edit`) are
  dropped. The call and its error message stay.
- **Stale reads.** A `read` of a file that a later `write` replaced, or that a
  later `edit` changed and a later `read` shows again, becomes a one-line note.

The model can also compress. Once the context passes `compressThreshold` of the
window, WOPR offers a `compress` tool and numbers user messages and tool
results `[#N]`. The model names a finished span by number and writes a summary.
From the next request, the summary replaces the span. The original goes to the
observation archive, and the summary names the `obs_recall` id that reads it
back.

### Pruning and the prompt cache

Changing an earlier message invalidates the provider's prompt cache from that
message on. WOPR therefore plans pruning before every request but applies it in
batches:

- when the cache is already cold: more than 5 minutes after the last request
  (unless `cacheWarming` is `idle`), after a compaction, or on the first request
  after WOPR starts;
- just before a threshold compaction, which pruning can make unnecessary;
- together with a `compress` call, for the edits after the compressed span;
- when the saving over the coming requests pays for rewriting the cache.

Offering `compress` and numbering messages also change the prompt, so WOPR
waits for a cold cache before it offers the tool, unless the context is 20
points past the threshold.

| Key | Default | Meaning |
|---|---|---|
| `contextPruning.enabled` | `true` | Turn every strategy on or off |
| `contextPruning.dedupe` | `true` | Replace older duplicate tool calls |
| `contextPruning.purgeErrors` | `true` | Drop large inputs of failed calls |
| `contextPruning.supersedeReads` | `true` | Replace stale reads |
| `contextPruning.compress` | `true` | Offer the `compress` tool |
| `contextPruning.compressThreshold` | `0.4` | Share of the context window in use before `compress` is offered |

`/router` shows the current state under **Context pruning**.

## What you lose

A summary is smaller than what it replaces. Exact wording, full file contents and
individual tool output do not survive it. The session file keeps every entry, so
nothing is lost on disk. Only what the model sees is reduced. See
[sessions](sessions.md).

Start a new session rather than compacting when the next task shares nothing with
the last one. A summary of unrelated work costs context and adds no value.

## Summary format

WOPR asks the model for a summary with fixed Markdown sections, so that the next model reads the same structure every time:

| Section | Content |
|---|---|
| `## Goal` | What the user wants to achieve. |
| `## Constraints & Preferences` | Requirements and preferences the user stated, or `(none)`. |
| `## Progress` | Three subsections: `### Done`, `### In Progress` and `### Blocked`. |
| `## Key Decisions` | Each decision with a short reason. |
| `## Next Steps` | An ordered list of what comes next. |
| `## Critical Context` | Data and references needed to continue, or `(none)`. |

The instructions tell the model to keep exact file paths, function names and error messages.

When the session already has a compaction, WOPR passes the earlier summary to the model in `<previous-summary>` tags. The model updates that summary instead of starting again, so earlier goals and decisions carry forward.

When the cut falls inside one long turn, WOPR summarizes the start of that turn separately, with the sections `## Original Request`, `## Progress So Far` and `## Context Needed to Continue`.

WOPR then appends the files the summarized part touched, as `<read-files>` and `<modified-files>` lists. A file that was both read and changed appears only under `<modified-files>`. The same two lists are stored as `readFiles` and `modifiedFiles` in the entry's `details`.

The result is a `compaction` entry in the session file with `summary`, `firstKeptEntryId` and `tokensBefore`. See [session file format](session-format.md#compaction). When WOPR rebuilds the context, the summary becomes a `compactionSummary` message. See [message types](message-types.md#compaction-summary).

## Branch summaries

When you move to another point with `/tree`, WOPR asks whether to summarize the branch you leave. Choose `No summary`, `Summarize`, or `Summarize with custom prompt`. Set `branchSummary.skipPrompt` to `true` to skip the question and move without a summary.

A branch summary uses the same sections as a compaction summary, without `## Critical Context`. WOPR stores it as a `branch_summary` entry and adds the same file lists.

## Related

- [Settings](settings.md) lists every compaction key.
- [Sessions](sessions.md) covers what the session file keeps.
- [Slash commands](slash-commands.md) lists `/compact`.
