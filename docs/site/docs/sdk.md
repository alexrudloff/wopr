# Go SDK

WOPR exposes its coding agent as Go packages. Use the SDK when a Go application needs to create and control WOPR sessions without starting the command-line interface as a child process.

Use RPC mode when the caller is not a Go program or when a process boundary is required.

## Module

```text
github.com/alexrudloff/wopr
```

WOPR is under active development. Pin an exact release or commit when you embed it. Do not assume API stability before the first public release.

After the repository and version tags are public, use the generated API reference
on pkg.go.dev:

- [`github.com/alexrudloff/wopr`](https://pkg.go.dev/github.com/alexrudloff/wopr)
- [`github.com/alexrudloff/wopr/agent`](https://pkg.go.dev/github.com/alexrudloff/wopr/agent)
- [`github.com/alexrudloff/wopr/ai`](https://pkg.go.dev/github.com/alexrudloff/wopr/ai)
- [`github.com/alexrudloff/wopr/coding`](https://pkg.go.dev/github.com/alexrudloff/wopr/coding)
- [`github.com/alexrudloff/wopr/tui`](https://pkg.go.dev/github.com/alexrudloff/wopr/tui)

## Minimal flow

A program normally:

1. creates a `coding.Services` container;
2. resolves a model;
3. starts or resumes a `coding.Session`;
4. sends a prompt;
5. consumes returned messages or session events;
6. closes the Session.

```go
package main

import (
    "context"
    "log"

    "github.com/alexrudloff/wopr/coding"
)

func main() {
    services, err := coding.NewServices(coding.ServicesOptions{
        CWD:      ".",
        AgentDir: coding.DefaultAgentDir(),
    })
    if err != nil {
        log.Fatal(err)
    }

    model, err := coding.BuildModel("github-copilot/gpt-4o-mini", services)
    if err != nil {
        log.Fatal(err)
    }

    session, err := coding.StartSession(services, coding.SessionStartOptions{
        Model:        model,
        SystemPrompt: "You are a concise coding assistant.",
    })
    if err != nil {
        log.Fatal(err)
    }
    defer session.Close()

    _, err = session.Send(context.Background(), "Summarize this repository.")
    if err != nil {
        log.Fatal(err)
    }
}
```

The repository contains a complete example at `examples/sdk/main.go`.

## Services

`coding.Services` owns shared collaborators such as settings, authentication, model registry state, and paths. Pass it to `coding.BuildModel` and `coding.StartSession` instead of constructing hidden global dependencies.

Set the working directory and agent directory explicitly when your application does not use command-line defaults.

The normal agent directory is:

```text
~/.wopr/agent
```

Use `WOPR_HOME` or explicit options for isolated tests and applications.

## Sessions

Start a fresh Session with `coding.StartSession`. Set `SessionStartOptions.ResumePath` to resume a saved Session file.

`Session.Send` blocks until the agent tool loop finishes or the context is cancelled. Do not wrap it in an unowned goroutine only to make it appear asynchronous.

Subscribe to Session events before sending when your application needs streaming updates.

## Models and authentication

`coding.BuildModel` resolves a provider and model through the Services container. Authenticate with the standalone `wopr` command or provide the application-specific credential mechanism before creating the model.

Verify a model from the command line:

```bash
wopr --print --model github-copilot/gpt-4o-mini "hello"
```

Do not copy credentials into source code.

## Tools

Pass caller-owned tools through `ExtraTools` on `coding.SessionStartOptions`. Use the public `agent.AgentTool` contract. `BeforeToolCall` hooks run before each tool call and can block it.

A programmatic Session does not automatically need every interactive CLI tool. Select the tools that belong to the application.

## Cancellation and shutdown

Pass a `context.Context` to session operations. Cancel it when the calling operation ends.

Close resources in this order:

1. finish or cancel active Session operations;
2. close each Session;
3. release application-owned Services dependencies.

Do not replay a failed tool operation automatically unless the application knows that the operation is idempotent.

## RPC alternative

Start WOPR in RPC mode when the caller needs a language-neutral process boundary:

```bash
wopr --mode rpc
```

See [RPC mode](rpc.md).

## Related documentation

- [RPC mode](rpc.md)
