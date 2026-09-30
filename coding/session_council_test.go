package coding

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/internal/codingagent/router"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
)

type councilTestHost map[string]bool

func (h councilTestHost) ModelInfo(provider, model string) (router.ModelInfo, bool) {
	return router.ModelInfo{DisplayName: model, ContextWindow: 200000, MaxOutputTokens: 16000}, h[provider+"/"+model]
}

func (councilTestHost) APIKey(string) string { return "" }

// The war council asks everyone but the orchestrator, keeps to private
// connections in private mode, skips members that can't answer, and drops a
// late member without waiting for it.
func TestWarCouncilMembership(t *testing.T) {
	cfg := router.DefaultConfig()
	cfg.Tiers = []router.TierConfig{
		{Name: "subscription", Cost: router.CostSubscription, Models: []router.ModelRef{
			{Provider: "anthropic", Model: "lead"}, {Provider: "openai-codex", Model: "second"}, {Provider: "openai-codex", Model: "signed-out"},
		}},
		{Name: "box", Cost: router.CostFreeRemote, ProbeURL: "http://127.0.0.1:1/v1/models", Models: []router.ModelRef{{Provider: "box", Model: "down"}}},
		{Name: "home", Cost: router.CostFreeLocal, Models: []router.ModelRef{{Provider: "home", Model: "local"}}},
	}
	cfg.PrivateProviders = []string{"box", "home"}
	r := router.New(cfg, councilTestHost{"anthropic/lead": true, "openai-codex/second": true, "box/down": true, "home/local": true})

	summary := func(members []router.CouncilMember) string {
		var out []string
		for _, m := range members {
			out = append(out, m.Ref.Spec()+"="+m.Skip)
		}
		return strings.Join(out, " ")
	}
	if got, want := summary(r.CouncilMembers(false, "anthropic/lead")), "openai-codex/second= openai-codex/signed-out=not signed in box/down=unreachable home/local="; got != want {
		t.Fatalf("members = %q, want %q", got, want)
	}
	if got, want := summary(r.CouncilMembers(true, "anthropic/lead")), "box/down=unreachable home/local="; got != want {
		t.Fatalf("private members = %q, want %q", got, want)
	}

	start := time.Now()
	proposals, missing := gatherCouncil(context.Background(), []councilCall{
		{name: "fast", spec: "home/local", run: func(context.Context) (string, error) { return "plan A", nil }},
		// A provider that never notices cancellation.
		{name: "slow", spec: "box/down", run: func(context.Context) (string, error) { time.Sleep(10 * time.Second); return "too late", nil }},
	}, 100*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("waited %s for a late member", elapsed)
	}
	if len(proposals) != 1 || proposals[0].text != "plan A" {
		t.Fatalf("proposals = %+v, want only the fast one", proposals)
	}
	if len(missing) != 1 || !strings.Contains(missing[0], "slow (late") {
		t.Fatalf("missing = %q, want the slow member dropped as late", missing)
	}
}

// A council build member works in its own worktree: its file tools can't
// reach the user's files, the user's tree is untouched until apply, apply
// lands exactly the member's change, and the worktree is gone afterwards.
func TestCouncilBuildWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	root := t.TempDir()
	run := func(args ...string) string {
		out, err := git(ctx, root, nil, append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	read := func(path string) string {
		data, _ := os.ReadFile(path)
		return string(data)
	}
	run("init", "-q")
	must(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\n"), 0o644))
	run("add", "a.txt")
	run("commit", "-qm", "base")
	// The user's uncommitted work: a changed file and an untracked one.
	must(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("two\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "b.txt"), []byte("new\n"), 0o644))

	w, err := newCouncilWorktree(ctx, root, filepath.Join(t.TempDir(), "council", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if read(filepath.Join(w.dir, "a.txt")) != "two\n" || read(filepath.Join(w.dir, "b.txt")) != "new\n" {
		t.Fatal("the worktree doesn't start from the user's uncommitted files")
	}
	write := confinedTool{AgentTool: &tools.WriteTool{CWD: w.dir, Queue: tools.NewFileMutationQueue()}, root: w.dir, cwd: w.dir}
	if _, err := write.Execute(ctx, "1", []byte(`{"path":`+strconv.Quote(filepath.Join(root, "a.txt"))+`,"content":"escaped\n"}`), nil); err == nil {
		t.Fatal("a write outside the worktree was allowed")
	}
	if _, err := write.Execute(ctx, "2", []byte(`{"path":"a.txt","content":"three\n"}`), nil); err != nil {
		t.Fatal(err)
	}
	diff, _, files, err := w.change(ctx)
	if err != nil || len(files) != 1 || files[0] != "a.txt" {
		t.Fatalf("change = %q, %v; want a.txt", files, err)
	}
	if read(filepath.Join(root, "a.txt")) != "two\n" {
		t.Fatal("the user's file changed before apply")
	}
	w.remove(ctx)
	if _, err := os.Stat(w.dir); !os.IsNotExist(err) {
		t.Fatal("the worktree is still on disk")
	}
	if list := run("worktree", "list", "--porcelain"); strings.Count(list, "worktree ") != 1 {
		t.Fatalf("git still lists the worktree:\n%s", list)
	}
	if err := applyCandidateDiff(ctx, root, diff); err != nil {
		t.Fatal(err)
	}
	if read(filepath.Join(root, "a.txt")) != "three\n" || read(filepath.Join(root, "b.txt")) != "new\n" {
		t.Fatal("apply didn't land exactly the member's change")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
