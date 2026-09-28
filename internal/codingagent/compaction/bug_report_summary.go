package compaction

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// bugSummarySystemPrompt and bugSummaryInstructions prompt the bug-report
// summary.
const bugSummarySystemPrompt = `You are helping a user file a bug report about wopr, the coding agent they are talking to. You will be shown the conversation transcript. Write a report for the wopr developers describing what the user was doing and what went wrong.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the report.`

const bugSummaryInstructions = `Write the bug report in Markdown with these sections:

## What the user was doing
One short paragraph.

## What went wrong
Concrete description of the failure: wrong output, errors, hangs, tool failures, unexpected behavior. Quote error messages and tool output verbatim where they exist.

## Steps to reproduce
Numbered list, as specific as the transcript allows.

## Relevant details
Tool calls involved, files touched, model behavior, anything else that helps a developer reproduce or locate the problem.

Do not include file contents, secrets, or credentials from the transcript; refer to files by path only. Keep the report factual and concise.`

const (
	bugSummaryMaxTokens = 4096
)

// GenerateBugReportSummaryOptions configures GenerateBugReportSummary.
type GenerateBugReportSummaryOptions struct {
	Messages  []agent.AgentMessage
	Hint      string
	Model     *ai.Model
	Completer SimpleCompleter
	StreamFn  StreamFn
	Retry     *RetryOptions
	// ThinkingLevel applies to reasoning-capable models.
	ThinkingLevel ai.ThinkingLevel
	// SessionID is the routing session ID forwarded without prompt caching.
	SessionID string
}

// selectBugReportMessages keeps the newest messages that fit tokenBudget,
// always keeping at least the last one, in original order.
func selectBugReportMessages(messages []agent.AgentMessage, tokenBudget int) []agent.AgentMessage {
	start := len(messages)
	tokens := 0
	for start > 0 {
		next := EstimateTokens(messages[start-1])
		if start < len(messages) && tokens+next > tokenBudget {
			break
		}
		tokens += next
		start--
	}
	return messages[start:]
}

// GenerateBugReportSummary asks the session model for a report when the user
// does not share the transcript.
func GenerateBugReportSummary(ctx context.Context, opts GenerateBugReportSummaryOptions) (string, error) {
	model := opts.Model
	if model == nil {
		return "", errors.New("No model selected")
	}
	contextWindow := model.Capabilities.ContextWindow
	if contextWindow <= 0 {
		contextWindow = ai.DefaultContextWindow
	}
	selected := selectBugReportMessages(opts.Messages, contextWindow*6/10)
	var parts []string
	if len(selected) < len(opts.Messages) {
		parts = append(parts, fmt.Sprintf("Note: only the last %d of %d messages are shown.", len(selected), len(opts.Messages)))
	}
	parts = append(parts, "<conversation>\n"+SerializeConversation(convertToLlm(selected))+"\n</conversation>")
	if hint := strings.TrimSpace(opts.Hint); hint != "" {
		parts = append(parts, "<user-report>\n"+hint+"\n</user-report>")
	}
	parts = append(parts, bugSummaryInstructions)
	request := summaryRequest(strings.Join(parts, "\n\n"))
	maxTokens := capMaxTokens(bugSummaryMaxTokens, model)
	options := createSummarizationOptions(model, maxTokens, opts.ThinkingLevel, opts.SessionID)
	text, _, err := completeSummarization(ctx, model, opts.Completer, opts.StreamFn, opts.Retry, bugSummarySystemPrompt, request, options)
	if ctx.Err() != nil {
		return "", errors.New("Bug report summary was cancelled")
	}
	if err != nil {
		return "", summarizationFailure("Bug report summary", err)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("Bug report summary was empty")
	}
	return text, nil
}
