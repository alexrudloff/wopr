package coding

import (
	"slices"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/internal/codingagent/askuser"
)

// initAskUser prepares ask_user. It joins the session's tools only while a
// frontend with a user has installed an asker, so print, JSON, and RPC
// modes never offer it and pay no request tokens for it; subagents get their
// own tool set. A caller tool of the same name wins, and --tools and
// --exclude-tools gate it like any tool.
func (s *Session) initAskUser(opts SessionOptions) {
	has := func(set map[string]struct{}) bool { _, ok := set[askuser.Name]; return ok }
	ours := func(t agent.AgentTool) bool { _, ok := t.(*askuser.Tool); return ok }
	// A clone carries its source's ask_user; this session installs its own.
	s.tools = slices.DeleteFunc(s.tools, ours)
	s.agent.SetTools(slices.DeleteFunc(slices.Clone(s.agent.Tools()), ours))
	taken := slices.ContainsFunc(s.tools, func(t agent.AgentTool) bool { return t.Name() == askuser.Name })
	s.askAllowed = !taken && (opts.AllowedTools == nil || has(opts.AllowedTools)) && !has(opts.ExcludedTools)
	s.askTool = &askuser.Tool{Ask: func() askuser.Asker {
		if p := s.asker.Load(); p != nil {
			return *p
		}
		return nil
	}}
}

// SetUserAsker installs the frontend's way to ask the user a question and
// turns ask_user on; nil turns it off. The change applies from the next
// provider request.
func (s *Session) SetUserAsker(ask askuser.Asker) {
	if ask == nil {
		s.asker.Store(nil)
	} else {
		s.asker.Store(&ask)
	}
	if !s.askAllowed {
		return
	}
	isAsk := func(t agent.AgentTool) bool { return t == agent.AgentTool(s.askTool) }
	if ask == nil {
		s.tools = slices.DeleteFunc(s.tools, isAsk)
		s.agent.SetTools(slices.DeleteFunc(slices.Clone(s.agent.Tools()), isAsk))
		return
	}
	if !slices.ContainsFunc(s.tools, isAsk) {
		s.tools = append(s.tools, s.askTool)
	}
	if active := s.agent.Tools(); !slices.ContainsFunc(active, isAsk) {
		s.agent.SetTools(append(slices.Clone(active), s.askTool))
	}
}
