// Package compaction: core compaction logic.
//
// All functions are pure except Compact (one LLM call via SimpleCompleter).
package compaction

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"uuid"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent"
)

// ─── Types ────────────────────────────────────────────────────────────────────

// CompactionDetails is stored in a CompactionEntry.Details for file tracking
// across compactions.
type CompactionDetails struct {
	ReadFiles     []string `json:"readFiles"`
	ModifiedFiles []string `json:"modifiedFiles"`
}

// CompactionSettings controls when and how compaction runs. Zero sizes
// scale with the model's window (see ForModel).
type CompactionSettings struct {
	Enabled          bool `json:"enabled"`
	ReserveTokens    int  `json:"reserveTokens"`    // tokens reserved for response; default ai.ContextReserve
	KeepRecentTokens int  `json:"keepRecentTokens"` // tokens to keep from recent history; default a fifth of the window, at most 20000
	// MaxContextTokens compacts once the context passes it even when the
	// window is larger; 0 leaves the window alone to decide.
	MaxContextTokens int `json:"maxContextTokens"`
}

// DefaultCompactionSettings are the compaction defaults.
var DefaultCompactionSettings = CompactionSettings{Enabled: true}

// maxKeepRecentTokens caps the default kept tail on large windows.
const maxKeepRecentTokens = 20000

// summaryBudget is the most a compaction summary can take: the history
// summary (8/10 of the reserve) plus a split turn's prefix summary (half).
func summaryBudget(reserve int) int { return reserve*8/10 + reserve/2 }

// ForModel sizes the settings for a model's window. The reserve defaults to
// ai.ContextReserve and the kept tail to a fifth of the window, at most
// 20000. Set values are clamped so a compaction always fits: the summary,
// the kept tail, and the reserve together stay within the window, which
// also leaves the compacted conversation under the next trigger.
func (s CompactionSettings) ForModel(window, maxOutput int) CompactionSettings {
	if window <= 0 {
		window = ai.DefaultContextWindow
	}
	reserve := s.ReserveTokens
	if reserve <= 0 {
		reserve = ai.ContextReserve(window, maxOutput)
	}
	reserve = min(reserve, window/4)
	keep := s.KeepRecentTokens
	if keep <= 0 {
		keep = min(window/5, maxKeepRecentTokens)
	}
	keep = max(0, min(keep, window-reserve-summaryBudget(reserve)))
	s.ReserveTokens, s.KeepRecentTokens = reserve, keep
	return s
}

// CutPointResult is where a compaction cuts the projected entries.
type CutPointResult struct {
	// FirstKeptEntryIndex is the index in entries[] of the first entry to keep.
	FirstKeptEntryIndex int
	// TurnStartIndex is the index of the user/bash message starting the split
	// turn, or -1 if not splitting.
	TurnStartIndex int
	// IsSplitTurn is true when the cut lands in the middle of a turn.
	IsSplitTurn bool
}

// CompactionPreparation is the output of PrepareCompaction.
type CompactionPreparation struct {
	FirstKeptEntryID    string
	MessagesToSummarize []agent.AgentMessage
	TurnPrefixMessages  []agent.AgentMessage
	IsSplitTurn         bool
	TokensBefore        int
	PreviousSummary     string
	FileOps             FileOperations
	Settings            CompactionSettings
}

// CompactionResult is the output of Compact.
type CompactionResult struct {
	Summary          string
	FirstKeptEntryID string
	TokensBefore     int
	Details          CompactionDetails
	// Usage is the combined usage of the summarization LLM call(s), if the
	// completer reported it.
	Usage *ai.Usage
}

// combineUsage sums two usage snapshots for the split-turn case (history +
// turn-prefix summaries run as two calls).
func combineUsage(a, b *ai.Usage) *ai.Usage {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	optionalSum := func(first, second *int) *int {
		if first == nil && second == nil {
			return nil
		}
		value := 0
		if first != nil {
			value += *first
		}
		if second != nil {
			value += *second
		}
		return &value
	}
	return &ai.Usage{
		Input:        a.Input + b.Input,
		Output:       a.Output + b.Output,
		Reasoning:    optionalSum(a.Reasoning, b.Reasoning),
		CacheRead:    a.CacheRead + b.CacheRead,
		CacheWrite:   a.CacheWrite + b.CacheWrite,
		CacheWrite1h: optionalSum(a.CacheWrite1h, b.CacheWrite1h),
		TotalTokens:  a.TotalTokens + b.TotalTokens,
		Cost: ai.UsageCost{
			Input: a.Cost.Input + b.Cost.Input, Output: a.Cost.Output + b.Cost.Output,
			CacheRead: a.Cost.CacheRead + b.Cost.CacheRead, CacheWrite: a.Cost.CacheWrite + b.Cost.CacheWrite,
			Total: a.Cost.Total + b.Cost.Total,
		},
	}
}

// SimpleCompleter is a minimal interface for LLM calls used by Compact.
// Allows test injection without live LLM calls. options are the request
// options completeSummarization built.
type SimpleCompleter interface {
	CompleteSimple(ctx context.Context, model *ai.Model, systemPrompt string, messages []agent.AgentMessage, options ai.StreamOptions) (string, *ai.Usage, error)
}

// StreamFn is an optional summarization path used instead of CompleteSimple.
type StreamFn func(ctx context.Context, model *ai.Model, systemPrompt string, messages []agent.AgentMessage, options ai.StreamOptions) (string, *ai.Usage, error)

// ─── Summarization Prompts ────────────────────────────────────────────────────

// SUMMARIZATION_PROMPT is the prompt for initial context summarization.
const SUMMARIZATION_PROMPT = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

// UPDATE_SUMMARIZATION_PROMPT is the prompt used when updating an existing summary.
const UPDATE_SUMMARIZATION_PROMPT = `The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

Update the existing structured summary with new information. RULES:
- PRESERVE all existing information from the previous summary
- ADD new progress, decisions, and context from the new messages
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed
- UPDATE "Next Steps" based on what was accomplished
- PRESERVE exact file paths, function names, and error messages
- If something is no longer relevant, you may remove it

Use this EXACT format:

## Goal
[Preserve existing goals, add new ones if the task expanded]

## Constraints & Preferences
- [Preserve existing, add new ones discovered]

## Progress
### Done
- [x] [Include previously done items AND newly completed items]

### In Progress
- [ ] [Current work - update based on progress]

### Blocked
- [Current blockers - remove if resolved]

## Key Decisions
- **[Decision]**: [Brief rationale] (preserve all previous, add new)

## Next Steps
1. [Update based on current state]

## Critical Context
- [Preserve important context, add new if needed]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

// turnPrefixSummarizationPrompt instructs the summary of a split user-message
// span's prefix.
const turnPrefixSummarizationPrompt = `The messages above are earlier context from an ongoing conversation. Later messages are stored separately and do not need to be reconstructed.

Create a concise checkpoint of the user's request and the progress shown above. This checkpoint will be placed before the later messages so the conversation can continue with the necessary context.

## Original Request
[What did the user ask for?]

## Progress So Far
- [Key decisions and work completed in these messages]

## Context Needed to Continue
- [Information from these messages needed to understand the later work]

Only summarize information explicitly present above. Do not infer or recreate later messages.`

// ─── Token Calculation ────────────────────────────────────────────────────────

// ShouldCompact reports whether compaction should trigger. s must be sized
// for the model (ForModel).
func ShouldCompact(contextTokens, contextWindow int, s CompactionSettings) bool {
	if !s.Enabled {
		return false
	}
	if contextWindow <= 0 {
		contextWindow = ai.DefaultContextWindow
	}
	threshold := contextWindow - s.ReserveTokens
	if s.MaxContextTokens > 0 {
		threshold = min(threshold, s.MaxContextTokens)
	}
	return contextTokens > threshold
}

// ─── Cut Point Detection ──────────────────────────────────────────────────────

// isCutPointMessage reports whether a context message may start the kept
// suffix. Tool results never can: they must
// follow their tool call.
func isCutPointMessage(message agent.AgentMessage) bool {
	switch message.Role() {
	case agent.RoleUser, agent.RoleAssistant, agent.RoleBashExecution, agent.RoleCustom,
		agent.RoleBranchSummary, agent.RoleCompactionSummary:
		return true
	}
	return false
}

// isTurnStartMessage reports whether a context message starts a user-message
// span.
func isTurnStartMessage(message agent.AgentMessage) bool {
	switch message.Role() {
	case agent.RoleUser, agent.RoleBashExecution, agent.RoleCustom,
		agent.RoleBranchSummary, agent.RoleCompactionSummary:
		return true
	}
	return false
}

// closestCutPointAtOrAfter prefers the closest cut point at or after index. If
// trailing tool results exceed the budget by themselves, the last cut point
// keeps their preceding assistant tool call.
func closestCutPointAtOrAfter(cutPoints []int, index int) int {
	for _, candidate := range cutPoints {
		if candidate >= index {
			return candidate
		}
	}
	return cutPoints[len(cutPoints)-1]
}

// ─── File Operation Extraction ────────────────────────────────────────────────

// extractFileOperations builds FileOperations from messages and the previous
// generated compaction's details.
func extractFileOperations(
	messages []agent.AgentMessage,
	entries []codingagent.SessionEntry,
	prevCompactionIndex int,
) FileOperations {
	ops := NewFileOps()

	if prevCompactionIndex >= 0 {
		var raw struct {
			FromHook bool               `json:"fromHook"`
			Details  *CompactionDetails `json:"details"`
		}
		if err := json.Unmarshal(entries[prevCompactionIndex].Raw(), &raw); err == nil && !raw.FromHook && raw.Details != nil {
			for _, f := range raw.Details.ReadFiles {
				ops.Read[f] = struct{}{}
			}
			for _, f := range raw.Details.ModifiedFiles {
				ops.Edited[f] = struct{}{}
			}
		}
	}

	for _, msg := range messages {
		ExtractFileOpsFromMessage(msg, &ops)
	}

	return ops
}

// ─── Compaction Preparation ───────────────────────────────────────────────────

// getMessagesFromProjectedEntryForCompaction returns an entry's projected
// messages without system messages, which are prompt state rather than
// conversation; the compaction entry carries their replay. The previous
// compaction contributes its summary through PreviousSummary instead.
func getMessagesFromProjectedEntryForCompaction(entry codingagent.ProjectedSessionEntry) []agent.AgentMessage {
	if entry.SourceEntry.Base.Type == "compaction" {
		return nil
	}
	out := make([]agent.AgentMessage, 0, len(entry.Messages))
	for _, message := range entry.Messages {
		if message.Role() != "system" {
			out = append(out, message)
		}
	}
	return out
}

// projectedMessages returns the conversation messages of entries, never nil.
func projectedMessages(entries []codingagent.ProjectedSessionEntry) []agent.AgentMessage {
	out := []agent.AgentMessage{}
	for _, entry := range entries {
		out = append(out, getMessagesFromProjectedEntryForCompaction(entry)...)
	}
	return out
}

// PrepareCompaction selects what a compaction of pathEntries, a root-to-leaf
// branch, summarizes and keeps. It works on
// the canonical session projection, so context edits, omissions, and the
// previous compaction's retained range all apply. It returns nil when the
// branch ends in a compaction or nothing would be summarized.
func PrepareCompaction(
	pathEntries []codingagent.SessionEntry,
	s CompactionSettings,
) *CompactionPreparation {
	if len(pathEntries) > 0 && pathEntries[len(pathEntries)-1].Base.Type == "compaction" {
		return nil
	}

	projection := codingagent.BuildSessionProjection(pathEntries)
	entries := projection.Entries
	// The newest compaction is projected first. Older compaction entries can
	// occur in its retained range, but their projected contribution is empty.
	prevCompactionIndex := slices.IndexFunc(entries, func(entry codingagent.ProjectedSessionEntry) bool {
		return entry.SourceEntry.Base.Type == "compaction" && len(entry.Messages) > 0
	})

	var previousSummary string
	boundaryStart := 0
	if prevCompactionIndex >= 0 {
		var previous codingagent.CompactionEntry
		_ = json.Unmarshal(entries[prevCompactionIndex].SourceEntry.Raw(), &previous)
		previousSummary = previous.Summary
		// The projection has already selected the previous compaction's kept range.
		boundaryStart = prevCompactionIndex + 1
	}
	tokensBefore := EstimateProjectedContextTokens(projection, pathEntries).Tokens
	cutPoint := findProjectedCutPoint(entries, boundaryStart, len(entries), s.KeepRecentTokens)
	if cutPoint.FirstKeptEntryIndex >= len(entries) {
		return nil
	}
	firstKeptEntryID := entries[cutPoint.FirstKeptEntryIndex].SourceEntry.Base.ID
	if firstKeptEntryID == "" {
		return nil
	}
	historyEnd := cutPoint.FirstKeptEntryIndex
	if cutPoint.IsSplitTurn {
		historyEnd = cutPoint.TurnStartIndex
	}

	toSummarize := projectedMessages(entries[boundaryStart:historyEnd])
	turnPrefix := []agent.AgentMessage{}
	if cutPoint.IsSplitTurn {
		turnPrefix = projectedMessages(entries[cutPoint.TurnStartIndex:cutPoint.FirstKeptEntryIndex])
	}
	if len(toSummarize) == 0 && len(turnPrefix) == 0 {
		return nil
	}

	sourceEntries := make([]codingagent.SessionEntry, len(entries))
	for i, entry := range entries {
		sourceEntries[i] = entry.SourceEntry
	}
	fileOps := extractFileOperations(toSummarize, sourceEntries, prevCompactionIndex)
	for _, message := range turnPrefix {
		ExtractFileOpsFromMessage(message, &fileOps)
	}

	return &CompactionPreparation{
		FirstKeptEntryID:    firstKeptEntryID,
		MessagesToSummarize: toSummarize,
		TurnPrefixMessages:  turnPrefix,
		IsSplitTurn:         cutPoint.IsSplitTurn,
		TokensBefore:        tokensBefore,
		PreviousSummary:     previousSummary,
		FileOps:             fileOps,
		Settings:            s,
	}
}

// CompactedTokens estimates a branch's size after compacting it with s:
// the kept tail plus the most the summary can take, never more than now.
// ok is false when nothing would be compacted.
func CompactedTokens(pathEntries []codingagent.SessionEntry, s CompactionSettings) (int, bool) {
	prep := PrepareCompaction(pathEntries, s)
	if prep == nil {
		return 0, false
	}
	kept := prep.TokensBefore - EstimateMessagesTokens(prep.MessagesToSummarize) - EstimateMessagesTokens(prep.TurnPrefixMessages)
	return min(prep.TokensBefore, max(kept, 0)+summaryBudget(s.ReserveTokens)), true
}

// ─── LLM Summarization ────────────────────────────────────────────────────────

// convertToLlm converts context messages to provider messages for a
// summarization prompt; messages not sent to the provider are dropped.
func convertToLlm(msgs []agent.AgentMessage) []ai.Message {
	out := make([]ai.Message, 0, len(msgs))
	for _, m := range msgs {
		if message, ok := m.LLMMessage(); ok {
			out = append(out, message)
		}
	}
	return out
}

// generateSummary calls the LLM to summarize messagesToSummarize.
// Uses UPDATE_SUMMARIZATION_PROMPT when previousSummary is non-empty.
func capMaxTokens(budget int, model *ai.Model) int {
	budget = max(budget, 0)
	if model != nil && model.Capabilities.MaxOutputTokens > 0 {
		return min(budget, model.Capabilities.MaxOutputTokens)
	}
	return budget
}

// summaryRequest is the one-user-message request a summary call sends.
func summaryRequest(text string) []agent.AgentMessage {
	return []agent.AgentMessage{{User: &agent.UserMessage{
		Role:      "user",
		Content:   []ai.UserContentBlock{ai.TextContent{Text: text}},
		Timestamp: time.Now().UnixMilli(),
	}}}
}

// createSummarizationOptions builds the request options for one summary
// call. The thinking level applies only to reasoning-capable models.
func createSummarizationOptions(model *ai.Model, maxTokens int, thinkingLevel ai.ThinkingLevel, sessionID string) ai.StreamOptions {
	options := ai.StreamOptions{MaxTokens: maxTokens, SessionID: sessionID}
	if model != nil && model.Capabilities.MaxThinking != "" {
		options.IsReasoning = true
		if thinkingLevel != "" && thinkingLevel != ai.ThinkingOff {
			options.Thinking = thinkingLevel
		}
	}
	return options
}

// completeSummarization is the shared choke point for every compaction,
// branch-summary, and bug-report summarization call. One-off summaries never
// write prompt cache; callers without a routing session ID get a fresh one,
// reused across retries.
func completeSummarization(
	ctx context.Context,
	model *ai.Model,
	completer SimpleCompleter,
	streamFn StreamFn,
	retry *RetryOptions,
	systemPrompt string,
	messages []agent.AgentMessage,
	options ai.StreamOptions,
) (string, *ai.Usage, error) {
	requestOptions := options
	if model != nil {
		requestOptions.ModelCost = model.CostRates()
	}
	requestOptions.Env = maps.Clone(options.Env)
	if requestOptions.Env == nil {
		requestOptions.Env = ai.ProviderEnv{}
	}
	requestOptions.Env["WOPR_CACHE_RETENTION"] = "none"
	requestOptions.CacheRetention = ai.CacheRetentionNone
	if requestOptions.SessionID == "" {
		requestOptions.SessionID = uuid.NewV7().String()
	}
	return completeSimpleWithRetries(ctx, retry, func() (string, *ai.Usage, error) {
		if streamFn != nil {
			return streamFn(ctx, model, systemPrompt, messages, requestOptions)
		}
		return completer.CompleteSimple(ctx, model, systemPrompt, messages, requestOptions)
	})
}

func generateSummary(
	ctx context.Context,
	messages []agent.AgentMessage,
	previousSummary string,
	reserveTokens int,
	model *ai.Model,
	completer SimpleCompleter,
	streamFn StreamFn,
	customInstructions string,
	thinkingLevel ai.ThinkingLevel,
	retry *RetryOptions,
	sessionID string,
) (string, *ai.Usage, error) {
	basePrompt := SUMMARIZATION_PROMPT
	if previousSummary != "" {
		basePrompt = UPDATE_SUMMARIZATION_PROMPT
	}
	if customInstructions != "" {
		basePrompt += "\n\nAdditional focus: " + customInstructions
	}

	convText := SerializeConversation(convertToLlm(messages))

	var sb strings.Builder
	sb.WriteString("<conversation>\n")
	sb.WriteString(convText)
	sb.WriteString("\n</conversation>\n\n")
	if previousSummary != "" {
		sb.WriteString("<previous-summary>\n")
		sb.WriteString(previousSummary)
		sb.WriteString("\n</previous-summary>\n\n")
	}
	sb.WriteString(basePrompt)
	promptText := sb.String()

	req := summaryRequest(promptText)

	maxTokens := capMaxTokens((8*reserveTokens)/10, model) // see summaryBudget
	options := createSummarizationOptions(model, maxTokens, thinkingLevel, sessionID)
	result, usage, err := completeSummarization(ctx, model, completer, streamFn, retry, SummarizationSystemPrompt, req, options)
	if err != nil {
		return "", nil, summarizationFailure("Summarization", err)
	}
	return result, usage, nil
}

// generateTurnPrefixSummary summarizes the prefix of a split turn.
func generateTurnPrefixSummary(
	ctx context.Context,
	messages []agent.AgentMessage,
	reserveTokens int,
	model *ai.Model,
	completer SimpleCompleter,
	streamFn StreamFn,
	thinkingLevel ai.ThinkingLevel,
	retry *RetryOptions,
	sessionID string,
) (string, *ai.Usage, error) {
	convText := SerializeConversation(convertToLlm(messages))
	promptText := "# Conversation\n" + convText + "\n\n# Instructions\n" + turnPrefixSummarizationPrompt
	req := summaryRequest(promptText)
	maxTokens := capMaxTokens(reserveTokens/2, model) // see summaryBudget
	options := createSummarizationOptions(model, maxTokens, thinkingLevel, sessionID)
	result, usage, err := completeSummarization(ctx, model, completer, streamFn, retry, SummarizationSystemPrompt, req, options)
	if err != nil {
		return "", nil, summarizationFailure("Turn prefix summarization", err)
	}
	return result, usage, nil
}

// ErrSummarizationToolCall rejects a provider response containing a tool call;
// standalone summarization requests never provide executable tools.
var ErrSummarizationToolCall = errors.New("summarization attempted to call a tool")

func summarizationFailure(operation string, err error) error {
	if errors.Is(err, ErrSummarizationToolCall) {
		return fmt.Errorf("%s attempted to call a tool", operation)
	}
	message := err.Error()
	if errors.Is(err, context.Canceled) {
		message = "This operation was aborted"
	}
	return fmt.Errorf("%s failed: %s", operation, message)
}

// SummaryRequestTokens estimates the input of the largest request Compact
// sends for prep: the serialized conversation it summarizes, with tool
// results already cut short, not the full context it replaces. A summarizer
// is chosen by this size, so a context past every model's window still
// compacts. The count is padded by half: the character estimate runs up to
// a third under a provider's count on code and tool output.
func SummaryRequestTokens(prep CompactionPreparation) int {
	history := ai.EstimateTextTokens(SummarizationSystemPrompt + UPDATE_SUMMARIZATION_PROMPT + prep.PreviousSummary + SerializeConversation(convertToLlm(prep.MessagesToSummarize)))
	prefix := ai.EstimateTextTokens(SummarizationSystemPrompt + turnPrefixSummarizationPrompt + SerializeConversation(convertToLlm(prep.TurnPrefixMessages)))
	return max(history, prefix) * 3 / 2
}

// FitSummaryRequest drops the oldest messages to summarize until
// SummaryRequestTokens(*prep) is at most budget, so a summarizer whose window
// can't take the whole conversation summarizes its newest part.
func FitSummaryRequest(prep *CompactionPreparation, budget int) {
	if SummaryRequestTokens(*prep) <= budget {
		return
	}
	used := SummaryRequestTokens(CompactionPreparation{PreviousSummary: prep.PreviousSummary, TurnPrefixMessages: prep.TurnPrefixMessages})
	start := len(prep.MessagesToSummarize)
	for start > 0 {
		cost := ai.EstimateTextTokens(SerializeConversation(convertToLlm(prep.MessagesToSummarize[start-1:start]))) * 3 / 2
		if used+cost > budget {
			break
		}
		used += cost
		start--
	}
	prep.MessagesToSummarize = prep.MessagesToSummarize[start:]
	// Per-message sums miss the separators the whole serialization adds.
	for len(prep.MessagesToSummarize) > 0 && SummaryRequestTokens(*prep) > budget {
		prep.MessagesToSummarize = prep.MessagesToSummarize[1:]
	}
}

// ─── Main Compaction Function ─────────────────────────────────────────────────

// Compact generates summaries for compaction using PrepareCompaction output.
// thinkingLevel applies to reasoning-capable models; an empty sessionID gives
// each summary request a fresh routing ID.
func Compact(
	ctx context.Context,
	prep CompactionPreparation,
	model *ai.Model,
	completer SimpleCompleter,
	streamFn StreamFn,
	customInstructions string,
	thinkingLevel ai.ThinkingLevel,
	retry *RetryOptions,
	sessionID string,
) (CompactionResult, error) {
	var summary string
	var usage *ai.Usage

	if prep.IsSplitTurn && len(prep.TurnPrefixMessages) > 0 {
		historySummary := cmp.Or(prep.PreviousSummary, "No prior history.")
		var historyUsage *ai.Usage
		if len(prep.MessagesToSummarize) > 0 {
			var err error
			historySummary, historyUsage, err = generateSummary(ctx, prep.MessagesToSummarize, prep.PreviousSummary, prep.Settings.ReserveTokens, model, completer, streamFn, customInstructions, thinkingLevel, retry, sessionID)
			if err != nil {
				return CompactionResult{}, err
			}
		}

		prefixSummary, prefixUsage, err := generateTurnPrefixSummary(ctx, prep.TurnPrefixMessages, prep.Settings.ReserveTokens, model, completer, streamFn, thinkingLevel, retry, sessionID)
		if err != nil {
			return CompactionResult{}, err
		}
		summary = historySummary + "\n\n---\n\n**Turn Context (split turn):**\n\n" + prefixSummary
		usage = combineUsage(historyUsage, prefixUsage)
	} else {
		var err error
		summary, usage, err = generateSummary(ctx, prep.MessagesToSummarize, prep.PreviousSummary, prep.Settings.ReserveTokens, model, completer, streamFn, customInstructions, thinkingLevel, retry, sessionID)
		if err != nil {
			return CompactionResult{}, err
		}
	}

	readFiles, modifiedFiles := ComputeFileLists(prep.FileOps)
	summary += FormatFileOperations(readFiles, modifiedFiles)

	if prep.FirstKeptEntryID == "" {
		return CompactionResult{}, errors.New("First kept entry has no UUID - session may need migration")
	}

	return CompactionResult{
		Summary:          summary,
		FirstKeptEntryID: prep.FirstKeptEntryID,
		TokensBefore:     prep.TokensBefore,
		Usage:            usage,
		Details: CompactionDetails{
			ReadFiles:     readFiles,
			ModifiedFiles: modifiedFiles,
		},
	}, nil
}
