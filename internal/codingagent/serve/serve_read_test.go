package serve

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/coding"
	codinghost "github.com/alexrudloff/wopr/coding/host"
	icodingagent "github.com/alexrudloff/wopr/internal/codingagent"
	"github.com/alexrudloff/wopr/internal/testenv"
)

// writeTestSession creates a test session file with the given entries (for *testing.T).
func writeTestSession(t *testing.T, dir, id string, entries []string) string {
	path := filepath.Join(dir, time.Now().UTC().Format("2006-01-02T15-04-05")+"_"+id+".jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	header := icodingagent.SessionHeader{
		Type:      "session",
		Version:   3,
		ID:        id,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		CWD:       "/test",
	}
	_, _ = f.WriteString(`{"type":"session","version":3,"id":"` + id + `","timestamp":"` + header.Timestamp + `","cwd":"/test"}` + "\n")

	for _, e := range entries {
		_, _ = f.WriteString(e + "\n")
	}
	_ = f.Close()
	return path
}

// writeTestSessionB creates a test session file with the given entries (for *testing.B).
func writeTestSessionB(b *testing.B, dir, id string, entries []string) string {
	path := filepath.Join(dir, time.Now().UTC().Format("2006-01-02T15-04-05")+"_"+id+".jsonl")
	f, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}

	header := icodingagent.SessionHeader{
		Type:      "session",
		Version:   3,
		ID:        id,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		CWD:       "/test",
	}
	_, _ = f.WriteString(`{"type":"session","version":3,"id":"` + id + `","timestamp":"` + header.Timestamp + `","cwd":"/test"}` + "\n")

	for _, e := range entries {
		_, _ = f.WriteString(e + "\n")
	}
	_ = f.Close()
	return path
}

// userEntry returns a user message entry JSON.
func userEntry(id, text string) string {
	return `{"type":"message","id":"` + id + `","parentId":null,"timestamp":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","message":{"role":"user","content":[{"type":"text","text":"` + text + `"}],"timestamp":` + strconv.FormatInt(time.Now().UnixMilli(), 10) + `}}`
}

// modelChangeEntry returns a model_change entry JSON.
func modelChangeEntry(id, provider, modelID string) string {
	return `{"type":"model_change","id":"` + id + `","parentId":null,"timestamp":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","provider":"` + provider + `","modelId":"` + modelID + `"}`
}

// infoEntry returns a session_info entry JSON.
func infoEntry(id, name string) string {
	return `{"type":"session_info","id":"` + id + `","parentId":null,"timestamp":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","name":"` + name + `"}`
}

// archiveEntry returns a session_archive custom entry JSON.
func archiveEntry(id string, archived int64) string {
	return `{"type":"custom","id":"` + id + `","parentId":null,"timestamp":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","customType":"session_archive","data":{"archived":` + strconv.FormatInt(archived, 10) + `}}`
}

// TestSessionMetaFindsTailMarkers tests that sessionMeta finds session_info and
// session_archive at the tail of a file whose head is larger than metaProbeBytes.
func TestSessionMetaFindsTailMarkers(t *testing.T) {
	dir := t.TempDir()
	id := "abc"

	entries := make([]string, 0, 502)
	for range 500 {
		entries = append(entries, userEntry(func() string { id, _ := icodingagent.GenerateEntryID(); return id }(), "x"))
	}
	entries = append(entries, infoEntry(func() string { id, _ := icodingagent.GenerateEntryID(); return id }(), "Test Session"))
	entries = append(entries, archiveEntry(func() string { id, _ := icodingagent.GenerateEntryID(); return id }(), time.Now().UnixMilli()))

	path := writeTestSession(t, dir, id, entries)

	rec, ok := sessionRecordAt(path)
	if !ok {
		t.Fatal("sessionRecordAt failed")
	}
	if rec.Name != "Test Session" {
		t.Errorf("expected name 'Test Session', got %q", rec.Name)
	}
	if rec.Archived == nil {
		t.Errorf("expected archived to be set")
	}
}

// TestSessionMetaResidual tests that a marker placed more than metaProbeBytes
// before the end of the file is not found, and the title falls back.
func TestSessionMetaResidual(t *testing.T) {
	dir := t.TempDir()
	id := "def"

	entries := []string{infoEntry(func() string { id, _ := icodingagent.GenerateEntryID(); return id }(), "Old Name")}
	// Write enough entries to push the marker beyond metaProbeBytes (32 KiB).
	// Each entry is roughly 200 bytes, so 1000 entries = ~200 KiB.
	for range 2000 {
		entries = append(entries, userEntry(func() string { id, _ := icodingagent.GenerateEntryID(); return id }(), "padding"))
	}

	path := writeTestSession(t, dir, id, entries)

	rec, ok := sessionRecordAt(path)
	if !ok {
		t.Fatal("sessionRecordAt failed")
	}
	if rec.Name == "Old Name" {
		t.Errorf("expected old name to be missed due to bound, got %q", rec.Name)
	}
}

// TestTitleFallback tests that the title falls back to first user message when no name exists.
func TestTitleFallback(t *testing.T) {
	dir := t.TempDir()
	id := "ghi"

	entries := []string{userEntry(func() string { id, _ := icodingagent.GenerateEntryID(); return id }(), "First user message")}
	path := writeTestSession(t, dir, id, entries)

	rec, ok := sessionRecordAt(path)
	if !ok {
		t.Fatal("sessionRecordAt failed")
	}
	if rec.Name != "First user message" {
		t.Errorf("expected fallback title 'First user message', got %q", rec.Name)
	}
}

// TestTailEntries tests that tailEntries returns the last N entries in ascending order.
func TestTailEntries(t *testing.T) {
	dir := t.TempDir()
	id := "jkl"

	entries := make([]string, 10)
	baseTime := time.Now().UTC()
	for i := range 10 {
		id := func() string { id, _ := icodingagent.GenerateEntryID(); return id }()
		ts := baseTime.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano)
		entries[i] = `{"type":"message","id":"` + id + `","parentId":null,"timestamp":"` + ts + `","message":{"role":"user","content":[{"type":"text","text":"msg"}],"timestamp":` + strconv.FormatInt(baseTime.Add(time.Duration(i)*time.Millisecond).UnixMilli(), 10) + `}}`
	}

	path := writeTestSession(t, dir, id, entries)
	result, err := tailEntries(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 3 {
		t.Errorf("expected 3 entries, got %d", len(result))
	}
	// Debug: print timestamps
	for i, r := range result {
		t.Logf("Result %d: id=%s, timestamp=%s", i, r.Base.ID, r.Base.Timestamp)
	}
	for i := 1; i < len(result); i++ {
		if result[i].Base.Timestamp < result[i-1].Base.Timestamp {
			t.Errorf("entries not in ascending order: %s >= %s", result[i-1].Base.Timestamp, result[i].Base.Timestamp)
		}
	}
}

// TestTailEntriesSkipsMalformed tests that a malformed interior record is skipped.
func TestTailEntriesSkipsMalformed(t *testing.T) {
	dir := t.TempDir()
	id := "mno"

	// Create 3 valid entries, then malformed, then 3 valid entries.
	// Request limit=5: the last 5 entries are (valid, malformed, valid, valid, valid).
	// The malformed one should be skipped, returning 4 valid entries.
	entries := make([]string, 0, 7)
	for range 3 {
		entries = append(entries, userEntry(func() string { id, _ := icodingagent.GenerateEntryID(); return id }(), "msg"))
	}
	entries = append(entries, "not valid json")
	for range 3 {
		entries = append(entries, userEntry(func() string { id, _ := icodingagent.GenerateEntryID(); return id }(), "msg"))
	}

	path := writeTestSession(t, dir, id, entries)
	result, err := tailEntries(path, 5)
	if err != nil {
		t.Fatal(err)
	}
	// Malformed line is skipped, so we get 4 valid entries (the 3 after + 1 before malformed)
	if len(result) != 4 {
		t.Errorf("expected 4 entries (skipping malformed), got %d", len(result))
	}
}

// TestWorkingTreePaths tests workingTreePaths against a fixture git repository.
func TestWorkingTreePaths(t *testing.T) {
	dir := t.TempDir()

	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test")
	cmd.Dir = dir
	_ = cmd.Run()

	file1 := filepath.Join(dir, "file1.txt")
	if err := os.WriteFile(file1, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", "file1.txt")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = dir
	_ = cmd.Run()

	if err := os.WriteFile(file1, []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}

	file2 := filepath.Join(dir, "untracked.txt")
	if err := os.WriteFile(file2, []byte("untracked"), 0o644); err != nil {
		t.Fatal(err)
	}

	file3 := filepath.Join(dir, "uni é.txt")
	if err := os.WriteFile(file3, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	file4 := filepath.Join(dir, "renamed.txt")
	if err := os.WriteFile(file4, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", "renamed.txt")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "add renamed")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "mv", "renamed.txt", "renamed2.txt")
	cmd.Dir = dir
	_ = cmd.Run()

	paths, err := workingTreePaths(dir)
	if err != nil {
		t.Fatal(err)
	}

	expected := map[string]bool{
		"file1.txt":     true,
		"untracked.txt": true,
		"uni é.txt":     true,
		"renamed2.txt":  true,
	}
	found := make(map[string]bool)
	for _, p := range paths {
		found[p] = true
	}
	for k := range expected {
		if !found[k] {
			t.Errorf("expected path %q not found", k)
		}
	}
	if found["renamed.txt"] {
		t.Errorf("renamed source 'renamed.txt' should be skipped, but was included")
	}
}

// TestWorkingTreePathsNonRepo tests that workingTreePaths returns empty slice outside a git repo.
func TestWorkingTreePathsNonRepo(t *testing.T) {
	dir := t.TempDir()
	paths, err := workingTreePaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Errorf("expected empty slice outside git repo, got %d paths", len(paths))
	}
}

// TestResolveInRoot tests the path allowlist.
func TestResolveInRoot(t *testing.T) {
	root := t.TempDir()

	subdir := filepath.Join(root, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(subdir, "file.txt")
	if err := os.WriteFile(file, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	resolved, err := resolveInRoot(root, "subdir/file.txt")
	if err != nil {
		t.Errorf("expected resolved path, got error: %v", err)
	}
	if resolved != file {
		t.Errorf("expected %q, got %q", file, resolved)
	}

	_, err = resolveInRoot(root, "../outside")
	if err == nil {
		t.Error("expected error for path escaping root")
	}

	_, err = resolveInRoot(root, "/etc/passwd")
	if err == nil {
		t.Error("expected error for absolute path")
	}

	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	testenv.Symlink(t, outsideFile, link)
	_, err = resolveInRoot(root, "link.txt")
	if err == nil {
		t.Error("expected error for symlink out of root")
	}
}

// TestEntriesToMessagesResolvesParentPastMetadata pins that an assistant
// message reports the nearest MESSAGE ancestor as its parentID, not the raw
// parentId. Online Context Compact interposes a custom
// harness-online-context-state-v1 entry between the user message and the
// assistant, so the raw parentId names a non-message entry. userEntry cannot
// build the custom/assistant entries or their parent links (it hardcodes
// type:message and parentId:null), so those two are hand-written.
func TestEntriesToMessagesResolvesParentPastMetadata(t *testing.T) {
	dir := t.TempDir()
	id := "parent-past-metadata"
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	path := writeTestSession(t, dir, id, []string{
		userEntry("u1", "hello"),
		`{"type":"custom","id":"c1","parentId":"u1","timestamp":"` + ts + `","customType":"harness-online-context-state-v1","data":{}}`,
		`{"type":"message","id":"a1","parentId":"c1","timestamp":"` + ts + `","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"api":"faux","provider":"test-faux","model":"faux-1","usage":{"input":1,"output":1,"cache":{"read":0,"write":0}},"stopReason":"stop","timestamp":1704067201000}}`,
	})

	entries, err := tailEntries(path, maxLimit)
	if err != nil {
		t.Fatal(err)
	}
	messages := entriesToMessages(entries, id, "/test", "/test", "", "")

	var got string
	found := false
	for _, m := range messages {
		if m.Info.ID == "a1" {
			got = m.Info.ParentID
			found = true
		}
	}
	if !found {
		t.Fatalf("assistant a1 not in transcript: %+v", messages)
	}
	if got != "u1" {
		t.Errorf("a1 parentID: expected the nearest message ancestor %q, got %q", "u1", got)
	}
}

// TestEntriesToMessagesAssistantParentsOnTurnUserInMultiStepToolTurn pins that
// every assistant of a multi-step tool turn reports the turn's initiating user
// message as its transcript parentID, matching the stream's turnUserID. The
// chain is u1 → c1 → a1 → t1(toolResult) → c2 → a2; a2's raw parent is c2, whose
// nearest message ancestor is t1, so a nearest-message walk would report t1 for
// a2 while a1 reports u1. custom and assistant/toolResult entries are
// hand-written for explicit parent links (userEntry hardcodes parentId:null).
func TestEntriesToMessagesAssistantParentsOnTurnUserInMultiStepToolTurn(t *testing.T) {
	dir := t.TempDir()
	id := "parent-multistep"
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	path := writeTestSession(t, dir, id, []string{
		userEntry("u1", "hello"),
		`{"type":"custom","id":"c1","parentId":"u1","timestamp":"` + ts + `","customType":"harness-online-context-state-v1","data":{}}`,
		`{"type":"message","id":"a1","parentId":"c1","timestamp":"` + ts + `","message":{"role":"assistant","content":[{"type":"toolCall","id":"call_1","name":"bash","arguments":{"command":"echo hi"}}],"api":"faux","provider":"test-faux","model":"faux-1","usage":{"input":1,"output":1,"cache":{"read":0,"write":0}},"stopReason":"toolUse","timestamp":1704067201000}}`,
		`{"type":"message","id":"t1","parentId":"a1","timestamp":"` + ts + `","message":{"role":"toolResult","toolCallId":"call_1","toolName":"bash","content":[{"type":"text","text":"hi\n"}],"isError":false,"timestamp":1704067202000}}`,
		`{"type":"custom","id":"c2","parentId":"t1","timestamp":"` + ts + `","customType":"harness-online-context-state-v1","data":{}}`,
		`{"type":"message","id":"a2","parentId":"c2","timestamp":"` + ts + `","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"api":"faux","provider":"test-faux","model":"faux-1","usage":{"input":1,"output":1,"cache":{"read":0,"write":0}},"stopReason":"stop","timestamp":1704067203000}}`,
	})

	entries, err := tailEntries(path, maxLimit)
	if err != nil {
		t.Fatal(err)
	}
	messages := entriesToMessages(entries, id, "/test", "/test", "", "")

	parents := make(map[string]string, len(messages))
	for _, m := range messages {
		parents[m.Info.ID] = m.Info.ParentID
	}
	for _, tc := range []struct{ id, want string }{
		{"a1", "u1"},
		{"a2", "u1"},
	} {
		got, ok := parents[tc.id]
		if !ok {
			t.Fatalf("assistant %s not in transcript: %+v", tc.id, messages)
		}
		if got != tc.want {
			t.Errorf("%s parentID: expected the turn's user message %q, got %q", tc.id, tc.want, got)
		}
	}
}

// TestEntriesToMessagesUserTurnParentIsEmpty pins that a user-role row reports an
// empty parentID, matching the stream's user message.updated (serve_sse.go messageEnd).
// A later user turn's raw parent is the previous assistant, so the nearest-message walk
// would report a1; user u2 must report "". userEntry hardcodes parentId:null, so u2's
// parent link is hand-written. Assistant resolution is pinned by the sibling test.
func TestEntriesToMessagesUserTurnParentIsEmpty(t *testing.T) {
	dir := t.TempDir()
	id := "parent-user-turn"
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	path := writeTestSession(t, dir, id, []string{
		userEntry("u1", "first"),
		`{"type":"message","id":"a1","parentId":"u1","timestamp":"` + ts + `","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"api":"faux","provider":"test-faux","model":"faux-1","usage":{"input":1,"output":1,"cache":{"read":0,"write":0}},"stopReason":"stop","timestamp":1704067201000}}`,
		`{"type":"message","id":"u2","parentId":"a1","timestamp":"` + ts + `","message":{"role":"user","content":[{"type":"text","text":"second"}],"timestamp":1704067202000}}`,
	})

	entries, err := tailEntries(path, maxLimit)
	if err != nil {
		t.Fatal(err)
	}
	messages := entriesToMessages(entries, id, "/test", "/test", "", "")

	parents := make(map[string]string, len(messages))
	for _, m := range messages {
		parents[m.Info.ID] = m.Info.ParentID
	}
	if got, ok := parents["u2"]; !ok {
		t.Fatalf("user u2 not in transcript: %+v", messages)
	} else if got != "" {
		t.Errorf("u2 parentID: expected empty (matching the stream), got %q", got)
	}
	if got := parents["a1"]; got != "u1" {
		t.Errorf("a1 parentID: expected the turn's user message %q, got %q", "u1", got)
	}
}

// BenchmarkServeListRead benchmarks the list sessions read path.
func BenchmarkServeListRead(b *testing.B) {
	for _, entriesPerSession := range []int{400, 800} {
		b.Run("entries="+strconv.Itoa(entriesPerSession), func(b *testing.B) {
			b.ReportAllocs()
			dir := b.TempDir()
			agentDir := b.TempDir()

			for range 50 {
				id := func() string { id, _ := icodingagent.GenerateEntryID(); return id }()
				entries := make([]string, entriesPerSession)
				for j := range entriesPerSession {
					entries[j] = userEntry(func() string { id, _ := icodingagent.GenerateEntryID(); return id }(), "msg")
				}
				writeTestSessionB(b, dir, id, entries)
			}

			services, _ := coding.NewServices(coding.ServicesOptions{
				CWD:            b.TempDir(),
				AgentDir:       agentDir,
				ProjectTrusted: func() *bool { b := true; return &b }(),
			})
			host := codinghost.New(services, nil)
			_ = &Server{services: services, host: host, sessionDir: dir}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				recs := make([]sessionRecord, 0, 50)
				entries, _ := os.ReadDir(dir)
				for _, entry := range entries {
					if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
						continue
					}
					rec, ok := sessionRecordAt(filepath.Join(dir, entry.Name()))
					if ok {
						recs = append(recs, rec)
					}
				}
				if len(recs) != 50 {
					b.Fatalf("expected 50 sessions, got %d", len(recs))
				}
			}
		})
	}
}

// BenchmarkServeTailRead benchmarks the tail read path.
func BenchmarkServeTailRead(b *testing.B) {
	for _, entriesPerSession := range []int{400, 800} {
		b.Run("entries="+strconv.Itoa(entriesPerSession), func(b *testing.B) {
			b.ReportAllocs()
			dir := b.TempDir()
			agentDir := b.TempDir()

			for range 50 {
				id := func() string { id, _ := icodingagent.GenerateEntryID(); return id }()
				entries := make([]string, entriesPerSession)
				for j := range entriesPerSession {
					entries[j] = userEntry(func() string { id, _ := icodingagent.GenerateEntryID(); return id }(), "msg")
				}
				writeTestSessionB(b, dir, id, entries)
			}

			services, _ := coding.NewServices(coding.ServicesOptions{
				CWD:            b.TempDir(),
				AgentDir:       agentDir,
				ProjectTrusted: func() *bool { b := true; return &b }(),
			})
			host := codinghost.New(services, nil)
			_ = &Server{services: services, host: host, sessionDir: dir}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				totalEntries := 0
				entries, _ := os.ReadDir(dir)
				for _, entry := range entries {
					if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
						continue
					}
					path := filepath.Join(dir, entry.Name())
					entries, err := tailEntries(path, 20)
					if err != nil {
						b.Fatal(err)
					}
					totalEntries += len(entries)
				}
				if totalEntries != 50*20 {
					b.Fatalf("expected %d total entries, got %d", 50*20, totalEntries)
				}
			}
		})
	}
}

// TestTailEntriesMultiBlock tests that tailEntries returns exactly 'limit' entries
// when entries span multiple 64 KiB read blocks.
func TestTailEntriesMultiBlock(t *testing.T) {
	dir := t.TempDir()
	id := "multi-block-test"

	// Create entries that span at least 3 read blocks (64 KiB each = 192 KiB minimum).
	// Each entry is ~1.5 KiB (large text content). 200 entries = ~300 KiB > 3 blocks.
	entries := make([]string, 200)
	baseTime := time.Now().UTC()
	// Large content to ensure multi-block: 1.5 KiB per entry
	largeContent := strings.Repeat("x", 1500)
	for i := range 200 {
		id := func() string { id, _ := icodingagent.GenerateEntryID(); return id }()
		ts := baseTime.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano)
		entries[i] = `{"type":"message","id":"` + id + `","parentId":null,"timestamp":"` + ts + `","message":{"role":"user","content":[{"type":"text","text":"` + largeContent + `"}],"timestamp":` + strconv.FormatInt(baseTime.Add(time.Duration(i)*time.Millisecond).UnixMilli(), 10) + `}}`
	}

	path := writeTestSession(t, dir, id, entries)
	result, err := tailEntries(path, 20)
	if err != nil {
		t.Fatal(err)
	}

	// Must return exactly 20 entries (the limit), not fewer due to block counting bug.
	if len(result) != 20 {
		t.Errorf("expected exactly 20 entries, got %d", len(result))
	}

	// Verify they are distinct (no duplicates from block aliasing)
	ids := make(map[string]bool)
	for _, r := range result {
		if ids[r.Base.ID] {
			t.Errorf("duplicate entry ID: %s", r.Base.ID)
		}
		ids[r.Base.ID] = true
	}

	// Verify they are in ascending order (oldest first)
	for i := 1; i < len(result); i++ {
		if result[i].Base.Timestamp < result[i-1].Base.Timestamp {
			t.Errorf("entries not in ascending order: %s >= %s", result[i-1].Base.Timestamp, result[i].Base.Timestamp)
		}
	}

	// Verify the last entry is complete (not truncated)
	last := result[len(result)-1]
	var me icodingagent.MessageEntry
	if err := json.Unmarshal(last.Raw(), &me); err != nil {
		t.Fatalf("last entry not valid JSON: %v", err)
	}
	if me.Message.Role() != "user" {
		t.Errorf("expected user message, got %s", me.Message.Role())
	}
}
