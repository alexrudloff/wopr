package coding

import (
	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
)

// Background shells: the session's bash tools share one registry of the jobs
// they leave running, which the TUI lists below the prompt.

// withBackgroundShells points every bash tool in list at registry.
func withBackgroundShells(list []agent.AgentTool, registry *tools.BackgroundShells) {
	for _, tool := range list {
		if bash, ok := tool.(*tools.BashTool); ok {
			bash.Background = registry
		}
	}
}

// BackgroundShells returns the session's background jobs.
func (s *Session) BackgroundShells() *tools.BackgroundShells { return s.bgShells }
