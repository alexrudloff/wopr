package router

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// Outcome learning: per model and day, the signals wopr already has about
// how well a model did (subagent results accepted or escalated, quotes it
// could verify, goal audits, tool calls that failed). Nothing reorders on
// its own; a model whose week clearly disagrees with its rank produces a
// suggestion for the user to act on.

// OutcomesFileName holds outcome counts under the agent directory.
const OutcomesFileName = "router-outcomes.json"

// Outcome windows: suggestions look at the last week; counts older than
// two weeks are dropped.
const (
	outcomeWeek = 7
	outcomeKeep = 14
)

// OutcomeCounts are one model's signals over a span.
type OutcomeCounts struct {
	// Tasks are subagent attempts; Escalated moved up after an
	// unacceptable result; Failed were not accepted at all.
	Tasks     int `json:"tasks,omitempty"`
	Escalated int `json:"escalated,omitempty"`
	Failed    int `json:"failed,omitempty"`
	// Quotes and QuotesUnverified count the evidence a subagent quoted.
	Quotes           int `json:"quotes,omitempty"`
	QuotesUnverified int `json:"quotesUnverified,omitempty"`
	// GoalPass and GoalFail are /goal completion audits of the model's work.
	GoalPass int `json:"goalPass,omitempty"`
	GoalFail int `json:"goalFail,omitempty"`
	// ToolCalls and ToolErrors count the orchestrator's tool calls.
	ToolCalls  int `json:"toolCalls,omitempty"`
	ToolErrors int `json:"toolErrors,omitempty"`
}

func (c *OutcomeCounts) add(o OutcomeCounts) {
	c.Tasks += o.Tasks
	c.Escalated += o.Escalated
	c.Failed += o.Failed
	c.Quotes += o.Quotes
	c.QuotesUnverified += o.QuotesUnverified
	c.GoalPass += o.GoalPass
	c.GoalFail += o.GoalFail
	c.ToolCalls += o.ToolCalls
	c.ToolErrors += o.ToolErrors
}

type outcomeFile struct {
	// Days maps a spec to its counts per day (2006-01-02).
	Days map[string]map[string]OutcomeCounts `json:"days"`
	// Dismissed maps a spec to the day the user kept it where it is; its
	// suggestion stays quiet for a week.
	Dismissed map[string]string `json:"dismissed,omitempty"`
}

var outcomeMu sync.Mutex

func readOutcomes(dir string) outcomeFile {
	f := outcomeFile{Days: map[string]map[string]OutcomeCounts{}, Dismissed: map[string]string{}}
	if data, err := os.ReadFile(filepath.Join(dir, OutcomesFileName)); err == nil {
		_ = json.Unmarshal(data, &f)
	}
	if f.Days == nil {
		f.Days = map[string]map[string]OutcomeCounts{}
	}
	if f.Dismissed == nil {
		f.Dismissed = map[string]string{}
	}
	return f
}

func writeOutcomes(dir string, f outcomeFile) {
	if data, err := json.MarshalIndent(f, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(dir, OutcomesFileName), append(data, '\n'), 0o600)
	}
}

// RecordOutcome adds o to spec's counts for today.
func RecordOutcome(dir, spec string, o OutcomeCounts) {
	if dir == "" || spec == "" {
		return
	}
	outcomeMu.Lock()
	defer outcomeMu.Unlock()
	f := readOutcomes(dir)
	now := time.Now()
	today := now.Format(time.DateOnly)
	days := f.Days[spec]
	if days == nil {
		days = map[string]OutcomeCounts{}
		f.Days[spec] = days
	}
	c := days[today]
	c.add(o)
	days[today] = c
	cutoff := now.AddDate(0, 0, -outcomeKeep).Format(time.DateOnly)
	for _, d := range f.Days {
		maps.DeleteFunc(d, func(day string, _ OutcomeCounts) bool { return day < cutoff })
	}
	writeOutcomes(dir, f)
}

// DismissSuggestion keeps spec where it is: its suggestion stays quiet for
// a week.
func DismissSuggestion(dir, spec string) {
	outcomeMu.Lock()
	defer outcomeMu.Unlock()
	f := readOutcomes(dir)
	f.Dismissed[spec] = time.Now().Format(time.DateOnly)
	writeOutcomes(dir, f)
}

// Suggestion proposes moving a model down the ranking.
type Suggestion struct {
	Spec   string
	Reason string
}

// Suggestions lists models whose last week clearly disagrees with their
// rank: many escalations, failed goal audits, unverified quotes, or failed
// tool calls.
func Suggestions(dir string) []Suggestion {
	outcomeMu.Lock()
	f := readOutcomes(dir)
	outcomeMu.Unlock()
	now := time.Now()
	since := now.AddDate(0, 0, -outcomeWeek).Format(time.DateOnly)
	var out []Suggestion
	for _, spec := range slices.Sorted(maps.Keys(f.Days)) {
		if day, ok := f.Dismissed[spec]; ok && day > since {
			continue
		}
		var c OutcomeCounts
		for day, counts := range f.Days[spec] {
			if day > since {
				c.add(counts)
			}
		}
		if reason := suggestionReason(c); reason != "" {
			out = append(out, Suggestion{Spec: spec, Reason: reason})
		}
	}
	return out
}

// suggestionReason says why a week of counts argues for a lower rank, or
// "" when it doesn't clearly.
func suggestionReason(c OutcomeCounts) string {
	switch {
	case c.Escalated >= 5 && c.Escalated*3 >= c.Tasks:
		return fmt.Sprintf("was escalated %d times this week", c.Escalated)
	case c.GoalFail >= 3 && c.GoalFail > c.GoalPass:
		return fmt.Sprintf("failed %d of %d goal audits this week", c.GoalFail, c.GoalFail+c.GoalPass)
	case c.QuotesUnverified >= 5 && c.QuotesUnverified*2 >= c.Quotes:
		return fmt.Sprintf("quoted %d lines this week that wopr couldn't verify", c.QuotesUnverified)
	case c.ToolErrors >= 10 && c.ToolErrors*4 >= c.ToolCalls:
		return fmt.Sprintf("had %d of %d tool calls fail this week", c.ToolErrors, c.ToolCalls)
	}
	return ""
}
