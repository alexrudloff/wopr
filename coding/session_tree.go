package coding

import (
	"encoding/json"
	"strings"

	"github.com/alexrudloff/wopr/ai"
	icodingagent "github.com/alexrudloff/wopr/internal/codingagent"
)

// treeNavigationTarget returns the leaf navigateTree moves to and the text it
// hands back to the editor. A user message or custom message target moves the
// leaf to its parent (nil for a root entry) and returns its text; any other
// entry becomes the leaf itself.
func treeNavigationTarget(entry icodingagent.SessionEntry) (newLeafID *string, editorText string) {
	if message, ok := entry.AsMessage(); ok && message.Message.User != nil {
		var text strings.Builder
		for _, block := range message.Message.User.Content {
			if textBlock, ok := block.(ai.TextContent); ok {
				text.WriteString(textBlock.Text)
			}
		}
		return entry.Base.ParentID, text.String()
	}
	if entry.Base.Type == "custom_message" {
		return entry.Base.ParentID, customMessageEditorText(entry.Raw())
	}
	id := entry.Base.ID
	return &id, ""
}

// customMessageEditorText returns a custom message's editor text: a string
// content is returned as is, and block content joins its text blocks.
func customMessageEditorText(raw json.RawMessage) string {
	var wire struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &wire) != nil {
		return ""
	}
	var text string
	if json.Unmarshal(wire.Content, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(wire.Content, &blocks) != nil {
		return ""
	}
	var joined strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			joined.WriteString(block.Text)
		}
	}
	return joined.String()
}

// treeBranchSummary is the summary navigateTree attaches at the new leaf.
type treeBranchSummary struct {
	Summary string
	Details any
	Usage   *ai.Usage
}

// moveTreeLeaf applies a tree navigation to the session manager. With a
// non-empty summary it branches at newLeafID with a branch_summary entry and
// labels that entry; otherwise it moves the leaf (nil resets it to the root)
// and labels the target. It returns the summary entry id, if one was created.
// The caller holds s.mu.
func (s *Session) moveTreeLeaf(targetID string, newLeafID *string, summary *treeBranchSummary, label string) (string, error) {
	if summary != nil && summary.Summary != "" {
		summaryID, err := s.inner.AppendBranchSummary(newLeafID, summary.Summary, summary.Details, false, summary.Usage)
		if err != nil {
			return "", err
		}
		if label != "" {
			if err := s.inner.AppendLabelChange(summaryID, &label); err != nil {
				return summaryID, err
			}
		}
		return summaryID, nil
	}
	if err := s.inner.SetLeafID(newLeafID); err != nil {
		return "", err
	}
	if label != "" {
		return "", s.inner.AppendLabelChange(targetID, &label)
	}
	return "", nil
}

func derefLeafID(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}

// branchSummaryEntry decodes the branch_summary entry with the given id, or
// returns nil when id is empty or names no branch summary.
func (s *Session) branchSummaryEntry(id string) *BranchSummaryEntry {
	if id == "" {
		return nil
	}
	entry, ok := s.inner.EntryByID(id)
	if !ok || entry.Base.Type != "branch_summary" {
		return nil
	}
	var summary BranchSummaryEntry
	if json.Unmarshal(entry.Raw(), &summary) != nil {
		return nil
	}
	return &summary
}
