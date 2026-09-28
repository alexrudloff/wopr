package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// apply_patch writes files, so a wrong hunk placement corrupts code: the
// cases pin placement, fuzzy context, add/delete/move, and all-or-nothing.
func TestApplyPatchPlacesHunksAndWritesNothingOnAMiss(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "<missing>"
		}
		return string(data)
	}
	run := func(patch string) (string, bool) {
		tool := &ApplyPatchTool{CWD: dir, Queue: NewFileMutationQueue()}
		args, _ := json.Marshal(map[string]string{"input": patch})
		r, err := tool.Execute(context.Background(), "1", args, nil)
		if err != nil {
			t.Fatal(err)
		}
		return r.Content, r.IsError
	}
	write("a.go", "package a\n\nfunc f() int {\n\tx := 1\n\treturn x\n}\n\nfunc g() int {\n\tx := 1\n\treturn x\n}\n")
	write("gone.txt", "bye\n")
	write("old.txt", "one\ntwo\n")
	out, failed := run(`*** Begin Patch
*** Update File: a.go
@@ func g() int {
-	x := 1
+	x := 2
*** Add File: sub/new.txt
+hello
*** Delete File: gone.txt
*** Update File: old.txt
*** Move to: moved.txt
 one
-two  
+three
*** End Patch`)
	if failed {
		t.Fatalf("patch failed: %s", out)
	}
	if got, want := read("a.go"), "package a\n\nfunc f() int {\n\tx := 1\n\treturn x\n}\n\nfunc g() int {\n\tx := 2\n\treturn x\n}\n"; got != want {
		t.Errorf("a.go = %q", got)
	}
	if read("sub/new.txt") != "hello\n" || read("gone.txt") != "<missing>" || read("old.txt") != "<missing>" || read("moved.txt") != "one\nthree\n" {
		t.Errorf("add/delete/move: new=%q gone=%q old=%q moved=%q", read("sub/new.txt"), read("gone.txt"), read("old.txt"), read("moved.txt"))
	}
	before := read("a.go")
	if out, failed := run("*** Begin Patch\n*** Add File: other.txt\n+x\n*** Update File: a.go\n-\tnot there\n+\ty\n*** End Patch"); !failed {
		t.Fatalf("a missing hunk must fail: %s", out)
	}
	if read("a.go") != before || read("other.txt") != "<missing>" {
		t.Error("a failed patch must write nothing")
	}
}
