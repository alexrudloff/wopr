package codingagent

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestProjectTrustStore_RoundTripAndInheritance(t *testing.T) {
	agentDir := t.TempDir()
	project := t.TempDir()
	child := filepath.Join(project, "sub", "deep")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}

	store := NewProjectTrustStore(agentDir)
	got, err := store.Get(project)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("Get(project) before set = %v, want nil", *got)
	}

	if err := store.Set(project, new(true)); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(project)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || !*got {
		t.Fatalf("Get(project) = %v, want true", got)
	}
	entry, err := store.GetEntry(child)
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil || !entry.Decision || entry.Path != GetProjectTrustPath(project) {
		t.Fatalf("GetEntry(child) = %+v, want inherited true from project", entry)
	}

	if _, err := os.Stat(filepath.Join(agentDir, "trust.json")); err != nil {
		t.Fatalf("trust.json not written: %v", err)
	}

	if err := store.Set(project, nil); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(child)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("Get(child) after clear = %v, want nil", *got)
	}
}

func TestProjectTrustStore_MalformedDataSurfacesError(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "invalid JSON", data: `{`, want: "Failed to read trust store"},
		{name: "array", data: `[]`, want: "expected an object"},
		{name: "invalid value", data: `{"/project":"yes"}`, want: `value for "/project" must be true, false, or null`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agentDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(agentDir, "trust.json"), []byte(tc.data), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := NewProjectTrustStore(agentDir).Get(t.TempDir())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Get() error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestProjectTrustStore_ConcurrentUpdatesDoNotLoseEntries(t *testing.T) {
	store := NewProjectTrustStore(t.TempDir())
	root := t.TempDir()
	const count = 8
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := range count {
		wg.Go(func() {
			errs <- store.Set(filepath.Join(root, string(rune('a'+i))), new(true))
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := range count {
		decision, err := store.Get(filepath.Join(root, string(rune('a'+i))))
		if err != nil {
			t.Fatal(err)
		}
		if decision == nil || !*decision {
			t.Fatalf("entry %d missing after concurrent updates", i)
		}
	}
}

func TestHasTrustRequiringProjectResources_ExactProjectResources(t *testing.T) {
	for _, entry := range []string{
		"settings.json", "skills", "prompts", "themes", "SYSTEM.md", "APPEND_SYSTEM.md",
	} {
		t.Run(entry, func(t *testing.T) {
			project := t.TempDir()
			path := filepath.Join(project, CONFIG_DIR_NAME, entry)
			if filepath.Ext(entry) == "" {
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if !HasTrustRequiringProjectResources(project) {
				t.Fatalf("%s should require project trust", entry)
			}
		})
	}
}

// A project .mcp.json names commands wopr would launch.
func TestHasTrustRequiringProjectResources_ProjectMCPFile(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, ProjectMCPFileName), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !HasTrustRequiringProjectResources(project) {
		t.Fatal(".mcp.json should require project trust")
	}
}
