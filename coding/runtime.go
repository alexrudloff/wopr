package coding

import (
	"fmt"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// SessionStartOptions configures a Session started with StartSession.
type SessionStartOptions struct {
	// Model is the LLM the agent calls. Required.
	Model *ai.Model

	// ThinkingLevel, when set, overrides the configured default thinking
	// level and a resumed Session's saved level (--thinking). It is clamped
	// to the model.
	ThinkingLevel string

	// SystemPrompt is the system prompt.
	SystemPrompt string

	// SystemPromptSections carries the caller-built structured prompt.
	SystemPromptSections ai.OrderedSections

	// AllowedTools restricts which tools may execute.
	AllowedTools map[string]struct{}

	// PinModel pins Model as the orchestrator when routing is on: the user
	// chose it (wopr --model), so the router routes only subagents.
	PinModel bool

	// ActiveBuiltinTools, when non-nil, restricts which built-in coding tools
	// are active (extra tools are unaffected). nil means all
	// built-in tools. The CLI default is [read, bash, edit, write], so grep/find/ls are
	// registered but inactive unless requested via --tools.
	ActiveBuiltinTools map[string]struct{}

	// ExcludedTools is a denylist of tool names removed from the final tool
	// set after allow/active filtering. Gates built-in and caller tools alike.
	ExcludedTools map[string]struct{}

	// NoTools selects which tools to drop when no explicit allowlist is provided.
	//   - "all": expose no tools
	//   - "builtin": omit built-in coding tools but keep extra tools
	NoTools string

	// SkipBuiltinTools, when true, omits the built-in coding tools while still
	// allowing extra tools.
	SkipBuiltinTools bool

	// BeforeToolCall hooks run before each tool call and may block.
	BeforeToolCall []agent.BeforeToolCallHook

	// ExtraTools are caller-supplied tools added to the built-in set.
	ExtraTools []agent.AgentTool

	// ResumePath, when non-empty, loads an existing JSONL.
	ResumePath string

	// SessionDir overrides the on-disk session directory for new sessions
	// and session lookups such as Resume/Continue.
	SessionDir string

	// SessionID specifies an exact session ID for new sessions (--session-id).
	SessionID string

	// NoSession disables session persistence (ephemeral mode, --no-session).
	NoSession bool
}

// StartSession starts a Session against svcs: a fresh one, or the one at
// opts.ResumePath.
func StartSession(svcs *Services, opts SessionStartOptions) (*Session, error) {
	if svcs == nil {
		return nil, fmt.Errorf("coding: StartSession: Services is required")
	}
	var tools []agent.AgentTool
	if len(opts.ExtraTools) > 0 {
		tools = append(tools, opts.ExtraTools...)
	}

	var allowedTools map[string]struct{}
	if opts.AllowedTools != nil {
		allowedTools = opts.AllowedTools
	} else if opts.NoTools == "all" {
		allowedTools = map[string]struct{}{}
	}
	skipBuiltinTools := opts.SkipBuiltinTools || opts.NoTools == "builtin"

	return NewSession(svcs, SessionOptions{
		Model:                opts.Model,
		ThinkingLevel:        opts.ThinkingLevel,
		SystemPrompt:         opts.SystemPrompt,
		SystemPromptSections: opts.SystemPromptSections,
		Tools:                tools,
		AllowedTools:         allowedTools,
		ActiveBuiltinTools:   opts.ActiveBuiltinTools,
		ExcludedTools:        opts.ExcludedTools,
		SkipBuiltinTools:     skipBuiltinTools,
		PinModel:             opts.PinModel,
		BeforeToolCall:       opts.BeforeToolCall,
		ResumePath:           opts.ResumePath,
		SessionDir:           opts.SessionDir,
		SessionID:            opts.SessionID,
		NoSession:            opts.NoSession,
		Transport:            ai.Transport(svcs.Settings().Transport),
	})
}
