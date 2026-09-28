// Package docsdrift gates the shipped documentation bundle against the code it
// describes.
//
// The pages in docs/site/docs are what a user reads and, embedded in the
// binary, what an agent loads to answer questions about wopr, so a stale claim there is
// not cosmetic: it sends both down a path that no longer exists. Prose intent
// cannot be machine-checked, but the enumerable claims can be, and those are the
// ones that rot silently as commands and settings move.
//
// Every check derives its expected set from the production source rather than a
// list maintained here, so the gate cannot drift from the thing it guards.
package docsdrift

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/alexrudloff/wopr/internal/codingagent"
)

// contentDir holds the user documentation bundle.
const contentDir = "../../docs/site/docs"

func docFiles(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(contentDir)
	if err != nil {
		t.Fatalf("read docs bundle: %v", err)
	}
	out := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(contentDir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		out[entry.Name()] = string(data)
	}
	if len(out) == 0 {
		t.Fatal("docs bundle is empty; the gate would pass vacuously")
	}
	return out
}

// Slash commands are matched only inside backticks. A bare "/foo" in prose
// collides with paths and regexes, which would make the gate noisy enough to be
// switched off.
var backtickedSlash = regexp.MustCompile("`(/[a-z][a-z0-9-]*)(?: [^`]*)?`")

func builtinNames(t *testing.T) (all map[string]bool, listable []string) {
	t.Helper()
	all = map[string]bool{}
	for _, cmd := range codingagent.BuiltinSlashCommands() {
		all["/"+cmd.Name] = true
		for _, alias := range cmd.Aliases {
			all["/"+alias] = true
		}
		if !cmd.Hidden {
			listable = append(listable, "/"+cmd.Name)
		}
	}
	sort.Strings(listable)
	return all, listable
}

// A documented command that does not exist is the worse of the two failures: the
// reader types it and it does nothing.
func TestDocumentedSlashCommandsExist(t *testing.T) {
	known, _ := builtinNames(t)
	// Commands the docs name in order to say they do not exist. wopr has neither,
	// and readers arrive expecting both, so the absence is worth stating.
	documentedAsAbsent := map[string]string{
		"/exit":  "WOPR has no /exit; use /quit",
		"/clear": "WOPR has no /clear; app.clear is a keybinding",
	}
	// Commands that come from prompts rather than the builtin registry. Each
	// entry records its source, so an obsolete one is visible.
	fromPrompts := map[string]string{
		"/review":    "built-in review prompt (internal/codingagent/interactive_route.go)",
		"/audit":     "built-in review prompt (internal/codingagent/interactive_route.go)",
		"/debt":      "built-in review prompt (internal/codingagent/interactive_route.go)",
		"/component": "example prompt template a user creates (prompt-templates.md)",
	}
	for name, body := range docFiles(t) {
		for _, match := range backtickedSlash.FindAllStringSubmatch(body, -1) {
			cmd := match[1]
			if known[cmd] || fromPrompts[cmd] != "" || documentedAsAbsent[cmd] != "" {
				continue
			}
			t.Errorf("%s documents %s, which is not a builtin command and has no "+
				"recorded source; remove it or record where it comes from", name, cmd)
		}
	}
}

// The reverse direction finds the silent gap: a shipped command nobody wrote
// down. /help lists it, so a user sees it and finds nothing in the docs.
func TestListableSlashCommandsAreDocumented(t *testing.T) {
	_, listable := builtinNames(t)
	var joined strings.Builder
	for _, body := range docFiles(t) {
		joined.WriteString(body)
	}
	all := joined.String()

	documentedCmds := map[string]bool{}
	for _, match := range backtickedSlash.FindAllStringSubmatch(all, -1) {
		documentedCmds[match[1]] = true
	}
	documented := func(_ string, cmd string) bool { return documentedCmds[cmd] }

	var undocumented []string
	for _, cmd := range listable {
		if !documented(all, cmd) {
			undocumented = append(undocumented, cmd)
		}
	}
	if len(undocumented) > 0 {
		t.Errorf("%d command(s) ship in /help but appear nowhere in the docs bundle:\n  %s",
			len(undocumented), strings.Join(undocumented, "\n  "))
	}
}

// A page nothing links to is a page nobody finds. index.md is the entry point
// the system prompt names, so every page must be reachable from it by links;
// the offline copy has no site navigation.
func TestEveryPageIsReachableFromTheIndex(t *testing.T) {
	files := docFiles(t)
	if _, ok := files["index.md"]; !ok {
		t.Fatal("docs have no index.md")
	}
	seen := map[string]bool{"index.md": true}
	queue := []string{"index.md"}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		for _, match := range docLink.FindAllStringSubmatch(files[name], -1) {
			if _, ok := files[match[1]]; ok && !seen[match[1]] {
				seen[match[1]] = true
				queue = append(queue, match[1])
			}
		}
	}
	for name := range files {
		if !seen[name] {
			t.Errorf("%s is not reachable by links from index.md", name)
		}
	}
}

var docLink = regexp.MustCompile(`\]\(([A-Za-z0-9_.-]+\.md)(?:#[^)]*)?\)`)

func TestInternalLinksResolve(t *testing.T) {
	files := docFiles(t)
	for name, body := range files {
		for _, match := range docLink.FindAllStringSubmatch(body, -1) {
			if _, ok := files[match[1]]; !ok {
				t.Errorf("%s links %s, which is not in the bundle", name, match[1])
			}
		}
	}
}

// Renaming a command and leaving the old name in the docs is the most common way
// this bundle goes stale, and the reader has no way to tell which is current.
func TestNoRetiredCommandNamesSurvive(t *testing.T) {
	retired := map[string]string{
		"wopr sdk":       "replaced by `wopr reload`",
		"wopr --profile": "removed",
		"wopr profile":   "removed",
		"`/profile`":     "removed",
		// WOPR_PROFILE and WOPR_PROFILE_DIR are live Go profiling variables.
		"WOPR_PROFILE_NAME": "removed",
		"WOPR_PROFILE_PATH": "removed",
		"profile.yaml":      "removed",
		"wopr cartridge":    "Cartridges were removed",
		"wopr install":      "the package manager was removed",
		"wopr extension ":   "the extension SDKs were removed",
		"--no-extensions":   "the extension host was removed",
	}
	for name, body := range docFiles(t) {
		for phrase, replacement := range retired {
			if strings.Contains(body, phrase) {
				t.Errorf("%s still references %q (%s)", name, phrase, replacement)
			}
		}
	}
}
