package compaction

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent"
)

// Estimate and compaction-preparation cases for context edits. The
// projection-only cases live in internal/codingagent/session_manager_context_edit_test.go.

func editAssistant(text string, usage ai.Usage) agent.AgentMessage {
	return agent.AgentMessage{Assistant: &agent.AssistantMessage{
		Role:       agent.RoleAssistant,
		Content:    []ai.AssistantContentBlock{ai.TextContent{Text: text}},
		API:        "faux",
		Provider:   "faux",
		ModelID:    "faux",
		Usage:      &usage,
		StopReason: ai.StopReasonStop,
		Timestamp:  time.Now().UnixMilli(),
	}}
}

var defaultEditUsage = ai.Usage{Input: 10, Output: 1, TotalTokens: 11}

func editUser(text string) agent.AgentMessage {
	return agent.AgentMessage{User: &agent.UserMessage{
		Role:      agent.RoleUser,
		Content:   []ai.UserContentBlock{ai.TextContent{Text: text}},
		Timestamp: time.Now().UnixMilli(),
	}}
}

type editSession struct {
	t    *testing.T
	sess *codingagent.Session
}

func newEditSession(t *testing.T) *editSession {
	t.Helper()
	return &editSession{t: t, sess: codingagent.NewSession("s", t.TempDir())}
}

func (e *editSession) message(message agent.AgentMessage) string {
	e.t.Helper()
	id, err := e.sess.AppendMessage(message)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *editSession) edit(targetID string, replacement any) {
	e.t.Helper()
	var edit *codingagent.ContextEditReplacement
	if replacement != nil {
		raw, err := json.Marshal(replacement)
		if err != nil {
			e.t.Fatal(err)
		}
		edit = &codingagent.ContextEditReplacement{Content: raw}
	}
	if _, err := e.sess.AppendContextEdit(targetID, edit); err != nil {
		e.t.Fatal(err)
	}
}

func (e *editSession) compact(summary, firstKept string, tokensBefore int) {
	e.t.Helper()
	if _, err := e.sess.AppendCompaction(summary, firstKept, tokensBefore, nil, false, nil); err != nil {
		e.t.Fatal(err)
	}
}

func (e *editSession) branch() []codingagent.SessionEntry { return e.sess.Branch(*e.sess.LeafID()) }

func (e *editSession) estimate() ai.ContextUsageEstimate {
	return EstimateProjectedContextTokens(e.sess.BuildSessionProjection(), e.branch())
}

func (e *editSession) prepare() *CompactionPreparation {
	settings := DefaultCompactionSettings
	settings.KeepRecentTokens = 1
	return PrepareCompaction(e.branch(), settings)
}

func messagesJSON(t *testing.T, messages []agent.AgentMessage) string {
	t.Helper()
	raw, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSessionContextEditUsesOnlyTheNewestSummaryWhenARepeatedCompactionRetainsEntriesBeforeTheOlderCompaction(t *testing.T) {
	e := newEditSession(t)
	e.message(editUser("summarized first"))
	retainedID := e.message(editUser("retained"))
	e.compact("first summary", retainedID, 100)
	e.message(editAssistant("after first compaction", defaultEditUsage))
	e.compact("second summary", retainedID, 80)
	e.message(editUser(strings.Repeat("new tail ", 100)))

	var summaries []string
	for _, message := range e.sess.BuildSessionProjection().Messages {
		if summary, ok := message.Custom["summary"].(string); ok {
			summaries = append(summaries, summary)
		}
	}
	if !slices.Equal(summaries, []string{"second summary"}) {
		t.Fatalf("summaries = %v", summaries)
	}
	if prep := e.prepare(); prep == nil || prep.PreviousSummary != "second summary" {
		t.Fatalf("preparation = %+v, want previous summary %q", prep, "second summary")
	}
}

func TestSessionContextEditDoesNotTrustPreEditAssistantUsageForProjectedContextEstimates(t *testing.T) {
	e := newEditSession(t)
	largeUserID := e.message(editUser(strings.Repeat("discarded input ", 2_000)))
	assistantID := e.message(editAssistant("small answer", ai.Usage{Input: 10_000, Output: 1, TotalTokens: 10_001}))
	e.edit(largeUserID, nil)

	edited := e.estimate()
	if edited.UsageTokens != 0 || edited.Tokens >= 100 {
		t.Fatalf("edited estimate = %+v, want no usage and < 100 tokens", edited)
	}
	e.edit(assistantID, nil)
	if got := e.estimate().Tokens; got != 0 {
		t.Fatalf("tokens after omitting everything = %d, want 0", got)
	}
}

func TestSessionContextEditPreparesCompactionFromEditedModelContent(t *testing.T) {
	e := newEditSession(t)
	omittedID := e.message(editUser(strings.Repeat("OMIT-ME ", 100)))
	e.message(editAssistant(strings.Repeat("old answer ", 100), defaultEditUsage))
	e.edit(omittedID, nil)
	e.message(editUser("keep"))
	e.message(editAssistant("suffix", defaultEditUsage))

	prep := e.prepare()
	if prep == nil {
		t.Fatal("preparation = nil")
	}
	if strings.Contains(messagesJSON(t, prep.MessagesToSummarize), "OMIT-ME") || strings.Contains(messagesJSON(t, prep.TurnPrefixMessages), "OMIT-ME") {
		t.Fatal("omitted content was summarized")
	}
}
