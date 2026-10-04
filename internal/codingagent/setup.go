package codingagent

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/router"
	"github.com/alexrudloff/wopr/tui"
)

// Setup connects models and keeps their settings. Its main screen lists
// connections (endpoints, API keys, signed-in subscriptions) with their
// models; adding a model is picking it and placing it in the ranking, and
// measuring its speed runs by itself. Routing has its own screen. A first
// run (no usable model) is a short path: connect, pick, and start. It opens
// on its own at the first launch with no usable model, and from /setup or
// `wopr setup` any time. Config files are edited in place and backed up.

// setupWizard is one run of setup.
type setupWizard struct {
	m      *InteractiveMode
	screen *setupScreen
	// backedUp names the config files backed up this run; backups lists
	// the backup files.
	backedUp map[string]bool
	backups  []string
}

// needsFirstRunSetup reports a first run: nothing has been set up yet (no
// router.json, and no model ever picked). An API key in the environment
// doesn't count: it makes a provider's models reachable, but nobody chose
// among them.
func (m *InteractiveMode) needsFirstRunSetup() bool {
	if m.opts.ModelRegistry == nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(m.opts.AgentDir, router.ConfigFileName)); err == nil {
		return false
	}
	return m.settings().DefaultModel == ""
}

// noUsableModel reports whether nothing could answer a prompt.
func (m *InteractiveMode) noUsableModel() bool {
	return m.opts.Model == nil && m.opts.ModelRegistry != nil && len(m.availableModelItems()) == 0
}

// setupCommand runs /setup.
func (m *InteractiveMode) setupCommand(string) error {
	if !m.homeVisible() {
		m.showToast("warning", "", "Setup runs from the home screen: start a new session with /new, then run /setup.")
		return nil
	}
	m.runSetup()
	return nil
}

// runSetupModal feeds md on the setup screen until it is done, or in a
// dialog when the setup screen isn't open (/upgrade's progress).
func (m *InteractiveMode) runSetupModal(md modal) bool {
	if m.setup == nil {
		return m.runDialog(md, dialogMedium)
	}
	previous := m.setup.body
	m.setup.body = md.component
	defer func() { m.setup.body = previous }()
	// Toasts close on a timer that posts to the main loop, which waits
	// while setup runs; expire them from here instead.
	stop := make(chan struct{})
	defer close(stop)
	tasks := make(chan func())
	forward := func(from <-chan func()) {
		for {
			select {
			case fn := <-from:
				select {
				case tasks <- fn:
				case <-stop:
					return
				}
			case <-stop:
				return
			}
		}
	}
	if md.tasks != nil {
		go forward(md.tasks)
	}
	go func() {
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				select {
				case tasks <- func() {
					m.expireToast()
					if m.setup != nil && m.setup.refresh != nil {
						m.setup.refresh()
					}
				}:
				case <-stop:
					return
				}
			case <-stop:
				return
			}
		}
	}()
	md.tasks = tasks
	return m.feedModal(md, false)
}

// runSetup runs setup and returns to the home screen.
func (m *InteractiveMode) runSetup() {
	if m.setup != nil {
		return
	}
	w := &setupWizard{m: m, screen: &setupScreen{m: m}}
	m.setup = w.screen
	m.tuiInst.ForceFullRender()
	defer func() {
		m.setup = nil
		if reloader, ok := m.opts.SessionHandle.(interface{ ReloadRouter() error }); ok {
			_ = reloader.ReloadRouter()
		}
		if m.noUsableModel() {
			m.showToast("warning", "", "No model is set up yet. Run /setup when you're ready.")
		}
		m.tuiInst.ForceFullRender()
		m.tuiInst.RequestRender()
	}()
	// A first run opens with a welcome; then setup is its menus, the same
	// as any other time.
	first := m.needsFirstRunSetup()
	if first && !w.welcome() {
		return
	}
	w.manage()
	w.chooseStartingModel(first)
}

// chooseStartingModel gives a session that has no model one: the first of
// the user's models (the strongest, when routing ranks them), with routing
// picking from there in auto mode while it is on.
func (w *setupWizard) chooseStartingModel(first bool) {
	// A first run replaces a model wopr picked by itself (say, from an API
	// key in the environment) with the user's strongest.
	if w.m.opts.Model != nil && !first {
		return
	}
	models := map[string]*setupModel{}
	for _, c := range w.loadConnections() {
		for _, s := range c.Models {
			models[s.spec()] = s
		}
	}
	w.reload()
	for _, spec := range w.currentRanking() {
		if s := models[spec]; s != nil && !s.NoRouting && w.m.switchModel(spec) == nil {
			if w.m.opts.SettingsManager != nil {
				_ = w.m.opts.SettingsManager.SetDefaultModelAndProvider(s.Provider, s.Model)
			}
			if r := w.m.sessionRouter(); r != nil && r.Available() {
				w.m.selectRoutingMode(router.ObjectiveAuto, false)
			}
			return
		}
	}
}

// showError puts a failure above the panel.
func (w *setupWizard) showError(title string, err error) {
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	w.screen.err = tui.NewErrorPanel(title, detail)
}

// setupBackValue is the value of the Back or Cancel row sel adds.
const setupBackValue = "\x00back"

// sel runs a DialogSelect on the setup screen with a last row that leaves
// it, labeled back ("Back" for a screen, "Cancel" for a choice); "" adds
// none (the list has its own way out). Choosing that row, or esc, reports
// false.
func (w *setupWizard) sel(d *tui.DialogSelect, back ...string) (tui.DialogOption, bool) {
	label := "Cancel"
	if len(back) > 0 {
		label = back[0]
	}
	if label != "" {
		d.AppendOption(tui.DialogOption{Title: label, Value: setupBackValue, Spaced: true, Pinned: true})
	}
	d.MaxHeight = w.screen.panelRoom()
	if !w.m.runSetupModal(modalOf(d)) || d.Cancelled() || d.Chosen().Value == setupBackValue {
		return tui.DialogOption{}, false
	}
	return d.Chosen(), true
}

// form runs a Form on the setup screen; false means cancelled.
func (w *setupWizard) form(f *tui.Form) bool {
	f.Reopen()
	f.MaxHeight = w.screen.room() - 1
	return w.m.runSetupModal(modalOf(f)) && !f.Cancelled()
}

// ─── First run ───────────────────────────────────────────────────────────

// welcome is the first page: GREETINGS, and Connect or Not now.
func (w *setupWizard) welcome() bool {
	page := &setupWelcome{start: time.Now()}
	w.screen.plain = true
	defer func() { w.screen.plain = false }()
	// Repaint while the greeting types itself out.
	wake := make(chan struct{}, 1)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		tick := time.NewTicker(greetingStep)
		defer tick.Stop()
		deadline := time.After(3 * time.Second)
		for {
			select {
			case <-tick.C:
				select {
				case wake <- struct{}{}:
				default:
				}
			case <-deadline:
				return
			case <-stop:
				return
			}
		}
	}()
	md := modalOf(page)
	md.wake = wake
	return w.m.runSetupModal(md) && !page.quit
}

// connectOptions are the ways to connect a model, in category.
func connectOptions(category string) []tui.DialogOption {
	return []tui.DialogOption{
		{Title: "Sign in with a subscription", Description: "ChatGPT, Claude, Copilot", Category: category, Value: "subscription"},
		{Title: "Add an API key", Description: "OpenRouter, Anthropic, OpenAI", Category: category, Value: "apikey"},
		{Title: "Add an endpoint", Description: "llama.cpp, vLLM, LiteLLM, Ollama", Category: category, Value: "endpoint"},
	}
}

// connectKind runs one way of connecting and returns the provider
// connected, or "".
func (w *setupWizard) connectKind(kind string) string {
	switch kind {
	case "subscription":
		return w.addSubscription()
	case "apikey":
		return w.addAPIKey()
	case "endpoint":
		return w.addEndpoint()
	}
	return ""
}

// ─── Connecting ──────────────────────────────────────────────────────────

// addSubscription signs in with one of the OAuth logins and returns the
// provider, or "" when nothing was connected.
func (w *setupWizard) addSubscription() string {
	var options []tui.DialogOption
	for _, p := range w.m.oauthProviderList("login-oauth") {
		footer := ""
		if p.Stored && p.StoredType == string(ai.CredentialOAuth) {
			footer = "signed in"
		}
		options = append(options, tui.DialogOption{Title: p.Name, Footer: footer, Value: p.ID})
	}
	d := tui.NewDialogSelect("Sign in with a subscription", options, "")
	chosen, ok := w.sel(d)
	if !ok {
		return ""
	}
	if chosen.Footer == "" && !w.login(chosen.Value, chosen.Title) {
		return ""
	}
	return chosen.Value
}

// login runs a provider's existing OAuth flow in the setup panel.
func (w *setupWizard) login(provider, name string) bool {
	var login func(d *loginDialog) (ai.Credential, error)
	switch provider {
	case "anthropic":
		login = anthropicLogin
	case "github-copilot":
		login = copilotLogin
	case "openai-codex":
		method := tui.NewDialogSelect("OpenAI Codex login method", []tui.DialogOption{
			{Title: "Browser login", Description: "opens chatgpt.com", Value: ai.OpenAICodexBrowserLoginMethod},
			{Title: "Device code login", Description: "for a machine without a browser", Value: ai.OpenAICodexDeviceCodeLoginMethod},
		}, "")
		chosen, ok := w.sel(method)
		if !ok {
			return false
		}
		login = codexLogin(chosen.Value)
	default:
		registered, ok := ai.GetOAuthProvider(provider)
		if !ok {
			w.showError("Unknown provider "+provider, nil)
			return false
		}
		login = registeredOAuthLogin(registered)
	}
	_, ok, err := w.m.runDialogLogin(w.m.runCtxOrBackground(), name, provider, login)
	if err != nil {
		w.showError("Sign-in to "+name+" failed", err)
		return false
	}
	if ok {
		w.m.updateProviderInfo()
	}
	return ok
}

// addAPIKey asks for a provider's key, checks it with one request that
// costs nothing, and stores it the way /login does; it returns the
// provider, or "" when nothing was connected.
func (w *setupWizard) addAPIKey() string {
	var options []tui.DialogOption
	for _, p := range w.m.oauthProviderList("login-api-key") {
		if p.AuthType != "api_key" {
			continue
		}
		footer := ""
		if p.Stored || p.AuthStatusSource != "" {
			footer = cmp.Or(p.AuthStatusLabel, "configured")
		}
		description := ""
		if inferTier(setupViaAPIKey, "", p.ID) == router.CostSubscription {
			// A monthly plan whose key comes from the provider's console.
			description = "subscription plan"
		}
		options = append(options, tui.DialogOption{Title: p.Name, Description: description, Footer: footer, Value: p.ID})
	}
	d := tui.NewDialogSelect("Add an API key", options, "")
	chosen, ok := w.sel(d)
	if !ok {
		return ""
	}
	provider, name, configured := chosen.Value, chosen.Title, chosen.Footer
	if configured != "" {
		// A key is already configured: use it, or replace it.
		use := tui.NewDialogSelect(name+" already has a key", []tui.DialogOption{
			{Title: "Use the configured key", Description: configured, Value: "use"},
			{Title: "Enter a new key", Value: "new"},
		}, "")
		choice, ok := w.sel(use)
		if !ok {
			return ""
		}
		if choice.Value == "use" {
			return provider
		}
	}
	key := tui.NewTextField("API key", "", "paste the key", true)
	key.Hint = "Stored in auth.json, as /login stores it. Checked with one request that costs nothing."
	f := tui.NewForm(name+" API key", key)
	f.Submit = "Check and save"
	f.Cancel = "Back"
	for {
		if !w.form(f) {
			return ""
		}
		if key.Value() == "" {
			f.Error = "Paste a key, or press esc to go back."
			continue
		}
		var checkErr error
		checked := true
		progress := &setupProgress{title: "Checking the " + name + " key", rows: []progressRow{{label: "asking " + name + " who the key belongs to", state: rowRunning}}, autoClose: true}
		if !w.m.runProgress(progress, func(ctx context.Context, update func(func())) {
			ok, err := verifyAPIKey(ctx, provider, key.Value())
			update(func() { checkErr, checked = err, ok })
		}) || progress.stopped {
			continue
		}
		if checkErr != nil {
			f.Error = "The key did not work: " + checkErr.Error()
			continue
		}
		if err := w.m.setAPIKey(provider, key.Value()); err != nil {
			f.Error = "Could not save the key: " + err.Error()
			continue
		}
		if !checked {
			w.m.showToast("warning", "", name+" keys can't be checked without a request that costs money; saved unchecked.")
		}
		return provider
	}
}

// verifyAPIKey checks a key with a request that costs nothing: the
// provider's model list (or OpenRouter's key endpoint, since its model list
// is public). ok is false when the provider can't be checked that way.
func verifyAPIKey(ctx context.Context, provider, key string) (bool, error) {
	api, base := ai.ProviderEndpoint(provider)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var req *http.Request
	var err error
	switch {
	case base == "":
		return false, nil
	case provider == "opencode" || provider == "opencode-go":
		// The model list is public, so ask for a completion with no
		// messages: a bad key is refused before the empty request is, and
		// an empty request runs nothing.
		models, _, listErr := listEndpointModels(ctx, setupEndpoint{BaseURL: base})
		if listErr != nil || len(models) == 0 {
			return false, nil
		}
		body := strings.NewReader(`{"model":` + strconv.Quote(models[0].ID) + `,"messages":[],"max_tokens":1}`)
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", body)
		if err != nil {
			return false, err
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := setupHTTP.Do(req)
		if err != nil {
			return true, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return true, readError(resp)
		}
		return true, nil
	case provider == "openrouter":
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, "https://openrouter.ai/api/v1/key", nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	case api == ai.APIAnthropicMessages:
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(base, "/v1")+"/v1/models", nil)
		if err == nil {
			req.Header.Set("x-api-key", key)
			req.Header.Set("anthropic-version", "2023-06-01")
		}
	case api == ai.APIGoogleGenerativeAI:
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, base+"/models?key="+key, nil)
	case api == ai.APIOpenAICompletions || api == ai.APIOpenAIResponses || api == ai.APIMistralConversations:
		_, _, listErr := listEndpointModels(ctx, setupEndpoint{BaseURL: base, APIKey: key})
		return true, listErr
	default:
		return false, nil
	}
	if err != nil {
		return false, err
	}
	resp, err := setupHTTP.Do(req)
	if err != nil {
		return true, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return true, readError(resp)
	}
	return true, nil
}

var providerIDClean = regexp.MustCompile(`[^a-z0-9]+`)

// addEndpoint asks for an OpenAI-compatible server, checks that it lists
// models, and saves it; it returns the provider, or "" when nothing was
// connected. Its models are added from its menu.
func (w *setupWizard) addEndpoint() string {
	existing := w.existingProviders()
	base := tui.NewTextField("Base URL", "", "e.g. http://localhost:8000/v1", false)
	base.Hint = "llama.cpp, vLLM, LiteLLM, Ollama, LM Studio: any server with /v1/models."
	name := tui.NewTextField("Name", "", "e.g. local", false)
	name.Hint = "The provider in model specs (name/model)."
	key := tui.NewTextField("API key (optional)", "", "none", true)
	key.Hint = "Sent as a bearer token; stored in auth.json."
	f := tui.NewForm("Add an OpenAI-compatible endpoint", base, name, key)
	f.Submit = "Check and save"
	f.Cancel = "Back"
	for {
		if !w.form(f) {
			return ""
		}
		f.Error = ""
		ep := setupEndpoint{BaseURL: normalizeBaseURL(base.Value()), APIKey: key.Value()}
		if ep.BaseURL == "" {
			f.Error = "Enter the server's base URL."
			f.Focus(0)
			continue
		}
		ep.Name = cmp.Or(name.Value(), hostOf(ep.BaseURL))
		ep.ID = strings.Trim(providerIDClean.ReplaceAllString(strings.ToLower(ep.Name), "-"), "-")
		var listErr error
		progress := &setupProgress{title: "Contacting " + ep.BaseURL, rows: []progressRow{{label: "GET " + ep.BaseURL + "/models", state: rowRunning}}, autoClose: true}
		if !w.m.runProgress(progress, func(ctx context.Context, update func(func())) {
			_, root, err := listEndpointModels(ctx, ep)
			update(func() { ep.BaseURL, listErr = root, err })
		}) || progress.stopped {
			continue
		}
		if listErr != nil {
			f.Error = "Could not list models: " + listErr.Error()
			continue
		}
		// The same server already in models.json keeps its provider id and
		// name.
		if id, ok := existing[ep.BaseURL]; ok {
			ep.ID, ep.Existing = id, true
			if c := w.connectionByID(id); c != nil {
				ep.Name = c.Name
			}
		} else if taken := slices.Contains(existingIDs(existing), ep.ID) || isBuiltInProvider(ep.ID); taken || ep.ID == "" {
			f.Error = fmt.Sprintf("The name %q is taken by another provider; choose another.", cmp.Or(ep.ID, ep.Name))
			f.Focus(1)
			continue
		}
		if ep.Existing {
			return ep.ID
		}
		if ep.APIKey != "" {
			if err := w.setStoredKey(ep.ID, ep.APIKey, "add a key"); err != nil {
				f.Error = "Could not save the key: " + err.Error()
				continue
			}
		}
		if err := w.writeConfig("models.json", func(src string) (string, error) { return addEndpointProvider(src, &ep) }); err != nil {
			f.Error = "Could not save: " + err.Error()
			continue
		}
		// An endpoint on the user's machine or network starts private; the
		// connection screen changes it.
		if privacySafeAddress(ep.BaseURL) {
			if err := w.writeConfig(router.ConfigFileName, func(src string) (string, error) { return setPrivateProvider(src, ep.ID, true) }); err != nil {
				f.Error = "Could not save: " + err.Error()
				continue
			}
		}
		w.reload()
		return ep.ID
	}
}

func hostOf(baseURL string) string {
	host := strings.TrimPrefix(strings.TrimPrefix(baseURL, "http://"), "https://")
	host, _, _ = strings.Cut(host, "/")
	host, _, _ = strings.Cut(host, ":")
	if host == "localhost" || host == "127.0.0.1" {
		return "local"
	}
	return host
}

// existingProviders maps models.json base URLs to provider ids.
func (w *setupWizard) existingProviders() map[string]string {
	out := map[string]string{}
	src, err := readConfigFile(filepath.Join(w.m.opts.AgentDir, "models.json"))
	if err != nil || strings.TrimSpace(src) == "" {
		return out
	}
	var cfg modelsConfig
	if json.Unmarshal([]byte(stripJSONComments(src)), &cfg) != nil {
		return out
	}
	for id, p := range cfg.Providers {
		if p.BaseURL != "" {
			out[strings.TrimRight(p.BaseURL, "/")] = id
		} else {
			out["provider:"+id] = id
		}
	}
	return out
}

func existingIDs(providers map[string]string) []string {
	var ids []string
	for _, id := range providers {
		ids = append(ids, id)
	}
	return ids
}

// count is "1 model", "3 models".
func count(n int, noun string) string {
	return strconv.Itoa(n) + " " + plural(n, noun, noun+"s")
}

func formatSeconds(s float64) string {
	if s < 10 {
		return strconv.FormatFloat(s, 'f', 1, 64) + "s"
	}
	return strconv.FormatFloat(s, 'f', 0, 64) + "s"
}

// providerKey is a provider's API key: the stored one, else the
// environment's.
func (w *setupWizard) providerKey(provider string) string {
	if auth, err := ai.NewAuthStorage(filepath.Join(w.m.opts.AgentDir, "auth.json")); err == nil {
		if key, ok, err := ai.ResolveStoredAPIKeyFromStorage(w.m.runCtxOrBackground(), auth, provider); err == nil && ok {
			return key
		}
	}
	return ai.GetEnvAPIKey(provider, nil)
}
