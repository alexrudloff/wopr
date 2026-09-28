package tui

// @<file> autocomplete using fd for fuzzy file search.
//
// wopr implements both fd-based fuzzy search (for @prefix) and os.ReadDir
// path completion (for naked paths when fd is unavailable).

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// pathDelimiters defines characters that separate path tokens in the
// editor buffer.
var pathDelimiters = map[byte]bool{
	' ':  true,
	'\t': true,
	'"':  true,
	'\'': true,
	'=':  true,
}

// CombinedProvider handles both slash-command and @-file autocomplete.
type CombinedProvider struct {
	slash   *SlashOnlyProvider
	baseDir string // project working directory
	fdPath  string // path to fd binary ("" if not available)

	// asyncFileSearch defers the @-file fd subprocess off the synchronous
	// GetSuggestions path. A deep `--follow --hidden` tree
	// walk (e.g. @~/<rare-name>) can take many seconds, so running it on the
	// input thread freezes typing. When enabled, GetSuggestions returns nil
	// for @-fd queries and the editor runs FileSearchTask off-thread with
	// cancellation. Off by default so direct/sync callers (tests, print mode)
	// keep the synchronous contract.
	asyncFileSearch bool
}

// SetAsyncFileSearch enables deferred, cancellable fd-backed @-file search.
// Interactive mode enables this so a slow tree walk cannot block keystrokes.
func (p *CombinedProvider) SetAsyncFileSearch(v bool) { p.asyncFileSearch = v }

var autocompleteCollator = collate.New(language.Und)

// NewCombinedProvider creates a provider that handles both slash commands
// and @-file autocomplete. If fdPath is empty, @-file completion uses
// os.ReadDir fallback (prefix match in target directory only, no fuzzy
// tree walk).
func NewCombinedProvider(cmds []SlashCommand, baseDir, fdPath string) *CombinedProvider {
	return &CombinedProvider{
		slash:   NewSlashOnlyProvider(cmds),
		baseDir: baseDir,
		fdPath:  fdPath,
	}
}

// GetSuggestions implements AutocompleteProvider. Tries @ prefix first,
// then slash. Naked-path completion is force-only:
// the popup opens on Tab, not while typing `./foo`.
func (p *CombinedProvider) GetSuggestions(lines []string, cursorLine, cursorCol int) *AutocompleteSuggestions {
	return p.getSuggestions(lines, cursorLine, cursorCol, false)
}

// GetSuggestionsForce is triggered by Tab outside a slash-command-name context to force naked
// path completion even when the prefix doesn't look path-like yet.
func (p *CombinedProvider) GetSuggestionsForce(lines []string, cursorLine, cursorCol int) *AutocompleteSuggestions {
	return p.getSuggestions(lines, cursorLine, cursorCol, true)
}

func (p *CombinedProvider) getSuggestions(lines []string, cursorLine, cursorCol int, force bool) *AutocompleteSuggestions {
	line, cursorCol, ok := lineAtCursor(lines, cursorLine, cursorCol)
	if !ok {
		return nil
	}
	before := line[:cursorCol]

	// Check for @ prefix first.
	atPrefix := extractAtPrefix(before)
	if atPrefix != "" {
		if p.asyncFileSearch && p.fdPath != "" {
			// The editor runs the fd subprocess off the input thread via
			// FileSearchTask so a slow tree walk can't block typing.
			return nil
		}
		raw, isQuoted := parseAtPrefix(atPrefix)
		items := p.getFileSuggestions(raw, isQuoted)
		if len(items) == 0 {
			return nil
		}
		return &AutocompleteSuggestions{Items: items, Prefix: atPrefix}
	}

	// Slash-command branch.
	if !force && strings.HasPrefix(before, "/") {
		return p.slash.GetSuggestions(lines, cursorLine, cursorCol)
	}
	if strings.HasPrefix(before, "/") {
		// In force mode, still delegate to slash for /<name> popups
		// because the path branch can't fire on a leading slash anyway.
		return p.slash.GetSuggestions(lines, cursorLine, cursorCol)
	}

	// Naked path branch is force-only in the interactive shipped behavior:
	// Tab triggers file completion, while ordinary typing does not open the
	// popup for `./`/`~/` paths.
	if !force {
		return nil
	}

	pathPrefix, ok := extractPathPrefix(before, force)
	if !ok {
		return nil
	}
	raw, isQuoted := parsePathOnlyPrefix(pathPrefix)
	items := p.readdirFileSuggestions(raw, isQuoted)
	if len(items) == 0 {
		return nil
	}
	// Strip the leading "@" buildCompletionValue added so naked path
	// completions don't accidentally turn a `./` into `@./`.
	for i := range items {
		items[i].Value = strings.TrimPrefix(items[i].Value, "@")
	}
	return &AutocompleteSuggestions{Items: items, Prefix: pathPrefix}
}

// ApplyCompletion implements AutocompleteProvider. Routes to @ or slash
// completion based on prefix shape.
func (p *CombinedProvider) ApplyCompletion(lines []string, cursorLine, cursorCol int, item AutocompleteItem, prefix string) ([]string, int, int) {
	line, cursorCol, ok := lineAtCursor(lines, cursorLine, cursorCol)
	if !ok {
		return lines, cursorLine, cursorCol
	}
	if len(prefix) > cursorCol {
		return lines, cursorLine, cursorCol
	}
	beforePrefix := line[:cursorCol-len(prefix)]
	afterCursor := line[cursorCol:]

	out := make([]string, len(lines))
	copy(out, lines)

	// @ file completion: prefix starts with "@".
	// Directories don't get trailing space (user continues typing path).
	// Files get trailing space.
	if strings.HasPrefix(prefix, "@") || strings.HasPrefix(prefix, `@"`) {
		isDirectory := strings.HasSuffix(item.Label, "/")
		suffix := ""
		if !isDirectory {
			suffix = " "
		}
		// Handle quoted completion: if item.Value ends with `"` and
		// afterCursor starts with `"`, consume the existing quote.
		adjustedAfter := afterCursor
		if strings.HasSuffix(item.Value, `"`) && strings.HasPrefix(afterCursor, `"`) {
			adjustedAfter = afterCursor[1:]
		}
		newLine := beforePrefix + item.Value + suffix + adjustedAfter
		out[cursorLine] = newLine

		cursorOffset := len(item.Value)
		// For directories inside quotes, place cursor before closing quote.
		if isDirectory && strings.HasSuffix(item.Value, `"`) {
			cursorOffset = len(item.Value) - 1
		}
		return out, cursorLine, len(beforePrefix) + cursorOffset + len(suffix)
	}

	// Naked path completion: prefix is a path-like token without "@" and
	// not a slash-command name. Directories keep the cursor
	// adjacent so the user can continue typing; files don't get a
	// trailing space (only @-completions do).
	isPathPrefix := strings.HasPrefix(prefix, `"`) ||
		strings.HasPrefix(prefix, "./") ||
		strings.HasPrefix(prefix, "../") ||
		strings.HasPrefix(prefix, "~/") ||
		strings.HasPrefix(prefix, "/") ||
		strings.Contains(prefix, "/")
	if !strings.HasPrefix(prefix, "/") && isPathPrefix {
		// (slash-command names start with `/`; naked paths starting
		// with `/` are absolute paths and handled below.)
		newLine := beforePrefix + item.Value + afterCursor
		out[cursorLine] = newLine
		isDirectory := strings.HasSuffix(item.Label, "/")
		cursorOffset := len(item.Value)
		if isDirectory && strings.HasSuffix(item.Value, `"`) {
			cursorOffset = len(item.Value) - 1
		}
		return out, cursorLine, len(beforePrefix) + cursorOffset
	}
	if strings.HasPrefix(prefix, "/") && !strings.Contains(prefix[1:], "/") {
		// Slash-command name: keep delegating to the slash provider.
	} else if strings.HasPrefix(prefix, "/") {
		// Absolute path like /usr/lo…: treat as naked path.
		newLine := beforePrefix + item.Value + afterCursor
		out[cursorLine] = newLine
		isDirectory := strings.HasSuffix(item.Label, "/")
		cursorOffset := len(item.Value)
		if isDirectory && strings.HasSuffix(item.Value, `"`) {
			cursorOffset = len(item.Value) - 1
		}
		return out, cursorLine, len(beforePrefix) + cursorOffset
	}

	// Slash-command completion: delegate.
	return p.slash.ApplyCompletion(lines, cursorLine, cursorCol, item, prefix)
}

// extractQuotedPrefix returns the text starting at the last unclosed " in
// text, or "" if all quotes are balanced.
func extractQuotedPrefix(text string) string {
	q := findUnclosedQuoteStart(text)
	if q < 0 {
		return ""
	}
	// Include a preceding @ if it makes the token an @-quoted prefix.
	if q > 0 && text[q-1] == '@' && isTokenStart(text, q-1) {
		return text[q-1:]
	}
	if isTokenStart(text, q) {
		return text[q:]
	}
	return ""
}

// extractPathPrefix extracts a path-like prefix from text-before-cursor.
//
//   - quoted prefix wins if present
//   - else the token after the last delimiter
//   - in force mode (Tab), always returns the token
//   - in non-force mode, only returns when the token contains "/", starts
//     with "." or "~/", or is empty and text ends with a space
func extractPathPrefix(text string, force bool) (string, bool) {
	if q := extractQuotedPrefix(text); q != "" && !strings.HasPrefix(q, "@") {
		return q, true
	}
	lastDelim := findLastDelimiter(text)
	prefix := text
	if lastDelim >= 0 {
		prefix = text[lastDelim+1:]
	}
	if force {
		return prefix, true
	}
	if strings.Contains(prefix, "/") ||
		strings.HasPrefix(prefix, ".") ||
		strings.HasPrefix(prefix, "~/") {
		return prefix, true
	}
	if prefix == "" && strings.HasSuffix(text, " ") {
		return prefix, true
	}
	return "", false
}

// parsePathOnlyPrefix returns the raw query path and quote state for a
// non-@ path prefix.
func parsePathOnlyPrefix(prefix string) (raw string, isQuoted bool) {
	if strings.HasPrefix(prefix, `"`) {
		return prefix[1:], true
	}
	return prefix, false
}

// extractAtPrefix extracts the @-prefixed token from text-before-cursor.
// Returns "" if no @ token is found. Handles both @path and @"path with
// spaces".
func extractAtPrefix(text string) string {
	// Check for quoted @ prefix first: @"partial or @"path/to
	quoteStart := findUnclosedQuoteStart(text)
	if quoteStart >= 0 && quoteStart > 0 && text[quoteStart-1] == '@' {
		// Ensure @ is at a token boundary.
		if isTokenStart(text, quoteStart-1) {
			return text[quoteStart-1:]
		}
		return ""
	}

	// Unquoted: find the last delimiter, check if @ follows.
	lastDelim := findLastDelimiter(text)
	tokenStart := 0
	if lastDelim >= 0 {
		tokenStart = lastDelim + 1
	}
	if tokenStart < len(text) && text[tokenStart] == '@' {
		return text[tokenStart:]
	}
	return ""
}

// findLastDelimiter returns the index of the last PATH_DELIMITER in text,
// or -1 if none found.
func findLastDelimiter(text string) int {
	for i := len(text) - 1; i >= 0; i-- {
		if pathDelimiters[text[i]] {
			return i
		}
	}
	return -1
}

// findUnclosedQuoteStart returns the index of the opening " that is still
// unclosed, or -1 if all quotes are balanced.
func findUnclosedQuoteStart(text string) int {
	inQuotes := false
	quoteStart := -1
	for i := range len(text) {
		if text[i] == '"' {
			inQuotes = !inQuotes
			if inQuotes {
				quoteStart = i
			}
		}
	}
	if inQuotes {
		return quoteStart
	}
	return -1
}

// isTokenStart returns true if index is at the start of a token
// (index 0 or preceded by a delimiter).
func isTokenStart(text string, index int) bool {
	return index == 0 || pathDelimiters[text[index-1]]
}

// parseAtPrefix extracts the raw path and quote state from an @ prefix.
// Returns (rawPrefix, isQuoted).
func parseAtPrefix(prefix string) (raw string, isQuoted bool) {
	if strings.HasPrefix(prefix, `@"`) {
		return prefix[2:], true
	}
	if strings.HasPrefix(prefix, "@") {
		return prefix[1:], false
	}
	return prefix, false
}

// buildCompletionValue builds the value string for an @ completion.
// Handles quoting for paths with spaces.
func buildCompletionValue(path string, isDirectory, isQuoted bool) string {
	needsQuotes := isQuoted || strings.Contains(path, " ")
	if !needsQuotes {
		return "@" + path
	}
	if isDirectory {
		return `@"` + path + `"`
	}
	return `@"` + path + `"`
}

// getFileSuggestions returns file completion suggestions for the given
// raw query. When fd is available, it uses fd for a fuzzy tree walk.
// Without fd, it uses os.ReadDir prefix matching in the resolved
// directory.
func (p *CombinedProvider) getFileSuggestions(rawQuery string, isQuoted bool) []AutocompleteItem {
	// fd provides fuzzy search for @ queries, including recursively within a
	// scoped directory. os.ReadDir is used only when fd is unavailable.
	if p.fdPath != "" {
		return p.fdFileSuggestions(rawQuery, isQuoted)
	}
	return p.readdirFileSuggestions(rawQuery, isQuoted)
}

// FileSearchTask returns a deferred, cancellable fd-backed @-file search
// for the cursor's @-prefix, or ok=false when async file search is disabled,
// fd is unavailable, or the buffer-before-cursor is not an @ query. The
// returned run closure executes fd under the caller's context so the editor
// can abort it on the next keystroke.
func (p *CombinedProvider) FileSearchTask(lines []string, cursorLine, cursorCol int) (string, func(context.Context) []AutocompleteItem, bool) {
	if !p.asyncFileSearch || p.fdPath == "" {
		return "", nil, false
	}
	line, cursorCol, ok := lineAtCursor(lines, cursorLine, cursorCol)
	if !ok {
		return "", nil, false
	}
	atPrefix := extractAtPrefix(line[:cursorCol])
	if atPrefix == "" {
		return "", nil, false
	}
	raw, isQuoted := parseAtPrefix(atPrefix)
	run := func(ctx context.Context) []AutocompleteItem {
		return p.fdFileSuggestionsCtx(ctx, raw, isQuoted)
	}
	return atPrefix, run, true
}

// fdSearchEntry is one path returned by fd. Directory paths include fd's
// trailing separator in path.
type fdSearchEntry struct {
	path        string
	isDirectory bool
}

// fdFileSuggestions uses fd for file search. For queries without `/`,
// runs a fuzzy search across the whole project tree. For queries with
// `/`, scopes to the resolved base directory. Each fuzzy search first queries
// direct children and then the recursive tree, so recursive result limits
// cannot hide a matching child of the selected directory.
func (p *CombinedProvider) fdFileSuggestions(rawQuery string, isQuoted bool) []AutocompleteItem {
	return p.fdFileSuggestionsCtx(context.Background(), rawQuery, isQuoted)
}

// fdFileSuggestionsCtx runs the fd-backed search under the caller's context.
// The interactive editor cancels this context on the next keystroke; there is
// no additional timeout.
func (p *CombinedProvider) fdFileSuggestionsCtx(ctx context.Context, rawQuery string, isQuoted bool) []AutocompleteItem {
	query := toDisplayPath(rawQuery)

	var baseDir, fdQuery, displayBase string

	if strings.Contains(query, "/") {
		lastSlash := strings.LastIndex(query, "/")
		displayBase = query[:lastSlash+1]
		fdQuery = query[lastSlash+1:]

		switch {
		case strings.HasPrefix(displayBase, "~/"):
			home, _ := os.UserHomeDir()
			baseDir = filepath.Join(home, displayBase[2:])
		case strings.HasPrefix(displayBase, "/"):
			baseDir = displayBase
		default:
			baseDir = filepath.Join(p.baseDir, displayBase)
		}

		info, err := os.Stat(baseDir)
		if err != nil || !info.IsDir() {
			// A slash in the query can identify path segments anywhere in the
			// project rather than an existing directory relative to baseDir.
			// Fall back to a root --full-path search in that case.
			baseDir = p.baseDir
			fdQuery = query
			displayBase = ""
		}
	} else {
		baseDir = p.baseDir
		fdQuery = query
	}

	baseDirEntries := p.walkDirectoryWithFd(ctx, baseDir, fdQuery, 1)
	recursiveEntries := p.walkDirectoryWithFd(ctx, baseDir, fdQuery, 0)
	if ctx.Err() != nil {
		return nil
	}
	seenPaths := make(map[string]struct{}, len(baseDirEntries)+len(recursiveEntries))
	entries := make([]fdSearchEntry, 0, len(baseDirEntries)+len(recursiveEntries))
	for _, entry := range baseDirEntries {
		seenPaths[entry.path] = struct{}{}
		entries = append(entries, entry)
	}
	for _, entry := range recursiveEntries {
		if _, ok := seenPaths[entry.path]; ok {
			continue
		}
		seenPaths[entry.path] = struct{}{}
		entries = append(entries, entry)
	}

	type scoredEntry struct {
		fdSearchEntry
		score int
	}
	scoredEntries := make([]scoredEntry, 0, len(entries))
	for _, entry := range entries {
		score := 1
		if fdQuery != "" {
			score = scoreEntry(entry.path, fdQuery, entry.isDirectory)
		}
		if score > 0 {
			scoredEntries = append(scoredEntries, scoredEntry{fdSearchEntry: entry, score: score})
		}
	}

	pathCollator := collate.New(language.Und)
	slices.SortFunc(scoredEntries, func(a, b scoredEntry) int {
		if a.score != b.score {
			return b.score - a.score
		}
		if depthDiff := pathDepth(a.path) - pathDepth(b.path); depthDiff != 0 {
			return depthDiff
		}
		if lengthDiff := utf16Length(a.path) - utf16Length(b.path); lengthDiff != 0 {
			return lengthDiff
		}
		return pathCollator.CompareString(a.path, b.path)
	})
	if len(scoredEntries) > 20 {
		scoredEntries = scoredEntries[:20]
	}

	items := make([]AutocompleteItem, 0, len(scoredEntries))
	for _, entry := range scoredEntries {
		pathWithoutSlash := entry.path
		if entry.isDirectory {
			pathWithoutSlash = strings.TrimSuffix(pathWithoutSlash, "/")
		}
		displayPath := pathWithoutSlash
		if displayBase != "" {
			displayPath = displayBase + pathWithoutSlash
		}
		entryName := filepath.Base(pathWithoutSlash)
		label := entryName
		completionPath := displayPath
		if entry.isDirectory {
			label += "/"
			completionPath += "/"
		}
		items = append(items, AutocompleteItem{
			Value:       buildCompletionValue(completionPath, entry.isDirectory, isQuoted),
			Label:       label,
			Description: displayPath,
		})
	}
	return items
}

func (p *CombinedProvider) walkDirectoryWithFd(ctx context.Context, baseDir, query string, maxDepth int) []fdSearchEntry {
	if ctx.Err() != nil {
		return nil
	}
	args := []string{
		"--base-directory", baseDir,
		"--max-results", "100",
		"--type", "f",
		"--type", "d",
		"--follow",
		"--hidden",
		"--exclude", ".git",
		"--exclude", ".git/*",
		"--exclude", ".git/**",
	}
	if maxDepth > 0 {
		args = append(args, "--max-depth", "1")
	}
	if strings.Contains(toDisplayPath(query), "/") {
		args = append(args, "--full-path")
	}
	if query != "" {
		args = append(args, buildFdPathQuery(query))
	}

	out, err := exec.CommandContext(ctx, p.fdPath, args...).Output()
	if err != nil || len(out) == 0 {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	entries := make([]fdSearchEntry, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		display := toDisplayPath(line)
		hasTrailingSlash := strings.HasSuffix(display, "/")
		normalized := strings.TrimSuffix(display, "/")
		if normalized == ".git" || strings.HasPrefix(normalized, ".git/") || strings.Contains(normalized, "/.git/") {
			continue
		}
		entries = append(entries, fdSearchEntry{path: display, isDirectory: hasTrailingSlash})
	}
	return entries
}

func pathDepth(path string) int {
	depth := 0
	inSegment := false
	for _, r := range toDisplayPath(path) {
		if r == '/' {
			inSegment = false
			continue
		}
		if !inSegment {
			depth++
			inSegment = true
		}
	}
	return depth
}

func utf16Length(s string) int {
	length := 0
	for _, r := range s {
		length++
		if r > 0xffff {
			length++
		}
	}
	return length
}

// readdirFileSuggestions uses os.ReadDir for synchronous directory listing.
// Used as fallback when fd is not available, or for scoped queries.
func (p *CombinedProvider) readdirFileSuggestions(rawQuery string, isQuoted bool) []AutocompleteItem {
	query := toDisplayPath(rawQuery)

	var searchDir, searchPrefix, displayBase string

	switch {
	case query == "" || query == "./" || query == "../" || query == "~/" || query == "/":
		// Root-level: list the target directory.
		displayBase = query
		switch {
		case strings.HasPrefix(query, "~/"):
			home, _ := os.UserHomeDir()
			searchDir = home
		case query == "/":
			searchDir = "/"
		default:
			searchDir = filepath.Join(p.baseDir, query)
		}
		searchPrefix = ""
	case strings.HasSuffix(query, "/"):
		// Dir ending with /: list contents.
		displayBase = query
		switch {
		case strings.HasPrefix(query, "~/"):
			home, _ := os.UserHomeDir()
			searchDir = filepath.Join(home, query[2:])
		case strings.HasPrefix(query, "/"):
			searchDir = query
		default:
			searchDir = filepath.Join(p.baseDir, query)
		}
		searchPrefix = ""
	default:
		// Split into dir + prefix.
		dir := filepath.Dir(query)
		searchPrefix = filepath.Base(query)
		displayBase = ""
		switch {
		case dir != ".":
			displayBase = toDisplayPath(dir) + "/"
		case strings.HasPrefix(query, "./"):
			displayBase = "./"
		case strings.HasPrefix(query, "../"):
			displayBase = "../"
		}
		switch {
		case strings.HasPrefix(query, "~/"):
			home, _ := os.UserHomeDir()
			if dir == "~" {
				searchDir = home
			} else {
				searchDir = filepath.Join(home, dir[2:])
			}
		case strings.HasPrefix(query, "/"):
			searchDir = dir
		default:
			searchDir = filepath.Join(p.baseDir, dir)
		}
	}

	dirEntries, err := os.ReadDir(searchDir)
	if err != nil {
		return nil
	}

	items := make([]AutocompleteItem, 0, len(dirEntries))
	lowerPrefix := strings.ToLower(searchPrefix)

	for _, de := range dirEntries {
		name := de.Name()
		if name == ".git" {
			continue
		}
		if lowerPrefix != "" && !strings.HasPrefix(strings.ToLower(name), lowerPrefix) {
			continue
		}
		isDir := de.IsDir()
		if de.Type()&os.ModeSymlink != 0 {
			// Resolve symlinks.
			info, err := os.Stat(filepath.Join(searchDir, name))
			if err == nil {
				isDir = info.IsDir()
			}
		}

		relPath := name
		if displayBase != "" {
			relPath = displayBase + name
		}
		relPath = toDisplayPath(relPath)

		completionPath := relPath
		label := name
		if isDir {
			completionPath += "/"
			label += "/"
		}
		value := buildCompletionValue(completionPath, isDir, isQuoted)

		items = append(items, AutocompleteItem{
			Value:       value,
			Label:       label,
			Description: relPath,
		})
	}

	// Directories first, then alphabetical.
	slices.SortFunc(items, func(a, b AutocompleteItem) int {
		aDir := strings.HasSuffix(a.Label, "/")
		bDir := strings.HasSuffix(b.Label, "/")
		if aDir && !bDir {
			return -1
		}
		if !aDir && bDir {
			return 1
		}
		return autocompleteCollator.CompareString(a.Label, b.Label)
	})
	return items
}

// toDisplayPath normalizes backslashes to forward slashes.
func toDisplayPath(path string) string {
	return strings.ReplaceAll(path, "\\", "/")
}

// buildFdPathQuery converts a query with / separators into an fd-compatible
// regex pattern.
func buildFdPathQuery(query string) string {
	normalized := toDisplayPath(query)
	if !strings.Contains(normalized, "/") {
		return normalized
	}
	hasTrailingSep := strings.HasSuffix(normalized, "/")
	trimmed := strings.Trim(normalized, "/")
	if trimmed == "" {
		return normalized
	}
	sepPattern := `[\\/]`
	segments := strings.Split(trimmed, "/")
	filtered := make([]string, 0, len(segments))
	for _, s := range segments {
		if s != "" {
			filtered = append(filtered, escapeRegex(s))
		}
	}
	if len(filtered) == 0 {
		return normalized
	}
	pattern := strings.Join(filtered, sepPattern)
	if hasTrailingSep {
		pattern += sepPattern
	}
	return pattern
}

// escapeRegex escapes regex metacharacters in a string.
func escapeRegex(s string) string {
	special := `.*+?^${}()|[]\`
	var b strings.Builder
	b.Grow(len(s))
	for _, c := range s {
		if strings.ContainsRune(special, c) {
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// scoreEntry scores a file path against the query for ranking.
// Higher = better match.
func scoreEntry(filePath, query string, isDirectory bool) int {
	fileName := filepath.Base(filePath)
	lowerFile := strings.ToLower(fileName)
	lowerQuery := strings.ToLower(query)

	score := 0
	switch {
	case lowerFile == lowerQuery:
		score = 100
	case strings.HasPrefix(lowerFile, lowerQuery):
		score = 80
	case strings.Contains(lowerFile, lowerQuery):
		score = 50
	case strings.Contains(strings.ToLower(filePath), lowerQuery):
		score = 30
	}

	if isDirectory && score > 0 {
		score += 10
	}
	return score
}
