// Package repo locates the repository root for the automation commands, which
// run as `go run ./automation/...` from anywhere inside the checkout.
package repo

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/mod/modfile"
)

// Module is the module path of the repository root's go.mod.
const Module = "github.com/alexrudloff/wopr"

// Root returns the nearest directory at or above the working directory whose
// go.mod declares Module. Nested modules (examples, eval task fixtures) are
// skipped.
func Root() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if data, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil && modfile.ModulePath(data) == Module {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("not inside the " + Module + " checkout")
		}
		dir = parent
	}
}
