# AI runtime reliability

## Read first

- `../docs/project/CONTEXT.md`
- `types.go` and `event_stream.go` in this package
- the provider you are changing

## Contract

A Provider receives a normalized Transcript. Public Message, content, Usage, and event shapes are stable: Sessions and the JSON and RPC protocols persist and expose them. An Event Stream preserves event order and has one terminal result. Provider setup can return an immediate error. Runtime, provider, authentication, and cancellation failures after stream creation terminate the stream.

## Failure modes

| Failure | Detection | Required result |
|---|---|---|
| Invalid JSON enters trusted provider code | Supply a non-JSON value or non-finite number | Reject it at the public boundary |
| Result depends on event consumption | Request Result without iteration after more than the former queue bound | Result completes |
| Iteration stops while waiting | Cancel the iterator context | No waiter or goroutine remains |
| Cancellation races with termination | Run concurrent Push, iteration, and Result under the race detector | One terminal result |
| A provider emits an invalid sequence | Exercise setup, start, block, delta, end, and terminal paths | Emit start, blocks, and one terminal event in order |
| Replay metadata crosses provider or model identity | Replay signed content through another model | Drop or transform it so no provider receives another provider's signature |
| Consumed events remain retained | Drain a long stream | Release consumed queue entries |
| A transport consumer stalls | Stop reading subprocess frames | Apply bounded transport backpressure and cancellation |

## Evidence

Keep wire-contract tests (request building, stream parsing, tool call pairing, OAuth refresh) for the providers the router uses by default, a handful of fixture cases each. Other providers are covered by the build. Add a test only when a regression would cost money, leak a credential, or lose data. Run `go test -race ./ai`.
