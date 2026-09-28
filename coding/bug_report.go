package coding

import (
	"context"

	"github.com/alexrudloff/wopr/internal/codingagent/compaction"
)

// SummarizeForBugReport asks the session model for a bug report summary of the
// current conversation. /bug calls it only after the user chose a summary
// instead of attaching the transcript.
func (s *Session) SummarizeForBugReport(ctx context.Context, hint string) (string, error) {
	return compaction.GenerateBugReportSummary(ctx, compaction.GenerateBugReportSummaryOptions{
		Messages:  s.Messages(),
		Hint:      hint,
		Model:     s.Model(),
		Completer: s.resolveCompleter(),
		StreamFn:  s.streamFn,
		Retry:     s.summarizationRetryOptions("bug-report", "manual"),
		// Forward the session's thinking level and routing ID.
		ThinkingLevel: s.ThinkingLevel(),
		SessionID:     s.ID(),
	})
}
