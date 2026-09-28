package codingagent

import (
	"context"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// ModelMutationOptions controls whether a Session mutation also saves a global default.
type ModelMutationOptions struct {
	Persist bool
}

// InteractiveSessionHandle is the contract InteractiveMode uses to
// access a pre-constructed coding session. *coding.Session in the
// public SDK package satisfies this: its Agent() and Inner() methods
// have the matching signatures.
//
// We use an interface (rather than importing coding/.Session
// directly) to break the import cycle: coding/ already imports
// internal/codingagent for its on-disk Session type, so
// internal/codingagent cannot import coding/.
//
// main.go (which imports both packages) does the bridging:
//
//	sess, _ := coding.NewSession(svcs, opts)
//	m := codingagent.NewInteractiveMode(codingagent.InteractiveOptions{
//	    SessionHandle: sess,  // *coding.Session implements this interface
//	    ...
//	})
type InteractiveSessionHandle interface {
	// Agent returns the underlying agent loop.
	Agent() *agent.Agent
	// Inner returns the on-disk session. The local-package type
	// reference (*Session) is the same as *icodingagent.Session
	// from coding/'s point of view.
	Inner() *Session
	// Events returns the agent's streaming event channel. Used by
	// processAgentEvents to drive live UI updates.
	Events() <-chan agent.AgentEvent
	// SetModel swaps the active LLM model mid-session and persists a
	// model_change audit entry.
	SetModel(*ai.Model, ...ModelMutationOptions) error
	// SetThinkingLevel applies and records reasoning without changing defaults unless Persist is set.
	SetThinkingLevel(ai.ThinkingLevel, ...ModelMutationOptions) error
	// StreamModel starts a mode-independent model operation through the
	// Session-owned runtime.
	StreamModel(context.Context, *ai.Model, ai.Context, ai.StreamOptions) *ai.AssistantMessageEventStream
	// AbortCompaction cancels an in-flight Compact() call. Safe to
	// call when no compaction is running (no-op).
	AbortCompaction()
	// CheckPromptCompaction runs prompt()'s compaction check before a new
	// user message starts a run.
	CheckPromptCompaction(ctx context.Context) error
	// RunAgentPrompt runs one agent run to settlement: start seeds it, then
	// automatic retry, overflow and length recovery, threshold compaction,
	// and queued input continue it until nothing asks for another run or ctx
	// is cancelled. The Events consumer must acknowledge the barriers it
	// waits on (see eventBarrier).
	RunAgentPrompt(ctx context.Context, start func(context.Context) ([]agent.AgentMessage, error)) ([]agent.AgentMessage, error)
	// AbortRetry cancels a pending automatic-retry delay without aborting
	// the run.
	AbortRetry()
	// AbortBranchSummary cancels an in-flight branch summarization. Safe
	// to call when no summarization is running (no-op).
	AbortBranchSummary()
	// ReplaceInner swaps the underlying on-disk session. Used by /resume
	// to point the SessionHandle at a newly loaded session so tree
	// navigation and compaction operate on the correct entries.
	ReplaceInner(sess *Session)
	// NavigateTree forks the session to targetID with optional branch
	// summarization. Returns NavigateTreeResult.
	NavigateTreeHandle(ctx context.Context, targetID string, summarize bool, customInstructions string) (NavigateTreeResult, error)
	// CacheWarmingStatus reports the Session's cache warmer, or nil when it
	// has none.
	CacheWarmingStatus() *CacheWarmingStatus
	// SetCacheWarmingMode persists the mode and reconciles active warming.
	SetCacheWarmingMode(CacheWarmingMode) error
	// OnAgentSettled tells the cache warmer that an agent run settled.
	OnAgentSettled()
}

// eventBarrier is the Session's FlushEvents marker on the Events channel.
// The owner loop acknowledges it once it has handled every earlier event,
// which lets RunAgentPrompt wait for the UI to handle every earlier event.
type eventBarrier interface {
	AcknowledgeEvent()
}

// RouterController is implemented by a session handle that owns a model
// router. The /router command uses it when present.
type RouterController interface {
	// RouterCommand runs one /router subcommand and returns markdown.
	RouterCommand(args string) (string, error)
}

// MCPController is implemented by a session handle that owns MCP servers.
// The /mcp command uses it when present.
type MCPController interface {
	// MCPCommand runs one /mcp subcommand and returns markdown.
	MCPCommand(args string) (string, error)
}
