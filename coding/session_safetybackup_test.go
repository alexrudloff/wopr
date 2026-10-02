package coding

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A history-rewriting git command and a database open each leave a backup
// that holds what the command could destroy.
func TestSafetyBackupSavesWhatACommandCouldDestroy(t *testing.T) {
	repo, dir := t.TempDir(), t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q")
	write("a.txt", "one\n")
	run("add", ".")
	run("commit", "-qm", "one")
	write("a.txt", "two\n")
	write("new.txt", "untracked\n")
	write("app.db", "db")
	write("app.db-wal", "wal")

	b := &safetyBackup{last: map[string]string{}, lastAt: map[string]string{}, pending: map[string][]string{}}
	notes := b.before(context.Background(), dir, repo, "cd "+repo+" && git -c x=y reset --hard HEAD && sqlite3 app.db .tables")
	if len(notes) != 2 {
		t.Fatalf("notes = %q", notes)
	}
	var found []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			found = append(found, filepath.Base(path))
		}
		return nil
	})
	got := strings.Join(found, " ")
	for _, want := range []string{"repo.bundle", "uncommitted.patch", "new.txt", "app.db", "app.db-wal"} {
		if !strings.Contains(" "+got+" ", " "+want+" ") {
			t.Errorf("backup lacks %s; has %s", want, got)
		}
	}
	// The old commits stay reachable inside the repo, under refs a
	// rewrite doesn't touch.
	if out, err := exec.Command("git", "-C", repo, "for-each-ref", "--format=%(refname)", "refs/wopr-backup/").Output(); err != nil || !strings.Contains(string(out), "/heads/") {
		t.Errorf("no kept refs: %q %v", out, err)
	}
	if notes := b.before(context.Background(), dir, repo, "git status && git log"); len(notes) != 0 {
		t.Errorf("read-only git backed up: %q", notes)
	}
}
