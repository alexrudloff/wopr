package codingagent

import (
	"cmp"
	"regexp"
	"strconv"
	"strings"
)

// `/changelog` slash command and parser.
//
// The parser scans for `## ` headers, parse the version triple from the first one
// matching `## [x.y.z]` or `## x.y.z`, accumulate body lines until the
// next `## ` line or EOF. Non-version headers (e.g. `## [Unreleased]`)
// reset the accumulator without recording an entry.
//
// `## [Unreleased]` is intentionally skipped: its content is by
// definition not in any released version yet, and surfacing it in the
// /changelog viewer would mislead the user about what version they're
// running.

// ChangelogEntry is one parsed `## [x.y.z]` block.
type ChangelogEntry struct {
	Major   int
	Minor   int
	Patch   int
	Content string // includes the `## [x.y.z]` header line itself, trimmed
}

// versionHeaderRE matches a `## [x.y.z]` or `## x.y.z` version header.
var versionHeaderRE = regexp.MustCompile(`^##\s+\[?(\d+)\.(\d+)\.(\d+)\]?`)

// ParseChangelog walks the markdown content of a CHANGELOG.md and
// returns one ChangelogEntry per `## [x.y.z]`-style header. Order is
// the order found in the file (typical convention: newest first).
// Malformed entries are silently skipped.
func ParseChangelog(content string) []ChangelogEntry {
	lines := strings.Split(content, "\n")

	var entries []ChangelogEntry
	var currentLines []string
	var currentVersion *ChangelogEntry

	flush := func() {
		if currentVersion != nil && len(currentLines) > 0 {
			entry := *currentVersion
			entry.Content = strings.TrimSpace(strings.Join(currentLines, "\n"))
			entries = append(entries, entry)
		}
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			flush()
			match := versionHeaderRE.FindStringSubmatch(line)
			if match != nil {
				major, _ := strconv.Atoi(match[1])
				minor, _ := strconv.Atoi(match[2])
				patch, _ := strconv.Atoi(match[3])
				currentVersion = &ChangelogEntry{Major: major, Minor: minor, Patch: patch}
				currentLines = []string{line}
			} else {
				// Non-version header (e.g. `## [Unreleased]`): reset.
				currentVersion = nil
				currentLines = nil
			}
			continue
		}
		if currentVersion != nil {
			currentLines = append(currentLines, line)
		}
	}
	flush()

	return entries
}

// FormatChangelogForChat takes parsed entries and produces the markdown
// block /changelog appends to the chat:
//   - reverse the entries (so they read oldest-at-top, newest-at-bottom
//     within the inline block),
//   - join their content with two newlines,
//   - wrap in a bold "What's New" header and horizontal-rule "borders"
//     (wopr's markdown renderer paints `---` as a separator line).
//
// Empty input returns the "No changelog entries found."
// fallback so a user with a missing/empty CHANGELOG sees a sensible
// message instead of an empty bordered block.
func FormatChangelogForChat(entries []ChangelogEntry) string {
	if len(entries) == 0 {
		return "No changelog entries found."
	}
	parts := make([]string, len(entries))
	for i, e := range entries {
		parts[len(entries)-1-i] = e.Content
	}
	return "---\n\n**What's New**\n\n" + strings.Join(parts, "\n\n") + "\n\n---"
}

// compareVersions compares two ChangelogEntry values by version number.
func compareVersions(v1, v2 ChangelogEntry) int {
	return cmp.Or(cmp.Compare(v1.Major, v2.Major), cmp.Compare(v1.Minor, v2.Minor), cmp.Compare(v1.Patch, v2.Patch))
}

// GetNewEntries returns the subset of entries whose version is strictly
// newer than sinceVersion (a "x.y.z" string).
func GetNewEntries(entries []ChangelogEntry, sinceVersion string) []ChangelogEntry {
	parts := strings.SplitN(sinceVersion, ".", 3)
	parseNum := func(s string) int {
		n, _ := strconv.Atoi(s)
		return n
	}
	var last ChangelogEntry
	if len(parts) >= 3 {
		last.Major = parseNum(parts[0])
		last.Minor = parseNum(parts[1])
		last.Patch = parseNum(parts[2])
	}
	var newer []ChangelogEntry
	for _, e := range entries {
		if compareVersions(e, last) > 0 {
			newer = append(newer, e)
		}
	}
	return newer
}

// recordChangelogVersion updates the recorded changelog version and returns the
// entries to show as the "What's New" banner. A fresh install (no recorded
// version) records the version and shows nothing, and so does a recorded
// version newer than this one (a downgrade). A version bump with new
// changelog entries records the version and returns them. A version bump with
// no new entries neither records nor shows anything.
func recordChangelogVersion(sm *SettingsManager, appVersion string, allEntries []ChangelogEntry) []ChangelogEntry {
	lastSeen := sm.Get().LastChangelogVersion
	if lastSeen == "" {
		_ = sm.SetLastChangelogVersion(appVersion)
		return nil
	}
	if lastSeen == appVersion {
		return nil
	}
	if CompareVersions(lastSeen, appVersion) > 0 {
		_ = sm.SetLastChangelogVersion(appVersion)
		return nil
	}
	newEntries := GetNewEntries(allEntries, lastSeen)
	if len(newEntries) == 0 {
		return nil
	}
	_ = sm.SetLastChangelogVersion(appVersion)
	return newEntries
}
