package codingagent

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/coding/version"
	"github.com/alexrudloff/wopr/internal/codingagent/router"
	"github.com/alexrudloff/wopr/tui"
	"github.com/alexrudloff/wopr/tui/widthx"
)

// Sidebar padding: one row above and below, two columns each side.
const (
	sidebarPadX = 2
	sidebarPadY = 1
)

// fileChange is one modified file's added and removed line counts.
type fileChange struct {
	path           string
	added, removed int
}

// sidebar is the fullscreen session sidebar: the session title, context
// usage and routing at the top; the agents, a card rotating through usage
// and speed, the work queue, and the modified files below; the working
// directory and version at the bottom. When the lower sections do not fit,
// they split into pages with dots to switch between them.
type sidebar struct {
	tui.BaseComponent
	m *InteractiveMode
}

// sidebarCardPages is the number of pages of the rotating card.
const sidebarCardPages = 2

// sidebarPart is a section below the fixed ones: rows bounds its height, and
// rows <= 0 draws all of it.
type sidebarPart func(rows int) []string

// sidebarDotsRows is the height of the page dots: a blank row and the dots.
const sidebarDotsRows = 2

func (s *sidebar) Render(width int) []string { return s.RenderSized(width, 0) }

// RenderSized fills the whole column with the panel background, the sections
// at the top and the directory and version at the bottom.
func (s *sidebar) RenderSized(width, height int) []string {
	th := tui.ActiveTheme()
	side := &s.m.side
	inner := max(1, width-2*sidebarPadX)
	stacked, page := sidebarCardLayout(height, sidebarCardPages, side.pageBase, time.Since(side.pageEpoch))
	side.lastHeight, side.lastPage = height, page
	fixed := [][]string{s.titleSection(inner), s.contextSection(inner), s.routingSection(inner)}
	footer := s.footer(inner)
	now := time.Now()
	parts := []sidebarPart{
		func(rows int) []string { return s.agentsSection(inner, rows, now) },
		func(int) []string { return s.card(inner, stacked, page) },
		func(rows int) []string { return s.queueSection(inner, rows) },
		func(rows int) []string { return s.filesSection(inner, rows) },
	}
	const agentsPart, cardPart = 0, 1
	full := make([][]string, len(parts))
	heights := make([]int, len(parts))
	for i, part := range parts {
		full[i] = part(0)
		if len(full[i]) > 0 {
			heights[i] = len(full[i]) + 1
		}
	}
	budget := -1 // unknown height: everything on one page
	if height > 0 {
		used := -1 // the first section has no separator
		for _, section := range fixed {
			if len(section) > 0 {
				used += len(section) + 1
			}
		}
		budget = max(0, height-2*sidebarPadY-len(footer)-1-used)
	}
	pages := sidebarPages(budget, heights)
	side.panelPages = len(pages)
	current := pages[side.panelPage%len(pages)]
	dots := len(pages) > 1
	if dots {
		budget -= sidebarDotsRows
	}

	var body []string
	add := func(section []string) {
		if len(body) > 0 {
			body = append(body, "")
		}
		body = append(body, section...)
	}
	for _, section := range fixed {
		if len(section) > 0 {
			add(section)
		}
	}
	side.cardTop, side.cardRows, side.agentTop = 0, 0, -1
	left := budget
	for _, i := range current {
		section := full[i]
		if budget >= 0 && heights[i] > left {
			if i == cardPart || left < 3 {
				continue
			}
			section = parts[i](left - 1)
		}
		if len(section) == 0 {
			continue
		}
		left -= len(section) + 1
		switch i {
		case cardPart:
			if !stacked {
				side.cardTop, side.cardRows = sidebarPadY+len(body)+1, len(section)
			}
		case agentsPart:
			side.agentTop = sidebarPadY + len(body) + 1
		}
		add(section)
	}
	rows := make([]string, 0, max(height, len(body)+len(footer)+2*sidebarPadY+1))
	for range sidebarPadY {
		rows = append(rows, "")
	}
	rows = append(rows, body...)
	side.dotsRow = -1
	if height > 0 {
		reserve := len(footer) + sidebarPadY
		if dots {
			reserve += sidebarDotsRows
		}
		for len(rows) < height-reserve {
			rows = append(rows, "")
		}
	} else {
		rows = append(rows, "")
	}
	if dots {
		side.dotsRow = len(rows) + 1
		rows = append(rows, "", pageDots(len(pages), side.panelPage%len(pages)))
	}
	rows = append(rows, footer...)
	for range sidebarPadY {
		rows = append(rows, "")
	}
	if height > 0 && len(rows) > height {
		rows = rows[:height]
	}
	panel := th.Bg("backgroundPanel")
	pad := strings.Repeat(" ", sidebarPadX)
	for i, row := range rows {
		rows[i] = tui.FillBackground(pad+widthx.TruncateToWidth(row, inner, "…", false), width, panel)
	}
	return rows
}

// pageDots renders "● ○ ○" with the current page lit.
func pageDots(pages, current int) string {
	th := tui.ActiveTheme()
	dots := make([]string, pages)
	for i := range pages {
		if i == current {
			dots[i] = th.FgText("text", "●")
		} else {
			dots[i] = th.FgText("textMuted", "○")
		}
	}
	return strings.Join(dots, " ")
}

// HandleMouse turns the rotating card to its next page, switches the
// sidebar page on a click on its dots, and opens an agent clicked in the
// Agents section.
func (s *sidebar) HandleMouse(event tui.TuiMouseEvent) *tui.TuiMouseDispatchResult {
	side := &s.m.side
	if event.Type != tui.MouseClick {
		return nil
	}
	switch row := event.Y - side.agentTop; {
	case side.cardRows > 0 && event.Y >= side.cardTop && event.Y < side.cardTop+side.cardRows:
		side.pageBase, side.pageEpoch = (side.lastPage+1)%sidebarCardPages, time.Now()
	case side.dotsRow >= 0 && event.Y == side.dotsRow:
		s.m.nextSidebarPage()
	case side.agentTop >= 0 && row >= 0 && row < len(side.agentRowIDs) && side.agentRowIDs[row] != "":
		id := side.agentRowIDs[row]
		if strings.HasPrefix(id, "sh_") {
			s.m.postUITask(func() { s.m.openShellDialog(id) })
		} else {
			s.m.postUITask(func() { s.m.openAgentDialog(id) })
		}
	default:
		return nil
	}
	s.m.tuiInst.RequestRender()
	return &tui.TuiMouseDispatchResult{Handled: true}
}

// sidebarPages splits the lower sections, given their heights including the
// separator (0 for an empty section), into pages of at most budget rows.
// Everything is on one page when it fits or the height is unknown (budget <
// 0); otherwise each page also gives up room for the dots. Sections keep
// their order, and one too tall for a page gets a page of its own.
func sidebarPages(budget int, heights []int) [][]int {
	var all []int
	total := 0
	for i, h := range heights {
		if h > 0 {
			all = append(all, i)
			total += h
		}
	}
	if budget < 0 || total <= budget || len(all) < 2 {
		return [][]int{all}
	}
	room := budget - sidebarDotsRows
	var pages [][]int
	var page []int
	used := 0
	for _, i := range all {
		if len(page) > 0 && used+heights[i] > room {
			pages = append(pages, page)
			page, used = nil, 0
		}
		page = append(page, i)
		used += heights[i]
	}
	return append(pages, page)
}

// nextSidebarPage shows the sidebar's next page.
func (m *InteractiveMode) nextSidebarPage() {
	if m.side.panelPages > 1 {
		m.side.panelPage = (m.side.panelPage + 1) % m.side.panelPages
	}
	m.tuiInst.RequestRender()
}

func bold(text string) string { return "\x1b[1m" + text + tui.SGRBoldDimReset }

func (s *sidebar) titleSection(width int) []string {
	th := tui.ActiveTheme()
	title := s.m.sessionTitle()
	var out []string
	for _, line := range widthx.WrapTextWithAnsi(title, width) {
		out = append(out, bold(th.FgText("text", line)))
	}
	if len(out) > 3 {
		out = append(out[:2:2], bold(th.FgText("text", widthx.TruncateToWidth(widthx.StripAnsi(out[2]), width-1, "", false)+"…")))
	}
	return out
}

// contextSection draws the context window as a bar split into the system
// prompt, the tool definitions, and the conversation's cached and new parts,
// then the token count and the last reply's cache-hit rate.
func (s *sidebar) contextSection(width int) []string {
	th := tui.ActiveTheme()
	snap := s.m.statusLine.Snapshot()
	total := snap.contextTokens
	window := snap.contextWindow()
	pct := 0
	if window > 0 && !snap.contextUnknown {
		pct = int(math.Round(float64(total) / float64(window) * 100))
	}
	system := min(s.m.side.systemTokens, total)
	tools := min(s.m.side.toolTokens, total-system)
	cached := 0
	if rate := snap.usage.latestCacheHitRate; rate != nil {
		cached = int(float64(total) * *rate / 100)
	}
	cachedConversation := max(0, min(cached-system-tools, total-system-tools))
	segments := []struct {
		tokens int
		token  string
	}{
		{system, "secondary"},
		{tools, "info"},
		{cachedConversation, "success"},
		{total - system - tools - cachedConversation, "primary"},
	}
	scale := max(window, total, 1)
	var bar strings.Builder
	cells, sum := 0, 0
	for _, segment := range segments {
		sum += segment.tokens
		end := int(math.Round(float64(sum) / float64(scale) * float64(width)))
		if end > cells {
			bar.WriteString(th.FgText(segment.token, strings.Repeat("█", end-cells)))
			cells = end
		}
	}
	bar.WriteString(th.FgText("borderSubtle", strings.Repeat("░", max(0, width-cells))))
	legend := func(token, label string) string { return th.FgText(token, "■") + " " + th.FgText("textMuted", label) }
	out := []string{
		bold(th.FgText("text", "Context")),
		bar.String(),
		strings.Join([]string{legend("secondary", "system"), legend("info", "tools"), legend("success", "cached"), legend("primary", "new")}, " "),
		th.FgText("textMuted", fmt.Sprintf("%s tokens · %d%% used", formatNumber(total), pct)),
	}
	if rate := snap.usage.latestCacheHitRate; rate != nil {
		out = append(out, th.FgText("textMuted", fmt.Sprintf("%d%% cached", int(math.Round(*rate)))))
	}
	return out
}

// card renders the usage and speed pages: both stacked, or the
// given page with page dots, padded to the tallest page so the sections
// below keep their place.
func (s *sidebar) card(width int, stacked bool, page int) []string {
	th := tui.ActiveTheme()
	pages := []func(int) (string, []string){s.usagePage, s.speedPage, s.modelsPage}
	var out []string
	if stacked {
		for i, render := range pages {
			title, lines := render(width)
			if i > 0 {
				out = append(out, "")
			}
			out = append(out, bold(th.FgText("text", title)))
			out = append(out, lines...)
		}
		return out
	}
	tallest := 0
	var title string
	var lines []string
	for i, render := range pages {
		t, l := render(width)
		tallest = max(tallest, len(l))
		if i == page {
			title, lines = t, l
		}
	}
	out = append(out, spread(bold(th.FgText("text", title)), pageDots(len(pages), page), width))
	out = append(out, lines...)
	for len(out) < tallest+1 {
		out = append(out, "")
	}
	return out
}

// usagePage shows the subscription windows reported by the providers, the
// session's spend, and the estimated saving from routing.
func (s *sidebar) usagePage(width int) (string, []string) {
	th := tui.ActiveTheme()
	var out []string
	now := time.Now()
	barWidth := max(4, width-30)
	for _, plan := range ai.SubscriptionUsage() {
		for i, window := range plan.Windows {
			name := ""
			if i == 0 {
				name = plan.Plan
			}
			filled := int(math.Round(window.UsedPercent / 100 * float64(barWidth)))
			token := "success"
			switch {
			case window.UsedPercent >= 90:
				token = "error"
			case window.UsedPercent >= 70:
				token = "warning"
			}
			line := th.FgText("text", fmt.Sprintf("%-8s", name)) + th.FgText("textMuted", fmt.Sprintf("%-3s", window.Label)) +
				th.FgText(token, strings.Repeat("█", filled)) + th.FgText("borderSubtle", strings.Repeat("░", barWidth-filled)) +
				th.FgText("text", fmt.Sprintf(" %3.0f%%", window.UsedPercent))
			if !window.ResetsAt.IsZero() {
				line += th.FgText("textMuted", " · resets "+shortDuration(window.ResetsAt.Sub(now)))
			}
			out = append(out, line)
		}
	}
	var cost float64
	cost = s.m.statusLine.Snapshot().usage.cost
	if math.IsNaN(cost) {
		cost = 0
	}
	out = append(out, th.FgText("text", fmt.Sprintf("$%.2f", cost))+th.FgText("textMuted", " spent"))
	if saved := s.m.side.routingSaved; saved >= 0.005 {
		out = append(out, th.FgText("success", fmt.Sprintf("~$%.2f", saved))+th.FgText("textMuted", " saved by routing (est.)"))
	}
	return "Usage", out
}

// shortDuration renders a duration in its largest unit: "43m", "2h", "3d".
func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}

// routingSection shows who picks the orchestrator's and the subagents'
// models, the routing targets with their health and expected time to first
// token, the latest decision, and the keyed statuses. Without model
// routing it shows only the tasks, measuring, and keyed statuses.
func (s *sidebar) routingSection(width int) []string {
	th := tui.ActiveTheme()
	m := s.m
	label := func(text string) string { return th.FgText("textMuted", fmt.Sprintf("%-14s", text)) }
	// The subagents' line shows only when they don't follow the
	// orchestrator's choice.
	var subagents []string
	if m.subagentsDiffer() {
		value := th.FgText("text", m.choiceLabel(m.subagentChoice()))
		if _, mode := modeSpec(m.subagentChoice()); mode {
			value = hexFg(phosphorHex(), m.choiceLabel(m.subagentChoice()))
		}
		subagents = append(subagents, label("Subagents")+value)
	}
	r := m.sessionRouter()
	if r == nil || !r.Available() {
		return s.activityLines(width, subagents)
	}
	out := []string{bold(th.FgText("text", "Routing"))}
	orchestrator := label("Orchestrator")
	if m.routingEnabled() {
		orchestrator += hexFg(phosphorHex(), modeTitle(r.Objective()))
		if m.route.model != "" {
			orchestrator += th.FgText("textMuted", " · now ") + th.FgText("text", m.route.model)
		}
	} else {
		if model := m.statusLine.Snapshot().model; model != nil {
			name := cmp.Or(model.DisplayName, model.ID)
			orchestrator += th.FgText("text", name)
		}
	}
	out = append(out, orchestrator)
	if w := m.warSession(); w != nil && w.WarCouncil() {
		out = append(out, label("War council")+hexFg(warHex(), fmt.Sprintf("☢ %d members", w.WarCouncilSize())))
	}
	out = append(out, subagents...)
	var fleet []string
	for _, target := range m.side.targets {
		token := "textMuted"
		switch target.Health {
		case router.TargetUp:
			token = "success"
		case router.TargetDown:
			token = "error"
		}
		item := th.FgText(token, "●") + " " + th.FgText("text", m.targetName(target.Provider))
		if target.TTFT > 0 {
			item += " " + th.FgText("textMuted", formatDuration(target.TTFT))
		}
		fleet = append(fleet, item)
	}
	line := ""
	for _, item := range fleet {
		switch {
		case line == "":
			line = item
		case widthx.VisibleWidth(line)+2+widthx.VisibleWidth(item) <= width:
			line += "  " + item
		default:
			out = append(out, line)
			line = item
		}
	}
	if line != "" {
		out = append(out, line)
	}
	if d := m.side.decision; d.tier != "" {
		last := th.FgText("textMuted", "last: ") + th.FgText("text", d.tier+"/"+d.model)
		if reason := strings.Join(strings.Fields(d.reason), " "); reason != "" {
			last += th.FgText("textMuted", " · "+reason)
		}
		out = append(out, widthx.TruncateToWidth(last, width, "…", false))
	}
	return s.activityLines(width, out)
}

// activityLines appends the tasks, the model being measured, and the keyed
// statuses to out.
func (s *sidebar) activityLines(width int, out []string) []string {
	th := tui.ActiveTheme()
	m := s.m
	label := func(text string) string { return th.FgText("textMuted", fmt.Sprintf("%-14s", text)) }
	if side := &m.side; side.tasks > 0 {
		var line strings.Builder
		line.WriteString(label("Tasks") + th.FgText("text", fmt.Sprint(side.tasks)))
		for _, run := range side.taskRuns {
			line.WriteString(th.FgText("textMuted", fmt.Sprintf(" · %s %d", run.target, run.count)))
		}
		out = append(out, widthx.TruncateToWidth(line.String(), width, "…", false))
	}
	if active := m.measurer().active(); active != "" {
		out = append(out, th.FgText("textMuted", "• "+widthx.TruncateToWidth("Measuring "+active+"…", width-2, "…", false)))
	}
	statuses := m.statusLine.Snapshot().keyedStatuses
	for _, key := range slices.Sorted(maps.Keys(statuses)) {
		if key == savingsStatusKey || key == "route" {
			continue
		}
		if text := strings.Join(strings.Fields(statuses[key]), " "); text != "" {
			out = append(out, th.FgText("textMuted", "• "+widthx.TruncateToWidth(text, width-2, "…", false)))
		}
	}
	return out
}

// targetNames are the short names of well-known providers in the fleet row.
var targetNames = map[string]string{"openai-codex": "ChatGPT", "anthropic": "Claude", "openrouter": "OpenRouter"}

// targetName is a provider's short name: a known name, else its display name
// when that is one word before any parenthesis ("vllm (office box)"), else its
// ID.
func (m *InteractiveMode) targetName(provider string) string {
	if name, ok := targetNames[provider]; ok {
		return name
	}
	name, _, _ := strings.Cut(m.providerName(provider), " (")
	if name == "" || strings.Contains(name, " ") {
		return provider
	}
	return name
}

// modelsShown caps the models listed on the models page.
const modelsShown = 4

// modelsPage lists each model used this session, the orchestrator's replies
// and the subagents run on it: calls, tokens, cost, and the average speed of
// its timed replies. Costliest first.
func (s *sidebar) modelsPage(width int) (string, []string) {
	th := tui.ActiveTheme()
	speed := &s.m.side.speed
	session := s.m.crashSessionFile()
	type row struct {
		name          string
		calls, agents int
		tokens        int
		cost          float64
		costUnknown   bool
		timed         speedSums
	}
	rows := map[string]*row{}
	get := func(provider, model string) *row {
		key := provider + "/" + model
		if rows[key] == nil {
			rows[key] = &row{name: s.m.modelName(provider, model)}
		}
		return rows[key]
	}
	if speed.session == session {
		for _, u := range speed.models {
			r := get(u.provider, u.model)
			r.calls, r.tokens, r.cost, r.costUnknown, r.timed = u.calls, u.input+u.output, u.cost, u.costUnknown, u.timed
		}
	}
	if registry := s.m.agentsRegistry(); registry != nil {
		for _, a := range registry.List() {
			provider, model, ok := strings.Cut(a.ModelSpec, "/")
			if !ok {
				continue
			}
			r := get(provider, model)
			r.agents++
			r.tokens += a.Tokens
			r.cost += a.Cost
		}
	}
	if len(rows) == 0 {
		return "Models", []string{th.FgText("textMuted", "No replies yet")}
	}
	sorted := slices.SortedFunc(maps.Values(rows), func(a, b *row) int {
		if c := cmp.Compare(b.cost, a.cost); c != 0 {
			return c
		}
		return cmp.Compare(b.tokens, a.tokens)
	})
	var out []string
	for i, r := range sorted {
		if i == modelsShown {
			out = append(out, th.FgText("textMuted", fmt.Sprintf("+%d more", len(sorted)-i)))
			break
		}
		out = append(out, spread(th.FgText("text", widthx.TruncateToWidth(r.name, max(1, width-9), "…", false)), th.FgText("text", modelCostText(r.cost, r.costUnknown)), width))
		var parts []string
		if r.calls > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", r.calls, plural(r.calls, "reply", "replies")))
		}
		if r.agents > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", r.agents, plural(r.agents, "agent", "agents")))
		}
		parts = append(parts, formatTokens(r.tokens)+" tok")
		out = append(out, th.FgText("textMuted", widthx.TruncateToWidth(strings.Join(parts, " · "), width, "…", false)))
		if ttft, rate := r.timed.averages(); ttft > 0 {
			line := formatDuration(ttft) + " first token"
			if rate > 0 {
				line = fmt.Sprintf("%.1f tok/s · ", rate) + line
			}
			out = append(out, th.FgText("textMuted", widthx.TruncateToWidth(line, width, "…", false)))
		}
	}
	return "Models", out
}

// speedPage shows the last reply's speed, the session's averages, and the
// efficiency mechanisms' savings this session.
func (s *sidebar) speedPage(width int) (string, []string) {
	th := tui.ActiveTheme()
	side := &s.m.side
	var out []string
	speedLine := func(label string, ttft time.Duration, tokensPerSec float64) string {
		line := th.FgText("textMuted", fmt.Sprintf("%-5s", label))
		if tokensPerSec > 0 {
			line += th.FgText("text", fmt.Sprintf("%.1f tok/s", tokensPerSec)) + th.FgText("textMuted", " · ")
		}
		return line + th.FgText("text", formatDuration(ttft)) + th.FgText("textMuted", " to first token")
	}
	if side.ttft > 0 {
		out = append(out, speedLine("last", side.ttft, side.tokensPerSec))
		if side.speed.replies > 1 && side.speed.session == s.m.crashSessionFile() {
			ttft, tokensPerSec := side.speed.averages()
			line := speedLine("avg", ttft, tokensPerSec)
			if count := th.FgText("textMuted", fmt.Sprintf(" · %d replies", side.speed.replies)); widthx.VisibleWidth(line+count) <= width {
				line += count
			}
			out = append(out, line)
		}
	} else {
		out = append(out, th.FgText("textMuted", "No reply timed yet"))
	}
	if side.savings > 0 {
		line := th.FgText("text", formatTokens(side.savedTokens)+" tokens") + th.FgText("textMuted", " saved")
		line += th.FgText("textMuted", fmt.Sprintf(" · %d %s", side.savings, plural(side.savings, "saving", "savings")))
		out = append(out, line)
	}
	return "Speed & efficiency", out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// filesSection lists the modified files, the newest rows-1 when rows > 0.
func (s *sidebar) filesSection(width, rows int) []string {
	changes := s.m.modifiedFiles
	if len(changes) == 0 {
		return nil
	}
	if rows > 0 {
		changes = changes[max(0, len(changes)-(rows-1)):]
	}
	th := tui.ActiveTheme()
	out := []string{bold(th.FgText("text", "Modified Files"))}
	for _, change := range changes {
		var stats []string
		plain := 0
		if change.added > 0 {
			text := fmt.Sprintf("+%d", change.added)
			stats, plain = append(stats, th.FgText("diffAdded", text)), plain+len(text)+1
		}
		if change.removed > 0 {
			text := fmt.Sprintf("-%d", change.removed)
			stats, plain = append(stats, th.FgText("diffRemoved", text)), plain+len(text)+1
		}
		right := strings.Join(stats, " ")
		room := max(2, width-plain)
		name := change.path
		if widthx.VisibleWidth(name) > room {
			runes := []rune(name)
			name = "…" + string(runes[max(0, len(runes)-(room-1)):])
		}
		gap := max(1, width-widthx.VisibleWidth(name)-widthx.VisibleWidth(right))
		out = append(out, th.FgText("textMuted", name)+strings.Repeat(" ", gap)+right)
	}
	return out
}

// footer shows the working directory ("~/Sites/" muted, "harness:main" in
// text) with the uncommitted file count and the commits ahead of and behind
// the upstream, then the version.
func (s *sidebar) footer(width int) []string {
	th := tui.ActiveTheme()
	snap := s.m.statusLine.Snapshot()
	git := s.m.side.git
	var stats []string
	if git.changed > 0 {
		stats = append(stats, th.FgText("warning", fmt.Sprintf("±%d", git.changed)))
	}
	if git.ahead > 0 {
		stats = append(stats, th.FgText("textMuted", fmt.Sprintf("↑%d", git.ahead)))
	}
	if git.behind > 0 {
		stats = append(stats, th.FgText("textMuted", fmt.Sprintf("↓%d", git.behind)))
	}
	right := strings.Join(stats, " ")
	room := width
	if right != "" {
		room = max(1, width-widthx.VisibleWidth(right)-1)
	}
	dir := snap.cwd
	if home, err := os.UserHomeDir(); err == nil && dir != "" {
		dir = formatCwdForFooter(dir, home)
	}
	branch := cmp.Or(snap.gitBranch, git.branch)
	if branch != "" {
		dir += ":" + branch
	}
	dir = truncateLeft(dir, room)
	parent, name := "", dir
	if i := strings.LastIndex(dir, string(filepath.Separator)); i >= 0 {
		parent, name = dir[:i+1], dir[i+1:]
	}
	path := widthx.TruncateToWidth(th.FgText("textMuted", parent)+th.FgText("text", name), room, "…", false)
	if right != "" {
		path += " " + right
	}
	version := th.FgText("success", "•") + " " + bold(th.FgText("text", "WOPR")) + " " + th.FgText("textMuted", version.Version)
	if key := s.m.keyHint(appCommandPalette); key != "" {
		hint := th.FgText("text", key) + th.FgText("textMuted", " commands")
		if gap := width - widthx.VisibleWidth(version) - widthx.VisibleWidth(hint); gap >= 2 {
			version += strings.Repeat(" ", gap) + hint
		}
	}
	return []string{path, "", version}
}

// sessionTitle is the session's name, else its first prompt, else "New session".
func (m *InteractiveMode) sessionTitle() string {
	if name := m.statusLine.Snapshot().sessionName; name != "" {
		return name
	}
	if m.firstPrompt != "" {
		return strings.Join(strings.Fields(m.firstPrompt), " ")
	}
	return "New session"
}

// recordFileChange adds a finished edit's or write's line counts to the
// modified files list, newest change last.
func (m *InteractiveMode) recordFileChange(path string, added, removed int) {
	if path == "" {
		return
	}
	if rel, err := filepath.Rel(m.opts.CWD, path); err == nil && !strings.HasPrefix(rel, "..") {
		path = rel
	}
	for i, change := range m.modifiedFiles {
		if change.path == path {
			change.added += added
			change.removed += removed
			m.modifiedFiles = append(slices.Delete(m.modifiedFiles, i, i+1), change)
			return
		}
	}
	m.modifiedFiles = append(m.modifiedFiles, fileChange{path: path, added: added, removed: removed})
}

// recordToolFileChange records a successful edit's diff counts or a write's
// line count.
func (m *InteractiveMode) recordToolFileChange(comp *tui.ToolExecutionComponent, toolName string, details any) {
	path := cmp.Or(comp.Arg("path"), comp.Arg("file_path"))
	if path == "" {
		return
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(m.opts.CWD, path)
	}
	switch toolName {
	case "edit":
		if diff := extractDiffString(details); diff != "" {
			added, removed := tui.DiffStats(diff)
			m.recordFileChange(path, added, removed)
		}
	case "write":
		content := strings.TrimRight(comp.Arg("content"), "\n")
		lines := 0
		if content != "" {
			lines = strings.Count(content, "\n") + 1
		}
		m.recordFileChange(path, lines, 0)
	}
}

// modelCostText is a model's cost in the sidebar: "$?" when nothing gave
// its price, "$0.12+?" when only part of its usage was priced.
func modelCostText(cost float64, unknown bool) string {
	switch {
	case unknown && cost == 0:
		return "$?"
	case unknown:
		return fmt.Sprintf("$%.2f+?", cost)
	}
	return fmt.Sprintf("$%.2f", cost)
}
