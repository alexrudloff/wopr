package evals

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ProfileOptions configures Profile.
type ProfileOptions struct {
	Binary string
	// Kinds is the WOPR_PROFILE value, for example "cpu,heap".
	Kinds   string
	Runs    int
	Prompt  string
	Timeout time.Duration
	Out     string
	// Merge, when set, merges the CPU profiles into this file for a
	// profile-guided build instead of summarizing them.
	Merge string
	Log   io.Writer
}

// Profile runs wopr round trips against the mock model with WOPR_PROFILE
// set, then summarizes the profiles with go tool pprof or merges them.
func Profile(opts ProfileOptions) error {
	out, err := filepath.Abs(opts.Out)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "wopr-eval-profile-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	home, cwd := filepath.Join(work, "home"), filepath.Join(work, "project")
	agentDir := filepath.Join(home, ".wopr", "agent")
	for _, dir := range []string{cwd, agentDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	server, err := StartMock()
	if err != nil {
		return err
	}
	defer func() { _ = server.Close() }()
	models, _ := json.Marshal(MockModels(server.URL))
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), models, 0o600); err != nil {
		return err
	}
	env := append(IsolatedEnv(home), "WOPR_PROFILE="+opts.Kinds, "WOPR_PROFILE_DIR="+out)
	argv := []string{opts.Binary, "--provider", providerFor(ProtocolChat), "--model", MockModel, "--no-session", "-p", opts.Prompt}
	for i := range opts.Runs {
		if r := RunProc(argv, env, cwd, opts.Timeout); r.Code != 0 {
			return fmt.Errorf("run %d exited %d: %s", i+1, r.Code, tail(r.Stderr, 800))
		}
	}
	files, _ := filepath.Glob(filepath.Join(out, "wopr-*"))
	_, _ = fmt.Fprintf(opts.Log, "profile: %d run(s), %d file(s) in %s\n", opts.Runs, len(files), out)
	var cpu []string
	for _, f := range files {
		if strings.HasSuffix(f, "-cpu.pprof") {
			cpu = append(cpu, f)
		}
	}
	if opts.Merge != "" {
		if len(cpu) == 0 {
			return fmt.Errorf("--merge needs cpu in the profile kinds")
		}
		if err := os.MkdirAll(filepath.Dir(opts.Merge), 0o755); err != nil {
			return err
		}
		merged, err := os.Create(opts.Merge)
		if err != nil {
			return err
		}
		cmd := exec.Command("go", append([]string{"tool", "pprof", "-proto"}, cpu...)...)
		cmd.Stdout = merged
		err = cmd.Run()
		if cerr := merged.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("go tool pprof: %w", err)
		}
		_, _ = fmt.Fprintf(opts.Log, "profile: merged %d CPU profiles into %s. For a profile-guided build, copy it to cmd/wopr/default.pgo and compare make evals before and after.\n", len(cpu), opts.Merge)
		return nil
	}
	for _, f := range files {
		if !strings.HasSuffix(f, ".pprof") {
			continue
		}
		args := []string{"tool", "pprof", "-top", "-nodecount=12"}
		if !strings.HasSuffix(f, "-cpu.pprof") {
			args = append(args, "-sample_index=alloc_space")
		}
		top, _ := exec.Command("go", append(args, opts.Binary, f)...).Output()
		lines := strings.Split(string(top), "\n")
		_, _ = fmt.Fprintf(opts.Log, "\n== %s\n%s\n", filepath.Base(f), strings.Join(lines[:min(len(lines), 18)], "\n"))
	}
	_, _ = fmt.Fprintf(opts.Log, "\nExplore: go tool pprof -http=: %s <file>.pprof    Trace: go tool trace <file>.trace\n", opts.Binary)
	return nil
}
