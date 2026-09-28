// Package gomodule guards the public Go module contract that makes
// `go install github.com/alexrudloff/wopr/cmd/wopr@vX.Y.Z` work.
//
// Go refuses `go install pkg@version` for a module whose go.mod carries a
// replace or exclude directive. The repository is one module; the release
// tags it vX.Y.Z (see docs/project/RELEASING.md and
// automation/release/module-tags.sh).
package gomodule

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

const rootModule = "github.com/alexrudloff/wopr"

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func parseMod(t *testing.T, path string) *modfile.File {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := modfile.Parse(path, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

// nestedRequirements returns the root go.mod's requirements on nested WOPR
// modules, keyed by their directory relative to the repository root.
func nestedRequirements(t *testing.T, root string) map[string]module.Version {
	t.Helper()
	out := map[string]module.Version{}
	for _, req := range parseMod(t, filepath.Join(root, "go.mod")).Require {
		if dir, ok := strings.CutPrefix(req.Mod.Path, rootModule+"/"); ok {
			out[dir] = req.Mod
		}
	}
	return out
}

func TestRootModuleIsGoInstallable(t *testing.T) {
	root := repoRoot(t)
	file := parseMod(t, filepath.Join(root, "go.mod"))
	if file.Module == nil || file.Module.Mod.Path != rootModule {
		t.Fatalf("root go.mod module path is not %s", rootModule)
	}
	for _, r := range file.Replace {
		t.Errorf("root go.mod replaces %s => %s; go install %s/cmd/wopr@version refuses any replace directive (resolve local modules through go.work instead)", r.Old.Path, r.New.Path, rootModule)
	}
	for _, e := range file.Exclude {
		t.Errorf("root go.mod excludes %s %s; go install pkg@version refuses any exclude directive", e.Mod.Path, e.Mod.Version)
	}

	for dir, mod := range nestedRequirements(t, root) {
		t.Errorf("root go.mod requires nested module %s %s (%s); the repository is one module", mod.Path, mod.Version, dir)
	}
}
