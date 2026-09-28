package evals

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// Scenario is one overhead measurement.
type Scenario struct {
	ID    string
	Title string
	// Protocol is the mock protocol a prompt scenario speaks; empty for
	// startup.
	Protocol string
	// Session resumes the large generated Session.
	Session bool
}

// Scenarios lists the overhead scenarios in report order.
var Scenarios = []Scenario{
	{ID: "startup", Title: "Startup (--version)"},
	{ID: "round-trip", Title: "One prompt, streamed reply (OpenAI Chat Completions)", Protocol: ProtocolChat},
	{ID: "round-trip-responses", Title: "One prompt, streamed reply (OpenAI Responses)", Protocol: ProtocolResponses},
	{ID: "round-trip-messages", Title: "One prompt, streamed reply (Anthropic Messages)", Protocol: ProtocolMessages},
	{ID: "resumed-session", Title: "One prompt after resuming a large Session", Protocol: ProtocolChat, Session: true},
}

// Host describes the machine a run was measured on.
type Host struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
	CPUs int    `json:"cpus"`
	Go   string `json:"go"`
}

// CurrentHost describes this machine.
func CurrentHost() Host {
	return Host{OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), Go: runtime.Version()}
}

// Failure records one failed overhead run.
type Failure struct {
	Code     int    `json:"code"`
	TimedOut bool   `json:"timed_out"`
	Requests int    `json:"requests"`
	Stderr   string `json:"stderr,omitempty"`
	Stdout   string `json:"stdout,omitempty"`
}

// ScenarioResult is one scenario's summary.
type ScenarioResult struct {
	Scenario     string    `json:"scenario"`
	Title        string    `json:"title"`
	Runs         int       `json:"runs"`
	WallMsMedian float64   `json:"wall_ms_median,omitempty"`
	WallMsP90    float64   `json:"wall_ms_p90,omitempty"`
	WallMsMin    float64   `json:"wall_ms_min,omitempty"`
	CPUMsMedian  float64   `json:"cpu_ms_median,omitempty"`
	RSSMiBMedian float64   `json:"rss_mib_median,omitempty"`
	Requests     int       `json:"requests,omitempty"`
	RequestBytes int       `json:"request_bytes,omitempty"`
	BytesSent    int       `json:"bytes_sent,omitempty"`
	SystemBytes  int       `json:"system_bytes,omitempty"`
	Tools        int       `json:"tools,omitempty"`
	Failures     []Failure `json:"failures,omitempty"`
}

// OverheadReport is the result of one overhead run.
type OverheadReport struct {
	Kind             string           `json:"kind"`
	Generated        string           `json:"generated"`
	Host             Host             `json:"host"`
	Version          string           `json:"version"`
	Runs             int              `json:"runs"`
	Warmup           int              `json:"warmup"`
	Prompt           string           `json:"prompt"`
	SessionExchanges int              `json:"session_exchanges"`
	Results          []ScenarioResult `json:"results"`
}

// Result returns the named scenario's result, or nil.
func (r *OverheadReport) Result(id string) *ScenarioResult {
	for i := range r.Results {
		if r.Results[i].Scenario == id {
			return &r.Results[i]
		}
	}
	return nil
}

// OverheadOptions configures Overhead.
type OverheadOptions struct {
	Binary           string
	Runs             int
	Warmup           int
	Prompt           string
	SessionExchanges int
	Timeout          time.Duration
	// Log receives one progress line per scenario; nil discards them.
	Log io.Writer
}

// credentialPrefixes are stripped from the environment so a measured run
// cannot reach a real provider.
var credentialPrefixes = []string{"OPENAI_", "ANTHROPIC_", "GEMINI_", "GOOGLE_", "AZURE_", "AWS_", "WOPR_", "OPENROUTER_", "XAI_", "GH_", "GITHUB_", "COPILOT_"}

// IsolatedEnv returns the environment for a wopr run under home: no
// credentials, WOPR_HOME inside home, and plain output.
func IsolatedEnv(home string) []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasSuffix(name, "_API_KEY") || strings.HasSuffix(name, "_TOKEN") || slices.ContainsFunc(credentialPrefixes, func(p string) bool { return strings.HasPrefix(name, p) }) {
			continue
		}
		switch name {
		case "HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "NO_COLOR", "TERM":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"), "XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
		"WOPR_HOME="+filepath.Join(home, ".wopr"), "WOPR_OFFLINE=1", "WOPR_ROUTER=off", "NO_COLOR=1", "TERM=dumb")
}

// providerFor names the mock provider that speaks protocol.
func providerFor(protocol string) string {
	return "bench-" + strings.TrimPrefix(strings.TrimPrefix(protocol, "openai-"), "anthropic-")
}

// MockModels returns a models.json with one mock provider per protocol.
func MockModels(baseURL string) map[string]any {
	providers := map[string]any{}
	for _, protocol := range Protocols {
		url := baseURL + "/v1"
		if protocol == ProtocolMessages {
			url = baseURL
		}
		providers[providerFor(protocol)] = map[string]any{"baseUrl": url, "api": protocol, "apiKey": "bench-key",
			"models": []any{map[string]any{"id": MockModel, "name": "Bench 1", "contextWindow": 128000, "maxTokens": 4096}}}
	}
	return map[string]any{"providers": providers}
}

// WriteSession writes a Session file with the given number of user and
// assistant exchanges.
func WriteSession(path string, exchanges int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	write := func(v any) {
		if err == nil {
			err = enc.Encode(v)
		}
	}
	write(map[string]any{"type": "session", "version": 3, "id": "00000000-0000-4000-8000-000000000001", "timestamp": "2026-09-23T00:00:00Z", "cwd": filepath.Dir(path)})
	var parent any
	for i := range exchanges {
		uid, aid := fmt.Sprintf("u%07d", i), fmt.Sprintf("a%07d", i)
		write(map[string]any{"type": "message", "id": uid, "parentId": parent, "timestamp": "2026-09-23T00:00:01Z",
			"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": fmt.Sprintf("Question %d: explain step %d.", i, i)}}, "timestamp": 1790000000000 + 2*i}})
		text := fmt.Sprintf("Step %d keeps every entry in order.\n\n```go\nfunc step%d() error { return nil }\n```\n", i, i)
		write(map[string]any{"type": "message", "id": aid, "parentId": uid, "timestamp": "2026-09-23T00:00:02Z",
			"message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}, "api": ProtocolChat, "provider": providerFor(ProtocolChat), "model": MockModel,
				"stopReason": "stop", "timestamp": 1790000000001 + 2*i,
				"usage": map[string]any{"input": 1, "output": 1, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 2,
					"cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0}}}})
		parent = aid
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

type sample struct {
	wall, cpu time.Duration
	rss       float64
}

// Summarize computes the timing summary of samples.
func Summarize(samples []sample) ScenarioResult {
	if len(samples) == 0 {
		return ScenarioResult{}
	}
	walls := make([]float64, len(samples))
	cpus := make([]float64, len(samples))
	rss := make([]float64, len(samples))
	for i, s := range samples {
		walls[i], cpus[i], rss[i] = ms(s.wall), ms(s.cpu), s.rss
	}
	slices.Sort(walls)
	// Nearest-rank p90.
	p90 := walls[max(0, int(math.Ceil(float64(len(walls))*0.9))-1)]
	return ScenarioResult{Runs: len(samples), WallMsMedian: round1(median(walls)), WallMsP90: round1(p90), WallMsMin: round1(walls[0]),
		CPUMsMedian: round1(median(cpus)), RSSMiBMedian: round1(median(rss))}
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	s := slices.Clone(values)
	slices.Sort(s)
	if n := len(s); n%2 == 1 {
		return s[n/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

// Overhead measures startup, a round trip over each protocol, and a resumed
// large Session against the mock model.
func Overhead(opts OverheadOptions) (*OverheadReport, error) {
	logf := func(format string, args ...any) {
		if opts.Log != nil {
			_, _ = fmt.Fprintf(opts.Log, format+"\n", args...)
		}
	}
	work, err := os.MkdirTemp("", "wopr-eval-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(work) }()
	home, cwd := filepath.Join(work, "home"), filepath.Join(work, "project")
	agentDir := filepath.Join(home, ".wopr", "agent")
	for _, dir := range []string{cwd, agentDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	server, err := StartMock()
	if err != nil {
		return nil, err
	}
	defer func() { _ = server.Close() }()
	models, _ := json.Marshal(MockModels(server.URL))
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), models, 0o600); err != nil {
		return nil, err
	}
	session, pristine := filepath.Join(work, "large-session.jsonl"), filepath.Join(work, "large-session.pristine")
	if err := WriteSession(pristine, opts.SessionExchanges); err != nil {
		return nil, err
	}
	env := IsolatedEnv(home)

	version := RunProc([]string{opts.Binary, "--version"}, env, cwd, time.Minute)
	if version.Code != 0 || version.TimedOut {
		return nil, fmt.Errorf("%s --version exited %d: %s", opts.Binary, version.Code, strings.TrimSpace(version.Stderr))
	}
	report := &OverheadReport{Kind: "overhead", Generated: time.Now().UTC().Format(time.RFC3339), Host: CurrentHost(),
		Version: lastLine(version.Stdout), Runs: opts.Runs, Warmup: opts.Warmup, Prompt: opts.Prompt, SessionExchanges: opts.SessionExchanges}
	logf("wopr-eval: %s", report.Version)

	for _, sc := range Scenarios {
		argv := []string{opts.Binary, "--version"}
		if sc.Protocol != "" {
			argv = []string{opts.Binary, "--provider", providerFor(sc.Protocol), "--model", MockModel}
			if sc.Session {
				argv = append(argv, "--session", session)
			} else {
				argv = append(argv, "--no-session")
			}
			argv = append(argv, "-p", opts.Prompt)
		}
		var samples []sample
		var failures []Failure
		var requests []Request
		for i := range opts.Warmup + opts.Runs {
			if sc.Session {
				if err := copyFile(pristine, session); err != nil {
					return nil, err
				}
			}
			server.Take()
			run := RunProc(argv, env, cwd, opts.Timeout)
			got := server.Take()
			if !runOK(sc, run, got) {
				failures = append(failures, Failure{Code: run.Code, TimedOut: run.TimedOut, Requests: len(got), Stderr: tail(run.Stderr, 600), Stdout: tail(run.Stdout, 300)})
				if len(failures) >= 2 {
					break
				}
				continue
			}
			if i >= opts.Warmup {
				samples = append(samples, sample{wall: run.Wall, cpu: run.CPU, rss: run.RSSMiB})
				requests = got
			}
		}
		result := Summarize(samples)
		result.Scenario, result.Title, result.Failures = sc.ID, sc.Title, failures
		if len(requests) > 0 {
			result.Requests, result.RequestBytes, result.SystemBytes, result.Tools = len(requests), requests[0].Bytes, requests[0].SystemBytes, requests[0].Tools
			for _, r := range requests {
				result.BytesSent += r.Bytes
			}
		}
		report.Results = append(report.Results, result)
		if result.Runs > 0 {
			logf("wopr-eval: %-21s %7.1f ms  p90 %7.1f ms  %5.1f MiB  %6d B", sc.ID, result.WallMsMedian, result.WallMsP90, result.RSSMiBMedian, result.RequestBytes)
		} else {
			logf("wopr-eval: %-21s FAILED (exit %d) %s", sc.ID, failures[0].Code, lastLine(failures[0].Stderr))
		}
	}
	return report, nil
}

// runOK reports whether a run did what its scenario measures: startup
// exits 0; a prompt exits 0, prints the mock's reply, and spoke only the
// scenario's protocol.
func runOK(sc Scenario, run ProcResult, got []Request) bool {
	if run.Code != 0 || run.TimedOut {
		return false
	}
	if sc.Protocol == "" {
		return true
	}
	if len(got) == 0 || !strings.Contains(run.Stdout, strings.Fields(MockReply)[0]) {
		return false
	}
	return !slices.ContainsFunc(got, func(r Request) bool { return r.Protocol != sc.Protocol })
}

// Failed reports whether any scenario produced no measured run.
func (r *OverheadReport) Failed() bool {
	return slices.ContainsFunc(r.Results, func(s ScenarioResult) bool { return s.Runs == 0 })
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

// WriteJSON writes v as indented JSON to path, creating its directory.
func WriteJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
