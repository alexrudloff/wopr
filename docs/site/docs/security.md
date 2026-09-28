# Security

WOPR runs with the permissions of the user who starts it. WOPR is not a security sandbox for model output, tools, skills, hooks, or shell commands.

Use a container, virtual machine, or another operating-system boundary when you need isolation.

## Project trust

A project can carry its own WOPR configuration. That configuration can change the model, the shell command prefix, the system prompt, and the instructions WOPR gives the model, so WOPR asks before it reads any of it. Cloning a repository and starting WOPR in it must not run that repository's choices; trust is the decision that separates the two.

### What needs trust

WOPR asks when the working directory contains one of these inputs:

| Path | What it changes |
|---|---|
| `.wopr/settings.json` | any setting, including the shell prefix and the model |
| `.wopr/skills/` | instructions given to the model |
| `.wopr/prompts/` | instructions given to the model |
| `.wopr/themes/` | colors only |
| `.wopr/SYSTEM.md` | the system prompt |
| `.wopr/APPEND_SYSTEM.md` | text appended to the system prompt |
| `.agents/skills/` | instructions given to the model; checked in the working directory and every parent, except your own `~/.agents/skills` |

A project with none of these needs no decision, and WOPR does not ask. Until you trust a project, WOPR uses your global configuration alone.

### Making the decision

WOPR asks once, when a session starts in an untrusted project that has one of the inputs above. Use `/trust` to manage the current project decision. Restart WOPR after changing trust so startup discovery runs under the new decision. `--approve` (`-a`) trusts the project for one run, and `--no-approve` (`-na`) ignores its files for one run.

Decisions are stored in `~/.wopr/agent/trust.json`, keyed by the project's canonical path. A path inherits the decision of its nearest stored ancestor, so trusting a directory trusts the repositories inside it.

### Deciding in advance

Set `defaultProjectTrust` in [settings](settings.md) to answer for every project that has no stored decision:

| Value | Behavior |
|---|---|
| `ask` | ask each time. This is the default |
| `always` | trust every project |
| `never` | trust no project |

`always` gives every repository you open the ability to change your settings and instructions. Set it only where you control what you clone.

## Secrets

Keep secret values outside settings, sessions, logs, command arguments, and image layers.

Use environment variables or owner-only files.

Do not put credentials in:

- a prompt or skill;
- a website example;

## Model providers

Provider credentials can grant access to paid services and private data. Use the provider's documented environment variable or WOPR login store. Do not copy tokens into repository settings.

Treat model output as untrusted input. A model can propose a harmful command even when the provider connection is trusted.

## Network content

The documentation site's model catalog contains third-party metadata. A listing does not prove WOPR compatibility, security, or endorsement.

Review the publisher, license, source, and integrity evidence before use.

## Report a vulnerability

Do not open a public issue for an undisclosed vulnerability. Report it privately through the repository's GitHub security advisories.

## Related documentation

- [Settings](settings.md) lists the keys a trusted project can set.
- [Slash commands](slash-commands.md) lists `/trust`.
