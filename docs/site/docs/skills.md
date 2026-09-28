# Skills

A skill is a block of task-specific instructions you load into a session. A skill adds *knowledge*: how to do a particular job well, in your own words, without writing code. When a skill is loaded, its instructions become part of the agent's guidance for that session.

Use a skill when the thing you want to reuse is a procedure or a convention, not a new tool: your commit-message format, how to run this repo's tests, the steps for cutting a release.

## What a skill is on disk

A skill is a directory named for the skill, containing a `SKILL.md` file:

```text
~/.wopr/skills/commit/SKILL.md
```

`SKILL.md` is markdown with a YAML frontmatter header:

```markdown
---
name: commit
description: Write a commit message in this repo's format.
---

Write commits as `type(scope): summary`. Keep the summary under 72 chars.
Explain *why* in the body, not what. Never mention tooling.
```

- **`name`** - the skill's identifier (defaults to the directory name).
- **`description`** - a one-line summary.
- **body** - the instructions, appended to the agent's system prompt under a `## Skills` heading when the skill is loaded.

## Load a skill

Explicitly, for one run:

```bash
wopr --skill commit
wopr --skill ./path/to/skill-dir      # a direct path also works
```

## Discovery

Beyond explicit `--skill` entries, wopr discovers skills already sitting in a project or your home dir.

WOPR scans:

- `~/.wopr/agent/skills/` and the `skills` setting,
- `.agents/skills/` at each directory from the workspace up to its root,
- `~/.agents/skills/` for user-level skills,
- `.wopr/skills/` in the workspace (only after the project is trusted).

Disable discovery with `--no-skills`. `wopr config` enables or disables individual discovered skills.

## See also

- [concepts](concepts.md) - where skills sit among resources.
- [settings](settings.md) - the `skills` setting.
