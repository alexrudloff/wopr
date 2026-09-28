package web

import (
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// HTMLToMarkdown converts a page to readable markdown. It keeps the main
// content (the first <main>, else the longest <article>, else <body>), drops
// scripts, styles, navigation, forms and hidden elements, and resolves links
// against base.
func HTMLToMarkdown(r io.Reader, base *url.URL) (title, markdown string, err error) {
	doc, err := html.Parse(r)
	if err != nil {
		return "", "", err
	}
	if t := find(doc, func(n *html.Node) bool { return n.DataAtom == atom.Title }); t != nil {
		title = strings.Join(strings.Fields(textOf(t)), " ")
	}
	root := contentRoot(doc)
	w := &mdWriter{base: base}
	w.children(root)
	return title, tidy(w.b.String()), nil
}

func find(n *html.Node, match func(*html.Node) bool) *html.Node {
	if n.Type == html.ElementNode && match(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := find(c, match); found != nil {
			return found
		}
	}
	return nil
}

func contentRoot(doc *html.Node) *html.Node {
	if main := find(doc, func(n *html.Node) bool { return n.DataAtom == atom.Main || attr(n, "role") == "main" }); main != nil {
		return main
	}
	var best *html.Node
	bestLen := 0
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Article {
			if l := len(textOf(n)); l > bestLen {
				best, bestLen = n, l
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if best != nil {
		return best
	}
	if body := find(doc, func(n *html.Node) bool { return n.DataAtom == atom.Body }); body != nil {
		return body
	}
	return doc
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && dropped(n) {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// dropped reports elements that carry no readable content.
func dropped(n *html.Node) bool {
	switch n.DataAtom {
	case atom.Script, atom.Style, atom.Noscript, atom.Template, atom.Svg, atom.Canvas, atom.Iframe,
		atom.Nav, atom.Footer, atom.Aside, atom.Form, atom.Button, atom.Input, atom.Select, atom.Textarea,
		atom.Head, atom.Object, atom.Embed, atom.Dialog:
		return true
	}
	role := attr(n, "role")
	return hasAttr(n, "hidden") || attr(n, "aria-hidden") == "true" || role == "navigation" || role == "banner" || role == "contentinfo" ||
		strings.Contains(strings.ReplaceAll(attr(n, "style"), " ", ""), "display:none")
}

type mdWriter struct {
	b    strings.Builder
	base *url.URL
	pre  int
	// list holds the open lists: 0 for unordered, else the next number.
	list []int
}

func (w *mdWriter) atLineStart() bool {
	s := w.b.String()
	return s == "" || strings.HasSuffix(s, "\n")
}

// block ends the current line and, when blank is set, leaves an empty line.
func (w *mdWriter) block(blank bool) {
	s := w.b.String()
	if s == "" {
		return
	}
	if !strings.HasSuffix(s, "\n") {
		w.b.WriteByte('\n')
		s += "\n"
	}
	if blank && !strings.HasSuffix(s, "\n\n") {
		w.b.WriteByte('\n')
	}
}

func (w *mdWriter) text(s string) {
	if w.pre > 0 {
		w.b.WriteString(s)
		return
	}
	s = collapseSpace(s)
	if w.atLineStart() {
		s = strings.TrimLeft(s, " ")
	}
	w.b.WriteString(s)
}

var spaceRE = regexp.MustCompile(`\s+`)

func collapseSpace(s string) string { return spaceRE.ReplaceAllString(s, " ") }

// inline renders n's children into a string.
func (w *mdWriter) inline(n *html.Node) string {
	sub := &mdWriter{base: w.base, pre: w.pre}
	sub.children(n)
	return strings.TrimSpace(collapseSpace(sub.b.String()))
}

func (w *mdWriter) children(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.node(c)
	}
}

func (w *mdWriter) node(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		w.text(n.Data)
		return
	case html.ElementNode:
	default:
		w.children(n)
		return
	}
	if dropped(n) {
		return
	}
	switch n.DataAtom {
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		if text := w.inline(n); text != "" {
			w.block(true)
			level, _ := strconv.Atoi(n.Data[1:])
			w.b.WriteString(strings.Repeat("#", level) + " " + text)
			w.block(true)
		}
	case atom.P, atom.Section, atom.Article, atom.Main, atom.Header, atom.Figure, atom.Details, atom.Dl, atom.Address, atom.Center:
		w.block(true)
		w.children(n)
		w.block(true)
	case atom.Div, atom.Figcaption, atom.Summary, atom.Dt, atom.Dd, atom.Caption:
		w.block(false)
		w.children(n)
		w.block(false)
	case atom.Br:
		w.b.WriteByte('\n')
	case atom.Hr:
		w.block(true)
		w.b.WriteString("---")
		w.block(true)
	case atom.A:
		w.link(n)
	case atom.Strong, atom.B:
		w.wrap(n, "**")
	case atom.Em, atom.I:
		w.wrap(n, "*")
	case atom.Code, atom.Kbd, atom.Samp:
		if w.pre > 0 {
			w.children(n)
		} else {
			w.wrap(n, "`")
		}
	case atom.Pre:
		w.preformatted(n)
	case atom.Ul, atom.Ol:
		start := 0
		if n.DataAtom == atom.Ol {
			start = 1
			if s, err := strconv.Atoi(attr(n, "start")); err == nil {
				start = s
			}
		}
		w.block(len(w.list) == 0)
		w.list = append(w.list, start)
		w.children(n)
		w.list = w.list[:len(w.list)-1]
		w.block(len(w.list) == 0)
	case atom.Li:
		w.item(n)
	case atom.Blockquote:
		sub := &mdWriter{base: w.base}
		sub.children(n)
		if quoted := tidy(sub.b.String()); quoted != "" {
			w.block(true)
			w.b.WriteString("> " + strings.ReplaceAll(quoted, "\n", "\n> "))
			w.block(true)
		}
	case atom.Table:
		w.table(n)
	case atom.Img:
		// Images carry no text a model can use without fetching them.
	default:
		w.children(n)
	}
}

func (w *mdWriter) wrap(n *html.Node, mark string) {
	text := w.inline(n)
	if text == "" {
		return
	}
	if !w.atLineStart() {
		if s := w.b.String(); !strings.HasSuffix(s, " ") && startsWord(n) {
			w.b.WriteByte(' ')
		}
	}
	w.b.WriteString(mark + text + mark)
}

// startsWord reports whether an inline element's source text began after
// whitespace, so the space survives the element's trimming.
func startsWord(n *html.Node) bool {
	if p := n.PrevSibling; p != nil && p.Type == html.TextNode {
		return strings.TrimRight(p.Data, " \t\n") != p.Data
	}
	return false
}

func (w *mdWriter) link(n *html.Node) {
	text := w.inline(n)
	href := strings.TrimSpace(attr(n, "href"))
	if text == "" {
		return
	}
	if startsWord(n) && !w.atLineStart() && !strings.HasSuffix(w.b.String(), " ") {
		w.b.WriteByte(' ')
	}
	if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(strings.ToLower(href), "javascript:") {
		w.b.WriteString(text)
		return
	}
	if w.base != nil {
		if u, err := w.base.Parse(href); err == nil {
			href = u.String()
		}
	}
	w.b.WriteString("[" + text + "](" + href + ")")
}

func (w *mdWriter) preformatted(n *html.Node) {
	lang := ""
	for _, node := range []*html.Node{n, n.FirstChild} {
		if node == nil {
			continue
		}
		for class := range strings.FieldsSeq(attr(node, "class")) {
			if l, ok := strings.CutPrefix(class, "language-"); ok {
				lang = l
			}
		}
	}
	sub := &mdWriter{base: w.base, pre: 1}
	sub.children(n)
	w.block(true)
	w.b.WriteString("```" + lang + "\n" + strings.Trim(sub.b.String(), "\n") + "\n```")
	w.block(true)
}

func (w *mdWriter) item(n *html.Node) {
	w.block(false)
	depth := max(len(w.list), 1)
	marker := "- "
	if len(w.list) > 0 && w.list[len(w.list)-1] > 0 {
		marker = strconv.Itoa(w.list[len(w.list)-1]) + ". "
		w.list[len(w.list)-1]++
	}
	w.b.WriteString(strings.Repeat("  ", depth-1) + marker)
	w.children(n)
	w.block(false)
}

func (w *mdWriter) table(n *html.Node) {
	var rows [][]string
	header := false
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Tr {
			var cells []string
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode && (c.DataAtom == atom.Td || c.DataAtom == atom.Th) {
					if c.DataAtom == atom.Th && len(rows) == 0 {
						header = true
					}
					cells = append(cells, strings.ReplaceAll(w.inline(c), "|", `\|`))
				}
			}
			if len(cells) > 0 {
				rows = append(rows, cells)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode || c.DataAtom != atom.Table {
				walk(c)
			}
		}
	}
	walk(n)
	if len(rows) == 0 {
		return
	}
	w.block(true)
	for i, row := range rows {
		w.b.WriteString("| " + strings.Join(row, " | ") + " |\n")
		if i == 0 && header {
			w.b.WriteString("|" + strings.Repeat(" --- |", len(row)) + "\n")
		}
	}
	w.block(true)
}

var blankRunRE = regexp.MustCompile(`\n{3,}`)

// tidy trims trailing spaces, collapses inner space runs outside code fences
// and limits blank lines to one.
func tidy(s string) string {
	lines := strings.Split(s, "\n")
	fence := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fence = !fence
			lines[i] = strings.TrimRight(line, " \t")
			continue
		}
		if fence {
			lines[i] = strings.TrimRight(line, " \t")
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		lines[i] = line[:indent] + strings.TrimSpace(collapseSpace(line[indent:]))
		if strings.TrimSpace(lines[i]) == "" {
			lines[i] = ""
		}
	}
	return strings.TrimSpace(blankRunRE.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}
