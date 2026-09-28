// Package goal is the session goal: an objective wopr keeps working toward
// across turns, continuing automatically until an independent audit accepts
// the orchestrator's completion claim or a cap pauses it. It holds the
// state, its caps, and the texts the models read; the session runs the
// loop.
package goal

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/internal/text"
)

// EntryType is the custom session entry type holding the goal.
const EntryType = "wopr-goal-v1"

// MessageType is the custom message type of the goal's kickoff and
// continuation messages.
const MessageType = "goal"

// Step is what a frontend does after a run settles.
type Step struct {
	// Message, when set, is the next automatic turn's message.
	Message *agent.AgentMessage
	// Wait reports background tasks of the goal still running; delivering
	// their results settles another run, which asks again.
	Wait bool
	// Event is "done" or "paused" when the goal just changed that way, and
	// Text says why.
	Event, Text string
}

// Status is the goal's state.
type Status string

// Goal statuses.
const (
	Active  Status = "active"
	Paused  Status = "paused"
	Done    Status = "done"
	Stopped Status = "stopped"
)

// Caps bound one working window of a goal; resuming opens a new window.
type Caps struct {
	Turns int `json:"turns"`
	// Cost is the most non-free spend in dollars.
	Cost float64 `json:"cost"`
	// Time is the most active wall time.
	Time time.Duration `json:"time"`
}

// DefaultCaps are the caps a goal gets unless /goal overrides them.
func DefaultCaps() Caps { return Caps{Turns: 20, Cost: 1.00, Time: time.Hour} }

// ParseArgs reads /goal's arguments: leading --turns N, --cost DOLLARS,
// and --time DURATION caps, then the objective.
func ParseArgs(args string) (string, Caps, error) {
	caps := DefaultCaps()
	fields := strings.Fields(args)
	for len(fields) >= 2 && strings.HasPrefix(fields[0], "--") {
		value := fields[1]
		var err error
		switch fields[0] {
		case "--turns":
			caps.Turns, err = strconv.Atoi(value)
		case "--cost":
			caps.Cost, err = strconv.ParseFloat(strings.TrimPrefix(value, "$"), 64)
		case "--time":
			caps.Time, err = time.ParseDuration(value)
		default:
			return "", caps, fmt.Errorf("unknown flag %s (--turns, --cost, --time)", fields[0])
		}
		if err != nil {
			return "", caps, fmt.Errorf("%s %s: %w", fields[0], value, err)
		}
		fields = fields[2:]
	}
	objective := strings.Join(fields, " ")
	if objective == "" {
		return "", caps, errors.New("usage: /goal [--turns N] [--cost DOLLARS] [--time DURATION] <objective>")
	}
	return objective, caps, nil
}

// IdleTurns is how many automatic turns in a row without a tool call pause
// the goal: the model is talking, not working.
const IdleTurns = 3

// maxObjective bounds the objective as the models read it.
const maxObjective = 1500

// State is the persisted goal.
type State struct {
	Version   int    `json:"version"`
	ID        string `json:"id"`
	Objective string `json:"objective"`
	Status    Status `json:"status"`
	// Reason says why the goal is paused or done.
	Reason string `json:"reason,omitempty"`
	Caps   Caps   `json:"caps"`
	// Turns, Spent, and Elapsed are this window's automatic turns, non-free
	// spend, and active time.
	Turns   int           `json:"turns"`
	Spent   float64       `json:"spent"`
	Elapsed time.Duration `json:"elapsed"`
	// TotalSpent is the spend across windows.
	TotalSpent float64 `json:"totalSpent"`
	// Idle counts automatic turns in a row without a tool call.
	Idle int `json:"idle,omitempty"`
	// Audits counts completion audits; Feedback is the last failed one's.
	Audits   int    `json:"audits,omitempty"`
	Feedback string `json:"feedback,omitempty"`
	// Audited identifies the last claim audited, so a claim is audited once.
	Audited string `json:"audited,omitempty"`
	// Started is when the goal was set.
	Started time.Time `json:"started"`
	// Tasks are the background tasks started while the goal was active.
	Tasks []string `json:"tasks,omitempty"`
}

// New returns an active goal.
func New(id, objective string, caps Caps, now time.Time) State {
	return State{Version: 1, ID: id, Objective: strings.TrimSpace(objective), Status: Active, Caps: caps, Started: now}
}

// Open reports whether the goal still needs work.
func (s State) Open() bool { return s.Status == Active || s.Status == Paused }

// CapHit names the cap this window has reached, or "".
func (s State) CapHit() string {
	switch {
	case s.Caps.Turns > 0 && s.Turns >= s.Caps.Turns:
		return fmt.Sprintf("turn cap reached (%d)", s.Caps.Turns)
	case s.Caps.Cost > 0 && s.Spent >= s.Caps.Cost:
		return fmt.Sprintf("spend cap reached ($%.2f)", s.Caps.Cost)
	case s.Caps.Time > 0 && s.Elapsed >= s.Caps.Time:
		return "time cap reached (" + s.Caps.Time.Round(time.Second).String() + ")"
	case s.Idle >= IdleTurns:
		return fmt.Sprintf("no progress in %d turns", IdleTurns)
	}
	return ""
}

// Summary is one line of status: status, turns, spend, and time.
func (s State) Summary() string {
	parts := []string{string(s.Status)}
	if s.Caps.Turns > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d turns", s.Turns, s.Caps.Turns))
	}
	if s.Caps.Cost > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f/$%.2f", s.Spent, s.Caps.Cost))
	}
	parts = append(parts, s.Elapsed.Round(time.Second).String())
	if s.Reason != "" {
		parts = append(parts, s.Reason)
	}
	return strings.Join(parts, " · ")
}

// Claim markers: a line the orchestrator ends its reply with.
const (
	MetMarker     = "GOAL MET"
	BlockedMarker = "GOAL BLOCKED"
)

// Claim is what the orchestrator's final reply says about the goal.
type Claim struct {
	// Kind is MetMarker, BlockedMarker, or "" for no claim.
	Kind string
	// Evidence is the text after the marker line, or the reply before it
	// when nothing follows.
	Evidence string
}

// ParseClaim finds the last marker line in a reply. A marker counts only
// on a line of its own (markdown emphasis and a trailing colon allowed), so
// prose that mentions it is not a claim.
func ParseClaim(text string) Claim {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, line := range slices.Backward(lines) {
		head, rest, _ := strings.Cut(strings.Trim(strings.TrimSpace(line), "*_`#> "), ":")
		head = strings.TrimSpace(strings.Trim(head, "*_` "))
		if head != MetMarker && head != BlockedMarker {
			continue
		}
		evidence := strings.TrimSpace(strings.Trim(rest, "*_` ") + "\n" + strings.Join(lines[i+1:], "\n"))
		if evidence == "" {
			evidence = strings.TrimSpace(strings.Join(lines[:i], "\n"))
		}
		return Claim{Kind: head, Evidence: evidence}
	}
	return Claim{}
}

// markerRule tells the orchestrator how to claim completion.
const markerRule = "When every requirement is met and verified, end your reply with a line `GOAL MET` followed by the evidence (paths:lines, commands and their output); an independent audit checks it. If only the user can unblock you, end with a line `GOAL BLOCKED: <why>`."

// Kickoff is the message that starts work on a new goal.
func (s State) Kickoff() string {
	return "Goal: " + clip(s.Objective, maxObjective) + "\nWork toward it across turns; wopr continues automatically until it is met. " + markerRule
}

// Continuation is the message of an automatic turn.
func (s State) Continuation() string {
	var b strings.Builder
	if s.Feedback != "" {
		b.WriteString("The completion audit failed: " + clip(s.Feedback, 1200) + "\n")
	}
	fmt.Fprintf(&b, "Continue toward the goal (auto turn %d", s.Turns)
	if s.Caps.Turns > 0 {
		fmt.Fprintf(&b, "/%d", s.Caps.Turns)
	}
	b.WriteString("): " + clip(s.Objective, maxObjective) + "\nTake the next concrete step; don't narrow the goal. " + markerRule)
	return b.String()
}

// AuditBrief is the auditor subagent's brief: the goal and the
// orchestrator's claim, which it must check against the working tree.
func (s State) AuditBrief(claim string) string {
	var b strings.Builder
	b.WriteString("Audit whether a goal is complete.\n\nGoal (the user's words):\n" + clip(s.Objective, maxObjective) + "\n\n")
	b.WriteString("The working agent claims it is met. Its claim is not evidence:\n<claim>\n" + clip(claim, 2000) + "\n</claim>\n\n")
	if s.Feedback != "" {
		b.WriteString("A previous audit failed it for: " + clip(s.Feedback, 600) + "\n\n")
	}
	b.WriteString("Check every requirement of the goal against the actual files and read-only commands. Stubs, TODOs, and claims you cannot confirm count as unmet. " +
		"Start ANSWER with PASS if every requirement is verifiably met, else with FAIL and what is missing or wrong, briefly. Quote the lines that prove it.")
	return b.String()
}

// Verdict reads an audit result: PASS only when the task was accepted and
// its answer starts with PASS. feedback is the answer for the
// orchestrator.
func Verdict(accepted bool, answer string) (pass bool, feedback string) {
	answer = strings.TrimSpace(answer)
	word := ""
	if fields := strings.Fields(answer); len(fields) > 0 {
		word = strings.ToUpper(strings.Trim(fields[0], "*_`:.,"))
	}
	if accepted && word == "PASS" {
		return true, answer
	}
	answer = cmp.Or(answer, "the audit gave no answer")
	if !accepted && word == "PASS" {
		answer = "the audit could not verify its evidence: " + answer
	}
	return false, answer
}

// clip trims s and clips it to n bytes.
func clip(s string, n int) string { return text.Clip(strings.TrimSpace(s), n) }
