package compaction

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

func mustSessionEntry(t *testing.T, v map[string]any) codingagent.SessionEntry {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	var base codingagent.SessionEntryBase
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatalf("unmarshal base: %v", err)
	}
	return codingagent.NewSessionEntry(raw, base)
}

func userEntry(id, text string) map[string]any {
	return map[string]any{
		"type": "message", "id": id, "parentId": nil, "timestamp": time.Now().UTC().Format(time.RFC3339),
		"message": map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "text", "text": text}},
		},
	}
}

func assistantEntry(id, text string) map[string]any {
	return map[string]any{
		"type": "message", "id": id, "parentId": nil, "timestamp": time.Now().UTC().Format(time.RFC3339),
		"message": map[string]any{
			"role":    "assistant",
			"content": []any{map[string]any{"type": "text", "text": text}},
		},
	}
}

func compactionEntry(id, summary, firstKeptID string, tokensBefore int) map[string]any {
	return map[string]any{
		"type": "compaction", "id": id, "parentId": nil, "timestamp": time.Now().UTC().Format(time.RFC3339),
		"summary": summary, "firstKeptEntryId": firstKeptID, "tokensBefore": tokensBefore,
	}
}

// fakeCompleter returns a fixed string for all CompleteSimple calls.
type fakeCompleter struct {
	response  string
	usage     *ai.Usage
	maxTokens []int
}

func (f *fakeCompleter) CompleteSimple(_ context.Context, _ *ai.Model, _ string, _ []agent.AgentMessage, options ai.StreamOptions) (string, *ai.Usage, error) {
	f.maxTokens = append(f.maxTokens, options.MaxTokens)
	return f.response, f.usage, nil
}

func TestCompactMissingFirstKeptEntryError(t *testing.T) {
	_, err := Compact(t.Context(), CompactionPreparation{
		MessagesToSummarize: []agent.AgentMessage{{User: &agent.UserMessage{Role: "user"}}},
		Settings:            CompactionSettings{ReserveTokens: 100},
	}, &ai.Model{}, &fakeCompleter{response: "summary"}, nil, "", "", nil, "")
	if err == nil || err.Error() != "First kept entry has no UUID - session may need migration" {
		t.Fatalf("Compact() error = %v", err)
	}
}

func TestShouldCompact(t *testing.T) {
	s := CompactionSettings{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 20000}
	cases := []struct {
		name          string
		contextTokens int
		contextWindow int
		want          bool
	}{
		{"over_threshold", 130_000, 128_000, true},
		{"under_threshold", 110_000, 128_000, false},
		{"at_threshold", 128_000 - 16_384, 128_000, false},
		{"just_over_threshold", 128_000 - 16_384 + 1, 128_000, true},
		{"zero_window", 130_000, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldCompact(tc.contextTokens, tc.contextWindow, s); got != tc.want {
				t.Errorf("ShouldCompact(%d, %d) = %v, want %v", tc.contextTokens, tc.contextWindow, got, tc.want)
			}
		})
	}

	// disabled setting always returns false
	disabled := CompactionSettings{Enabled: false, ReserveTokens: 16384}
	if ShouldCompact(999_999, 128_000, disabled) {
		t.Error("disabled settings should never compact")
	}
}

func TestPrepareCompaction(t *testing.T) {
	// Build a 20-entry session with a prior compaction at entry 5.
	// Layout: 5 entries (u+a pairs), compaction entry, then 14 more entries.
	entries := make([]codingagent.SessionEntry, 0, 20)

	// First 4 entries (2 turns).
	for i := range 2 {
		entries = append(entries,
			mustSessionEntry(t, userEntry("u"+string(rune('0'+i)), strings.Repeat("x", 200))),
			mustSessionEntry(t, assistantEntry("a"+string(rune('0'+i)), strings.Repeat("y", 200))),
		)
	}

	// Compaction entry pointing at "u2" as firstKeptEntryId.
	entries = append(entries, mustSessionEntry(t, compactionEntry("c0", "Previous summary.", "u2", 5000)))

	// 15 more entries starting at "u2" (the firstKeptEntryId).
	for i := range 7 {
		uid := "u" + string(rune('2'+i))
		aid := "a" + string(rune('2'+i))
		entries = append(entries,
			mustSessionEntry(t, userEntry(uid, strings.Repeat("m", 200))),
			mustSessionEntry(t, assistantEntry(aid, strings.Repeat("n", 200))),
		)
	}
	// One final user entry.
	entries = append(entries, mustSessionEntry(t, userEntry("ufinal", strings.Repeat("z", 200))))

	s := CompactionSettings{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 500}
	prep := PrepareCompaction(entries, s)

	if prep == nil {
		t.Fatal("PrepareCompaction returned nil; expected non-nil")
	}
	if prep.FirstKeptEntryID == "" {
		t.Error("FirstKeptEntryID is empty")
	}
	if len(prep.MessagesToSummarize) == 0 {
		t.Error("MessagesToSummarize is empty; expected at least some messages")
	}
	if prep.PreviousSummary != "Previous summary." {
		t.Errorf("PreviousSummary = %q, want %q", prep.PreviousSummary, "Previous summary.")
	}
}

// TestPrepareCompaction_PriorCompactionKeptFromRoot guards the boundaryStart
// fix: when the prior compaction's firstKeptEntryId resolves to index 0, every
// entry up to the prior compaction must still feed the next summary's input,
// not be skipped. The old `boundaryStart == 0` sentinel conflated "found at
// index 0" with "not found" and dropped them: losing conversation up to the
// last compaction. The fallback keys off `firstKeptEntryIndex >= 0`.
func TestPrepareCompaction_PriorCompactionKeptFromRoot(t *testing.T) {
	const marker = "FIRSTENTRYMARKER"
	entries := []codingagent.SessionEntry{
		mustSessionEntry(t, userEntry("u0", marker)),                        // index 0 == firstKeptEntryId
		mustSessionEntry(t, assistantEntry("a0", strings.Repeat("y", 200))), // index 1
		mustSessionEntry(t, compactionEntry("c0", "Prev.", "u0", 5000)),     // index 2
	}
	for i := range 8 {
		entries = append(entries,
			mustSessionEntry(t, userEntry("un"+string(rune('0'+i)), strings.Repeat("m", 200))),
			mustSessionEntry(t, assistantEntry("an"+string(rune('0'+i)), strings.Repeat("n", 200))),
		)
	}

	s := CompactionSettings{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 500}
	prep := PrepareCompaction(entries, s)
	if prep == nil {
		t.Fatal("PrepareCompaction returned nil")
	}
	found := false
	for _, m := range prep.MessagesToSummarize {
		if m.User == nil {
			continue
		}
		for _, c := range m.User.Content {
			if tc, ok := c.(ai.TextContent); ok && strings.Contains(tc.Text, marker) {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("MessagesToSummarize dropped the index-0 entry (firstKeptEntryId); boundaryStart bug regressed")
	}
}

func TestCompact_FakeCompleter(t *testing.T) {
	prep := CompactionPreparation{
		FirstKeptEntryID: "kept-entry-1",
		MessagesToSummarize: []agent.AgentMessage{
			{
				User: &agent.UserMessage{
					Role:    "user",
					Content: []ai.UserContentBlock{ai.TextContent{Text: "do something"}},
				},
			},
		},
		TokensBefore: 1000,
		FileOps:      NewFileOps(),
		Settings:     DefaultCompactionSettings,
	}
	prep.FileOps.Read["internal/foo.go"] = struct{}{}

	fc := &fakeCompleter{response: "## Summary\nThis is the summary."}
	model := &ai.Model{ID: "fake-model"}

	result, err := Compact(context.Background(), prep, model, fc, nil, "", "", nil, "")
	if err != nil {
		t.Fatalf("Compact returned error: %v", err)
	}
	if result.Summary == "" {
		t.Error("Summary is empty")
	}
	if !strings.Contains(result.Summary, "## Summary") {
		t.Errorf("Summary does not contain fake response; got: %q", result.Summary[:min(100, len(result.Summary))])
	}
	if result.FirstKeptEntryID != "kept-entry-1" {
		t.Errorf("FirstKeptEntryID = %q, want %q", result.FirstKeptEntryID, "kept-entry-1")
	}
	if result.TokensBefore != 1000 {
		t.Errorf("TokensBefore = %d, want 1000", result.TokensBefore)
	}
}

// TestCompactPropagatesUsage verifies the summarization call's usage surfaces on
// CompactionResult so it can be persisted and counted toward session cost.
func TestCompactPropagatesUsage(t *testing.T) {
	prep := CompactionPreparation{
		FirstKeptEntryID: "keep-1",
		MessagesToSummarize: []agent.AgentMessage{{User: &agent.UserMessage{
			Role: "user", Content: []ai.UserContentBlock{ai.TextContent{Text: "hi"}},
		}}},
		Settings: CompactionSettings{ReserveTokens: 1000},
	}
	fc := &fakeCompleter{response: "summary", usage: &ai.Usage{Input: 42, Output: 7}}
	result, err := Compact(context.Background(), prep, &ai.Model{}, fc, nil, "", "", nil, "")
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if result.Usage == nil || result.Usage.Input != 42 || result.Usage.Output != 7 {
		t.Fatalf("result.Usage = %+v, want input 42 output 7", result.Usage)
	}
}

// TestForModelCompactionFits guards small windows: after a compaction the
// summary, the kept tail, and the reply reserve fit the window, so the
// conversation neither overflows nor needs compacting again at once, and
// by default compaction starts where routing stops fitting the model.
func TestForModelCompactionFits(t *testing.T) {
	sets := []CompactionSettings{
		{Enabled: true},
		{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 20000},
		{Enabled: true, ReserveTokens: 60000, KeepRecentTokens: 100000},
	}
	for _, window := range []int{4096, 8192, 32768, 65536, 200000, 1000000} {
		for _, maxOutput := range []int{0, 4096, 16000, window} {
			for i, set := range sets {
				s := set.ForModel(window, maxOutput)
				after := summaryBudget(s.ReserveTokens) + s.KeepRecentTokens
				if s.ReserveTokens <= 0 || after+s.ReserveTokens > window || ShouldCompact(after, window, s) {
					t.Errorf("window %d, max output %d, settings %d: %+v leaves %d tokens after compacting", window, maxOutput, i, s, after)
				}
				if i == 0 && window-s.ReserveTokens != ai.UsableContext(window, maxOutput) {
					t.Errorf("window %d, max output %d: compaction starts at %d, routing fits up to %d", window, maxOutput, window-s.ReserveTokens, ai.UsableContext(window, maxOutput))
				}
			}
		}
	}
}
