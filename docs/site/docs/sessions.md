# Sessions

A session is one conversation with its full history. WOPR writes every session to
disk as it goes, so you can leave and come back, look at what happened, or start
a new line of work from an earlier point.

## Where sessions are stored

Sessions live under `~/.wopr/agent/sessions/`, in a directory per project. The
directory name is the project path with separators replaced, wrapped in `--`, so
sessions for different projects never mix.

Each session is one JSON Lines file: one entry per line, appended as the session
runs. A crash costs at most the last line, because nothing is rewritten.

Images (screenshots a model reads, images you paste) are kept once in a `blobs`
folder beside the session files, named by their SHA-256, and the session file
holds a reference. Sessions in the project share it, so a fork or clone stores
no second copy. Blobs no session references are removed when a session is
deleted and by a daily sweep. A session whose image blob is gone still loads,
with `[image missing: …]` in its place; older sessions with images inline load
as before.

Set `sessionDir` in [settings](settings.md) to store sessions elsewhere.

## Working with sessions

| Command | What it does |
|---|---|
| `/new` | Start a session with no history |
| `/resume` | Open a different session |
| `/tree` | Show the session as a tree and move to any point |
| `/fork` | Continue from the current point in a new session |
| `/clone` | Copy the session |
| `/name` | Give the session a name |
| `/export` | Write the session to a file |
| `/share` | Publish the session as a secret GitHub gist |

Resuming a session (`/resume`, `wopr -c`, `wopr --resume`) puts back how it
was running: its model, thinking level, and routing (the mode, a pinned
orchestrator, or routing off), and the sidebar as you left it (reply speeds per
model, savings, the last routing decision, finished tasks, and modified files).
`--model` and `WOPR_ROUTER` on the command line still win. A new session starts
with the routing you last chose and an empty sidebar.

## Sharing a session

Run `/share` to publish the active branch as a secret GitHub gist. WOPR renders the branch as a Markdown transcript (prompts, model responses, tool calls, and tool output) and creates the gist with the [GitHub CLI](https://cli.github.com), then prints the gist URL. WOPR shows a privacy notice before it uploads.

`/share` needs `gh` installed and signed in; run `gh auth login` once. A secret gist is unlisted, not private: anyone with its URL can read it. Delete it from GitHub when you no longer need it. Press `Escape` while the upload indicator is open to cancel. WOPR never uploads a Session automatically. Use `/export` instead when the Session must stay local.

## The session tree

`/tree` shows the session as its entries. Move to an entry and continue from
there. Earlier work stays; the new turns branch from the point you chose.

Use this after a wrong turn. Rather than telling the model to forget, move above
the mistake and continue, so the failed attempt never reaches the model again.

Filter the tree when it grows. `ctrl+u` shows your messages only, `ctrl+t` hides
tool results, and `ctrl+l` shows labeled entries. See [keybindings](keybindings.md).

## Undoing file changes

Before `write`, `edit`, or `apply_patch` changes a file, WOPR keeps a copy of it
(or notes that it didn't exist) in the session's archive. `/undo` puts back the
last change: the file returns byte for byte, and a file the tool created is
removed. Run `/undo` again to step further back. `/undo <path>` undoes the latest
change to one file, and `/undo prompt` undoes everything since your last prompt.
It is also in the command palette as **Undo last change**.

If the file changed after the tool's change (you edited it, or a shell command
did), `/undo` still restores it and saves the newer version in the archive, and
says where. The model is told about the undo at its next turn.

Changes made by shell commands (`sed -i`, a Python rewrite, `cat > file`) are
covered too, and the bash card shows their diff. WOPR keeps a copy of every file
the model reads or writes, so a shell change to one of them can be undone. In a
git repository it also compares `git status` before and after each command: a
tracked file that was clean before is restored from git's index, and a new
untracked file counts as created. Outside git, a new file counts as created only
when the command names it. A file that already had uncommitted changes before
the command, and that the model never read, can't be restored and isn't
recorded. For a file WOPR has no change for, `/undo <path>` says when
`git restore` would reset it, and doesn't run it.

Files larger than 5 MB aren't copied. The copies live with the session and go
when it does.

A `write` that would replace a file the session found already there, hasn't
changed, and shares almost nothing with (under a fifth of its lines, ignoring
bare tags and brackets, in a file of three lines or more) is refused to the model, which then writes to a new path or passes
`overwrite: true`. Rewrites of files the session is working on aren't affected.

## Naming and finding

An unnamed session is identified by its time and first message, which is hard to
recognize later. `/name` gives it a name, and `ctrl+n` in the session list filters
to named sessions.

## What a session keeps

The file keeps every entry: your messages, the model's replies, every tool call
and its result, and each compaction. It is the complete record.

The model sees less than the file holds. Compaction replaces older entries with a
summary for the model, and does not change the file. See
[compaction](compaction.md).

## Related

- [Compaction](compaction.md) covers what the model sees as a session grows.
- [Keybindings](keybindings.md) lists the tree and session-list keys.
- [Slash commands](slash-commands.md) lists every Session command.
