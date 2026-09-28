package codingagent

import (
	"context"
	"math/rand/v2"
	"os"
	"os/user"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/coding/version"
	"github.com/alexrudloff/wopr/internal/codingagent/router"
	"github.com/alexrudloff/wopr/tui"
	"github.com/alexrudloff/wopr/tui/widthx"
)

// The home screen: a drawing of the WOPR machine with its live lamps, the
// greeting beneath it, the prompt, a tip, and the directory and version.
// Terminals too small for the machine show the WOPR wordmark instead.

// Tagline is wopr's tagline, the question WOPR asks in WarGames.
const Tagline = "SHALL WE PLAY A GAME?"

// Home screen geometry and timing.
const (
	homePromptMaxWidth = 84
	homeTipsPadTop     = 3
	lampGroupWidth     = 4
	greetingDelay      = 300 * time.Millisecond
	greetingStep       = 45 * time.Millisecond
	greetingHold       = time.Second
	cursorBlink        = 530 * time.Millisecond
)

// WOPR console colors.
const (
	phosphorBlue = "#8fd8e8"
	lampRed      = "#ff2a1a"
	lampOrange   = "#ff7b22"
	lampYellow   = "#ffc43d"
	warRed       = "#ff4a3d"
)

func hexFg(hex, text string) string { return tui.ThemeHexFg(hex) + text + tui.SGRFgReset }

// phosphorHex is the WOPR terminal's blue on dark themes and the theme's
// info color on light ones.
func phosphorHex() string {
	colors := tui.ActiveTheme().Colors()
	if isDarkHex(colors["background"]) {
		return phosphorBlue
	}
	return colors["info"]
}

// isDarkHex reports whether a hex color's luminance is under one half.
func isDarkHex(hex string) bool {
	digits := strings.TrimPrefix(hex, "#")
	value, err := strconv.ParseUint(digits, 16, 32)
	if err != nil || len(digits) != 6 {
		return true
	}
	r, g, b := float64(value>>16&0xff), float64(value>>8&0xff), float64(value&0xff)
	return 0.299*r+0.587*g+0.114*b < 128
}

// phosphorCursor draws the prompt cursor as the WOPR terminal's blinking
// block: dark text on phosphor blue while lit, the plain cell while dark.
func (m *InteractiveMode) phosphorCursor(cell string) string {
	if m.cursorLit() {
		return tui.ThemeHexBg(phosphorHex()) + tui.ThemeHexFg(tui.ActiveTheme().Colors()["background"]) + widthx.StripAnsi(cell) + "\x1b[0m"
	}
	return cell
}

// cursorLit reports whether the blinking cursor is in its lit half.
func (m *InteractiveMode) cursorLit() bool {
	return (time.Since(m.cursorEpoch)/cursorBlink)%2 == 0
}

// The WOPR machine, drawn on a grid of colored cells: a long console with a
// dark lamp window under its rounded housing and two doors and an access panel in
// its base, and a taller tower with a dot-matrix display, the WOPR label, a
// door, and a rounded side bump, on a dark plinth.
const (
	machineWidth     = 80
	machineHeight    = 13
	machineMinHeight = 40 // terminal rows the machine needs beside the prompt
	machineBody      = "#3d3d42"
	machineSeam      = "#26262a"
	machineHandle    = "#b8b8bc"
	machinePlinth    = "#1c1c1f"
	machinePanel     = "#111113"
	machineLampOff   = "#3a2418"
	machineWindow    = "#0c0c0d"
	machineLabel     = "#f2f2f2"
	machineDimLED    = "#3a2418"
)

// cell is one grid position: a glyph with foreground and background colors
// ("" is the screen's own).
type cell struct {
	ch     rune
	fg, bg string
	bold   bool
}

type cellGrid [][]cell

func newCellGrid(width, height int) cellGrid {
	g := make(cellGrid, height)
	for y := range g {
		g[y] = slices.Repeat([]cell{{ch: ' '}}, width)
	}
	return g
}

func (g cellGrid) set(x, y int, ch rune, fg, bg string) {
	if y >= 0 && y < len(g) && x >= 0 && x < len(g[y]) {
		g[y][x] = cell{ch: ch, fg: fg, bg: bg}
	}
}

// bold marks cells x0..x1 of row y bold.
func (g cellGrid) bold(x0, x1, y int) {
	for x := x0; x <= x1 && y >= 0 && y < len(g) && x < len(g[y]); x++ {
		g[y][x].bold = true
	}
}

func (g cellGrid) fill(x0, y0, x1, y1 int, bg string) {
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			g.set(x, y, ' ', "", bg)
		}
	}
}

func (g cellGrid) text(x, y int, s, fg, bg string) {
	for i, r := range []rune(s) {
		g.set(x+i, y, r, fg, bg)
	}
}

// roundTop draws a rounded top edge over columns x0..x1 of row y.
func (g cellGrid) roundTop(x0, x1, y int, color string) {
	g.set(x0, y, '▗', color, "")
	for x := x0 + 1; x < x1; x++ {
		g.set(x, y, '▄', color, "")
	}
	g.set(x1, y, '▖', color, "")
}

// box draws a thin outline in fg on the body color.
func (g cellGrid) box(x0, y0, x1, y1 int, fg string) {
	for x := x0 + 1; x < x1; x++ {
		g.set(x, y0, '─', fg, machineBody)
		g.set(x, y1, '─', fg, machineBody)
	}
	for y := y0 + 1; y < y1; y++ {
		g.set(x0, y, '│', fg, machineBody)
		g.set(x1, y, '│', fg, machineBody)
	}
	g.set(x0, y0, '┌', fg, machineBody)
	g.set(x1, y0, '┐', fg, machineBody)
	g.set(x0, y1, '└', fg, machineBody)
	g.set(x1, y1, '┘', fg, machineBody)
}

func (g cellGrid) lines() []string {
	out := make([]string, len(g))
	for y, row := range g {
		var b strings.Builder
		fg, bg, bold := "", "", false
		for _, c := range row {
			if c.bold != bold {
				if c.bold {
					b.WriteString("\x1b[1m")
				} else {
					b.WriteString(tui.SGRBoldDimReset)
				}
				bold = c.bold
			}
			if c.bg != bg {
				if c.bg == "" {
					b.WriteString(tui.SGRBgReset)
				} else {
					b.WriteString(tui.ThemeHexBg(c.bg))
				}
				bg = c.bg
			}
			if c.ch != ' ' && c.fg != fg {
				b.WriteString(tui.ThemeHexFg(c.fg))
				fg = c.fg
			}
			b.WriteRune(c.ch)
		}
		b.WriteString(tui.SGRFgReset + tui.SGRBgReset + tui.SGRBoldDimReset)
		out[y] = b.String()
	}
	return out
}

// woprMachine draws the machine at this moment; every LED blinks on its own
// rhythm (see ledColor).
func (m *InteractiveMode) woprMachine() []string {
	g := newCellGrid(machineWidth, machineHeight)
	body := machineBody
	// Tower with its rounded top, and the side bump.
	g.roundTop(51, 76, 0, body)
	g.fill(50, 1, 77, 11, body)
	g.roundTop(78, 79, 5, body)
	g.fill(78, 6, 79, 11, body)
	// Console housing and base.
	g.roundTop(3, 49, 3, body)
	g.fill(2, 4, 49, 8, body)
	g.fill(0, 9, 49, 11, body)
	for x := 1; x < 49; x++ {
		g.set(x, 8, '▁', machineSeam, body)
	}
	for x := 50; x < 78; x++ {
		g.set(x, 8, '▁', machineSeam, body)
	}
	// Lamp window.
	g.fill(5, 5, 46, 7, machinePanel)
	now := time.Now()
	const lampBits = 32
	for r := range 3 {
		x := 6
		for c := range lampBits {
			if c > 0 && c%lampGroupWidth == 0 {
				x++
			}
			g.set(x, 5+r, '●', m.ledColor(uint64(r*lampBits+c), now, lampPalette), machinePanel)
			x++
		}
	}
	// Tower display: a dot-matrix face, two eyes above a wide mouth, with a
	// line of LEDs down each side.
	g.fill(55, 1, 72, 4, machineWindow)
	led := func(x, y int) { g.set(x, y, '●', m.ledColor(uint64(1000+y*100+x), now, facePalette), machineWindow) }
	for y := 1; y <= 2; y++ {
		for x := 58; x <= 60; x++ {
			led(x, y)
		}
		for x := 67; x <= 69; x++ {
			led(x, y)
		}
	}
	for y := 3; y <= 4; y++ {
		for x := 62; x <= 65; x++ {
			led(x, y)
		}
	}
	for y := 1; y <= 3; y++ {
		led(56, y)
		led(71, y)
	}
	// WOPR and what it stands for, in white on the machine.
	g.text(62, 6, "WOPR", machineLabel, body)
	g.bold(62, 65, 6)
	g.text(50+(28-27)/2, 7, "War Operation Plan Response", machineLabel, body)
	// Doors, handles, and the access panel.
	for _, door := range [][2]int{{3, 18}, {21, 36}, {53, 74}} {
		g.box(door[0], 9, door[1], 11, machineSeam)
		g.set((door[0]+door[1])/2, 10, '◎', machineHandle, body)
	}
	g.box(40, 9, 45, 11, machineSeam)
	g.set(41, 10, '·', "#8a7a50", body)
	g.set(44, 10, '·', "#8a7a50", body)
	// Plinth shadow.
	for x := 1; x < 79; x++ {
		g.set(x, 12, '▀', machinePlinth, "")
	}
	return g.lines()
}

// LED palettes: the lamp panel runs red, orange, and yellow; the face runs
// mostly red with orange and yellow.
var (
	lampPalette = []string{lampRed, lampRed, lampOrange, lampYellow}
	facePalette = []string{lampRed, lampRed, lampRed, lampOrange, lampYellow}
)

// ledColor is one LED's color at a moment. Each LED gets its own rhythm from
// the launch's random seed: a few hold steady, a few flicker fast, and most
// change slowly, every two to nine seconds, each with its own phase, so the
// machine looks alive and different every launch.
func (m *InteractiveMode) ledColor(id uint64, now time.Time, palette []string) string {
	h := mixBits(m.homeSeed, id)
	periods := []int64{0, 0, 0, 1, 3, 13, 21, 21, 34, 34, 34, 55, 55, 55, 89, 89}
	period := periods[h%uint64(len(periods))]
	step := uint64(0)
	if period > 0 {
		step = uint64((now.UnixMilli()/100 + int64(h>>8%97)) / period)
	}
	state := mixBits(h, step)
	if state%100 >= 55 {
		return machineLampOff
	}
	return palette[(state>>8)%uint64(len(palette))]
}

// mixBits spreads its inputs into a well-mixed value.
func mixBits(a, b uint64) uint64 {
	h := (a*0x9e3779b97f4a7c15 ^ b) * 0xbf58476d1ce4e5b9
	return h ^ h>>31
}

// woprFont is the wordmark's 6×6 pixel font; "#" is a lit pixel.
var woprFont = map[rune][6]string{
	'W': {"#....#", "#....#", "#.##.#", "#.##.#", "#.##.#", "######"},
	'O': {"######", "#....#", "#....#", "#....#", "#....#", "######"},
	'P': {"######", "#....#", "#....#", "######", "#.....", "#....."},
	'R': {"######", "#....#", "#....#", "######", "#..#..", "#...##"},
}

// woprWordmark renders "WOPR" in half-block pixels, three rows tall.
func woprWordmark() []string {
	rows := make([]string, 3)
	for row := range rows {
		var b strings.Builder
		for i, letter := range "WOPR" {
			if i > 0 {
				b.WriteString("  ")
			}
			glyph := woprFont[letter]
			for col := range len(glyph[0]) {
				top, bottom := glyph[2*row][col] == '#', glyph[2*row+1][col] == '#'
				switch {
				case top && bottom:
					b.WriteString("█")
				case top:
					b.WriteString("▀")
				case bottom:
					b.WriteString("▄")
				default:
					b.WriteString(" ")
				}
			}
		}
		rows[row] = b.String()
	}
	return rows
}

// homeBanner is the wordmark, "War Operation Plan Response" with its initials
// bright, and the greeting line typing itself out in phosphor blue.
type homeBanner struct {
	tui.BaseComponent
	m *InteractiveMode
}

func (b *homeBanner) Render(width int) []string {
	th := tui.ActiveTheme()
	if b.m.homeEpoch.IsZero() {
		b.m.homeEpoch = time.Now()
	}
	var lines []string
	for _, row := range woprWordmark() {
		lines = append(lines, bold(th.FgText("text", row)))
	}
	var subtitle []string
	for word := range strings.FieldsSeq("WAR OPERATION PLAN RESPONSE") {
		subtitle = append(subtitle, th.FgText("text", word[:1])+th.FgText("textMuted", word[1:]))
	}
	lines = append(lines, "", strings.Join(subtitle, " "), "")
	greeting := []string{"GREETINGS " + userDisplayName() + ".", Tagline}
	lines = append(lines, "\x1b[1m"+tui.ThemeHexFg(phosphorHex())+typedGreeting(greeting[0], time.Since(b.m.homeEpoch))+tui.SGRFgReset+tui.SGRBoldDimReset)
	blockWidth := 0
	for _, line := range append(woprWordmark(), "WAR OPERATION PLAN RESPONSE", greeting[0], greeting[1]) {
		blockWidth = max(blockWidth, widthx.VisibleWidth(line))
	}
	tall := b.m.tuiInst.Height() >= machineMinHeight
	center := func(line string, lineWidth int) string {
		return strings.Repeat(" ", max(0, (width-lineWidth)/2)) + line
	}
	if !tall || width < machineWidth+2 {
		for i, line := range lines {
			if line != "" {
				lines[i] = center(line, blockWidth)
			}
		}
		return lines
	}
	// The machine, full width, with the greeting typing out beneath it.
	var out []string
	for _, row := range b.m.woprMachine() {
		out = append(out, center(row, machineWidth))
	}
	greetingWidth := max(len(greeting[0]), len(greeting[1]))
	return append(out, "", center(lines[len(lines)-1], greetingWidth))
}

// typedGreeting is the greeting line at a moment: after a short pause it
// types the greeting, holds it for a second, clears, and types the tagline,
// which stays.
func typedGreeting(greeting string, elapsed time.Duration) string {
	typed := func(text string, since time.Duration) string {
		runes := []rune(text)
		return string(runes[:min(len(runes), max(0, int(since/greetingStep)))])
	}
	elapsed -= greetingDelay
	if elapsed < 0 {
		return ""
	}
	shown := time.Duration(len([]rune(greeting))) * greetingStep
	if elapsed < shown+greetingHold {
		return typed(greeting, elapsed)
	}
	return typed(Tagline, elapsed-shown-greetingHold)
}

// userDisplayName is the logged-in user's full name, else their login, in
// capitals.
func userDisplayName() string {
	if u, err := user.Current(); err == nil {
		if u.Name != "" {
			return strings.ToUpper(u.Name)
		}
		return strings.ToUpper(u.Username)
	}
	return "PROFESSOR FALKEN"
}

// homeTips are the tips shown under the home prompt. {text} is highlighted,
// {key:action} is replaced with the action's key, {war:text} is drawn in the
// war red, and {model} names the strongest signed-in model.
var homeTips = []string{
	"{ctrl+x g} starts {war:GLOBAL THERMONUCLEAR WAR}: a new session on {model} at max thinking",
	"Press {key:app.commandPalette} to see all available actions and commands",
	"The leader key is {ctrl+x}; combine it with other keys for quick actions",
	"Press {tab} on an empty prompt to cycle your recent models",
	"Press {key:app.thinking.cycle} to raise or lower the thinking level",
	"Start a message with {!} to run a shell command",
	"Type {@} to attach a file to your prompt",
	"Press {ctrl+x m} to switch models and {ctrl+x t} to switch themes",
	"Press {ctrl+x l} to switch sessions and {ctrl+x n} to start a new one",
	"Press {esc} twice to stop the model mid-response",
	"Run {/setup} to connect models, or to turn on model routing",
	"Run {/review} to check your changes for bugs and code you can cut",
	"Drop an opencode theme into {~/.config/opencode/themes} and wopr loads it",
}

var tipMarkup = regexp.MustCompile(`\{([^}]+)\}`)

// homeTip renders one tip: "● Tip " in the warning color, then the tip with
// highlighted spans.
type homeTip struct {
	tui.BaseComponent
	m     *InteractiveMode
	index int
}

func (t *homeTip) Render(width int) []string {
	th := tui.ActiveTheme()
	tip := homeTips[t.index%len(homeTips)]
	if strings.Contains(tip, "{model}") {
		if _, name := t.m.strongestModel(); name != "" {
			tip = strings.ReplaceAll(tip, "{model}", name)
		} else {
			tip = homeTips[1]
		}
	}
	var b strings.Builder
	last := 0
	for _, loc := range tipMarkup.FindAllStringSubmatchIndex(tip, -1) {
		b.WriteString(th.FgText("textMuted", tip[last:loc[0]]))
		span := tip[loc[2]:loc[3]]
		switch {
		case strings.HasPrefix(span, "key:"):
			b.WriteString(th.FgText("text", t.m.keyHint(strings.TrimPrefix(span, "key:"))))
		case strings.HasPrefix(span, "war:"):
			b.WriteString(hexFg(warRed, strings.TrimPrefix(span, "war:")))
		default:
			b.WriteString(th.FgText("text", span))
		}
		last = loc[1]
	}
	b.WriteString(th.FgText("textMuted", tip[last:]))
	rows := make([]string, homeTipsPadTop, homeTipsPadTop+2)
	lead := th.FgText("warning", "● Tip ")
	for i, line := range widthx.WrapTextWithAnsi(b.String(), max(1, width-6)) {
		if i == 0 {
			rows = append(rows, lead+line)
		} else {
			rows = append(rows, "      "+line)
		}
	}
	return rows
}

// homeFooter is the bottom row: the working directory and git branch on the
// left, WOPR and its version on the right, padded one row above and below.
type homeFooter struct {
	tui.BaseComponent
	m *InteractiveMode
}

func (f *homeFooter) Render(width int) []string {
	th := tui.ActiveTheme()
	snap := f.m.statusLine.Snapshot()
	dir := snap.cwd
	if home, err := os.UserHomeDir(); err == nil && dir != "" {
		dir = formatCwdForFooter(dir, home)
	}
	if snap.gitBranch != "" {
		dir += ":" + snap.gitBranch
	}
	version := "WOPR " + version.Version
	right := th.FgText("textMuted", version)
	if latest := f.m.availableUpdate; latest != "" {
		// A newer release stays named here after its toast is gone.
		note := latest + " available: /upgrade"
		version += " · " + note
		right += th.FgText("textMuted", " · ") + th.FgText("accent", note)
	}
	inner := max(1, width-4)
	dir = truncateLeft(dir, max(1, inner-len(version)-2))
	return []string{"", "  " + spread(th.FgText("textMuted", dir), right, inner), ""}
}

// thinkingMeterBars is the filled bars per thinking level; off and unknown
// levels have none.
var thinkingMeterBars = map[string]int{"minimal": 1, "low": 1, "medium": 3, "high": 4, "xhigh": 5, "max": 5}

// thinkingMeter renders a thinking level as "thinking ▮▮▮▯▯": one bar for
// minimal or low, up to five for the deepest level, all hollow when off.
func thinkingMeter(level string) string {
	filled := thinkingMeterBars[level]
	meter := strings.Repeat("▮", filled) + strings.Repeat("▯", 5-filled)
	return bold(tui.ActiveTheme().FgText("warning", "thinking "+meter))
}

// sessionRouter is the session's model router, or nil.
func (m *InteractiveMode) sessionRouter() *router.Router {
	if routed, ok := m.opts.SessionHandle.(interface{ Router() *router.Router }); ok {
		return routed.Router()
	}
	return nil
}

// strongestModel is the most capable subscription model with working
// credentials: the model a Global Thermonuclear War session runs on.
func (m *InteractiveMode) strongestModel() (spec, name string) {
	r := m.sessionRouter()
	if r == nil {
		return "", ""
	}
	if m.authedProviders == nil {
		m.authedProviders = AuthenticatedProviders(m.opts.AgentDir)
	}
	var best router.ModelRef
	for _, tier := range r.Config().Tiers {
		if tier.Cost != router.CostSubscription {
			continue
		}
		for _, ref := range tier.Models {
			if m.authedProviders[ref.Provider] && ref.Capability > best.Capability {
				best = ref
			}
		}
	}
	if best.Model == "" {
		return "", ""
	}
	return best.Provider + "/" + best.Model, m.modelName(best.Provider, best.Model)
}

// globalThermonuclearWar starts a new session on the strongest model at its
// deepest thinking, with routing off.
func (m *InteractiveMode) globalThermonuclearWar(ctx context.Context) {
	spec, name := m.strongestModel()
	if spec == "" {
		m.showToast("warning", "", "No subscription model is signed in.")
		return
	}
	if !m.homeVisible() {
		m.dispatchSlash("/new")
	}
	if r := m.sessionRouter(); r != nil {
		r.SetMode(router.ModeOff)
	}
	if m.switchModel(spec) != nil {
		m.showToast("error", "", "Could not switch to "+name+".")
		return
	}
	if top := maxThinkingIndex(m.opts.Model); top > 0 {
		_ = m.applyThinkingLevel(levelsForModel(m.opts.Model)[top], false)
	}
	m.showToast("error", "GLOBAL THERMONUCLEAR WAR", name+" · max thinking · routing off")
}

// centered places component in the middle of the row, at most maxWidth wide.
func centered(component tui.Component, maxWidth int) tui.Component {
	grow := func(c tui.Component) tui.StackChild {
		return tui.StackChild{Component: c, Basis: new(0), Grow: new(1), Shrink: new(1)}
	}
	middle := tui.StackChild{Component: component, Basis: new(maxWidth), Grow: new(0), Shrink: new(1), MinSize: new(1)}
	return tui.NewHStack([]tui.StackChild{grow(tui.NewSpacer(0)), middle, grow(tui.NewSpacer(0))}, tui.StackOptions{})
}

// buildHomeRoot lays out the home screen between elastic spacers, over the
// footer.
func (m *InteractiveMode) buildHomeRoot() tui.Component {
	fixed := func(c tui.Component, size int) tui.StackChild {
		return tui.StackChild{Component: c, Basis: new(size), Grow: new(0), Shrink: new(1)}
	}
	elastic := func() tui.StackChild {
		return tui.StackChild{Component: tui.NewSpacer(0), Basis: new(0), Grow: new(1), Shrink: new(1)}
	}
	natural := func(c tui.Component) tui.StackChild {
		return tui.StackChild{Component: c, Grow: new(0), Shrink: new(0)}
	}
	tip := &homeTip{m: m, index: rand.IntN(len(homeTips))}
	return tui.NewVStack([]tui.StackChild{
		elastic(),
		natural(centered(&homeBanner{m: m}, homePromptMaxWidth)),
		fixed(tui.NewSpacer(0), 1),
		natural(centered(m.editorContainer, homePromptMaxWidth)),
		{Component: centered(tip, homePromptMaxWidth), Grow: new(0), Shrink: new(1)},
		elastic(),
		natural(&homeFooter{m: m}),
	}, tui.StackOptions{})
}
