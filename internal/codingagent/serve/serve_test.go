package serve

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/ai/aitest"
	"github.com/alexrudloff/wopr/coding"
	codinghost "github.com/alexrudloff/wopr/coding/host"
	"github.com/alexrudloff/wopr/coding/version"
	"github.com/alexrudloff/wopr/internal/codingagent/askuser"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
	"github.com/alexrudloff/wopr/internal/codingagent/web"
	"github.com/alexrudloff/wopr/internal/testenv"
)

// TestMain sets up hermetic test environment.
func TestMain(m *testing.M) {
	// Mirror coding/host/host_test.go:22-59
	testRoot, err := os.MkdirTemp("", "wopr-serve-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create isolated test config root:", err)
		os.Exit(2)
	}
	// Unset WOPR_CODING_AGENT_DIR and WOPR_CODING_AGENT_SESSION_DIR
	for _, key := range []string{"WOPR_CODING_AGENT_DIR", "WOPR_CODING_AGENT_SESSION_DIR"} {
		if err := os.Unsetenv(key); err != nil {
			fmt.Fprintf(os.Stderr, "clear inherited %s: %v\n", key, err)
			_ = os.RemoveAll(testRoot)
			os.Exit(2)
		}
	}
	// Unset every key from ai.ProviderCredentialEnvVars() and web.SearchEnvVars()
	for _, v := range append(ai.ProviderCredentialEnvVars(), web.SearchEnvVars()...) {
		_ = os.Unsetenv(v)
	}
	// Apply aitest.HermeticCredentialEnv
	for key, value := range aitest.HermeticCredentialEnv(testRoot) {
		_ = os.Setenv(key, value)
	}
	// Point WOPR_HOME at a temp dir removed afterwards
	if err := os.Setenv("WOPR_HOME", filepath.Join(testRoot, "wopr")); err != nil {
		fmt.Fprintln(os.Stderr, "set isolated WOPR_HOME:", err)
		_ = os.RemoveAll(testRoot)
		os.Exit(2)
	}
	code := m.Run()
	if err := os.RemoveAll(testRoot); err != nil {
		fmt.Fprintln(os.Stderr, "remove isolated test config root:", err)
		if code == 0 {
			code = 2
		}
	}
	os.Exit(code)
}

// TestHealthAnswersOverARealListener verifies that GET /global/health returns
// 200 with the correct content type and body over a real loopback listener.
func TestHealthAnswersOverARealListener(t *testing.T) {
	// Create minimal services and host for health check.
	cwd := t.TempDir()
	agentDir := t.TempDir()
	services, err := coding.NewServices(coding.ServicesOptions{
		CWD:            cwd,
		AgentDir:       agentDir,
		ProjectTrusted: func() *bool { b := true; return &b }(),
	})
	if err != nil {
		t.Fatal(err)
	}
	host := codinghost.New(services, nil)

	srv, ln, err := New("127.0.0.1:0", host, "", "test-faux/faux-1")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	resp, err := http.Get("http://" + ln.Addr().String() + "/global/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" || !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("expected Content-Type application/json, got %q", contentType)
	}

	var body struct {
		Healthy bool   `json:"healthy"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if !body.Healthy {
		t.Fatalf("expected healthy=true, got %v", body.Healthy)
	}
	if body.Version != version.Version {
		t.Fatalf("expected version=%q, got %q", version.Version, body.Version)
	}
}

// promptServer starts a serve server over a test-faux host and returns its base
// URL and the host behind it. WOPR_TEST_FAUX=1 is required: BuildModel refuses
// test-faux without it, so createSession would answer 500 instead of the code
// under test.
func promptServer(t *testing.T) (string, *codinghost.Host) {
	env := promptEnvFor(t)
	return env.baseURL, env.host
}

// promptEnv is promptServer's whole setup, kept so a restart test can start a
// second server over the same files with a host that owns nothing.
type promptEnv struct {
	baseURL    string
	host       *codinghost.Host
	services   *coding.Services
	agentDir   string
	sessionDir string
}

func promptEnvFor(t *testing.T) promptEnv {
	t.Helper()
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
	sessionDir := filepath.Join(agentDir, "sessions", "--"+strings.ReplaceAll(dir, "/", "-")+"--")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	host := codinghost.New(services, nil)
	srv, ln, err := New("127.0.0.1:0", host, sessionDir, "test-faux/faux-1")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return promptEnv{baseURL: "http://" + ln.Addr().String(), host: host, services: services, agentDir: agentDir, sessionDir: sessionDir}
}

// createPromptSession creates a live session and returns its id.
func createPromptSession(t *testing.T, baseURL string) string {
	t.Helper()
	resp, err := http.Post(baseURL+"/session", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /session: expected 200, got %d", resp.StatusCode)
	}
	var dto sessionDTO
	if err := json.NewDecoder(resp.Body).Decode(&dto); err != nil {
		t.Fatal(err)
	}
	return dto.ID
}

// postJSON sends a body to path and returns the status and the whole response
// body. It drains and closes the body itself, so a caller cannot leak one.
func postJSON(t *testing.T, baseURL, path, body string) (int, []byte) {
	t.Helper()
	resp, err := http.Post(baseURL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, payload
}

// TestPromptAsyncAnswers204ForALiveSession verifies the accepted-prompt
// contract: 204, no body, and the run actually happened after the response was
// written — the detached context, not the request's.
func TestPromptAsyncAnswers204ForALiveSession(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)

	status, body := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"parts":[{"type":"text","text":"reply with exactly: async-ok"}]}`)
	if status != http.StatusNoContent {
		t.Fatalf("prompt_async: expected 204, got %d", status)
	}
	if len(body) != 0 {
		t.Errorf("prompt_async: expected an empty body, got %q", body)
	}

	// The turn must still be running or finished after 204. An r.Context()
	// passed into the run would have killed it and left the transcript empty.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		msgs := readTranscript(t, baseURL, id)
		for _, m := range msgs {
			if m.Info.Role == "assistant" && len(m.Parts) > 0 && m.Parts[0].Text == "async-ok" {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the run did not reach the transcript after prompt_async answered 204")
}

// TestPromptAsyncAnswers404ForAnUnknownSession verifies the not-live case.
func TestPromptAsyncAnswers404ForAnUnknownSession(t *testing.T) {
	baseURL, _ := promptServer(t)
	status, _ := postJSON(t, baseURL, "/session/no-such-session/prompt_async",
		`{"parts":[{"type":"text","text":"hi"}]}`)
	if status != http.StatusNotFound {
		t.Fatalf("prompt_async unknown session: expected 404, got %d", status)
	}
}

// TestPromptAsyncAnswers400ForAForeignDirectory verifies directoryParam applies
// to the prompt endpoints.
func TestPromptAsyncAnswers400ForAForeignDirectory(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)
	status, _ := postJSON(t, baseURL, "/session/"+id+"/prompt_async?directory=/somewhere/else",
		`{"parts":[{"type":"text","text":"hi"}]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("prompt_async foreign directory: expected 400, got %d", status)
	}
}

// TestPromptAsyncAnswers400ForAMalformedBody verifies the malformed-body case,
// distinct from the oversize case.
func TestPromptAsyncAnswers400ForAMalformedBody(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)
	status, _ := postJSON(t, baseURL, "/session/"+id+"/prompt_async", `{"parts":`)
	if status != http.StatusBadRequest {
		t.Fatalf("prompt_async malformed body: expected 400, got %d", status)
	}
}

// TestPromptAsyncAnswers413ForABodyOverTheCap verifies the body cap on the
// endpoint that ingests base64 images from an unauthenticated LAN peer.
func TestPromptAsyncAnswers413ForABodyOverTheCap(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)
	huge := `{"parts":[{"type":"text","text":"` + strings.Repeat("x", maxBodyBytes+1024) + `"}]}`
	status, _ := postJSON(t, baseURL, "/session/"+id+"/prompt_async", huge)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("prompt_async oversize body: expected 413, got %d", status)
	}
}

// TestPromptAsyncAppliesTheClientModel covers the model a phone names per
// prompt. KKCode sends `model` on prompt_async, and the handler used to decode
// it and drop it, so every turn ran on whatever model the server started with
// while the client showed the one it picked.
//
// The queued half is the reason the swap runs inside the run slot rather than at
// accept time: B is accepted (204) while A is still running, and A's model must
// still be faux-1 at that moment. A swap at accept time would answer 204 having
// already changed the model of the turn in flight.
func TestPromptAsyncAppliesTheClientModel(t *testing.T) {
	env := promptEnvFor(t)
	baseURL := env.baseURL
	// A stored key is what puts test-faux in the models the server offers; with
	// none, AvailableModels is empty and every model reference is unoffered.
	if err := os.WriteFile(filepath.Join(env.agentDir, "auth.json"),
		[]byte(`{"test-faux":{"type":"api_key","key":"sk-test"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	id := createPromptSession(t, baseURL)

	// A model this server does not offer is refused, not accepted and ignored.
	unknown, unknownBody := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"parts":[{"type":"text","text":"hi"}],"model":{"providerID":"nope","modelID":"nope"}}`)
	if unknown != http.StatusBadRequest {
		t.Fatalf("prompt_async naming an unoffered model: got %d (%s), want 400", unknown, unknownBody)
	}
	if !strings.Contains(string(unknownBody), "model not found") {
		t.Errorf("a refused model must say so, got %q", unknownBody)
	}

	// A on the default model, long enough to still hold the run slot below.
	first, _ := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"parts":[{"type":"text","text":"TUI_LIVE_STREAM_PAUSE"}]}`)
	if first != http.StatusNoContent {
		t.Fatalf("first prompt_async: expected 204, got %d", first)
	}
	waitForBusy(t, baseURL, id)

	// B names faux-2 and is queued behind A.
	second, secondBody := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"parts":[{"type":"text","text":"reply with exactly: second"}],"model":{"providerID":"test-faux","modelID":"faux-2"}}`)
	if second != http.StatusNoContent {
		t.Fatalf("queued prompt_async naming faux-2: got %d (%s), want 204", second, secondBody)
	}
	if got := sessionModelOf(t, baseURL, id); got.ID != "faux-1" {
		t.Fatalf("a queued prompt changed the running turn's model to %q", got.ID)
	}

	// B's own turn runs on the model it asked for.
	waitForIdle(t, baseURL, id)
	if got := sessionModelOf(t, baseURL, id); got.ID != "faux-2" || got.ProviderID != "test-faux" {
		t.Fatalf("session model after the queued turn = %+v, want {faux-2 test-faux}", got)
	}
}

// sessionModelOf is the model GET /session/{id} reports for a session.
func sessionModelOf(t *testing.T, baseURL, id string) sessionModel {
	t.Helper()
	status, body := getRaw(t, baseURL, "/session/"+id)
	if status != http.StatusOK {
		t.Fatalf("GET /session/%s: got %d (%s)", id, status, body)
	}
	var dto sessionDTO
	if err := json.Unmarshal(body, &dto); err != nil {
		t.Fatalf("decode session %s: %v", id, err)
	}
	return dto.Model
}

// TestPromptAsyncQueuesWhileTheSessionIsBusy verifies the queue contract: a
// second prompt_async while a run is in flight is accepted (204) and runs as the
// next user turn, after the first settles, in FIFO order. The first prompt is
// TUI_LIVE_STREAM_PAUSE, which streams for roughly two seconds and settles
// naturally, so the order is observed without an abort.
func TestPromptAsyncQueuesWhileTheSessionIsBusy(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)

	first, _ := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"parts":[{"type":"text","text":"TUI_LIVE_STREAM_PAUSE"}]}`)
	if first != http.StatusNoContent {
		t.Fatalf("first prompt_async: expected 204, got %d", first)
	}
	// waitForBusy returns while A still holds the run slot, so B's reservation
	// lands on a taken slot rather than racing A's start.
	waitForBusy(t, baseURL, id)

	second, body := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"parts":[{"type":"text","text":"reply with exactly: second"}]}`)
	if second != http.StatusNoContent {
		t.Fatalf("second prompt_async while busy: expected 204, got %d (%s)", second, body)
	}

	// Backstop only: A settles on its own and B runs after it. If either hangs,
	// the abort keeps the test bounded.
	t.Cleanup(func() { _, _ = postJSON(t, baseURL, "/session/"+id+"/abort", `{}`) })

	roleText := func(m messageDTO) string {
		var b strings.Builder
		for _, p := range m.Parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}

	deadline := time.Now().Add(30 * time.Second)
	var msgs []messageDTO
	for time.Now().Before(deadline) {
		msgs = readTranscript(t, baseURL, id)
		found := false
		for _, m := range msgs {
			if m.Info.Role == "assistant" && strings.TrimSpace(roleText(m)) == "second" {
				found = true
				break
			}
		}
		if found {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	userA, asstA, userB, asstB := -1, -1, -1, -1
	for i, m := range msgs {
		text := roleText(m)
		switch {
		case userA < 0 && m.Info.Role == "user" && strings.Contains(text, "TUI_LIVE_STREAM_PAUSE"):
			userA = i
		case userA >= 0 && asstA < 0 && m.Info.Role == "assistant":
			asstA = i
		case asstA >= 0 && userB < 0 && m.Info.Role == "user" && strings.Contains(text, "reply with exactly: second"):
			userB = i
		case userB >= 0 && asstB < 0 && m.Info.Role == "assistant" && strings.TrimSpace(text) == "second":
			asstB = i
		}
	}
	if userA < 0 || asstA <= userA || userB <= asstA || asstB <= userB {
		roles := make([]string, 0, len(msgs))
		for _, m := range msgs {
			roles = append(roles, m.Info.Role+":"+strings.TrimSpace(roleText(m)))
		}
		t.Fatalf("queued prompt did not run after the active one in FIFO order: userA=%d asstA=%d userB=%d asstB=%d; transcript=%v",
			userA, asstA, userB, asstB, roles)
	}
}

// TestMessageAnswers200WithTheFinishedAssistantMessage verifies /message's
// contract: 200 with the nested {info, parts} shape and the assistant reply.
func TestMessageAnswers200WithTheFinishedAssistantMessage(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)

	status, payload := postJSON(t, baseURL, "/session/"+id+"/message",
		`{"parts":[{"type":"text","text":"reply with exactly: message-ok"}],"system":"ignored","format":{},"agent":"build"}`)
	if status != http.StatusOK {
		t.Fatalf("POST /message: expected 200, got %d", status)
	}
	var dto messageDTO
	if err := json.Unmarshal(payload, &dto); err != nil {
		t.Fatalf("decode %q: %v", payload, err)
	}
	if dto.Info.Role != "assistant" {
		t.Errorf("info.role: expected assistant, got %q", dto.Info.Role)
	}
	if dto.Info.SessionID != id {
		t.Errorf("info.sessionID: expected %q, got %q", id, dto.Info.SessionID)
	}
	if dto.Info.ID == "" {
		t.Error("info.id: expected a transcript entry id, got empty")
	}
	if len(dto.Parts) != 1 || dto.Parts[0].Text != "message-ok" {
		t.Fatalf("parts: expected one text part %q, got %+v", "message-ok", dto.Parts)
	}
	// The literal form, not partID's own output: asserting against the helper
	// would pass for any helper that is self-consistent.
	if want := dto.Info.ID + "-text-0"; dto.Parts[0].ID != want {
		t.Errorf("parts[0].id: expected %q, got %q", want, dto.Parts[0].ID)
	}

	// The /message response threads the assistant reply onto the initiating
	// user message, so POST, GET and the stream agree on the parentID.
	var userID string
	for _, m := range readTranscript(t, baseURL, id) {
		if m.Info.Role == "user" {
			userID = m.Info.ID
			break
		}
	}
	if userID == "" {
		t.Fatal("no user message in the transcript")
	}
	if dto.Info.ParentID != userID {
		t.Errorf("info.parentID: expected the initiating user message %q, got %q", userID, dto.Info.ParentID)
	}
}

// TestMessageAnswers404ForAnUnknownSession verifies the not-live case.
func TestMessageAnswers404ForAnUnknownSession(t *testing.T) {
	baseURL, _ := promptServer(t)
	status, _ := postJSON(t, baseURL, "/session/no-such-session/message",
		`{"parts":[{"type":"text","text":"hi"}]}`)
	if status != http.StatusNotFound {
		t.Fatalf("POST /message unknown session: expected 404, got %d", status)
	}
}

// TestMessageAnswers413ForABodyOverTheCap verifies the body cap on /message.
func TestMessageAnswers413ForABodyOverTheCap(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)
	huge := `{"parts":[{"type":"file","url":"data:image/png;base64,` + strings.Repeat("A", maxBodyBytes+1024) + `"}]}`
	status, _ := postJSON(t, baseURL, "/session/"+id+"/message", huge)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("POST /message oversize body: expected 413, got %d", status)
	}
}

// TestMessageAnswers400ForAnUnresolvableModel verifies the model picker cannot
// get a 200 that changed nothing.
func TestMessageAnswers400ForAnUnresolvableModel(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)
	status, _ := postJSON(t, baseURL, "/session/"+id+"/message",
		`{"parts":[{"type":"text","text":"hi"}],"model":{"providerID":"nope","modelID":"nope"}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("POST /message unknown model: expected 400, got %d", status)
	}
}

// TestMessageAnswers409WhileTheSessionIsBusy pins /message's half of the
// one-run-per-session rule. The reservation is taken inside a goroutine, so its
// ErrSessionBusy arrives with no transcript entry behind it: a handler that
// inspects the entry id first answers 500 "run produced no persisted assistant
// message" for a request that was never accepted, and the client retries a
// refusal forever.
//
// The second request carries a model this server does not offer, which pins the
// order as well as the status: the reservation is taken before the model is
// resolved, so a busy session is refused without its session-wide model being
// touched.
func TestMessageAnswers409WhileTheSessionIsBusy(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)

	first, _ := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"parts":[{"type":"text","text":"Run: sleep for a while"}]}`)
	if first != http.StatusNoContent {
		t.Fatalf("first prompt_async: expected 204, got %d", first)
	}
	waitForBusy(t, baseURL, id)

	contended, body := postJSON(t, baseURL, "/session/"+id+"/message",
		`{"parts":[{"type":"text","text":"reply with exactly: second"}]}`)
	if contended != http.StatusConflict {
		t.Fatalf("busy /message: expected 409, got %d with %s", contended, body)
	}
	if !strings.Contains(string(body), "session is busy") {
		t.Errorf("a refused /message must name the busy session, got %q", body)
	}

	// The same refusal with a model this server does not offer: the other order
	// answers 400 here, and on a request naming a model this server does offer it
	// has already swapped the session's model.
	withModel, modelBody := postJSON(t, baseURL, "/session/"+id+"/message",
		`{"parts":[{"type":"text","text":"reply with exactly: third"}],"model":{"providerID":"nope","modelID":"nope"}}`)
	if withModel != http.StatusConflict {
		t.Errorf("busy /message carrying a model: expected 409, got %d with %s", withModel, modelBody)
	}

	// Abort so the 45s tool does not outlive the test.
	abort, _ := postJSON(t, baseURL, "/session/"+id+"/abort", `{}`)
	if abort != http.StatusOK {
		t.Fatalf("abort: expected 200, got %d", abort)
	}
}

// TestAbortAnswers200ForAnIdleSession verifies "stop nothing" is success, with
// the JSON boolean opencode returns: the client decodes the body and rejects an
// empty, content-type-less reply as "is this an opencode server?".
func TestAbortAnswers200ForAnIdleSession(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)
	status, body := postJSON(t, baseURL, "/session/"+id+"/abort", `{}`)
	if status != http.StatusOK {
		t.Fatalf("abort on idle session: expected 200, got %d", status)
	}
	if got := strings.TrimSpace(string(body)); got != "true" {
		t.Fatalf("abort body = %q, want true", got)
	}
}

// TestAbortAnswers404ForAnUnknownSession verifies the not-live case.
func TestAbortAnswers404ForAnUnknownSession(t *testing.T) {
	baseURL, _ := promptServer(t)
	status, _ := postJSON(t, baseURL, "/session/no-such-session/abort", `{}`)
	if status != http.StatusNotFound {
		t.Fatalf("abort unknown session: expected 404, got %d", status)
	}
}

// TestAbortStopsARunningPrompt verifies abort reaches a run whose reservation
// is outstanding, which coding's own cancel cannot: beginAgentRun has not run
// yet at the reservation.
func TestAbortStopsARunningPrompt(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)

	started, _ := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"parts":[{"type":"text","text":"Run: sleep for a while"}]}`)
	if started != http.StatusNoContent {
		t.Fatalf("prompt_async: expected 204, got %d", started)
	}

	waitForBusy(t, baseURL, id)

	status, _ := postJSON(t, baseURL, "/session/"+id+"/abort", `{}`)
	if status != http.StatusOK {
		t.Fatalf("abort: expected 200, got %d", status)
	}
	waitForIdle(t, baseURL, id)
}

// TestSessionStatusCarriesSessionID verifies the pull-path status can be
// attributed to a session.
func TestSessionStatusCarriesSessionID(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)
	resp, err := http.Get(baseURL + "/session/status")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var dtos []sessionStatusDTO
	if err := json.NewDecoder(resp.Body).Decode(&dtos); err != nil {
		t.Fatal(err)
	}
	if len(dtos) != 1 {
		t.Fatalf("expected 1 status, got %d", len(dtos))
	}
	if dtos[0].SessionID != id {
		t.Errorf("sessionID: expected %q, got %q", id, dtos[0].SessionID)
	}
	if dtos[0].Type != "idle" {
		t.Errorf("type: expected idle, got %q", dtos[0].Type)
	}
}

// TestPartIDsAreUniqueWithinAMessage kills the serve-3 defect: the transcript
// loop enumerated content with no index, so two text blocks in one assistant
// message produced two parts with the same id. The fixture is written to the
// session file directly because test-faux has no two-same-kind-block script.
//
// The literal -text-1 and -text-2 also pin the index-before-filtering rule:
// ServerToolContent yields no part but still consumes index 0.
func TestPartIDsAreUniqueWithinAMessage(t *testing.T) {
	sessionDir := t.TempDir()
	id := "duplicate-parts"
	entryID := "0123456789abcdef"
	writeTestSession(t, sessionDir, id, []string{
		`{"type":"message","id":"` + entryID + `","parentId":null,"timestamp":"2024-01-01T00:00:01.000000000Z","message":{"role":"assistant","content":[` +
			`{"type":"serverTool","raw":{"type":"web_search_tool_result","content":[]}},` +
			`{"type":"text","text":"first"},` +
			`{"type":"text","text":"second"}` +
			`],"api":"faux","provider":"test-faux","model":"faux-1","usage":{"input":1,"output":1,"cache":{"read":0,"write":0}},"stopReason":"stop","timestamp":1704067201000}}`,
	})

	messages := readTranscriptFrom(t, sessionDir, id)
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	got := make([]string, 0, len(messages[0].Parts))
	for _, p := range messages[0].Parts {
		got = append(got, p.ID)
	}
	want := []string{entryID + "-text-1", entryID + "-text-2"}
	if len(got) != len(want) {
		t.Fatalf("part ids: expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parts[%d].id: expected %q, got %q", i, want[i], got[i])
		}
	}
}

// TestPartIDsAgreeBetweenStreamAndTranscript pins that the SSE mapper and the
// transcript build the same id for the same (entryID, kind, index). It asserts
// the literal form the helper produces, not set equality: a duplicate collapses
// in a set on both sides, which is how the serve-3 defect passed green.
func TestPartIDsAgreeBetweenStreamAndTranscript(t *testing.T) {
	if partID("m1", "text", 1, "") != "m1-text-1" {
		t.Errorf("text form: expected m1-text-1, got %q", partID("m1", "text", 1, ""))
	}
	if partID("m1", "reasoning", 2, "") != "m1-reasoning-2" {
		t.Errorf("reasoning form: expected m1-reasoning-2, got %q", partID("m1", "reasoning", 2, ""))
	}
	if partID("m1", "image", 0, "") != "m1-image-0" {
		t.Errorf("image form: expected m1-image-0, got %q", partID("m1", "image", 0, ""))
	}
	if partID("m1", "tool", 3, "call-9") != "m1-tool-call-9" {
		t.Errorf("tool form: expected m1-tool-call-9, got %q", partID("m1", "tool", 3, "call-9"))
	}
	// The index is over the full content slice, so a leading block that yields
	// no part must still consume its index.
	sessionDir := t.TempDir()
	id := "index-alignment"
	entryID := "fedcba9876543210"
	writeTestSession(t, sessionDir, id, []string{
		`{"type":"message","id":"` + entryID + `","parentId":null,"timestamp":"2024-01-01T00:00:01.000000000Z","message":{"role":"assistant","content":[` +
			`{"type":"serverTool","raw":{"type":"web_search_tool_result","content":[]}},` +
			`{"type":"text","text":"after the unmapped block"}` +
			`],"api":"faux","provider":"test-faux","model":"faux-1","usage":{"input":1,"output":1,"cache":{"read":0,"write":0}},"stopReason":"stop","timestamp":1704067201000}}`,
	})
	messages := readTranscriptFrom(t, sessionDir, id)
	if len(messages) != 1 || len(messages[0].Parts) != 1 {
		t.Fatalf("expected 1 message with 1 part, got %d messages", len(messages))
	}
	if got, want := messages[0].Parts[0].ID, entryID+"-text-1"; got != want {
		t.Errorf("part id: expected %q (index over the full content slice), got %q", want, got)
	}
}

// readTranscript reads GET /session/{id}/message.
func readTranscript(t *testing.T, baseURL, id string) []messageDTO {
	t.Helper()
	resp, err := http.Get(baseURL + "/session/" + id + "/message")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var dtos []messageDTO
	if err := json.NewDecoder(resp.Body).Decode(&dtos); err != nil {
		t.Fatal(err)
	}
	return dtos
}

// readTranscriptFrom runs entriesToMessages over a written session file, which
// is the same projection GET /session/{id}/message serves. The written header's
// cwd and root are "/test"; only the two model-fallback arguments are "".
func readTranscriptFrom(t *testing.T, sessionDir, id string) []messageDTO {
	t.Helper()
	path := filepath.Join(sessionDir, time.Now().UTC().Format("2006-01-02T15-04-05")+"_"+id+".jsonl")
	entries, err := tailEntries(path, maxLimit)
	if err != nil {
		t.Fatal(err)
	}
	return entriesToMessages(entries, id, "/test", "/test", "", "")
}

func waitForBusy(t *testing.T, baseURL, id string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if !sessionIsIdle(t, baseURL, id) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the session never became busy")
}

func waitForIdle(t *testing.T, baseURL, id string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if sessionIsIdle(t, baseURL, id) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the session never returned to idle")
}

func sessionIsIdle(t *testing.T, baseURL, id string) bool {
	t.Helper()
	resp, err := http.Get(baseURL + "/session/status")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var dtos []sessionStatusDTO
	if err := json.NewDecoder(resp.Body).Decode(&dtos); err != nil {
		t.Fatal(err)
	}
	for _, dto := range dtos {
		if dto.SessionID == id {
			return dto.Type == "idle"
		}
	}
	t.Fatalf("session %q is absent from /session/status", id)
	return false
}

// TestListSessionsReturnsAllSessions verifies that GET /session returns all
// sessions on disk with the correct IDs. Runs with -count=20 to catch races.
func TestListSessionsReturnsAllSessions(t *testing.T) {
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
	host := codinghost.New(services, nil)

	sessionDir := filepath.Join(agentDir, "sessions", "--test--")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create 5 session files on disk with different IDs.
	ids := make([]string, 5)
	for i := range 5 {
		id := fmt.Sprintf("session-%02d", i)
		ids[i] = id
		path := filepath.Join(sessionDir, time.Now().UTC().Format("2006-01-02T15-04-05")+"_"+id+".jsonl")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		header := `{"type":"session","version":3,"id":"` + id + `","timestamp":"2024-01-01T00:00:00.000000000Z","cwd":"/test"}`
		_, _ = f.WriteString(header + "\n")
		entry := `{"type":"message","id":"e1","parentId":null,"timestamp":"2024-01-01T00:00:01.000000000Z","message":{"role":"user","content":[{"type":"text","text":"hi"}],"timestamp":1704067201000}}`
		_, _ = f.WriteString(entry + "\n")
		_ = f.Close()
	}

	srv, ln, err := New("127.0.0.1:0", host, sessionDir, "test-faux/faux-1")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	baseURL := "http://" + ln.Addr().String()

	resp, err := http.Get(baseURL + "/session")
	if err != nil {
		t.Fatal(err)
	}
	respBody := make([]byte, 4096)
	n, _ := resp.Body.Read(respBody)
	_ = resp.Body.Close()

	var sessions []map[string]any
	if err := json.Unmarshal(respBody[:n], &sessions); err != nil {
		t.Fatal(err)
	}

	if len(sessions) != 5 {
		t.Fatalf("expected 5 sessions, got %d", len(sessions))
	}

	found := make(map[string]bool)
	for _, s := range sessions {
		id := s["id"].(string)
		found[id] = true
	}
	for _, id := range ids {
		if !found[id] {
			t.Errorf("expected session %q not found", id)
		}
	}
}

// ─── /global/event ─────────────────────────────────────────────────────────

// sseFrame is one parsed frame off /global/event.
type sseFrame struct {
	Name string
	Data string
}

// TestSSEStreamDropsAStalledConnectionAtTheByteBound reaches end 2 of the
// stream over a real listener: a client that connects and then never reads, a
// producer that overruns the connection's byte budget, and a terminal frame the
// client can act on instead of a stream that just stops mid-message.
//
// A raw socket, not http.Client: the transport reads into its own buffer as
// fast as the server writes, so a net/http client never stalls the server and
// the queue would never fill.
func TestSSEStreamDropsAStalledConnectionAtTheByteBound(t *testing.T) {
	baseURL, host := promptServer(t)
	createPromptSession(t, baseURL)

	addr := strings.TrimPrefix(baseURL, "http://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := fmt.Fprintf(conn, "GET /global/event HTTP/1.1\r\nHost: %s\r\nAccept: text/event-stream\r\nCache-Control: no-cache\r\n\r\n", addr); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(60 * time.Second)); err != nil {
		t.Fatal(err)
	}

	// The response is chunked, so the body has to be de-chunked rather than
	// read as raw bytes: one Read can return the head and several frames
	// together, and reading by frame boundary would then skip frames.
	reader := bufio.NewReader(conn)
	head := readHead(t, reader)
	for _, want := range []string{
		"HTTP/1.1 200 OK",
		"Content-Type: text/event-stream",
		"Cache-Control: no-cache",
		"Connection: keep-alive",
		"X-Accel-Buffering: no",
	} {
		if !strings.Contains(head, want) {
			t.Errorf("response head: expected %q, got:\n%s", want, head)
		}
	}

	// Nothing reads the body until the overrun is published, but the first frame
	// is read now so the assertions about it are made before the stall.
	body := httputil.NewChunkedReader(reader)
	frames := readSSE(t, body, func(frames []sseFrame) bool { return len(frames) > 0 })
	if len(frames) == 0 || frames[0].Name != "server.connected" {
		t.Fatalf("expected server.connected as the first frame, got %s", summarize(frames))
	}
	if !strings.Contains(frames[0].Data, `"version":`) {
		t.Errorf("server.connected must carry the server version, got %q", frames[0].Data)
	}

	blob := strings.Repeat("x", 1<<20)
	for range sseQueueBytes/(1<<20) + 8 {
		err := host.Publish(codinghost.Event{
			Type:    eventMessagePartDelta,
			Session: "",
			Data:    ssePartDelta{MessageID: "msg", PartID: "msg-text-0", Field: "text", Delta: blob},
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	frames = append(frames, readSSE(t, body, func(frames []sseFrame) bool {
		for _, f := range frames {
			if f.Data == "[DONE]" {
				return true
			}
		}
		return false
	})...)
	if len(frames) == 0 || frames[len(frames)-1].Data != "[DONE]" {
		t.Fatalf("the overrun ended the stream with no [DONE] sentinel; %d frames arrived: %s", len(frames), summarize(frames))
	}
	dropAt := -1
	for i, f := range frames {
		if f.Name == "session.error" {
			dropAt = i
			break
		}
	}
	if dropAt < 0 {
		t.Fatalf("the overrun ended the stream with no session.error; frames: %s", summarize(frames))
	}
	if !strings.Contains(frames[dropAt].Data, `"name":"SSEDropped"`) {
		t.Errorf("the terminal frame must name the byte bound, got %q", frames[dropAt].Data)
	}
	var terminal sseSessionError
	if err := json.Unmarshal([]byte(frames[dropAt].Data), &terminal); err != nil {
		t.Fatalf("decode the terminal frame %q: %v", frames[dropAt].Data, err)
	}
	if terminal.SessionID != "" {
		t.Errorf("the terminal frame is server-wide, got sessionID %q", terminal.SessionID)
	}

	// The prefix-before-terminal ordering is pinned at the queue, not here:
	// TestServeFanoutDropsStalledSubscribers reads the subscription directly, so
	// it can see whether frames were queued. Over a socket the kernel buffer
	// absorbs several MiB before the handler blocks, which is enough that this
	// test passes even if the queue were discarded on drop.
}

// TestStreamPartIDsMatchTranscriptPartIDs is the topology gate: the parts the
// stream published and the persisted transcript must report the same literal
// part ids for the same message. Deltas stream under the assistant entry id the
// Session mints at message start, so both sides build the ids from that one id.
// First-appearance order, de-duplicated — a repeated part id collapses in a set
// on both sides, which is how serve-3's duplicate-part-id defect passed green.
func TestStreamPartIDsMatchTranscriptPartIDs(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := openSSE(t, baseURL, ctx)
	defer func() { _ = resp.Body.Close() }()

	status, _ := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"parts":[{"type":"text","text":"reply with exactly: topo-ok"}]}`)
	if status != http.StatusNoContent {
		t.Fatalf("prompt_async: expected 204, got %d", status)
	}

	// Stop on session.idle, which is emitted only after the run and its
	// message_end persist have settled. Stopping at the first part.updated would
	// fire on the TextEnd frame, before MessageEnd persists the entry, and the
	// transcript read below would race an assistant message that is not there yet.
	frames := readSSEUntil(t, resp, func(frames []sseFrame) bool {
		return slices.ContainsFunc(frames, func(f sseFrame) bool { return f.Name == eventSessionIdle })
	})
	cancel()

	entryID := assistantMessageUpdatedID(frames)
	if entryID == "" {
		t.Fatalf("no message.updated for the assistant message; frames: %s", summarize(frames))
	}
	var streamed []string
	for _, f := range frames {
		if f.Name != eventMessagePartUpdated {
			continue
		}
		var payload ssePartUpdated
		if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
			continue
		}
		if payload.Part.MessageID != entryID {
			continue
		}
		// First appearance only: the streamed create and its final frame carry
		// the one part id, so what is compared is the id topology, not frame count.
		if !slices.Contains(streamed, payload.Part.ID) {
			streamed = append(streamed, payload.Part.ID)
		}
	}
	// The literal form, not partID's own output: asserting against the helper
	// would pass for any helper that is self-consistent.
	want := []string{entryID + "-text-0"}
	if !slices.Equal(streamed, want) {
		t.Errorf("streamed part ids: expected %v, got %v", want, streamed)
	}

	var persisted []string
	for _, m := range readTranscript(t, baseURL, id) {
		if m.Info.ID != entryID || m.Info.Role != "assistant" {
			continue
		}
		for _, p := range m.Parts {
			persisted = append(persisted, p.ID)
		}
	}
	if !slices.Equal(persisted, want) {
		t.Errorf("transcript part ids: expected %v, got %v", want, persisted)
	}
}

// TestSSETurnLifecycleParity is the one-turn contract: a user turn announces
// the user message under its transcript id, then streams one assistant message
// under a single stable id for the whole run. That id is the one the transcript
// reports, so the client renders the reply once instead of once per source id.
func TestSSETurnLifecycleParity(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := openSSE(t, baseURL, ctx)
	defer func() { _ = resp.Body.Close() }()

	const promptText = "reply with exactly: parity-ok"
	status, _ := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"parts":[{"type":"text","text":"`+promptText+`"}]}`)
	if status != http.StatusNoContent {
		t.Fatalf("prompt_async: expected 204, got %d", status)
	}

	// Stop on session.idle: it is emitted only after the run and its message_end
	// persist have settled, so the transcript read below cannot race persistence.
	frames := readSSEUntil(t, resp, func(frames []sseFrame) bool {
		for _, f := range frames {
			if f.Name == eventSessionIdle {
				return true
			}
		}
		return false
	})
	cancel()

	// 1. Exactly two user message.updated frames (opencode's two-phase pair):
	// same id, the start frame without a summary key, the settle frame with
	// summary.diffs == [].
	userInfos := userMessageUpdatedInfos(t, frames)
	if len(userInfos) != 2 {
		t.Fatalf("expected exactly two user message.updated, got %d; frames: %s", len(userInfos), summarize(frames))
	}
	var userID string
	_ = json.Unmarshal(userInfos[0]["id"], &userID)
	if userID == "" {
		t.Fatalf("the user message.updated carries no id; frames: %s", summarize(frames))
	}
	var settleUserID string
	_ = json.Unmarshal(userInfos[1]["id"], &settleUserID)
	if settleUserID != userID {
		t.Errorf("user message.updated ids disagree: %q against %q", userID, settleUserID)
	}
	assertUserSummaryOmitted(t, userInfos[0])
	assertUserSummaryEmpty(t, userInfos[1])

	// 2. The user's text part, under the same id, carries the prompt verbatim.
	wantUserPart := userID + "-text-0"
	userPartText := ""
	userPartSeen := false
	for _, f := range frames {
		if f.Name != eventMessagePartUpdated {
			continue
		}
		var payload ssePartUpdated
		if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
			continue
		}
		if payload.Part.ID != wantUserPart {
			continue
		}
		userPartSeen = true
		if payload.Part.MessageID != userID {
			t.Errorf("user part messageID: expected %q, got %q", userID, payload.Part.MessageID)
		}
		userPartText = payload.Part.Text
	}
	if !userPartSeen {
		t.Fatalf("no user message.part.updated %q; frames: %s", wantUserPart, summarize(frames))
	}
	if userPartText != promptText {
		t.Errorf("user part text: expected %q, got %q", promptText, userPartText)
	}

	// 3. Every assistant message.updated names one id.
	assistantID := assistantMessageUpdatedID(frames)
	if assistantID == "" {
		t.Fatalf("no assistant message.updated; frames: %s", summarize(frames))
	}
	for _, f := range frames {
		if f.Name != eventMessageUpdated {
			continue
		}
		var payload sseMessageUpdated
		if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
			continue
		}
		if payload.Info.Role == "assistant" && payload.Info.ID != assistantID {
			t.Errorf("assistant message.updated ids disagree: %q against %q", payload.Info.ID, assistantID)
		}
	}

	// 3b. Every assistant message.updated names the user message as its parent,
	// so a client can thread the reply onto the prompt it answered.
	for _, f := range frames {
		if f.Name != eventMessageUpdated {
			continue
		}
		var payload sseMessageUpdated
		if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
			continue
		}
		if payload.Info.Role == "assistant" && payload.Info.ParentID != userID {
			t.Errorf("assistant message.updated parentID: expected %q, got %q",
				userID, payload.Info.ParentID)
		}
	}

	// 4. Every assistant delta names the same part.
	deltaPartID := ""
	deltaCount := 0
	for _, f := range frames {
		if f.Name != eventMessagePartDelta {
			continue
		}
		var payload ssePartDelta
		if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
			continue
		}
		if payload.MessageID != assistantID {
			continue
		}
		deltaCount++
		if deltaPartID == "" {
			deltaPartID = payload.PartID
			continue
		}
		if payload.PartID != deltaPartID {
			t.Errorf("assistant deltas disagree on partID: %q against %q", payload.PartID, deltaPartID)
		}
	}
	if deltaCount == 0 {
		t.Fatalf("no assistant message.part.delta; frames: %s", summarize(frames))
	}

	// 5. A message.part.updated publishes the delta's part under the same id.
	// The first such frame for the id is now the empty create (Text == ""), so
	// the final content is asserted once the frame carrying it arrives rather
	// than on every frame for the id.
	partSeen := false
	sawFinal := false
	for _, f := range frames {
		if f.Name != eventMessagePartUpdated {
			continue
		}
		var payload ssePartUpdated
		if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
			continue
		}
		if payload.Part.ID != deltaPartID {
			continue
		}
		partSeen = true
		if payload.Part.Type != "text" {
			t.Errorf("part type: expected text, got %q", payload.Part.Type)
		}
		if payload.Part.Text == "parity-ok" {
			sawFinal = true
		}
	}
	if !partSeen {
		t.Fatalf("no message.part.updated for the delta part %q; frames: %s", deltaPartID, summarize(frames))
	}
	if !sawFinal {
		t.Errorf("no message.part.updated carried the final text %q for part %q; frames: %s",
			"parity-ok", deltaPartID, summarize(frames))
	}

	// 6. The turn settled.
	if !slices.ContainsFunc(frames, func(f sseFrame) bool { return f.Name == eventSessionIdle }) {
		t.Fatalf("no session.idle frame; frames: %s", summarize(frames))
	}

	// 7. The transcript reports the same assistant id and part id.
	transcriptPartID := ""
	for _, m := range readTranscript(t, baseURL, id) {
		if m.Info.ID != assistantID || m.Info.Role != "assistant" {
			continue
		}
		for _, p := range m.Parts {
			transcriptPartID = p.ID
		}
	}
	if transcriptPartID != deltaPartID {
		t.Errorf("transcript part id: expected %q, got %q", deltaPartID, transcriptPartID)
	}
}

// TestSSEUserMessageTwoPhase pins opencode's two-phase user message.updated:
// one prompted turn emits the user frame once at turn start with no summary
// key, and once in the settle burst with summary:{"diffs":[]}, under one id and
// clock. Frame parity only; the read path is asserted elsewhere and unchanged.
func TestSSEUserMessageTwoPhase(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := openSSE(t, baseURL, ctx)
	defer func() { _ = resp.Body.Close() }()

	status, _ := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"parts":[{"type":"text","text":"reply with exactly: two-phase-ok"}]}`)
	if status != http.StatusNoContent {
		t.Fatalf("prompt_async: expected 204, got %d", status)
	}
	frames := readSSEUntil(t, resp, stopOnSessionIdle)
	cancel()

	infos := userMessageUpdatedInfos(t, frames)
	if len(infos) != 2 {
		t.Fatalf("expected exactly two user message.updated, got %d; frames: %s", len(infos), summarize(frames))
	}
	var firstID, secondID string
	_ = json.Unmarshal(infos[0]["id"], &firstID)
	_ = json.Unmarshal(infos[1]["id"], &secondID)
	if firstID == "" {
		t.Fatalf("first user message.updated carries no id; frames: %s", summarize(frames))
	}
	if firstID != secondID {
		t.Errorf("user message.updated ids disagree: %q against %q", firstID, secondID)
	}
	firstCreated := userMessageCreated(t, infos[0])
	secondCreated := userMessageCreated(t, infos[1])
	if firstCreated == 0 {
		t.Errorf("first user message.updated time.created is zero, want a stamp")
	}
	if firstCreated != secondCreated {
		t.Errorf("user message.updated time.created disagree: %d against %d", firstCreated, secondCreated)
	}
	assertUserSummaryOmitted(t, infos[0])
	assertUserSummaryEmpty(t, infos[1])
}

// TestSSETurnSessionFramesMatchOpencode pins the turn-level session frames the
// mapper adds on top of the prior-wave message and part frames: session.updated
// before session.status busy at turn start, and session.updated + session.diff
// {diff:[]} at settle, before session.status idle and session.idle. The expected
// sequence is an in-test fixture transcribed from opencode's turn census; this
// test never reads a developer capture.
//
// Non-vacuity: dropping the session.updated emission fails (1) and (2); emitting
// diff:null fails (3); emitting session.diff after idle fails (3).
func TestSSETurnSessionFramesMatchOpencode(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := openSSE(t, baseURL, ctx)
	defer func() { _ = resp.Body.Close() }()

	status, _ := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"parts":[{"type":"text","text":"reply with exactly: parity-ok"}]}`)
	if status != http.StatusNoContent {
		t.Fatalf("prompt_async: expected 204, got %d", status)
	}

	// One read only: a second readSSEUntil would build a second bufio.Reader
	// over the same body and lose the frames the first already buffered. Stop at
	// session.idle, which follows every session frame of the turn.
	frames := readSSEUntil(t, resp, func(frames []sseFrame) bool {
		for _, f := range frames {
			if f.Name == eventSessionIdle {
				return true
			}
		}
		return false
	})
	cancel()

	// Normalized session contract: server.* and message.* frames are dropped
	// (message.* is the prior waves' surface), and a session.status contributes
	// its name without its busy/idle payload.
	var (
		normalized     []string
		updates        []sessionDTO
		firstStatus    string
		lastStatus     string
		diffCount      int
		diffProperties map[string]json.RawMessage
	)
	for _, f := range frames {
		switch f.Name {
		case eventSessionUpdated:
			normalized = append(normalized, f.Name)
			var payload sseSessionUpdated
			if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
				t.Fatalf("decode session.updated: %v (data %s)", err, f.Data)
			}
			updates = append(updates, payload.Info)
		case "session.diff":
			normalized = append(normalized, f.Name)
			diffCount++
			if err := json.Unmarshal([]byte(f.Data), &diffProperties); err != nil {
				t.Fatalf("decode session.diff: %v (data %s)", err, f.Data)
			}
			var payload struct {
				SessionID string            `json:"sessionID"`
				Diff      []json.RawMessage `json:"diff"`
			}
			if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
				t.Fatalf("decode session.diff: %v (data %s)", err, f.Data)
			}
			if payload.SessionID != id {
				t.Errorf("session.diff sessionID: expected %q, got %q", id, payload.SessionID)
			}
			if payload.Diff == nil || len(payload.Diff) != 0 {
				t.Errorf("session.diff diff: expected a non-null empty array, got %s", f.Data)
			}
		case eventSessionStatus:
			normalized = append(normalized, f.Name)
			var payload sseSessionStatus
			if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
				t.Fatalf("decode session.status: %v (data %s)", err, f.Data)
			}
			if firstStatus == "" {
				firstStatus = payload.Status.Type
			}
			lastStatus = payload.Status.Type
		case eventSessionIdle:
			normalized = append(normalized, f.Name)
		}
	}

	// 1. The first session contract frame is session.updated, and the first
	// status after it is busy.
	if len(normalized) == 0 || normalized[0] != eventSessionUpdated {
		t.Fatalf("first session contract frame: expected %q, got %v; frames: %s",
			eventSessionUpdated, normalized, summarize(frames))
	}
	if firstStatus != "busy" {
		t.Errorf("first session.status: expected busy, got %q; frames: %s", firstStatus, summarize(frames))
	}

	// 2. Exactly two session.updated with the session's id and a moving
	// time.updated.
	if len(updates) != 2 {
		t.Fatalf("session.updated count: expected 2, got %d; frames: %s", len(updates), summarize(frames))
	}
	for i, info := range updates {
		if info.ID != id {
			t.Errorf("session.updated[%d] info.id: expected %q, got %q", i, id, info.ID)
		}
		if info.Time.Updated <= 0 {
			t.Errorf("session.updated[%d] info.time.updated: expected > 0, got %d", i, info.Time.Updated)
		}
	}
	if updates[1].Time.Updated <= updates[0].Time.Updated {
		t.Errorf("session.updated info.time.updated did not move forward: %d then %d",
			updates[0].Time.Updated, updates[1].Time.Updated)
	}

	// 3. Exactly one session.diff, properties {sessionID, diff} with diff an
	// empty array (not null), before the idle status.
	if diffCount != 1 {
		t.Fatalf("session.diff count: expected 1, got %d; frames: %s", diffCount, summarize(frames))
	}
	if len(diffProperties) != 2 || diffProperties["sessionID"] == nil || diffProperties["diff"] == nil {
		t.Errorf("session.diff properties: expected exactly {sessionID, diff}, got %v", diffProperties)
	}

	// 4. session.status idle precedes session.idle, and it is the last status.
	if len(normalized) < 2 || normalized[len(normalized)-2] != eventSessionStatus || normalized[len(normalized)-1] != eventSessionIdle {
		t.Fatalf("idle tail: expected [%s %s], got %v; frames: %s",
			eventSessionStatus, eventSessionIdle, normalized, summarize(frames))
	}
	if lastStatus != "idle" {
		t.Errorf("last session.status: expected idle, got %q", lastStatus)
	}

	// 5. The normalized session contract subsequence, the transcribed fixture.
	want := []string{
		eventSessionUpdated, eventSessionStatus,
		eventSessionUpdated, "session.diff", eventSessionStatus, eventSessionIdle,
	}
	if !slices.Equal(normalized, want) {
		t.Fatalf("normalized session contract sequence:\n got %v\nwant %v\nframes: %s",
			normalized, want, summarize(frames))
	}
}

// TestSSETurnFrameParity pins the per-part frame order a client needs to
// materialise exactly one part per streamed block: an empty create
// message.part.updated carrying time:{start}, then the text delta, then one
// timed message.part.updated carrying time:{start,end} when the block ends. It
// also pins the assistant message's completion clock. Without the create-first
// order and the timestamps a client (KKCode) shows the reply twice and never
// returns to idle.
//
// Non-vacuity: dropping the delta's createPart leaves the TextEndEvent's create
// present but after the delta, failing the order check
// (createIdx < deltaIdx < finalIdx); dropping the part Time stamp leaves no
// finalIdx for step 2; dropping info.Time.Completed fails step 5; minting a
// fresh part id per frame fails step 1.
func TestSSETurnFrameParity(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := openSSE(t, baseURL, ctx)
	defer func() { _ = resp.Body.Close() }()

	status, _ := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"parts":[{"type":"text","text":"reply with exactly: parity-ok"}]}`)
	if status != http.StatusNoContent {
		t.Fatalf("prompt_async: expected 204, got %d", status)
	}

	frames := readSSEUntil(t, resp, func(frames []sseFrame) bool {
		for _, f := range frames {
			if f.Name == eventSessionIdle {
				return true
			}
		}
		return false
	})
	cancel()

	assistantID := assistantMessageUpdatedID(frames)
	if assistantID == "" {
		t.Fatalf("no assistant message.updated; frames: %s", summarize(frames))
	}
	// The literal id, not partID's own output: asserting against the helper
	// would pass for any helper that is self-consistent.
	partID := assistantID + "-text-0"

	// 1. Stable id and 2. order. The create, the first delta and the final
	// timed frame must all name the one literal part id, in that order.
	createIdx, deltaIdx, finalIdx := -1, -1, -1
	var createPart, finalPart *partDTO
	for i, f := range frames {
		switch f.Name {
		case eventMessagePartUpdated:
			var payload ssePartUpdated
			if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
				continue
			}
			if payload.Part.ID != partID {
				continue
			}
			switch {
			case payload.Part.Time != nil && payload.Part.Time.End == 0:
				if createIdx == -1 {
					createIdx = i
					p := payload.Part
					createPart = &p
				}
			case payload.Part.Time != nil && payload.Part.Time.End > 0 && payload.Part.Text == "parity-ok":
				if finalIdx == -1 {
					finalIdx = i
					p := payload.Part
					finalPart = &p
				}
			}
		case eventMessagePartDelta:
			var payload ssePartDelta
			if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
				continue
			}
			if payload.PartID == partID && deltaIdx == -1 {
				deltaIdx = i
			}
		}
	}
	if createIdx == -1 || deltaIdx == -1 || finalIdx == -1 {
		t.Fatalf("part %q: expected create, delta and final frames, got create=%d delta=%d final=%d; frames: %s",
			partID, createIdx, deltaIdx, finalIdx, summarize(frames))
	}
	if createIdx >= deltaIdx || deltaIdx >= finalIdx {
		t.Errorf("part %q frame order: create=%d delta=%d final=%d, want create < delta < final",
			partID, createIdx, deltaIdx, finalIdx)
	}

	// 3. Create time: opencode's empty part with time:{start}.
	if createPart.Text != "" {
		t.Errorf("create frame text: expected empty, got %q", createPart.Text)
	}
	if createPart.Time.Start <= 0 {
		t.Errorf("create frame time.start: expected > 0, got %d", createPart.Time.Start)
	}
	if createPart.Time.End != 0 {
		t.Errorf("create frame time.end: expected 0 (omitted), got %d", createPart.Time.End)
	}

	// 4. Final time: same start, end at or after it.
	if finalPart.Time.Start != createPart.Time.Start {
		t.Errorf("final frame time.start: expected %d, got %d", createPart.Time.Start, finalPart.Time.Start)
	}
	if finalPart.Time.End < finalPart.Time.Start {
		t.Errorf("final frame time.end %d precedes time.start %d", finalPart.Time.End, finalPart.Time.Start)
	}

	// 5. Message completion: the placeholder carries no completed time, the
	// last assistant message.updated does.
	var assistantUpdates []sseMessageUpdated
	for _, f := range frames {
		if f.Name != eventMessageUpdated {
			continue
		}
		var payload sseMessageUpdated
		if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
			continue
		}
		if payload.Info.Role == "assistant" {
			assistantUpdates = append(assistantUpdates, payload)
		}
	}
	if len(assistantUpdates) < 2 {
		t.Fatalf("expected a placeholder and a completion assistant message.updated, got %d; frames: %s",
			len(assistantUpdates), summarize(frames))
	}
	first := assistantUpdates[0]
	last := assistantUpdates[len(assistantUpdates)-1]
	if first.Info.Time.Completed != nil {
		t.Errorf("placeholder assistant message.updated carries completed=%d, want nil", *first.Info.Time.Completed)
	}
	if last.Info.Time.Completed == nil || *last.Info.Time.Completed <= 0 {
		t.Errorf("final assistant message.updated completed = %v, want > 0; frames: %s",
			last.Info.Time.Completed, summarize(frames))
	}
	// The created clock is the message's own, stamped at start and carried
	// unchanged onto the completion frame, so the two assistant frames agree.
	if last.Info.Time.Created != first.Info.Time.Created {
		t.Errorf("final assistant message.updated created = %d, want the placeholder's %d; frames: %s",
			last.Info.Time.Created, first.Info.Time.Created, summarize(frames))
	}
	if last.Info.Time.Completed != nil && *last.Info.Time.Completed < last.Info.Time.Created {
		t.Errorf("final assistant message.updated completed %d precedes created %d; frames: %s",
			*last.Info.Time.Completed, last.Info.Time.Created, summarize(frames))
	}
}

// TestMessageEndDoesNotRepublishStreamedPart pins the end-of-turn contract: the
// messageEnd burst must not re-send a part the stream already published. A
// republished text or reasoning part is rebuilt with no Time, so a frame after
// the final message.updated overwrites the streamed part's {start,end} with a
// timeless one and gives the client a second part update opencode never emits.
func TestMessageEndDoesNotRepublishStreamedPart(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := openSSE(t, baseURL, ctx)
	defer func() { _ = resp.Body.Close() }()

	status, _ := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"parts":[{"type":"text","text":"reply with exactly: parity-ok"}]}`)
	if status != http.StatusNoContent {
		t.Fatalf("prompt_async: expected 204, got %d", status)
	}

	// Stop on session.idle: messageEnd emits its burst before it, so the frames
	// include the whole turn up to and including the republish window.
	frames := readSSEUntil(t, resp, func(frames []sseFrame) bool {
		return slices.ContainsFunc(frames, func(f sseFrame) bool { return f.Name == eventSessionIdle })
	})
	cancel()

	assistantID := assistantMessageUpdatedID(frames)
	if assistantID == "" {
		t.Fatalf("no assistant message.updated; frames: %s", summarize(frames))
	}
	partID := assistantID + "-text-0"

	// finalMsgIdx is the completion frame: the last assistant message.updated,
	// which messageEnd emits before its part loop.
	finalMsgIdx := -1
	for i, f := range frames {
		if f.Name != eventMessageUpdated {
			continue
		}
		var payload sseMessageUpdated
		if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
			continue
		}
		if payload.Info.Role == "assistant" {
			finalMsgIdx = i
		}
	}
	if finalMsgIdx == -1 {
		t.Fatalf("no assistant message.updated; frames: %s", summarize(frames))
	}

	sawTimedEnd := false
	for i, f := range frames {
		if f.Name != eventMessagePartUpdated {
			continue
		}
		var payload ssePartUpdated
		if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
			continue
		}
		if payload.Part.ID != partID {
			continue
		}
		if payload.Part.Time == nil {
			t.Errorf("frame %d for part %q carries no time; frames: %s", i, partID, summarize(frames))
		} else if payload.Part.Time.End > 0 {
			sawTimedEnd = true
		}
		if i > finalMsgIdx {
			t.Errorf("frame %d for part %q follows the completion message.updated at %d (republish); frames: %s",
				i, partID, finalMsgIdx, summarize(frames))
		}
	}
	if !sawTimedEnd {
		t.Errorf("no frame for part %q carried time.end > 0; the streamed end did not survive; frames: %s",
			partID, summarize(frames))
	}
}

// TestToolCallDeltaReachesTheStream drives a tool-call delta through a Host
// forwarder, which is the only place the mapper reads a streamed tool call. It
// is the test -race needs: no other committed test takes a host session through
// a tool, so a mapper that read the stream builder's live partial instead of the
// per-event copy went green on a path nothing exercised.
//
// The part id is the assertion that the two sources agree: a delta and the tool
// part the transcript reports are only the same part if the call id came from
// the message, because the id is built from it.
func TestToolCallDeltaReachesTheStream(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := openSSE(t, baseURL, ctx)
	defer func() { _ = resp.Body.Close() }()

	status, _ := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"parts":[{"type":"text","text":"Run: expr 20 + 22"}]}`)
	if status != http.StatusNoContent {
		t.Fatalf("prompt_async: expected 204, got %d", status)
	}

	frames := readSSEUntil(t, resp, func(frames []sseFrame) bool {
		return toolDeltaPartID(frames) != "" && toolUpdatedPartID(frames) != ""
	})
	cancel()

	partID := toolDeltaPartID(frames)
	if partID == "" {
		t.Fatalf("a tool call streamed no arguments delta; frames: %s", summarize(frames))
	}
	// The literal shape, not partID's own output: the id has to name the streamed
	// message and end in the call id, because a tool part is identified by its
	// call and not by its index.
	assistantID := assistantMessageUpdatedID(frames)
	if assistantID == "" {
		t.Fatalf("no assistant message.updated on the stream; frames: %s", summarize(frames))
	}
	if !strings.HasPrefix(partID, assistantID+"-") {
		t.Errorf("a delta's partID must belong to the streamed message %q, got %q", assistantID, partID)
	}
	_, callID, ok := strings.Cut(partID, "-tool-")
	if !ok || callID == "" {
		t.Fatalf("a tool delta's partID must carry the call id, got %q", partID)
	}
	toolPartSeen := false
	for _, f := range frames {
		if f.Name != eventMessagePartUpdated {
			continue
		}
		var payload ssePartUpdated
		if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
			continue
		}
		if payload.Part.Type != "tool" || payload.Part.CallID != callID {
			continue
		}
		toolPartSeen = true
		if payload.Part.ID != partID {
			t.Errorf("the tool part and the delta must name one part: %q against %q", payload.Part.ID, partID)
		}
	}
	if !toolPartSeen {
		t.Fatalf("no tool message.part.updated for call id %q reached the stream; frames: %s", callID, summarize(frames))
	}
}

// toolDeltaPartID returns the partID of the first streamed tool arguments delta.
func toolDeltaPartID(frames []sseFrame) string {
	for _, f := range frames {
		if f.Name != eventMessagePartDelta {
			continue
		}
		var payload ssePartDelta
		if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
			continue
		}
		if payload.Field == "arguments" {
			return payload.PartID
		}
	}
	return ""
}

// toolUpdatedPartID returns the id of the first streamed tool
// message.part.updated, or "" when none has arrived.
func toolUpdatedPartID(frames []sseFrame) string {
	for _, f := range frames {
		if f.Name != eventMessagePartUpdated {
			continue
		}
		var payload ssePartUpdated
		if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
			continue
		}
		if payload.Part.Type == "tool" {
			return payload.Part.ID
		}
	}
	return ""
}

// assistantMessageUpdatedID returns the id the stream announced for the
// assistant message, or "" when it announced none. The stream puts one stable
// id on the assistant message for the whole run; every caller parses that
// frame the same way rather than re-deriving the shape.
func assistantMessageUpdatedID(frames []sseFrame) string {
	for _, f := range frames {
		if f.Name != eventMessageUpdated {
			continue
		}
		var payload sseMessageUpdated
		if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
			continue
		}
		if payload.Info.Role == "assistant" && payload.Info.ID != "" {
			return payload.Info.ID
		}
	}
	return ""
}

// TestToolCallDeltaAgreesWithThePartItCompletes pins the one reachable
// unresolvable-call-id case: an OpenAI-compatible server that streams
// tool_calls with no id. toolPart and the transcript both build their tool id
// from the same empty value, so the delta must too, or it is a frame for a
// part nothing else ever announces. The arguments are the only live channel
// for a tool call in flight, so losing them leaves the bubble empty until the
// client refetches history.
func TestToolCallDeltaAgreesWithThePartItCompletes(t *testing.T) {
	mapper := &eventMapper{sessionID: "sess-guard", assistantID: "sess-guard"}
	ev := agent.MessageUpdateEvent{
		Message: agent.AgentMessage{Assistant: &agent.AssistantMessage{Role: "assistant"}},
		AssistantMessageEvent: ai.ToolCallDeltaEvent{
			ContentIndex: 0, Delta: `{"command":`,
			Partial: &ai.AssistantMessage{Content: []ai.AssistantContentBlock{
				ai.ToolCall{Name: "bash"},
			}},
		},
	}

	got, err := mapper.translate(ev, "")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	// The first frame creates the part on first sight and the second streams its
	// arguments; a delta before its create is the frame order the client
	// mis-renders.
	if len(got) != 2 || got[0].Type != eventMessagePartUpdated || got[1].Type != eventMessagePartDelta {
		t.Fatalf("an id-less tool call must create then stream its arguments, got %+v", got)
	}
	var createPayload ssePartUpdated
	if err := json.Unmarshal(mustMarshal(t, got[0].Data), &createPayload); err != nil {
		t.Fatalf("decode the create: %v", err)
	}
	var payload ssePartDelta
	if err := json.Unmarshal(mustMarshal(t, got[1].Data), &payload); err != nil {
		t.Fatalf("decode the delta: %v", err)
	}
	// The same empty call id toolPart and the transcript produce, so the delta
	// matches the part it completes.
	if want := "sess-guard-tool-"; createPayload.Part.ID != want || payload.PartID != want {
		t.Errorf("partID: expected %q on both create and delta, got create %q delta %q", want, createPayload.Part.ID, payload.PartID)
	}

	streamed := &ai.AssistantMessage{Content: []ai.AssistantContentBlock{ai.ToolCall{ID: "call-9", Name: "bash"}}}
	ev.AssistantMessageEvent = ai.ToolCallDeltaEvent{ContentIndex: 0, Delta: `{"command":`, Partial: streamed}
	got, err = mapper.translate(ev, "")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if len(got) != 2 || got[0].Type != eventMessagePartUpdated || got[1].Type != eventMessagePartDelta {
		t.Fatalf("a delta with a resolvable call id must create then emit one message.part.delta, got %+v", got)
	}
	if err := json.Unmarshal(mustMarshal(t, got[1].Data), &payload); err != nil {
		t.Fatalf("decode the delta: %v", err)
	}
	if want := "sess-guard-tool-call-9"; payload.PartID != want {
		t.Errorf("partID: expected %q, got %q", want, payload.PartID)
	}
}

// mustMarshal marshals v or fails the test.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %+v: %v", v, err)
	}
	return b
}

// TestFailedRunEmitsSessionError covers the failure report. Without it a prompt
// that streams deltas and then fails leaves the app with a blank bubble and a
// session it still believes is busy.
func TestFailedRunEmitsSessionError(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := openSSE(t, baseURL, ctx)
	defer func() { _ = resp.Body.Close() }()

	// test-faux fails an unrecognised request, and the failure is not one of the
	// retryable provider errors, so the run settles with one errored message.
	status, _ := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"parts":[{"type":"text","text":"serve-sse-failure-probe"}]}`)
	if status != http.StatusNoContent {
		t.Fatalf("prompt_async: expected 204, got %d", status)
	}

	frames := readSSEUntil(t, resp, func(frames []sseFrame) bool {
		for _, f := range frames {
			if f.Name == "session.error" {
				return true
			}
		}
		return false
	})
	cancel()

	var found *sseSessionError
	for _, f := range frames {
		if f.Name != "session.error" {
			continue
		}
		var payload sseSessionError
		if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
			continue
		}
		found = &payload
	}
	if found == nil {
		t.Fatalf("a failed run emitted no session.error; frames: %s", summarize(frames))
	}
	if found.SessionID != id {
		t.Errorf("session.error sessionID: expected %q, got %q", id, found.SessionID)
	}
	if !strings.Contains(found.Error.Message, "test-faux: unhandled request") {
		t.Errorf("session.error must carry the provider's reason, got %q", found.Error.Message)
	}
}

// TestSSEStreamCapsConcurrentConnections pins the cap's decrement. A leaked
// count is silent and permanent: once 16 slots are stuck the endpoint is dead for
// the life of the process, and only a reconnect can prove otherwise.
func TestSSEStreamCapsConcurrentConnections(t *testing.T) {
	baseURL, _ := promptServer(t)
	createPromptSession(t, baseURL)

	cancels := make([]context.CancelFunc, 0, maxSSEConnections)
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()
	for range maxSSEConnections {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		resp := openSSE(t, baseURL, ctx)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("connection %d: expected 200, got %d", len(cancels), resp.StatusCode)
		}
	}

	resp, err := http.Get(baseURL + "/global/event")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("connection %d: expected 503, got %d", maxSSEConnections+1, resp.StatusCode)
	}

	// Release one slot: the endpoint has to accept again, which it cannot if the
	// decrement is missing.
	cancels[0]()
	if !waitForSSESlot(t, baseURL) {
		t.Fatal("a disconnected stream did not release its slot")
	}
}

// waitForSSESlot polls until /global/event accepts a connection again.
func waitForSSESlot(t *testing.T, baseURL string) bool {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/global/event")
		if err != nil {
			t.Fatal(err)
		}
		status := resp.StatusCode
		_ = resp.Body.Close()
		if status == http.StatusOK {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// readSSEUntil reads frames until stop reports it has what it needs, the stream
// ends, or the budget elapses. The budget closes the body, so a regression that
// emits nothing fails with the frames it did emit instead of hanging to go
// test's 10-minute panic.
func readSSEUntil(t *testing.T, resp *http.Response, stop func([]sseFrame) bool) []sseFrame {
	t.Helper()
	timer := time.AfterFunc(30*time.Second, func() { _ = resp.Body.Close() })
	defer timer.Stop()
	return readSSE(t, resp.Body, stop)
}

// openSSE connects to /global/event under a cancellable context and returns the
// response. Cancelling the context is the client disconnect the stream's first
// of three ends is built on.
func openSSE(t *testing.T, baseURL string, ctx context.Context) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/global/event", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /global/event: expected 200, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type: expected text/event-stream, got %q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control: expected no-cache, got %q", got)
	}
	return resp
}

// readSSE reads frames until stop reports it has what it needs or the stream
// ends. stop sees only complete frames.
func readSSE(t *testing.T, body io.Reader, stop func([]sseFrame) bool) []sseFrame {
	t.Helper()
	var (
		frames []sseFrame
		name   string
		data   []string
	)
	reader := bufio.NewReader(body)
	flush := func() {
		if name == "" && len(data) == 0 {
			return
		}
		joined := strings.Join(data, "\n")
		// opencode frames carry no event: line; the type and properties live in
		// the data envelope. Unwrap it so callers keep seeing Name + payload.
		if name == "" {
			var env struct {
				Payload struct {
					Type       string          `json:"type"`
					Properties json.RawMessage `json:"properties"`
				} `json:"payload"`
			}
			if err := json.Unmarshal([]byte(joined), &env); err == nil && env.Payload.Type != "" {
				name = env.Payload.Type
				joined = string(env.Payload.Properties)
			}
		}
		frames = append(frames, sseFrame{Name: name, Data: joined})
		name, data = "", nil
	}
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			switch line = strings.TrimRight(line, "\r\n"); {
			case line == "":
				flush()
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = append(data, strings.TrimPrefix(line, "data: "))
			}
		}
		if err != nil {
			flush()
			return frames
		}
		if stop(frames) {
			return frames
		}
	}
}

// readHead reads an HTTP response head up to and including its blank line.
func readHead(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	var head strings.Builder
	for {
		line, err := reader.ReadString('\n')
		head.WriteString(line)
		if line == "\r\n" || line == "\n" {
			return head.String()
		}
		if err != nil {
			t.Fatalf("reading the response head: %v (got %q)", err, head.String())
		}
	}
}

// summarize renders frames for a failure message.
func summarize(frames []sseFrame) string {
	lines := make([]string, 0, len(frames))
	for _, f := range frames {
		lines = append(lines, f.Name+" "+truncate(f.Data, 120))
	}
	return strings.Join(lines, " | ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ─── /permission, /question*, /session/{id}/todo, /file/content ─────────────

// TestNewRegistersEveryRouteWithoutPanicking is the gate for the permission
// route's wildcard name. ServeMux rejects a pattern that reuses one wildcard
// name and HandleFunc panics on that error, so the wrong name aborts New and
// takes all thirty-four routes with it. The wire path is byte-identical either
// way, so a request cannot tell the two spellings apart; what this asserts is
// that New survives and the mux it built answers.
func TestNewRegistersEveryRouteWithoutPanicking(t *testing.T) {
	env := promptEnvFor(t)

	for _, path := range []string{"/permission", "/question"} {
		resp, err := http.Get(env.baseURL + path)
		if err != nil {
			t.Fatal(err)
		}
		status := resp.StatusCode
		_ = resp.Body.Close()
		if status == http.StatusNotFound {
			t.Errorf("%s: expected a registered route, got 404", path)
		}
	}
}

// TestPermissionSubmitRoutesAnswer501OnEveryMatchingPath verifies the one
// remaining 501 route answers without reading a body, looking up a session, or
// parsing an id: an unknown session is 501 here, not 404, because the client
// reads 404 as "host gone" and this route is not implemented either way. The
// GET /permission list answers 200 [] and is covered by
// TestPermissionListAnswersEmptyArray.
func TestPermissionSubmitRoutesAnswer501OnEveryMatchingPath(t *testing.T) {
	env := promptEnvFor(t)
	id := createPromptSession(t, env.baseURL)

	for _, path := range []string{
		"/session/" + id + "/permissions/per-1",
		"/session/no-such-session/permissions/per-1",
		"/session/anything/permissions/anything-else",
	} {
		status, body := postJSON(t, env.baseURL, path, `{}`)
		if status != http.StatusNotImplemented {
			t.Errorf("POST %s: expected 501, got %d (%s)", path, status, body)
			continue
		}
		var dto errorDTO
		if err := json.Unmarshal(body, &dto); err != nil {
			t.Errorf("POST %s: body is not an error object: %v (%s)", path, err, body)
			continue
		}
		if dto.Error != "not implemented" {
			t.Errorf("POST %s: expected \"not implemented\", got %q", path, dto.Error)
		}
	}

	// No catch-all is registered, so the one-segment form is ServeMux's own 404
	// rather than a 501 for a route that was never registered.
	if status, _ := postJSON(t, env.baseURL, "/session/"+id+"/permissions", `{}`); status != http.StatusNotFound {
		t.Errorf("POST /session/{id}/permissions: expected 404, got %d", status)
	}
}

// TestAPIHealthAnswersHealthyWithoutVersion pins the KKCode connect probe:
// 200, healthy true, and no version key (that belongs to /global/health).
func TestAPIHealthAnswersHealthyWithoutVersion(t *testing.T) {
	env := promptEnvFor(t)
	status, body := getRaw(t, env.baseURL, "/api/health")
	if status != http.StatusOK {
		t.Fatalf("GET /api/health: expected 200, got %d (%s)", status, body)
	}
	var got apiHealthResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("GET /api/health: decode: %v (%s)", err, body)
	}
	if !got.Healthy {
		t.Errorf("GET /api/health: expected healthy=true, got %s", body)
	}
	if strings.Contains(string(body), `"version"`) {
		t.Errorf("GET /api/health: expected no version key, got %s", body)
	}
}

// TestExperimentalSessionMatchesSessionList proves the two list routes agree
// when no session is archived: with one seeded session the two bodies decode to
// the same non-empty []sessionDTO, and the foreign-directory validation still
// applies (200 on the root, 400 on a mismatch). They diverge once a session is
// archived — /experimental/session omits it while /session stays inclusive; see
// TestDeleteSessionArchives.
func TestExperimentalSessionMatchesSessionList(t *testing.T) {
	env := promptEnvFor(t)
	createPromptSession(t, env.baseURL)

	statusList, bodyList := getRaw(t, env.baseURL, "/session")
	statusExp, bodyExp := getRaw(t, env.baseURL, "/experimental/session")
	if statusList != http.StatusOK || statusExp != http.StatusOK {
		t.Fatalf("/session=%d, /experimental/session=%d, want both 200", statusList, statusExp)
	}
	var list, exp []sessionDTO
	if err := json.Unmarshal(bodyList, &list); err != nil {
		t.Fatalf("decode /session: %v (%s)", err, bodyList)
	}
	if err := json.Unmarshal(bodyExp, &exp); err != nil {
		t.Fatalf("decode /experimental/session: %v (%s)", err, bodyExp)
	}
	if len(list) == 0 {
		t.Fatal("/session returned no sessions after seeding one; the compare would be trivial")
	}
	if !reflect.DeepEqual(list, exp) {
		t.Errorf("/experimental/session differs from /session:\n got %+v\nwant %+v", exp, list)
	}

	root, err := filepath.EvalSymlinks(env.services.CWD())
	if err != nil {
		t.Fatal(err)
	}
	if status, body := getRaw(t, env.baseURL, "/experimental/session?directory="+url.QueryEscape(root)); status != http.StatusOK {
		t.Errorf("GET /experimental/session?directory=<root>: expected 200, got %d (%s)", status, body)
	}
	if status, _ := getRaw(t, env.baseURL, "/experimental/session?directory=/somewhere/else"); status != http.StatusBadRequest {
		t.Errorf("GET /experimental/session foreign directory: expected 400, got %d", status)
	}
}

// TestDeleteSessionArchives proves DELETE /session/{id} archives rather than
// hard-deletes: it answers 200 true, stamps time.archived, hides the session
// from /experimental/session, keeps it in /session, and 404s an unknown id.
func TestDeleteSessionArchives(t *testing.T) {
	env := promptEnvFor(t)
	id := createPromptSession(t, env.baseURL)

	// DELETE → 200, application/json, body "true".
	req, _ := http.NewRequest(http.MethodDelete, env.baseURL+"/session/"+id, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("DELETE Content-Type = %q, want application/json", ct)
	}
	if got := strings.TrimSpace(string(body)); got != "true" {
		t.Errorf("DELETE body = %q, want true", got)
	}

	// time.archived carries the archive timestamp on GET /session/{id}.
	status, gbody := sessionRequest(t, "GET", env.baseURL, id, "")
	if status != http.StatusOK {
		t.Fatalf("GET /session/{id} status = %d, want 200", status)
	}
	dto := decodeDTO(t, gbody)
	if dto.Time.Archived == nil {
		t.Fatalf("GET /session/{id} time.archived absent after DELETE")
	}
	if *dto.Time.Archived <= 0 {
		t.Errorf("time.archived = %d, want a positive ms stamp", *dto.Time.Archived)
	}

	// Absent from /experimental/session …
	_, ebody := getRaw(t, env.baseURL, "/experimental/session")
	var exp []sessionDTO
	if err := json.Unmarshal(ebody, &exp); err != nil {
		t.Fatalf("decode /experimental/session: %v", err)
	}
	for _, s := range exp {
		if s.ID == id {
			t.Errorf("archived session %q present in /experimental/session", id)
		}
	}

	// … still present in /session.
	_, lbody := getRaw(t, env.baseURL, "/session")
	var list []sessionDTO
	if err := json.Unmarshal(lbody, &list); err != nil {
		t.Fatalf("decode /session: %v", err)
	}
	found := false
	for _, s := range list {
		if s.ID == id {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("archived session %q missing from /session", id)
	}

	// Unknown id → 404.
	if status, _ := sessionRequest(t, "DELETE", env.baseURL, "unknown-session", ""); status != http.StatusNotFound {
		t.Errorf("DELETE unknown session status = %d, want 404", status)
	}
}

// TestAPICommandShape pins the exact body KKCode decodes: a global project id,
// both directories equal to EvalSymlinks(CWD()), and data as [] never null.
func TestAPICommandShape(t *testing.T) {
	env := promptEnvFor(t)
	status, body := getRaw(t, env.baseURL, "/api/command")
	if status != http.StatusOK {
		t.Fatalf("GET /api/command: expected 200, got %d (%s)", status, body)
	}
	var cmd apiCommandDTO
	if err := json.Unmarshal(body, &cmd); err != nil {
		t.Fatalf("GET /api/command: decode: %v (%s)", err, body)
	}
	want, err := filepath.EvalSymlinks(env.services.CWD())
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Location.Project.ID != "global" {
		t.Errorf("GET /api/command: expected project id global, got %q", cmd.Location.Project.ID)
	}
	if cmd.Location.Directory != want {
		t.Errorf("GET /api/command: location.directory = %q, want %q", cmd.Location.Directory, want)
	}
	if cmd.Location.Project.Directory != want {
		t.Errorf("GET /api/command: location.project.directory = %q, want %q", cmd.Location.Project.Directory, want)
	}
	if cmd.Data == nil {
		t.Errorf("GET /api/command: data is null, want [] (%s)", body)
	}
	if len(cmd.Data) != 0 {
		t.Errorf("GET /api/command: expected empty data, got %v", cmd.Data)
	}
}

// TestPermissionListAnswersEmptyArray pins the repointed route: 200 with the
// literal [] body, never null and never the old 501.
func TestPermissionListAnswersEmptyArray(t *testing.T) {
	env := promptEnvFor(t)
	status, body := getRaw(t, env.baseURL, "/permission")
	if status != http.StatusOK {
		t.Fatalf("GET /permission: expected 200, got %d (%s)", status, body)
	}
	if got := strings.TrimSpace(string(body)); got != "[]" {
		t.Errorf("GET /permission: expected [], got %s", got)
	}
}

// TestNewRoutesIgnoreAForeignDirectory proves tolerance is deliberate: KKCode
// appends ?directory= to these, and a 400 here breaks the first connect.
func TestNewRoutesIgnoreAForeignDirectory(t *testing.T) {
	env := promptEnvFor(t)
	for _, path := range []string{"/api/health", "/config", "/api/command"} {
		status, body := getRaw(t, env.baseURL, path+"?directory=/somewhere/else")
		if status != http.StatusOK {
			t.Errorf("GET %s?directory=/somewhere/else: expected 200, got %d (%s)", path, status, body)
		}
	}
}

// TestQuestionListAnswersAnEmptyArrayWithNothingPending verifies the empty case
// is 200 with an array, never 404 and never null: the client polls this to fill
// a list and spends 404 on "the host is gone".
func TestQuestionListAnswersAnEmptyArrayWithNothingPending(t *testing.T) {
	env := promptEnvFor(t)
	resp, err := http.Get(env.baseURL + "/question")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /question: expected 200, got %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(body)); got != "[]" {
		t.Errorf("GET /question: expected [], got %s", got)
	}
}

// TestQuestionReplyRejectsUnknownIDsAndBadBodies covers every status the reply
// route owes a client, without a pending question: the body is validated first,
// so each of these is decided before any id is looked up.
func TestQuestionReplyRejectsUnknownIDsAndBadBodies(t *testing.T) {
	env := promptEnvFor(t)
	oversize := `{"choice":"` + strings.Repeat("x", maxBodyBytes+1024) + `"}`

	for name, tc := range map[string]struct {
		path string
		body string
		want int
	}{
		"unknown id, reply":        {"/question/q-nope/reply", `{"choice":"Postgres"}`, http.StatusNotFound},
		"unknown id, reject":       {"/question/q-nope/reject", `{"declined":true}`, http.StatusNotFound},
		"unknown id, empty body":   {"/question/q-nope/reject", ``, http.StatusNotFound},
		"neither choice nor other": {"/question/q-nope/reply", `{}`, http.StatusBadRequest},
		"both choice and other":    {"/question/q-nope/reply", `{"choice":"a","other":"b"}`, http.StatusBadRequest},
		"malformed body":           {"/question/q-nope/reply", `{"choice":`, http.StatusBadRequest},
		"oversize body":            {"/question/q-nope/reply", oversize, http.StatusRequestEntityTooLarge},
	} {
		status, body := postJSON(t, env.baseURL, tc.path, tc.body)
		if status != tc.want {
			t.Errorf("%s: expected %d, got %d (%s)", name, tc.want, status, body)
		}
	}
}

// TestAskReplyRoundTrip drives the whole question path over a real listener: a
// blocked ask_user appears on the stream and in GET /question, a reply answers
// 200 with no body, the chosen option reaches the transcript, and the question
// is gone afterwards so it cannot be answered twice.
func TestAskReplyRoundTrip(t *testing.T) {
	env := promptEnvFor(t)
	id := createPromptSession(t, env.baseURL)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream := openSSE(t, env.baseURL, ctx)
	defer func() { _ = stream.Body.Close() }()

	promptAsyncOK(t, env.baseURL, id, "Run: ask a question")

	question := waitForQuestion(t, env.baseURL)
	if got, want := question.Question, "Which store should the service use?"; got != want {
		t.Errorf("question: expected %q, got %q", want, got)
	}
	if len(question.Options) != 2 {
		t.Errorf("question: expected 2 options, got %d (%+v)", len(question.Options), question.Options)
	}
	if got, want := question.Options, []askuser.Option{
		{Label: "Postgres", Description: "already deployed"},
		{Label: "SQLite", Description: "simplest on one host"},
	}; !slices.Equal(got, want) {
		t.Errorf("question options: expected %+v, got %+v", want, got)
	}
	if question.SessionID != id {
		t.Errorf("sessionID: expected %q, got %q", id, question.SessionID)
	}
	if question.ID == "" {
		t.Fatal("question: the entry carries no id to reply to")
	}

	status, body := postJSON(t, env.baseURL, "/question/"+question.ID+"/reply", `{"choice":"Postgres"}`)
	if status != http.StatusOK {
		t.Fatalf("reply: expected 200, got %d (%s)", status, body)
	}
	if len(body) != 0 {
		t.Errorf("reply: expected no body, got %q", body)
	}

	frames := readSSEUntil(t, stream, func(f []sseFrame) bool {
		return hasFrame(f, "question.asked") && hasFrame(f, "question.replied")
	})
	if !hasFrame(frames, "question.asked") {
		t.Errorf("question.asked never arrived on the stream: %s", summarize(frames))
	}
	if !hasFrame(frames, "question.replied") {
		t.Errorf("question.replied never arrived on the stream: %s", summarize(frames))
	}

	waitForIdle(t, env.baseURL, id)
	waitForTranscriptText(t, env.baseURL, id, "User chose: Postgres")
	if got := readQuestions(t, env.baseURL); len(got) != 0 {
		t.Errorf("GET /question after the answer: expected none pending, got %+v", got)
	}
}

// TestAskRejectRoundTrip is the same path with the other answer: the declined
// text is what the model reads, so it has to reach the transcript.
func TestAskRejectRoundTrip(t *testing.T) {
	env := promptEnvFor(t)
	id := createPromptSession(t, env.baseURL)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream := openSSE(t, env.baseURL, ctx)
	defer func() { _ = stream.Body.Close() }()

	promptAsyncOK(t, env.baseURL, id, "Run: ask a question")
	question := waitForQuestion(t, env.baseURL)

	status, body := postJSON(t, env.baseURL, "/question/"+question.ID+"/reject", `{"declined":true}`)
	if status != http.StatusOK {
		t.Fatalf("reject: expected 200, got %d (%s)", status, body)
	}
	if len(body) != 0 {
		t.Errorf("reject: expected no body, got %q", body)
	}

	frames := readSSEUntil(t, stream, func(f []sseFrame) bool {
		return hasFrame(f, "question.asked") && hasFrame(f, "question.rejected")
	})
	if !hasFrame(frames, "question.rejected") {
		t.Errorf("question.rejected never arrived on the stream: %s", summarize(frames))
	}

	waitForIdle(t, env.baseURL, id)
	waitForTranscriptText(t, env.baseURL, id, "User declined to answer")
}

// TestAbortWhileAQuestionIsPendingRejectsIt verifies the only cancel a client
// can reach: the entry goes away, the card resolves, the tool fails with the
// aborted error rather than a fabricated answer, and a late reply is 404 instead
// of a silent success.
func TestAbortWhileAQuestionIsPendingRejectsIt(t *testing.T) {
	env := promptEnvFor(t)
	id := createPromptSession(t, env.baseURL)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream := openSSE(t, env.baseURL, ctx)
	defer func() { _ = stream.Body.Close() }()

	promptAsyncOK(t, env.baseURL, id, "Run: ask a question")
	question := waitForQuestion(t, env.baseURL)

	// A pending question holds the session's run slot, so a second prompt is
	// refused for as long as the phone deliberates. This is the one-run-per-
	// session invariant doing its job, not a bug.
	if status, _ := postJSON(t, env.baseURL, "/session/"+id+"/prompt_async",
		`{"parts":[{"type":"text","text":"hi"}]}`); status != http.StatusConflict {
		t.Errorf("prompt_async while a question is pending: expected 409, got %d", status)
	}

	if status, _ := postJSON(t, env.baseURL, "/session/"+id+"/abort", `{}`); status != http.StatusOK {
		t.Fatalf("abort: expected 200, got %d", status)
	}

	frames := readSSEUntil(t, stream, func(f []sseFrame) bool { return hasFrame(f, "question.rejected") })
	if !hasFrame(frames, "question.rejected") {
		t.Errorf("question.rejected never arrived on the stream: %s", summarize(frames))
	}

	if got := readQuestions(t, env.baseURL); len(got) != 0 {
		t.Errorf("GET /question after the abort: expected none pending, got %+v", got)
	}
	if status, _ := postJSON(t, env.baseURL, "/question/"+question.ID+"/reply",
		`{"choice":"Postgres"}`); status != http.StatusNotFound {
		t.Errorf("reply after the abort: expected 404, got %d", status)
	}

	waitForIdle(t, env.baseURL, id)
	waitForTranscriptText(t, env.baseURL, id, "question was aborted")
}

// TestAskHonoursAReplyThatClaimedTheEntryBeforeAbort pins the fallback at
// serve_question.go:128-130. A reply claims the entry and returns 200 before the
// run's abort is observed; the blocked ask must then return that answer rather
// than ctx.Err(). synctest.Wait makes the interleaving deterministic: with the
// answers slot empty, ctx.Done is the only ready select case, so the abort
// branch is taken, and the second Wait returns only once ask is durably blocked
// on the reply's answer.
func TestAskHonoursAReplyThatClaimedTheEntryBeforeAbort(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &Server{host: codinghost.New(nil, nil)}
		s.questions.seed()

		want := askuser.Answer{Choice: "Postgres"}
		q := askuser.Question{Question: "Which store should the service use?"}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		type outcome struct {
			answer askuser.Answer
			err    error
		}
		done := make(chan outcome, 1)
		go func() {
			answer, err := s.ask(ctx, "session-1", q)
			done <- outcome{answer, err}
		}()

		// ask registers the question, publishes, then blocks in select.
		synctest.Wait()

		// A concurrent reply takes the entry and will return 200: take()
		// succeeds exactly as replyQuestion's take does before its send.
		dtos := s.questions.list()
		if len(dtos) != 1 {
			t.Fatalf("expected one pending question, got %d", len(dtos))
		}
		pending, ok := s.questions.take(dtos[0].ID)
		if !ok {
			t.Fatal("the reply could not claim the entry")
		}

		// Abort. The answers slot is empty, so ctx.Done is the only ready case
		// and ask deterministically takes the abort branch; Wait returns only
		// once ask is durably blocked on the reply's answer.
		cancel()
		synctest.Wait()

		pending.answers <- want

		got := <-done
		if got.err != nil || got.answer != want {
			t.Fatalf("a reply that already answered 200 was lost: answer=%+v err=%v", got.answer, got.err)
		}
	})
}

// TestTwoConcurrentRepliesToOneQuestionYieldOneWinner covers the answer-once
// window: taking the entry and its channel has to happen under one lock, or the
// loser's send blocks forever on a capacity-one slot the winner already filled.
func TestTwoConcurrentRepliesToOneQuestionYieldOneWinner(t *testing.T) {
	env := promptEnvFor(t)
	id := createPromptSession(t, env.baseURL)

	promptAsyncOK(t, env.baseURL, id, "Run: ask a question")
	question := waitForQuestion(t, env.baseURL)

	statuses := make(chan int, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			resp, err := http.Post(env.baseURL+"/question/"+question.ID+"/reply", "application/json", strings.NewReader(`{"choice":"Postgres"}`))
			if err != nil {
				statuses <- -1
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			statuses <- resp.StatusCode
		}()
	}
	close(start)

	var ok, notFound, other int
	for range 2 {
		switch status := <-statuses; status {
		case http.StatusOK:
			ok++
		case http.StatusNotFound:
			notFound++
		default:
			other++
			t.Errorf("reply: unexpected status %d", status)
		}
	}
	if ok != 1 || notFound != 1 || other != 0 {
		t.Fatalf("two concurrent replies: expected one 200 and one 404, got %d 200s and %d 404s", ok, notFound)
	}

	// One delivery: the transcript carries the answer on the tool part once.
	// If the loser's send were not blocked by the single-slot channel it would
	// show up here as a second completed tool result.
	waitForIdle(t, env.baseURL, id)
	if got := countToolResults(t, env.baseURL, id, "User chose: Postgres"); got != 1 {
		t.Errorf("the transcript carries the answer on %d tool parts, want exactly 1", got)
	}
}

// TestSessionTodoServesTheNewestTodowriteResult verifies the list is read out
// of the transcript rather than held in this process: it survives a server that
// owns no session, it is empty before the first write, and the same items ride
// the tool part's own metadata on the transcript route.
func TestSessionTodoServesTheNewestTodowriteResult(t *testing.T) {
	env := promptEnvFor(t)
	id := createPromptSession(t, env.baseURL)

	// No todowrite yet: 200 and an array, never null.
	resp, err := http.Get(env.baseURL + "/session/" + id + "/todo")
	if err != nil {
		t.Fatal(err)
	}
	empty, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /todo before any write: expected 200, got %d", resp.StatusCode)
	}
	if got := strings.TrimSpace(string(empty)); got != "[]" {
		t.Errorf("GET /todo before any write: expected [], got %s", got)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream := openSSE(t, env.baseURL, ctx)
	defer func() { _ = stream.Body.Close() }()

	promptAsyncOK(t, env.baseURL, id, "Run: write todos")
	waitForIdle(t, env.baseURL, id)

	want := []tools.Todo{
		{ID: "todo-1", Content: "read the spec", Status: "completed", Priority: "high"},
		{ID: "todo-2", Content: "write the code", Status: "in_progress", Priority: "medium"},
		{ID: "todo-3", Content: "cut the changelog", Status: "pending", Priority: "low"},
	}
	got := waitForTodos(t, env.baseURL, id, len(want))
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("todo[%d]: got %+v, want %+v", i, got[i], want[i])
		}
	}

	frames := readSSEUntil(t, stream, func(f []sseFrame) bool { return hasFrame(f, "todo.updated") })
	if !hasFrame(frames, "todo.updated") {
		t.Errorf("todo.updated never arrived on the stream: %s", summarize(frames))
	}

	// The recovery surface carries the same items, because a client with no
	// replay rebuilds its todo panel from history rather than from the stream.
	var state struct {
		Metadata struct {
			Todos []tools.Todo `json:"todos"`
		} `json:"metadata"`
	}
	found := false
	for _, m := range readTranscript(t, env.baseURL, id) {
		for _, p := range m.Parts {
			if p.Tool != tools.Name {
				continue
			}
			found = true
			if err := json.Unmarshal(p.State, &state); err != nil {
				t.Fatalf("the todowrite part's state is not an object: %v (%s)", err, p.State)
			}
		}
	}
	if !found {
		t.Fatalf("no %s tool part in the transcript", tools.Name)
	}
	if len(state.Metadata.Todos) != len(want) {
		t.Fatalf("metadata.todos: got %d items, want %d", len(state.Metadata.Todos), len(want))
	}
	for i := range want {
		if state.Metadata.Todos[i] != want[i] {
			t.Errorf("metadata.todos[%d]: got %+v, want %+v", i, state.Metadata.Todos[i], want[i])
		}
	}

	// A session is a file and the server is not: the list must be there for a
	// client that reconnects to a server which owns no live session.
	got = readTodos(t, restartServer(t, env), id)
	if len(got) != len(want) {
		t.Fatalf("after a restart: got %d items, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("after a restart, todo[%d]: got %+v, want %+v", i, got[i], want[i])
		}
	}

	if status := getStatus(t, restartServer(t, env), "/session/no-such-session/todo"); status != http.StatusNotFound {
		t.Errorf("GET /todo for an unknown session: expected 404, got %d", status)
	}

	// Newest wins. One write cannot tell the newest result from the first one
	// found, so the same session writes twice: /todo is the second list, and a
	// reader that stops at the first match sees the stale one.
	promptAsyncOK(t, env.baseURL, id, "Run: rewrite todos")
	waitForIdle(t, env.baseURL, id)
	newest := []tools.Todo{
		{ID: "todo-1", Content: "read the spec", Status: "completed", Priority: "high"},
		{ID: "todo-2", Content: "write the code", Status: "in_progress", Priority: "medium"},
		{ID: "todo-3", Content: "cut the changelog", Status: "completed", Priority: "low"},
		{ID: "todo-4", Content: "ship the fix round", Status: "pending", Priority: "high"},
	}
	after := waitForTodos(t, env.baseURL, id, len(newest))
	if len(after) != len(newest) {
		t.Fatalf("after the second write: got %d items, want the newest list's %d", len(after), len(newest))
	}
	for i := range newest {
		if after[i] != newest[i] {
			t.Errorf("after the second write, todo[%d]: got %+v, want %+v", i, after[i], newest[i])
		}
	}
}

// TestRejectedTodowriteDoesNotBroadcastTodoUpdated pins the guard at
// serve_sse.go:316: a todowrite the agent rejects carries nil Details, and a
// client must not see an empty todo.updated for it. The run's own settle frame
// bounds the read, and a protocol-level control proves the rejected call was
// emitted, so the absence cannot pass vacuously.
func TestRejectedTodowriteDoesNotBroadcastTodoUpdated(t *testing.T) {
	env := promptEnvFor(t)
	id := createPromptSession(t, env.baseURL)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream := openSSE(t, env.baseURL, ctx)
	defer func() { _ = stream.Body.Close() }()

	promptAsyncOK(t, env.baseURL, id, "Run: failed todowrite")

	frames := readSSEUntil(t, stream, func(f []sseFrame) bool {
		if hasFrame(f, "todo.updated") {
			return true
		}
		return slices.ContainsFunc(f, func(fr sseFrame) bool {
			if fr.Name != "session.status" {
				return false
			}
			var st sessionStatusDTO
			return json.Unmarshal([]byte(fr.Data), &st) == nil && st.Type == "idle"
		})
	})

	// Protocol-level positive control: the rejected call reached the wire as a
	// tool part with status "error". partDTO/ssePartUpdated are the wire shapes
	// the client decodes, so this asserts the event, not an error string.
	rejected := slices.ContainsFunc(frames, func(fr sseFrame) bool {
		if fr.Name != "message.part.updated" {
			return false
		}
		var payload struct {
			Part partDTO `json:"part"`
		}
		if json.Unmarshal([]byte(fr.Data), &payload) != nil {
			return false
		}
		if payload.Part.Type != "tool" || payload.Part.Tool != tools.Name {
			return false
		}
		var state struct {
			Status string `json:"status"`
		}
		return json.Unmarshal(payload.Part.State, &state) == nil && state.Status == "error"
	})
	if !rejected {
		t.Errorf("the rejected todowrite never reached the stream as an error tool part: %s", summarize(frames))
	}

	if hasFrame(frames, "todo.updated") {
		t.Errorf("a rejected todowrite broadcast todo.updated: %s", summarize(frames))
	}
}

// TestFileContentServesTextInsideTheRootAndRefusesTheRest covers the allowlist
// on the read path, including the control the client leans on this server for:
// a symlink out of the project is a refusal, not a read.
func TestFileContentServesTextInsideTheRootAndRefusesTheRest(t *testing.T) {
	env := promptEnvFor(t)
	root := env.services.CWD()

	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("notes.txt", "hello from the project\n")

	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("not yours\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testenv.Symlink(t, outside, filepath.Join(root, "escape-link"))
	// A link whose target is gone: it resolves to nothing, which is a refusal
	// and not a missing path, because the path the client named does exist.
	testenv.Symlink(t, filepath.Join(root, "no-such-target.txt"), filepath.Join(root, "dangling-link"))
	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "invalid.txt"), []byte{'a', 0xff, 0xfe, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	// A fixed size, deliberately not derived from the cap: a fixture sized off
	// the constant it is testing passes whether the constant is 8 MiB or 32.
	if err := os.WriteFile(filepath.Join(root, "huge.txt"), []byte(strings.Repeat("x", 9<<20)), 0o644); err != nil {
		t.Fatal(err)
	}

	status, body := getRaw(t, env.baseURL, "/file/content?path=notes.txt")
	if status != http.StatusOK {
		t.Fatalf("a text file inside the root: expected 200, got %d (%s)", status, body)
	}
	var payload map[string]string
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("the 200 body is not an object: %v (%s)", err, body)
	}
	if len(payload) != 1 || payload["content"] != "hello from the project\n" {
		t.Errorf("expected exactly {\"content\":\"hello from the project\\n\"}, got %s", body)
	}

	for name, path := range map[string]string{
		"missing file":  "no-such-file.txt",
		"escaping path": "../" + filepath.Base(filepath.Dir(outside)) + "/" + filepath.Base(outside),
		"absolute path": outside,
		"symlink out":   "escape-link",
		"dangling link": "dangling-link",
		"directory":     ".",
		"binary (NUL)":  "binary.bin",
		"invalid UTF-8": "invalid.txt",
		"over the cap":  "huge.txt",
	} {
		status, body := getRaw(t, env.baseURL, "/file/content?path="+url.QueryEscape(path))
		want := map[string]int{
			"missing file":  http.StatusNotFound,
			"escaping path": http.StatusForbidden,
			"absolute path": http.StatusForbidden,
			"symlink out":   http.StatusForbidden,
			"dangling link": http.StatusForbidden,
			"directory":     http.StatusBadRequest,
			"binary (NUL)":  http.StatusUnsupportedMediaType,
			"invalid UTF-8": http.StatusUnsupportedMediaType,
			"over the cap":  http.StatusRequestEntityTooLarge,
		}[name]
		if status != want {
			t.Errorf("%s (%s): expected %d, got %d (%s)", name, path, want, status, body)
		}
	}

	// A file this process may stat but not open: the read fails after the size
	// check already passed, so the handler must answer 500 rather than claim
	// the file is missing. Root bypasses permission bits, and on Windows mode
	// 0000 only sets the read-only attribute, which does not stop the owner
	// reading it, so neither host can produce the failure.
	if os.Geteuid() != 0 && runtime.GOOS != "windows" {
		write("unreadable.txt", "you cannot read me\n")
		if err := os.Chmod(filepath.Join(root, "unreadable.txt"), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "unreadable.txt"), 0o644) })
		if status, body := getRaw(t, env.baseURL, "/file/content?path=unreadable.txt"); status != http.StatusInternalServerError {
			t.Errorf("an unreadable file: expected 500, got %d (%s)", status, body)
		}
	}
}

// TestFileContentRefusesASymlinkSwappedInAfterResolution drives the
// resolve->open window directly: beforeOpen replaces a path component with a
// symlink out of the root after the handler has resolved it, exactly what a
// writer inside the root can do between resolveInRoot and the open. The kernel
// walk inside os.Root refuses the swapped symlink; replace that confinement
// with a plain os.Open of the joined path and this test serves the outside
// file instead.
//
// Two cases swap different components — the leaf file, and an intermediate
// directory. The second defeats a leaf-only check that would leave an in-root
// intermediate component free to be replaced.
func TestFileContentRefusesASymlinkSwappedInAfterResolution(t *testing.T) {
	root := t.TempDir()
	services, err := coding.NewServices(coding.ServicesOptions{
		CWD:            root,
		AgentDir:       t.TempDir(),
		ProjectTrusted: func() *bool { b := true; return &b }(),
	})
	if err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "notes.txt")
	if err := os.WriteFile(outside, []byte("not yours\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		path    string
		prepare func(t *testing.T)
		swap    func(t *testing.T)
	}{
		{
			name: "leaf-file",
			path: "notes.txt",
			prepare: func(t *testing.T) {
				if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("inside\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			swap: func(t *testing.T) {
				if err := os.Remove(filepath.Join(root, "notes.txt")); err != nil {
					t.Fatal(err)
				}
				testenv.Symlink(t, outside, filepath.Join(root, "notes.txt"))
			},
		},
		{
			name: "intermediate-directory",
			path: "sub/notes.txt",
			prepare: func(t *testing.T) {
				if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "sub", "notes.txt"), []byte("inside\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			swap: func(t *testing.T) {
				if err := os.RemoveAll(filepath.Join(root, "sub")); err != nil {
					t.Fatal(err)
				}
				testenv.Symlink(t, outsideDir, filepath.Join(root, "sub"))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.prepare(t)

			// Runs on the handler's own goroutine (serveFileContent is
			// synchronous), so t.Fatal is safe here.
			beforeOpen := func() { tc.swap(t) }

			srv := &Server{services: services}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/file/content?path="+tc.path, nil)
			srv.serveFileContent(rec, req, beforeOpen)

			// Inspect the body before the status so both assertions stay live:
			// a leak is the more important failure, and a t.Fatalf on the
			// status first would make the leak check unreachable.
			if body := rec.Body.String(); strings.Contains(body, "not yours") {
				t.Fatalf("the outside file's contents were served (%d): %s", rec.Code, body)
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("a path swapped for an outside symlink: expected 403, got %d (%s)", rec.Code, rec.Body)
			}
		})
	}
}

// TestListFilesRefusesASymlinkSwappedInAfterResolution drives the
// resolve->open window directly: beforeOpen replaces a path component with a
// symlink out of the root after the handler has resolved it, exactly what a
// writer inside the root can do between resolveInRoot and the open. The kernel
// walk inside os.Root refuses the swapped symlink; replace that confinement with
// a plain os.ReadDir of the resolved string and this test serves the outside
// directory's entries instead.
//
// Two cases swap different components — the requested directory itself, and an
// intermediate directory. The second defeats a leaf-only check that would leave
// an in-root intermediate component free to be replaced.
func TestListFilesRefusesASymlinkSwappedInAfterResolution(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		prepare func(t *testing.T, root, outsideDir string)
	}{
		{
			name: "requested-directory",
			path: "sub",
			prepare: func(t *testing.T, root, outsideDir string) {
				if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(outsideDir, "outside-name"), []byte("not yours\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "parent-directory",
			path: "sub/nested",
			prepare: func(t *testing.T, root, outsideDir string) {
				if err := os.MkdirAll(filepath.Join(root, "sub", "nested"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(outsideDir, "nested"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(outsideDir, "nested", "outside-name"), []byte("not yours\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Per-subtest root and outsideDir: case 1's leftover symlink cannot
			// seed case 2.
			root, outsideDir := t.TempDir(), t.TempDir()
			tc.prepare(t, root, outsideDir)

			services, err := coding.NewServices(coding.ServicesOptions{
				CWD:            root,
				AgentDir:       t.TempDir(),
				ProjectTrusted: func() *bool { b := true; return &b }(),
			})
			if err != nil {
				t.Fatal(err)
			}

			// The swapped component must be a real directory outside the root,
			// or os.Stat follows the link, 404s, and the open is never reached.
			swap := func() {
				if err := os.RemoveAll(filepath.Join(root, "sub")); err != nil {
					t.Fatal(err)
				}
				testenv.Symlink(t, outsideDir, filepath.Join(root, "sub"))
			}

			srv := &Server{services: services}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/file?path="+tc.path, nil)
			// Runs on the handler's own goroutine (serveListFiles is
			// synchronous), so t.Fatal is safe in the hook.
			srv.serveListFiles(rec, req, swap)

			// Inspect the body before the status so both assertions stay live:
			// a leak is the more important failure, and a t.Fatalf on the
			// status first would make the leak check unreachable.
			if body := rec.Body.String(); strings.Contains(body, "outside-name") {
				t.Fatalf("the outside directory's entries were served (%d): %s", rec.Code, body)
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("a path swapped for an outside symlink: expected 403, got %d (%s)", rec.Code, rec.Body)
			}
		})
	}
}

// TestListFilesEntryPathsAndFileRejection pins the DTO loop to the per-entry
// relative path: if it used the directory's own rel instead, every Path would be
// "." (or the directory's name) rather than the entry's, and a path that resolves
// to a regular file would still be listed instead of answered 400.
func TestListFilesEntryPathsAndFileRejection(t *testing.T) {
	root := t.TempDir()
	services, err := coding.NewServices(coding.ServicesOptions{
		CWD:            root,
		AgentDir:       t.TempDir(),
		ProjectTrusted: func() *bool { b := true; return &b }(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha.txt", "gamma.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "beta"), 0o755); err != nil {
		t.Fatal(err)
	}

	srv := &Server{services: services}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/file?path=.", nil)
	srv.serveListFiles(rec, req, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("listing the root: expected 200, got %d (%s)", rec.Code, rec.Body)
	}
	var dtos []fileNodeDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dtos); err != nil {
		t.Fatalf("decode listing: %v (%s)", err, rec.Body)
	}
	if len(dtos) != 3 {
		t.Fatalf("path=. returned %d entries, want 3: %+v", len(dtos), dtos)
	}
	for _, d := range dtos {
		wantPath := d.Name
		if d.Type == "directory" {
			wantPath = d.Name + "/"
		}
		if d.Path != wantPath {
			t.Fatalf("entry path = %q, want %q: %+v", d.Path, wantPath, d)
		}
		if !filepath.IsAbs(d.Absolute) || filepath.Base(d.Absolute) != d.Name {
			t.Fatalf("entry absolute = %q, want an absolute path ending in %q: %+v", d.Absolute, d.Name, d)
		}
		if d.Ignored {
			t.Fatalf("entry %q ignored = true, want false", d.Name)
		}
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/file?path=gamma.txt", nil)
	srv.serveListFiles(rec2, req2, nil)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("a regular file: expected 400, got %d (%s)", rec2.Code, rec2.Body)
	}
}

// TestListFilesAnswers400ForAForeignDirectory verifies directoryParam applies to
// /file, matching its four siblings. The absent and correct directories must
// still list entries, so a handler that answered 400 unconditionally cannot pass.
func TestListFilesAnswers400ForAForeignDirectory(t *testing.T) {
	root := t.TempDir()
	services, err := coding.NewServices(coding.ServicesOptions{
		CWD:            root,
		AgentDir:       t.TempDir(),
		ProjectTrusted: func() *bool { b := true; return &b }(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "alpha.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &Server{services: services}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/file?directory=/somewhere/else", nil)
	srv.listFiles(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a foreign directory: expected 400, got %d (%s)", rec.Code, rec.Body)
	}

	for _, target := range []string{"/file", "/file?directory=" + url.QueryEscape(root)} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		srv.listFiles(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d (%s)", target, rec.Code, rec.Body)
		}
		var dtos []fileNodeDTO
		if err := json.Unmarshal(rec.Body.Bytes(), &dtos); err != nil {
			t.Fatalf("%s: decode listing: %v (%s)", target, err, rec.Body)
		}
		if len(dtos) != 1 || dtos[0].Name != "alpha.txt" {
			t.Fatalf("%s: expected the one entry, got %+v", target, dtos)
		}
	}
}

// TestQuestionStoreTakesOneEntryUnderConcurrency covers the invariant the
// answer-once window rests on: the lookup and the delete are one critical
// section, so of many callers racing for one id exactly one gets the entry. Over
// HTTP two replies are too far apart to overlap the window reliably, which is
// why this drives the store itself.
//
// The window between the lookup and the delete is a few instructions wide, so a
// single round of callers misses it most of the time and a green round proves
// little. Many entries with all their callers released by one close keep the
// whole shape in the millisecond range while making a missed window the
// exception. Two oracles, because they fail for different reasons: a winner
// count above the number of entries catches a second caller that landed in the
// window, and the unsynchronized map access a delete outside the lock causes is
// refused outright by the race detector and by the runtime's concurrent-map
// guard.
func TestQuestionStoreTakesOneEntryUnderConcurrency(t *testing.T) {
	var store questionStore
	store.seed()

	const entries, callers = 64, 16
	ids := make([]string, 0, entries)
	start := make(chan struct{})
	var won atomic.Int64
	var wg sync.WaitGroup
	for range entries {
		id, _ := store.add("ses_1", askuser.Question{Question: "which?"})
		ids = append(ids, id)
		for range callers {
			wg.Go(func() {
				<-start
				if _, ok := store.take(id); ok {
					won.Add(1)
				}
			})
		}
	}
	close(start)
	wg.Wait()

	if got := won.Load(); got != entries {
		t.Errorf("%d takes succeeded over %d entries and %d callers, want exactly %d", got, entries, entries*callers, entries)
	}
	if got := len(store.list()); got != 0 {
		t.Errorf("the store still holds %d entries after every winner took one", got)
	}
	if _, ok := store.take(ids[0]); ok {
		t.Error("a second take after the entry was removed returned it")
	}
}

// ─── DTO shape parity with opencode ────────────────────────────────────────

// objKeys decodes a JSON object body into its keys mapped to raw values, so a
// test can assert presence and JSON kind without a typed mirror going stale.
func objKeys(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("body is not a JSON object: %v (%s)", err, body)
	}
	return m
}

// jsonKind names a raw value's JSON kind: object, array, string, number, bool,
// null, or missing for an absent value.
func jsonKind(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "missing"
	}
	switch trimmed[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "bool"
	case 'n':
		return "null"
	default:
		return "number"
	}
}

// assertKind fails unless m[key] is present with JSON kind want, and returns
// the raw value for further inspection.
func assertKind(t *testing.T, m map[string]json.RawMessage, key, want string) json.RawMessage {
	t.Helper()
	raw, ok := m[key]
	if !ok {
		t.Errorf("missing key %q", key)
		return nil
	}
	if got := jsonKind(raw); got != want {
		t.Errorf("key %q: kind %s, want %s (%s)", key, got, want, raw)
	}
	return raw
}

// assertObject asserts m[key] is a JSON object and returns its keys.
func assertObject(t *testing.T, m map[string]json.RawMessage, key string) map[string]json.RawMessage {
	t.Helper()
	raw := assertKind(t, m, key, "object")
	if raw == nil {
		return nil
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: object decode: %v (%s)", key, err, raw)
	}
	return out
}

// assertArray asserts m[key] is a JSON array and returns its raw items.
func assertArray(t *testing.T, m map[string]json.RawMessage, key string) []json.RawMessage {
	t.Helper()
	raw := assertKind(t, m, key, "array")
	if raw == nil {
		return nil
	}
	var out []json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: array decode: %v (%s)", key, err, raw)
	}
	return out
}

// assertStringEqual asserts m[key] is a JSON string equal to want.
func assertStringEqual(t *testing.T, m map[string]json.RawMessage, key, want string) {
	t.Helper()
	raw := assertKind(t, m, key, "string")
	if raw == nil {
		return
	}
	var got string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%s: string decode: %v", key, err)
	}
	if got != want {
		t.Errorf("%s = %q, want %q", key, got, want)
	}
}

// assertSessionShape asserts every key and JSON kind of sidecar section 3.
func assertSessionShape(t *testing.T, body []byte) {
	t.Helper()
	s := objKeys(t, body)
	for _, key := range []string{"id", "slug", "projectID", "directory", "path", "title", "agent", "version"} {
		assertKind(t, s, key, "string")
	}
	model := assertObject(t, s, "model")
	for _, key := range []string{"id", "providerID", "variant"} {
		assertKind(t, model, key, "string")
	}
	summary := assertObject(t, s, "summary")
	for _, key := range []string{"additions", "deletions", "files"} {
		assertKind(t, summary, key, "number")
	}
	assertKind(t, s, "cost", "number")
	tokens := assertObject(t, s, "tokens")
	for _, key := range []string{"input", "output", "reasoning", "total"} {
		assertKind(t, tokens, key, "number")
	}
	cache := assertObject(t, tokens, "cache")
	assertKind(t, cache, "read", "number")
	assertKind(t, cache, "write", "number")
	tm := assertObject(t, s, "time")
	assertKind(t, tm, "created", "number")
	assertKind(t, tm, "updated", "number")
	project := assertObject(t, s, "project")
	assertKind(t, project, "id", "string")
	assertKind(t, project, "worktree", "string")
}

// assertInfoShape asserts every key and JSON kind of message info (sidecar
// section 5) on an already-decoded info object. The shape branches on role: a
// user message carries opencode's nested model/summary shape, every other role
// the flat shape.
func assertInfoShape(t *testing.T, info map[string]json.RawMessage) {
	t.Helper()
	var role string
	if raw, ok := info["role"]; ok {
		_ = json.Unmarshal(raw, &role)
	}
	if role == "user" {
		assertUserInfoShape(t, info, true)
		return
	}
	for _, key := range []string{"id", "sessionID", "role", "parentID", "mode", "agent", "modelID", "providerID", "finish"} {
		assertKind(t, info, key, "string")
	}
	path := assertObject(t, info, "path")
	assertKind(t, path, "cwd", "string")
	assertKind(t, path, "root", "string")
	assertKind(t, info, "cost", "number")
	tokens := assertObject(t, info, "tokens")
	for _, key := range []string{"total", "input", "output", "reasoning"} {
		assertKind(t, tokens, key, "number")
	}
	cache := assertObject(t, tokens, "cache")
	assertKind(t, cache, "read", "number")
	assertKind(t, cache, "write", "number")
	tm := assertObject(t, info, "time")
	assertKind(t, tm, "created", "number")
}

// assertUserInfoShape asserts the nested user-message info shape: exactly the
// keys id, sessionID, role, time, summary, agent, model; a created time; a
// summary whose diffs is an array (never null); and a model object. The exact
// key count is what a flat user object fails on. wantSummary selects the two
// stream phases: true is the read-path/settle shape with summary, false is the
// SSE start frame whose summary key is absent. Shared by assertInfoShape's
// role branch, TestMessageInfoRoleShapes, and the SSE sites so the user wire
// shape has one definition.
func assertUserInfoShape(t *testing.T, info map[string]json.RawMessage, wantSummary bool) {
	t.Helper()
	want := []string{"id", "sessionID", "role", "time", "summary", "agent", "model"}
	if !wantSummary {
		want = []string{"id", "sessionID", "role", "time", "agent", "model"}
	}
	if len(info) != len(want) {
		keys := make([]string, 0, len(info))
		for k := range info {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		t.Fatalf("user info keys = %v, want %v", keys, want)
	}
	for _, key := range []string{"id", "sessionID", "role", "agent"} {
		assertKind(t, info, key, "string")
	}
	tm := assertObject(t, info, "time")
	assertKind(t, tm, "created", "number")
	if wantSummary {
		summary := assertObject(t, info, "summary")
		if diffs := assertArray(t, summary, "diffs"); len(diffs) != 0 {
			t.Errorf("user info summary.diffs = %v, want []", diffs)
		}
	}
	model := assertObject(t, info, "model")
	assertKind(t, model, "providerID", "string")
	assertKind(t, model, "modelID", "string")
}

// userMessageUpdatedInfos decodes every message.updated frame's info object,
// keeps the user-role ones in frame order, and returns them as raw key maps so
// summary presence is observable (sseMessageUpdated carries no summary field).
func userMessageUpdatedInfos(t *testing.T, frames []sseFrame) []map[string]json.RawMessage {
	t.Helper()
	var infos []map[string]json.RawMessage
	for _, f := range frames {
		if f.Name != eventMessageUpdated {
			continue
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
			continue
		}
		raw, ok := payload["info"]
		if !ok {
			continue
		}
		var info map[string]json.RawMessage
		if err := json.Unmarshal(raw, &info); err != nil {
			continue
		}
		var role string
		_ = json.Unmarshal(info["role"], &role)
		if role != "user" {
			continue
		}
		infos = append(infos, info)
	}
	return infos
}

// assertUserSummaryOmitted asserts a user info object carries no summary key.
func assertUserSummaryOmitted(t *testing.T, info map[string]json.RawMessage) {
	t.Helper()
	if raw, ok := info["summary"]; ok {
		t.Errorf("user info must not carry a summary key, got %s", raw)
	}
}

// userMessageCreated decodes a user info object's time.created (ms).
func userMessageCreated(t *testing.T, info map[string]json.RawMessage) int64 {
	t.Helper()
	raw, ok := info["time"]
	if !ok {
		t.Fatalf("user info must carry a time key")
	}
	var ts struct {
		Created int64 `json:"created"`
	}
	if err := json.Unmarshal(raw, &ts); err != nil {
		t.Fatalf("user info time is not an object: %v (%s)", err, raw)
	}
	return ts.Created
}

// assertUserSummaryEmpty asserts a user info object carries summary as an
// object whose diffs is an empty, non-null array.
func assertUserSummaryEmpty(t *testing.T, info map[string]json.RawMessage) {
	t.Helper()
	raw, ok := info["summary"]
	if !ok {
		t.Fatalf("user info must carry a summary key")
	}
	if string(raw) == "null" {
		t.Fatalf("user info summary is null, want an object")
	}
	var summary map[string]json.RawMessage
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("user info summary is not an object: %v (%s)", err, raw)
	}
	diffsRaw, ok := summary["diffs"]
	if !ok {
		t.Fatalf("user info summary.diffs key missing")
	}
	if string(diffsRaw) == "null" {
		t.Fatalf("user info summary.diffs is null, want []")
	}
	var diffs []json.RawMessage
	if err := json.Unmarshal(diffsRaw, &diffs); err != nil {
		t.Fatalf("user info summary.diffs is not an array: %v (%s)", err, diffsRaw)
	}
	if len(diffs) != 0 {
		t.Errorf("user info summary.diffs = %v, want []", diffs)
	}
}

// assertMessageShape asserts one {info, parts} message and every part's keys.
func assertMessageShape(t *testing.T, body []byte) int {
	t.Helper()
	m := objKeys(t, body)
	assertInfoShape(t, assertObject(t, m, "info"))
	parts := assertArray(t, m, "parts")
	for i, raw := range parts {
		var p map[string]json.RawMessage
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("parts[%d] is not an object: %v (%s)", i, err, raw)
		}
		for _, key := range []string{"id", "sessionID", "messageID", "type", "snapshot"} {
			assertKind(t, p, key, "string")
		}
	}
	return len(parts)
}

// TestConfigShapeMatchesOpencode asserts GET /config carries the captured 12-key
// object with the right JSON kinds, and the server's model spec verbatim.
func TestConfigShapeMatchesOpencode(t *testing.T) {
	env := promptEnvFor(t)
	status, body := getRaw(t, env.baseURL, "/config")
	if status != http.StatusOK {
		t.Fatalf("GET /config: expected 200, got %d (%s)", status, body)
	}
	cfg := objKeys(t, body)
	assertKind(t, cfg, "$schema", "string")
	assertKind(t, cfg, "command", "object")
	assertKind(t, cfg, "plugin", "array")
	assertStringEqual(t, cfg, "model", "test-faux/faux-1")
	assertKind(t, cfg, "subagent_depth", "number")
	assertKind(t, cfg, "username", "string")
	assertKind(t, cfg, "mode", "object")
	assertKind(t, cfg, "agent", "object")
	assertKind(t, cfg, "provider", "object")
	assertKind(t, cfg, "mcp", "object")
	assertKind(t, cfg, "instructions", "array")
	assertKind(t, cfg, "permission", "object")
}

// TestConfigProvidersShapeMatchesOpencode asserts the array-of-providers shape,
// the full nested model key set, and that key is always empty.
func TestConfigProvidersShapeMatchesOpencode(t *testing.T) {
	env := promptEnvFor(t)
	createPromptSession(t, env.baseURL)

	status, body := getRaw(t, env.baseURL, "/config/providers")
	if status != http.StatusOK {
		t.Fatalf("GET /config/providers: expected 200, got %d (%s)", status, body)
	}
	top := objKeys(t, body)
	providers := assertArray(t, top, "providers")
	assertKind(t, top, "default", "object")
	if len(providers) == 0 {
		t.Fatal("providers is empty; the shape assertions would be vacuous")
	}
	for i, raw := range providers {
		var p map[string]json.RawMessage
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("providers[%d] is not an object: %v (%s)", i, err, raw)
		}
		assertKind(t, p, "id", "string")
		assertKind(t, p, "name", "string")
		assertKind(t, p, "source", "string")
		assertKind(t, p, "env", "array")
		assertStringEqual(t, p, "key", "")
		assertKind(t, p, "options", "object")
		models := assertObject(t, p, "models")
		if len(models) == 0 {
			t.Errorf("providers[%d].models is empty; no model shape to assert", i)
		}
		for id, mraw := range models {
			var model map[string]json.RawMessage
			if err := json.Unmarshal(mraw, &model); err != nil {
				t.Fatalf("providers[%d].models[%s] is not an object: %v (%s)", i, id, err, mraw)
			}
			for _, key := range []string{"id", "providerID", "name", "family", "status", "release_date"} {
				assertKind(t, model, key, "string")
			}
			for _, key := range []string{"api", "capabilities", "cost", "limit", "options", "headers", "variants"} {
				assertKind(t, model, key, "object")
			}
		}
	}
}

// TestAgentShapeMatchesOpencode asserts each /agent item carries the captured 13
// keys and no model key.
func TestAgentShapeMatchesOpencode(t *testing.T) {
	env := promptEnvFor(t)
	status, body := getRaw(t, env.baseURL, "/agent")
	if status != http.StatusOK {
		t.Fatalf("GET /agent: expected 200, got %d (%s)", status, body)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatalf("GET /agent body is not an array: %v (%s)", err, body)
	}
	if len(items) == 0 {
		t.Fatal("GET /agent returned no agents")
	}
	for i, raw := range items {
		var a map[string]json.RawMessage
		if err := json.Unmarshal(raw, &a); err != nil {
			t.Fatalf("agent[%d] is not an object: %v (%s)", i, err, raw)
		}
		for _, key := range []string{"name", "description", "mode"} {
			assertKind(t, a, key, "string")
		}
		assertKind(t, a, "native", "bool")
		for _, key := range []string{"hidden", "topP", "temperature", "color", "variant", "prompt", "steps"} {
			assertKind(t, a, key, "null")
		}
		assertKind(t, a, "permission", "array")
		assertKind(t, a, "options", "object")
		if _, ok := a["model"]; ok {
			t.Errorf("agent[%d] carries a model key; the captured delta omits it", i)
		}
	}
}

// TestDiskSessionModelFromChangeEntry: a disk-only session reports the model its
// newest model_change names, not the server default; one without a model_change
// still reports the server default.
func TestDiskSessionModelFromChangeEntry(t *testing.T) {
	env := promptEnvFor(t)
	writeTestSession(t, env.sessionDir, "disk-model",
		[]string{modelChangeEntry("mc-1", "anthropic", "claude-x"), userEntry("u-1", "hi")})
	writeTestSession(t, env.sessionDir, "disk-nomodel",
		[]string{userEntry("u-2", "hi")})
	base := restartServer(t, env)

	status, body := getRaw(t, base, "/session/disk-model")
	if status != http.StatusOK {
		t.Fatalf("GET /session/disk-model: expected 200, got %d (%s)", status, body)
	}
	var got sessionDTO
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode disk-model: %v", err)
	}
	if got.Model != (sessionModel{ID: "claude-x", ProviderID: "anthropic", Variant: "default"}) {
		t.Fatalf("disk-model model = %+v, want {claude-x anthropic default}", got.Model)
	}

	status, body = getRaw(t, base, "/session/disk-nomodel")
	if status != http.StatusOK {
		t.Fatalf("GET /session/disk-nomodel: expected 200, got %d (%s)", status, body)
	}
	var fallback sessionDTO
	if err := json.Unmarshal(body, &fallback); err != nil {
		t.Fatalf("decode disk-nomodel: %v", err)
	}
	if fallback.Model != (sessionModel{ID: "faux-1", ProviderID: "test-faux", Variant: "default"}) {
		t.Fatalf("disk-nomodel model = %+v, want {faux-1 test-faux default}", fallback.Model)
	}
}

// TestSessionShapeMatchesOpencode drives every route that reaches
// buildSessionDTO plus a disk-only session (restart), so both model sources are
// covered.
func TestSessionShapeMatchesOpencode(t *testing.T) {
	env := promptEnvFor(t)

	// POST /session: a live session, model from the live Model().
	status, createBody := postJSON(t, env.baseURL, "/session", "{}")
	if status != http.StatusOK {
		t.Fatalf("POST /session: expected 200, got %d (%s)", status, createBody)
	}
	assertSessionShape(t, createBody)
	var created sessionDTO
	if err := json.Unmarshal(createBody, &created); err != nil {
		t.Fatalf("decode POST /session: %v", err)
	}
	id := created.ID
	if id == "" {
		t.Fatal("POST /session returned an empty id")
	}

	// GET /session/{id}
	status, body := getRaw(t, env.baseURL, "/session/"+id)
	if status != http.StatusOK {
		t.Fatalf("GET /session/{id}: expected 200, got %d (%s)", status, body)
	}
	assertSessionShape(t, body)

	// PATCH /session/{id} returns the same expanded DTO.
	status, body = sessionRequest(t, "PATCH", env.baseURL, id, `{"title":"shape parity"}`)
	if status != http.StatusOK {
		t.Fatalf("PATCH /session/{id}: expected 200, got %d (%s)", status, body)
	}
	assertSessionShape(t, body)

	// GET /session and /experimental/session: the list elements share the shape.
	for _, path := range []string{"/session", "/experimental/session"} {
		status, body = getRaw(t, env.baseURL, path)
		if status != http.StatusOK {
			t.Fatalf("GET %s: expected 200, got %d (%s)", path, status, body)
		}
		var list []json.RawMessage
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatalf("GET %s: body is not an array: %v (%s)", path, err, body)
		}
		if len(list) == 0 {
			t.Fatalf("GET %s: no sessions to assert", path)
		}
		for _, raw := range list {
			assertSessionShape(t, raw)
		}
	}

	// Disk-only session: no live host owns it, so the model comes from the
	// parsed server spec. The restart reproduces post-restart state.
	diskID := "disk-only-shape"
	writeTestSession(t, env.sessionDir, diskID, []string{userEntry("disk-entry-1", "hi")})
	diskBase := restartServer(t, env)
	status, body = getRaw(t, diskBase, "/session/"+diskID)
	if status != http.StatusOK {
		t.Fatalf("GET /session/{disk}: expected 200, got %d (%s)", status, body)
	}
	assertSessionShape(t, body)
}

// TestMessageInfoShapeMatchesOpencode drives a prompt and asserts both the POST
// and GET message bodies carry the captured info shape and the snapshot part key.
func TestMessageInfoShapeMatchesOpencode(t *testing.T) {
	env := promptEnvFor(t)
	id := createPromptSession(t, env.baseURL)

	status, body := postJSON(t, env.baseURL, "/session/"+id+"/message",
		`{"parts":[{"type":"text","text":"reply with exactly: shape-ok"}]}`)
	if status != http.StatusOK {
		t.Fatalf("POST /session/{id}/message: expected 200, got %d (%s)", status, body)
	}
	parts := assertMessageShape(t, body)

	status, body = getRaw(t, env.baseURL, "/session/"+id+"/message")
	if status != http.StatusOK {
		t.Fatalf("GET /session/{id}/message: expected 200, got %d (%s)", status, body)
	}
	var list []json.RawMessage
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("GET /session/{id}/message: body is not an array: %v (%s)", err, body)
	}
	if len(list) == 0 {
		t.Fatal("GET /session/{id}/message returned no messages")
	}
	for _, raw := range list {
		parts += assertMessageShape(t, raw)
	}
	if parts == 0 {
		t.Fatal("no message parts across POST and GET; the snapshot assertion was vacuous")
	}
}

// TestSSEMessageUpdatedShapeMatchesOpencode proves the streamed message.updated
// info tracks the polled shape.
func TestSSEMessageUpdatedShapeMatchesOpencode(t *testing.T) {
	env := promptEnvFor(t)
	id := createPromptSession(t, env.baseURL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := openSSE(t, env.baseURL, ctx)
	defer func() { _ = resp.Body.Close() }()

	promptAsyncOK(t, env.baseURL, id, "reply with exactly: sse-shape-ok")
	frames := readSSEUntil(t, resp, func(frames []sseFrame) bool {
		return hasFrame(frames, eventMessageUpdated)
	})
	cancel()

	found := false
	for _, f := range frames {
		if f.Name != eventMessageUpdated {
			continue
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
			continue
		}
		info := assertObject(t, payload, "info")
		var role string
		if raw, ok := info["role"]; ok {
			_ = json.Unmarshal(raw, &role)
		}
		if role == "user" {
			// The first message.updated of a turn is the start frame, which
			// carries no summary.
			assertUserInfoShape(t, info, false)
		} else {
			assertInfoShape(t, info)
		}
		found = true
	}
	if !found {
		t.Fatalf("no message.updated frame carried an info object; frames: %s", summarize(frames))
	}
}

// TestMessageInfoRoleShapes pins the one place the wire shape branches on role:
// a user message carries a nested model and summary; an assistant message keeps
// the flat shape. Both builder callers are covered because the client crashed on
// the GET read path and the SSE path alike: entriesToMessages for the read path,
// eventMapper.messageEnd for the SSE user message.updated. Assertions go through
// assertInfoShape, which decodes the marshaled top-level keys, so a flat user
// object fails even though the Go struct still has the fields.
func TestMessageInfoRoleShapes(t *testing.T) {
	assertModel := func(t *testing.T, info map[string]json.RawMessage) {
		t.Helper()
		var model struct {
			ProviderID string `json:"providerID"`
			ModelID    string `json:"modelID"`
		}
		if err := json.Unmarshal(info["model"], &model); err != nil {
			t.Fatalf("decode user model: %v", err)
		}
		if model.ProviderID != "prov" || model.ModelID != "model" {
			t.Errorf("user info model = %+v, want prov/model", model)
		}
	}
	infoOf := func(t *testing.T, msg any) map[string]json.RawMessage {
		t.Helper()
		top := objKeys(t, mustMarshal(t, msg))
		info, ok := top["info"]
		if !ok {
			t.Fatalf("message has no info key: %s", mustMarshal(t, msg))
		}
		return objKeys(t, info)
	}

	// Read path: GET /session/{id}/message projects entriesToMessages.
	t.Run("read", func(t *testing.T) {
		dir := t.TempDir()
		ts := time.Now().UTC().Format(time.RFC3339Nano)
		path := writeTestSession(t, dir, "shape", []string{
			userEntry("u1", "hi"),
			`{"type":"message","id":"a1","parentId":"u1","timestamp":"` + ts + `","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"api":"faux","provider":"test-faux","model":"faux-1","usage":{"input":1,"output":1,"cache":{"read":0,"write":0}},"stopReason":"stop","timestamp":1704067201000}}`,
		})
		entries, err := tailEntries(path, maxLimit)
		if err != nil {
			t.Fatal(err)
		}
		byID := map[string]messageDTO{}
		for _, m := range entriesToMessages(entries, "shape", "/test", "/test", "prov", "model") {
			byID[m.Info.ID] = m
		}
		if _, ok := byID["u1"]; !ok {
			t.Fatalf("user u1 not in transcript: %+v", byID)
		}
		userInfo := infoOf(t, byID["u1"])
		assertInfoShape(t, userInfo) // branches to the nested user shape
		assertModel(t, userInfo)
		if _, ok := byID["a1"]; !ok {
			t.Fatalf("assistant a1 not in transcript: %+v", byID)
		}
		assertInfoShape(t, infoOf(t, byID["a1"])) // flat assistant shape
	})

	// SSE path: the user message.updated the client crashed on. messageEnd emits
	// the message.updated AND a message.part.updated for each text block
	// (serve_sse.go:493-503), so locate the updated frame rather than assuming it
	// is the only event.
	t.Run("sse_user", func(t *testing.T) {
		m := &eventMapper{sessionID: "shape", cwd: "/test", root: "/test", providerID: "prov", modelID: "model"}
		got, err := m.translate(agent.MessageEndEvent{Message: agent.AgentMessage{
			User: &agent.UserMessage{Role: "user", Content: []ai.UserContentBlock{ai.TextContent{Text: "hi"}}},
		}}, "u1")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) == 0 || got[0].Type != eventMessageUpdated {
			t.Fatalf("want message.updated first, got %+v", got)
		}
		var updated any
		for _, e := range got {
			if e.Type == eventMessageUpdated {
				updated = e.Data
				break
			}
		}
		if updated == nil {
			t.Fatalf("no message.updated among %d events: %+v", len(got), got)
		}
		info := infoOf(t, updated)
		assertUserInfoShape(t, info, false)
		assertModel(t, info)
	})
}

// ─── helpers ───────────────────────────────────────────────────────────────

func hasFrame(frames []sseFrame, name string) bool {
	return slices.ContainsFunc(frames, func(f sseFrame) bool { return f.Name == name })
}

func promptAsyncOK(t *testing.T, baseURL, id, text string) {
	t.Helper()
	status, body := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"parts":[{"type":"text","text":"`+text+`"}]}`)
	if status != http.StatusNoContent {
		t.Fatalf("prompt_async: expected 204, got %d (%s)", status, body)
	}
}

func readQuestions(t *testing.T, baseURL string) []questionDTO {
	t.Helper()
	resp, err := http.Get(baseURL + "/question")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /question: expected 200, got %d", resp.StatusCode)
	}
	var dtos []questionDTO
	if err := json.NewDecoder(resp.Body).Decode(&dtos); err != nil {
		t.Fatal(err)
	}
	return dtos
}

func waitForQuestion(t *testing.T, baseURL string) questionDTO {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if dtos := readQuestions(t, baseURL); len(dtos) > 0 {
			return dtos[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no question was ever pending")
	return questionDTO{}
}

func readTodos(t *testing.T, baseURL, id string) []tools.Todo {
	t.Helper()
	status, body := getRaw(t, baseURL, "/session/"+id+"/todo")
	if status != http.StatusOK {
		t.Fatalf("GET /todo: expected 200, got %d (%s)", status, body)
	}
	var todos []tools.Todo
	if err := json.Unmarshal(body, &todos); err != nil {
		t.Fatalf("GET /todo: body is not a todo list: %v (%s)", err, body)
	}
	return todos
}

func waitForTodos(t *testing.T, baseURL, id string, want int) []tools.Todo {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if todos := readTodos(t, baseURL, id); len(todos) >= want {
			return todos
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the todo list never reached %d items", want)
	return nil
}

func waitForTranscriptText(t *testing.T, baseURL, id, want string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if countTranscriptText(t, baseURL, id, want) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the transcript never contained %q", want)
}

func countTranscriptText(t *testing.T, baseURL, id, want string) int {
	t.Helper()
	n := 0
	for _, m := range readTranscript(t, baseURL, id) {
		for _, p := range m.Parts {
			if strings.Contains(p.Text, want) || strings.Contains(string(p.State), want) {
				n++
			}
		}
	}
	return n
}

// countToolResults counts finished tool parts whose state carries want. The
// answer reaches the transcript twice — on the tool part's state and again on
// the toolResult message's own text part — so a count that is about deliveries
// has to look at tool parts only.
func countToolResults(t *testing.T, baseURL, id, want string) int {
	t.Helper()
	n := 0
	for _, m := range readTranscript(t, baseURL, id) {
		for _, p := range m.Parts {
			if p.Type == "tool" && strings.Contains(string(p.State), want) {
				n++
			}
		}
	}
	return n
}

func getRaw(t *testing.T, baseURL, path string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(baseURL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

func getStatus(t *testing.T, baseURL, path string) int {
	t.Helper()
	status, _ := getRaw(t, baseURL, path)
	return status
}

// restartServer starts a second serve server over the same session files with a
// host that owns no session: the state every session is in after a restart.
func restartServer(t *testing.T, env promptEnv) string {
	t.Helper()
	srv, ln, err := New("127.0.0.1:0", codinghost.New(env.services, nil), env.sessionDir, "test-faux/faux-1")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "http://" + ln.Addr().String()
}

// stopOnSessionIdle is the readSSEUntil predicate the prompt tests share:
// session.idle is emitted only after the run and its message_end persist have
// settled, so a transcript read after it cannot race persistence.
func stopOnSessionIdle(frames []sseFrame) bool {
	for _, f := range frames {
		if f.Name == eventSessionIdle {
			return true
		}
	}
	return false
}

// firstUserMessageID returns the id of the first user message in the transcript,
// or "" when there is none.
func firstUserMessageID(msgs []messageDTO) string {
	for _, m := range msgs {
		if m.Info.Role == "user" {
			return m.Info.ID
		}
	}
	return ""
}

// lastUserMessageID returns the id of the last user message in the transcript,
// or "" when there is none.
func lastUserMessageID(msgs []messageDTO) string {
	id := ""
	for _, m := range msgs {
		if m.Info.Role == "user" {
			id = m.Info.ID
		}
	}
	return id
}

// lastUserMessage returns the last user-role message in the transcript, or nil
// when there is none.
func lastUserMessage(msgs []messageDTO) *messageDTO {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Info.Role == "user" {
			return &msgs[i]
		}
	}
	return nil
}

// TestPromptMessageIDHonoredEndToEnd pins the item's contract: a client-minted
// messageID in the prompt body becomes the user transcript entry id on the SSE
// user message.updated, on its part, and on GET, so a client that rendered the
// bubble optimistically under that id reconciles instead of double-rendering. A
// prompt with no messageID still mints a server id, and stream and GET agree on
// it.
func TestPromptMessageIDHonoredEndToEnd(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)

	ctx := t.Context()
	resp := openSSE(t, baseURL, ctx)
	defer func() { _ = resp.Body.Close() }()

	status, body := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"messageID":"msg_test1","parts":[{"type":"text","text":"reply with exactly: id-ok"}]}`)
	if status != http.StatusNoContent {
		t.Fatalf("prompt_async with messageID: expected 204, got %d (%s)", status, body)
	}
	frames := readSSEUntil(t, resp, stopOnSessionIdle)

	// (a) Exactly two user message.updated frames (opencode's two-phase pair)
	// carrying the client id; the start frame has no summary, the settle frame
	// does.
	userInfos := userMessageUpdatedInfos(t, frames)
	if len(userInfos) != 2 {
		t.Fatalf("expected exactly two user message.updated, got %d; frames: %s", len(userInfos), summarize(frames))
	}
	var streamUserID string
	_ = json.Unmarshal(userInfos[0]["id"], &streamUserID)
	if streamUserID != "msg_test1" {
		t.Errorf("stream user id: expected client id %q, got %q", "msg_test1", streamUserID)
	}
	var settleUserID string
	_ = json.Unmarshal(userInfos[1]["id"], &settleUserID)
	if settleUserID != streamUserID {
		t.Errorf("user message.updated ids disagree: %q against %q", streamUserID, settleUserID)
	}
	assertUserSummaryOmitted(t, userInfos[0])
	assertUserSummaryEmpty(t, userInfos[1])

	// (b) The user text part derives from the client id.
	partSeen := false
	for _, f := range frames {
		if f.Name != eventMessagePartUpdated {
			continue
		}
		var payload ssePartUpdated
		if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
			continue
		}
		if payload.Part.MessageID != "msg_test1" {
			continue
		}
		partSeen = true
		if want := "msg_test1-text-0"; payload.Part.ID != want {
			t.Errorf("stream user part id: expected %q, got %q", want, payload.Part.ID)
		}
	}
	if !partSeen {
		t.Fatalf("no user message.part.updated under %q; frames: %s", "msg_test1", summarize(frames))
	}

	// (c) GET reports the same id.
	if got := firstUserMessageID(readTranscript(t, baseURL, id)); got != "msg_test1" {
		t.Errorf("GET user id: expected client id %q, got %q", "msg_test1", got)
	}

	// A second prompt with no messageID mints a server id; stream == GET.
	status, body = postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"parts":[{"type":"text","text":"reply with exactly: id-minted"}]}`)
	if status != http.StatusNoContent {
		t.Fatalf("prompt_async without messageID: expected 204, got %d (%s)", status, body)
	}
	frames = readSSEUntil(t, resp, stopOnSessionIdle)

	mintedStream := ""
	for _, f := range frames {
		if f.Name != eventMessageUpdated {
			continue
		}
		var payload sseMessageUpdated
		if err := json.Unmarshal([]byte(f.Data), &payload); err != nil {
			continue
		}
		if payload.Info.Role == "user" {
			mintedStream = payload.Info.ID
		}
	}
	if mintedStream == "" {
		t.Fatalf("minted user message.updated carries no id; frames: %s", summarize(frames))
	}
	if got := lastUserMessageID(readTranscript(t, baseURL, id)); got != mintedStream {
		t.Errorf("minted id disagrees: stream %q, GET %q", mintedStream, got)
	}
}

// TestPromptMessageIDHonoredOnMessage pins that POST /session/{id}/message
// honors a valid client id through the same shared helper as prompt_async.
func TestPromptMessageIDHonoredOnMessage(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)

	status, body := postJSON(t, baseURL, "/session/"+id+"/message",
		`{"messageID":"msg_sync1","parts":[{"type":"text","text":"reply with exactly: sync-ok"}]}`)
	if status != http.StatusOK {
		t.Fatalf("POST /message with messageID: expected 200, got %d (%s)", status, body)
	}
	if got := firstUserMessageID(readTranscript(t, baseURL, id)); got != "msg_sync1" {
		t.Errorf("GET user id: expected client id %q, got %q", "msg_sync1", got)
	}
}

// TestPromptSystemAppliesToTheTurn pins serve-prompt-system: a prompt body's
// system text is appended (never substituted) to that turn's provider system
// prompt, persisted on the user message, echoed by GET and both SSE user frames,
// and absent from a later turn that carries none. Both prompt endpoints have
// their own before closure, so each gets its own step.
func TestPromptSystemAppliesToTheTurn(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)

	const (
		poetText = "reply with exactly: poet-ok"
		poetSys  = "You are a poet"
	)
	withSystem := `{"parts":[{"type":"text","text":"` + poetText + `"}],"system":"` + poetSys + `"}`
	withoutSystem := `{"parts":[{"type":"text","text":"` + poetText + `"}]}`

	// Open the stream before the turn so both user frames are observable.
	ctx := t.Context()
	resp := openSSE(t, baseURL, ctx)
	defer func() { _ = resp.Body.Close() }()

	// Turn 1 — POST /session/{id}/message with system.
	status, payload := postJSON(t, baseURL, "/session/"+id+"/message", withSystem)
	if status != http.StatusOK {
		t.Fatalf("POST /message with system: expected 200, got %d (%s)", status, payload)
	}
	frames := readSSEUntil(t, resp, stopOnSessionIdle)

	// (a) GET echoes the system text on the turn's user message. This is already
	// the persisted-JSON round-trip: entriesToMessages decodes the persisted
	// entry raw through the AgentMessage wire shape.
	user := lastUserMessage(readTranscript(t, baseURL, id))
	if user == nil {
		t.Fatal("no user message reached the transcript")
	}
	if user.Info.System != poetSys {
		t.Errorf("GET user system: expected %q, got %q", poetSys, user.Info.System)
	}

	// (b) the provider request carried the base prompt plus the appended text,
	// not the text alone.
	captured := ai.LastTestFauxSystemPrompt()
	if !strings.HasSuffix(captured, poetSys) {
		t.Errorf("captured system prompt must end with %q, got %q", poetSys, captured)
	}
	if len(captured) <= len(poetSys) {
		t.Errorf("captured system prompt must include the base prompt plus the appended text, got %q", captured)
	}

	// (c) both SSE user frames — the message_end start frame and the settled
	// frame, under one id — carry the system text.
	infos := userMessageUpdatedInfos(t, frames)
	if len(infos) != 2 {
		t.Fatalf("expected exactly two user message.updated, got %d; frames: %s", len(infos), summarize(frames))
	}
	var frameID string
	for i, info := range infos {
		var got string
		if err := json.Unmarshal(info["system"], &got); err != nil {
			t.Fatalf("user frame %d has no decodable system key: %v (%v)", i, err, info)
		}
		if got != poetSys {
			t.Errorf("user frame %d system: expected %q, got %q", i, poetSys, got)
		}
		var frameSystemID string
		_ = json.Unmarshal(info["id"], &frameSystemID)
		if i == 0 {
			frameID = frameSystemID
		} else if frameSystemID != frameID {
			t.Errorf("user message.updated ids disagree: %q against %q", frameID, frameSystemID)
		}
	}

	// Turn 2 — no system. The previous override must not bleed: the new user
	// entry carries no key and the provider request omits the text.
	status, payload = postJSON(t, baseURL, "/session/"+id+"/message", withoutSystem)
	if status != http.StatusOK {
		t.Fatalf("second POST /message: expected 200, got %d (%s)", status, payload)
	}
	if got := lastUserMessage(readTranscript(t, baseURL, id)); got == nil || got.Info.System != "" {
		t.Errorf("second turn user system: expected empty, got %+v", got)
	}
	if captured := ai.LastTestFauxSystemPrompt(); strings.Contains(captured, poetSys) {
		t.Errorf("second turn carried a leaked system text: %q", captured)
	}
	// Drain turn 2's frames so the next read waits on turn 3, not a buffered
	// turn-2 idle.
	_ = readSSEUntil(t, resp, stopOnSessionIdle)

	// Turn 3 — prompt_async has its own before closure, so it gets its own step.
	status, payload = postJSON(t, baseURL, "/session/"+id+"/prompt_async", withSystem)
	if status != http.StatusNoContent {
		t.Fatalf("prompt_async with system: expected 204, got %d (%s)", status, payload)
	}
	_ = readSSEUntil(t, resp, stopOnSessionIdle)
	if got := lastUserMessage(readTranscript(t, baseURL, id)); got == nil || got.Info.System != poetSys {
		t.Errorf("prompt_async user system: expected %q, got %+v", poetSys, got)
	}
	if captured := ai.LastTestFauxSystemPrompt(); !strings.HasSuffix(captured, poetSys) {
		t.Errorf("prompt_async captured system prompt must end with %q, got %q", poetSys, captured)
	}
}

// TestPromptMessageIDRejectsMalformedAndDuplicate pins the two 400s: an id not
// starting with "msg" is a schema rejection on both prompt endpoints, and an id
// already in the append-only transcript is refused rather than duplicated.
func TestPromptMessageIDRejectsMalformedAndDuplicate(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)

	for _, path := range []string{"prompt_async", "message"} {
		status, body := postJSON(t, baseURL, "/session/"+id+"/"+path,
			`{"messageID":"bogus","parts":[{"type":"text","text":"hi"}]}`)
		if status != http.StatusBadRequest {
			t.Errorf("%s malformed messageID: expected 400, got %d (%s)", path, status, body)
			continue
		}
		if got, want := strings.TrimSpace(string(body)), `{"error":"invalid messageID"}`; got != want {
			t.Errorf("%s malformed messageID body: expected %s, got %s", path, want, got)
		}
	}

	// The first msg_dup is honored once its user message persists.
	status, body := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"messageID":"msg_dup","parts":[{"type":"text","text":"reply with exactly: dup-ok"}]}`)
	if status != http.StatusNoContent {
		t.Fatalf("first msg_dup: expected 204, got %d (%s)", status, body)
	}
	deadline := time.Now().Add(20 * time.Second)
	persisted := false
	for time.Now().Before(deadline) && !persisted {
		for _, m := range readTranscript(t, baseURL, id) {
			if m.Info.Role == "user" && m.Info.ID == "msg_dup" {
				persisted = true
			}
		}
		if !persisted {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !persisted {
		t.Fatal("the first msg_dup never reached the transcript")
	}
	waitForIdle(t, baseURL, id)

	status, body = postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"messageID":"msg_dup","parts":[{"type":"text","text":"again"}]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("duplicate messageID: expected 400, got %d (%s)", status, body)
	}
	if got, want := strings.TrimSpace(string(body)), `{"error":"messageID already exists"}`; got != want {
		t.Errorf("duplicate messageID body: expected %s, got %s", want, got)
	}
}

// TestQueuedPromptMessageIDPreserved pins that a prompt accepted while the
// session is busy keeps its id when it later runs: the id rides the queued
// entry through releaseSlot, not just the immediately-started prompt.
func TestQueuedPromptMessageIDPreserved(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)

	first, body := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"parts":[{"type":"text","text":"TUI_LIVE_STREAM_PAUSE"}]}`)
	if first != http.StatusNoContent {
		t.Fatalf("first prompt_async: expected 204, got %d (%s)", first, body)
	}
	waitForBusy(t, baseURL, id)

	queued, body := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"messageID":"msg_queued","parts":[{"type":"text","text":"reply with exactly: queued-ok"}]}`)
	if queued != http.StatusNoContent {
		t.Fatalf("queued prompt_async: expected 204, got %d (%s)", queued, body)
	}
	t.Cleanup(func() { _, _ = postJSON(t, baseURL, "/session/"+id+"/abort", `{}`) })
	waitForIdle(t, baseURL, id)

	// waitForIdle can observe the gap between the first run settling and the
	// queued one starting, so poll the transcript for the persisted id.
	deadline := time.Now().Add(30 * time.Second)
	found := false
	for time.Now().Before(deadline) && !found {
		for _, m := range readTranscript(t, baseURL, id) {
			if m.Info.Role == "user" && m.Info.ID == "msg_queued" {
				found = true
			}
		}
		if !found {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !found {
		t.Fatal("the queued prompt's id was not preserved into the transcript")
	}
}

// TestPromptMessageIDDuplicateRejectedWhileBusy pins that two requests carrying
// the same client messageID cannot both be accepted while the first is still in
// flight: the id is claimed at accept, before its user message persists, so the
// second is refused atomically with 400 rather than queued into a second
// transcript entry under the same id.
func TestPromptMessageIDDuplicateRejectedWhileBusy(t *testing.T) {
	baseURL, _ := promptServer(t)
	id := createPromptSession(t, baseURL)

	// A occupies the run slot; waitForBusy returns while it still holds it.
	first, body := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"parts":[{"type":"text","text":"TUI_LIVE_STREAM_PAUSE"}]}`)
	if first != http.StatusNoContent {
		t.Fatalf("first prompt_async: expected 204, got %d (%s)", first, body)
	}
	waitForBusy(t, baseURL, id)

	// B is accepted and queued under msg_race; its user message has not persisted
	// yet, so the id is only visible via host's in-flight reservation.
	queued, body := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"messageID":"msg_race","parts":[{"type":"text","text":"reply with exactly: queued-race"}]}`)
	if queued != http.StatusNoContent {
		t.Fatalf("queued prompt_async: expected 204, got %d (%s)", queued, body)
	}
	// C carries the same id before B persists; it must be refused.
	duplicate, body := postJSON(t, baseURL, "/session/"+id+"/prompt_async",
		`{"messageID":"msg_race","parts":[{"type":"text","text":"reply with exactly: duplicate-race"}]}`)
	if duplicate != http.StatusBadRequest {
		t.Fatalf("duplicate prompt_async: expected 400, got %d (%s)", duplicate, body)
	}
	if got, want := strings.TrimSpace(string(body)), `{"error":"messageID already exists"}`; got != want {
		t.Errorf("duplicate prompt_async body: expected %s, got %s", want, got)
	}
	t.Cleanup(func() { _, _ = postJSON(t, baseURL, "/session/"+id+"/abort", `{}`) })

	// waitForIdle can observe the gap between the first run settling and the
	// queued one starting, so poll the transcript for the persisted id.
	deadline := time.Now().Add(30 * time.Second)
	var entries []messageDTO
	for time.Now().Before(deadline) && len(entries) == 0 {
		for _, m := range readTranscript(t, baseURL, id) {
			if m.Info.Role == "user" && m.Info.ID == "msg_race" {
				entries = append(entries, m)
			}
		}
		if len(entries) == 0 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one user entry with id %q, got %d", "msg_race", len(entries))
	}
	text := ""
	for _, p := range entries[0].Parts {
		text += p.Text
	}
	if !strings.Contains(text, "queued-race") {
		t.Errorf("persisted msg_race text: expected it to contain %q, got %q", "queued-race", text)
	}
}

// TestProjectCurrentReturnsServedRootProject guards GET /project/current: it
// answers 200 with a projectDTO object whose sandboxes slice is present (never
// null). Deleting the route's mux.HandleFunc line in serve.go leaves ServeMux
// with no match, it answers 404, and this test goes red.
func TestProjectCurrentReturnsServedRootProject(t *testing.T) {
	env := promptEnvFor(t)

	status, body := getRaw(t, env.baseURL, "/project/current")
	if status != http.StatusOK {
		t.Fatalf("GET /project/current: expected 200, got %d", status)
	}
	var dto projectDTO
	if err := json.Unmarshal(body, &dto); err != nil {
		t.Fatalf("GET /project/current: decode projectDTO: %v", err)
	}
	if dto.Sandboxes == nil {
		t.Error("GET /project/current: sandboxes is null, contract requires a non-nil slice")
	}
}

// TestProviderReturnsSortedAuthenticatedNames guards GET /provider: it answers
// 200 with a non-nil, sorted []string (empty under the hermetic test env).
// Deleting the route's mux.HandleFunc line in serve.go leaves ServeMux with no
// match, it answers 404, and this test goes red.
func TestProviderReturnsSortedAuthenticatedNames(t *testing.T) {
	env := promptEnvFor(t)

	status, body := getRaw(t, env.baseURL, "/provider")
	if status != http.StatusOK {
		t.Fatalf("GET /provider: expected 200, got %d", status)
	}
	var names []string
	if err := json.Unmarshal(body, &names); err != nil {
		t.Fatalf("GET /provider: decode []string: %v", err)
	}
	if names == nil {
		t.Fatal("GET /provider: body is null, contract requires []")
	}
	if !slices.IsSorted(names) {
		t.Errorf("GET /provider: names are not sorted: %v", names)
	}
}

// TestFindFileReturnsWalkedFileNodes guards GET /find/file: after seeding
// hello.txt in CWD it answers 200 with the walked fileNodeDTO list whose
// hello.txt entry carries the full node shape. Deleting the route's
// mux.HandleFunc line in serve.go leaves ServeMux with no match, it answers
// 404, and this test goes red.
func TestFindFileReturnsWalkedFileNodes(t *testing.T) {
	env := promptEnvFor(t)
	if err := os.WriteFile(filepath.Join(env.services.CWD(), "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	status, body := getRaw(t, env.baseURL, "/find/file")
	if status != http.StatusOK {
		t.Fatalf("GET /find/file: expected 200, got %d", status)
	}
	var nodes []fileNodeDTO
	if err := json.Unmarshal(body, &nodes); err != nil {
		t.Fatalf("GET /find/file: decode []fileNodeDTO: %v", err)
	}
	var found *fileNodeDTO
	for i := range nodes {
		if nodes[i].Name == "hello.txt" {
			found = &nodes[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("GET /find/file: no hello.txt node in %+v", nodes)
	}
	if found.Name != "hello.txt" {
		t.Errorf("hello.txt name: expected %q, got %q", "hello.txt", found.Name)
	}
	if found.Path != "hello.txt" {
		t.Errorf("hello.txt path: expected %q, got %q", "hello.txt", found.Path)
	}
	if found.Type != "file" {
		t.Errorf("hello.txt type: expected %q, got %q", "file", found.Type)
	}
	if found.Ignored {
		t.Error("hello.txt ignored: expected false")
	}
	if found.Absolute == "" {
		t.Error("hello.txt absolute: expected non-empty")
	}
}

// TestFileStatusAnswersNonNilArray guards GET /file/status: on the plain
// promptEnvFor tempdir (not a git repo) it answers 200 with the empty but
// non-null []fileStatusDTO. Deleting the route's mux.HandleFunc line in
// serve.go leaves ServeMux with no match, it answers 404, and this test goes
// red.
func TestFileStatusAnswersNonNilArray(t *testing.T) {
	env := promptEnvFor(t)

	status, body := getRaw(t, env.baseURL, "/file/status")
	if status != http.StatusOK {
		t.Fatalf("GET /file/status: expected 200, got %d", status)
	}
	var statuses []fileStatusDTO
	if err := json.Unmarshal(body, &statuses); err != nil {
		t.Fatalf("GET /file/status: decode []fileStatusDTO: %v", err)
	}
	if statuses == nil {
		t.Error("GET /file/status: body is null, contract requires []")
	}
}

// TestStubbedSessionRoutesAnswer501 guards the three not-yet-implemented
// session routes: POST /session/{id}/fork, POST /session/{id}/revert and
// GET /session/{id}/diff. Each answers 501 with errorDTO{error:"not
// implemented"}. Deleting any one of the three mux.HandleFunc lines in serve.go
// (fork, revert, diff) leaves ServeMux with no match for that route, it answers
// 404, and this test goes red.
func TestStubbedSessionRoutesAnswer501(t *testing.T) {
	env := promptEnvFor(t)
	id := createPromptSession(t, env.baseURL)

	routes := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/session/" + id + "/fork"},
		{http.MethodPost, "/session/" + id + "/revert"},
		{http.MethodGet, "/session/" + id + "/diff"},
	}
	for _, rt := range routes {
		var status int
		var body []byte
		if rt.method == http.MethodGet {
			status, body = getRaw(t, env.baseURL, rt.path)
		} else {
			status, body = postJSON(t, env.baseURL, rt.path, "{}")
		}
		// 501 is the current contract for this stub; update this line when the
		// route is implemented.
		if status != http.StatusNotImplemented {
			t.Errorf("%s %s: expected 501, got %d", rt.method, rt.path, status)
			continue
		}
		var dto errorDTO
		if err := json.Unmarshal(body, &dto); err != nil {
			t.Errorf("%s %s: decode errorDTO: %v", rt.method, rt.path, err)
			continue
		}
		if dto.Error != "not implemented" {
			t.Errorf("%s %s: expected error %q, got %q", rt.method, rt.path, "not implemented", dto.Error)
		}
	}
}
