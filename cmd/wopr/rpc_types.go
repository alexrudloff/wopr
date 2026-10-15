// Go types for the RPC protocol.
// Commands arrive as JSON lines on stdin; responses and events are emitted
// as JSON lines on stdout. Each object has a "type" discriminant field.

package main

import (
	"bytes"
	"encoding/json"
	"io"

	codinghost "github.com/alexrudloff/wopr/coding/host"
)

// Aliases keep the protocol DTOs nameable in package main, where
// rpcCommandCatalog assembles them. A type alias is identical to its target, so
// cmd/wopr/llama_test.go builds the same literals it builds today.
type (
	RPCSourceInfo   = codinghost.RPCSourceInfo
	RPCSlashCommand = codinghost.RPCSlashCommand
)

// writeJSONLine serialises v to a JSON line (no HTML escaping) and writes
// it followed by a newline. A broken writer is silently ignored because a
// dead stdout means the client has gone away.
//
// Uses json.NewEncoder + SetEscapeHTML(false) per AGENTS.md rule: never use
// json.Marshal for strings that appear in display/wire output containing
// shell operators (&&, <, >, etc.).
func writeJSONLine(w io.Writer, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return // encoding should never fail for our well-typed structs
	}
	_, _ = w.Write(buf.Bytes()) // Encode already appends '\n'
}
