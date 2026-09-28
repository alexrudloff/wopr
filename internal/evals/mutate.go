package evals

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Mutation tasks take one real Go file, apply one mechanical bug whose
// inverse is known, and describe it in plain English. A run passes when the
// file's gofmt output hashes to the original's, so any fix that restores
// the code counts, whatever its whitespace. Generation is seeded and
// reproducible.

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
	var ids []string
	used := map[[2]string]bool{}
	for _, c := range pool {
		if len(ids) == count {
			break
		}
		if used[[2]string{c.path, c.kind}] {
			continue
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
		rel, _ := filepath.Rel(repoRoot, c.path)
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
