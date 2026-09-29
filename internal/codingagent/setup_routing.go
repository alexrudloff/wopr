package codingagent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/internal/codingagent/router"
	"github.com/alexrudloff/wopr/tui"
	"github.com/alexrudloff/wopr/tui/widthx"
)

// The Model routing screen. Routing is Off (wopr runs the model the user
// picks), Basic (fixed rules on each model's rank, window, speed, and cost
// class), or Jev (Jev classifies each prompt; its endpoint is tested before
// it saves, and the Basic rules route whenever it can't answer). While
// routing is on, the screen also holds the models in order, strongest first
// (space picks one up and the arrows carry it), each model's settings
// (enter: used by routing, abliterated, strengths), and suggestions from
// how models have been doing. Nothing is written until Save.

// routingRow is one ranked model.
type routingRow struct {
	spec, name, cost              string
	use, abliterated, privacySafe bool
	strengths                     []string
}

// routingSuggestion is a suggestion on screen.
type routingSuggestion struct {
	spec, text string
}

// routingView is the Model routing screen's component.
type routingView struct {
	tui.BaseComponent
	// engine is the choice on screen: engineOff, router.EngineBasic, or
	// router.EngineJev; the ranking shows unless it is off.
	engine      string
	rows        []*routingRow
	suggestions []routingSuggestion
	kept        []string
	endpoint    *tui.TextInput
	jevKey      *tui.TextInput
	jevModel    *tui.TextInput
	// jevPrivate is Jev's Privacy Safe checkbox.
	jevPrivate bool
	// keys says which Jev keys exist, for the key field's placeholder.
	keys      jevKeys
	testNote  string
	err       string
	focus     int
	maxHeight int
	done      bool
	action    string
	// moving is the model picked up to move, and moveFrom where it was.
	moving   string
	moveFrom int
	// settingsOf is the model whose settings the user asked to edit.
	settingsOf string
}

// engineOff is the Off choice; engineChoices are the choices in order.
const engineOff = "off"

var engineChoices = []string{engineOff, router.EngineBasic, router.EngineJev}

var engineLabels = map[string]string{engineOff: "Off", router.EngineBasic: "Basic", router.EngineJev: "Jev"}

// Focusable items, in order.
const (
	itemEngine = iota
	itemEndpoint
	itemJevKey
	itemJevModel
	itemJevPrivate
	itemSuggestMove
	itemSuggestKeep
	itemRow
	itemButton
)

// Button actions.
const (
	actionSave   = "Save"
	actionCancel = "Cancel"
)

type routingItem struct {
	kind   int
	index  int // suggestion index
	spec   string
	button string
}

func (v *routingView) buttons() []string { return []string{actionSave, actionCancel} }

// routes reports whether the choice on screen routes.
func (v *routingView) routes() bool { return v.engine != engineOff }

// choose moves the engine choice delta places, without wrapping.
func (v *routingView) choose(delta int) {
	i := slices.Index(engineChoices, v.engine) + delta
	v.engine = engineChoices[max(0, min(i, len(engineChoices)-1))]
}

func (v *routingView) items() []routingItem {
	out := []routingItem{{kind: itemEngine}}
	if v.engine == router.EngineJev {
		out = append(out, routingItem{kind: itemEndpoint}, routingItem{kind: itemJevKey}, routingItem{kind: itemJevModel}, routingItem{kind: itemJevPrivate})
	}
	if v.routes() {
		for i := range v.suggestions {
			out = append(out, routingItem{kind: itemSuggestMove, index: i}, routingItem{kind: itemSuggestKeep, index: i})
		}
		for _, row := range v.rows {
			out = append(out, routingItem{kind: itemRow, spec: row.spec})
		}
	}
	for _, b := range v.buttons() {
		out = append(out, routingItem{kind: itemButton, button: b})
	}
	return out
}

func (v *routingView) current() routingItem {
	items := v.items()
	v.focus = max(0, min(v.focus, len(items)-1))
	return items[v.focus]
}

func (v *routingView) Done() bool { return v.done }

func (v *routingView) finish(action string) { v.done, v.action = true, action }

// focusOn moves the focus to the first item matching.
func (v *routingView) focusOn(match func(routingItem) bool) {
	for k, item := range v.items() {
		if match(item) {
			v.focus = k
			return
		}
	}
}

// index is a model's position in the order, or -1.
func (v *routingView) index(spec string) int {
	return slices.IndexFunc(v.rows, func(row *routingRow) bool { return row.spec == spec })
}

// move moves a model delta places in the order, keeping the focus on it.
func (v *routingView) move(spec string, delta int) {
	i := v.index(spec)
	j := i + delta
	if i < 0 || j < 0 || j >= len(v.rows) {
		return
	}
	row := v.rows[i]
	v.rows = slices.Insert(slices.Delete(v.rows, i, i+1), j, row)
	v.focusOn(func(item routingItem) bool { return item.kind == itemRow && item.spec == spec })
}

func (v *routingView) row(spec string) *routingRow {
	if i := v.index(spec); i >= 0 {
		return v.rows[i]
	}
	return nil
}

func (v *routingView) HandleInput(data string) {
	defer v.Invalidate()
	item := v.current()
	if v.moving != "" {
		// A picked-up model: up and down carry it, space or enter drops
		// it, esc puts it back.
		switch {
		case tui.MatchesKeyID(data, "up"):
			v.move(v.moving, -1)
		case tui.MatchesKeyID(data, "down"):
			v.move(v.moving, 1)
		case tui.MatchesKeyID(data, "space"), tui.MatchesKeyID(data, "enter"):
			v.moving = ""
		case tui.MatchesKeyID(data, "escape"), tui.MatchesKeyID(data, "ctrl+c"):
			v.move(v.moving, v.moveFrom-v.index(v.moving))
			v.moving = ""
		}
		return
	}
	switch {
	case tui.MatchesKeyID(data, "escape"), tui.MatchesKeyID(data, "ctrl+c"):
		v.finish(actionCancel)
		return
	case item.kind == itemRow && tui.MatchesKeyID(data, "alt+up"):
		v.move(item.spec, -1)
		return
	case item.kind == itemRow && tui.MatchesKeyID(data, "alt+down"):
		v.move(item.spec, 1)
		return
	case tui.MatchesKeyID(data, "home"):
		v.focus = 0
		return
	case tui.MatchesKeyID(data, "end"):
		v.focus = len(v.items()) - 1
		return
	case tui.MatchesKeyID(data, "up"), tui.MatchesKeyID(data, "shift+tab"):
		v.focus = max(0, v.focus-1)
		return
	case tui.MatchesKeyID(data, "down"), tui.MatchesKeyID(data, "tab"):
		v.focus = min(len(v.items())-1, v.focus+1)
		return
	}
	switch item.kind {
	case itemEngine:
		switch {
		case tui.MatchesKeyID(data, "left"):
			v.choose(-1)
		case tui.MatchesKeyID(data, "right"):
			v.choose(1)
		case tui.MatchesKeyID(data, "space"):
			v.engine = engineChoices[(slices.Index(engineChoices, v.engine)+1)%len(engineChoices)]
		case tui.MatchesKeyID(data, "enter"):
			v.focus++
		}
	case itemJevPrivate:
		if tui.MatchesKeyID(data, "space") || tui.MatchesKeyID(data, "enter") {
			v.jevPrivate = !v.jevPrivate
		}
	case itemEndpoint, itemJevKey, itemJevModel:
		if tui.MatchesKeyID(data, "enter") {
			v.focus++
			return
		}
		switch item.kind {
		case itemJevModel:
			v.jevModel.HandleInput(data)
		case itemJevKey:
			v.jevKey.HandleInput(data)
		default:
			v.endpoint.HandleInput(data)
		}
	case itemRow:
		switch {
		case tui.MatchesKeyID(data, "space"):
			v.moving, v.moveFrom = item.spec, v.index(item.spec)
		case tui.MatchesKeyID(data, "enter"):
			v.settingsOf = item.spec
			v.finish("settings")
		}
	default:
		if !tui.MatchesKeyID(data, "enter") && !tui.MatchesKeyID(data, "space") {
			if tui.MatchesKeyID(data, "left") {
				v.focus = max(0, v.focus-1)
			} else if tui.MatchesKeyID(data, "right") {
				v.focus = min(len(v.items())-1, v.focus+1)
			}
			return
		}
		switch item.kind {
		case itemSuggestMove:
			s := v.suggestions[item.index]
			v.suggestions = slices.Delete(v.suggestions, item.index, item.index+1)
			v.move(s.spec, 1)
		case itemSuggestKeep:
			v.kept = append(v.kept, v.suggestions[item.index].spec)
			v.suggestions = slices.Delete(v.suggestions, item.index, item.index+1)
		case itemButton:
			v.finish(item.button)
		}
	}
}

// strengthLabels are the strength tags' names on screen, by Jev domain.
var strengthLabels = map[string]string{
	"frontend_ui":        "frontend",
	"backend_api":        "backend",
	"data_sql_pipelines": "data/SQL",
	"infra_devops_build": "infra/build",
	"tests":              "tests",
	"docs_prose":         "docs",
	"systems_perf":       "systems/perf",
	"general":            "general",
}

func strengthWords(tags []string) string {
	var words []string
	for _, t := range tags {
		words = append(words, cmp.Or(strengthLabels[t], t))
	}
	return strings.Join(words, ", ")
}

func (v *routingView) Render(width int) []string {
	th := tui.ActiveTheme()
	pad := "    "
	inner := max(1, width-8)
	focus := v.current()
	button := func(label string, focused bool) string {
		if focused {
			return tui.FillBackground(" "+bold(th.FgText("selectedListItemText", label))+" ", len([]rune(label))+2, th.Bg("primary"))
		}
		return tui.FillBackground(" "+th.FgText("text", label)+" ", len([]rune(label))+2, th.Bg("backgroundElement"))
	}
	field := func(label string, focused bool, value string) string {
		l := th.FgText("textMuted", fmt.Sprintf("%-14s", label))
		if focused {
			l = bold(th.FgText("primary", fmt.Sprintf("%-14s", label)))
		}
		return pad + l + value
	}
	input := func(in *tui.TextInput, focused bool, width int) string {
		in.Focused = focused
		text := in.Render(width)[0]
		if !focused {
			text = strings.ReplaceAll(widthx.StripAnsi(text), widthx.CursorMarker, "")
			if in.Text() == "" {
				return th.FgText("textMuted", text)
			}
			return th.FgText("text", text)
		}
		return text
	}
	var top, list, bottom []string
	top = append(top, pad+spread(bold(th.FgText("text", "Model routing")), th.FgText("textMuted", "esc"), inner), "")
	var choices []string
	for _, c := range engineChoices {
		switch {
		case c == v.engine && focus.kind == itemEngine:
			choices = append(choices, bold(th.FgText("primary", "● "+engineLabels[c])))
		case c == v.engine:
			choices = append(choices, th.FgText("text", "● "+engineLabels[c]))
		default:
			choices = append(choices, th.FgText("textMuted", "○ "+engineLabels[c]))
		}
	}
	top = append(top, field("Routing", focus.kind == itemEngine, strings.Join(choices, "   ")), "")
	intro := map[string]string{
		engineOff:          "wopr runs the model you pick with /model or Tab, subagents included.",
		router.EngineBasic: "Fixed rules pick a model for each prompt and subagent from your order, each model's window, its measured speed, and its cost class. Modes (auto, cost, speed, quality) appear in /model and Tab.",
		router.EngineJev:   "Jev classifies each prompt and subagent brief, and routing picks a model for it. Whenever Jev can't answer, the Basic rules route. Modes (auto, cost, speed, quality) appear in /model and Tab.",
	}[v.engine]
	for _, line := range widthx.WrapTextWithAnsi(intro, inner) {
		top = append(top, pad+th.FgText("textMuted", line))
	}
	if v.engine == router.EngineJev {
		top = append(top, "")
		v.jevKey.SetPlaceholder(v.keys.placeholder(v.endpoint.Text()))
		top = append(top, field("Jev endpoint", focus.kind == itemEndpoint, input(v.endpoint, focus.kind == itemEndpoint, max(10, inner-14))))
		top = append(top, field("API key", focus.kind == itemJevKey, input(v.jevKey, focus.kind == itemJevKey, max(10, inner-14))))
		top = append(top, field("Jev model", focus.kind == itemJevModel, input(v.jevModel, focus.kind == itemJevModel, max(10, inner-14))))
		check := "[ ]"
		if v.jevPrivate {
			check = "[x]"
		}
		checkText := th.FgText("text", check) + th.FgText("textMuted", " Privacy Safe: prompts sent to Jev stay with you. Private mode asks Jev only when checked.")
		if focus.kind == itemJevPrivate {
			checkText = bold(th.FgText("primary", check)) + th.FgText("textMuted", " Privacy Safe: prompts sent to Jev stay with you. Private mode asks Jev only when checked.")
		}
		top = append(top, field("", focus.kind == itemJevPrivate, widthx.TruncateToWidth(checkText, max(10, inner-14), "…", false)))
		if v.testNote != "" {
			for _, line := range widthx.WrapTextWithAnsi(v.testNote, max(10, inner-14)) {
				top = append(top, pad+strings.Repeat(" ", 14)+th.FgText("textMuted", line))
			}
		}
	}
	focusRow := -1
	if v.routes() {
		order := "Strongest first. Basic routing takes the highest-ranked model the mode allows; the cost and speed modes go by cost class and measured speed first."
		if v.engine == router.EngineJev {
			order = "Strongest first. Harder work goes to models higher up; routing picks the fastest one that is strong enough."
		}
		top = append(top, "", pad+bold(th.FgText("text", "Put your models in order")))
		for _, line := range widthx.WrapTextWithAnsi(order, inner) {
			top = append(top, pad+th.FgText("textMuted", line))
		}
		top = append(top, "")
		for i, s := range v.suggestions {
			for _, line := range widthx.WrapTextWithAnsi(s.text, inner) {
				top = append(top, pad+th.FgText("warning", line))
			}
			top = append(top, pad+button("Move down", focus.kind == itemSuggestMove && focus.index == i)+"  "+button("Keep", focus.kind == itemSuggestKeep && focus.index == i), "")
		}
		nameWidth := max(12, min(40, inner-30))
		for rank, row := range v.rows {
			use := "routing"
			if !row.use {
				use = "not used"
			}
			var flags []string
			if row.abliterated {
				flags = append(flags, "abliterated")
			}
			if row.privacySafe {
				flags = append(flags, "privacy safe")
			}
			if len(row.strengths) > 0 {
				flags = append(flags, "strong: "+strengthWords(row.strengths))
			}
			moving := row.spec == v.moving
			if moving {
				flags = []string{"moving"}
			}
			name := widthx.TruncateToWidth(row.name, nameWidth, "…", false)
			line := fmt.Sprintf("%2d  ", rank+1) + name + strings.Repeat(" ", max(1, nameWidth-widthx.VisibleWidth(name)+1)) + fmt.Sprintf("%-14s%-9s", row.cost, use) + strings.Join(flags, " · ")
			line = widthx.TruncateToWidth(line, inner+1, "…", false)
			focused := focus.kind == itemRow && focus.spec == row.spec
			switch {
			case moving:
				// Picked up: lifted out in the accent color.
				focusRow = len(list)
				list = append(list, " "+tui.FillBackground(" ▸ "+bold(th.FgText("selectedListItemText", line)), inner+4, th.Bg("accent")))
			case focused:
				focusRow = len(list)
				list = append(list, " "+tui.FillBackground("   "+bold(th.FgText("selectedListItemText", line)), inner+4, th.Bg("primary")))
			case !row.use:
				list = append(list, pad+th.FgText("textMuted", line))
			default:
				list = append(list, pad+th.FgText("text", line))
			}
		}
		if len(v.rows) == 0 {
			list = append(list, pad+th.FgText("textMuted", "No models yet: connect some on the main screen."))
		}
	}
	if v.err != "" {
		bottom = append(bottom, "")
		for _, line := range widthx.WrapTextWithAnsi(v.err, inner) {
			bottom = append(bottom, pad+th.FgText("error", line))
		}
	}
	keys := "tab next · esc cancel"
	switch focus.kind {
	case itemEngine:
		keys = "←→ choose · tab next · esc cancel"
	case itemRow:
		keys = "space pick up · ↑↓ move · space drop · enter settings"
		if v.moving != "" {
			keys = "↑↓ move · space drop · esc cancel"
		}
	case itemEndpoint, itemJevKey, itemJevModel:
		keys = "Save checks Jev answers first"
	}
	var row []string
	for _, b := range v.buttons() {
		row = append(row, button(b, focus.kind == itemButton && focus.button == b))
	}
	bottom = append(bottom, "", pad+strings.Join(row, "  "), pad+th.FgText("textMuted", widthx.TruncateToWidth(keys, inner, "…", false)), "")
	// Show as much of the list as fits, around the focused model.
	room := len(list)
	if v.maxHeight > 0 {
		room = max(3, v.maxHeight-len(top)-len(bottom))
	}
	if len(list) > room {
		room = max(2, room-1) // a line says how many are hidden
		start := 0
		if focusRow >= 0 {
			start = max(0, min(focusRow-room/2, len(list)-room))
		}
		hidden := len(list) - room
		list = append(list[start:start+room:start+room], pad+th.FgText("textMuted", fmt.Sprintf("… %d more (↑↓ to scroll)", hidden)))
	}
	out := make([]string, 0, len(top)+len(list)+len(bottom))
	out = append(out, top...)
	out = append(out, list...)
	return append(out, bottom...)
}

// routingScreen runs the Model routing screen and saves what it says to.
func (w *setupWizard) routingScreen() {
	r := w.m.sessionRouter()
	if r == nil {
		w.showError("Model routing is not available in this session.", nil)
		return
	}
	cfg := r.Config()
	jev := cfg.Jev
	was := engineOff
	if r.Available() {
		was = cfg.EngineName()
	}
	v := &routingView{engine: was}
	models := map[string]*setupModel{}
	for _, c := range w.loadConnections() {
		for _, s := range c.Models {
			models[s.spec()] = s
		}
	}
	names := map[string]int{}
	for _, s := range models {
		names[cmp.Or(s.Name, s.Model)]++
	}
	for _, spec := range w.currentRanking() {
		s := models[spec]
		name := cmp.Or(s.Name, s.Model)
		if names[name] > 1 {
			// Models that share a name are told apart by provider and id.
			name += " (" + spec + ")"
		}
		v.rows = append(v.rows, &routingRow{spec: spec, name: name, cost: costLabels[s.Tier], use: !s.NoRouting, abliterated: s.Uncensored, privacySafe: s.PrivacySafe, strengths: slices.Clone(s.Strengths)})
	}
	if was != engineOff {
		for _, s := range router.Suggestions(w.m.opts.AgentDir) {
			if model, ok := models[s.Spec]; ok && !model.NoRouting {
				v.suggestions = append(v.suggestions, routingSuggestion{spec: s.Spec, text: cmp.Or(model.Name, model.Model) + " " + s.Reason + ". Move it down?"})
			}
		}
	}
	prompt := ""
	v.endpoint = tui.NewInput(tui.InputOptions{Prompt: &prompt, Placeholder: "https://api.typesafe.ai"})
	v.endpoint.SetText(jev.Endpoint)
	v.jevKey = tui.NewInput(tui.InputOptions{Prompt: &prompt})
	v.jevKey.Mask = true
	v.jevModel = tui.NewInput(tui.InputOptions{Prompt: &prompt, Placeholder: "jev-1.13"})
	v.jevModel.SetText(jev.Model)
	v.jevPrivate = jev.PrivacySafe
	v.keys = w.jevKeys(jev)
	switch {
	case len(v.suggestions) > 0:
		v.focusOn(func(item routingItem) bool { return item.kind == itemSuggestMove })
	case was != engineOff && len(v.rows) > 0:
		v.focusOn(func(item routingItem) bool { return item.kind == itemRow })
	}
	for {
		v.done, v.action = false, ""
		v.maxHeight = w.screen.room() - 1
		if !w.m.runSetupModal(modalOf(v)) || v.action == actionCancel {
			return
		}
		v.err = ""
		if v.action == "settings" {
			w.modelRoutingSettings(v.row(v.settingsOf), v.engine)
			continue
		}
		if v.action != actionSave {
			continue
		}
		if v.engine == engineOff {
			if was != engineOff {
				if err := w.writeConfig(router.ConfigFileName, func(src string) (string, error) {
					return mergeRouterJSON(src, routerPlan{Enabled: new(false)})
				}); err != nil {
					v.err = "Could not save: " + err.Error()
					continue
				}
				w.reload()
				w.m.showFlash("Model routing off: wopr runs the model you pick")
			}
			return
		}
		plan := routerPlan{Enabled: new(true), Engine: v.engine}
		if v.engine == router.EngineJev {
			endpoint, model := router.NormalizeJevEndpoint(v.endpoint.Text()), strings.TrimSpace(v.jevModel.Text())
			key := strings.TrimSpace(v.jevKey.Text())
			next := router.JevConfig{Endpoint: endpoint, Model: model, APIKeyProvider: jev.APIKeyProvider, PrivacySafe: v.jevPrivate}
			if key != "" || endpoint != jev.Endpoint {
				// A typed key is saved for Jev; a new endpoint picks its
				// key automatically.
				next.APIKeyProvider = ""
			}
			changed := endpoint != jev.Endpoint || model != jev.Model || next.APIKeyProvider != jev.APIKeyProvider
			if (changed || key != "" || was != router.EngineJev) && !w.testJev(v, next, key) {
				continue
			}
			if key != "" {
				if err := w.setStoredKey(router.JevKeyProvider, key, "replace"); err != nil {
					v.err = "Could not save the key: " + err.Error()
					continue
				}
				v.jevKey.SetText("")
				v.keys.saved = true
			}
			v.endpoint.SetText(endpoint)
			v.keys.endpoint = endpoint
			if next.APIKeyProvider == "" {
				v.keys.none, v.keys.provider = false, ""
			}
			if changed || v.jevPrivate != jev.PrivacySafe {
				plan.Jev = &next
			}
		}
		if err := w.saveRouting(v, models, plan); err != nil {
			v.err = err.Error()
			continue
		}
		if was == engineOff {
			w.m.selectRoutingMode(router.ObjectiveAuto, false)
		}
		w.m.showFlash("Model routing saved: " + engineLabels[v.engine])
		return
	}
}

// testJev asks Jev to classify a sample prompt with cfg and reports how it
// went on the screen; true means it answered.
func (w *setupWizard) testJev(v *routingView, cfg router.JevConfig, key string) bool {
	if cfg.Endpoint == "" || cfg.Model == "" {
		v.err = "Enter the Jev endpoint and model first."
		v.focusOn(func(item routingItem) bool {
			return item.kind == itemEndpoint && cfg.Endpoint == "" || item.kind == itemJevModel && cfg.Endpoint != ""
		})
		return false
	}
	var testErr error
	var took time.Duration
	progress := &setupProgress{title: "Testing Jev", rows: []progressRow{{label: "classifying a sample prompt", state: rowRunning}}, autoClose: true}
	w.m.runProgress(progress, func(ctx context.Context, update func(func())) {
		start := time.Now()
		if key == "" {
			key = router.ResolveJevKey(cfg, w.providerKey)
		}
		_, err := router.TestJev(ctx, cfg, key)
		update(func() { testErr, took = err, time.Since(start) })
	})
	switch {
	case progress.stopped:
		v.testNote = "Test stopped."
		return false
	case testErr != nil:
		v.testNote = ""
		v.err = "Jev didn't answer: " + strings.TrimPrefix(testErr.Error(), "router: ")
		return false
	}
	v.testNote = fmt.Sprintf("Jev answered in %s.", took.Round(time.Millisecond))
	return true
}

// modelRoutingSettings edits how routing treats one model: whether it
// uses it, abliterated, and its strengths (the areas of work, Jev's brief
// domains, where Jev routing prefers it among equally suitable models). The
// Model routing screen's Save writes them.
func (w *setupWizard) modelRoutingSettings(row *routingRow, engine string) {
	if row == nil {
		return
	}
	use := tui.NewCheckField("Use for routing", row.use)
	use.Hint = "Unchecked, routing never picks it; /model still can."
	abliterated := tui.NewCheckField("Abliterated", row.abliterated)
	abliterated.Hint = "No refusal training. The uncensored mode routes only to these."
	private := tui.NewCheckField("Privacy Safe", row.privacySafe)
	private.Hint = "Data stays with you: your machine, your network, or a deployment you trust. Private mode routes only to these."
	fields := []*tui.FormField{use, abliterated, private}
	for i, d := range router.Domains {
		f := tui.NewCheckField(strengthLabels[d], slices.Contains(row.strengths, d))
		if i == 0 {
			f.Section = "Strengths"
		}
		fields = append(fields, f)
	}
	use.Section = "Routing"
	f := tui.NewForm(row.name, fields...)
	f.Intro = []string{"Among models strong enough for a subagent's work, routing prefers one strong in its area. Space checks."}
	if engine != router.EngineJev {
		f.Intro = []string{"Strengths are used by Jev routing only: the Basic rules don't judge what a prompt is about. Space checks."}
	}
	if !w.form(f) {
		return
	}
	row.use, row.abliterated, row.privacySafe, row.strengths = use.Checked(), abliterated.Checked(), private.Checked(), nil
	for i, d := range router.Domains {
		if fields[3+i].Checked() {
			row.strengths = append(row.strengths, d)
		}
	}
}

// saveRouting writes the Model routing screen when routing is on: the
// ranking and the strengths it gives, per model whether routing uses it,
// abliterated, and its strength tags, and plan (routing on, the engine,
// and a new Jev endpoint or model that answered its test).
func (w *setupWizard) saveRouting(v *routingView, models map[string]*setupModel, plan routerPlan) error {
	var ranking router.Ranking
	var changes []*setupModel
	for _, row := range v.rows {
		ranking = append(ranking, row.spec)
		s := models[row.spec]
		if s.NoRouting == row.use || s.Uncensored != row.abliterated || s.PrivacySafe != row.privacySafe || !slices.Equal(s.Strengths, row.strengths) {
			s.NoRouting, s.Uncensored, s.PrivacySafe, s.Strengths = !row.use, row.abliterated, row.privacySafe, row.strengths
			changes = append(changes, s)
		}
	}
	if r := w.m.sessionRouter(); r != nil && r.Objective() == router.ObjectiveUncensored && !slices.ContainsFunc(v.rows, func(row *routingRow) bool { return row.use && row.abliterated }) {
		return errors.New("this session is in the uncensored mode, which needs a model marked abliterated that routing uses")
	}
	if r := w.m.sessionRouter(); r != nil && r.Objective() == router.ObjectivePrivate && !slices.ContainsFunc(v.rows, func(row *routingRow) bool { return row.use && row.privacySafe }) {
		return errors.New("this session is in private mode, which needs a model marked Privacy Safe that routing uses")
	}
	if err := w.saveRanking(ranking, nil, changes); err != nil {
		return err
	}
	if err := w.writeConfig(router.ConfigFileName, func(src string) (string, error) { return mergeRouterJSON(src, plan) }); err != nil {
		return err
	}
	for _, spec := range v.kept {
		router.DismissSuggestion(w.m.opts.AgentDir, spec)
	}
	w.reload()
	return nil
}

// jevKeys records which keys could authenticate Jev, so the key field can
// say which one a blank field uses.
type jevKeys struct {
	endpoint   string // the saved endpoint, which provider and none apply to
	saved      bool   // a key saved for Jev
	provider   string // the provider named by apiKeyProvider, if any
	none       bool   // apiKeyProvider "none": never send a key
	openRouter bool   // an OpenRouter key, stored or in the environment
	env        bool   // TYPESAFE_API_KEY is set
}

func (w *setupWizard) jevKeys(jev router.JevConfig) jevKeys {
	k := jevKeys{
		endpoint:   jev.Endpoint,
		saved:      w.storedCredential(router.JevKeyProvider),
		openRouter: w.providerKey("openrouter") != "",
		env:        strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY")) != "",
	}
	switch jev.APIKeyProvider {
	case "", router.JevKeyProvider:
	case "none":
		k.none = true
	default:
		k.provider = w.m.providerName(jev.APIKeyProvider)
	}
	return k
}

// placeholder describes what a blank key field sends to endpoint.
func (k jevKeys) placeholder(endpoint string) string {
	same := router.NormalizeJevEndpoint(endpoint) == k.endpoint
	switch {
	case same && k.none:
		return "none sent (optional)"
	case same && k.provider != "":
		return "using your " + k.provider + " key"
	case k.saved:
		return "saved (type to replace)"
	case router.IsOpenRouterEndpoint(endpoint) && k.openRouter:
		return "using your OpenRouter key"
	case !router.IsOpenRouterEndpoint(endpoint) && k.env:
		return "using TYPESAFE_API_KEY"
	}
	return "optional: blank sends none"
}
