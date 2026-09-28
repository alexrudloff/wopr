// Session fork helpers and the /tree node adapter.

package codingagent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/alexrudloff/wopr/tui"
)

// ForkToNewSession creates a NEW session file branched at the PARENT of
// userMsgEntryID (so the selected user message itself is excluded) and
// switches sm's current session to it. It returns the new session and the
// selected message's text, which the caller prefills into the editor for
// modification. The fork branches from the selected entry's parent, or is a
// fresh session carrying `parentSession` when the selected message is the
// root.
func (sm *SessionManager) ForkToNewSession(source *Session, userMsgEntryID string) (*Session, string, error) {
	if source == nil {
		return nil, "", fmt.Errorf("fork: nil source session")
	}
	entry, ok := source.EntryByID(userMsgEntryID)
	if !ok {
		return nil, "", fmt.Errorf("fork: entry %q not found", userMsgEntryID)
	}
	var selectedText string
	if me, ok := source.messageFor(entry); ok {
		selectedText = extractMessageText(me)
	}
	if entry.Base.ParentID == nil {
		// Selected message is the root: fork into a fresh empty session that
		// only records parentSession.
		id := generateSessionID()
		newSess, err := sm.Create(id, source.Path())
		if err != nil {
			return nil, "", err
		}
		return newSess, selectedText, nil
	}
	newSess, err := sm.Clone(source, *entry.Base.ParentID)
	if err != nil {
		return nil, "", err
	}
	return newSess, selectedText, nil
}

// runSlotSelector shows a bordered SlotSelector (no filter, fixed title and
// hint rows) in the editor slot and returns the chosen index.
func (m *InteractiveMode) runSlotSelector(sel *tui.SlotSelectorComponent) (int, bool) {
	if !m.runInSlot(modalOf(sel)) || sel.Cancelled() {
		return -1, false
	}
	return sel.SelectedIndex(), true
}

// runLoginDialog shows the login dialog in the editor slot until it is done.
// The caller drives the login from a goroutine and signals state changes on
// renderNotify.
func (m *InteractiveMode) runLoginDialog(dlg *tui.LoginDialog, renderNotify <-chan struct{}) bool {
	md := modalOf(dlg)
	md.wake = renderNotify
	return m.runInSlot(md) && !dlg.Cancelled()
}

// userMessageSelectorItems extracts the user-role messages from the current
// path-to-leaf and returns parallel slices of entry IDs and message texts in
// chronological order (oldest to newest).
func userMessageSelectorItems(s *Session) (ids []string, labels []string) {
	leaf := s.LeafID()
	if leaf == nil {
		return nil, nil
	}
	path := s.Branch(*leaf)
	for _, e := range path {
		me, ok := s.messageFor(e)
		if !ok || me.Message.User == nil {
			continue
		}
		txt := extractMessageText(me)
		if txt == "" {
			continue
		}
		ids = append(ids, me.ID)
		labels = append(labels, txt)
	}
	return ids, labels
}

// treeNodeAdapter bridges *SessionTreeNode (codingagent) →
// tui.TreeNode (TUI-package interface). Label rendering is delegated
// to treeRowFormatter (per-type labels and per-tool argument summaries).
// The formatter is shared across all
// adapters in a single /tree open via the `f` pointer: both for
// efficiency (one toolCallMap pre-walk per session) and so siblings
// agree on home-path shortening + tool-name resolution.
type treeNodeAdapter struct {
	n *SessionTreeNode
	f *treeRowFormatter
}

func (a *treeNodeAdapter) NodeID() string { return a.n.Entry.Base.ID }

func (a *treeNodeAdapter) NodeLabelTimestamp() string { return a.n.LabelTimestamp }

func (a *treeNodeAdapter) NodeBranchLabel() string { return a.n.Label }

// NodeFilterTags classifies the entry for /tree's filter-mode
// skip-set. `label`, `context_edit`,
// `custom`, `model_change`, `thinking_level_change`, `session_info` are all
// tagged `"settings"` and hidden in the default filter mode.
// Adds `"tool_result"` (no-tools mode),
// `"user"` (user-only mode), and `"labeled"` (labeled-only mode).
func (a *treeNodeAdapter) NodeFilterTags() []string {
	var tags []string
	switch a.n.Entry.Base.Type {
	case "label", "context_edit", "custom", "model_change", "thinking_level_change", "session_info":
		tags = append(tags, "settings")
	case "usage":
		tags = append(tags, "usage")
	case "message":
		if me, ok := a.f.asMessage(a.n.Entry); ok {
			switch me.Message.Role() {
			case "user":
				tags = append(tags, "user")
			case "toolResult":
				tags = append(tags, "tool_result")
			}
		}
	}
	if a.n.Label != "" {
		tags = append(tags, "labeled")
	}
	return tags
}

func (a *treeNodeAdapter) NodeSearchableText() string {
	// The branch label plus role, content, and per-type fields, joined
	// as a plain string the tui lowercases and substring-matches.
	e := a.n.Entry
	parts := []string{}
	if a.n.Label != "" {
		parts = append(parts, a.n.Label)
	}
	switch e.Base.Type {
	case "message":
		if a.f != nil {
			if me, ok := a.f.asMessage(e); ok {
				parts = append(parts, me.Message.Role())
				parts = append(parts, extractMessageText(me))
			}
		}
	case "custom_message":
		var ent CustomMessageEntry
		_ = json.Unmarshal(e.raw, &ent)
		parts = append(parts, ent.CustomType, extractCustomMessageText(ent))
	case "compaction":
		parts = append(parts, "compaction", e.Base.Type)
	case "branch_summary":
		var ent BranchSummaryEntry
		_ = json.Unmarshal(e.raw, &ent)
		parts = append(parts, "branch", "summary", ent.Summary)
	case "session_info":
		var ent SessionInfoEntry
		_ = json.Unmarshal(e.raw, &ent)
		parts = append(parts, "title", ent.Name)
	case "model_change":
		var ent ModelChangeEntry
		_ = json.Unmarshal(e.raw, &ent)
		parts = append(parts, "model", ent.ModelID)
	case "thinking_level_change":
		var ent ThinkingLevelEntry
		_ = json.Unmarshal(e.raw, &ent)
		parts = append(parts, "thinking", ent.ThinkingLevel)
	case "custom":
		var ent CustomEntry
		_ = json.Unmarshal(e.raw, &ent)
		parts = append(parts, "custom", ent.CustomType)
	case "context_edit":
		mode, targetID := contextEditSummary(e)
		parts = append(parts, "context edit", mode, targetID)
	case "label":
		var ent LabelEntry
		_ = json.Unmarshal(e.raw, &ent)
		label := ""
		if ent.Label != nil {
			label = *ent.Label
		}
		parts = append(parts, "label", label)
	}
	return strings.Join(parts, " ")
}

func (a *treeNodeAdapter) NodeLabel() string {
	f := a.f
	if f == nil {
		// Adapter built without a formatter (e.g. older callers /
		// unit tests). Build a one-shot formatter with no toolCallMap;
		// tool_result rows will fall through to "[tool]" but every
		// other type renders identically.
		f = newTreeRowFormatter(nil)
	}
	return f.FormatTreeRow(a.n.Entry)
}

func (a *treeNodeAdapter) NodeChildren() []tui.TreeNode {
	// Filter out tool-call-only assistant messages unconditionally.
	// Suppressed nodes are not reparented: their
	// visible children are pulled up to take their slot. Recursive
	// because a chain of suppressed nodes (asst-tool-only →
	// tool_result_user → asst-tool-only → …) collapses to its
	// non-suppressed leaves.
	out := make([]tui.TreeNode, 0, len(a.n.Children))
	for _, c := range a.n.Children {
		child := &treeNodeAdapter{n: c, f: a.f}
		if a.f != nil && a.f.shouldSuppressInTree(c.Entry) {
			out = append(out, child.NodeChildren()...)
			continue
		}
		out = append(out, child)
	}
	return out
}
