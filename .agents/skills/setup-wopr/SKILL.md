---
name: setup-wopr
description: Check and prepare only the tools needed for a selected wopr task.
---

# Set up wopr development

Start by identifying one task:

1. run an existing wopr binary;
2. build wopr from source; or
3. run the complete verification suite.

If the user did not select a task, ask which task they need. Do not install the
complete toolchain by default.

Read `docs/site/docs/development.md` and `go.mod` before checking versions. Repository files are the version authority.

## Inspect the host

Check the operating system and only the commands required for the selected task.
Use non-mutating commands such as:

```bash
uname -srm
go version
git --version
tmux -V
```

Report installed, missing, and incompatible tools separately.

## Requirements by task

| Task | Required tools |
|---|---|
| Run an existing binary | a compatible wopr binary |
| Build wopr | Git and the Go version required by `go.mod` |
| Run complete verification | Go, Git, and tmux on Unix |

## Installation boundary

Before installing or replacing a tool:

1. show the missing tool and required version;
2. check for an existing manager such as `mise`, `asdf`, Homebrew, or the host
   package manager;
3. propose one command appropriate for the host; and
4. ask the user for approval.

Do not use `sudo`, modify global Git configuration, replace a system toolchain,
or pipe a downloaded script into a shell without explicit approval.

## Verify the selected task

Use the smallest maintained command:

```bash
make build
```

For the complete repository suite, use:

```bash
make check
```

Report every command run, its result, and any tool that still requires user
approval.
