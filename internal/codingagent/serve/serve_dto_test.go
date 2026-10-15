package serve

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/coding"
	host "github.com/alexrudloff/wopr/coding/host"
)

// TestMessageDTODecode tests that messageDTO decodes correctly.
func TestMessageDTODecode(t *testing.T) {
	dto := messageDTO{
		Info: messageInfoDTO{
			ID:        "msg-1",
			SessionID: "sess-1",
			Role:      "assistant",
			Time:      messageTime{Created: 1700000000000},
		},
		Parts: []partDTO{
			{ID: "p1", MessageID: "msg-1", SessionID: "sess-1", Type: "text", Text: "Hello"},
			{ID: "p2", MessageID: "msg-1", SessionID: "sess-1", Type: "tool", Tool: "bash", CallID: "call-1", State: json.RawMessage(`{"status":"pending"}`)},
		},
	}

	data, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}

	var mirror struct {
		Info struct {
			ID        string                  `json:"id"`
			SessionID string                  `json:"sessionID"`
			Role      string                  `json:"role"`
			Time      struct{ Created int64 } `json:"time"`
		} `json:"info"`
		Parts []struct {
			ID        string          `json:"id"`
			MessageID string          `json:"messageID"`
			SessionID string          `json:"sessionID"`
			Type      string          `json:"type"`
			Text      string          `json:"text,omitempty"`
			Tool      string          `json:"tool,omitempty"`
			CallID    string          `json:"callID,omitempty"`
			State     json.RawMessage `json:"state,omitempty"`
		} `json:"parts"`
	}

	if err := json.Unmarshal(data, &mirror); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if mirror.Info.ID != dto.Info.ID {
		t.Errorf("info.id: expected %q, got %q", dto.Info.ID, mirror.Info.ID)
	}
	if mirror.Info.SessionID != "sess-1" {
		t.Errorf("info.sessionID: expected sess-1, got %q", mirror.Info.SessionID)
	}
	if mirror.Info.Role != "assistant" {
		t.Errorf("info.role: expected assistant, got %q", mirror.Info.Role)
	}
	if mirror.Info.Time.Created != 1700000000000 {
		t.Errorf("info.time.created: expected 1700000000000, got %d", mirror.Info.Time.Created)
	}
	if len(mirror.Parts) != 2 {
		t.Errorf("expected 2 parts, got %d", len(mirror.Parts))
	}
	if mirror.Parts[0].ID != "p1" {
		t.Errorf("parts[0].id: expected p1, got %q", mirror.Parts[0].ID)
	}
	if mirror.Parts[0].Text != "Hello" {
		t.Errorf("parts[0].text: expected Hello, got %q", mirror.Parts[0].Text)
	}
	if mirror.Parts[1].CallID != "call-1" {
		t.Errorf("parts[1].callID: expected call-1, got %q", mirror.Parts[1].CallID)
	}
}

// TestSessionStatusDTODecode tests sessionStatusDTO.
func TestSessionStatusDTODecode(t *testing.T) {
	dto := sessionStatusDTO{Type: "busy"}
	data, _ := json.Marshal(dto)

	var mirror struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &mirror); err != nil {
		t.Fatal(err)
	}
	if mirror.Type != "busy" {
		t.Errorf("expected busy, got %q", mirror.Type)
	}
}

// TestProjectDTODecode tests projectDTO.
func TestProjectDTODecode(t *testing.T) {
	dto := projectDTO{ID: "abc123", Worktree: "/home/user/project"}
	data, _ := json.Marshal(dto)

	var mirror struct {
		ID       string `json:"id"`
		Worktree string `json:"worktree"`
	}
	if err := json.Unmarshal(data, &mirror); err != nil {
		t.Fatal(err)
	}
	if mirror.ID != "abc123" || mirror.Worktree != "/home/user/project" {
		t.Errorf("projectDTO mismatch")
	}
}

// TestErrorDTODecode tests errorDTO.
func TestErrorDTODecode(t *testing.T) {
	dto := errorDTO{Error: "something went wrong"}
	data, _ := json.Marshal(dto)

	var mirror struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(data, &mirror); err != nil {
		t.Fatal(err)
	}
	if mirror.Error != "something went wrong" {
		t.Errorf("error message mismatch")
	}
}

// TestRequestBodyFieldsAreBuiltins tests that all request body structs
// have only builtin field types (no named types that would leak pkg.Type in errors).
func TestRequestBodyFieldsAreBuiltins(t *testing.T) {
	// The only request body in serve is the PATCH /session/{id} body.
	// It uses an inline struct with *string and *struct{Archived *int64} fields.
	// Both are builtin or anonymous struct types.

	// Use reflection to check all types that serve decodes into.
	// We reflect the production type, not a test-local copy (MAJOR 6).
	checkStruct(t, reflect.TypeFor[patchSessionBody]())
}

func checkStruct(t *testing.T, typ reflect.Type) {
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return
	}
	for field := range typ.Fields() {
		checkFieldType(t, field.Type)
	}
}

func checkFieldType(t *testing.T, typ reflect.Type) {
	// Dereference pointers, slices, maps, arrays.
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map || typ.Kind() == reflect.Array {
		switch typ.Kind() {
		case reflect.Pointer:
			typ = typ.Elem()
		case reflect.Slice:
			typ = typ.Elem()
		case reflect.Map:
			typ = typ.Elem()
		case reflect.Array:
			typ = typ.Elem()
		}
	}

	// The key check: for builtin types, PkgPath() is empty.
	// Named types from packages (like codingagent.SessionHeader) have a non-empty PkgPath().
	// Anonymous structs also have empty PkgPath().
	if typ.PkgPath() != "" {
		t.Errorf("request body field has named type %s (pkg.Type would leak in errors)", typ.String())
	}
}

// TestPatchThenGetAgrees tests that PATCH /session/{id} then GET /session/{id}
// returns the same data in both reachable states: cold (file on disk) and live (no file).
// For each state, tests both PATCH bodies (rename and archive) - 4 cases total (MAJOR 7).
func TestPatchThenGetAgrees(t *testing.T) {
	// Case 1: cold session (file on disk), rename
	t.Run("cold_rename", func(t *testing.T) {
		agentDir := t.TempDir()

		services, err := coding.NewServices(coding.ServicesOptions{
			CWD:            t.TempDir(),
			AgentDir:       agentDir,
			ProjectTrusted: func() *bool { b := true; return &b }(),
		})
		if err != nil {
			t.Fatal(err)
		}
		host := host.New(services, nil)

		sessionDir := filepath.Join(agentDir, "sessions", "--test--")
		if err := os.MkdirAll(sessionDir, 0o755); err != nil {
			t.Fatal(err)
		}

		// Create a session file with header and two entries.
		id := "cold-session"
		path := filepath.Join(sessionDir, time.Now().UTC().Format("2006-01-02T15-04-05")+"_"+id+".jsonl")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		header := `{"type":"session","version":3,"id":"` + id + `","timestamp":"2024-01-01T00:00:00.000000000Z","cwd":"/test"}`
		_, _ = f.WriteString(header + "\n")
		entry1 := `{"type":"message","id":"e1","parentId":null,"timestamp":"2024-01-01T00:00:01.000000000Z","message":{"role":"user","content":[{"type":"text","text":"hi"}],"timestamp":1704067201000}}`
		_, _ = f.WriteString(entry1 + "\n")
		_ = f.Close()

		srv, ln, err := New("127.0.0.1:0", host, sessionDir, "test-faux/faux-1")
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = srv.Serve(ln) }()
		defer func() { _ = srv.Close() }()

		baseURL := "http://" + ln.Addr().String()

		// PATCH to rename
		patchBody := `{"title":"New Name"}`
		req, err := http.NewRequest("PATCH", baseURL+"/session/"+id, strings.NewReader(patchBody))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PATCH failed: %d", resp.StatusCode)
		}

		// Assert PATCH 200 body
		var patchDTO sessionDTO
		err = json.NewDecoder(resp.Body).Decode(&patchDTO)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()

		if patchDTO.Title != "New Name" {
			t.Errorf("PATCH title: expected 'New Name', got %q", patchDTO.Title)
		}
		if patchDTO.Time.Archived != nil {
			t.Errorf("PATCH time.archived: expected nil, got %v", *patchDTO.Time.Archived)
		}

		// GET to verify
		resp, err = http.Get(baseURL + "/session/" + id)
		if err != nil {
			t.Fatal(err)
		}
		var getDTO sessionDTO
		err = json.NewDecoder(resp.Body).Decode(&getDTO)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()

		// Assert PATCH == GET
		if patchDTO.Title != getDTO.Title {
			t.Errorf("title mismatch: PATCH=%q GET=%q", patchDTO.Title, getDTO.Title)
		}
		if (patchDTO.Time.Archived == nil) != (getDTO.Time.Archived == nil) {
			t.Errorf("archived presence mismatch: PATCH=%v GET=%v", patchDTO.Time.Archived, getDTO.Time.Archived)
		}
		if patchDTO.Time.Archived != nil && *patchDTO.Time.Archived != *getDTO.Time.Archived {
			t.Errorf("archived value mismatch: PATCH=%d GET=%d", *patchDTO.Time.Archived, *getDTO.Time.Archived)
		}
	})

	// Case 2: cold session (file on disk), archive
	t.Run("cold_archive", func(t *testing.T) {
		agentDir := t.TempDir()

		services, err := coding.NewServices(coding.ServicesOptions{
			CWD:            t.TempDir(),
			AgentDir:       agentDir,
			ProjectTrusted: func() *bool { b := true; return &b }(),
		})
		if err != nil {
			t.Fatal(err)
		}
		host := host.New(services, nil)

		sessionDir := filepath.Join(agentDir, "sessions", "--test--")
		if err := os.MkdirAll(sessionDir, 0o755); err != nil {
			t.Fatal(err)
		}

		id := "cold-session-archive"
		path := filepath.Join(sessionDir, time.Now().UTC().Format("2006-01-02T15-04-05")+"_"+id+".jsonl")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		header := `{"type":"session","version":3,"id":"` + id + `","timestamp":"2024-01-01T00:00:00.000000000Z","cwd":"/test"}`
		_, _ = f.WriteString(header + "\n")
		entry1 := `{"type":"message","id":"e1","parentId":null,"timestamp":"2024-01-01T00:00:01.000000000Z","message":{"role":"user","content":[{"type":"text","text":"hi"}],"timestamp":1704067201000}}`
		_, _ = f.WriteString(entry1 + "\n")
		_ = f.Close()

		srv, ln, err := New("127.0.0.1:0", host, sessionDir, "test-faux/faux-1")
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = srv.Serve(ln) }()
		defer func() { _ = srv.Close() }()

		baseURL := "http://" + ln.Addr().String()

		// PATCH to archive
		archiveTime := time.Now().UnixMilli()
		patchBody := fmt.Sprintf(`{"time":{"archived":%d}}`, archiveTime)
		req, err := http.NewRequest("PATCH", baseURL+"/session/"+id, strings.NewReader(patchBody))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PATCH failed: %d", resp.StatusCode)
		}

		// Assert PATCH 200 body
		var patchDTO sessionDTO
		err = json.NewDecoder(resp.Body).Decode(&patchDTO)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()

		if patchDTO.Time.Archived == nil {
			t.Errorf("PATCH time.archived: expected %d, got nil", archiveTime)
		} else if *patchDTO.Time.Archived != archiveTime {
			t.Errorf("PATCH time.archived: expected %d, got %d", archiveTime, *patchDTO.Time.Archived)
		}
		// Title should remain unchanged after archiving (only archived timestamp is set/cleared)

		// GET to verify
		resp, err = http.Get(baseURL + "/session/" + id)
		if err != nil {
			t.Fatal(err)
		}
		var getDTO sessionDTO
		err = json.NewDecoder(resp.Body).Decode(&getDTO)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()

		// Assert PATCH == GET
		if (patchDTO.Time.Archived == nil) != (getDTO.Time.Archived == nil) {
			t.Errorf("archived presence mismatch")
		} else if patchDTO.Time.Archived != nil && *patchDTO.Time.Archived != *getDTO.Time.Archived {
			t.Errorf("archived value mismatch: PATCH=%d GET=%d", *patchDTO.Time.Archived, *getDTO.Time.Archived)
		}
		if patchDTO.Title != getDTO.Title {
			t.Errorf("title mismatch: PATCH=%q GET=%q", patchDTO.Title, getDTO.Title)
		}
	})

	// Case 3: live session (no file yet), rename
	t.Run("live_rename", func(t *testing.T) {
		t.Setenv("WOPR_TEST_FAUX", "1")
		dir := t.TempDir()
		agentDir := t.TempDir()

		services, err := coding.NewServices(coding.ServicesOptions{
			CWD:            dir,
			AgentDir:       agentDir,
			ProjectTrusted: func() *bool { b := true; return &b }(),
		})
		if err != nil {
			t.Fatal(err)
		}
		host := host.New(services, nil)

		sessionDir := filepath.Join(agentDir, "sessions", "--"+strings.ReplaceAll(dir, "/", "-")+"--")
		if err := os.MkdirAll(sessionDir, 0o755); err != nil {
			t.Fatal(err)
		}

		srv, ln, err := New("127.0.0.1:0", host, sessionDir, "test-faux/faux-1")
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = srv.Serve(ln) }()
		defer func() { _ = srv.Close() }()

		baseURL := "http://" + ln.Addr().String()

		// Create session (POST /session)
		resp, err := http.Post(baseURL+"/session", "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		var createDTO sessionDTO
		err = json.NewDecoder(resp.Body).Decode(&createDTO)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		id := createDTO.ID

		// PATCH to rename
		patchBody := `{"title":"Live Session Name"}`
		req, err := http.NewRequest("PATCH", baseURL+"/session/"+id, strings.NewReader(patchBody))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PATCH failed: %d", resp.StatusCode)
		}

		// Assert PATCH 200 body
		var patchDTO sessionDTO
		err = json.NewDecoder(resp.Body).Decode(&patchDTO)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()

		if patchDTO.Title != "Live Session Name" {
			t.Errorf("PATCH title: expected 'Live Session Name', got %q", patchDTO.Title)
		}
		if patchDTO.Time.Archived != nil {
			t.Errorf("PATCH time.archived: expected nil, got %v", *patchDTO.Time.Archived)
		}

		// GET to verify
		resp, err = http.Get(baseURL + "/session/" + id)
		if err != nil {
			t.Fatal(err)
		}
		var getDTO sessionDTO
		err = json.NewDecoder(resp.Body).Decode(&getDTO)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()

		// Assert PATCH == GET
		if patchDTO.Title != getDTO.Title {
			t.Errorf("title mismatch: PATCH=%q GET=%q", patchDTO.Title, getDTO.Title)
		}
		if (patchDTO.Time.Archived == nil) != (getDTO.Time.Archived == nil) {
			t.Errorf("archived presence mismatch")
		}
	})

	// Case 4: live session (no file yet), archive
	t.Run("live_archive", func(t *testing.T) {
		t.Setenv("WOPR_TEST_FAUX", "1")
		dir := t.TempDir()
		agentDir := t.TempDir()

		services, err := coding.NewServices(coding.ServicesOptions{
			CWD:            dir,
			AgentDir:       agentDir,
			ProjectTrusted: func() *bool { b := true; return &b }(),
		})
		if err != nil {
			t.Fatal(err)
		}
		host := host.New(services, nil)

		sessionDir := filepath.Join(agentDir, "sessions", "--"+strings.ReplaceAll(dir, "/", "-")+"--")
		if err := os.MkdirAll(sessionDir, 0o755); err != nil {
			t.Fatal(err)
		}

		srv, ln, err := New("127.0.0.1:0", host, sessionDir, "test-faux/faux-1")
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = srv.Serve(ln) }()
		defer func() { _ = srv.Close() }()

		baseURL := "http://" + ln.Addr().String()

		// Create session (POST /session)
		resp, err := http.Post(baseURL+"/session", "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		var createDTO sessionDTO
		err = json.NewDecoder(resp.Body).Decode(&createDTO)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		id := createDTO.ID

		// PATCH to archive
		archiveTime := time.Now().UnixMilli()
		patchBody := fmt.Sprintf(`{"time":{"archived":%d}}`, archiveTime)
		req, err := http.NewRequest("PATCH", baseURL+"/session/"+id, strings.NewReader(patchBody))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PATCH failed: %d", resp.StatusCode)
		}

		// Assert PATCH 200 body
		var patchDTO sessionDTO
		err = json.NewDecoder(resp.Body).Decode(&patchDTO)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()

		if patchDTO.Time.Archived == nil {
			t.Errorf("PATCH time.archived: expected %d, got nil", archiveTime)
		} else if *patchDTO.Time.Archived != archiveTime {
			t.Errorf("PATCH time.archived: expected %d, got %d", archiveTime, *patchDTO.Time.Archived)
		}
		// Title should remain unchanged after archiving (only archived timestamp is set/cleared)

		// GET to verify
		resp, err = http.Get(baseURL + "/session/" + id)
		if err != nil {
			t.Fatal(err)
		}
		var getDTO sessionDTO
		err = json.NewDecoder(resp.Body).Decode(&getDTO)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()

		// Assert PATCH == GET
		if (patchDTO.Time.Archived == nil) != (getDTO.Time.Archived == nil) {
			t.Errorf("archived presence mismatch")
		} else if patchDTO.Time.Archived != nil && *patchDTO.Time.Archived != *getDTO.Time.Archived {
			t.Errorf("archived value mismatch: PATCH=%d GET=%d", *patchDTO.Time.Archived, *getDTO.Time.Archived)
		}
		if patchDTO.Title != getDTO.Title {
			t.Errorf("title mismatch: PATCH=%q GET=%q", patchDTO.Title, getDTO.Title)
		}
	})
}

// writeUnterminatedSession writes a session file whose last record has no
// trailing newline. writeTestSession terminates every record, which is exactly
// the property under test here: an append to such a file has to terminate the
// last line itself or it glues onto it.
func writeUnterminatedSession(t *testing.T, dir, id string, entries []string) string {
	t.Helper()
	header := `{"type":"session","version":3,"id":"` + id + `","timestamp":"2024-01-01T00:00:00.000000000Z","cwd":"/test"}`
	path := filepath.Join(dir, time.Now().UTC().Format("2006-01-02T15-04-05")+"_"+id+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(append([]string{header}, entries...), "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// sessionRequest issues one request against /session/{id} and returns the
// status and the raw body. The body is returned raw so a caller can measure
// key presence, which sessionTime.Archived's omitempty hides.
func sessionRequest(t *testing.T, method, baseURL, id, body string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
		req, err := http.NewRequest(method, baseURL+"/session/"+id, rdr)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		return readResponse(t, req)
	}
	req, err := http.NewRequest(method, baseURL+"/session/"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	return readResponse(t, req)
}

func readResponse(t *testing.T, req *http.Request) (int, []byte) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, data
}

// decodeDTO decodes a session response body into sessionDTO.
func decodeDTO(t *testing.T, body []byte) sessionDTO {
	t.Helper()
	var dto sessionDTO
	if err := json.Unmarshal(body, &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return dto
}

// archivedPresent reports whether the response body's time object carries an
// "archived" key. sessionDTO cannot answer this: sessionTime.Archived is
// omitempty, so a nil pointer and an absent key are the same wire value.
func archivedPresent(t *testing.T, body []byte) bool {
	t.Helper()
	var probe struct {
		Time map[string]json.RawMessage `json:"time"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		t.Fatalf("decode: %v", err)
	}
	_, present := probe.Time["archived"]
	return present
}

// assertTailLinesParse checks that the last two lines of a session file each
// unmarshal as a JSON object independently. A newline count cannot detect a
// glued append: the bad append adds exactly one "\n" either way.
func assertTailLinesParse(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if len(lines) < 2 {
		t.Fatalf("%s: want at least 2 lines, got %d", path, len(lines))
	}
	for _, line := range lines[len(lines)-2:] {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Errorf("tail line is not a standalone JSON object: %v\nline: %s", err, line)
		}
	}
}

// TestPatchRenameAndArchiveOnFileBackedSession drives PATCH /session/{id} over a
// real listener against sessions written straight to disk, the state every
// session is in after a restart or an import. It covers the three defects the
// file-backed reader and the file-level appends carried: newest-record-wins
// (title and archived stamp), the null that clears an archive stamp, and the
// missing separator on a file that does not end in a newline.
func TestPatchRenameAndArchiveOnFileBackedSession(t *testing.T) {
	agentDir := t.TempDir()

	services, err := coding.NewServices(coding.ServicesOptions{
		CWD:            t.TempDir(),
		AgentDir:       agentDir,
		ProjectTrusted: func() *bool { b := true; return &b }(),
	})
	if err != nil {
		t.Fatal(err)
	}
	h := host.New(services, nil)

	sessionDir := filepath.Join(agentDir, "sessions", "--test--")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Five fixtures, all file-backed. (a) rename, (b) archive-then-clear,
	// (e) re-archive: every record terminated. (c) archive append and
	// (d) rename append: last record unterminated, so the append under test
	// has to write the separator itself.
	const (
		idRename    = "file-backed-rename"
		idClear     = "file-backed-clear"
		idNoNLArch  = "file-backed-no-nl-archive"
		idNoNLName  = "file-backed-no-nl-rename"
		idReArchive = "file-backed-re-archive"
	)
	writeTestSession(t, sessionDir, idRename, []string{infoEntry("oldinfo01", "ORIGINAL-NAME")})
	writeTestSession(t, sessionDir, idClear, nil)
	writeTestSession(t, sessionDir, idReArchive, []string{archiveEntry("arc000002", 111)})
	pathNoNLArchive := writeUnterminatedSession(t, sessionDir, idNoNLArch, []string{archiveEntry("arc000001", 111)})
	pathNoNLRename := writeUnterminatedSession(t, sessionDir, idNoNLName, []string{infoEntry("oldinfo02", "ORIGINAL-NAME")})

	srv, ln, err := New("127.0.0.1:0", h, sessionDir, "test-faux/faux-1")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	base := "http://" + ln.Addr().String()

	// A rename must report the new title in the PATCH body and in a later GET.
	// Status alone is not evidence: the defect answered 200 while returning the
	// old title.
	t.Run("rename_takes_the_newest_session_info", func(t *testing.T) {
		status, body := sessionRequest(t, "PATCH", base, idRename, `{"title":"NEW-TITLE"}`)
		if status != http.StatusOK {
			t.Fatalf("PATCH status = %d, want 200", status)
		}
		if got := decodeDTO(t, body).Title; got != "NEW-TITLE" {
			t.Errorf("PATCH title = %q, want %q", got, "NEW-TITLE")
		}

		status, body = sessionRequest(t, "GET", base, idRename, "")
		if status != http.StatusOK {
			t.Fatalf("GET status = %d, want 200", status)
		}
		dto := decodeDTO(t, body)
		if dto.Title != "NEW-TITLE" {
			t.Errorf("GET title = %q, want %q", dto.Title, "NEW-TITLE")
		}
		if dto.Version != coding.Version {
			t.Errorf("GET version = %q, want %q", dto.Version, coding.Version)
		}
	})

	// The clear is a value, not a deletion: a null is the newest answer and must
	// not fall through to the older numeric stamp.
	t.Run("clear_takes_the_newest_archive_record", func(t *testing.T) {
		status, _ := sessionRequest(t, "PATCH", base, idClear, `{"time":{"archived":222}}`)
		if status != http.StatusOK {
			t.Fatalf("PATCH archive status = %d, want 200", status)
		}
		status, body := sessionRequest(t, "GET", base, idClear, "")
		if status != http.StatusOK {
			t.Fatalf("GET status = %d, want 200", status)
		}
		archived := decodeDTO(t, body).Time.Archived
		if archived == nil {
			t.Fatalf("GET time.archived absent, want 222")
		}
		if *archived != 222 {
			t.Errorf("GET time.archived = %d, want 222", *archived)
		}

		status, _ = sessionRequest(t, "PATCH", base, idClear, `{"time":{"archived":null}}`)
		if status != http.StatusOK {
			t.Fatalf("PATCH clear status = %d, want 200", status)
		}
		status, body = sessionRequest(t, "GET", base, idClear, "")
		if status != http.StatusOK {
			t.Fatalf("GET status = %d, want 200", status)
		}
		if archivedPresent(t, body) {
			t.Errorf("GET time.archived present after clear, want absent")
		}
	})

	// A second archive must win over the stamp already on disk.
	t.Run("rearchive_takes_the_newest_archive_record", func(t *testing.T) {
		status, _ := sessionRequest(t, "PATCH", base, idReArchive, `{"time":{"archived":222}}`)
		if status != http.StatusOK {
			t.Fatalf("PATCH status = %d, want 200", status)
		}
		status, body := sessionRequest(t, "GET", base, idReArchive, "")
		if status != http.StatusOK {
			t.Fatalf("GET status = %d, want 200", status)
		}
		archived := decodeDTO(t, body).Time.Archived
		if archived == nil {
			t.Fatalf("GET time.archived absent, want 222")
		}
		if *archived != 222 {
			t.Errorf("GET time.archived = %d, want 222 not the 111 on disk", *archived)
		}
	})

	// Both file-level appends have to terminate the file's last line. (c) is the
	// archive append, (d) the rename append; (d) is the only assertion anywhere
	// in this package that covers RenameSession's separator.
	t.Run("appends_terminate_an_unterminated_last_line", func(t *testing.T) {
		status, _ := sessionRequest(t, "PATCH", base, idNoNLArch, `{"time":{"archived":222}}`)
		if status != http.StatusOK {
			t.Fatalf("PATCH archive status = %d, want 200", status)
		}
		assertTailLinesParse(t, pathNoNLArchive)

		status, _ = sessionRequest(t, "PATCH", base, idNoNLName, `{"title":"NEW-TITLE"}`)
		if status != http.StatusOK {
			t.Fatalf("PATCH rename status = %d, want 200", status)
		}
		assertTailLinesParse(t, pathNoNLRename)
	})
}

// TestIDPatternValidation tests that the session ID pattern validation rejects
// invalid characters (., /, etc.) and accepts valid ones.
func TestIDPatternValidation(t *testing.T) {
	validIDs := []string{
		"abc123",
		"abc-def",
		"ABC123",
		"a",
		strings.Repeat("a", 64),
	}
	for _, id := range validIDs {
		if !sessionIDPattern.MatchString(id) {
			t.Errorf("expected %q to be valid, but it was rejected", id)
		}
	}

	invalidIDs := []string{
		"abc.def",
		"abc/def",
		"abc:def",
		"abc def",
		"",
		strings.Repeat("a", 65),
		"abc$def",
		"abc*def",
		"abc?def",
		"abc[def",
		"abc]def",
		"abc{def",
		"abc}def",
		"abc|def",
	}
	for _, id := range invalidIDs {
		if sessionIDPattern.MatchString(id) {
			t.Errorf("expected %q to be invalid, but it was accepted", id)
		}
	}
}

// TestProjectIDStable tests that projectID returns a stable 12-char hex string
// for the same input, and different for different inputs.
func TestProjectIDStable(t *testing.T) {
	// Same input should produce same output
	id1 := projectID("/home/user/project")
	id2 := projectID("/home/user/project")
	if id1 != id2 {
		t.Errorf("projectID not stable: %s != %s", id1, id2)
	}
	if len(id1) != 12 {
		t.Errorf("projectID length should be 12, got %d", len(id1))
	}
	for _, c := range id1 {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Errorf("projectID contains non-hex char: %c", c)
		}
	}

	// Different inputs should produce different outputs (with very high probability)
	id3 := projectID("/home/user/other")
	if id1 == id3 {
		t.Errorf("projectID collision: %s == %s", id1, id3)
	}
}
