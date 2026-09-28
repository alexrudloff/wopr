package evals

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestJudgeNeedsCheckAndUntouchedProtectedFiles(t *testing.T) {
	for _, c := range []struct {
		check, tampered, want bool
	}{{true, false, true}, {false, false, false}, {true, true, false}} {
		if got := Judge(c.check, c.tampered); got != c.want {
			t.Errorf("Judge(%v, %v) = %v", c.check, c.tampered, got)
		}
	}
}

func TestProtectedDigestSeesEditsAndDeletions(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"test_x.py": "assert True\n", "x.py": "x = 1\n"})
	before := Digest(dir, []string{"test_x.py"})
	writeFiles(t, dir, map[string]string{"x.py": "x = 2\n"})
	if Digest(dir, []string{"test_x.py"}) != before {
		t.Fatal("editing an unprotected file must not change the digest")
	}
	writeFiles(t, dir, map[string]string{"test_x.py": "assert False\n"})
	if Digest(dir, []string{"test_x.py"}) == before {
		t.Fatal("editing a protected file must change the digest")
	}
	if err := os.Remove(filepath.Join(dir, "test_x.py")); err != nil {
		t.Fatal(err)
	}
	if Digest(dir, []string{"test_x.py"}) == before {
		t.Fatal("deleting a protected file must change the digest")
	}
}
