package codingagent

import (
	"context"
	"time"

	"github.com/alexrudloff/wopr/internal/codingagent/router"
	"github.com/alexrudloff/wopr/tui"
)

// appCommandPalette opens the command palette.
const appCommandPalette = "app.commandPalette"

// leaderTimeout is how long a leader key press waits for its second key.
const leaderTimeout = 2 * time.Second

// interruptWindow is how long a first interrupt press stays armed; a second
// press inside it aborts the running turn.
const interruptWindow = 5 * time.Second

// routeInfo is the latest routing decision, shown as the prompt's model.
type routeInfo struct {
	provider, model, tier, thinking string
}

// keyHint returns the display text of the first key bound to action, or "".
func (m *InteractiveMode) keyHint(action string) string {
	keys := m.keybindings.resolved[action]
	if len(keys) == 0 {
		return ""
	}
	return tui.FormatKeyText(string(keys[0]), false)
}

// leaderPending reports whether a leader key press awaits its second key.
func (m *InteractiveMode) leaderPending() bool {
	return !m.leaderDeadline.IsZero() && time.Now().Before(m.leaderDeadline)
}

// interruptArmed reports whether one interrupt press is waiting for the
// confirming second press.
func (m *InteractiveMode) interruptArmed() bool {
	return !m.interruptDeadline.IsZero() && time.Now().Before(m.interruptDeadline)
}

// routingEnabled reports whether prompts are routed across models.
func (m *InteractiveMode) routingEnabled() bool {
	routed, ok := m.opts.SessionHandle.(interface{ Router() *router.Router })
	if !ok {
		return false
	}
	r := routed.Router()
	return r != nil && r.Auto()
}

// homeVisible reports whether the fullscreen home screen is showing: the
// transcript and the pending queue are both empty.
func (m *InteractiveMode) homeVisible() bool {
	return m.chatContainer.IsEmpty() &&
		(m.pendingMessagesContainer == nil || m.pendingMessagesContainer.IsEmpty())
}

// leaderKey starts a two-key sequence.
const leaderKey = "ctrl+x"

// armInterrupt waits for a confirming second interrupt press and repaints
// the hint when the window lapses.
func (m *InteractiveMode) armInterrupt() {
	m.interruptDeadline = time.Now().Add(interruptWindow)
	m.tuiInst.RequestRender()
	time.AfterFunc(interruptWindow, func() { m.postUITask(m.tuiInst.RequestRender) })
}

// handleLeaderKey handles the leader key and the key after it. It reports
// whether it consumed data.
func (m *InteractiveMode) handleLeaderKey(ctx context.Context, data string) bool {
	if !m.leaderPending() {
		if !tui.MatchesKeyID(data, leaderKey) {
			return false
		}
		m.leaderDeadline = time.Now().Add(leaderTimeout)
		m.tuiInst.RequestRender()
		time.AfterFunc(leaderTimeout, func() { m.postUITask(m.tuiInst.RequestRender) })
		return true
	}
	m.leaderDeadline = time.Time{}
	defer m.tuiInst.RequestRender()
	for _, binding := range leaderBindings {
		if tui.MatchesKeyID(data, binding.key) {
			binding.run(m, ctx)
			return true
		}
	}
	if tui.MatchesKeyID(data, questionKey) || tui.MatchesKeyID(data, "shift+/") {
		if m.pendingQuestion == nil {
			m.showFlash("No question is waiting")
		} else {
			m.openPendingQuestion()
		}
	}
	return true
}

// leaderSlash runs command from the leader table.
func leaderSlash(command string) func(*InteractiveMode, context.Context) {
	return func(m *InteractiveMode, _ context.Context) { m.dispatchSlash(command) }
}

// leaderBindings are the keys that follow the leader, in match order.
var leaderBindings = []struct {
	key string
	run func(*InteractiveMode, context.Context)
}{
	{"n", leaderSlash("/new")},
	{"l", leaderSlash("/resume")},
	{"m", func(m *InteractiveMode, _ context.Context) { m.handleModelPicker() }},
	{"t", func(m *InteractiveMode, _ context.Context) { m.openThemeDialog() }},
	{"b", func(m *InteractiveMode, _ context.Context) { m.toggleSidebar() }},
	{"c", leaderSlash("/compact")},
	{"e", (*InteractiveMode).openExternalEditor},
	{"x", leaderSlash("/export")},
	{"y", func(m *InteractiveMode, _ context.Context) { m.handleCopyCommand(true, true) }},
	{"s", leaderSlash("/session")},
	{"g", (*InteractiveMode).globalThermonuclearWar},
	{"j", leaderSlash("/tree")},
	{"h", func(m *InteractiveMode, _ context.Context) { m.toggleThinkingVisibility() }},
	{"p", (*InteractiveMode).openCommandPalette},
	{"a", func(m *InteractiveMode, _ context.Context) { m.pickAgent(false) }},
	{"o", func(m *InteractiveMode, _ context.Context) { m.nextSidebarPage() }},
	{"q", func(m *InteractiveMode, _ context.Context) { m.requestQuit() }},
}
