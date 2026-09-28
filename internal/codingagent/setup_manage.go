package codingagent

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/router"
	"github.com/alexrudloff/wopr/tui"
)

// Setup's main screen is organized the way people think about their
// models: connections (an endpoint, an API key, a signed-in subscription),
// each holding models. A connection has its own settings (name, address,
// key, sign-in); a model has its name and context window, and routing is
// one section of it: whether routing uses it, what it costs (fixed by how
// it is connected, except for an endpoint), its strength, and whether it
// is abliterated. Every edit is written when saved.

// setupConnection is one way models are reached.
type setupConnection struct {
	ID   string
	Kind setupKind
	Name string
	// BaseURL is an endpoint's address.
	BaseURL string
	// HasKey reports a stored key (auth.json); KeyEnv names the environment
	// variable a key comes from instead.
	HasKey bool
	KeyEnv string
	Models []*setupModel
}

// kindLabel names a connection kind for people.
func kindLabel(k setupKind) string {
	switch k {
	case setupViaSubscription:
		return "subscription"
	case setupViaAPIKey:
		return "API key"
	}
	return "endpoint"
}

// Cost labels: what each cost class means to the person paying.
var costLabels = map[string]string{
	router.CostFreeLocal:    "free, local",
	router.CostFreeRemote:   "free, network",
	router.CostSubscription: "subscription",
	router.CostPaid:         "per token",
}

// costChoices are the cost classes a model can have given how it is
// connected: a subscription is a subscription and an API key is paid per
// token; only an endpoint's cost depends on where it runs.
func costChoices(s *setupModel) []string {
	switch {
	case s.Endpoint != nil:
		return []string{router.CostFreeLocal, router.CostFreeRemote, router.CostPaid}
	case s.Kind == setupViaSubscription || s.Provider == "kimi-coding":
		return []string{router.CostSubscription}
	}
	return []string{router.CostPaid}
}

// ─── Connections ─────────────────────────────────────────────────────────

// loadConnections reads what is configured now: endpoints from models.json,
// signed-in subscriptions, and API keys, each with its models (an
// endpoint's definitions, and the models routing uses from the others).
func (w *setupWizard) loadConnections() []*setupConnection {
	var defined modelsConfig
	if src, err := readConfigFile(filepath.Join(w.m.opts.AgentDir, "models.json")); err == nil && strings.TrimSpace(src) != "" {
		_ = json.Unmarshal([]byte(stripJSONComments(src)), &defined)
	}
	refs := map[string][]router.ModelRef{}
	tierOf := map[string]router.TierConfig{}
	if r := w.m.sessionRouter(); r != nil {
		for _, tier := range r.Config().Tiers {
			for _, ref := range tier.Models {
				if _, seen := tierOf[ref.Spec()]; seen {
					continue
				}
				ref.Capability = cmp.Or(ref.Capability, tier.Capability)
				refs[ref.Provider] = append(refs[ref.Provider], ref)
				tierOf[ref.Spec()] = tier
			}
		}
	}
	route := func(s *setupModel) {
		if tier, ok := tierOf[s.spec()]; ok {
			for _, ref := range refs[s.Provider] {
				if ref.Model == s.Model {
					entry := ref
					s.Ref, s.FromTier, s.Tier = &entry, tier.Name, tier.Cost
					s.Capability, s.Uncensored = ref.Capability, ref.Uncensored
					s.Strengths = nil
					for _, d := range router.Domains {
						if ref.Affinity[d] > 0 {
							s.Strengths = append(s.Strengths, d)
						}
					}
				}
			}
		} else {
			s.NoRouting = true
		}
		s.origName, s.origContext = s.Name, s.Context
	}
	var out []*setupConnection
	for _, id := range slices.Sorted(maps.Keys(defined.Providers)) {
		prov := defined.Providers[id]
		if prov.BaseURL == "" {
			continue // overrides of a built-in provider, not a connection
		}
		c := &setupConnection{ID: id, Kind: setupViaEndpoint, Name: cmp.Or(prov.Name, id), BaseURL: strings.TrimRight(prov.BaseURL, "/")}
		c.HasKey = w.storedCredential(id)
		for _, md := range prov.Models {
			s := &setupModel{Existing: true, Defined: true, Kind: setupViaEndpoint, Provider: id, Model: md.ID, Name: cmp.Or(md.Name, md.ID),
				Endpoint: &setupEndpoint{ID: id, Name: c.Name, BaseURL: c.BaseURL, Existing: true}}
			if md.ContextWindow != nil {
				s.Context = *md.ContextWindow
			}
			s.Tier = inferTier(setupViaEndpoint, c.BaseURL, id)
			route(s)
			c.Models = append(c.Models, s)
		}
		out = append(out, c)
	}
	// A catalog model is configured when routing uses it or the ranking
	// lists it (added, then taken out of routing).
	ranked := map[string][]string{}
	if r := w.m.sessionRouter(); r != nil {
		for _, spec := range r.Config().Ranking {
			provider, model := splitSpec(spec)
			ranked[provider] = append(ranked[provider], model)
		}
	}
	catalog := func(id, name string, kind setupKind) *setupConnection {
		c := &setupConnection{ID: id, Kind: kind, Name: name}
		models := make([]string, 0, len(refs[id]))
		for _, ref := range refs[id] {
			models = append(models, ref.Model)
		}
		for _, model := range ranked[id] {
			if !slices.Contains(models, model) {
				models = append(models, model)
			}
		}
		for _, model := range models {
			s := &setupModel{Existing: true, Kind: kind, Provider: id, Model: model, Name: w.m.modelName(id, model), Tier: inferTier(kind, "", id)}
			if override, ok := defined.Providers[id].ModelOverrides[model]; ok && override.ContextWindow != nil {
				s.Context = *override.ContextWindow
			} else if cat, ok := ai.LookupModelExact(s.spec()); ok {
				s.Context = cat.ContextWindow
			}
			route(s)
			c.Models = append(c.Models, s)
		}
		return c
	}
	seen := map[string]bool{}
	for _, c := range out {
		seen[c.ID] = true
	}
	for _, p := range w.m.oauthProviderList("login-oauth") {
		// A stored API key for a provider that also offers a login is an
		// API-key connection, not a sign-in.
		if p.Stored && p.StoredType == string(ai.CredentialOAuth) && !seen[p.ID] {
			seen[p.ID] = true
			out = append(out, catalog(p.ID, p.Name, setupViaSubscription))
		}
	}
	for _, p := range w.m.oauthProviderList("login-api-key") {
		if p.AuthType != "api_key" || seen[p.ID] || !p.Stored && p.AuthStatusSource == "" {
			continue
		}
		seen[p.ID] = true
		c := catalog(p.ID, p.Name, setupViaAPIKey)
		c.HasKey = p.Stored
		if !p.Stored && p.AuthStatusSource == string(ai.AuthSourceEnvironment) {
			c.KeyEnv = p.AuthStatusLabel
		}
		out = append(out, c)
	}
	return out
}

// storedCredential reports whether auth.json holds a credential for id.
func (w *setupWizard) storedCredential(id string) bool {
	auth, err := ai.NewAuthStorage(filepath.Join(w.m.opts.AgentDir, "auth.json"))
	if err != nil {
		return false
	}
	_, ok, _ := auth.GetRaw(id)
	return ok
}

// connectionDetail is the one-line description of a connection.
func connectionDetail(c *setupConnection) string {
	switch c.Kind {
	case setupViaEndpoint:
		detail := "endpoint · " + strings.TrimPrefix(strings.TrimPrefix(c.BaseURL, "http://"), "https://")
		if c.HasKey {
			detail += " · key"
		}
		return detail
	case setupViaSubscription:
		return "subscription · signed in"
	}
	if c.KeyEnv != "" {
		return "API key · from $" + strings.TrimPrefix(c.KeyEnv, "$")
	}
	return "API key · stored"
}

// modelFooter summarizes a model in a list: measuring, or its speed.
func (w *setupWizard) modelFooter(s *setupModel) string {
	if st, ok := w.m.measurer().get(s.spec()); ok && st.running {
		return "measuring…"
	} else if ok && st.failed {
		return "couldn't measure"
	}
	if speed := w.speedShort(s); speed != "" {
		return speed
	}
	return costLabels[s.Tier]
}

// speedShort is a model's speed in a few characters, e.g. "0.3s · 59 tok/s".
func (w *setupWizard) speedShort(s *setupModel) string {
	stat, ok := w.speedStat(s.spec())
	if !ok {
		return ""
	}
	out := formatSeconds(stat.BaseSeconds)
	if stat.OutputTokensPerSecond > 0 {
		out += fmt.Sprintf(" · %.0f tok/s", stat.OutputTokensPerSecond)
	}
	return out
}

// speedStat is what router-speed.json holds for spec.
func (w *setupWizard) speedStat(spec string) (router.SpeedStat, bool) {
	src, err := readConfigFile(filepath.Join(w.m.opts.AgentDir, router.StatsFileName))
	if err != nil || src == "" {
		return router.SpeedStat{}, false
	}
	var stats map[string]router.SpeedStat
	if json.Unmarshal([]byte(src), &stats) != nil {
		return router.SpeedStat{}, false
	}
	stat, ok := stats[spec]
	return stat, ok && stat.Samples > 0
}

// ─── The main screen ─────────────────────────────────────────────────────

// manage is the main screen: connections, ways to connect another,
// routing, and done.
func (w *setupWizard) manage() {
	highlight := ""
	for {
		conns := w.loadConnections()
		w.updateNote(conns)
		var options []tui.DialogOption
		for _, c := range conns {
			options = append(options, tui.DialogOption{Title: c.Name, Description: connectionDetail(c), Category: "Connections",
				Footer: count(len(c.Models), "model"), Value: "conn:" + c.ID})
		}
		for _, o := range connectOptions("Connect") {
			o.Value = "add:" + o.Value
			options = append(options, o)
		}
		options = append(options,
			tui.DialogOption{Title: "Model routing", Footer: w.routingSummary(), Category: "Routing", Value: "routing"},
			tui.DialogOption{Title: "Done", Description: "back to the home screen", Value: "done", Pinned: true})
		d := tui.NewDialogSelect("Models", options, "")
		d.Select(cmp.Or(highlight, options[0].Value))
		// The status line follows measuring, and clears when it ends.
		seen := w.m.measurer().changes()
		w.screen.refresh = func() {
			if now := w.m.measurer().changes(); now != seen {
				seen = now
				w.updateNote(w.loadConnections())
			}
		}
		w.screen.leaving = true
		chosen, ok := w.sel(d, "")
		w.screen.leaving, w.screen.refresh = false, nil
		if !ok || chosen.Value == "done" {
			return
		}
		highlight = chosen.Value
		w.screen.err = nil
		kind, id, _ := strings.Cut(chosen.Value, ":")
		switch kind {
		case "conn":
			for _, c := range conns {
				if c.ID == id {
					w.openConnection(c.ID)
				}
			}
		case "add":
			// A new connection goes straight to its models; then back here,
			// on it.
			if provider := w.connectKind(id); provider != "" {
				highlight = "conn:" + provider
				if c := w.connectionByID(provider); c != nil {
					w.addFromConnection(c)
				}
			}
		case "routing":
			w.routingScreen()
		}
	}
}

func (w *setupWizard) updateNote(conns []*setupConnection) {
	models := 0
	for _, c := range conns {
		models += len(c.Models)
	}
	note := count(len(conns), "connection") + " · " + count(models, "model")
	if active := w.m.measurer().active(); active != "" {
		note += " · measuring " + active + "…"
	}
	w.screen.note = note
}

// routingSummary is model routing's state on the main screen.
func (w *setupWizard) routingSummary() string {
	if r := w.m.sessionRouter(); r != nil && r.Available() {
		if r.Engine() == router.EngineJev {
			return "on · Jev"
		}
		return "on · Basic"
	}
	return "off"
}

// ─── A connection ────────────────────────────────────────────────────────

// connectionByID loads one connection.
func (w *setupWizard) connectionByID(id string) *setupConnection {
	for _, c := range w.loadConnections() {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// openConnection shows an existing connection's models and its settings;
// model rows fill in as measuring finishes.
func (w *setupWizard) openConnection(id string) {
	highlight := ""
	for {
		c := w.connectionByID(id)
		if c == nil {
			return
		}
		build := func() []tui.DialogOption {
			var options []tui.DialogOption
			for i, s := range c.Models {
				options = append(options, tui.DialogOption{Title: cmp.Or(s.Name, s.Model), Description: descIfDifferent(s.Model, s.Name), Category: "Models",
					Footer: w.modelFooter(s), Value: "model:" + strconv.Itoa(i)})
			}
			options = append(options, tui.DialogOption{Title: "Add models from " + c.Name, Category: "Models", Value: "add"})
			switch c.Kind {
			case setupViaEndpoint:
				options = append(options,
					tui.DialogOption{Title: "Edit name, address, and key", Description: c.BaseURL, Category: "Connection", Value: "edit"},
					tui.DialogOption{Title: "Remove this endpoint", Description: "and its models", Category: "Connection", Value: "remove"})
			case setupViaAPIKey:
				if c.KeyEnv != "" {
					options = append(options, tui.DialogOption{Title: "The key comes from $" + c.KeyEnv, Description: "set in your shell", Category: "Connection", Value: "env"})
				} else {
					options = append(options,
						tui.DialogOption{Title: "Replace the API key", Category: "Connection", Value: "rekey"},
						tui.DialogOption{Title: "Remove the API key", Category: "Connection", Value: "signout"})
				}
			case setupViaSubscription:
				options = append(options,
					tui.DialogOption{Title: "Sign in again", Category: "Connection", Value: "signin"},
					tui.DialogOption{Title: "Sign out", Category: "Connection", Value: "signout"})
			}
			return options
		}
		d := tui.NewDialogSelect(c.Name+" · "+kindLabel(c.Kind), build(), "")
		d.Intro = []string{measureWhy}
		if c.Kind == setupViaAPIKey && len(c.Models) > 0 {
			if cost := requestCost(c.Models[0].spec()); cost > 0 {
				d.Intro = []string{measureWhy + fmt.Sprintf(" For a model paid per token that costs about $%.4f.", cost)}
			}
		}
		d.Select(cmp.Or(highlight, "model:0", "add"))
		// Rows update as measuring finishes.
		seen := w.m.measurer().changes()
		w.screen.refresh = func() {
			if now := w.m.measurer().changes(); now != seen {
				seen = now
				d.SetOptions(build())
				w.updateNote(w.loadConnections())
			}
		}
		chosen, ok := w.sel(d, "Back")
		w.screen.refresh = nil
		if !ok {
			return
		}
		highlight = chosen.Value
		w.screen.err = nil
		switch kind, index, _ := strings.Cut(chosen.Value, ":"); kind {
		case "model":
			n, _ := strconv.Atoi(index)
			w.modelScreen(c.Models[n])
		case "add":
			w.addFromConnection(c)
		case "edit":
			w.editEndpoint(c)
		case "remove":
			if w.removeEndpoint(c) {
				return
			}
		case "rekey":
			w.replaceKey(c)
		case "signin":
			w.login(c.ID, c.Name)
		case "signout":
			if w.signOut(c) {
				return
			}
		case "env":
			w.m.showToast("info", "", "Unset $"+c.KeyEnv+" in your shell to disconnect "+c.Name+", or set another key there.")
		}
	}
}

// addFromConnection runs a connection's model checklist and adds what was
// checked.
func (w *setupWizard) addFromConnection(c *setupConnection) {
	var picked []*setupModel
	if c.Kind == setupViaEndpoint {
		picked = w.addFromEndpoint(c)
	} else {
		picked = w.pickCatalogModels(c.ID, c.Name, c.Kind)
	}
	w.addModels(picked)
}

// ─── A model ─────────────────────────────────────────────────────────────

// modelScreen shows a model's settings: its name and context window, its
// measured speed (with Measure again), what it costs, and Remove.
func (w *setupWizard) modelScreen(s *setupModel) {
	th := tui.ActiveTheme()
	name := tui.NewTextField("Name", cmp.Or(s.Name, s.Model), s.Model, false)
	name.Hint = "Shown in wopr; the id stays " + s.Model + "."
	contextField := tui.NewTextField("Context window", strconv.Itoa(cmp.Or(s.Context, defaultContextWindow)), "32768", false)
	contextField.Hint = "Tokens the model takes at once. For a server, the size it was started with."
	choices := costChoices(s)
	var costOptions []string
	for _, c := range choices {
		costOptions = append(costOptions, costLabels[c])
	}
	cost := tui.NewChoiceField("Cost", costOptions, max(0, slices.Index(choices, s.Tier)))
	if s.Endpoint != nil {
		cost.Hint = "Local: runs on this machine. Network: your own hardware elsewhere. Per token: a service that bills per use."
	} else {
		cost.Hint = "Set by how it is connected (" + kindLabel(s.Kind) + ")."
	}
	f := tui.NewForm(cmp.Or(s.Name, s.Model), name, contextField, cost)
	f.Extra = []string{"Measure again", "Remove this model"}
	about := s.spec() + " · " + kindLabel(s.Kind)
	if rank := w.rankWords(s); rank != "" {
		about += " · " + rank
	}
	// The speed line follows measuring while the screen is open.
	f.IntroFunc = func(int) []string {
		return []string{th.FgText("textMuted", about), th.FgText("text", "Speed: "+w.speedWords(s))}
	}
	for {
		if !w.form(f) {
			return
		}
		switch f.Pressed() {
		case "Measure again":
			// The connection's row shows it measuring.
			w.m.measureModel(s)
			return
		case "Remove this model":
			if w.removeModel(s) {
				return
			}
			continue
		}
		n, err := strconv.Atoi(strings.ReplaceAll(contextField.Value(), ",", ""))
		if err != nil || n < 1024 {
			f.Error = "Context window: enter a number of tokens, for example 32768."
			f.Focus(1)
			continue
		}
		s.Name, s.Context, s.Edited = name.Value(), n, true
		tierChanged := choices[cost.Choice] != s.Tier
		s.Tier = choices[cost.Choice]
		err = w.writeConfig("models.json", func(src string) (string, error) {
			if strings.TrimSpace(src) == "" {
				src = "{}\n"
			}
			return applyModelsJSONChanges(src, []*setupModel{s})
		})
		if err == nil && tierChanged && !s.NoRouting {
			err = w.writeConfig(router.ConfigFileName, func(src string) (string, error) {
				return mergeRouterJSON(src, routerPlan{Existing: []*setupModel{s}})
			})
		}
		s.Edited = false
		if err != nil {
			f.Error = "Could not save: " + err.Error()
			continue
		}
		s.origName, s.origContext = s.Name, s.Context
		w.reload()
		w.m.showFlash("Saved " + cmp.Or(s.Name, s.spec()))
		return
	}
}

// speedWords is a model's measured speed in plain words.
func (w *setupWizard) speedWords(s *setupModel) string {
	if st, ok := w.m.measurer().get(s.spec()); ok {
		if st.running {
			return "measuring (" + st.step + ")…"
		}
		if st.summary != "" {
			return st.summary + "."
		}
	}
	stat, ok := w.speedStat(s.spec())
	if !ok {
		return "not measured yet."
	}
	out := "starts answering in " + formatSeconds(stat.BaseSeconds)
	if stat.OutputTokensPerSecond > 0 {
		out += fmt.Sprintf(", writes %.0f tokens a second", stat.OutputTokensPerSecond)
	}
	if stat.PerKSeconds > 0 {
		out += ", reads a long prompt at " + formatSeconds(stat.PerKSeconds) + " per 1K tokens"
	}
	return out + "."
}

// rankWords says where a model stands in the ranking while routing is on.
func (w *setupWizard) rankWords(s *setupModel) string {
	if r := w.m.sessionRouter(); r == nil || !r.Available() {
		return ""
	}
	if i := slices.Index(w.currentRanking(), s.spec()); i >= 0 {
		words := fmt.Sprintf("ranked %d", i+1)
		if s.NoRouting {
			words += ", not used by routing"
		}
		return words + " in Model routing"
	}
	return "not ranked"
}

// removeModel removes a model after asking: from routing and the ranking,
// and an endpoint's definition from models.json.
func (w *setupWizard) removeModel(s *setupModel) bool {
	name := cmp.Or(s.Name, s.Model)
	details := []string{"Takes it off your models."}
	if s.Defined {
		details = []string{"Deletes it from models.json and your models.", "The file is backed up first."}
	}
	if !w.confirm("Remove "+name+"?", "Remove "+name, details...) {
		return false
	}
	ranking := slices.DeleteFunc(w.currentRanking(), func(spec string) bool { return spec == s.spec() })
	s.Removed = true
	err := w.writeConfig("models.json", func(src string) (string, error) {
		if !s.Defined || strings.TrimSpace(src) == "" {
			return src, nil
		}
		return applyModelsJSONChanges(src, []*setupModel{s})
	})
	if err == nil {
		err = w.saveRanking(ranking, nil, []*setupModel{s})
	}
	if err != nil {
		w.showError("Could not remove "+name, err)
		return false
	}
	w.reload()
	w.m.showFlash("Removed " + name)
	return true
}

// ─── Connection settings ─────────────────────────────────────────────────

// editEndpoint edits an endpoint's display name, address, and key; true
// when saved.
func (w *setupWizard) editEndpoint(c *setupConnection) bool {
	name := tui.NewTextField("Name", c.Name, c.ID, false)
	name.Hint = "How the endpoint is shown. Model specs keep the id " + c.ID + "."
	address := tui.NewTextField("Address", c.BaseURL, "e.g. http://localhost:8000/v1", false)
	address.Hint = "The API root, usually ending in /v1."
	keyOptions := []string{"none", "add a key"}
	if c.HasKey {
		keyOptions = []string{"keep", "replace", "remove"}
	}
	key := tui.NewChoiceField("API key", keyOptions, 0)
	key.Hint = "Sent as a bearer token; stored in auth.json."
	f := tui.NewForm("Endpoint · "+c.ID, name, address, key)
	f.Submit = "Save"
	for {
		if !w.form(f) {
			return false
		}
		newURL := normalizeBaseURL(address.Value())
		apiKey, keyChange := "", key.Value()
		if keyChange == "replace" || keyChange == "add a key" {
			entered, ok := w.askKey(c.Name)
			if !ok {
				continue
			}
			apiKey = entered
		} else if keyChange == "keep" {
			apiKey = w.providerKey(c.ID)
		}
		// Check the address (and key) before saving.
		var listErr error
		progress := &setupProgress{title: "Contacting " + newURL, rows: []progressRow{{label: "GET " + newURL + "/models", state: rowRunning}}, autoClose: true}
		if !w.m.runProgress(progress, func(ctx context.Context, update func(func())) {
			_, root, err := listEndpointModels(ctx, setupEndpoint{BaseURL: newURL, APIKey: apiKey})
			update(func() { newURL, listErr = root, err })
		}) || progress.stopped {
			continue
		}
		if listErr != nil {
			f.Error = "The endpoint didn't answer: " + listErr.Error()
			continue
		}
		oldURL := c.BaseURL
		hasKey := apiKey != "" && keyChange != "remove" && keyChange != "none"
		err := w.writeConfig("models.json", func(src string) (string, error) {
			var err error
			if src, err = jsoncSet(src, strconv.Quote(name.Value()), "providers", c.ID, "name"); err != nil {
				return "", err
			}
			if src, err = jsoncSet(src, strconv.Quote(newURL), "providers", c.ID, "baseUrl"); err != nil {
				return "", err
			}
			return jsoncSet(src, strconv.FormatBool(hasKey), "providers", c.ID, "authHeader")
		})
		if err == nil && newURL != oldURL {
			err = w.writeConfig(router.ConfigFileName, func(src string) (string, error) {
				if strings.TrimSpace(src) == "" {
					return src, nil
				}
				return updateProbeURL(src, oldURL+"/models", newURL+"/models")
			})
		}
		if err == nil {
			err = w.setStoredKey(c.ID, apiKey, keyChange)
		}
		if err != nil {
			f.Error = "Could not save: " + err.Error()
			continue
		}
		w.reload()
		w.m.showFlash("Saved " + name.Value())
		return true
	}
}

// setStoredKey applies an endpoint key choice to auth.json.
func (w *setupWizard) setStoredKey(id, key, change string) error {
	auth, err := ai.NewAuthStorage(filepath.Join(w.m.opts.AgentDir, "auth.json"))
	if err != nil {
		return err
	}
	switch change {
	case "replace", "add a key":
		return auth.Set(id, ai.Credential{Type: ai.CredentialAPIKey, Key: key})
	case "remove":
		if _, ok, _ := auth.Get(id); ok {
			return auth.Delete(id)
		}
	}
	return nil
}

// askKey asks for an API key.
func (w *setupWizard) askKey(name string) (string, bool) {
	key := tui.NewTextField("API key", "", "paste the key", true)
	f := tui.NewForm(name+" API key", key)
	f.Submit = "Use this key"
	for {
		if !w.form(f) {
			return "", false
		}
		if key.Value() == "" {
			f.Error = "Paste a key, or press esc to go back."
			continue
		}
		return key.Value(), true
	}
}

// replaceKey checks and stores a new key for an API-key connection.
func (w *setupWizard) replaceKey(c *setupConnection) {
	for {
		key, ok := w.askKey(c.Name)
		if !ok {
			return
		}
		var checkErr error
		progress := &setupProgress{title: "Checking the " + c.Name + " key", rows: []progressRow{{label: "asking " + c.Name + " who the key belongs to", state: rowRunning}}, autoClose: true}
		if !w.m.runProgress(progress, func(ctx context.Context, update func(func())) {
			_, err := verifyAPIKey(ctx, c.ID, key)
			update(func() { checkErr = err })
		}) || progress.stopped {
			continue
		}
		if checkErr != nil {
			w.showError("The key did not work", checkErr)
			continue
		}
		if err := w.m.setAPIKey(c.ID, key); err != nil {
			w.showError("Could not save the key", err)
			return
		}
		w.screen.err = nil
		w.m.showFlash("Replaced the " + c.Name + " key")
		return
	}
}

// confirm asks before something that can't be undone from setup.
func (w *setupWizard) confirm(title, action string, details ...string) bool {
	d := tui.NewDialogSelect(title, []tui.DialogOption{
		{Title: action, Value: "yes", Details: details},
		{Title: "Cancel", Value: "no"},
	}, "")
	d.Select("no")
	chosen, ok := w.sel(d, "")
	return ok && chosen.Value == "yes"
}

// removeEndpoint deletes an endpoint, its models, and its key; true when
// removed.
func (w *setupWizard) removeEndpoint(c *setupConnection) bool {
	if !w.confirm("Remove "+c.Name+"?", "Remove "+c.Name,
		"Deletes it and its "+count(len(c.Models), "model")+" from models.json and your models,",
		"and deletes its stored key. The files are backed up first.") {
		return false
	}
	err := w.writeConfig("models.json", func(src string) (string, error) { return removeModelsJSONProvider(src, c.ID) })
	if err == nil {
		gone := map[string]bool{}
		for _, s := range c.Models {
			s.Removed = true
			gone[s.spec()] = true
		}
		ranking := slices.DeleteFunc(w.currentRanking(), func(spec string) bool { return gone[spec] })
		err = w.saveRanking(ranking, nil, c.Models)
	}
	if err == nil {
		err = w.setStoredKey(c.ID, "", "remove")
	}
	if err != nil {
		w.showError("Could not remove "+c.Name, err)
		return false
	}
	w.reload()
	w.m.showFlash("Removed " + c.Name)
	return true
}

// signOut removes a subscription login or a stored API key; true when done.
func (w *setupWizard) signOut(c *setupConnection) bool {
	what := "Sign out of " + c.Name
	if c.Kind == setupViaAPIKey {
		what = "Remove the " + c.Name + " key"
	}
	if !w.confirm(what+"?", what, "Its models stay on your list and are skipped until you connect it again.") {
		return false
	}
	if err := w.m.runOAuthLogout(c.ID); err != nil {
		w.showError(what+" failed", err)
		return false
	}
	w.reload()
	return true
}

// addFromEndpoint lists an endpoint's models and returns the ones picked.
func (w *setupWizard) addFromEndpoint(c *setupConnection) []*setupModel {
	ep := &setupEndpoint{ID: c.ID, Name: c.Name, BaseURL: c.BaseURL, Existing: true}
	if c.HasKey {
		ep.APIKey = w.providerKey(c.ID)
	}
	var models []remoteModel
	var listErr error
	progress := &setupProgress{title: "Contacting " + c.BaseURL, rows: []progressRow{{label: "GET " + c.BaseURL + "/models", state: rowRunning}}, autoClose: true}
	if !w.m.runProgress(progress, func(ctx context.Context, update func(func())) {
		list, _, err := listEndpointModels(ctx, *ep)
		update(func() { models, listErr = list, err })
	}) || progress.stopped {
		return nil
	}
	if listErr != nil {
		w.showError("Could not list "+c.Name+"'s models", listErr)
		return nil
	}
	return w.pickEndpointModels(ep, models)
}

// ─── Writing ─────────────────────────────────────────────────────────────

// writeConfig merges into one config file, backing it up the first time
// this setup run changes it.
func (w *setupWizard) writeConfig(name string, merge func(string) (string, error)) error {
	setupFilesMu.Lock()
	defer setupFilesMu.Unlock()
	path := filepath.Join(w.m.opts.AgentDir, name)
	src, err := readConfigFile(path)
	if err != nil {
		return err
	}
	out, err := merge(src)
	if err != nil {
		return err
	}
	if w.backedUp == nil {
		w.backedUp = map[string]bool{}
	}
	backup, err := writeConfigFile(path, out, time.Now(), !w.backedUp[name])
	if backup != "" {
		w.backedUp[name] = true
		w.backups = append(w.backups, filepath.Base(backup))
	}
	return err
}

// reload re-reads the registry and the router after a write.
func (w *setupWizard) reload() {
	if w.m.opts.ModelRegistry != nil {
		w.m.opts.ModelRegistry.Refresh()
	}
	if reloader, ok := w.m.opts.SessionHandle.(interface{ ReloadRouter() error }); ok {
		if err := reloader.ReloadRouter(); err != nil {
			w.showError("router.json no longer loads", err)
		}
	}
	w.m.authedProviders = nil
	w.m.updateProviderInfo()
}
