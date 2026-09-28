package tui

import (
	"cmp"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alexrudloff/wopr/tui/widthx"
)

var listItemPattern = regexp.MustCompile(`^(\s*)([-*]|\d+\.)\s+(.*)$`)

var emailPattern = regexp.MustCompile(`^[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}$`)

// Markdown renders markdown text to ANSI-annotated lines.
type Markdown struct {
	invalidatable
	Content string
	// Transform is an optional display-only rewrite of Content applied at the
	// render width before parsing. Used to replace Mermaid code blocks with rendered diagrams.
	// A Transform that reads state outside (Content, width) must report it through
	// TransformState, or the render cache will serve a stale result.
	Transform func(markdown string, width int) string
	// TransformState reports external transform inputs so a retained Markdown component invalidates cached lines when those inputs change without a Content or width change.
	TransformState func() string
	// defaultColor styles ordinary text tokens; explicit Markdown styles remain independent. Empty means the terminal-default foreground.
	defaultColor  string
	defaultItalic bool
	// Render cache.
	// Short-circuits Render() when content and width haven't changed.
	cachedContent        string
	cachedWidth          int
	cachedLines          []string
	cachedTransformState string
	cachedTheme          *Theme
}

func NewMarkdown(content string) *Markdown { return &Markdown{Content: content} }

// SetDefaultColor sets the ANSI foreground applied to ordinary Markdown text tokens. Headings, code, list markers and blockquotes retain their own theme styles. Passing an empty string restores terminal-default foreground and invalidates the cache.
func (m *Markdown) SetDefaultColor(open string) {
	if m.defaultColor == open {
		return
	}
	m.defaultColor = open
	m.cachedLines = nil
}

// applyDefaultStyle wraps text tokens, leaving code spans and other explicitly themed tokens independent.
func (m *Markdown) applyDefaultStyle(s string) string {
	if m.defaultColor != "" {
		s = m.defaultColor + s + SGRFgReset
	}
	if m.defaultItalic {
		s = ansiSpan("\x1b[3m", SGRItalicReset, s)
	}
	return s
}

func (m *Markdown) inlineMarkdown(s string) string {
	prefix := m.defaultColor
	if m.defaultItalic {
		prefix = "\x1b[3m" + prefix
	}
	return renderInlineMarkdown(s, inlineStyleContext{applyText: m.applyDefaultStyle, stylePrefix: prefix})
}

func (m *Markdown) Render(width int) []string {
	width = max(width, 1)
	transformState := ""
	if m.TransformState != nil {
		transformState = m.TransformState()
	}
	if m.cachedLines != nil && m.cachedContent == m.Content && m.cachedWidth == width &&
		m.cachedTransformState == transformState && m.cachedTheme == ActiveTheme() {
		return m.cachedLines
	}
	content := m.Content
	if m.Transform != nil {
		content = m.Transform(content, width)
	}
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))

	emitBlank := func() {
		if len(out) == 0 || out[len(out)-1] == "" {
			return
		}
		out = append(out, "")
	}

	// emit appends one logical line, wrapping at `width` (visual columns).
	// Wrapping happens AFTER inline formatting so soft-wrapped continuations
	// inherit the same ANSI styling. The continuation rows are NOT indented
	// (indenting inside lists would be wrong here because we don't track
	// list nesting).
	emit := func(line string) {
		if widthx.VisibleWidth(line) <= width {
			out = append(out, line)
			return
		}
		out = append(out, wrapText(line, width)...)
	}

	inCode := false
	codeLang := ""
	codeFence := byte(0)
	codeFenceLen := 0
	var codeLines []string

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		nextLine := ""
		if i+1 < len(lines) {
			nextLine = lines[i+1]
		}

		if inCode {
			if isMarkdownFenceClose(line, codeFence, codeFenceLen) {
				out = append(out, renderCodeBlock(codeLang, codeLines, width)...)
				if strings.TrimSpace(nextLine) != "" {
					emitBlank()
				}
				inCode = false
				codeLang = ""
				codeFence = 0
				codeFenceLen = 0
				continue
			}
			codeLines = append(codeLines, line)
			continue
		}
		if fence, fenceLen, lang, ok := parseMarkdownFenceOpen(line); ok {
			emitBlank()
			inCode = true
			codeFence = fence
			codeFenceLen = fenceLen
			codeLang = lang
			codeLines = nil
			continue
		}

		if isTableHeaderLine(line) && isTableSeparatorLine(nextLine) {
			header := splitTableRow(line)
			align := parseTableAlignment(nextLine)
			var rows [][]string
			j := i + 2
			for j < len(lines) && isTableHeaderLine(lines[j]) {
				rows = append(rows, splitTableRow(lines[j]))
				j++
			}
			raw := strings.Join(lines[i:j], "\n")
			out = append(out, m.renderMarkdownTable(header, rows, align, raw, width)...)
			if j < len(lines) && strings.TrimSpace(lines[j]) != "" {
				emitBlank()
			}
			i = j - 1
			continue
		}

		// Block LaTeX ($$...$$, \[...\]): may span multiple lines. Checked
		// before paragraph handling and after code fences (code wins).
		if blockLatexStart(line) {
			if tok, ok := tokenizeBlockLatex(strings.Join(lines[i:], "\n")); ok {
				for bl := range strings.SplitSeq(renderBlockLatex(tok), "\n") {
					emit(m.applyDefaultStyle(bl))
				}
				consumed := strings.Count(tok.raw, "\n")
				if strings.HasSuffix(tok.raw, "\n") {
					consumed--
				}
				i += consumed
				if i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != "" {
					emitBlank()
				}
				continue
			}
		}

		// Headings: use theme colors. Inline markdown (code, bold, etc.)
		// inside headings is parsed via inlineMarkdown(), then resets are
		// patched to re-apply the base heading style so subsequent text
		// stays styled (bold+cyan re-applied after inline `code`, underline
		// re-applied for h1).
		th := ActiveTheme()
		headingColor := cmp.Or(th.MDHeading, "\033[1;33m")
		if depth, text, ok := parseATXHeading(line); ok {
			if depth >= 3 {
				text = strings.Repeat("#", depth) + " " + text
			}
			style := headingColor
			if depth == 1 {
				style = "\033[1;4m" + headingColor
			}
			emit(headingInline(text, style))
			if strings.TrimSpace(nextLine) != "" {
				emitBlank()
			}
			continue
		}

		// Horizontal rule
		hrColor := cmp.Or(th.MDHr, "\033[2m")
		if line == "---" || line == "***" || line == "___" {
			out = append(out, hrColor+strings.Repeat("─", width)+SGRFgReset)
			if strings.TrimSpace(nextLine) != "" {
				emitBlank()
			}
			continue
		}

		// Blockquote blocks, including lazy continuation lines.
		if isBlockquoteLine(line) {
			j := i
			quoted := make([]string, 0, 4)
			for j < len(lines) {
				current := lines[j]
				switch {
				case isBlockquoteLine(current):
					quoted = append(quoted, stripBlockquotePrefix(current))
				case j > i && isLazyBlockquoteContinuation(current):
					quoted = append(quoted, current)
				default:
					goto renderQuote
				}
				j++
			}
		renderQuote:
			quoteLines := NewMarkdown(strings.Join(quoted, "\n")).Render(max(1, width-2))
			out = append(out, renderQuotedLines(quoteLines)...)
			i = j - 1
			continue
		}

		// Lists with preserved source indentation and marker shape.
		if indent, marker, body, ok := parseMarkdownListItem(line); ok {
			out = append(out, m.renderMarkdownListItem(indent, marker, body, width)...)
			continue
		}

		// Empty line → blank
		if strings.TrimSpace(line) == "" {
			emitBlank()
			continue
		}

		// Normal paragraph: apply inline formatting then wrap.
		emit(m.inlineMarkdown(line))
	}

	// Unclosed code block
	if inCode {
		out = append(out, renderCodeBlock(codeLang, codeLines, width)...)
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	out = wrapRenderedLines(out, width)

	m.cachedContent = m.Content
	m.cachedWidth = width
	m.cachedLines = out
	m.cachedTransformState = transformState
	m.cachedTheme = ActiveTheme()
	return out
}

// wrapRenderedLines is Render's final pass: every rendered
// non-image line goes through wrapTextWithAnsi(line, contentWidth), so no
// block renderer (tables, code, quotes, lists) can emit a row wider than the
// component. wrapTextWithAnsi returns a fitting line unchanged, so only
// overflowing rows are touched.
func wrapRenderedLines(lines []string, width int) []string {
	for i, line := range lines {
		if widthx.IsImageLine(line) || widthx.VisibleWidth(line) <= width {
			continue
		}
		out := append([]string(nil), lines[:i]...)
		for _, l := range lines[i:] {
			if widthx.IsImageLine(l) || widthx.VisibleWidth(l) <= width {
				out = append(out, l)
				continue
			}
			out = append(out, widthx.WrapTextWithAnsi(l, width)...)
		}
		return out
	}
	return lines
}

func parseMarkdownFenceOpen(line string) (fence byte, fenceLen int, lang string, ok bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < 3 || trimmed[0] != '`' && trimmed[0] != '~' {
		return 0, 0, "", false
	}
	fence = trimmed[0]
	for fenceLen < len(trimmed) && trimmed[fenceLen] == fence {
		fenceLen++
	}
	if fenceLen < 3 {
		return 0, 0, "", false
	}
	lang = strings.TrimSpace(trimmed[fenceLen:])
	if fence == '`' && strings.ContainsRune(lang, '`') {
		return 0, 0, "", false
	}
	return fence, fenceLen, lang, true
}

func isMarkdownFenceClose(line string, fence byte, minLen int) bool {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < minLen || trimmed[0] != fence {
		return false
	}
	fenceLen := 0
	for fenceLen < len(trimmed) && trimmed[fenceLen] == fence {
		fenceLen++
	}
	return fenceLen >= minLen && strings.TrimSpace(trimmed[fenceLen:]) == ""
}

const maxATXHeadingDepth = len("######")

func parseATXHeading(line string) (depth int, text string, ok bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return 0, "", false
	}
	for depth < len(trimmed) && depth <= maxATXHeadingDepth && trimmed[depth] == '#' {
		depth++
	}
	if depth == 0 || depth > maxATXHeadingDepth || depth >= len(trimmed) || trimmed[depth] != ' ' {
		return 0, "", false
	}
	return depth, strings.TrimSpace(trimmed[depth+1:]), true
}

type tableAlignment int

const (
	tableAlignLeft tableAlignment = iota
	tableAlignCenter
	tableAlignRight
)

func isTableHeaderLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|")
}

func isTableSeparatorLine(line string) bool {
	if !isTableHeaderLine(line) {
		return false
	}
	for _, cell := range splitTableRow(line) {
		trimmed := strings.TrimSpace(cell)
		if trimmed == "" {
			return false
		}
		trimmed = strings.TrimPrefix(trimmed, ":")
		trimmed = strings.TrimSuffix(trimmed, ":")
		if trimmed == "" || strings.Trim(trimmed, "-") != "" {
			return false
		}
	}
	return true
}

func splitTableRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, "|")
	trimmed = strings.TrimSuffix(trimmed, "|")
	parts := strings.Split(trimmed, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func parseTableAlignment(line string) []tableAlignment {
	parts := splitTableRow(line)
	out := make([]tableAlignment, len(parts))
	for i, cell := range parts {
		trimmed := strings.TrimSpace(cell)
		switch {
		case strings.HasPrefix(trimmed, ":") && strings.HasSuffix(trimmed, ":"):
			out[i] = tableAlignCenter
		case strings.HasSuffix(trimmed, ":"):
			out[i] = tableAlignRight
		default:
			out[i] = tableAlignLeft
		}
	}
	return out
}

func longestWordWidth(text string) int {
	words := strings.Fields(text)
	if len(words) == 0 {
		return 1
	}
	longest := 1
	for _, word := range words {
		if w := widthx.VisibleWidth(word); w > longest {
			longest = w
		}
	}
	if longest > 30 {
		return 30
	}
	return longest
}

func alignTableCell(text string, width int, align tableAlignment) string {
	pad := max(0, width-widthx.VisibleWidth(text))
	switch align {
	case tableAlignRight:
		return strings.Repeat(" ", pad) + text
	case tableAlignCenter:
		left := pad / 2
		right := pad - left
		return strings.Repeat(" ", left) + text + strings.Repeat(" ", right)
	default:
		return text + strings.Repeat(" ", pad)
	}
}

func (m *Markdown) renderMarkdownTable(header []string, rows [][]string, align []tableAlignment, raw string, availableWidth int) []string {
	numCols := len(header)
	if numCols == 0 {
		return nil
	}
	borderOverhead := 3*numCols + 1
	availableForCells := availableWidth - borderOverhead
	if availableForCells < numCols {
		return widthx.WrapTextWithAnsi(raw, availableWidth)
	}

	natural := make([]int, numCols)
	minWordWidths := make([]int, numCols)
	for i, cell := range header {
		text := m.inlineMarkdown(cell)
		natural[i] = widthx.VisibleWidth(text)
		minWordWidths[i] = longestWordWidth(text)
	}
	for _, row := range rows {
		for i := range numCols {
			cell := ""
			if i < len(row) {
				cell = m.inlineMarkdown(row[i])
			}
			if w := widthx.VisibleWidth(cell); w > natural[i] {
				natural[i] = w
			}
			if w := longestWordWidth(cell); w > minWordWidths[i] {
				minWordWidths[i] = w
			}
		}
	}

	minWidths := append([]int(nil), minWordWidths...)
	minCellsWidth := 0
	for _, w := range minWidths {
		minCellsWidth += w
	}
	if minCellsWidth > availableForCells {
		for i := range minWidths {
			minWidths[i] = 1
		}
		remaining := availableForCells - numCols
		if remaining > 0 {
			totalWeight := 0
			for _, width := range minWordWidths {
				totalWeight += max(0, width-1)
			}
			allocated := 0
			for i, width := range minWordWidths {
				weight := max(0, width-1)
				growth := 0
				if totalWeight > 0 {
					growth = int(math.Floor(float64(weight) / float64(totalWeight) * float64(remaining)))
				}
				minWidths[i] += growth
				allocated += growth
			}
			leftover := remaining - allocated
			for i := 0; leftover > 0 && i < numCols; i++ {
				minWidths[i]++
				leftover--
			}
		}
		minCellsWidth = 0
		for _, width := range minWidths {
			minCellsWidth += width
		}
	}

	totalNatural := borderOverhead
	for _, w := range natural {
		totalNatural += w
	}
	columnWidths := make([]int, numCols)
	copy(columnWidths, minWidths)
	if totalNatural <= availableWidth {
		for i := range numCols {
			if natural[i] > columnWidths[i] {
				columnWidths[i] = natural[i]
			}
		}
	} else {
		extraWidth := max(0, availableForCells-minCellsWidth)
		totalGrowPotential := 0
		for i := range numCols {
			totalGrowPotential += max(0, natural[i]-minWidths[i])
		}
		for i := range numCols {
			grow := 0
			if totalGrowPotential > 0 {
				grow = int(math.Floor(float64(max(0, natural[i]-minWidths[i])) / float64(totalGrowPotential) * float64(extraWidth)))
			}
			columnWidths[i] = minWidths[i] + grow
		}
		allocated := 0
		for _, w := range columnWidths {
			allocated += w
		}
		remaining := availableForCells - allocated
		for remaining > 0 {
			grew := false
			for i := range numCols {
				if remaining == 0 {
					break
				}
				if columnWidths[i] < natural[i] {
					columnWidths[i]++
					remaining--
					grew = true
				}
			}
			if !grew {
				break
			}
		}
	}

	topCells := make([]string, numCols)
	for i, w := range columnWidths {
		topCells[i] = strings.Repeat("─", w)
	}
	separator := "├─" + strings.Join(topCells, "─┼─") + "─┤"
	lines := []string{"┌─" + strings.Join(topCells, "─┬─") + "─┐"}

	headerWrapped := make([][]string, numCols)
	maxHeaderLines := 1
	for i, cell := range header {
		headerWrapped[i] = widthx.WrapTextWithAnsi(m.inlineMarkdown(cell), max(1, columnWidths[i]))
		if len(headerWrapped[i]) > maxHeaderLines {
			maxHeaderLines = len(headerWrapped[i])
		}
	}
	th := ActiveTheme()
	for lineIdx := range maxHeaderLines {
		parts := make([]string, numCols)
		for col := range numCols {
			text := ""
			if lineIdx < len(headerWrapped[col]) {
				text = headerWrapped[col][lineIdx]
			}
			parts[col] = "\033[1m" + alignTableCell(text, columnWidths[col], tableAlignLeft) + th.Reset
		}
		lines = append(lines, "│ "+strings.Join(parts, " │ ")+" │")
	}
	lines = append(lines, separator)

	for rowIdx, row := range rows {
		wrapped := make([][]string, numCols)
		maxRowLines := 1
		for col := range numCols {
			text := ""
			if col < len(row) {
				text = m.inlineMarkdown(row[col])
			}
			wrapped[col] = widthx.WrapTextWithAnsi(text, max(1, columnWidths[col]))
			if len(wrapped[col]) > maxRowLines {
				maxRowLines = len(wrapped[col])
			}
		}
		for lineIdx := range maxRowLines {
			parts := make([]string, numCols)
			for col := range numCols {
				text := ""
				if lineIdx < len(wrapped[col]) {
					text = wrapped[col][lineIdx]
				}
				parts[col] = alignTableCell(text, columnWidths[col], tableAlignLeft)
			}
			lines = append(lines, "│ "+strings.Join(parts, " │ ")+" │")
		}
		if rowIdx < len(rows)-1 {
			lines = append(lines, separator)
		}
	}

	bottomCells := make([]string, numCols)
	for i, w := range columnWidths {
		bottomCells[i] = strings.Repeat("─", w)
	}
	lines = append(lines, "└─"+strings.Join(bottomCells, "─┴─")+"─┘")
	return lines
}

func parseMarkdownListItem(line string) (indent, marker, body string, ok bool) {
	match := listItemPattern.FindStringSubmatch(line)
	if match == nil {
		return "", "", "", false
	}
	return match[1], match[2], match[3], true
}

// listTaskPattern follows GFM task-list rules: a
// list item whose text starts with "[ ]", "[x]", or "[X]" plus a space is a
// task item, and the checkbox with its trailing spaces leaves the item text.
var listTaskPattern = regexp.MustCompile(`^\[([ xX])\] +`)

// splitListTaskMarker returns the normalized task marker ("[x] " or "[ ] ")
// and the remaining item text.
func splitListTaskMarker(body string) (taskMarker, rest string) {
	match := listTaskPattern.FindStringSubmatch(body)
	if match == nil {
		return "", body
	}
	if match[1] == " " {
		return "[ ] ", body[len(match[0]):]
	}
	return "[x] ", body[len(match[0]):]
}

func (m *Markdown) renderMarkdownListItem(indent, marker, body string, width int) []string {
	th := ActiveTheme()
	if indent != "" {
		indent += " "
	}
	taskMarker, body := splitListTaskMarker(body)
	marker += " " + taskMarker
	styledMarker := marker
	if th.MDListBullet != "" {
		styledMarker = th.MDListBullet + marker + "\033[39m"
	}
	prefix := indent + styledMarker
	continuation := strings.Repeat(" ", widthx.VisibleWidth(indent+marker))
	wrapped := widthx.WrapTextWithAnsi(m.inlineMarkdown(body), max(1, width-widthx.VisibleWidth(indent+marker)))
	if len(wrapped) == 0 {
		return []string{prefix}
	}
	out := []string{prefix + wrapped[0]}
	for _, line := range wrapped[1:] {
		out = append(out, continuation+line)
	}
	return out
}

func isBlockquoteLine(line string) bool {
	trimmed := strings.TrimLeft(line, " ")
	return strings.HasPrefix(trimmed, ">")
}

func stripBlockquotePrefix(line string) string {
	trimmed := strings.TrimLeft(line, " ")
	if !strings.HasPrefix(trimmed, ">") {
		return line
	}
	after := trimmed[1:]
	if strings.HasPrefix(after, " ") {
		return after[1:]
	}
	return after
}

func isLazyBlockquoteContinuation(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	if isBlockquoteLine(line) {
		return false
	}
	if strings.HasPrefix(trimmed, "```") || isTableHeaderLine(line) || isTableSeparatorLine(line) {
		return false
	}
	if trimmed == "---" || trimmed == "***" || trimmed == "___" {
		return false
	}
	if strings.HasPrefix(trimmed, "#") {
		return false
	}
	if _, _, _, ok := parseMarkdownListItem(line); ok {
		return false
	}
	return true
}

func renderQuotedLines(lines []string) []string {
	th := ActiveTheme()
	quoteColor := cmp.Or(th.MDQuote, "\033[2m")
	quotePrefix := quoteColor + "\033[3m"
	border := quoteColor + "│ "
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			out = append(out, border+SGRFgReset)
			continue
		}
		out = append(out, border+quotePrefix+line+SGRItalicReset+SGRFgReset)
	}
	for len(out) > 0 && out[len(out)-1] == border+SGRFgReset {
		out = out[:len(out)-1]
	}
	return out
}

// inlineMarkdown applies bold, italic, inline code, and strikethrough formatting.
// headingInline renders `text` as a heading line: applies `baseStyle`,
// then inline-parses bold/italic/code/links inside the text so each
// nested SGR span ends with the relevant scoped reset. The line ends
// by closing heading bold, underline, and foreground color without
// clearing any active background.
//
// The heading SGR wraps inline-span SGRs and is reapplied after nested
// style resets so subsequent text stays styled.
func headingInline(text, baseStyle string) string {
	rendered := inlineMarkdown(text)
	// Re-apply base style after scoped inline resets so heading color,
	// boldness, and underline remain active after nested spans.
	for _, close := range []string{SGRFgReset, SGRBoldDimReset, SGRItalicReset, SGRStrikeReset, SGRUnderlineReset, SGRInverseReset} {
		rendered = strings.ReplaceAll(rendered, close, close+baseStyle)
	}
	return baseStyle + rendered + SGRBoldDimReset + SGRUnderlineReset + SGRFgReset
}

type inlineStyleContext struct {
	applyText   func(string) string
	stylePrefix string
}

// ansiSpan nests theme decorations: an inner closing code restores the outer span until its own close.
func ansiSpan(open, close, text string) string {
	return open + strings.ReplaceAll(text, close, open) + close
}

func inlineMarkdown(s string) string {
	return renderInlineMarkdown(s, inlineStyleContext{applyText: func(text string) string { return text }})
}

func renderInlineMarkdown(s string, style inlineStyleContext) string {
	th := ActiveTheme()
	var out, plain strings.Builder
	flushText := func() {
		if plain.Len() > 0 {
			out.WriteString(style.applyText(plain.String()))
			plain.Reset()
		}
	}
	emitToken := func(token string) {
		flushText()
		out.WriteString(token)
		out.WriteString(style.stylePrefix)
	}
	i := 0
	runes := []rune(s)
scan:
	for i < len(runes) {
		// Markdown link: [text](url)
		if runes[i] == '[' {
			if text, url, next, ok := parseMarkdownLink(runes, i); ok {
				emitToken(styleMarkdownLink(renderInlineMarkdown(text, style), text, url, th))
				i = next
				continue
			}
		}
		// Inline code: `...`
		if runes[i] == '`' {
			j := i + 1
			for j < len(runes) && runes[j] != '`' {
				j++
			}
			codeColor := th.MDCode
			closeCode := SGRFgReset
			if codeColor == "" {
				codeColor = "\033[7m"
				closeCode = SGRInverseReset
			}
			emitToken(codeColor + string(runes[i+1:j]) + closeCode)
			i = j + 1
			continue
		}
		// Bold **...**, italic *...*, strikethrough ~~...~~ (in that order).
		for _, sp := range inlineSpans {
			if j, ok := closingDelim(runes, i, sp.delim); ok {
				n := len(sp.delim)
				emitToken(ansiSpan(sp.open, sp.close, renderInlineMarkdown(string(runes[i+n:j]), style)))
				i = j + n
				continue scan
			}
		}
		// Bare URLs and emails.
		if text, url, next, ok := parseAutoLink(runes, i); ok {
			emitToken(styleMarkdownLink(style.applyText(text), text, url, th))
			i = next
			continue
		}
		// Code spans take precedence over inline LaTeX.
		if runes[i] == '$' || (runes[i] == '\\' && i+1 < len(runes) && (runes[i+1] == '(' || runes[i+1] == '[')) {
			if tok, ok := tokenizeInlineLatex(string(runes[i:])); ok {
				flushText()
				out.WriteString(style.applyText(renderInlineLatex(tok)))
				i += utf8.RuneCountInString(tok.raw)
				continue
			}
		}
		plain.WriteRune(runes[i])
		i++
	}
	flushText()
	result := out.String()
	for style.stylePrefix != "" && strings.HasSuffix(result, style.stylePrefix) {
		result = strings.TrimSuffix(result, style.stylePrefix)
	}
	return result
}

// inlineSpans are the delimited inline styles, tried in order.
var inlineSpans = []struct {
	delim       []rune
	open, close string
}{
	{[]rune("**"), "\033[1m", SGRBoldDimReset},
	{[]rune("*"), "\033[3m", SGRItalicReset},
	{[]rune("~~"), "\033[9m", SGRStrikeReset},
}

// closingDelim reports where delim closes a span that runes[i:] opens with
// delim.
func closingDelim(runes []rune, i int, delim []rune) (int, bool) {
	at := func(j int) bool { return len(runes)-j >= len(delim) && slices.Equal(runes[j:j+len(delim)], delim) }
	if !at(i) {
		return 0, false
	}
	for j := i + len(delim); j+len(delim) <= len(runes); j++ {
		if at(j) {
			return j, true
		}
	}
	return 0, false
}

func parseMarkdownLink(runes []rune, start int) (text, url string, next int, ok bool) {
	closeBracket := slices.Index(runes[start+1:], ']') + start + 1
	if closeBracket == start || closeBracket+1 >= len(runes) || runes[closeBracket+1] != '(' {
		return "", "", start, false
	}
	closeParen := slices.Index(runes[closeBracket+2:], ')')
	if closeParen == -1 {
		return "", "", start, false
	}
	closeParen += closeBracket + 2
	text = string(runes[start+1 : closeBracket])
	url = string(runes[closeBracket+2 : closeParen])
	return text, url, closeParen + 1, true
}

func autoLinkPrefix(runes []rune, start int) string {
	return string(runes[start:min(start+len("https://"), len(runes))])
}

func parseAutoLink(runes []rune, start int) (text, url string, next int, ok bool) {
	end := start
	for end < len(runes) && !unicode.IsSpace(runes[end]) {
		end++
	}
	candidate := trimTrailingLinkPunctuation(string(runes[start:end]))
	remaining := autoLinkPrefix(runes, start)
	if strings.HasPrefix(remaining, "https://") || strings.HasPrefix(remaining, "http://") {
		if candidate == "" {
			return "", "", start, false
		}
		return candidate, candidate, start + utf8.RuneCountInString(candidate), true
	}
	if emailPattern.MatchString(candidate) {
		return candidate, "mailto:" + candidate, start + utf8.RuneCountInString(candidate), true
	}
	return "", "", start, false
}

func trimTrailingLinkPunctuation(s string) string {
	for s != "" {
		switch s[len(s)-1] {
		case '.', ',', ';', ':', '!', '?':
			s = s[:len(s)-1]
		case ')':
			if strings.Count(s, "(") < strings.Count(s, ")") {
				s = s[:len(s)-1]
				continue
			}
			return s
		default:
			return s
		}
	}
	return s
}

func styleMarkdownLink(display, rawText, url string, th *Theme) string {
	linkColor := cmp.Or(th.MDLink, "\033[34m")
	styled := "\033[4m" + linkColor + display + SGRUnderlineReset + SGRFgReset
	if Capabilities().Hyperlinks {
		return Hyperlink(styled, url)
	}
	hrefForComparison := url
	if after, ok := strings.CutPrefix(hrefForComparison, "mailto:"); ok {
		hrefForComparison = after
	}
	if rawText == url || rawText == hrefForComparison {
		return styled
	}
	urlColor := cmp.Or(th.MDLinkUrl, th.Muted)
	return styled + urlColor + " (" + url + ")" + SGRFgReset
}

// renderCodeBlock renders a fenced code block's lines with no fences, box, or
// indent: syntax-highlighted through HighlightCode when the fence names a
// language, otherwise in the code block color. Long lines wrap.
func renderCodeBlock(lang string, lines []string, width int) []string {
	var body []string
	if lang != "" {
		if highlighted := HighlightCode(strings.Join(lines, "\n"), lang); len(highlighted) == len(lines) {
			body = highlighted
		}
	}
	if body == nil {
		color := ActiveTheme().MDCodeBlock
		body = make([]string, len(lines))
		for i, line := range lines {
			body[i] = color + line + SGRFgReset
		}
	}
	out := make([]string, 0, len(body))
	for _, line := range body {
		out = append(out, widthx.WrapTextWithAnsi(line, max(1, width))...)
	}
	return out
}
