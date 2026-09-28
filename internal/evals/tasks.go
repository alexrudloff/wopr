package evals

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/format"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Task is one live coding task: a starting repository, a prompt, and how
// to judge the result.
type Task struct {
	ID     string `toml:"-"`
	Files  string `toml:"-"`
	Prompt string `toml:"prompt"`
	// Check is a shell command run in the repository; exit 0 passes.
	Check string `toml:"check"`
	// GofmtSHA256 maps a Go file to the SHA-256 of its gofmt output; the
	// file must format to exactly that. Mutation tasks use it.
	GofmtSHA256 map[string]string `toml:"gofmt_sha256"`
	// Protected files must be unchanged at the end.
	Protected []string `toml:"protected"`
	// Timeout is in seconds (default 600).
	Timeout int `toml:"timeout"`
}

// LoadTasks reads every <root>/<id>/task.toml, or only the ids in spec (a
// comma-separated list; "" or "all" means every task).
func LoadTasks(root, spec string) ([]Task, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	if spec != "" && spec != "all" {
		for id := range strings.SplitSeq(spec, ",") {
			wanted[strings.TrimSpace(id)] = true
		}
	}
	var tasks []Task
	for _, e := range entries {
		path := filepath.Join(root, e.Name(), "task.toml")
		if !e.IsDir() || (spec != "" && spec != "all" && !wanted[e.Name()]) {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			continue
		}
		var t Task
		md, err := toml.DecodeFile(path, &t)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if undecoded := md.Undecoded(); len(undecoded) > 0 {
			return nil, fmt.Errorf("%s: unknown key %s", path, undecoded[0])
		}
		if t.Prompt == "" || (t.Check == "" && len(t.GofmtSHA256) == 0) {
			return nil, fmt.Errorf("%s: needs prompt and check or gofmt_sha256", path)
		}
		t.ID, t.Files = e.Name(), filepath.Join(root, e.Name(), "files")
		if t.Timeout == 0 {
			t.Timeout = 600
		}
		tasks = append(tasks, t)
	}
	var missing []string
	for id := range wanted {
		if !slices.ContainsFunc(tasks, func(t Task) bool { return t.ID == id }) {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return nil, fmt.Errorf("unknown task(s): %s", strings.Join(missing, ", "))
	}
	return tasks, nil
}

// Digest hashes the named files under root; a missing file hashes as
// missing, so deleting a protected file changes the digest.
func Digest(root string, names []string) string {
	h := sha256.New()
	for _, name := range slices.Sorted(slices.Values(names)) {
		h.Write([]byte(name + "\x00"))
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			data = []byte("<missing>")
		}
		h.Write(data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// GofmtHash returns the SHA-256 of src's gofmt output.
func GofmtHash(src []byte) (string, error) {
	out, err := format.Source(src)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(out)
	return hex.EncodeToString(sum[:]), nil
}

// CheckResult is the outcome of a task's check in one repository.
type CheckResult struct {
	Passed bool
	Output string
}

// RunCheck judges repo against the task's check command and gofmt hashes.
func RunCheck(t Task, repo string) CheckResult {
	var out strings.Builder
	passed := true
	for _, name := range slices.Sorted(maps.Keys(t.GofmtSHA256)) {
		want := t.GofmtSHA256[name]
		src, err := os.ReadFile(filepath.Join(repo, name))
		got := ""
		if err == nil {
			got, err = GofmtHash(src)
		}
		if err != nil || got != want {
			passed = false
			fmt.Fprintf(&out, "%s: gofmt output does not match the original", name)
			if err != nil {
				fmt.Fprintf(&out, " (%v)", err)
			}
			out.WriteString("\n")
		}
	}
	if t.Check != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "sh", "-c", t.Check)
		cmd.Dir = repo
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, &buf
		if err := cmd.Run(); err != nil {
			passed = false
			if _, ok := errors.AsType[*exec.ExitError](err); !ok {
				fmt.Fprintf(&out, "check: %v\n", err)
			}
		}
		out.Write(buf.Bytes())
	}
	return CheckResult{Passed: passed, Output: tail(out.String(), 400)}
}

// Judge decides a run: the check passed and no protected file changed. A
// run cut off at its time limit is still judged on what it left behind;
// the summary counts those passes separately.
func Judge(checkPassed, tampered bool) bool {
	return checkPassed && !tampered
}

// CopyTree copies the regular files under src into dst.
func CopyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}
