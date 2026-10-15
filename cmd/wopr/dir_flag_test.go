package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexrudloff/wopr/internal/testenv"
)

// sessionStoreDir mirrors encodeCwdForSessionDir (internal/codingagent) so the
// test can name the default session store for a --dir cwd.
func sessionStoreDir(agentDir, cwd string) string {
	s := strings.TrimPrefix(cwd, "/")
	s = strings.NewReplacer("/", "-", `\`, "-", ":", "-").Replace(s)
	return filepath.Join(agentDir, "sessions", "--"+s+"--")
}

func TestResolveWorkingDir(t *testing.T) {
	dir := t.TempDir()
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}

	got, err := resolveWorkingDir(dir)
	if err != nil {
		t.Fatalf("resolveWorkingDir(%q): %v", dir, err)
	}
	if got != want {
		t.Fatalf("resolveWorkingDir(%q) = %q, want %q", dir, got, want)
	}

	if _, err := resolveWorkingDir(filepath.Join(dir, "does-not-exist")); err == nil {
		t.Fatal("resolveWorkingDir(missing) succeeded, want error")
	}

	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveWorkingDir(file); err == nil {
		t.Fatal("resolveWorkingDir(file) succeeded, want error")
	}

	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	testenv.Symlink(t, target, link)
	got, err = resolveWorkingDir(link)
	if err != nil {
		t.Fatalf("resolveWorkingDir(symlink): %v", err)
	}
	if got != resolvedTarget {
		t.Fatalf("resolveWorkingDir(symlink) = %q, want %q", got, resolvedTarget)
	}
}

func TestDirFlagIsSessionCWD(t *testing.T) {
	home := t.TempDir()
	agentDir := filepath.Join(home, "agent")
	otherDir := t.TempDir()
	targetDir := t.TempDir()

	p := startRPCProcessAt(t, otherDir, []string{
		"HOME=" + home,
		"WOPR_HOME=" + home,
		"WOPR_CODING_AGENT_DIR=" + agentDir,
		"WOPR_TEST_FAUX=1",
	}, "--dir", targetDir, "--model", "test-faux/faux-1")

	p.send(`{"id":"b","type":"bash","command":"pwd"}`)
	p.await("bash pwd", func(r rpcRecord) bool {
		if !isSuccessResponse(r, "b") {
			return false
		}
		data := r["data"].(map[string]any)
		got := strings.TrimSpace(fmt.Sprint(data["output"]))
		if got != targetDir {
			t.Fatalf("pwd = %q, want %q (process cwd was %q)", got, targetDir, otherDir)
		}
		return true
	})

	// Persistence is on (no --no-session): a prompt must write the session
	// store under the --dir cwd, not the process cwd.
	p.send(`{"id":"p","type":"prompt","message":"say hi"}`)
	p.await("prompt settling", func(r rpcRecord) bool { return r["type"] == "agent_settled" })

	store := sessionStoreDir(agentDir, targetDir)
	entries, err := os.ReadDir(store)
	if err != nil {
		t.Fatalf("session store %s: %v", store, err)
	}
	if len(entries) == 0 {
		t.Fatalf("session store %s is empty", store)
	}
	p.closeAndWait("dir flag run")
}

func TestResumeStoredCWDSupersedesDirFlag(t *testing.T) {
	home := t.TempDir()
	agentDir := filepath.Join(home, "agent")
	otherDir := t.TempDir()
	dirFlag := t.TempDir()
	storedCWD := t.TempDir()
	stored := writeStartupSession(t, t.TempDir(), "stored-cwd", storedCWD)

	p := startRPCProcessAt(t, otherDir, []string{
		"HOME=" + home,
		"WOPR_HOME=" + home,
		"WOPR_CODING_AGENT_DIR=" + agentDir,
		"WOPR_TEST_FAUX=1",
	}, "--dir", dirFlag, "--session", stored, "--model", "test-faux/faux-1")

	p.send(`{"id":"b","type":"bash","command":"pwd"}`)
	p.await("bash pwd", func(r rpcRecord) bool {
		if !isSuccessResponse(r, "b") {
			return false
		}
		data := r["data"].(map[string]any)
		got := strings.TrimSpace(fmt.Sprint(data["output"]))
		if got != storedCWD {
			t.Fatalf("pwd = %q, want stored cwd %q (--dir %q)", got, storedCWD, dirFlag)
		}
		return true
	})
	p.closeAndWait("resume stored cwd run")
}
