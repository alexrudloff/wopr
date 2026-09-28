package coding

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

func newTreeTestSession(t *testing.T) *Session {
	t.Helper()
	sess, err := NewSession(newTestServices(t), SessionOptions{Model: fakeModel()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func appendTreeUser(t *testing.T, sess *Session, text string) string {
	t.Helper()
	id, err := sess.inner.AppendMessage(agent.AgentMessage{User: &agent.UserMessage{
		Role:    "user",
		Content: []ai.UserContentBlock{ai.TextContent{Text: text}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func appendTreeAssistant(t *testing.T, sess *Session, text string) string {
	t.Helper()
	id, err := sess.inner.AppendMessage(agent.AgentMessage{Assistant: &agent.AssistantMessage{
		Role:       agent.RoleAssistant,
		Content:    []ai.AssistantContentBlock{ai.TextContent{Text: text}},
		StopReason: ai.StopReasonStop,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// promptCapturingCompleter records each summarization prompt.
type promptCapturingCompleter struct {
	summary string
	prompts []string
}

func (c *promptCapturingCompleter) CompleteSimple(_ context.Context, _ *ai.Model, _ string, messages []agent.AgentMessage, _ ai.StreamOptions) (string, *ai.Usage, error) {
	var prompt strings.Builder
	for _, message := range messages {
		if message.User == nil {
			continue
		}
		for _, block := range message.User.Content {
			if text, ok := block.(ai.TextContent); ok {
				prompt.WriteString(text.Text)
			}
		}
	}
	c.prompts = append(c.prompts, prompt.String())
	return c.summary, nil, nil
}

func treeLeaf(sess *Session) string {
	if leaf := sess.inner.LeafID(); leaf != nil {
		return *leaf
	}
	return "<nil>"
}

// Tree navigation tests use a fake summarizer. WOPR sessions start with bootstrap
// model/thinking entries, so the first user message is not a root entry unless
// the leaf is reset before it is appended.

func TestNavigateTreeSummaryAttachesToNestedUserParent(t *testing.T) {
	sess := newTreeTestSession(t)
	sess.completer = &fakeCompleter{summary: "summary"}
	appendTreeUser(t, sess, "Message one")
	a1 := appendTreeAssistant(t, sess, "a1")
	u2 := appendTreeUser(t, sess, "Message two")
	appendTreeAssistant(t, sess, "a2")
	appendTreeUser(t, sess, "Message three")
	appendTreeAssistant(t, sess, "a3")

	res, err := sess.NavigateTree(context.Background(), u2, NavigateTreeOptions{Summarize: true})
	if err != nil {
		t.Fatalf("NavigateTree: %v", err)
	}
	if res.Cancelled || res.EditorText != "Message two" || res.SummaryEntry == nil {
		t.Fatalf("result = %+v", res)
	}
	if res.SummaryEntry.ParentID == nil || *res.SummaryEntry.ParentID != a1 {
		t.Fatalf("summary parentId = %v, want %s", res.SummaryEntry.ParentID, a1)
	}
	var childTypes []string
	for _, entry := range sess.inner.Entries() {
		if entry.Base.ParentID != nil && *entry.Base.ParentID == a1 {
			childTypes = append(childTypes, entry.Base.Type)
		}
	}
	if len(childTypes) != 2 || !slices.Contains(childTypes, "branch_summary") || !slices.Contains(childTypes, "message") {
		t.Fatalf("children of a1 = %v, want message and branch_summary", childTypes)
	}
}

func TestNavigateTreeBetweenBranchesSummarizesLeftBranch(t *testing.T) {
	sess := newTreeTestSession(t)
	completer := &promptCapturingCompleter{summary: "left branch"}
	sess.completer = completer
	appendTreeUser(t, sess, "Main branch start")
	a1 := appendTreeAssistant(t, sess, "a1")
	u2 := appendTreeUser(t, sess, "Main branch continue")
	appendTreeAssistant(t, sess, "a2")
	if err := sess.inner.Fork(a1); err != nil {
		t.Fatal(err)
	}
	appendTreeUser(t, sess, "Branch path")
	appendTreeAssistant(t, sess, "a3")

	res, err := sess.NavigateTree(context.Background(), u2, NavigateTreeOptions{Summarize: true})
	if err != nil {
		t.Fatalf("NavigateTree: %v", err)
	}
	if res.Cancelled || res.EditorText != "Main branch continue" || res.SummaryEntry == nil || res.SummaryEntry.Summary == "" {
		t.Fatalf("result = %+v", res)
	}
	if len(completer.prompts) != 1 || !strings.Contains(completer.prompts[0], "Branch path") || strings.Contains(completer.prompts[0], "Main branch continue") {
		t.Fatalf("summary prompt did not cover exactly the branch being left: %q", completer.prompts)
	}
}

// Aborting during summarization returns
// { cancelled: true, aborted: true } and leaves the session unchanged.
func TestNavigateTreeAbortDuringSummarization(t *testing.T) {
	sess := newTreeTestSession(t)
	completer := newBlockingCompactionCompleter()
	sess.completer = completer
	target := appendTreeUser(t, sess, "Tell me about something")
	appendTreeAssistant(t, sess, "a1")
	appendTreeUser(t, sess, "Continue")
	leafBefore := appendTreeAssistant(t, sess, "a2")
	entriesBefore := len(sess.inner.Entries())

	type outcome struct {
		res NavigateTreeResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := sess.NavigateTree(context.Background(), target, NavigateTreeOptions{Summarize: true})
		done <- outcome{res, err}
	}()
	<-completer.started
	if !sess.IsCompacting() {
		t.Fatal("IsCompacting = false during branch summarization")
	}
	sess.AbortBranchSummary()
	got := <-done

	if got.err != nil {
		t.Fatalf("NavigateTree: %v", got.err)
	}
	if !got.res.Cancelled || !got.res.Aborted || got.res.SummaryEntry != nil {
		t.Fatalf("result = %+v, want cancelled and aborted without summary", got.res)
	}
	if n := len(sess.inner.Entries()); n != entriesBefore {
		t.Fatalf("entries = %d, want %d", n, entriesBefore)
	}
	if leaf := treeLeaf(sess); leaf != leafBefore {
		t.Fatalf("leaf = %s, want %s", leaf, leafBefore)
	}
}
