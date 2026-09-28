package codingagent

import (
	"bytes"
	"encoding/json"
	"time"
)

// The sidebar's session data (reply timing per model, savings, the last
// routing decision, finished tasks, modified files) is kept as a "sidebar"
// custom entry after each run, so resuming a session shows its sidebar as it
// was left. Token and cost totals and the context gauge already come from
// the session's own entries.

// sidebarEntryType is the custom entry holding the sidebar snapshot.
const sidebarEntryType = "sidebar"

type speedSnapshot struct {
	Replies      int   `json:"replies,omitempty"`
	TTFTMs       int64 `json:"ttftMs,omitempty"`
	OutTokens    int   `json:"outTokens,omitempty"`
	GenerationMs int64 `json:"generationMs,omitempty"`
}

func snapSpeed(s speedSums) speedSnapshot {
	return speedSnapshot{Replies: s.replies, TTFTMs: s.ttft.Milliseconds(), OutTokens: s.outTokens, GenerationMs: s.generation.Milliseconds()}
}

func (s speedSnapshot) sums() speedSums {
	return speedSums{replies: s.Replies, ttft: time.Duration(s.TTFTMs) * time.Millisecond, outTokens: s.OutTokens, generation: time.Duration(s.GenerationMs) * time.Millisecond}
}

type modelSnapshot struct {
	Provider string        `json:"provider"`
	Model    string        `json:"model"`
	Calls    int           `json:"calls,omitempty"`
	Input    int           `json:"input,omitempty"`
	Output   int           `json:"output,omitempty"`
	Cost     float64       `json:"cost,omitempty"`
	Timed    speedSnapshot `json:"timed"`
}

type sidebarSnapshot struct {
	Speed        speedSnapshot   `json:"speed"`
	Models       []modelSnapshot `json:"models,omitempty"`
	LastTTFTMs   int64           `json:"lastTtftMs,omitempty"`
	LastRate     float64         `json:"lastTokensPerSec,omitempty"`
	Savings      int             `json:"savings,omitempty"`
	SavedTokens  int             `json:"savedTokens,omitempty"`
	RoutingSaved float64         `json:"routingSaved,omitempty"`
	Tasks        int             `json:"tasks,omitempty"`
	TaskRuns     []struct {
		Target string `json:"target"`
		Count  int    `json:"count"`
	} `json:"taskRuns,omitempty"`
	Decision struct {
		Tier   string `json:"tier,omitempty"`
		Model  string `json:"model,omitempty"`
		Reason string `json:"reason,omitempty"`
	} `json:"decision"`
	Files []struct {
		Path    string `json:"path"`
		Added   int    `json:"added,omitempty"`
		Removed int    `json:"removed,omitempty"`
	} `json:"files,omitempty"`
	FirstPrompt string `json:"firstPrompt,omitempty"`
}

// sidebarSnapshotNow captures the session-scoped sidebar data.
func (m *InteractiveMode) sidebarSnapshotNow() sidebarSnapshot {
	side := &m.side
	var snap sidebarSnapshot
	if side.speed.session == m.crashSessionFile() {
		snap.Speed = snapSpeed(side.speed.speedSums)
		for _, u := range side.speed.models {
			snap.Models = append(snap.Models, modelSnapshot{Provider: u.provider, Model: u.model, Calls: u.calls, Input: u.input, Output: u.output, Cost: u.cost, Timed: snapSpeed(u.timed)})
		}
	}
	snap.LastTTFTMs, snap.LastRate = side.ttft.Milliseconds(), side.tokensPerSec
	snap.Savings, snap.SavedTokens, snap.RoutingSaved = side.savings, side.savedTokens, side.routingSaved
	snap.Tasks = side.tasks
	for _, run := range side.taskRuns {
		snap.TaskRuns = append(snap.TaskRuns, struct {
			Target string `json:"target"`
			Count  int    `json:"count"`
		}{run.target, run.count})
	}
	snap.Decision.Tier, snap.Decision.Model, snap.Decision.Reason = side.decision.tier, side.decision.model, side.decision.reason
	for _, f := range m.modifiedFiles {
		snap.Files = append(snap.Files, struct {
			Path    string `json:"path"`
			Added   int    `json:"added,omitempty"`
			Removed int    `json:"removed,omitempty"`
		}{f.path, f.added, f.removed})
	}
	snap.FirstPrompt = m.firstPrompt
	return snap
}

// saveSidebarSnapshot records the sidebar in the session when it changed
// since the last snapshot.
func (m *InteractiveMode) saveSidebarSnapshot() {
	session := m.currentSession()
	if session == nil {
		return
	}
	data, err := json.Marshal(m.sidebarSnapshotNow())
	if err != nil || bytes.Equal(data, m.lastSidebarSnapshot) {
		return
	}
	if session.AppendCustomEntry(sidebarEntryType, json.RawMessage(data)) == nil {
		m.lastSidebarSnapshot = data
	}
}

// restoreSidebar clears the session-scoped sidebar data and loads the
// current session's last snapshot, if it has one: after a resume, a /new,
// or startup on a resumed session.
func (m *InteractiveMode) restoreSidebar() {
	side := &m.side
	side.speed = replySpeed{}
	side.ttft, side.tokensPerSec = 0, 0
	side.savings, side.savedTokens, side.routingSaved = 0, 0, 0
	side.tasks, side.taskRuns = 0, nil
	side.decision = routeDecision{}
	m.modifiedFiles, m.firstPrompt, m.lastSidebarSnapshot = nil, "", nil
	session := m.currentSession()
	if session == nil {
		return
	}
	var raw json.RawMessage
	for _, entry := range session.Entries() {
		if entry.Base.Type != "custom" {
			continue
		}
		var custom struct {
			CustomType string          `json:"customType"`
			Data       json.RawMessage `json:"data"`
		}
		if json.Unmarshal(entry.Raw(), &custom) == nil && custom.CustomType == sidebarEntryType {
			raw = custom.Data
		}
	}
	var snap sidebarSnapshot
	if raw == nil || json.Unmarshal(raw, &snap) != nil {
		return
	}
	path := m.crashSessionFile()
	side.speed.reset(path)
	side.speed.speedSums = snap.Speed.sums()
	for _, u := range snap.Models {
		used := side.speed.model(path, u.Provider, u.Model)
		used.calls, used.input, used.output, used.cost, used.timed = u.Calls, u.Input, u.Output, u.Cost, u.Timed.sums()
	}
	side.ttft, side.tokensPerSec = time.Duration(snap.LastTTFTMs)*time.Millisecond, snap.LastRate
	side.savings, side.savedTokens, side.routingSaved = snap.Savings, snap.SavedTokens, snap.RoutingSaved
	side.tasks = snap.Tasks
	for _, run := range snap.TaskRuns {
		side.taskRuns = append(side.taskRuns, taskRun{target: run.Target, count: run.Count})
	}
	side.decision = routeDecision{tier: snap.Decision.Tier, model: snap.Decision.Model, reason: snap.Decision.Reason}
	for _, f := range snap.Files {
		m.modifiedFiles = append(m.modifiedFiles, fileChange{path: f.Path, added: f.Added, removed: f.Removed})
	}
	m.firstPrompt = snap.FirstPrompt
	m.lastSidebarSnapshot = raw
}
