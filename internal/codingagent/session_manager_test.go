package codingagent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alexrudloff/wopr/agent"
)

// ForkFromFile (`wopr --fork`) copies the FULL entry tree from a
// source file into a fresh session file recording parentSession: unlike
// Clone, which keeps only the linear path to a chosen leaf.
func TestSessionManagerForkFromFile_CopiesFullTreeToNewFile(t *testing.T) {
	dir := t.TempDir()
	sm := NewSessionManagerWithDir("/tmp/test-forkfrom", dir)
	src, _ := sm.Create("sess-forkfrom", "")
	u1, _ := src.AppendMessage(mkUserMsg("u1"))
	a1, _ := src.AppendMessage(mkAssistantMsg("a1"))
	// Branch off u1 so a1 becomes an orphan tail; append an assistant on
	// the new branch to flush the whole tree (incl. a1) to disk.
	_ = src.Fork(u1)
	u2, _ := src.AppendMessage(mkUserMsg("branch-b"))
	a2, _ := src.AppendMessage(mkAssistantMsg("reply-b"))

	forked, err := sm.ForkFromFile(src.Path())
	if err != nil {
		t.Fatalf("ForkFromFile: %v", err)
	}
	if forked.Path() == src.Path() {
		t.Errorf("--fork must create a NEW file, got same path as source")
	}
	if forked.ID() == src.ID() {
		t.Errorf("forked session must have a fresh id, got %q", forked.ID())
	}
	absSrc, _ := filepath.Abs(src.Path())
	if forked.ParentSession() != absSrc {
		t.Errorf("parentSession=%q want %q", forked.ParentSession(), absSrc)
	}
	// Full tree preserved, including the orphan a1 that Clone would drop.
	for _, id := range []string{u1, a1, u2, a2} {
		if _, ok := forked.EntryByID(id); !ok {
			t.Errorf("forked session missing entry %s (full tree not copied)", id)
		}
	}
	// Source file stays intact and independent.
	if _, err := os.Stat(src.Path()); err != nil {
		t.Errorf("source file gone after fork: %v", err)
	}
}

func TestSessionManager_CreateAndLoad(t *testing.T) {
	dir := t.TempDir()
	sm := NewSessionManagerWithDir("/tmp/test", dir)
	sess, err := sm.Create("roundtrip-1", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	flushSession(t, sess)
	path := sess.Path()

	sm2 := NewSessionManagerWithDir("/tmp/test", dir)
	loaded, err := sm2.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.ID() != "roundtrip-1" {
		t.Errorf("loaded ID = %q, want roundtrip-1", loaded.ID())
	}
	if loaded.CWD() != "/tmp/test" {
		t.Errorf("loaded CWD = %q, want /tmp/test", loaded.CWD())
	}
}

func TestSessionManager_Load_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	badFile := filepath.Join(dir, "bad.jsonl")
	if err := os.WriteFile(badFile, []byte("not json at all\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sm := NewSessionManagerWithDir("/tmp/test", dir)
	_, err := sm.Load(badFile)
	if err == nil {
		t.Fatal("expected error loading invalid JSON")
	}
}

func TestSessionManager_SaveAndReload(t *testing.T) {
	dir := t.TempDir()
	sm := NewSessionManagerWithDir("/tmp/test", dir)
	sess, err := sm.Create("persist-1", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Append an entry via AppendEntry. Use an assistant role so the
	// session flushes to disk.
	entry := MessageEntry{
		Type:      "message",
		ID:        "entry-001",
		ParentID:  nil,
		Timestamp: "2026-01-01T00:00:00Z",
		Message:   agent.AgentMessage{Assistant: &agent.AssistantMessage{Role: agent.RoleAssistant}},
	}
	if err := sess.AppendEntry(entry); err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}

	// Reload
	sm2 := NewSessionManagerWithDir("/tmp/test", dir)
	loaded, err := sm2.Load(sess.Path())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	entries := loaded.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Base.ID != "entry-001" {
		t.Errorf("entry ID = %q, want entry-001", entries[0].Base.ID)
	}
	if entries[0].Base.Type != "message" {
		t.Errorf("entry type = %q, want message", entries[0].Base.Type)
	}
}
