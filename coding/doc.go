// Package coding is wopr's session core: the services, sessions, routing,
// and efficiency wiring that the interactive, print, and RPC modes run on.
//
// [Services] owns shared configuration and credentials: auth storage, the
// model registry, and settings. Construct it once per process (or per CWD).
//
// [Session] is one live conversation. It wraps the agent loop, the on-disk
// JSONL transcript, and the slash command registry, and exposes Send for
// prompts, Events for streaming, and the session-management methods (Fork,
// Clone, Tree, SetName). Resuming (SessionStartOptions.ResumePath) rebuilds
// the agent's message history from the transcript.
//
// Session.Fork moves the conversation leaf to an earlier entry; the next Send
// branches from there. Session.Clone snapshots the current path-to-leaf as a
// new transcript and returns an independent Session.
//
// Session.Events returns a buffered channel of [agent.AgentEvent] values.
// Subscribe before calling Send; events carry text deltas, tool calls, tool
// results, and end-of-turn markers.
package coding
