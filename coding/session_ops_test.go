package coding

import (
	"strings"
	"testing"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// helper: append a user msg directly via inner session, bypassing Send
// so tests don't need a real LLM.
func appendUser(t *testing.T, s *Session, text string) string {
	t.Helper()
	msg := agent.AgentMessage{
		User: &agent.UserMessage{
			Role:      "user",
			Content:   []ai.UserContentBlock{ai.TextContent{Text: text}},
			Timestamp: 1,
		},
	}
	id, err := s.inner.AppendMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func appendAsst(t *testing.T, s *Session, text string) string {
	t.Helper()
	msg := agent.AgentMessage{
		Assistant: &agent.AssistantMessage{
			Role:      "assistant",
			Content:   []ai.AssistantContentBlock{ai.TextContent{Text: text}},
			Timestamp: 2,
		},
	}
	id, err := s.inner.AppendMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// ─── Fork ────────────────────────────────────────────────────────────────────

func TestSessionForkRebuildsAgentMessages(t *testing.T) {
	// Public-API mirror of internal e2e test
	// TestE2E_ForkRewindsAgentHistoryAndExcludesAbandonedTail.
	// Proves the fork-and-rebuild bug we caught in 3.1c stays fixed
	// at the SDK layer.
	svcs := newTestServices(t)
	sess, _ := NewSession(svcs, SessionOptions{Model: fakeModel()})
	defer func() { _ = sess.Close() }()

	uid1 := appendUser(t, sess, "first question")
	aid1 := appendAsst(t, sess, "first answer")
	appendUser(t, sess, "DEAD-END question")
	appendAsst(t, sess, "DEAD-END answer")

	// Rebuild agent's in-memory state to match disk so we can fork.
	sess.agent.SetMessages(sess.inner.BuildContext(nil))
	if got := sess.Messages(); len(got) != 4 || got[0].User == nil {
		t.Fatalf("pre-fork: want 4 directly appended conversation messages, got %v", got)
	}

	// Fork at the assistant reply to the first turn.
	if err := sess.Fork(aid1); err != nil {
		t.Fatal(err)
	}
	got := sess.Messages()
	if len(got) != 2 || got[0].User == nil {
		t.Fatalf("post-fork: want 2 retained conversation messages, got %v", got)
	}
	for _, m := range got {
		txt := messageText(m)
		if strings.Contains(txt, "DEAD-END") {
			t.Errorf("abandoned tail leaked: %q", txt)
		}
	}
	_ = uid1
}

// ─── Clone ───────────────────────────────────────────────────────────────────

func TestSessionCloneCreatesIndependentFile(t *testing.T) {
	svcs := newTestServices(t)
	sess, _ := NewSession(svcs, SessionOptions{Model: fakeModel()})
	defer func() { _ = sess.Close() }()
	appendUser(t, sess, "shared turn")
	appendAsst(t, sess, "shared reply")

	cloned, err := sess.Clone()
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	defer func() { _ = cloned.Close() }()

	if cloned.Path() == sess.Path() {
		t.Fatal("clone should have a distinct path")
	}
	if got := cloned.Messages(); len(got) != 2 || got[0].User == nil || got[1].Assistant == nil {
		t.Errorf("clone agent should have only the directly persisted user and assistant; got %v", got)
	}
}

// ─── Tree, LeafID ────────────────────────────────────────────────────────────

// ─── SetName ─────────────────────────────────────────────────────────────────

// ─── DispatchSlash ───────────────────────────────────────────────────────────

// ─── helpers ────────────────────────────────────────────────────────────────

func messageText(m agent.AgentMessage) string {
	switch {
	case m.User != nil:
		var b strings.Builder
		for _, c := range m.User.Content {
			if t, ok := c.(ai.TextContent); ok {
				b.WriteString(t.Text)
			}
		}
		return b.String()
	case m.Assistant != nil:
		var b strings.Builder
		for _, c := range m.Assistant.Content {
			if t, ok := c.(ai.TextContent); ok {
				b.WriteString(t.Text)
			}
		}
		return b.String()
	}
	return ""
}
