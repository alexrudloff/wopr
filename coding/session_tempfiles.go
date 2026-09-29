package coding

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/internal/codingagent/tempfiles"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
)

// initTempFiles tracks the temp files this session's tools create, unless
// the cleanupTempFiles setting is off.
func (s *Session) initTempFiles() {
	if !s.services.SettingsManager().Get().GetCleanupTempFiles() {
		return
	}
	t := tempfiles.Process(s.services.AgentDir())
	s.temp = t
	s.agent.AddBeforeToolCallHook(func(_ context.Context, callID, toolName string, _ json.RawMessage) agent.ToolCallHookResult {
		if toolName == "bash" {
			t.BeforeCommand(callID)
		}
		return agent.ToolCallHookResult{}
	})
	s.agent.AddAfterToolCallHook(func(_ context.Context, callID, toolName string, args json.RawMessage, result agent.AgentToolResult) agent.AfterToolCallResult {
		switch toolName {
		case "bash":
			t.AfterCommand(callID, s.ID(), string(args)+"\n"+result.Content)
			if details, ok := result.Details.(*tools.BashDetails); ok && details != nil && details.FullOutputPath != "" {
				// The tool's own full-output log.
				t.RecordPath(s.ID(), details.FullOutputPath)
			}
		case "write", "edit":
			var in struct {
				Path string `json:"path"`
			}
			if json.Unmarshal(args, &in) == nil && in.Path != "" {
				path := in.Path
				if !filepath.IsAbs(path) {
					path = filepath.Join(s.services.CWD(), path)
				}
				t.RecordPath(s.ID(), path)
			}
		}
		return agent.AfterToolCallResult{}
	})
}

// recordBashLog records the full-output log a user shell command (!cmd)
// left in the temp directory.
func (s *Session) recordBashLog(path string) {
	if s.temp != nil && path != "" {
		s.temp.RecordPath(s.ID(), path)
	}
}

// sessionTempDir is the TMPDIR for this session's shell commands, or "" when
// temp files aren't tracked.
func (s *Session) sessionTempDir() string {
	if s.temp == nil {
		return ""
	}
	tempfiles.Use(s.ID())
	return tempfiles.SessionDir(s.ID())
}

// cleanTempAfterCompaction deletes the session's temp files that neither
// the summary nor the kept messages mention.
func (s *Session) cleanTempAfterCompaction() {
	if s.temp == nil {
		return
	}
	text, err := json.Marshal(s.agent.Messages())
	if err != nil {
		return
	}
	s.noteTempCleaned(s.temp.CleanUnreferenced(s.ID(), string(text)))
}

func (s *Session) noteTempCleaned(n int) {
	if n > 0 {
		s.emitEvent(agent.TempFilesCleanedEvent{Count: n})
	}
}
