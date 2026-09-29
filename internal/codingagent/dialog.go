package codingagent

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/internal/codingagent/goal"
	"github.com/alexrudloff/wopr/internal/codingagent/router"
	"github.com/alexrudloff/wopr/tui"
)

// Dialog widths.
const (
	dialogMedium = 60
	dialogLarge  = 88
	// dialogBackdrop is the opacity of the black cover over the screen while
	// a dialog is open.
	dialogBackdrop = 150.0 / 255
)

// dialogFrame is a dialog's panel: one blank row, then the content, all on
// the panel background.
type dialogFrame struct {
	tui.BaseComponent
	child tui.Component
}

func (f *dialogFrame) Render(width int) []string {
	panel := tui.ActiveTheme().Bg("backgroundPanel")
	lines := f.child.Render(width)
	out := make([]string, 0, len(lines)+1)
	out = append(out, tui.FillBackground("", width, panel))
	for _, line := range lines {
		out = append(out, tui.FillBackground(line, width, panel))
	}
	return out
}

// HandleMouse passes a mouse event to the content, below the blank row.
func (f *dialogFrame) HandleMouse(event tui.TuiMouseEvent) *tui.TuiMouseDispatchResult {
	handler, ok := f.child.(tui.MouseHandler)
	if !ok {
		return nil
	}
	event.Y--
	return handler.HandleMouse(event)
}

// dialogBackdropState reports the backdrop for the renderer: dimmed while a
// dialog is open.
func (m *InteractiveMode) dialogBackdropState() (float64, string) {
	if m.dialogDepth > 0 {
		return dialogBackdrop, themeHex("text")
	}
	return 0, ""
}

// dialogOverlayOptions places a dialog of the given width a quarter of the
// way down a terminal.
func dialogOverlayOptions(width, termWidth, termHeight int) tui.OverlayOptions {
	return tui.OverlayOptions{
		Width:  max(20, min(width, termWidth-2)),
		Anchor: "top-center",
		Margin: tui.OverlayMargin{Top: termHeight / 4},
	}
}

// runDialog shows md's component as a dialog a quarter of the way down the
// screen, over a dimmed backdrop, and feeds it input until done. It returns
// false when stop closes or input ends first.
func (m *InteractiveMode) runDialog(md modal, width int) bool {
	frame := &dialogFrame{child: md.component}
	handle := m.tuiInst.OpenOverlay(frame, dialogOverlayOptions(width, m.tuiInst.Width(), m.tuiInst.Height()))
	m.dialogDepth++
	defer func() {
		m.dialogDepth--
		handle.Close()
		m.tuiInst.RequestRender()
	}()
	_, mouse := md.component.(tui.MouseHandler)
	return m.feedModal(md, mouse)
}

// runDialogSelect runs a DialogSelect and returns the chosen option.
func (m *InteractiveMode) runDialogSelect(d *tui.DialogSelect, width int) (tui.DialogOption, bool) {
	d.TermHeight = func() int { return m.tuiInst.Height() }
	if !m.runDialog(modalOf(d), width) || d.Cancelled() {
		return tui.DialogOption{}, false
	}
	return d.Chosen(), true
}

// paletteCommand is one command palette entry.
type paletteCommand struct {
	title, category, key string
	suggested            bool
	run                  func()
}

// openCommandPalette lists every action and command, grouped by category,
// suggested ones first, and runs the chosen one.
func (m *InteractiveMode) openCommandPalette(ctx context.Context) {
	slash := func(command string) func() { return func() { m.dispatchSlash(command) } }
	leader := func(key string) string { return "ctrl+x " + key }
	sidebarTitle := "Hide sidebar"
	if !m.sidebarShown(m.tuiInst.Width()) {
		sidebarTitle = "Show sidebar"
	}
	thinkingTitle := "Hide thinking"
	if m.hideThinking {
		thinkingTitle = "Show thinking"
	}
	routerTitle := "Turn routing off"
	if !m.routingEnabled() {
		routerTitle = "Turn routing on"
	}
	commands := []paletteCommand{
		{title: "Switch session", category: "Session", key: leader("l"), suggested: true, run: slash("/resume")},
		{title: "New session", category: "Session", key: leader("n"), suggested: !m.homeVisible(), run: slash("/new")},
		{title: "Switch model", category: "Agent", key: leader("m"), suggested: true, run: m.handleModelPicker},
		{title: "Rename session", category: "Session", run: slash("/name")},
		{title: "Jump to message", category: "Session", key: leader("j"), run: slash("/tree")},
		{title: "Global Thermonuclear War", category: "Agent", key: leader("g"), run: func() { m.globalThermonuclearWar(ctx) }},
		{title: "Fork session", category: "Session", run: slash("/fork")},
		{title: "Undo last change", category: "Session", run: slash("/undo")},
		{title: "Compact session", category: "Session", key: leader("c"), run: slash("/compact")},
		{title: "Copy last assistant message", category: "Session", key: leader("y"), run: slash("/copy")},
		{title: "Export session transcript", category: "Session", key: leader("x"), run: slash("/export")},
		{title: "Share session", category: "Session", run: slash("/share")},
		{title: "Session info", category: "Session", key: leader("s"), run: slash("/session")},
		{title: sidebarTitle, category: "Session", key: leader("b"), run: m.toggleSidebar},
		{title: "Toggle tool details", category: "Session", key: m.keyHint("app.tools.expand"), run: m.toggleAllTools},
		{title: thinkingTitle, category: "Session", key: leader("h"), run: m.toggleThinkingVisibility},
		{title: "Open editor", category: "Session", key: leader("e"), run: func() { m.openExternalEditor(ctx) }},
		{title: "Cycle thinking level", category: "Agent", key: m.keyHint("app.thinking.cycle"), run: m.cycleThinkingLevel},
		{title: "Next model", category: "Agent", key: m.keyHint("app.model.cycleForward"), run: func() { m.cycleModel(true) }},
		{title: "Switch theme", category: "System", key: leader("t"), run: m.openThemeDialog},
		{title: "Settings", category: "System", run: slash("/settings")},
		{title: "Connect provider", category: "System", run: slash("/login")},
		{title: "Disconnect provider", category: "System", run: slash("/logout")},
		{title: "Keyboard shortcuts", category: "System", run: slash("/hotkeys")},
		{title: "Reload resources", category: "System", run: slash("/reload")},
		{title: "Upgrade " + AppName, category: "System", run: slash("/upgrade")},
		{title: "Changelog", category: "System", run: slash("/changelog")},
		{title: "Exit the app", category: "System", key: leader("q"), run: slash("/quit")},
	}
	if r := m.sessionRouter(); r != nil && r.Available() {
		commands = append(commands,
			paletteCommand{title: routerTitle, category: "Agent", run: func() {
				if m.routingEnabled() {
					m.dispatchSlash("/router off")
				} else {
					m.dispatchSlash("/router on")
				}
			}},
			paletteCommand{title: "Routing status", category: "Agent", run: slash("/router status")})
	}
	if registry := m.agentsRegistry(); registry != nil {
		commands = append(commands, paletteCommand{title: "Show agent…", category: "Agent", key: leader("a"), run: func() { m.pickAgent(false) }})
		if len(registry.Running()) > 0 {
			commands = append(commands, paletteCommand{title: "Stop agent…", category: "Agent", suggested: true, run: func() { m.pickAgent(true) }})
		}
	}
	if m.pendingQuestion != nil {
		commands = append(commands, paletteCommand{title: "Answer waiting question", category: "Agent", key: leader(questionKey), suggested: true, run: m.openPendingQuestion})
	}
	if host := m.goalHost(); host != nil {
		if st, ok := host.Goal(); ok && st.Status == goal.Paused {
			commands = append(commands, paletteCommand{title: "Resume goal", category: "Agent", suggested: true, run: slash("/goal resume")})
		}
		if st, ok := host.Goal(); ok && st.Open() {
			commands = append(commands, paletteCommand{title: "Stop goal", category: "Agent", run: slash("/goal stop")})
		}
	}
	if n := m.interruptedAgents(); n > 0 {
		commands = append(commands, paletteCommand{title: "Re-dispatch interrupted agents", category: "Agent", suggested: true, run: func() { m.redispatchInterrupted(ctx) }})
	}
	if m.side.panelPages > 1 {
		commands = append(commands, paletteCommand{title: "Next sidebar page", category: "Session", key: leader("o"), run: m.nextSidebarPage})
	}
	for _, template := range m.promptTemplates {
		commands = append(commands, paletteCommand{title: "/" + template.Name, category: "Commands", run: slash("/" + template.Name)})
	}

	runs := make(map[string]func(), len(commands))
	var suggested, rest []tui.DialogOption
	for i, command := range commands {
		value := command.category + "/" + command.title
		runs[value] = command.run
		option := tui.DialogOption{Title: command.title, Category: command.category, Footer: command.key, Value: value}
		rest = append(rest, option)
		if command.suggested {
			option.Category = "Suggested"
			option.Value = "suggested:" + value
			runs[option.Value] = commands[i].run
			suggested = append(suggested, option)
		}
	}
	dialog := tui.NewDialogSelect("Commands", append(suggested, rest...), "")
	dialog.Filter = func(query string, options []tui.DialogOption) []tui.DialogOption {
		if query == "" {
			return options
		}
		return tui.FuzzyFilter(slices.DeleteFunc(slices.Clone(options), func(o tui.DialogOption) bool {
			return strings.HasPrefix(o.Value, "suggested:")
		}), query, func(o tui.DialogOption) string { return o.Title + " " + o.Category })
	}
	chosen, ok := m.runDialogSelect(dialog, dialogMedium)
	if !ok {
		return
	}
	if run := runs[chosen.Value]; run != nil {
		run()
		m.tuiInst.RequestRender()
	}
}

// pickModelDialog lists the configured models (those set up and checked in
// /setup) grouped by connection in setup's order, the routing modes first
// while routing is on, the current one marked; it returns the chosen
// model's spec or mode.
func (m *InteractiveMode) pickModelDialog(initialQuery string) (string, bool) {
	configured := m.configuredModels()
	names := map[string]int{}
	for _, c := range configured {
		names[c.name]++
	}
	var options []tui.DialogOption
	for _, c := range configured {
		provider, id := splitSpec(c.spec)
		footer, description := "", ""
		if m.freeProvider(provider) {
			footer = "Free"
		}
		if names[c.name] > 1 && c.name != id {
			// Models that share a name are told apart by id.
			description = id
		}
		options = append(options, tui.DialogOption{Title: c.name, Description: description, Category: c.group, Footer: footer, Value: c.spec})
	}
	current := modelSpec(m.opts.Model)
	if r := m.sessionRouter(); r != nil {
		var modes []tui.DialogOption
		for _, o := range r.Objectives() {
			modes = append(modes, tui.DialogOption{Title: modeTitle(o), Category: "Modes", Description: modeDescriptions[o], Value: string(o)})
		}
		options = append(modes, options...)
		if m.routingEnabled() {
			current = string(r.Objective())
		}
	}
	if len(options) == 0 {
		m.showToast("warning", "", "No models set up yet: run /setup from the home screen to connect some.")
		return "", false
	}
	dialog := tui.NewDialogSelect("Select model", options, current)
	dialog.Flat = true
	if initialQuery != "" {
		dialog.HandleInput(initialQuery)
	}
	chosen, ok := m.runDialogSelect(dialog, dialogMedium)
	if !ok {
		return "", false
	}
	return chosen.Value, true
}

// modeDescriptions describe the model picker's routing modes.
var modeDescriptions = map[router.Objective]string{
	router.ObjectiveAuto:       "A model for each prompt from your order, and routed subagents",
	router.ObjectiveCost:       "Free models first, subscription at most; never pay-per-token",
	router.ObjectiveSpeed:      "The fastest model capable enough, with less thinking",
	router.ObjectiveQuality:    "A model near the strongest available, with more thinking",
	router.ObjectiveUncensored: "Uncensored models only, orchestrator and subagents",
	router.ObjectivePrivate:    "Privacy Safe models only, for everything; never falls back",
}

// modeTitle is a routing mode's name in the model picker.
func modeTitle(o router.Objective) string {
	return strings.ToUpper(string(o[:1])) + string(o[1:])
}

// modeBadge is the routing mode as the prompt shows it; private mode
// carries a lock so it is never missed.
func modeBadge(o router.Objective) string {
	if o == router.ObjectivePrivate {
		return "🔒 private"
	}
	return string(o)
}

// selectRoutingMode lets the router pick the orchestrator under mode o and
// routes the subagents under it too;
// flash reports the choice.
func (m *InteractiveMode) selectRoutingMode(o router.Objective, flash bool) {
	r := m.sessionRouter()
	if r == nil {
		return
	}
	if err := r.SetObjective(o); err != nil {
		m.showToast("error", "", err.Error())
		return
	}
	// A mode for the orchestrator is the subagents' mode too, until
	// /model → Subagents splits them again.
	if c := m.subagentChooser(); c != nil {
		_ = c.SetSubagentChoice(string(o))
	}
	if flash {
		m.showFlash(modeTitle(o) + ": " + modeDescriptions[o])
	}
	m.saveRouting()
	m.tuiInst.RequestRender()
}

// saveRouting records the routing choice in the session, so resuming it
// routes the same way, and in settings, so the next start restores it: the
// mode while the router picks the orchestrator, else "pinned" or "off" with
// the orchestrating model as the default model. Without routing only the
// model is saved.
func (m *InteractiveMode) saveRouting() {
	if m.tabbing {
		m.routingSavePending = true
		return
	}
	m.routingSavePending = false
	if recorder, ok := m.opts.SessionHandle.(interface{ RecordRouting() }); ok {
		recorder.RecordRouting()
	}
	r := m.sessionRouter()
	if r == nil || m.opts.SettingsManager == nil {
		return
	}
	choice := string(router.ModePinned)
	if r.Auto() {
		choice = string(r.Objective())
	}
	routing := r.Available()
	subagents := m.subagentChoice()
	model := m.opts.Model
	_ = m.opts.SettingsManager.UpdateGlobal(func(s *Settings) {
		if routing {
			s.Routing = choice
		}
		s.Subagents = subagents
		if choice == string(router.ModePinned) {
			if model != nil {
				s.DefaultModel = model.ID
				s.DefaultProvider = model.ProviderMeta.ProviderID
				if s.DefaultProvider == "" && model.Provider != nil {
					s.DefaultProvider = model.Provider.ID()
				}
			}
		}
	})
}

// pinOrchestrator makes the model the user picked the orchestrator. The
// pick overrides any routing mode: the mode goes back to auto, so subagents
// keep routing through Jev.
func (m *InteractiveMode) pinOrchestrator() {
	r := m.sessionRouter()
	if r == nil {
		return
	}
	if o := r.Objective(); r.Auto() && o != router.ObjectiveAuto {
		m.showFlash(modeTitle(o) + " mode off for the orchestrator: your model orchestrates")
	}
	r.PinOrchestrator()
}

// freeProvider reports whether a provider runs on the user's own
// hardware: routing lists it in a free-local or free-remote tier.
func (m *InteractiveMode) freeProvider(id string) bool {
	r := m.sessionRouter()
	if r == nil {
		return false
	}
	for _, tier := range r.Config().Tiers {
		if tier.Cost != router.CostFreeLocal && tier.Cost != router.CostFreeRemote {
			continue
		}
		for _, ref := range tier.Models {
			if ref.Provider == id {
				return true
			}
		}
	}
	return false
}

// automaticTheme is the theme setting that follows the terminal's light or
// dark appearance.
const automaticTheme = "light/dark"

// pickThemeDialog lists the themes and Automatic (light/dark), previews the
// highlighted one live, and returns the chosen setting; dismissing restores
// the previous theme.
func (m *InteractiveMode) pickThemeDialog() (string, bool) {
	original := m.settings().Theme
	current := cmp.Or(original, tui.DefaultThemeName)
	automatic := automaticTheme
	if _, _, ok := tui.ParseAutoThemeSetting(original); ok {
		automatic = original
	}
	options := []tui.DialogOption{{Title: "Automatic (light/dark)", Value: automatic}}
	for _, name := range tui.ActiveThemeRegistry().Names() {
		if name == "dark" || name == "light" {
			continue
		}
		options = append(options, tui.DialogOption{Title: name, Value: name})
	}
	dialog := tui.NewDialogSelect("Themes", options, current)
	dialog.OnMove = func(option tui.DialogOption) {
		tui.SetThemeSetting(option.Value)
		m.tuiInst.ForceFullRender()
	}
	chosen, ok := m.runDialogSelect(dialog, dialogMedium)
	tui.SetThemeSetting(original)
	m.tuiInst.ForceFullRender()
	return chosen.Value, ok
}

// openThemeDialog lets the user pick a theme and saves it.
func (m *InteractiveMode) openThemeDialog() {
	chosen, ok := m.pickThemeDialog()
	if !ok {
		return
	}
	tui.SetThemeSetting(chosen)
	if m.opts.SettingsManager != nil {
		_ = m.opts.SettingsManager.SetTheme(chosen)
	}
	m.tuiInst.ForceFullRender()
}

// Sidebar modes: shown when the terminal is wide enough, always, or never.
const (
	sidebarAuto = ""
	sidebarShow = "show"
	sidebarHide = "hide"
)

// sidebarShown reports whether the sidebar is shown at a terminal width.
func (m *InteractiveMode) sidebarShown(width int) bool {
	switch m.sidebarMode {
	case sidebarShow:
		return true
	case sidebarHide:
		return false
	}
	return width >= sidebarMinTerminalWidth
}

// toggleSidebar hides a visible sidebar or shows a hidden one, and saves
// the choice. The first hide says how to bring it back.
func (m *InteractiveMode) toggleSidebar() {
	if m.sidebarShown(m.tuiInst.Width()) {
		m.sidebarMode = sidebarHide
		if !m.side.hideHintShown {
			m.side.hideHintShown = true
			m.showFlash("Sidebar hidden · ctrl+x b to show")
		}
	} else {
		m.sidebarMode = sidebarShow
	}
	if m.opts.SettingsManager != nil {
		mode := m.sidebarMode
		_ = m.opts.SettingsManager.UpdateGlobal(func(s *Settings) { s.FullscreenSidebar = mode })
	}
	m.tuiInst.RequestRender()
}

// sessionOptions lists sessions newest first, grouped by day with their time
// on the right.
func sessionOptions(infos []SessionInfo) []tui.DialogOption {
	slices.SortStableFunc(infos, func(a, b SessionInfo) int { return b.Modified.Compare(a.Modified) })
	options := make([]tui.DialogOption, 0, len(infos))
	for _, info := range infos {
		title := cmp.Or(info.Name, strings.Join(strings.Fields(info.FirstMessage), " "), "New session")
		options = append(options, tui.DialogOption{
			Title:    title,
			Category: sessionDay(info.Modified),
			Footer:   info.Modified.Format("3:04 PM"),
			Value:    info.Path,
		})
	}
	return options
}

// pickSessionDialog lists this folder's sessions; ctrl+r renames the
// highlighted one and ctrl+d deletes it.
func (m *InteractiveMode) pickSessionDialog(sm *SessionManager) (string, bool) {
	current := ""
	if session := m.currentSession(); session != nil {
		current = session.Path()
	}
	load := func() []tui.DialogOption {
		infos, err := sm.ListCurrentSessions()
		if err != nil {
			return nil
		}
		return sessionOptions(infos)
	}
	highlight := current
	for {
		var rename *tui.DialogOption
		dialog := tui.NewDialogSelect("Sessions", load(), current)
		dialog.Select(highlight)
		dialog.Actions = []tui.DialogAction{{
			Title: "Rename",
			Key:   "ctrl+r",
			Run: func(option tui.DialogOption) {
				rename = &option
				dialog.Dismiss()
			},
		}, {
			Title: "Delete",
			Key:   "ctrl+d",
			Run: func(option tui.DialogOption) {
				if option.Value == current {
					return
				}
				if err := sm.DeleteSession(option.Value); err == nil {
					dialog.SetOptions(load())
				}
			},
		}}
		chosen, ok := m.runDialogSelect(dialog, dialogLarge)
		if rename == nil {
			if !ok || chosen.Value == "" {
				return "", false
			}
			return chosen.Value, true
		}
		// Renaming swaps the list for a prompt, then returns to it.
		highlight = rename.Value
		name, ok := m.promptDialog("Rename session", rename.Title)
		if name = strings.TrimSpace(name); !ok || name == "" {
			continue
		}
		if err := sm.RenameSession(rename.Value, name); err != nil {
			m.showError(err.Error())
			continue
		}
		if rename.Value == current {
			m.statusLine.SetName(name)
		}
	}
}

// dialogPrompt is a one-line text entry in a dialog: a bold title with "esc"
// on the right, the input, and the enter hint.
type dialogPrompt struct {
	tui.BaseComponent
	title           string
	input           *tui.TextInput
	done, cancelled bool
}

func newDialogPrompt(title, value string) *dialogPrompt {
	prompt := ""
	p := &dialogPrompt{title: title, input: tui.NewInput(tui.InputOptions{Prompt: &prompt})}
	p.input.SetText(value)
	p.input.OnSubmit = func(string) { p.done = true }
	p.input.OnEscape = func() { p.done, p.cancelled = true, true }
	return p
}

func (p *dialogPrompt) HandleInput(data string) {
	p.input.HandleInput(data)
	p.Invalidate()
}

func (p *dialogPrompt) Done() bool { return p.done }

func (p *dialogPrompt) Render(width int) []string {
	th := tui.ActiveTheme()
	pad := strings.Repeat(" ", 4)
	inner := max(1, width-8)
	out := []string{pad + spread(bold(th.FgText("text", p.title)), th.FgText("textMuted", "esc"), inner), ""}
	p.input.Focused = true
	for _, line := range p.input.Render(inner) {
		out = append(out, pad+line)
	}
	return append(out, "", pad+th.FgText("text", "Save")+" "+th.FgText("textMuted", "enter"), "")
}

// promptDialog asks for one line of text, starting from value.
func (m *InteractiveMode) promptDialog(title, value string) (string, bool) {
	p := newDialogPrompt(title, value)
	if !m.runDialog(modalOf(p), dialogLarge) || p.cancelled {
		return "", false
	}
	return p.input.Text(), true
}

// sessionDay names the day a session was last active: Today, Yesterday, or
// the date.
func sessionDay(t time.Time) string {
	now := time.Now()
	day := func(t time.Time) time.Time {
		y, mo, d := t.Date()
		return time.Date(y, mo, d, 0, 0, 0, 0, t.Location())
	}
	switch day(now).Sub(day(t)) {
	case 0:
		return "Today"
	case 24 * time.Hour:
		return "Yesterday"
	}
	return t.Format("Mon Jan 2, 2006")
}
