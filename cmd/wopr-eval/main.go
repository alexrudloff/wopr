// Command wopr-eval measures wopr. It is a development tool, driven by the
// Makefile's Evals targets (make help lists them), and is not shipped.
//
//	wopr-eval overhead   startup, round trips, and a resumed Session against a mock model
//	wopr-eval live       evals/tasks with real models, per routing mode or pinned model
//	wopr-eval mutate     seeded bug-fix tasks from wopr's own Go source
//	wopr-eval check      apply evals/budgets.toml to an overhead run
//	wopr-eval report     render a results file as Markdown
//	wopr-eval publish    copy reviewed results to evals/results and regenerate the docs page
//	wopr-eval profile    profile round trips with WOPR_PROFILE
//	wopr-eval benchcmp   compare two go test -bench outputs by median
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/internal/codingagent/router"
	"github.com/alexrudloff/wopr/internal/evals"
)

const usage = `usage: wopr-eval <command> [flags]

commands:
  overhead   startup, round trips, and a resumed Session against a mock model
  live       evals/tasks with real models, per routing mode or pinned model
  mutate     seeded bug-fix tasks from wopr's own Go source
  check      apply evals/budgets.toml to an overhead run
  report     render a results file as Markdown
  publish    copy reviewed results to evals/results and regenerate the docs page
  profile    profile round trips with WOPR_PROFILE
  benchcmp   compare two go test -bench outputs by median

Run wopr-eval <command> -h for its flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	commands := map[string]func([]string) error{
		"overhead": overhead, "live": live, "mutate": mutate, "check": check,
		"report": report, "publish": publish, "profile": profile, "benchcmp": benchcmp,
	}
	run, ok := commands[os.Args[1]]
	if !ok {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err := run(os.Args[2:]); err != nil {
		if code, ok := errors.AsType[exitCode](err); ok {
			os.Exit(int(code))
		}
		fmt.Fprintf(os.Stderr, "wopr-eval %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
}

// exitCode ends the program with a status and no message; the command has
// already said why.
type exitCode int

func (c exitCode) Error() string { return fmt.Sprintf("exit %d", int(c)) }

func newFlags(name string) *flag.FlagSet {
	return flag.NewFlagSet("wopr-eval "+name, flag.ExitOnError)
}

// writeResults writes v as JSON to out and its Markdown next to it.
func writeResults(out string, v any, markdown string) error {
	if err := evals.WriteJSON(out, v); err != nil {
		return err
	}
	md := strings.TrimSuffix(out, filepath.Ext(out)) + ".md"
	if err := os.WriteFile(md, []byte(markdown), 0o644); err != nil {
		return err
	}
	fmt.Printf("wopr-eval: wrote %s and %s\n", out, md)
	return nil
}

func overhead(args []string) error {
	fs := newFlags("overhead")
	bin := fs.String("bin", "bin/wopr", "wopr binary")
	runs := fs.Int("runs", 10, "measured runs per scenario")
	warmup := fs.Int("warmup", 1, "warm-up runs per scenario")
	prompt := fs.String("prompt", "Say hello", "prompt for the round-trip scenarios")
	exchanges := fs.Int("session-exchanges", 2000, "exchanges in the resumed Session")
	timeout := fs.Duration("timeout", 2*time.Minute, "kill a run after this long")
	out := fs.String("out", "tmp/evals/overhead.json", "results file")
	_ = fs.Parse(args)
	binary, err := filepath.Abs(*bin)
	if err != nil {
		return err
	}
	r, err := evals.Overhead(evals.OverheadOptions{Binary: binary, Runs: *runs, Warmup: *warmup, Prompt: *prompt, SessionExchanges: *exchanges, Timeout: *timeout, Log: os.Stdout})
	if err != nil {
		return err
	}
	if err := writeResults(*out, r, evals.RenderOverhead(r, "#")); err != nil {
		return err
	}
	if r.Failed() {
		return exitCode(1)
	}
	return nil
}

// splitList splits a comma-separated flag value, dropping empty items.
func splitList(s string) []string {
	var out []string
	for item := range strings.SplitSeq(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func live(args []string) error {
	fs := newFlags("live")
	bin := fs.String("bin", "bin/wopr", "wopr binary")
	modes := fs.String("mode", "", "comma-separated routing modes to compare (auto, cost, speed, quality, uncensored)")
	models := fs.String("model", "", "comma-separated provider/model pins to compare, each with routing off")
	tasks := fs.String("tasks", "all", "comma-separated task ids")
	tasksDir := fs.String("tasks-dir", "evals/tasks", "task directory; wopr-eval mutate writes one")
	runs := fs.Int("runs", 1, "runs per task and configuration")
	parallel := fs.Int("parallel", 1, "runs to execute at once")
	timeout := fs.Duration("timeout", 0, "override every task's timeout")
	out := fs.String("out", "tmp/evals/live.json", "results file")
	transcripts := fs.String("transcripts", "", "directory that keeps each run's --mode json output")
	_ = fs.Parse(args)
	var configs []evals.Config
	for _, mode := range splitList(*modes) {
		if _, ok := router.ParseObjective(mode); !ok {
			return fmt.Errorf("unknown routing mode %q; use auto, cost, speed, quality, or uncensored", mode)
		}
		configs = append(configs, evals.Config{Mode: mode})
	}
	for _, model := range splitList(*models) {
		if !strings.Contains(model, "/") {
			return fmt.Errorf("model %q must be provider/model", model)
		}
		configs = append(configs, evals.Config{Model: model})
	}
	if len(configs) == 0 {
		configs = []evals.Config{{}}
	}
	list, err := evals.LoadTasks(*tasksDir, *tasks)
	if err != nil {
		return err
	}
	binary, err := filepath.Abs(*bin)
	if err != nil {
		return err
	}
	if *transcripts != "" {
		if err := os.MkdirAll(*transcripts, 0o755); err != nil {
			return err
		}
	}
	r, err := evals.Live(evals.LiveOptions{Binary: binary, Tasks: list, Configs: configs, Runs: *runs, Parallel: *parallel, Timeout: *timeout, Transcripts: *transcripts, Log: os.Stdout})
	if err != nil {
		return err
	}
	return writeResults(*out, r, evals.RenderLive(r, "#"))
}

func mutate(args []string) error {
	fs := newFlags("mutate")
	count := fs.Int("count", 30, "tasks to generate")
	seed := fs.Uint64("seed", 1, "random seed")
	roots := fs.String("roots", "agent,ai,tui,coding", "comma-separated corpus directories")
	out := fs.String("out", "tmp/evals/mutation-tasks", "task directory to write")
	_ = fs.Parse(args)
	ids, err := evals.Mutate(".", splitList(*roots), *out, *count, *seed)
	if err != nil {
		return err
	}
	fmt.Printf("mutate: %d tasks in %s (seed %d); run them with make evals-live EVAL_TASKS_DIR=%s MODE=...\n", len(ids), *out, *seed, *out)
	return nil
}

func check(args []string) error {
	fs := newFlags("check")
	budgets := fs.String("budgets", "evals/budgets.toml", "budgets file")
	_ = fs.Parse(args)
	results := "tmp/evals/overhead.json"
	if fs.NArg() > 0 {
		results = fs.Arg(0)
	}
	r, _, err := evals.LoadReport(results)
	if err != nil {
		return err
	}
	if r == nil {
		return fmt.Errorf("%s is not an overhead run", results)
	}
	b, err := evals.LoadBudgets(*budgets)
	if err != nil {
		return err
	}
	if evals.CheckBudgets(r, b, os.Stdout) > 0 {
		return exitCode(1)
	}
	return nil
}

func report(args []string) error {
	fs := newFlags("report")
	out := fs.String("out", "-", "Markdown file, or - for stdout")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: wopr-eval report <results.json>")
	}
	o, l, err := evals.LoadReport(fs.Arg(0))
	if err != nil {
		return err
	}
	text := ""
	if o != nil {
		text = evals.RenderOverhead(o, "#")
	} else {
		text = evals.RenderLive(l, "#")
	}
	if *out == "-" {
		fmt.Print(text)
		return nil
	}
	return os.WriteFile(*out, []byte(text), 0o644)
}

func publish(args []string) error {
	fs := newFlags("publish")
	overheadIn := fs.String("overhead", "", "overhead results to publish (default: keep evals/results/latest.json)")
	liveIn := fs.String("live", "", "live results to publish (default: keep evals/results/live.json)")
	dir := fs.String("results-dir", "evals/results", "published results directory")
	page := fs.String("page", "docs/site/docs/evals.md", "generated docs page")
	_ = fs.Parse(args)
	overheadPath, livePath := filepath.Join(*dir, "latest.json"), filepath.Join(*dir, "live.json")
	for _, c := range []struct{ from, to, kind string }{{*overheadIn, overheadPath, "overhead"}, {*liveIn, livePath, "live"}} {
		if c.from == "" {
			continue
		}
		o, l, err := evals.LoadReport(c.from)
		if err != nil {
			return err
		}
		if (c.kind == "overhead") != (o != nil) || (c.kind == "live") != (l != nil) {
			return fmt.Errorf("%s is not a %s run", c.from, c.kind)
		}
		data, err := os.ReadFile(c.from)
		if err != nil {
			return err
		}
		if err := os.WriteFile(c.to, data, 0o644); err != nil {
			return err
		}
	}
	o, _, err := evals.LoadReport(overheadPath)
	if err != nil {
		return err
	}
	var l *evals.LiveReport
	if _, statErr := os.Stat(livePath); statErr == nil {
		if _, l, err = evals.LoadReport(livePath); err != nil {
			return err
		}
	}
	if err := os.WriteFile(*page, []byte(evals.RenderPage(o, l)), 0o644); err != nil {
		return err
	}
	fmt.Printf("publish: wrote %s\n", *page)
	return nil
}

func profile(args []string) error {
	fs := newFlags("profile")
	bin := fs.String("bin", "bin/wopr", "wopr binary")
	kinds := fs.String("kinds", "cpu,heap", "WOPR_PROFILE kinds")
	runs := fs.Int("runs", 1, "round trips")
	prompt := fs.String("prompt", "Say hello", "prompt")
	timeout := fs.Duration("timeout", 2*time.Minute, "kill a run after this long")
	out := fs.String("out", "tmp/profile", "profile directory")
	merge := fs.String("merge", "", "merge the CPU profiles into this file (for PGO)")
	_ = fs.Parse(args)
	binary, err := filepath.Abs(*bin)
	if err != nil {
		return err
	}
	return evals.Profile(evals.ProfileOptions{Binary: binary, Kinds: *kinds, Runs: *runs, Prompt: *prompt, Timeout: *timeout, Out: *out, Merge: *merge, Log: os.Stdout})
}

func benchcmp(args []string) error {
	fs := newFlags("benchcmp")
	threshold := fs.Float64("threshold", 5, "percent increase that counts as a regression")
	fail := fs.Bool("fail", false, "exit 1 when a regression is found")
	_ = fs.Parse(args)
	if fs.NArg() != 2 {
		return errors.New("usage: wopr-eval benchcmp [flags] <old.txt> <new.txt>")
	}
	n, err := evals.BenchCompare(fs.Arg(0), fs.Arg(1), *threshold, os.Stdout)
	if err != nil {
		return err
	}
	if n > 0 && *fail {
		return exitCode(1)
	}
	return nil
}
