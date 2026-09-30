package evals

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Config is one way to pick models for a live run: a routing mode, a
// pinned model, or wopr's own default.
type Config struct {
	// Mode is a routing mode (auto, cost, speed, quality, uncensored).
	Mode string `json:"mode,omitempty"`
	// Model pins provider/model with routing off.
	Model string `json:"model,omitempty"`
	// GTW runs wopr in Global Thermonuclear War (--gtw): the top model at
	// max thinking with the war council.
	GTW bool `json:"gtw,omitempty"`
}

// Label names the configuration in reports.
func (c Config) Label() string {
	switch {
	case c.GTW:
		return "gtw"
	case c.Model != "":
		return c.Model
	case c.Mode != "":
		return "mode " + c.Mode
	}
	return "default"
}

// Args returns the wopr arguments and extra environment for c.
func (c Config) Args() (args, env []string) {
	switch {
	case c.GTW:
		return []string{"--gtw"}, nil
	case c.Model != "":
		return []string{"--model", c.Model}, []string{"WOPR_ROUTER=off"}
	case c.Mode != "":
		return nil, []string{"WOPR_ROUTER=" + c.Mode}
	}
	return nil, nil
}

// Metrics are what one run's --mode json transcript reports.
type Metrics struct {
	Turns        int `json:"turns"`
	ToolCalls    int `json:"tool_calls"`
	PollingCalls int `json:"polling_calls"`
	// InputTokens is the whole prompt (uncached + cache read + cache
	// write); CacheReadTokens is its cache-read share.
	InputTokens     int     `json:"input_tokens"`
	CacheReadTokens int     `json:"cache_read_tokens"`
	OutputTokens    int     `json:"output_tokens"`
	CostUSD         float64 `json:"cost_usd"`
	// Models maps provider/model to the number of turns it answered.
	Models map[string]int `json:"models,omitempty"`
}

var (
	pollingRE      = regexp.MustCompile(`^\s*(sleep|ps|pgrep|top|watch|wait|jobs)\b|^\s*tail\s+-f\b`)
	pollingSplitRE = regexp.MustCompile(`&&|;|\|\|`)
)

// IsPolling reports whether a shell command only waits on or inspects a
// running job instead of doing work.
func IsPolling(command string) bool {
	parts := pollingSplitRE.Split(command, -1)
	seen := false
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		if !pollingRE.MatchString(part) {
			return false
		}
		seen = true
	}
	return seen
}

// ParseTranscript sums wopr's --mode json events: every finished assistant
// message is one turn, with its model, usage, cost, and tool calls.
func ParseTranscript(stdout string) Metrics {
	m := Metrics{Models: map[string]int{}}
	scanner := bufio.NewScanner(strings.NewReader(stdout))
	scanner.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var event struct {
			Type    string `json:"type"`
			Message struct {
				Role     string `json:"role"`
				Provider string `json:"provider"`
				Model    string `json:"model"`
				Usage    struct {
					Input      int `json:"input"`
					Output     int `json:"output"`
					CacheRead  int `json:"cacheRead"`
					CacheWrite int `json:"cacheWrite"`
					Cost       struct {
						Total float64 `json:"total"`
					} `json:"cost"`
				} `json:"usage"`
				Content []struct {
					Type      string         `json:"type"`
					Arguments map[string]any `json:"arguments"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &event) != nil || event.Type != "message_end" || event.Message.Role != "assistant" {
			continue
		}
		msg := event.Message
		m.Turns++
		m.InputTokens += msg.Usage.Input + msg.Usage.CacheRead + msg.Usage.CacheWrite
		m.CacheReadTokens += msg.Usage.CacheRead
		m.OutputTokens += msg.Usage.Output
		m.CostUSD += msg.Usage.Cost.Total
		if msg.Model != "" {
			m.Models[msg.Provider+"/"+msg.Model]++
		}
		for _, part := range msg.Content {
			if part.Type != "toolCall" {
				continue
			}
			m.ToolCalls++
			if command, _ := part.Arguments["command"].(string); IsPolling(command) {
				m.PollingCalls++
			}
		}
	}
	return m
}

// TaskResult is one attempt at one task.
type TaskResult struct {
	Config      string  `json:"config"`
	Task        string  `json:"task"`
	Run         int     `json:"run"`
	Passed      bool    `json:"passed"`
	Exit        int     `json:"exit"`
	TimedOut    bool    `json:"timed_out"`
	Tampered    bool    `json:"tampered"`
	WallS       float64 `json:"wall_s"`
	CPUS        float64 `json:"cpu_s"`
	RSSMiB      float64 `json:"rss_mib"`
	Metrics     Metrics `json:"metrics"`
	CheckOutput string  `json:"check_output,omitempty"`
	// Transcript is wopr's --mode json output.
	Transcript string `json:"-"`
}

// Summary aggregates one configuration's attempts.
type Summary struct {
	Config   string `json:"config"`
	Passed   int    `json:"passed"`
	Attempts int    `json:"attempts"`
	// PassedAtCutoff counts passes whose run hit the time limit.
	PassedAtCutoff int     `json:"passed_at_cutoff"`
	PassRate       float64 `json:"pass_rate"`
	// PassRateLow and PassRateHigh are the 95% Wilson score interval.
	PassRateLow  float64        `json:"pass_rate_low"`
	PassRateHigh float64        `json:"pass_rate_high"`
	CostUSD      float64        `json:"cost_usd"`
	CostPerTask  float64        `json:"cost_per_task"`
	CostPerPass  *float64       `json:"cost_per_pass"`
	InputTokens  float64        `json:"input_tokens_per_task"`
	CachedTokens float64        `json:"cached_tokens_per_task"`
	CachedShare  float64        `json:"cached_share"`
	OutputTokens float64        `json:"output_tokens_per_task"`
	Turns        float64        `json:"turns_per_task"`
	ToolCalls    float64        `json:"tool_calls_per_task"`
	PollingCalls float64        `json:"polling_calls_per_task"`
	WallSMedian  float64        `json:"wall_s_median"`
	WallSTotal   float64        `json:"wall_s_total"`
	Models       map[string]int `json:"models,omitempty"`
}

// LiveReport is the result of one live run.
type LiveReport struct {
	Kind      string       `json:"kind"`
	Generated string       `json:"generated"`
	Host      Host         `json:"host"`
	Version   string       `json:"version"`
	Runs      int          `json:"runs"`
	Tasks     []string     `json:"tasks"`
	Results   []TaskResult `json:"results"`
	Summary   []Summary    `json:"summary"`
}

// LiveOptions configures Live.
type LiveOptions struct {
	Binary  string
	Tasks   []Task
	Configs []Config
	Runs    int
	// Parallel is how many runs go at once (default 1).
	Parallel int
	// Timeout overrides each task's own timeout when non-zero.
	Timeout time.Duration
	// Transcripts, when set, is a directory that keeps each run's
	// --mode json output.
	Transcripts string
	Log         io.Writer
}

// Live runs every task under every configuration with wopr's own
// configuration (credentials and router.json), in a fresh copy of the task.
func Live(opts LiveOptions) (*LiveReport, error) {
	work, err := os.MkdirTemp("", "wopr-eval-live-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(work) }()
	version := RunProc([]string{opts.Binary, "--version"}, os.Environ(), work, time.Minute)
	report := &LiveReport{Kind: "live", Generated: time.Now().UTC().Format(time.RFC3339), Host: CurrentHost(), Version: lastLine(version.Stdout), Runs: opts.Runs}
	for _, t := range opts.Tasks {
		report.Tasks = append(report.Tasks, t.ID)
	}
	type job struct {
		cfg Config
		t   Task
		run int
	}
	var jobs []job
	for _, cfg := range opts.Configs {
		for _, t := range opts.Tasks {
			for run := range opts.Runs {
				jobs = append(jobs, job{cfg, t, run})
			}
		}
	}
	results := make([]TaskResult, len(jobs))
	errs := make([]error, len(jobs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, max(opts.Parallel, 1))
	for i, j := range jobs {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			// Each run gets its own root, removed once it is judged, so no other
			// copy of the task (solved or not) is on disk while it runs.
			root, err := os.MkdirTemp(work, "run-")
			if err != nil {
				results[i], errs[i] = TaskResult{}, err
				return
			}
			r, err := RunTask(opts.Binary, j.t, j.cfg, filepath.Join(root, "repo"), opts.Timeout)
			_ = os.RemoveAll(root)
			r.Run = j.run
			if err == nil && opts.Transcripts != "" {
				name := strings.NewReplacer("/", "_", " ", "_").Replace(fmt.Sprintf("%s-%s-%d.jsonl", j.cfg.Label(), j.t.ID, j.run))
				err = os.WriteFile(filepath.Join(opts.Transcripts, name), []byte(r.Transcript), 0o600)
			}
			results[i], errs[i] = r, err
			if err != nil || opts.Log == nil {
				return
			}
			verdict := "FAIL"
			if r.Passed {
				verdict = "pass"
			}
			mu.Lock()
			defer mu.Unlock()
			_, _ = fmt.Fprintf(opts.Log, "live: %-24s %-40s %s %7.1f s  %3d turns  $%.4f  %d tok  %s\n", j.cfg.Label(), j.t.ID, verdict, r.WallS, r.Metrics.Turns, r.Metrics.CostUSD,
				r.Metrics.InputTokens+r.Metrics.OutputTokens, strings.Join(slices.Sorted(maps.Keys(r.Metrics.Models)), ","))
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	report.Results = results
	for _, cfg := range opts.Configs {
		var mine []TaskResult
		for _, r := range results {
			if r.Config == cfg.Label() {
				mine = append(mine, r)
			}
		}
		report.Summary = append(report.Summary, SummarizeLive(cfg.Label(), mine))
	}
	return report, nil
}

// RunTask copies t into repo, runs wopr on its prompt under cfg, and judges
// the result.
func RunTask(binary string, t Task, cfg Config, repo string, timeout time.Duration) (TaskResult, error) {
	if err := CopyTree(t.Files, repo); err != nil {
		return TaskResult{}, err
	}
	if _, err := exec.LookPath("git"); err == nil {
		// A repository lets the agent diff its own work.
		for _, argv := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=wopr-eval", "-c", "user.email=wopr-eval@localhost", "commit", "-qm", "task"}} {
			cmd := exec.Command("git", argv...)
			cmd.Dir = repo
			_ = cmd.Run()
		}
	}
	before := Digest(repo, t.Protected)
	args, env := cfg.Args()
	// A session outside the repository keeps the agent's view clean while
	// the mechanisms that need a persisted session (observation archive,
	// reducer) run as they do interactively.
	argv := append([]string{binary, "--mode", "json", "--session-dir", repo + ".sessions"}, args...)
	argv = append(argv, "-p", t.Prompt)
	if timeout == 0 {
		timeout = time.Duration(t.Timeout) * time.Second
	}
	run := RunProc(argv, append(sandboxEnv(filepath.Dir(repo)), env...), repo, timeout)
	check := RunCheck(t, repo)
	tampered := Digest(repo, t.Protected) != before
	output := check.Output
	if run.Code != 0 && strings.TrimSpace(run.Stderr) != "" {
		output = tail(output+"\nwopr: "+lastLine(run.Stderr), 400)
	}
	return TaskResult{Config: cfg.Label(), Task: t.ID, Passed: Judge(check.Passed, tampered), Exit: run.Code, TimedOut: run.TimedOut,
		Tampered: tampered, WallS: round2(run.Wall.Seconds()), CPUS: round2(run.CPU.Seconds()), RSSMiB: round1(run.RSSMiB),
		Metrics: ParseTranscript(run.Stdout), CheckOutput: output, Transcript: run.Stdout}, nil
}

// SummarizeLive aggregates one configuration's attempts.
func SummarizeLive(label string, results []TaskResult) Summary {
	s := Summary{Config: label, Attempts: len(results), Models: map[string]int{}}
	if len(results) == 0 {
		return s
	}
	var walls []float64
	var in, cached, out, turns, calls, polls float64
	for _, r := range results {
		if r.Passed {
			s.Passed++
			if r.TimedOut {
				s.PassedAtCutoff++
			}
		}
		s.CostUSD += r.Metrics.CostUSD
		in += float64(r.Metrics.InputTokens)
		cached += float64(r.Metrics.CacheReadTokens)
		out += float64(r.Metrics.OutputTokens)
		turns += float64(r.Metrics.Turns)
		calls += float64(r.Metrics.ToolCalls)
		polls += float64(r.Metrics.PollingCalls)
		walls = append(walls, r.WallS)
		s.WallSTotal += r.WallS
		for model, n := range r.Metrics.Models {
			s.Models[model] += n
		}
	}
	s.PassRate = 100 * float64(s.Passed) / float64(s.Attempts)
	s.PassRateLow, s.PassRateHigh = Wilson(s.Passed, s.Attempts)
	n := float64(len(results))
	s.CostPerTask, s.InputTokens, s.CachedTokens, s.OutputTokens, s.Turns, s.ToolCalls, s.PollingCalls = s.CostUSD/n, in/n, cached/n, out/n, turns/n, calls/n, polls/n
	if in > 0 {
		s.CachedShare = cached / in
	}
	if s.Passed > 0 {
		perPass := s.CostUSD / float64(s.Passed)
		s.CostPerPass = &perPass
	}
	s.WallSMedian = round2(median(walls))
	s.WallSTotal = round2(s.WallSTotal)
	return s
}

// Wilson returns the 95% Wilson score interval, in percent, for passed
// successes out of n attempts.
func Wilson(passed, n int) (low, high float64) {
	if n == 0 {
		return 0, 0
	}
	const z = 1.959964
	p, nf := float64(passed)/float64(n), float64(n)
	center := (p + z*z/(2*nf)) / (1 + z*z/nf)
	half := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf)) / (1 + z*z/nf)
	return round2(100 * max(0, center-half)), round2(100 * min(1, center+half))
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// sandboxEnv is the environment a live run starts with: HOME inside the
// run's root, so the agent's view of home holds nothing but the task, while
// wopr's own configuration and credentials (WOPR_HOME) and Go's module and
// build caches keep pointing at the real ones.
func sandboxEnv(root string) []string {
	home := filepath.Join(root, "home")
	_ = os.MkdirAll(home, 0o700)
	keep := map[string]string{}
	if v := os.Getenv("WOPR_HOME"); v != "" {
		keep["WOPR_HOME"] = v
	} else if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		keep["WOPR_HOME"] = filepath.Join(v, "wopr")
	} else if real, err := os.UserHomeDir(); err == nil {
		keep["WOPR_HOME"] = filepath.Join(real, ".wopr")
	}
	for _, name := range []string{"GOMODCACHE", "GOCACHE", "GOPATH"} {
		if out, err := exec.Command("go", "env", name).Output(); err == nil {
			if v := strings.TrimSpace(string(out)); v != "" {
				keep[name] = v
			}
		}
	}
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		_, overridden := keep[name]
		return name == "HOME" || overridden
	})
	env = append(env, "HOME="+home)
	for name, v := range keep {
		env = append(env, name+"="+v)
	}
	return env
}
