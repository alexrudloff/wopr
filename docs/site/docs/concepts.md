# How WOPR fits together

WOPR is a small coding agent. The shortest accurate model is:

> Resources shape a session. Settings select them. Trust gates the ones a
> project contributes.

Return to the [docs index](index.md) at any time.

## Resources

| Resource | What it does | Where WOPR finds it |
|---|---|---|
| Skill | reusable instructions the model can load | `~/.wopr/agent/skills`, trusted `.wopr/skills`, `.agents/skills`, `skills` setting, `--skill` |
| Prompt template | a reusable `/name` prompt | `~/.wopr/agent/prompts`, trusted `.wopr/prompts`, `prompts` setting, `--prompt-template` |
| Theme | TUI colors | `~/.wopr/agent/themes`, trusted `.wopr/themes`, opencode theme directories, `themes` setting, `--theme` |
| Context file | project instructions | `AGENTS.md` and `CLAUDE.md` from the working directory up |

`wopr config` lists discovered skills, prompt templates, and themes and toggles
them for the user or the project. `wopr status` reports the same inventory.

## Settings and trust

User settings live in `~/.wopr/agent/settings.json`. Project settings live in
`.wopr/settings.json` and apply only after you trust the project. See
[Settings](settings.md) and [Project trust](security.md).

## Where to go next

- [Skills](skills.md) for reusable instructions.
- [Prompt templates](prompt-templates.md) for reusable prompts.
- [Config](configuration.md) for paths.
