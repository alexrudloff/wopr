package codingagent

import (
	"cmp"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/tui"
)

// authSelectorProviderNames holds the provider names the auth selector shows
// where they differ from the OAuth flow's own name (for example "Meta", not
// "Meta (Muse subscription)").
var authSelectorProviderNames = map[string]string{
	"meta":         "Meta",
	"openai-codex": "OpenAI Codex",
}

// oauthProviderList returns the auth providers available to /login and /logout.
func (m *InteractiveMode) oauthProviderList(mode string) []tui.OAuthProvider {
	var all []tui.OAuthProvider
	for _, provider := range ai.GetOAuthProviders() {
		name := provider.Name()
		if providerName, ok := authSelectorProviderNames[provider.ID()]; ok {
			name = providerName
		}
		all = append(all, tui.OAuthProvider{ID: provider.ID(), Name: name, AuthType: "oauth"})
	}
	slices.SortFunc(all, func(a, b tui.OAuthProvider) int {
		return strings.Compare(a.Name, b.Name)
	})
	if mode == "login-api-key" {
		// Pull the canonical API-key provider list from ai/ so any drift between
		// the selector and the rest of the codebase is impossible. github-copilot
		// is intentionally excluded: it belongs to the OAuth/subscription list.
		all = nil
		for _, p := range ai.APIKeyProviders() {
			all = append(all, tui.OAuthProvider{ID: p.ID, Name: p.Name, AuthType: "api_key"})
		}
		all = m.withLlamaLoginProvider(all)
	}
	auth, err := ai.NewAuthStorage(filepath.Join(m.opts.AgentDir, "auth.json"))
	if err != nil {
		return all
	}
	creds, err := auth.Load()
	if err != nil {
		return all
	}
	for i := range all {
		if c, ok := creds[all[i].ID]; ok {
			all[i].Stored = true
			all[i].StoredType = string(c.Type)
			all[i].AuthStatusSource = "stored"
		}
		if all[i].AuthType == "api_key" {
			status := auth.GetAuthStatus(all[i].ID)
			if !all[i].Stored {
				all[i].AuthStatusSource = string(status.Source)
				all[i].AuthStatusLabel = status.Label
			}
		}
	}
	m.applyLlamaAuthStatus(all)
	if mode == "logout" {
		logged := make([]tui.OAuthProvider, 0, len(all))
		for _, p := range all {
			if p.Stored {
				logged = append(logged, p)
			}
		}
		return logged
	}
	return all
}

// beginLogin registers cancel as the active background-login canceller and
// returns a generation token. A later endLogin(token) clears it only if no
// newer login has replaced it.
func (m *InteractiveMode) beginLogin(cancel context.CancelFunc) int {
	m.loginMu.Lock()
	defer m.loginMu.Unlock()
	m.activeLoginGen++
	m.activeLoginCancel = cancel
	return m.activeLoginGen
}

// endLogin clears the active-login canceller if it still belongs to token,
// called when a login goroutine finishes so a subsequent Esc/Ctrl+C is not
// swallowed by a stale login.
func (m *InteractiveMode) endLogin(token int) {
	m.loginMu.Lock()
	defer m.loginMu.Unlock()
	if m.activeLoginGen == token {
		m.activeLoginCancel = nil
	}
}

// cancelActiveLogin aborts an in-progress background login if one is active,
// returning true when it consumed the request. Safe to call on every
// interrupt/clear keystroke: it is a no-op when no login is running.
func (m *InteractiveMode) cancelActiveLogin() bool {
	m.loginMu.Lock()
	cancel := m.activeLoginCancel
	m.activeLoginCancel = nil
	m.loginMu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// runOAuthLogin runs the interactive OAuth login flow for a provider.
// Uses status-line flash + chat messages for the device-flow state.
func (m *InteractiveMode) runOAuthLogin(loginCtx context.Context, provider string) error {
	switch provider {
	case "anthropic":
		return m.runLoginAnthropic(loginCtx)
	case "openai-codex":
		return m.runLoginOpenAICodex(loginCtx)
	default:
		oauthProvider, ok := ai.GetOAuthProvider(provider)
		if !ok {
			return fmt.Errorf("unknown OAuth provider %q", provider)
		}
		return m.runLoginRegisteredOAuth(loginCtx, oauthProvider)
	}
}

// loginDialog is a login dialog driven from a background login goroutine;
// notify wakes the editor-slot loop that renders it.
type loginDialog struct {
	*tui.LoginDialog
	ctx  context.Context
	wake chan struct{}
}

func (d *loginDialog) notify() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// input shows an input prompt and waits for the answer, the dialog's
// cancellation, or ctx.
func (d *loginDialog) input(ctx context.Context, message, placeholder string) (string, error) {
	ch := d.ShowInput(message, placeholder)
	d.notify()
	select {
	case v, ok := <-ch:
		if !ok {
			return "", fmt.Errorf("Login cancelled")
		}
		return v, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-d.ctx.Done():
		return "", d.ctx.Err()
	}
}

func (d *loginDialog) showAuth(url, instructions string) {
	d.ShowAuth(url, instructions)
	d.notify()
	_ = openBrowser(url)
}

func (d *loginDialog) progress(msg string) {
	d.ShowProgress(msg)
	d.notify()
}

// oauthCallbacks returns the dialog-backed OAuth callbacks every dialog login shares.
func (d *loginDialog) oauthCallbacks() ai.OAuthLoginCallbacks {
	return ai.OAuthLoginCallbacks{
		OnDeviceCode: func(info ai.OAuthDeviceCodeInfo) {
			d.showAuth(info.VerificationURI, fmt.Sprintf("Enter code: %s", info.UserCode))
		},
		OnAuth: func(info ai.OAuthAuthInfo) { d.showAuth(info.URL, info.Instructions) },
		OnManualCodeInput: func() (string, error) {
			return d.input(d.ctx, "Paste redirect URL below, or complete login in browser:", "")
		},
		OnProgress: d.progress,
	}
}

// runDialogLogin runs login in the background behind a login dialog titled
// title and stores the credential under providerID. It returns the auth file
// path and whether the login succeeded. Post-login UI belongs to the caller:
// it runs back on the main input-loop goroutine once the dialog closes, where
// a runOnMain post from the login goroutine would deadlock.
func (m *InteractiveMode) runDialogLogin(ctx context.Context, title, providerID string, login func(d *loginDialog) (ai.Credential, error)) (string, bool, error) {
	auth, err := ai.NewAuthStorage(filepath.Join(m.opts.AgentDir, "auth.json"))
	if err != nil {
		return "", false, fmt.Errorf("auth storage: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	d := &loginDialog{LoginDialog: tui.NewLoginDialog(title, cancel), ctx: ctx, wake: make(chan struct{}, 16)}
	go func() {
		defer cancel()
		cred, err := login(d)
		if err != nil {
			if ctx.Err() == nil {
				d.progress(fmt.Sprintf("Login failed: %v", err))
			}
			return
		}
		if err := auth.Set(providerID, cred); err != nil {
			d.progress(fmt.Sprintf("Failed to store credentials: %v", err))
			return
		}
		if m.opts.ModelRegistry != nil {
			m.opts.ModelRegistry.Refresh()
		}
		d.Success()
		d.notify()
	}()
	// ok == !Cancelled() is true only when the goroutine reached d.Success().
	if !m.runLoginDialog(d.LoginDialog, d.wake) {
		return "", false, nil
	}
	return auth.Path(), true, nil
}

func (m *InteractiveMode) runLoginRegisteredOAuth(loginCtx context.Context, provider ai.OAuthProviderInterface) error {
	authPath, ok, err := m.runDialogLogin(loginCtx, provider.Name(), provider.ID(), registeredOAuthLogin(provider))
	if ok {
		m.showStatus(fmt.Sprintf("Logged in to %s. Credentials saved to %s", provider.Name(), authPath))
		m.updateProviderInfo()
	}
	return err
}

// registeredOAuthLogin runs a registered provider's OAuth flow in a login
// dialog.
func registeredOAuthLogin(provider ai.OAuthProviderInterface) func(d *loginDialog) (ai.Credential, error) {
	return func(d *loginDialog) (ai.Credential, error) {
		cb := d.oauthCallbacks()
		cb.OnPrompt = func(value ai.OAuthPrompt) (string, error) {
			input, err := d.input(d.ctx, value.Message, value.Placeholder)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(input) == "" && !value.AllowEmpty {
				return "", fmt.Errorf("%s is required", value.Message)
			}
			return input, nil
		}
		cb.OnSelect = func(value ai.OAuthSelectPrompt) (string, error) {
			if len(value.Options) == 0 {
				return "", fmt.Errorf("%s has no options", value.Message)
			}
			return "", fmt.Errorf("interactive selection is not available for %s; use a provider-specific login command", provider.Name())
		}
		cred, err := provider.Login(d.ctx, cb)
		return ai.Credential{Type: ai.CredentialOAuth, Refresh: cred.Refresh, Access: cred.Access, Expires: cred.Expires, ProjectID: cred.ProjectID}, err
	}
}

// codexLogin runs the OpenAI Codex (ChatGPT) OAuth flow with the chosen
// method in a login dialog.
func codexLogin(method string) func(d *loginDialog) (ai.Credential, error) {
	return func(d *loginDialog) (ai.Credential, error) {
		cb := d.oauthCallbacks()
		cb.OnSelect = func(ai.OAuthSelectPrompt) (string, error) { return method, nil }
		cred, err := ai.LoginOpenAICodex(d.ctx, cb)
		return ai.Credential{Type: ai.CredentialOAuth, Refresh: cred.Refresh, Access: cred.Access, Expires: cred.Expires}, err
	}
}

// anthropicLogin runs the Claude Pro/Max OAuth flow in a login dialog,
// with the paste-the-redirect fallback the dialog offers.
func anthropicLogin(d *loginDialog) (ai.Credential, error) {
	cred, err := ai.LoginAnthropic(d.ctx, d.oauthCallbacks())
	return ai.Credential{Type: ai.CredentialOAuth, Refresh: cred.Refresh, Access: cred.Access, Expires: cred.Expires}, err
}

// copilotLogin runs the GitHub Copilot device flow in a login dialog.
func copilotLogin(d *loginDialog) (ai.Credential, error) {
	return ai.LoginGitHubCopilot(d.ctx, ai.CopilotLoginCallbacks{
		OnPrompt: func(promptCtx context.Context) (string, error) {
			return d.input(promptCtx, "GitHub Enterprise URL/domain (blank for github.com)", "company.ghe.com")
		},
		OnAuth: func(verificationURL, userCode string) {
			d.showAuth(verificationURL, fmt.Sprintf("Enter code: %s", userCode))
		},
		OnProgress: d.progress,
	})
}

// runLoginOpenAICodex runs the OpenAI Codex (ChatGPT) OAuth flow.
// It first presents a method selector (browser vs device-code), then runs
// the chosen flow.
// The browser path uses the PKCE + localhost callback dialog; the device-code
// path (RFC 8628) shows the user code while polling.
func (m *InteractiveMode) runLoginOpenAICodex(loginCtx context.Context) error {
	// Method selector.
	methodSel := tui.NewSlotSelector("Select OpenAI Codex login method:", []string{
		"Browser login (default)",
		"Device code login (headless)",
	})
	methodIdx, methodOK := m.runSlotSelector(methodSel)
	if !methodOK {
		return nil
	}
	loginMethod := ai.OpenAICodexBrowserLoginMethod
	if methodIdx == 1 {
		loginMethod = ai.OpenAICodexDeviceCodeLoginMethod
	}

	authPath, ok, err := m.runDialogLogin(loginCtx, buildAuthProviderName("openai-codex"), "openai-codex", codexLogin(loginMethod))
	if ok {
		m.showStatus(fmt.Sprintf("Logged in to OpenAI Codex. Credentials saved to %s", authPath))
		m.updateProviderInfo()
	}
	return err
}

// runLoginAnthropic runs the Anthropic Claude Pro/Max OAuth flow (PKCE +
// localhost callback). It follows the github-copilot login layout but uses
// the authorization-code/PKCE shape from ai.LoginAnthropic. A manual
// "paste redirect URL" fallback is intentionally not wired here: the wopr
// line renderer does not own the editor input the way an overlay would, and
// the localhost callback covers the same-machine path. On a remote machine,
// run `wopr login anthropic` from a terminal that can reach the callback URL,
// or set ANTHROPIC_API_KEY directly.
func (m *InteractiveMode) runLoginAnthropic(loginCtx context.Context) error {
	auth, err := ai.NewAuthStorage(filepath.Join(m.opts.AgentDir, "auth.json"))
	if err != nil {
		return fmt.Errorf("auth storage: %w", err)
	}

	m.appendChatBlock(tui.NewMarkdown("**Login to " + ai.AnthropicOAuthDisplayName + "**"))

	loginCtx, loginCancel := context.WithCancel(loginCtx)
	loginToken := m.beginLogin(loginCancel)

	cb := ai.OAuthLoginCallbacks{
		OnAuth: func(info ai.OAuthAuthInfo) {
			msg := fmt.Sprintf(
				"1. Open: %s\n2. Authorize in your browser.\n3. The callback will return automatically.\n\n%s\n\nWaiting for authorization... (Ctrl+C to cancel)",
				info.URL, info.Instructions)
			m.runOnMain(m.runCtx, func() {
				m.appendChatBlock(tui.NewMarkdown(msg))
				m.tuiInst.Render()
			})
			_ = openBrowser(info.URL)
		},
		OnProgress: func(msg string) { m.runOnMain(m.runCtx, func() { m.showStatus(msg) }) },
	}

	go func() {
		defer loginCancel()
		defer m.endLogin(loginToken)

		cred, err := ai.LoginAnthropic(loginCtx, cb)
		if err != nil {
			m.runOnMain(m.runCtx, func() {
				if loginCtx.Err() != nil {
					m.appendChatBlock(tui.NewMarkdown("Login cancelled."))
				} else {
					m.appendChatBlock(tui.NewMarkdown(fmt.Sprintf("Login failed: %v", err)))
				}
				m.tuiInst.ForceFullRender()
				m.tuiInst.Render()
			})
			return
		}

		anthCred := ai.Credential{
			Type:    ai.CredentialOAuth,
			Refresh: cred.Refresh,
			Access:  cred.Access,
			Expires: cred.Expires,
		}
		if err := auth.Set("anthropic", anthCred); err != nil {
			m.runOnMain(m.runCtx, func() {
				m.appendChatBlock(tui.NewMarkdown(fmt.Sprintf("Failed to store credentials: %v", err)))
				m.tuiInst.ForceFullRender()
				m.tuiInst.Render()
			})
			return
		}

		if m.opts.ModelRegistry != nil {
			m.opts.ModelRegistry.Refresh()
		}

		// The status touches UI state the main loop owns, so apply it there.
		// Picking a model is /setup's or /model's job, not login's.
		m.runOnMain(m.runCtx, func() {
			authPath := auth.Path()
			status := fmt.Sprintf("Logged in to Anthropic. Credentials saved to %s", authPath)
			m.showStatus(status)
			m.appendToChat(tui.NewMarkdown("✓ " + status))
			m.updateProviderInfo()
			m.tuiInst.ForceFullRender()
			m.tuiInst.Render()
		})
	}()

	return nil
}

// runLoginGitHubCopilotDialog runs the user-invoked GitHub Copilot login flow
// with the login dialog surface.
func (m *InteractiveMode) runLoginGitHubCopilotDialog(loginCtx context.Context) error {
	authPath, ok, err := m.runDialogLogin(loginCtx, buildAuthProviderName("github-copilot"), "github-copilot", copilotLogin)
	if ok {
		status := fmt.Sprintf("Logged in to GitHub Copilot. Credentials saved to %s", authPath)
		m.showStatus(status)
		m.appendToChat(tui.NewMarkdown("✓ " + status))
		m.updateProviderInfo()
		m.tuiInst.ForceFullRender()
		m.tuiInst.Render()
	}
	return err
}

// runOAuthLogout removes stored OAuth credentials for a provider.
func (m *InteractiveMode) runOAuthLogout(provider string) error {
	auth, err := ai.NewAuthStorage(filepath.Join(m.opts.AgentDir, "auth.json"))
	if err != nil {
		return fmt.Errorf("auth storage: %w", err)
	}

	deleted := false
	if _, ok, _ := auth.Get(provider); ok {
		if err := auth.Delete(provider); err != nil {
			return fmt.Errorf("logout: %w", err)
		}
		deleted = true
	}
	// Logout also drops the provider's --api-key runtime key.
	if m.opts.ModelRegistry != nil {
		if _, ok := m.opts.ModelRegistry.RuntimeAPIKey(provider); ok {
			m.opts.ModelRegistry.RemoveRuntimeAPIKey(provider)
			deleted = true
		}
	}
	if !deleted {
		m.statusLine.Flash(fmt.Sprintf("No credentials stored for %q. Use /login first.", provider))
		return nil
	}

	m.updateProviderInfo()
	m.tuiInst.ForceFullRender()
	m.tuiInst.Render()
	return nil
}

// applyEditorMaxVisible sets the editor's max visible visual-line cap
// to `max(5, floor(terminalRows * 0.3))`.
// Called on startup and SIGWINCH.
func (m *InteractiveMode) applyEditorMaxVisible() {
	rows := m.tuiInst.Height()
	if rows <= 0 {
		rows = 30
	}
	cap := max(rows*30/100, 5)
	m.editor.SetMaxVisibleLines(cap)
}

// updateProviderInfo updates the footer from the available model snapshot or the active scope, including dynamically registered providers. It does not refresh catalogs.
func (m *InteractiveMode) updateProviderInfo() {
	items := m.availableModelItems()
	providers := make(map[string]struct{})
	for _, item := range items {
		providers[item.Provider] = struct{}{}
	}
	m.statusLine.SetProviderCount(len(providers))

	m.statusLine.SetUsingSubscription(m.footerUsingSubscription(m.opts.Model))
}

// footerUsingSubscription reports whether the footer shows "(sub)": Kimi
// Coding is subscription-backed despite API-key authentication; any other
// provider needs a stored OAuth login whose provider is a subscription login.
func (m *InteractiveMode) footerUsingSubscription(model *ai.Model) bool {
	if model == nil {
		return false
	}
	providerID := model.ProviderMeta.ProviderID
	if providerID == "" && model.Provider != nil {
		providerID = model.Provider.ID()
	}
	if providerID == "kimi-coding" {
		return true
	}
	if !ai.IsOAuthSubscriptionProvider(providerID) {
		return false
	}
	auth, err := ai.NewAuthStorage(filepath.Join(m.opts.AgentDir, "auth.json"))
	if err != nil {
		return false
	}
	cred, ok, err := auth.Get(providerID)
	return ok && err == nil && cred.Type == ai.CredentialOAuth
}

// newFooter builds the footer bound to the session's stored usage totals and
// to the active model's subscription marker.
func (m *InteractiveMode) newFooter() *StatusLine {
	footer := NewStatusLine(m.opts.Model, nil)
	footer.SetUsageTotalsSource(m.footerUsageTotals)
	footer.SetSubscriptionResolver(m.footerUsingSubscription)
	return footer
}

// footerUsageTotals reads the current session's all-entry usage totals.
func (m *InteractiveMode) footerUsageTotals() footerUsageTotals {
	session := m.currentSession()
	if session == nil {
		return footerUsageTotals{}
	}
	return session.FooterUsageTotals()
}

// openBrowser opens a URL in the default browser.
func openBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "windows":
		cmd = "cmd"
		args = []string{"/c", "start", url}
	default: // linux, freebsd, etc.
		cmd = "xdg-open"
		args = []string{url}
	}
	return exec.Command(cmd, args...).Start()
}

func formatProviderErrorForDisplay(stopReason, raw string) (statusText, chatText string) {
	clean := compactProviderError(raw)
	if strings.Contains(clean, "github-copilot") && (strings.Contains(clean, "Bad credentials") || strings.Contains(clean, "HTTP 401") || strings.Contains(clean, "token refresh failed")) {
		// Name both possible causes rather
		// than asserting expiry, since a refresh also fails on rate limits,
		// network loss, and provider outages. The provider's own text renders
		// in the assistant block below and carries the specific reason.
		return "GitHub Copilot authentication failed: credentials may have expired or network is unavailable; run wopr login", clean
	}
	verb := "failed"
	if stopReason == "aborted" {
		verb = "aborted"
	}
	statusText = "Provider request " + verb
	chatText = clean
	if len(chatText) > 360 {
		chatText = chatText[:357] + "..."
	}
	return statusText, chatText
}

// networkFailures name the network failures a user can act on, matched in
// the "network error: …" text the provider transport reports.
var networkFailures = []struct{ match, what string }{
	{"connection refused", "isn't reachable (connection refused)"},
	{"no such host", "isn't reachable (host not found)"},
	{"i/o timeout", "didn't answer (timed out)"},
	{"handshake timeout", "didn't answer (timed out)"},
	{"stream interrupted", "stopped responding (connection closed)"},
	{"closed network connection", "stopped responding (connection closed)"},
	{"connection reset", "stopped responding (connection reset)"},
	{"unexpected eof", "stopped responding (connection closed)"},
	{"broken pipe", "stopped responding (connection closed)"},
}

// networkFailure names a network failure, e.g. "isn't reachable
// (connection refused)"; ok is false for any other error.
func networkFailure(raw string) (what string, ok bool) {
	lower := strings.ToLower(raw)
	if !strings.Contains(lower, "network error") {
		return "", false
	}
	for _, f := range networkFailures {
		if strings.Contains(lower, f.match) {
			return f.what, true
		}
	}
	return "stopped responding (network error)", true
}

// assistantErrorText is the error a failed reply shows: a sentence naming
// the model's server for a network failure, else the provider's own
// message, with the attempt count when retries ran.
func (m *InteractiveMode) assistantErrorText(message *agent.AssistantMessage, attempts int) string {
	if message == nil {
		return ""
	}
	after := ""
	if attempts > 1 {
		after = fmt.Sprintf(" after %d attempts", attempts)
	}
	if what, ok := networkFailure(message.ErrorMessage); ok {
		return cmp.Or(m.providerName(message.Provider), "The model server") + " " + what + after + ". Try again, or switch models with /model or Tab."
	}
	if after != "" {
		return message.ErrorMessage + " (" + strings.TrimPrefix(after, " ") + ")"
	}
	return message.ErrorMessage
}

func compactProviderError(raw string) string {
	clean := strings.TrimSpace(raw)
	clean = strings.ReplaceAll(clean, "\r\n", " ")
	clean = strings.ReplaceAll(clean, "\n", " ")
	clean = strings.Join(strings.Fields(clean), " ")
	return clean
}

// finalizeRunningTools freezes every tool component still in ToolStateRunning,
// stopping its live "Elapsed X.Xs" footer from recomputing time.Since(start) on
// every subsequent render. While a tool block stays running after it has
// scrolled above the viewport, each recompute changes a line the differential
// renderer cannot reach in place, forcing a full clearing repaint (the flicker
// seen after aborting a long-running tool). Called from the Esc abort dispatch
// and from agent_end; idempotent because FinalizeAborted no-ops once a tool is
// terminal, and a genuine ToolExecutionEnd arriving later still overwrites the
// frozen placeholder with the real result via SetResult.
