package codingagent

import (
	"testing"
)

func TestSetSessionNamePersistsToDisk(t *testing.T) {
	sm := tempSessionMgr(t)
	sess, _ := sm.Create("sess-named", "")
	_, _ = sess.AppendMessage(mkUserMsg("hi"))

	// Mimic the closure in buildSlashContext.
	id := generateEntryID()
	parent := sess.LeafID()
	entry := SessionInfoEntry{
		Type:      "session_info",
		ID:        id,
		ParentID:  parent,
		Timestamp: "2026-04-29T19:00:00Z",
		Name:      "my project",
	}
	if err := sess.AppendEntry(entry); err != nil {
		t.Fatal(err)
	}
	flushSession(t, sess)

	// Reload and verify the name surfaces in SessionInfo.
	sm2 := NewSessionManagerWithDir(sm.cwd, sm.sessionDir)
	infos, err := sm2.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Name != "my project" {
		t.Errorf("name not persisted: %#v", infos)
	}
}
