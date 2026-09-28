package coding

import "github.com/alexrudloff/wopr/internal/codingagent/tools"

// initBashArchive lets the bash tool archive the raw output of a result it
// compacted, so the model can read it back with obs_recall. Archiving needs
// ObservationPack; without it the compacted result points at `| cat`.
func (s *Session) initBashArchive() {
	for _, tool := range s.tools {
		if bash, ok := tool.(*tools.BashTool); ok && bash.Compact {
			bash.Archive = s.archiveBashOutput
		}
	}
}

func (s *Session) archiveBashOutput(key, text string) string {
	if s.efficiency == nil || s.efficiency.pack == nil {
		return ""
	}
	id, err := s.efficiency.pack.Archive("bash", key, text)
	if err != nil {
		return ""
	}
	return id
}
