# Windows setup

WOPR supports source builds on Windows. Do not treat Windows as release-supported until native release verification passes for the published artifact.

## Build from source

Install Git and Go 1.27.1. Then run:

```powershell
git clone git@github.com:alexrudloff/wopr.git
Set-Location WOPR
go build -o bin\wopr.exe .\cmd\wopr
.\bin\wopr.exe --version
```

Windows support is a preview. Instead of building from source, install with `go install github.com/alexrudloff/wopr/cmd/wopr@latest` or download the `windows-amd64` zip from [GitHub Releases](https://github.com/alexrudloff/wopr/releases). The `install.sh` installer supports macOS and Linux only.

## Configuration paths

WOPR stores configuration below the user profile by default:

```text
%USERPROFILE%\.wopr
%USERPROFILE%\.wopr\agent
```

Set `WOPR_HOME` to use an isolated configuration root. Set `WOPR_CODING_AGENT_DIR` only when you need to override the agent directory.

## Terminal

Use Windows Terminal or another terminal that supports the required input and ANSI behavior.

Ctrl+Enter can provide multiline input where Shift+Enter is intercepted by the terminal. See [Terminal setup](terminal-setup.md).

## External editor

WOPR selects an editor in this order:

1. `externalEditor` in `~/.wopr/agent/settings.json`;
2. `VISUAL`;
3. `EDITOR`;
4. Notepad on Windows.

Use Ctrl+X then E to open the current editor buffer.

## Updating

WOPR does not replace a running `wopr.exe` in place, because Windows cannot replace a running program. `wopr update` stops with the manual steps instead; download the release zip from [GitHub Releases](https://github.com/alexrudloff/wopr/releases) and replace the file, or run `go install github.com/alexrudloff/wopr/cmd/wopr@latest` again.

## Verification

Run the Windows-native unit and integration gates before claiming support for an artifact. A Linux cross-build alone does not prove Windows terminal, process, filesystem, or update behavior.
