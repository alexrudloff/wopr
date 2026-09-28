// Package resources discovers skills, prompt templates, and themes from
// conventional resource directories and configured settings entries.
package resources

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/internal/codingagent/frontmatter"
)

// Kind identifies a discoverable resource class.
type Kind string

const (
	Skills  Kind = "skills"
	Prompts Kind = "prompts"
	Themes  Kind = "themes"
)

// Name returns the public name of one resource: a skill directory's declared
// frontmatter name or its directory name, otherwise the file name without its
// extension.
func Name(kind Kind, resourcePath string) (string, error) {
	if kind == Skills {
		data, err := os.ReadFile(filepath.Join(resourcePath, "SKILL.md"))
		if err != nil {
			return "", fmt.Errorf("read skill %s: %w", resourcePath, err)
		}
		if declared := frontmatter.Parse(string(data)).String("name"); declared != "" {
			return declared, nil
		}
		return filepath.Base(resourcePath), nil
	}
	base := filepath.Base(resourcePath)
	return strings.TrimSuffix(base, filepath.Ext(base)), nil
}

// Collect expands files or conventional resource directories for kind.
func Collect(paths []string, kind Kind) []string {
	files := make([]string, 0)
	for _, resourcePath := range paths {
		if resourcePath == "" {
			continue
		}
		info, err := os.Stat(resourcePath)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			files = append(files, resourcePath)
			continue
		}
		switch kind {
		case Skills:
			if _, err := os.Stat(filepath.Join(resourcePath, "SKILL.md")); err == nil {
				files = append(files, resourcePath)
			} else {
				files = append(files, DiscoverSkillDirs(resourcePath)...)
			}
		case Prompts:
			files = append(files, walkFilesWithSuffix(resourcePath, ".md")...)
		case Themes:
			files = append(files, walkFilesWithSuffix(resourcePath, ".json")...)
		}
	}
	return Deduplicate(files)
}

// DiscoverAutomatic returns resources in one conventional resource directory.
func DiscoverAutomatic(dir string, kind Kind) []string {
	switch kind {
	case Prompts:
		return discoverFlatFiles(dir, ".md")
	case Themes:
		return discoverFlatFiles(dir, ".json")
	case Skills:
		return DiscoverSkillDirs(dir)
	default:
		return nil
	}
}

// ResolveConfigured expands configured paths and applies their include/exclude
// patterns relative to baseDir.
func ResolveConfigured(entries []string, baseDir string, kind Kind) []string {
	plain, patterns := SplitPatterns(entries)
	resolved := make([]string, 0, len(plain))
	for _, entry := range plain {
		if filepath.IsAbs(entry) {
			resolved = append(resolved, entry)
		} else {
			resolved = append(resolved, filepath.Clean(filepath.Join(baseDir, entry)))
		}
	}
	return ApplyPatterns(Collect(resolved, kind), patterns, baseDir, kind)
}

// FilterAutomatic applies configured overrides to automatically discovered paths.
func FilterAutomatic(paths, overrides []string, baseDir string, kind Kind) []string {
	filtered := make([]string, 0, len(paths))
	for _, resourcePath := range paths {
		if EnabledByOverrides(resourcePath, overrides, baseDir, kind) {
			filtered = append(filtered, resourcePath)
		}
	}
	return filtered
}

// SplitPatterns separates literal entries from glob and override entries.
func SplitPatterns(entries []string) (plain, patterns []string) {
	for _, entry := range entries {
		if entry == "" {
			continue
		}
		if strings.HasPrefix(entry, "!") || strings.HasPrefix(entry, "+") || strings.HasPrefix(entry, "-") || strings.Contains(entry, "*") || strings.Contains(entry, "?") {
			patterns = append(patterns, entry)
		} else {
			plain = append(plain, entry)
		}
	}
	return plain, patterns
}

// ApplyPatterns applies allowlist, exclude, force-include, and force-exclude
// patterns without changing input order.
func ApplyPatterns(allPaths, patterns []string, baseDir string, kind Kind) []string {
	includes := make([]string, 0)
	excludes := make([]string, 0)
	forceIncludes := make([]string, 0)
	forceExcludes := make([]string, 0)
	for _, pattern := range patterns {
		switch {
		case strings.HasPrefix(pattern, "+"):
			forceIncludes = append(forceIncludes, pattern[1:])
		case strings.HasPrefix(pattern, "-"):
			forceExcludes = append(forceExcludes, pattern[1:])
		case strings.HasPrefix(pattern, "!"):
			excludes = append(excludes, pattern[1:])
		default:
			includes = append(includes, pattern)
		}
	}
	result := make([]string, 0, len(allPaths))
	if len(includes) == 0 {
		result = append(result, allPaths...)
	} else {
		for _, resourcePath := range allPaths {
			if matchesAnyPattern(resourcePath, includes, baseDir, kind) {
				result = append(result, resourcePath)
			}
		}
	}
	if len(excludes) > 0 {
		result = slices.DeleteFunc(result, func(resourcePath string) bool {
			return matchesAnyPattern(resourcePath, excludes, baseDir, kind)
		})
	}
	if len(forceIncludes) > 0 {
		for _, resourcePath := range allPaths {
			if !slices.Contains(result, resourcePath) && matchesAnyExactPattern(resourcePath, forceIncludes, baseDir, kind) {
				result = append(result, resourcePath)
			}
		}
	}
	if len(forceExcludes) > 0 {
		result = slices.DeleteFunc(result, func(resourcePath string) bool {
			return matchesAnyExactPattern(resourcePath, forceExcludes, baseDir, kind)
		})
	}
	return result
}

// EnabledByOverrides reports whether one automatically discovered path remains
// active after configured overrides.
func EnabledByOverrides(resourcePath string, patterns []string, baseDir string, kind Kind) bool {
	var excludes, forceIncludes, forceExcludes []string
	for _, pattern := range patterns {
		switch {
		case strings.HasPrefix(pattern, "!"):
			excludes = append(excludes, pattern[1:])
		case strings.HasPrefix(pattern, "+"):
			forceIncludes = append(forceIncludes, pattern[1:])
		case strings.HasPrefix(pattern, "-"):
			forceExcludes = append(forceExcludes, pattern[1:])
		}
	}
	enabled := len(excludes) == 0 || !matchesAnyPattern(resourcePath, excludes, baseDir, kind)
	if len(forceIncludes) > 0 && matchesAnyExactPattern(resourcePath, forceIncludes, baseDir, kind) {
		enabled = true
	}
	if len(forceExcludes) > 0 && matchesAnyExactPattern(resourcePath, forceExcludes, baseDir, kind) {
		enabled = false
	}
	return enabled
}

// DiscoverSkillDirs recursively discovers skill roots and root-level Markdown
// skills. A directory containing SKILL.md is one skill and is not traversed
// further. Symlinked files and directories are followed, while hidden entries,
// node_modules, and paths excluded by .gitignore, .ignore, or .fdignore are
// skipped.
func DiscoverSkillDirs(dir string) []string {
	return discoverSkillDirs(dir, false)
}

// DiscoverAgentSkillDirs applies the cross-agent `.agents/skills` convention:
// root Markdown files are documentation, while nested Markdown files are
// standalone skills.
func DiscoverAgentSkillDirs(dir string) []string {
	return discoverSkillDirs(dir, true)
}

func discoverSkillDirs(dir string, agentsMode bool) []string {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil
	}
	var paths []string
	seenDirs := make(map[string]struct{})
	var walk func(string, []skillIgnoreRule)
	walk = func(current string, rules []skillIgnoreRule) {
		canonical, err := filepath.EvalSymlinks(current)
		if err != nil {
			return
		}
		if _, seen := seenDirs[canonical]; seen {
			return
		}
		seenDirs[canonical] = struct{}{}
		rules = appendSkillIgnoreRules(rules, current, root)
		entries, err := os.ReadDir(current)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if entry.Name() != "SKILL.md" {
				continue
			}
			fullPath := filepath.Join(current, entry.Name())
			info, err := os.Stat(fullPath)
			if err == nil && info.Mode().IsRegular() && !skillPathIgnored(fullPath, false, root, rules) {
				paths = append(paths, current)
				return
			}
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") || entry.Name() == "node_modules" {
				continue
			}
			fullPath := filepath.Join(current, entry.Name())
			info, err := os.Stat(fullPath)
			if err != nil {
				continue
			}
			if skillPathIgnored(fullPath, info.IsDir(), root, rules) {
				continue
			}
			switch {
			case info.IsDir():
				walk(fullPath, rules)
			case info.Mode().IsRegular() && strings.HasSuffix(entry.Name(), ".md") && ((agentsMode && current != root) || (!agentsMode && current == root)):
				paths = append(paths, fullPath)
			}
		}
	}
	walk(root, nil)
	return paths
}

type skillIgnoreRule struct {
	pattern string
	negated bool
}

func appendSkillIgnoreRules(rules []skillIgnoreRule, dir, root string) []skillIgnoreRule {
	relDir, err := filepath.Rel(root, dir)
	if err != nil {
		return rules
	}
	prefix := ""
	if relDir != "." {
		prefix = filepath.ToSlash(relDir) + "/"
	}
	for _, name := range []string{".gitignore", ".ignore", ".fdignore"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		for line := range strings.SplitSeq(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			negated := strings.HasPrefix(trimmed, "!")
			if negated {
				trimmed = strings.TrimPrefix(trimmed, "!")
			}
			trimmed = strings.TrimPrefix(trimmed, "/")
			rules = append(rules, skillIgnoreRule{pattern: prefix + trimmed, negated: negated})
		}
	}
	return rules
}

func skillPathIgnored(candidate string, directory bool, root string, rules []skillIgnoreRule) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	ignored := false
	for _, rule := range rules {
		pattern := strings.TrimSuffix(rule.pattern, "/")
		matched := skillIgnoreMatch(pattern, rel)
		if directory && !matched {
			matched = skillIgnoreMatch(pattern, rel+"/")
		}
		if matched {
			ignored = !rule.negated
		}
	}
	return ignored
}

func skillIgnoreMatch(pattern, candidate string) bool {
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "/") {
		for part := range strings.SplitSeq(candidate, "/") {
			if matched, _ := path.Match(pattern, part); matched {
				return true
			}
		}
	}
	re := regexp.QuoteMeta(pattern)
	re = strings.ReplaceAll(re, `\*\*`, `.*`)
	re = strings.ReplaceAll(re, `\*`, `[^/]*`)
	re = strings.ReplaceAll(re, `\?`, `[^/]`)
	matched, _ := regexp.MatchString(`^`+re+`(?:/.*)?$`, candidate)
	return matched
}

// Deduplicate removes empty and repeated strings without changing order.
func Deduplicate(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func discoverFlatFiles(dir, suffix string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var paths []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || entry.IsDir() || !strings.HasSuffix(entry.Name(), suffix) {
			continue
		}
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	return paths
}

func walkFilesWithSuffix(root, suffix string) []string {
	out := make([]string, 0)
	_ = filepath.Walk(root, func(filePath string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.IsDir() {
			if filePath != root && strings.HasPrefix(filepath.Base(filePath), ".") {
				return filepath.SkipDir
			}
			if filepath.Base(filePath) == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(filePath, suffix) {
			out = append(out, filePath)
		}
		return nil
	})
	return out
}

func matchesAnyPattern(filePath string, patterns []string, baseDir string, kind Kind) bool {
	relative := filepath.ToSlash(mustRel(baseDir, filePath))
	name := filepath.Base(filePath)
	absolute := filepath.ToSlash(filePath)
	isSkill := kind == Skills
	parentDir := filepath.Dir(filePath)
	parentRelative := filepath.ToSlash(mustRel(baseDir, parentDir))
	parentName := filepath.Base(parentDir)
	parentAbsolute := filepath.ToSlash(parentDir)
	for _, patternValue := range patterns {
		normalized := filepath.ToSlash(patternValue)
		if matchGlob(relative, normalized) || matchGlob(name, normalized) || matchGlob(absolute, normalized) {
			return true
		}
		if isSkill && (matchGlob(parentRelative, normalized) || matchGlob(parentName, normalized) || matchGlob(parentAbsolute, normalized)) {
			return true
		}
	}
	return false
}

func matchesAnyExactPattern(filePath string, patterns []string, baseDir string, kind Kind) bool {
	relative := filepath.ToSlash(mustRel(baseDir, filePath))
	absolute := filepath.ToSlash(filePath)
	isSkill := kind == Skills
	parentDir := filepath.Dir(filePath)
	parentRelative := filepath.ToSlash(mustRel(baseDir, parentDir))
	parentAbsolute := filepath.ToSlash(parentDir)
	for _, patternValue := range patterns {
		normalized := normalizeExactPattern(patternValue)
		if normalized == relative || normalized == absolute {
			return true
		}
		if isSkill && (normalized == parentRelative || normalized == parentAbsolute) {
			return true
		}
	}
	return false
}

func normalizeExactPattern(patternValue string) string {
	if after, ok := strings.CutPrefix(patternValue, "./"); ok {
		return filepath.ToSlash(after)
	}
	if after, ok := strings.CutPrefix(patternValue, ".\\"); ok {
		return filepath.ToSlash(after)
	}
	return filepath.ToSlash(patternValue)
}

func mustRel(baseDir, target string) string {
	relative, err := filepath.Rel(baseDir, target)
	if err != nil {
		return target
	}
	return relative
}

func matchGlob(name, patternValue string) bool {
	matched, err := path.Match(patternValue, name)
	if err != nil {
		return name == patternValue
	}
	return matched || name == patternValue
}
