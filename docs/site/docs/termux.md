# Termux on Android

WOPR does not currently publish or qualify an Android release artifact. This page records an experimental source-build path only.

## Experimental source build

Install Git and a compatible Go toolchain in Termux:

```bash
pkg update
pkg install git golang
```

Clone WOPR after the repository becomes public:

```bash
git clone https://github.com/alexrudloff/wopr.git
cd WOPR
go build -o bin/wopr ./cmd/wopr
./bin/wopr --version
```

Use Go 1.27.1 for this build. The module requires Go 1.27 or newer.

## Configuration

WOPR uses:

```text
~/.wopr
~/.wopr/agent
```

Create global instructions under the WOPR agent directory when needed:

```bash
mkdir -p ~/.wopr/agent
$EDITOR ~/.wopr/agent/AGENTS.md
```

## Limitations

Android and Termux are not release-qualified WOPR targets yet. Verify:

- terminal input and keybindings;
- clipboard behavior;
- filesystem permissions;
- provider TLS and authentication;
- shutdown and Session persistence.

Use an operating-system boundary appropriate for sensitive data. Termux package isolation is not a substitute for reviewing model-generated commands.
