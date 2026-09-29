package codingagent

import (
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/web"
	"github.com/alexrudloff/wopr/tui"
)

// searchKeyIDs are the auth.json entries search keys are stored under.
var searchKeyIDs = map[string]string{"brave": "brave-search", "tavily": "tavily"}

// webSearchConfig is the search section of settings.json.
func (w *setupWizard) webSearchConfig() web.SearchConfig {
	global, project := w.m.opts.SettingsManager.RawSection("web")
	return web.ParseConfig(global, project).Search
}

// webSearchBackend resolves search the way a session does: settings, then
// the environment, then keys stored here.
func (w *setupWizard) webSearchBackend() *web.Backend {
	auth, _ := ai.NewAuthStorage(filepath.Join(w.m.opts.AgentDir, "auth.json"))
	stored := func(id string) string {
		if auth == nil {
			return ""
		}
		cred, _, _ := auth.Get(id)
		return cred.Key
	}
	return web.NewSearchTool(w.webSearchConfig(), web.SearchLookup(os.Getenv, stored)).Backend
}

// webSearchSummary is web search's state on the main screen.
func (w *setupWizard) webSearchSummary() string {
	if b := w.webSearchBackend(); b.Keyed() {
		return b.Describe()
	}
	return "provider search, else DuckDuckGo"
}

// webSearchScreen sets the search keys and the SearXNG instance. With none,
// Claude and GPT models use their provider's own search and every other
// model uses DuckDuckGo.
func (w *setupWizard) webSearchScreen() {
	const back, searx, private = "\x00back", "\x00searx", "\x00private"
	for {
		cfg := w.webSearchConfig()
		auth, _ := ai.NewAuthStorage(filepath.Join(w.m.opts.AgentDir, "auth.json"))
		keyState := func(provider string) string {
			if auth != nil {
				if _, ok, _ := auth.Get(searchKeyIDs[provider]); ok {
					return "key saved"
				}
			}
			return "no key"
		}
		options := []tui.DialogOption{
			{Title: "Brave Search", Description: "an API key; used first when set", Footer: keyState("brave"), Category: "Search providers", Value: "brave"},
			{Title: "Tavily", Description: "an API key", Footer: keyState("tavily"), Category: "Search providers", Value: "tavily"},
			{Title: "SearXNG", Description: "a search instance you run", Footer: cmp.Or(cfg.URL, "not set"), Category: "Search providers", Value: searx},
		}
		if cfg.URL != "" {
			mark := "[ ] "
			if cfg.Private {
				mark = "[x] "
			}
			options = append(options, tui.DialogOption{Title: mark + "Private SearXNG", Description: "private mode counts its searches as private", Category: "Search providers", Value: private})
		}
		options = append(options, tui.DialogOption{Title: "Back", Value: back, Pinned: true})
		d := tui.NewDialogSelect("Web search · "+w.webSearchSummary(), options, "")
		chosen, ok := w.sel(d)
		if !ok || chosen.Value == back {
			return
		}
		switch chosen.Value {
		case "brave", "tavily":
			w.searchKey(chosen.Value, chosen.Title, keyState(chosen.Value) == "key saved")
		case searx:
			w.searxURL(cfg.URL)
		case private:
			w.setWebSearch(func(src string) (string, error) {
				return jsoncSet(src, strconv.FormatBool(!cfg.Private), "web", "search", "private")
			})
		}
	}
}

// searchKey stores or removes a search provider's key.
func (w *setupWizard) searchKey(provider, name string, saved bool) {
	if saved {
		d := tui.NewDialogSelect(name, []tui.DialogOption{
			{Title: "Replace the key", Value: "replace"},
			{Title: "Remove the key", Value: "remove"},
			{Title: "Back", Value: "back", Pinned: true},
		}, "")
		chosen, ok := w.sel(d)
		if !ok || chosen.Value == "back" {
			return
		}
		if chosen.Value == "remove" {
			if err := w.setStoredKey(searchKeyIDs[provider], "", "remove"); err != nil {
				w.showError("Removing the key", err)
				return
			}
			w.m.showFlash(name + " key removed")
			return
		}
	}
	key, ok := w.askKey(name)
	if !ok {
		return
	}
	if err := w.setStoredKey(searchKeyIDs[provider], strings.TrimSpace(key), "add a key"); err != nil {
		w.showError("Saving the key", err)
		return
	}
	w.m.showFlash(name + " key saved")
}

// searxURL sets or clears the SearXNG instance.
func (w *setupWizard) searxURL(current string) {
	field := tui.NewTextField("URL", current, "http://localhost:8888", false)
	f := tui.NewForm("SearXNG", field)
	f.Submit = "Save"
	if !w.form(f) {
		return
	}
	url := strings.TrimSpace(field.Value())
	w.setWebSearch(func(src string) (string, error) {
		if url == "" {
			return jsoncSet(src, `""`, "web", "search", "url")
		}
		value, _ := json.Marshal(url)
		out, err := jsoncSet(src, string(value), "web", "search", "url")
		if err != nil {
			return "", err
		}
		return jsoncSet(out, `"searxng"`, "web", "search", "provider")
	})
}

func (w *setupWizard) setWebSearch(merge func(string) (string, error)) {
	if err := w.writeConfig("settings.json", merge); err != nil {
		w.showError("Saving web search", err)
		return
	}
	w.m.showFlash("Web search saved")
}
