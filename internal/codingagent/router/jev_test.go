package router

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestScanSecrets(t *testing.T) {
	hits := []string{
		"use key sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123 here",
		"AKIAIOSFODNN7EXAMPLE",
		"token ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----",
		"postgres://admin:hunter22@db.internal/app",
		`password = "correct-horse-battery"`,
		"API_KEY=0123456789abcdefXYZ",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
	}
	for _, text := range hits {
		redacted, hit := ScanSecrets(text)
		if !hit || !strings.Contains(redacted, "[REDACTED]") {
			t.Errorf("missed secret in %q -> %q", text, redacted)
		}
	}
	for _, text := range []string{
		"where does the api key get loaded? look at config.go",
		`apiKey := os.Getenv("OPENAI_API_KEY")`,
		"password string `json:\"password\"`",
		"https://github.com/alexrudloff/wopr",
		"the token budget is 400K",
	} {
		if _, hit := ScanSecrets(text); hit {
			t.Errorf("false positive on %q", text)
		}
	}
}

// fakeJev is a local decisions endpoint that records request bodies.
type fakeJev struct {
	mu     sync.Mutex
	bodies []string
	status int
	delay  time.Duration
	reply  map[string]any
	// answer, when set, replies to each request body instead of reply.
	answer func(body string) map[string]any
}

func (f *fakeJev) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.bodies = append(f.bodies, string(body))
	status, delay, reply := f.status, f.delay, f.reply
	if f.answer != nil {
		reply = f.answer(string(body))
	}
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	_ = json.NewEncoder(w).Encode(reply)
}

func (f *fakeJev) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.bodies...)
}

func jevRouter(t *testing.T, jev *fakeJev) *Router {
	t.Helper()
	r := newTestRouter(t, withFakeJev(t, testConfig(), jev), testHost())
	teachSpeeds(r)
	return r
}

// withFakeJev turns routing on in cfg with jev as its classifier.
func withFakeJev(t *testing.T, cfg Config, jev *fakeJev) Config {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(jev.handler))
	t.Cleanup(srv.Close)
	cfg.Enabled = true
	cfg.Jev = JevConfig{Endpoint: srv.URL, Model: "jev-test", APIKeyProvider: "none", TimeoutMs: 200, MaxPromptChars: 6000}
	return cfg
}

// withJev turns routing on in cfg with a classifier that is never called.
func withJev(cfg Config) Config {
	cfg.Enabled = true
	cfg.Jev = JevConfig{Endpoint: "http://127.0.0.1:1", Model: "jev-test", APIKeyProvider: "none", TimeoutMs: 200}
	return cfg
}

// promptJev answers like Jev: design and architecture prompts are plans
// that need a frontier model, other prompts are chat, and briefs are
// routine general work.
func promptJev() *fakeJev {
	return &fakeJev{answer: func(body string) map[string]any {
		if strings.Contains(body, qDifficulty) {
			return briefReply(map[string]float64{"routine": 1}, "general")
		}
		var req struct {
			State struct {
				Prompt string `json:"prompt"`
			} `json:"state"`
		}
		_ = json.Unmarshal([]byte(body), &req)
		kind, score, deep := "chat", 0.2, 0.1
		if strings.Contains(strings.ToLower(req.State.Prompt), "design") {
			kind, score, deep = "plan", 2.4, 0.9
		}
		return map[string]any{"answers": map[string]any{
			qKind:      map[string]any{"type": "choice", "choice": kind},
			qComplex:   map[string]any{"type": "score", "score": score},
			qCapable:   map[string]any{"type": "score", "score": score},
			qDeep:      map[string]any{"type": "noul", "noul": deep},
			qSensitive: map[string]any{"type": "noul", "noul": 0.0},
		}}
	}}
}

func briefReply(p map[string]float64, domain string) map[string]any {
	return map[string]any{
		"model": "typesafe/jev-test",
		"answers": map[string]any{
			qDifficulty: map[string]any{"type": "choice", "choice": "routine", "probabilities": p},
			qDomain:     map[string]any{"type": "choice", "choice": domain, "probabilities": map[string]float64{domain: 0.9}},
			qRisk:       map[string]any{"type": "noul", "noul": 0.1},
			qJudgment:   map[string]any{"type": "noul", "noul": 0.1},
		},
	}
}

func TestJevBriefProbabilitiesPickTheLevel(t *testing.T) {
	jev := &fakeJev{reply: briefReply(map[string]float64{"mechanical": 0.6, "routine": 0.3, "complex": 0.1}, "backend_api")}
	r := jevRouter(t, jev)
	d := r.DecideBrief(context.Background(), BriefInput{Type: "explore", Effort: "medium", Brief: "How are sessions persisted?", ContextTokens: 4000})
	if d == nil || d.need != 0.6 || d.Brief.Source != "jev" || d.Brief.Domain != "backend_api" {
		t.Fatalf("decision %+v brief %+v", d, d.Brief)
	}
	if d.Tier != "local" {
		t.Fatalf("a small routine brief should take the capable free model, got %s", d.Spec())
	}
	body := jev.requests()[0]
	if !strings.Contains(body, `"model":"jev-test"`) || !strings.Contains(body, "How are sessions persisted?") {
		t.Fatalf("request %s", body)
	}
	for _, model := range []string{"opus", "sonnet", "qwen", "deepseek"} {
		if strings.Contains(strings.ToLower(body), model) {
			t.Fatalf("brief questions must describe work, not models: found %q", model)
		}
	}
}

func TestSecretBriefNeverReachesJevAndStaysOwned(t *testing.T) {
	jev := &fakeJev{reply: briefReply(map[string]float64{"deep": 1}, "general")}
	r := jevRouter(t, jev)
	for _, effort := range []string{"medium", "thorough"} {
		d := r.DecideBrief(context.Background(), BriefInput{Type: "explore", Effort: effort, Brief: "check why ghp_abcdefghijklmnopqrstuvwxyz0123456789 fails", ContextTokens: 4000})
		if d == nil || !d.Brief.Secret || (d.Tier != "local" && d.Tier != "lan") {
			t.Fatalf("secret brief must stay on owned hardware, got %+v", d)
		}
		if next := r.EscalateBrief(d, BriefInput{ContextTokens: 4000}); next != nil && next.Tier != "local" && next.Tier != "lan" {
			t.Fatalf("secret brief escalated off owned hardware to %s", next.Spec())
		}
	}
	if n := len(jev.requests()); n != 0 {
		t.Fatalf("a secret brief reached Jev %d times", n)
	}
}

func TestJevFailureRetriesOnceThenOpensTheBreaker(t *testing.T) {
	jev := &fakeJev{delay: time.Second}
	r := jevRouter(t, jev)
	in := BriefInput{Type: "explore", Effort: "medium", Brief: "trace the request path", ContextTokens: 4000}
	start := time.Now()
	d := r.DecideBrief(context.Background(), in)
	if elapsed := time.Since(start); elapsed > 900*time.Millisecond {
		t.Fatalf("two 200ms attempts took %s", elapsed)
	}
	if d == nil || !d.Basic {
		t.Fatalf("a Jev failure should route the brief by the Basic rules, got %+v", d)
	}
	if n := len(jev.requests()); n != 2 {
		t.Fatalf("expected one retry (2 requests), got %d", n)
	}
	start = time.Now()
	if d := r.DecideBrief(context.Background(), in); d == nil || !d.Basic {
		t.Fatalf("open breaker: %+v", d)
	}
	if n := len(jev.requests()); n != 2 || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("the open breaker should fail fast without calling Jev (%d requests)", n)
	}
	if !strings.Contains(r.Status(), "unreachable") {
		t.Fatalf("status should show Jev unreachable:\n%s", r.Status())
	}
}

func TestJevAuthErrorIsNotRetried(t *testing.T) {
	jev := &fakeJev{status: http.StatusUnauthorized}
	r := jevRouter(t, jev)
	r.DecideBrief(context.Background(), BriefInput{Type: "explore", Effort: "medium", Brief: "x", ContextTokens: 4000})
	if n := len(jev.requests()); n != 1 || r.jev.breakerRemaining() < 4*time.Minute {
		t.Fatalf("401: %d requests, breaker %s", n, r.jev.breakerRemaining())
	}
}

func TestMainPromptSecretsAreRedactedBeforeJev(t *testing.T) {
	jev := &fakeJev{reply: map[string]any{"answers": map[string]any{qKind: map[string]any{"type": "choice", "choice": "debug"}}}}
	r := jevRouter(t, jev)
	class, _ := r.Classify(context.Background(), Input{Prompt: "why does AKIAIOSFODNN7EXAMPLE get rejected?"})
	body := jev.requests()[0]
	if strings.Contains(body, "AKIAIOSFODNN7EXAMPLE") || !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("secret reached Jev: %s", body)
	}
	if class.Sensitive != 1 {
		t.Fatalf("a scanner hit must mark the prompt sensitive, got %.2f", class.Sensitive)
	}
}

func TestJevFailurePausesRoutingWithOneNotice(t *testing.T) {
	jev := promptJev()
	r := jevRouter(t, jev)
	first := r.Decide(context.Background(), Input{Prompt: "hi", ContextTokens: 2000})
	if first == nil {
		t.Fatal("no first decision")
	}
	jev.mu.Lock()
	jev.status = http.StatusBadGateway
	jev.mu.Unlock()
	if next := r.Decide(context.Background(), Input{Prompt: "design the whole architecture", ContextTokens: 3000}); next == nil || next.Spec() != first.Spec() {
		t.Fatalf("a Jev failure should keep the warm model, got %+v", next)
	}
	if r.TakePauseNotice() == "" || r.TakePauseNotice() != "" {
		t.Fatal("Jev going unreachable should give exactly one notice")
	}
	r.Decide(context.Background(), Input{Prompt: "again", ContextTokens: 3000})
	if r.TakePauseNotice() != "" {
		t.Fatal("a pause that goes on must not notify again")
	}
}
