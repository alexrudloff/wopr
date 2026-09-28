package pruning

import (
	"bytes"
	"encoding/json"
	"sync"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/internal/codingagent"
	"github.com/alexrudloff/wopr/internal/codingagent/compaction"
)

// Item is one model-visible message of the projected context.
type Item struct {
	// EntryID is the session entry a context edit can target, or "" for
	// messages no edit can change (system messages, summaries, shell runs).
	EntryID string
	// Ordinal is the marker number the compress tool addresses, or 0.
	Ordinal int
	Message agent.AgentMessage
	Tokens  int
	// Edited reports that an earlier context edit already changed the entry.
	Edited bool
}

// BuildItems pairs each projected message with its source entry. path is the
// branch from the root to the leaf, which carries the context edits.
func BuildItems(projection codingagent.SessionProjection, path []codingagent.SessionEntry, ordinals *Ordinals) []Item {
	edited := map[string]bool{}
	for _, entry := range path {
		if entry.Base.Type != "context_edit" {
			continue
		}
		var edit struct {
			TargetID string `json:"targetId"`
		}
		if json.Unmarshal(entry.Raw(), &edit) == nil {
			edited[edit.TargetID] = true
		}
	}
	var numbers map[string]int
	if ordinals != nil {
		numbers = ordinals.Update(path)
	}
	items := make([]Item, 0, len(projection.Messages))
	for _, projected := range projection.Entries {
		entry := projected.SourceEntry
		targetable := len(projected.Messages) == 1 && (entry.Base.Type == "message" || entry.Base.Type == "custom_message") && projected.Messages[0].System == nil
		for _, message := range projected.Messages {
			item := Item{Message: message, Tokens: compaction.EstimateTokens(message)}
			if targetable {
				item.EntryID = entry.Base.ID
				item.Edited = edited[entry.Base.ID]
				item.Ordinal = numbers[entry.Base.ID]
			}
			items = append(items, item)
		}
	}
	return items
}

// Ordinals numbers the user and tool-result message entries of a branch in
// path order. A number never changes once given, so a marker rendered into a
// message stays byte-identical across requests and compactions.
type Ordinals struct {
	mu      sync.Mutex
	numbers map[string]int
	seen    int
	lastID  string
	count   int
}

// Update extends the numbering over path and returns it. A path that no
// longer extends the one seen before (a /tree move) is numbered afresh.
func (o *Ordinals) Update(path []codingagent.SessionEntry) map[string]int {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.numbers == nil || o.seen > len(path) || (o.seen > 0 && path[o.seen-1].Base.ID != o.lastID) {
		o.numbers, o.seen, o.count = map[string]int{}, 0, 0
	}
	for _, entry := range path[o.seen:] {
		if markable(entry) {
			o.count++
			o.numbers[entry.Base.ID] = o.count
		}
	}
	o.seen = len(path)
	if o.seen > 0 {
		o.lastID = path[o.seen-1].Base.ID
	}
	return o.numbers
}

var rolePrefix = []byte(`"role":`)

// markable reports whether entry is a user or tool-result message, reading
// only the first role key rather than decoding the whole entry.
func markable(entry codingagent.SessionEntry) bool {
	if entry.Base.Type != "message" {
		return false
	}
	raw := entry.Raw()
	_, after, ok := bytes.Cut(raw, rolePrefix)
	if !ok {
		return false
	}
	rest := bytes.TrimLeft(after, " ")
	return bytes.HasPrefix(rest, []byte(`"user"`)) || bytes.HasPrefix(rest, []byte(`"toolResult"`))
}
