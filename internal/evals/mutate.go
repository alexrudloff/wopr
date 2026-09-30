package evals

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Mutation tasks take one real Go file, apply one mechanical bug whose
// inverse is known, and describe it in plain English. When the file's
// package has tests, the task ships a snapshot of the whole module (so the
// package builds and its tests run offline) and passes when the package's
// tests pass with its test files unchanged; a mutation is kept only if those
// tests catch it. A package without tests falls back to a one-file task
// judged by the file's gofmt output, and never gets a removed guard, which
// nothing could check. Generation is seeded and reproducible.

var (
	funcRE  = regexp.MustCompile(`^func (?:\([^)]*\) )?(\w+)`)
	plainRE = regexp.MustCompile("^[^\"`/]*$") // no strings, runes, or comments
	ifRE    = regexp.MustCompile(`^\s*if .+ \{$`)
	retRE   = regexp.MustCompile(`^\s*return\b`)
	closeRE = regexp.MustCompile(`^\s*\}$`)
)

// mutation is one applicable bug: replace span lines at a line with
// replacement.
type mutation struct {
	replacement []string
	span        int
	desc, hint  string
}

type mutator struct {
	kind string
	find func(lines []string, i int) (mutation, bool)
}

func swap(kind, old, repl, desc, hint string) mutator {
	return mutator{kind: kind, find: func(lines []string, i int) (mutation, bool) {
		line := lines[i]
		if plainRE.MatchString(line) && strings.Count(line, old) == 1 && !strings.HasPrefix(strings.TrimSpace(line), "func ") {
			return mutation{replacement: []string{strings.Replace(line, old, repl, 1)}, span: 1, desc: desc, hint: hint}, true
		}
		return mutation{}, false
	}}
}

// mutators are the bug kinds, in a fixed order so generation is
// reproducible.
var mutators = []mutator{
	swap("invert-equality", " == ", " != ", "A comparison operator was inverted.", "Restore the original comparison."),
	swap("invert-inequality", " != ", " == ", "A comparison operator was inverted.", "Restore the original comparison."),
	swap("flip-boolean", "return true", "return false", "A boolean literal was flipped.", "Restore the original boolean value."),
	swap("off-by-one", " < len(", " <= len(", "An off-by-one error was introduced in a bound.", "Restore the original bound."),
	swap("swap-logic", " && ", " || ", "A logical operator was swapped.", "Restore the original logical operator."),
	{kind: "drop-guard", find: func(lines []string, i int) (mutation, bool) {
		if i+2 < len(lines) && ifRE.MatchString(lines[i]) && plainRE.MatchString(lines[i]) && retRE.MatchString(lines[i+1]) && closeRE.MatchString(lines[i+2]) {
			return mutation{span: 3, desc: "A guard clause (early return) was removed.", hint: "Restore the missing guard clause (if statement with early return)."}, true
		}
		return mutation{}, false
	}},
}

type candidate struct {
	path, kind, fn string
	line           int
	mutation
}

func candidates(path string, src []byte) []candidate {
	lines := strings.Split(string(src), "\n")
	var out []candidate
	fn := ""
	for i, line := range lines {
		if m := funcRE.FindStringSubmatch(line); m != nil {
			fn = m[1]
		}
		if fn == "" {
			continue
		}
		for _, mu := range mutators {
			if hit, ok := mu.find(lines, i); ok {
				out = append(out, candidate{path: path, kind: mu.kind, fn: fn, line: i, mutation: hit})
			}
		}
	}
	return out
}

// corpusFiles lists the hand-written, non-test Go files under roots, in
// lexical order.
func corpusFiles(repoRoot string, roots []string) ([]string, error) {
	var files []string
	for _, root := range roots {
		err := filepath.WalkDir(filepath.Join(repoRoot, root), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			head, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !strings.Contains(string(head[:min(len(head), 2000)]), "Code generated") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

// Mutate writes count mutation tasks under out, drawn with seed from the Go
// files under roots (relative to repoRoot), and returns their ids. Each task
// is kept only if its check fails as mutated and passes restored.
func Mutate(repoRoot string, roots []string, out string, count int, seed uint64) ([]string, error) {
	files, err := corpusFiles(repoRoot, roots)
	if err != nil {
		return nil, err
	}
	var pool []candidate
	sources := map[string][]byte{}
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		sources[path] = src
		pool = append(pool, candidates(path, src)...)
	}
	if absOut, err := filepath.Abs(out); err == nil {
		if absRepo, err := filepath.Abs(repoRoot); err == nil && (absOut == absRepo || strings.HasPrefix(absOut, absRepo+string(filepath.Separator))) {
			return nil, errInsideSource
		}
	}
	rng := rand.New(rand.NewPCG(seed, 0))
	rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	if err := os.RemoveAll(out); err != nil {
		return nil, err
	}
	// The mutated files do not compile outside their packages; a go.mod
	// makes out its own module, so `go build ./...` in a checkout skips it.
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(out, "go.mod"), []byte("module mutationtasks\n\ngo 1.26\n"), 0o644); err != nil {
		return nil, err
	}
	// scratch is one snapshot of the module that candidates are tried in.
	scratch, err := os.MkdirTemp("", "wopr-eval-mutate-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	if err := moduleSnapshot(repoRoot, scratch); err != nil {
		return nil, err
	}
	basePasses := map[string]bool{}
	var ids []string
	used := map[[2]string]bool{}
	tries := 0
	for _, c := range pool {
		if len(ids) == count || tries >= count*12 {
			break
		}
		if used[[2]string{c.path, c.kind}] {
			continue
		}
		rel, _ := filepath.Rel(repoRoot, c.path)
		if tests := packageTests(filepath.Dir(c.path)); len(tests) > 0 {
			tries++
			pkg := filepath.ToSlash(filepath.Dir(rel))
			if _, ok := basePasses[pkg]; !ok {
				basePasses[pkg] = packageTestsPass(scratch, pkg)
			}
			if !basePasses[pkg] {
				continue
			}
			id, err := testedTask(scratch, out, rel, pkg, tests, sources[c.path], c, seed)
			if err != nil {
				return nil, err
			}
			if id != "" {
				used[[2]string{c.path, c.kind}] = true
				ids = append(ids, id)
			}
			continue
		}
		if c.kind == "drop-guard" {
			continue // without tests, only the exact text could judge it
		}
		original := sources[c.path]
		lines := strings.Split(string(original), "\n")
		mutated := strings.Join(append(append(append([]string{}, lines[:c.line]...), c.replacement...), lines[c.line+c.span:]...), "\n")
		want, err := GofmtHash(original)
		if err != nil {
			continue
		}
		got, err := GofmtHash([]byte(mutated))
		if err != nil || got == want {
			continue // The mutation must still parse and must change the code.
		}
		used[[2]string{c.path, c.kind}] = true
		name := filepath.Base(c.path)
		id := fmt.Sprintf("%s-%s-%d", c.kind, strings.TrimSuffix(name, ".go"), c.line+1)
		dir := filepath.Join(out, id)
		if err := os.MkdirAll(filepath.Join(dir, "files"), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, "files", name), []byte(mutated), 0o644); err != nil {
			return nil, err
		}
		prompt := fmt.Sprintf("# Fix the bug in `%s`\n\n%s\nThe issue is in the `%s` function.\n%s", name, c.desc, c.fn, c.hint)
		toml := fmt.Sprintf("# Generated by wopr-eval mutate --seed %d from %s line %d.\nprompt = %s\ntimeout = 300\n\n[gofmt_sha256]\n%s = %s\n",
			seed, filepath.ToSlash(rel), c.line+1, strconv.Quote(prompt), strconv.Quote(name), strconv.Quote(want))
		if err := os.WriteFile(filepath.Join(dir, "task.toml"), []byte(toml), 0o644); err != nil {
			return nil, err
		}
		if !verified(Task{GofmtSHA256: map[string]string{name: want}}, filepath.Join(dir, "files"), name, original) {
			if err := os.RemoveAll(dir); err != nil {
				return nil, err
			}
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// verified reports whether the task fails on the mutated files and passes
// once the original is restored.
func verified(t Task, files, name string, original []byte) bool {
	if RunCheck(t, files).Passed {
		return false
	}
	restored, err := os.MkdirTemp("", "wopr-eval-verify-")
	if err != nil {
		return false
	}
	defer func() { _ = os.RemoveAll(restored) }()
	if os.WriteFile(filepath.Join(restored, name), original, 0o600) != nil {
		return false
	}
	return RunCheck(t, restored).Passed
}

// moduleSnapshot writes the committed tree of the module at repoRoot into
// dir.
func moduleSnapshot(repoRoot, dir string) error {
	cmd := exec.Command("sh", "-c", `git -C "$1" archive --format=tar HEAD | tar -x -C "$2"`, "sh", repoRoot, dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("snapshot %s: %w: %s", repoRoot, err, out)
	}
	return nil
}

// packageTests lists the _test.go files in dir.
func packageTests(dir string) []string {
	matches, _ := filepath.Glob(filepath.Join(dir, "*_test.go"))
	return matches
}

// packageTestCheck runs the package's tests offline, from the module cache.
func packageTestCheck(pkg string) string {
	return "GOFLAGS=-mod=readonly GOPROXY=off GOWORK=off go test -count=1 ./" + pkg
}

func packageTestsPass(root, pkg string) bool {
	return RunCheck(Task{Check: packageTestCheck(pkg)}, root).Passed
}

// testedTask tries c in the scratch snapshot: kept only when the package's
// tests fail with the mutation, it becomes a task whose files are the
// mutated snapshot. It returns the task id, or "" when the tests don't
// catch the mutation.
func testedTask(scratch, out, rel, pkg string, tests []string, original []byte, c candidate, seed uint64) (string, error) {
	lines := strings.Split(string(original), "\n")
	mutated := strings.Join(append(append(append([]string{}, lines[:c.line]...), c.replacement...), lines[c.line+c.span:]...), "\n")
	target := filepath.Join(scratch, rel)
	if err := os.WriteFile(target, []byte(mutated), 0o644); err != nil {
		return "", err
	}
	defer func() { _ = os.WriteFile(target, original, 0o644) }()
	if packageTestsPass(scratch, pkg) {
		return "", nil
	}
	name := filepath.Base(rel)
	id := fmt.Sprintf("%s-%s-%d", c.kind, strings.TrimSuffix(name, ".go"), c.line+1)
	dir := filepath.Join(out, id)
	if err := CopyTree(scratch, filepath.Join(dir, "files")); err != nil {
		return "", err
	}
	var protected []string
	for _, test := range tests {
		protected = append(protected, strconv.Quote(filepath.ToSlash(filepath.Join(pkg, filepath.Base(test)))))
	}
	prompt := fmt.Sprintf("# Fix the bug in `%s`\n\n%s\nThe issue is in the `%s` function.\n%s\n`go test ./%s` shows the failure.",
		filepath.ToSlash(rel), c.desc, c.fn, c.hint, pkg)
	toml := fmt.Sprintf("# Generated by wopr-eval mutate --seed %d from %s line %d.\nprompt = %s\ncheck = %s\nprotected = [%s]\ntimeout = 600\n",
		seed, filepath.ToSlash(rel), c.line+1, strconv.Quote(prompt), strconv.Quote(packageTestCheck(pkg)), strings.Join(protected, ", "))
	if err := os.WriteFile(filepath.Join(dir, "task.toml"), []byte(toml), 0o644); err != nil {
		return "", err
	}
	return id, nil
}

// errInsideSource refuses mutation output inside the source tree, where a
// model could read the original next to the mutation.
var errInsideSource = errors.New("mutation tasks must be written outside the source tree")
