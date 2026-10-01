package coding

import (
	"fmt"
	"math"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/efficiency"
)

// Deadline: a run started with --deadline (or WOPR_DEADLINE) knows its time
// budget from the system prompt and gets a short note as it runs down, at
// half, four fifths, and 95% of the budget.

// deadlineState is the budget and the notes already sent.
type deadlineState struct {
	start, end time.Time
	sent       int // notes sent: 1 after half, 2 after 80%, 3 after 95%
}

// deadlineMarks are the elapsed fractions that send a note.
var deadlineMarks = [...]float64{0.5, 0.8, 0.95}

// SetDeadline gives the session a time budget from now. Zero or less
// clears it. The efficiency setting deadlineNotes turns it off.
func (s *Session) SetDeadline(budget time.Duration) {
	if budget <= 0 || s.efficiency != nil && !s.efficiency.cfg.DeadlineNotes {
		s.deadline = deadlineState{}
		return
	}
	now := time.Now()
	s.deadline = deadlineState{start: now, end: now.Add(budget)}
}

// systemSections is the base system prompt plus the time budget, when set.
func (s *Session) systemSections() ai.OrderedSections {
	d := s.deadline
	if d.end.IsZero() {
		return s.baseSystemSections
	}
	text := fmt.Sprintf("<time_budget>\nYou have %s for this task; it ends at %s. Plan to finish inside it: prefer approaches that complete, bound searches and brute-force ranges, and write required output files early, then improve them. Time notes arrive as the budget runs down.\n</time_budget>",
		roundMinutes(d.end.Sub(d.start)), d.end.Format("15:04:05"))
	return append(cloneSystemSections(s.baseSystemSections), ai.PromptSection{Name: "time_budget", Value: new(text)})
}

// deadlineNote is the note for the highest mark crossed since the last
// one, if any.
func (s *Session) deadlineNote() []agent.AgentMessage {
	d := &s.deadline
	if d.end.IsZero() || d.sent >= len(deadlineMarks) {
		return nil
	}
	now := time.Now()
	elapsed := float64(now.Sub(d.start)) / float64(d.end.Sub(d.start))
	mark := d.sent
	for mark < len(deadlineMarks) && elapsed >= deadlineMarks[mark] {
		mark++
	}
	if mark == d.sent {
		return nil
	}
	d.sent = mark
	left := roundMinutes(max(d.end.Sub(now), 0))
	var text string
	switch mark {
	case 1:
		text = fmt.Sprintf("Time note: half of the time budget is used; %s left. Make sure your current approach finishes in time.", left)
	case 2:
		text = fmt.Sprintf("Time note: %s left. Prefer finishing over exploring: shrink searches and brute-force ranges, and make sure every required output file exists now, even if provisional.", left)
	default:
		text = fmt.Sprintf("Time note: %s left. Write your best answer and every required output now, then stop.", left)
	}
	return []agent.AgentMessage{{Custom: map[string]any{
		"role":       agent.RoleCustom,
		"customType": efficiency.DeadlineMessageType,
		"content":    text,
		"display":    true,
		"timestamp":  now.UnixMilli(),
	}}}
}

// roundMinutes renders a duration as whole minutes, or seconds under two
// minutes.
func roundMinutes(d time.Duration) string {
	if d < 2*time.Minute {
		return fmt.Sprintf("%d seconds", int(math.Round(d.Seconds())))
	}
	return fmt.Sprintf("%d minutes", int(math.Round(d.Minutes())))
}
