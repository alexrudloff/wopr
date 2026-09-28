package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexrudloff/wopr/internal/codingagent"
)

func TestResolveProjectTrustedHonorsOverrideBeforeProjectResources(t *testing.T) {
	project := trustProjectFixture(t)
	for _, trusted := range []bool{false, true} {
		got, err := resolveProjectTrusted(context.Background(), projectTrustResolutionOptions{
			CWD: project, Store: codingagent.NewProjectTrustStore(t.TempDir()), Override: new(trusted), Default: "ask",
		})
		if err != nil {
			t.Fatal(err)
		}
		if got != trusted {
			t.Fatalf("override %v resolved to %v", trusted, got)
		}
	}
}

func trustProjectFixture(t *testing.T) string {
	t.Helper()
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, codingagent.CONFIG_DIR_NAME, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	return project
}
