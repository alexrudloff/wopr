package codingagent

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"

	"github.com/alexrudloff/wopr/internal/codingagent/tools"
	"github.com/alexrudloff/wopr/internal/text"
	"github.com/alexrudloff/wopr/tui"
)

// ─── Settings file shape ─────────────────────────────────────────────────────

// Settings is one settings layer (global, project, or the merge of both) in
// the settings.json shape. Pointer fields tell an explicit false or zero from
// an unset key; the getters apply the defaults.
type Settings struct {
	DefaultProvider      string `json:"defaultProvider,omitempty"`
	DefaultModel         string `json:"defaultModel,omitempty"`
	DefaultThinkingLevel string `json:"defaultThinkingLevel,omitempty"`
	// Routing is the orchestrator's last choice, restored at the next start
	// while routing is on: a mode name (the router picks the orchestrator
	// under it) or "pinned" (the default model orchestrates).
	Routing string `json:"routing,omitempty"`
	// Subagents is the subagents' last choice, restored at the next start:
	// a mode name (routed under it), a "provider/model" spec, or "same"
	// (the orchestrator's model).
	Subagents string `json:"subagents,omitempty"`
	// ModelThinkingLevels holds per-model default thinking levels keyed by
	// "provider/modelId".
	ModelThinkingLevels map[string]string `json:"modelThinkingLevels,omitempty"`
	// Transport is "sse", "websocket", "websocket-cached", or "auto" (the
	// default).
	Transport string `json:"transport,omitempty"`
	// SteeringMode and FollowUpMode dispatch queued messages "all" at once or
	// "one-at-a-time" (the default).
	SteeringMode string `json:"steeringMode,omitempty"`
	FollowUpMode string `json:"followUpMode,omitempty"`

	Theme string `json:"theme,omitempty"`
	// FullscreenExitOutput is "clear" (the default), "transcript", or
	// "resume-hint".
	FullscreenExitOutput string `json:"fullscreenExitOutput,omitempty"`
	// FullscreenScrollbar is "auto" (the default), "always", or "hidden".
	FullscreenScrollbar string `json:"fullscreenScrollbar,omitempty"`
	// FullscreenSidebar is "" (auto: shown when the terminal is wide enough),
	// "show", or "hide"; ctrl+x b toggles it and saves the choice.
	FullscreenSidebar string `json:"fullscreenSidebar,omitempty"`
	// FullscreenCopyOnSelect copies a completed selection. Default: true.
	FullscreenCopyOnSelect *bool `json:"fullscreenCopyOnSelect,omitempty"`
	// FullscreenScrollSpeed is the lines one wheel step scrolls (1-10,
	// default 3).
	FullscreenScrollSpeed *int `json:"fullscreenScrollSpeed,omitempty"`

	Compaction    *CompactionSettingsJSON `json:"compaction,omitempty"`
	BranchSummary *BranchSummarySettings  `json:"branchSummary,omitempty"`
	Retry         *RetrySettingsJSON      `json:"retry,omitempty"`

	// HideThinkingBlock shows thinking blocks as stubs; Ctrl+T toggles it.
	HideThinkingBlock *bool  `json:"hideThinkingBlock,omitempty"`
	ShellPath         string `json:"shellPath,omitempty"`
	// CommandPrefix is prepended to every bash tool command as its own
	// first line (e.g. "set -e").
	CommandPrefix     string `json:"shellCommandPrefix,omitempty"`
	QuietStartup      *bool  `json:"quietStartup,omitempty"`
	CollapseChangelog *bool  `json:"collapseChangelog,omitempty"`
	// ShowCacheMissNotices adds transcript notices for significant prompt
	// cache misses. Default: false.
	ShowCacheMissNotices *bool `json:"showCacheMissNotices,omitempty"`
	// EnableAttributionHeaders sends wopr's app-attribution headers to
	// OpenRouter, NVIDIA, and Cloudflare. Default: true.
	EnableAttributionHeaders *bool `json:"enableAttributionHeaders,omitempty"`

	Skills  []string `json:"skills,omitempty"`
	Prompts []string `json:"prompts,omitempty"`
	Themes  []string `json:"themes,omitempty"`
	// CleanupTempFiles deletes the temp files sessions create (default on).
	CleanupTempFiles *bool `json:"cleanupTempFiles,omitempty"`
	// WarCouncil configures Global Thermonuclear War's council.
	WarCouncil *WarCouncilSettings `json:"warCouncil,omitempty"`
	// EnableSkillCommands lists /skill:name commands in autocomplete.
	// Default: true.
	EnableSkillCommands *bool `json:"enableSkillCommands,omitempty"`

	Terminal *TerminalSettings `json:"terminal,omitempty"`
	Images   *ImageSettings    `json:"images,omitempty"`

	// DefaultTools is the initial built-in tool selection; nil means the
	// default read, bash, edit and write.
	DefaultTools []string `json:"defaultTools,omitempty"`
	// DoubleEscapeAction is what double-Esc on an empty editor does: "fork",
	// "tree" (the default), or "none".
	DoubleEscapeAction string `json:"doubleEscapeAction,omitempty"`
	// TreeFilterMode is the filter /tree opens with.
	TreeFilterMode string `json:"treeFilterMode,omitempty"`
	// Hashline selects hashline anchors on read and edit: "auto" (the
	// default: on for models on free router tiers), "on", or "off". A router
	// model's own "hashline" flag wins.
	Hashline string `json:"hashline,omitempty"`
	// Diagnostics configures the checker edit and write run on the file they
	// changed.
	Diagnostics *DiagnosticsSettings `json:"diagnostics,omitempty"`
	// BashCompaction drops known noise (git hints, passing test lines) from
	// bash output before the model sees it. Default: true.
	BashCompaction *bool `json:"bashCompaction,omitempty"`
	// DefaultProjectTrust applies when no saved decision resolves project
	// trust: "ask" (the default), "always", or "never". Global only.
	DefaultProjectTrust string                   `json:"defaultProjectTrust,omitempty"`
	ThinkingBudgets     *ThinkingBudgetsSettings `json:"thinkingBudgets,omitempty"`
	// EditorPaddingX (0-3, default 0), OutputPad (0-1, default 1), and
	// AutocompleteMaxVisible (3-20, default 5) size the prompt and transcript.
	EditorPaddingX         *int   `json:"editorPaddingX,omitempty"`
	OutputPad              *int   `json:"outputPad,omitempty"`
	AutocompleteMaxVisible *int   `json:"autocompleteMaxVisible,omitempty"`
	ExternalEditor         string `json:"externalEditor,omitempty"`
	// ShowHardwareCursor shows the terminal's own cursor. Default: false, or
	// WOPR_HARDWARE_CURSOR=1.
	ShowHardwareCursor *bool             `json:"showHardwareCursor,omitempty"`
	Markdown           *MarkdownSettings `json:"markdown,omitempty"`
	SessionDir         string            `json:"sessionDir,omitempty"`
	// HTTPIdleTimeoutMs keeps the authored value: a number, a numeric string,
	// or "disabled". GetHttpIdleTimeoutMs parses it.
	HTTPIdleTimeoutMs json.RawMessage `json:"httpIdleTimeoutMs,omitempty"`
	// CacheWarming is read from global settings only, because each refresh
	// costs money.
	CacheWarming CacheWarmingMode `json:"cacheWarming,omitempty"`
	// LastChangelogVersion is the version whose changelog the user last saw.
	LastChangelogVersion string `json:"lastChangelogVersion,omitempty"`
}

// TerminalSettings is settings.terminal.
type TerminalSettings struct {
	// ShowImages renders images when the terminal supports them. Default: true.
	ShowImages *bool `json:"showImages,omitempty"`
	// ImageWidthCells is the rendered image width. Default: 60.
	ImageWidthCells      int   `json:"imageWidthCells,omitempty"`
	ShowTerminalProgress *bool `json:"showTerminalProgress,omitempty"`
	// Hyperlinks, Images, and TrueColor keep the authored value; see
	// GetTerminalCapabilityOverrides.
	Hyperlinks json.RawMessage `json:"hyperlinks,omitempty"`
	Images     json.RawMessage `json:"images,omitempty"`
	TrueColor  json.RawMessage `json:"trueColor,omitempty"`
}

// ImageSettings is settings.images.
type ImageSettings struct {
	// AutoResize fits images to the width. Default: true.
	AutoResize *bool `json:"autoResize,omitempty"`
	// BlockImages hides images in the transcript. Default: false.
	BlockImages *bool `json:"blockImages,omitempty"`
}

// CompactionSettingsJSON is settings.compaction.
type CompactionSettingsJSON struct {
	Enabled          *bool `json:"enabled,omitempty"`
	ReserveTokens    *int  `json:"reserveTokens,omitempty"`
	KeepRecentTokens *int  `json:"keepRecentTokens,omitempty"`
	// ModelOverrides maps exact "provider/modelId" keys to token settings
	// that take precedence for that model.
	ModelOverrides map[string]CompactionModelOverride `json:"modelOverrides,omitempty"`
}

// CompactionModelOverride holds per-model compaction token settings.
type CompactionModelOverride struct {
	ReserveTokens    *int `json:"reserveTokens,omitempty"`
	KeepRecentTokens *int `json:"keepRecentTokens,omitempty"`
}

// BranchSummarySettings is settings.branchSummary.
type BranchSummarySettings struct {
	ReserveTokens *int  `json:"reserveTokens,omitempty"`
	SkipPrompt    *bool `json:"skipPrompt,omitempty"`
}

// RetrySettingsJSON is settings.retry. Defaults: enabled, maxRetries 3,
// baseDelayMs 2000, maxAgentDelayMs 60000.
type RetrySettingsJSON struct {
	Enabled         *bool                  `json:"enabled,omitempty"`
	MaxRetries      *int                   `json:"maxRetries,omitempty"`
	BaseDelayMs     *int                   `json:"baseDelayMs,omitempty"`
	MaxAgentDelayMs *int                   `json:"maxAgentDelayMs,omitempty"`
	Provider        *ProviderRetrySettings `json:"provider,omitempty"`
}

// ProviderRetrySettings is settings.retry.provider: provider request
// retries and deadlines.
type ProviderRetrySettings struct {
	TimeoutMs  *int `json:"timeoutMs,omitempty"`
	MaxRetries *int `json:"maxRetries,omitempty"`
	// MaxRetryDelayMs caps each retry delay; an explicit 0 removes the cap.
	// Default: 60000.
	MaxRetryDelayMs *int `json:"maxRetryDelayMs,omitempty"`
}

// ThinkingBudgetsSettings holds custom token budgets for each thinking level.
type ThinkingBudgetsSettings struct {
	Minimal *int `json:"minimal,omitempty"`
	Low     *int `json:"low,omitempty"`
	Medium  *int `json:"medium,omitempty"`
	High    *int `json:"high,omitempty"`
}

// MarkdownSettings holds markdown rendering settings.
type MarkdownSettings struct {
	// Mermaid is "streaming" (the default), "final", or "off".
	Mermaid string `json:"mermaid,omitempty"`
}

// DiagnosticsSettings configures post-edit diagnostics.
type DiagnosticsSettings struct {
	// Enabled defaults to true.
	Enabled *bool `json:"enabled,omitempty"`
	// TimeoutMs bounds one checker run (default 4000).
	TimeoutMs int `json:"timeoutMs,omitempty"`
	// Commands overrides the checker per language (go, typescript,
	// javascript, python, shell) with {file} and {dir} placeholders; "off"
	// turns a language off.
	Commands map[string]string `json:"commands,omitempty"`
}

// CacheWarmingMode selects when prompt caches are kept warm.
type CacheWarmingMode string

// CacheWarmingModes lists the modes in display order.
var CacheWarmingModes = []CacheWarmingMode{"off", "streaming", "idle"}

// defaultCacheWarmingMode warms only while the agent runs.
const defaultCacheWarmingMode CacheWarmingMode = "streaming"

const defaultHTTPIdleTimeoutMs = 300_000

// UnmarshalJSON decodes a settings file. A value whose JSON type does not
// match its key is dropped and the rest of the file still loads. The older
// object form of "skills" and retry.maxDelayMs are still read.
func (s *Settings) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if skills, ok := raw["skills"].(map[string]any); ok {
		if _, exists := raw["enableSkillCommands"]; !exists {
			if enabled, ok := skills["enableSkillCommands"]; ok {
				raw["enableSkillCommands"] = enabled
			}
		}
		if dirs, ok := skills["customDirectories"].([]any); ok && len(dirs) > 0 {
			raw["skills"] = dirs
		} else {
			delete(raw, "skills")
		}
	}
	if retry, ok := raw["retry"].(map[string]any); ok {
		if maxDelayMs, ok := retry["maxDelayMs"]; ok {
			provider, _ := retry["provider"].(map[string]any)
			if provider == nil {
				provider = map[string]any{}
			}
			if _, exists := provider["maxRetryDelayMs"]; !exists {
				provider["maxRetryDelayMs"] = maxDelayMs
			}
			retry["provider"] = provider
			delete(retry, "maxDelayMs")
		}
	}
	type plain Settings
	var decoded plain
	if err := decodeTolerant(raw, &decoded); err != nil {
		return err
	}
	*s = Settings(decoded)
	return nil
}

// decodeTolerant decodes raw into out. A field whose JSON type does not
// match is dropped; when the error names no removable path, the undecodable
// top-level keys and array elements go once and decoding retries.
func decodeTolerant[T any](raw map[string]any, out *T) error {
	decodes := func(key string, value any) bool {
		data, err := json.Marshal(map[string]any{key: value})
		if err != nil {
			return false
		}
		var probe T
		return json.Unmarshal(data, &probe) == nil
	}
	sanitized := false
	for {
		normalized, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		var decoded T
		err = json.Unmarshal(normalized, &decoded)
		if err == nil {
			*out = decoded
			return nil
		}
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) && deleteJSONPath(raw, strings.Split(typeErr.Field, ".")) {
			continue
		}
		if sanitized {
			return err
		}
		sanitized = true
		removed := false
		for key, value := range raw {
			if decodes(key, value) {
				continue
			}
			removed = true
			items, isArray := value.([]any)
			if !isArray {
				delete(raw, key)
				continue
			}
			kept := make([]any, 0, len(items))
			for _, item := range items {
				if decodes(key, []any{item}) {
					kept = append(kept, item)
				}
			}
			raw[key] = kept
		}
		if !removed {
			return err
		}
	}
}

// deleteJSONPath removes the deepest object key along path that exists. It
// reports whether it removed one.
func deleteJSONPath(object map[string]any, path []string) bool {
	if len(path) == 0 || path[0] == "" {
		return false
	}
	value, ok := object[path[0]]
	if !ok {
		return false
	}
	if nested, isObject := value.(map[string]any); isObject && len(path) > 1 && deleteJSONPath(nested, path[1:]) {
		return true
	}
	delete(object, path[0])
	return true
}

// clone returns a deep copy of s.
func (s Settings) clone() Settings {
	data, err := json.Marshal(s)
	if err != nil {
		return s
	}
	var out Settings
	if err := json.Unmarshal(data, &out); err != nil {
		return s
	}
	return out
}

// mergeSettings lays over on base: keys over sets override, objects merge key
// by key, and arrays and other values replace.
func mergeSettings(base, over Settings) Settings {
	merged, err := settingsJSONMap(base)
	if err != nil {
		return base
	}
	top, err := settingsJSONMap(over)
	if err != nil {
		return base
	}
	mergeJSONObject(merged, top)
	data, err := json.Marshal(merged)
	if err != nil {
		return base
	}
	var out Settings
	if err := json.Unmarshal(data, &out); err != nil {
		return base
	}
	return out
}

func mergeJSONObject(base, over map[string]json.RawMessage) {
	for key, value := range over {
		overObject, overIsObject := rawJSONObject(value)
		baseObject, baseIsObject := rawJSONObject(base[key])
		if !overIsObject || !baseIsObject {
			base[key] = value
			continue
		}
		mergeJSONObject(baseObject, overObject)
		if encoded, err := json.Marshal(baseObject); err == nil {
			base[key] = encoded
		}
	}
}

// ─── Getters ────────────────────────────────────────────────────────────────

// terminal and images return the nested settings, creating them for a
// write.
func (s *Settings) terminal() *TerminalSettings {
	if s.Terminal == nil {
		s.Terminal = &TerminalSettings{}
	}
	return s.Terminal
}

func (s *Settings) images() *ImageSettings {
	if s.Images == nil {
		s.Images = &ImageSettings{}
	}
	return s.Images
}

// GetCacheWarmingMode returns the cache-warming mode, or "streaming" when it
// is unset or invalid.
func (s Settings) GetCacheWarmingMode() CacheWarmingMode {
	if slices.Contains(CacheWarmingModes, s.CacheWarming) {
		return s.CacheWarming
	}
	return defaultCacheWarmingMode
}

func boolOr(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}

// GetShellPath satisfies tools.SettingsView so the bash tool can resolve the
// user's preferred shell without importing internal/codingagent (which would
// create a cycle).
func (s Settings) GetShellPath() (string, error) { return normalizeSettingsPath(s.ShellPath) }
func (s Settings) GetCommandPrefix() string      { return s.CommandPrefix }

// GetShowImages returns whether images render. Default: true.
func (s Settings) GetShowImages() bool {
	return s.Terminal == nil || boolOr(s.Terminal.ShowImages, true)
}

// GetImageWidthCells returns the max image width in columns. Default: 60.
func (s Settings) GetImageWidthCells() int {
	if s.Terminal == nil || s.Terminal.ImageWidthCells <= 0 {
		return 60
	}
	return s.Terminal.ImageWidthCells
}

// GetShowTerminalProgress returns whether OSC 9;4 progress is shown.
func (s Settings) GetShowTerminalProgress() bool {
	return s.Terminal != nil && boolOr(s.Terminal.ShowTerminalProgress, false)
}

// GetImageAutoResize returns whether images resize to fit. Default: true.
func (s Settings) GetImageAutoResize() bool {
	return s.Images == nil || boolOr(s.Images.AutoResize, true)
}

// GetBlockImages returns whether the transcript hides images.
func (s Settings) GetBlockImages() bool {
	return s.Images != nil && boolOr(s.Images.BlockImages, false)
}

// GetShowHardwareCursor returns whether the hardware cursor is shown.
func (s Settings) GetShowHardwareCursor() bool {
	return boolOr(s.ShowHardwareCursor, os.Getenv("WOPR_HARDWARE_CURSOR") == "1")
}

// GetEditorPaddingX returns horizontal editor padding. Default: 0.
func (s Settings) GetEditorPaddingX() int {
	if s.EditorPaddingX == nil {
		return 0
	}
	return *s.EditorPaddingX
}

// GetOutputPad returns horizontal output padding, 0 or 1. Default: 1.
func (s Settings) GetOutputPad() int {
	if s.OutputPad == nil {
		return 1
	}
	return max(0, min(1, *s.OutputPad))
}

// GetAutocompleteMaxVisible returns the autocomplete rows. Default: 5.
func (s Settings) GetAutocompleteMaxVisible() int {
	if s.AutocompleteMaxVisible == nil {
		return 5
	}
	return *s.AutocompleteMaxVisible
}

func (s Settings) GetHideThinkingBlock() bool    { return boolOr(s.HideThinkingBlock, false) }
func (s Settings) GetShowCacheMissNotices() bool { return boolOr(s.ShowCacheMissNotices, false) }
func (s Settings) GetQuietStartup() bool         { return boolOr(s.QuietStartup, false) }
func (s Settings) GetCollapseChangelog() bool    { return boolOr(s.CollapseChangelog, false) }

// GetBashCompaction reports whether bash output is compacted. Default: true.
func (s Settings) GetBashCompaction() bool { return boolOr(s.BashCompaction, true) }

// GetCleanupTempFiles reports whether sessions' temp files are cleaned up.
func (s Settings) GetCleanupTempFiles() bool { return boolOr(s.CleanupTempFiles, true) }

// WarCouncilSettings are the models the war council leaves out, so a model
// set up later joins it, and how long a member may take.
type WarCouncilSettings struct {
	Excluded       []string `json:"excluded,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
	// BuildTimeoutSeconds bounds a member building a candidate change.
	BuildTimeoutSeconds int `json:"buildTimeoutSeconds,omitempty"`
}

// DefaultWarCouncilTimeout is how long a council member may take by default.
const DefaultWarCouncilTimeout = 5 * time.Minute

// DefaultWarCouncilBuildTimeout is how long a member may take to build a
// candidate by default.
const DefaultWarCouncilBuildTimeout = 15 * time.Minute

// GetWarCouncilBuildTimeout returns how long a member may take to build a
// candidate.
func (s Settings) GetWarCouncilBuildTimeout() time.Duration {
	if c := s.WarCouncil; c != nil && c.BuildTimeoutSeconds > 0 {
		return time.Duration(c.BuildTimeoutSeconds) * time.Second
	}
	return DefaultWarCouncilBuildTimeout
}

// GetWarCouncil returns the council settings with the default time limit.
func (s Settings) GetWarCouncil() (excluded []string, timeout time.Duration) {
	timeout = DefaultWarCouncilTimeout
	if c := s.WarCouncil; c != nil {
		excluded = c.Excluded
		if c.TimeoutSeconds > 0 {
			timeout = time.Duration(c.TimeoutSeconds) * time.Second
		}
	}
	return excluded, timeout
}

// GetEnableSkillCommands reports whether /skill:name commands autocomplete.
func (s Settings) GetEnableSkillCommands() bool { return boolOr(s.EnableSkillCommands, true) }

// GetDiagnostics reports whether post-edit diagnostics run, their timeout,
// and the per-language command overrides.
func (s Settings) GetDiagnostics() (bool, time.Duration, map[string]string) {
	d := s.Diagnostics
	if d == nil {
		return true, tools.DefaultDiagnosticsTimeout, nil
	}
	timeout := tools.DefaultDiagnosticsTimeout
	if d.TimeoutMs > 0 {
		timeout = time.Duration(d.TimeoutMs) * time.Millisecond
	}
	return boolOr(d.Enabled, true), timeout, maps.Clone(d.Commands)
}

// GetFullscreenExitOutput returns "clear" (the default), "transcript", or
// "resume-hint".
func (s Settings) GetFullscreenExitOutput() string {
	switch s.FullscreenExitOutput {
	case "transcript", "resume-hint":
		return s.FullscreenExitOutput
	}
	return "clear"
}

func (s Settings) GetFullscreenCopyOnSelect() bool { return boolOr(s.FullscreenCopyOnSelect, true) }

// GetFullscreenScrollbar returns "always", "hidden", or "auto" (the default).
// GetFullscreenScrollSpeed returns the lines one wheel step scrolls.
func (s Settings) GetFullscreenScrollSpeed() int {
	if s.FullscreenScrollSpeed == nil {
		return 3
	}
	return max(1, min(10, *s.FullscreenScrollSpeed))
}

func (s Settings) GetFullscreenScrollbar() string {
	if mode := s.FullscreenScrollbar; mode == "always" || mode == "hidden" {
		return mode
	}
	return "auto"
}

// GetFullscreenSidebar returns "show", "hide", or "" (auto, the default).
func (s Settings) GetFullscreenSidebar() string {
	switch mode := s.FullscreenSidebar; mode {
	case sidebarShow, sidebarHide:
		return mode
	}
	return sidebarAuto
}

// GetMermaidRenderingMode returns "off", "final", or "streaming" (the
// default).
func (s Settings) GetMermaidRenderingMode() string {
	if s.Markdown != nil && (s.Markdown.Mermaid == "off" || s.Markdown.Mermaid == "final") {
		return s.Markdown.Mermaid
	}
	return "streaming"
}

// GetDoubleEscapeAction returns the double-Esc action. Default: "tree".
func (s Settings) GetDoubleEscapeAction() string { return cmp.Or(s.DoubleEscapeAction, "tree") }

// GetSteeringMode and GetFollowUpMode return the queue dispatch modes.
// Default: "one-at-a-time".
func (s Settings) GetSteeringMode() string { return cmp.Or(s.SteeringMode, "one-at-a-time") }
func (s Settings) GetFollowUpMode() string { return cmp.Or(s.FollowUpMode, "one-at-a-time") }

// GetTerminalCapabilityOverrides maps the terminal settings onto capability
// overrides: terminal.images "kitty" or "iterm2" selects that protocol and
// false disables images; a boolean terminal.trueColor or terminal.hyperlinks
// overrides detection. "auto" and any other value leave detection alone.
func (s Settings) GetTerminalCapabilityOverrides() tui.CapabilityOverrides {
	var overrides tui.CapabilityOverrides
	if s.Terminal == nil {
		return overrides
	}
	switch images := decodeSettingJSON(s.Terminal.Images).(type) {
	case string:
		if images == string(tui.ImageProtocolKitty) || images == string(tui.ImageProtocolITerm2) {
			protocol := tui.ImageProtocol(images)
			overrides.Images = &protocol
		}
	case bool:
		if !images {
			none := tui.ImageProtocol("")
			overrides.Images = &none
		}
	}
	if trueColor, ok := decodeSettingJSON(s.Terminal.TrueColor).(bool); ok {
		overrides.TrueColor = &trueColor
	}
	if hyperlinks, ok := decodeSettingJSON(s.Terminal.Hyperlinks).(bool); ok {
		overrides.Hyperlinks = &hyperlinks
	}
	return overrides
}

// decodeSettingJSON decodes one authored settings value, or returns nil when
// it is absent or malformed.
func decodeSettingJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return value
}

// GetHttpIdleTimeoutMs returns the HTTP header/body idle timeout in
// milliseconds. Default: 300000 (5 minutes). Zero disables the timeout. An
// unparseable value is an error.
func (s Settings) GetHttpIdleTimeoutMs() (int, error) {
	if len(s.HTTPIdleTimeoutMs) == 0 {
		return defaultHTTPIdleTimeoutMs, nil
	}
	value := decodeSettingJSON(s.HTTPIdleTimeoutMs)
	if timeoutMs, ok := parseHTTPIdleTimeoutMs(value); ok {
		return timeoutMs, nil
	}
	return 0, fmt.Errorf("Invalid httpIdleTimeoutMs setting: %v", value)
}

func parseHTTPIdleTimeoutMs(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return 0, false
		}
		return int(math.Floor(v)), true
	case string:
		trimmed := strings.TrimSpace(v)
		if strings.EqualFold(trimmed, "disabled") {
			return 0, true
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if trimmed == "" || err != nil {
			return 0, false
		}
		return parseHTTPIdleTimeoutMs(parsed)
	}
	return 0, false
}

// CompactionConfig is the resolved compaction settings (compaction imports
// codingagent, so its own type cannot be used here). Zero token settings
// scale with the model's window (compaction.CompactionSettings.ForModel).
type CompactionConfig struct {
	Enabled          bool
	ReserveTokens    int
	KeepRecentTokens int
}

var defaultCompactionConfig = CompactionConfig{Enabled: true}

// BranchSummaryConfig is the resolved branch summary settings.
type BranchSummaryConfig struct {
	ReserveTokens int
	SkipPrompt    bool
}

// RetryConfig holds resolved retry settings with defaults applied.
type RetryConfig struct {
	Enabled     bool
	MaxRetries  int
	BaseDelayMs int
	MaxDelayMs  int
}

// ProviderRetryConfig holds resolved provider/SDK retry settings.
type ProviderRetryConfig struct {
	TimeoutMs       int
	MaxRetries      int
	MaxRetryDelayMs int
}

func setInt(dst *int, src *int) {
	if src != nil {
		*dst = *src
	}
}

// GetModelCompactionSettings returns compaction settings for one model. Each
// token setting resolves through compaction.modelOverrides["provider/modelID"],
// then the ordinary setting, then the built-in default. Empty provider and
// modelID select no override.
func (s Settings) GetModelCompactionSettings(provider, modelID string) CompactionConfig {
	result := defaultCompactionConfig
	c := s.Compaction
	if c == nil {
		return result
	}
	result.Enabled = boolOr(c.Enabled, true)
	setInt(&result.ReserveTokens, c.ReserveTokens)
	setInt(&result.KeepRecentTokens, c.KeepRecentTokens)
	if provider == "" && modelID == "" {
		return result
	}
	if override, ok := c.ModelOverrides[provider+"/"+modelID]; ok {
		setInt(&result.ReserveTokens, override.ReserveTokens)
		setInt(&result.KeepRecentTokens, override.KeepRecentTokens)
	}
	return result
}

// GetBranchSummarySettings returns branch summary settings with defaults.
func (s Settings) GetBranchSummarySettings() BranchSummaryConfig {
	result := BranchSummaryConfig{ReserveTokens: 16384}
	if b := s.BranchSummary; b != nil {
		setInt(&result.ReserveTokens, b.ReserveTokens)
		result.SkipPrompt = boolOr(b.SkipPrompt, false)
	}
	return result
}

// GetRetrySettings returns retry settings with defaults applied.
func (s Settings) GetRetrySettings() RetryConfig {
	result := RetryConfig{Enabled: true, MaxRetries: 3, BaseDelayMs: 2000, MaxDelayMs: 60000}
	if r := s.Retry; r != nil {
		result.Enabled = boolOr(r.Enabled, true)
		setInt(&result.MaxRetries, r.MaxRetries)
		setInt(&result.BaseDelayMs, r.BaseDelayMs)
		setInt(&result.MaxDelayMs, r.MaxAgentDelayMs)
	}
	return result
}

// GetProviderRetrySettings returns provider retry settings with defaults
// applied. timeoutMs maps onto the net/http idle deadline
// (GetProviderRequestTimeoutMs); maxRetries and maxRetryDelayMs drive the
// provider retry transport (ai.ConfigureProviderRetry).
func (s Settings) GetProviderRetrySettings() ProviderRetryConfig {
	result := ProviderRetryConfig{MaxRetryDelayMs: 60000}
	if s.Retry != nil && s.Retry.Provider != nil {
		p := s.Retry.Provider
		setInt(&result.TimeoutMs, p.TimeoutMs)
		setInt(&result.MaxRetries, p.MaxRetries)
		setInt(&result.MaxRetryDelayMs, p.MaxRetryDelayMs)
	}
	return result
}

// GetProviderRequestTimeoutMs returns the per-request stream/read timeout:
// retry.provider.timeoutMs when set, otherwise httpIdleTimeoutMs.
func (s Settings) GetProviderRequestTimeoutMs() (int, error) {
	if s.Retry != nil && s.Retry.Provider != nil && s.Retry.Provider.TimeoutMs != nil {
		return *s.Retry.Provider.TimeoutMs, nil
	}
	return s.GetHttpIdleTimeoutMs()
}

// GetHashline returns "on", "off", or "auto" (the default, also used for an
// unknown value).
func (s Settings) GetHashline() string {
	if s.Hashline == "on" || s.Hashline == "off" {
		return s.Hashline
	}
	return "auto"
}

// GetModelThinkingLevel returns the per-model default thinking level, or "".
func (s Settings) GetModelThinkingLevel(provider, modelID string) string {
	return s.ModelThinkingLevels[provider+"/"+modelID]
}

// ─── SettingsManager ──────────────────────────────────────────────────────────

// SettingsManager loads and merges settings: global, then project, then
// this run's overrides.
type SettingsManager struct {
	// mu guards the settings layers: a cache-warming refresh reads the mode
	// from its own goroutine while /settings writes it.
	mu             sync.RWMutex
	global         Settings
	project        Settings
	overrides      Settings
	merged         Settings
	agentDir       string
	cwd            string
	projectTrusted bool
	globalLoadErr  error
	projectLoadErr error
	errors         []SettingsError
}

// SettingsError is a settings load or write diagnostic.
type SettingsError struct {
	Scope string
	// Path is the settings file behind the error.
	Path  string
	Error error
}

// NewSettingsManager creates a SettingsManager for the given directories.
func NewSettingsManager(cwd, agentDir string) *SettingsManager {
	return NewSettingsManagerWithProjectTrust(cwd, agentDir, true)
}

// NewSettingsManagerWithProjectTrust creates a settings manager that reads
// project settings only when projectTrusted is true.
func NewSettingsManagerWithProjectTrust(cwd, agentDir string, projectTrusted bool) *SettingsManager {
	sm := &SettingsManager{cwd: cwd, agentDir: agentDir, projectTrusted: projectTrusted}
	sm.Load()
	return sm
}

// DefaultAgentDir returns the default <ConfigRoot>/agent directory.
func DefaultAgentDir() string {
	return filepath.Join(ConfigRoot(), "agent")
}

// AgentDir returns the global settings directory backing this manager.
func (sm *SettingsManager) AgentDir() string { return sm.agentDir }

// CWD returns the project directory backing this manager.
func (sm *SettingsManager) CWD() string { return sm.cwd }

func (sm *SettingsManager) globalPath() string {
	return filepath.Join(sm.agentDir, "settings.json")
}

func (sm *SettingsManager) projectPath() string {
	return filepath.Join(sm.cwd, CONFIG_DIR_NAME, "settings.json")
}

// Load reads settings from disk. Errors recorded earlier stay until
// DrainErrors.
func (sm *SettingsManager) Load() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.globalLoadErr = sm.loadLayer(&sm.global, "global", sm.globalPath())
	sm.project, sm.projectLoadErr = Settings{}, nil
	if sm.projectTrusted {
		sm.projectLoadErr = sm.loadLayer(&sm.project, "project", sm.projectPath())
	}
	sm.remergeLocked()
}

// loadLayer reads one settings file into layer. A missing file empties the
// layer; a file that fails to load keeps the layer as it was and records the
// error.
func (sm *SettingsManager) loadLayer(layer *Settings, scope, path string) error {
	s, err := loadSettingsFile(path)
	switch {
	case err == nil:
		*layer = s
	case errors.Is(err, os.ErrNotExist):
		*layer = Settings{}
		return nil
	default:
		sm.errors = append(sm.errors, SettingsError{Scope: scope, Path: path, Error: err})
	}
	return err
}

func (sm *SettingsManager) remergeLocked() {
	sm.merged = mergeSettings(mergeSettings(sm.global, sm.project), sm.overrides)
}

// Get returns the merged settings. The manager replaces its merged value
// rather than changing it, so the result stays valid; callers change
// settings through UpdateGlobal, never through what its fields point to.
func (sm *SettingsManager) Get() Settings {
	if sm == nil {
		return Settings{}
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.merged
}

// Reload re-reads settings from disk.
func (sm *SettingsManager) Reload() { sm.Load() }

// DrainErrors returns accumulated load/write diagnostics and clears them.
func (sm *SettingsManager) DrainErrors() []SettingsError {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	out := sm.errors
	sm.errors = nil
	return out
}

// GetGlobalSettings returns the persisted global settings layer.
func (sm *SettingsManager) GetGlobalSettings() Settings {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.global.clone()
}

// GetProjectSettings returns the persisted project settings layer.
func (sm *SettingsManager) GetProjectSettings() Settings {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.project.clone()
}

// IsProjectTrusted reports whether project settings are readable and writable.
func (sm *SettingsManager) IsProjectTrusted() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.projectTrusted
}

// SetProjectTrusted changes project-settings access and reloads both layers.
func (sm *SettingsManager) SetProjectTrusted(trusted bool) {
	sm.mu.Lock()
	sm.projectTrusted = trusted
	sm.mu.Unlock()
	sm.Load()
}

// ApplyOverrides lays run-only settings (command-line flags) over the merged
// settings. They are never saved.
func (sm *SettingsManager) ApplyOverrides(overrides Settings) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.overrides = overrides.clone()
	sm.remergeLocked()
}

// UpdateGlobal applies fn to the global settings, saves the change to
// <agentDir>/settings.json, and refreshes the merged view.
func (sm *SettingsManager) UpdateGlobal(fn func(*Settings)) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.globalLoadErr != nil {
		return fmt.Errorf("global settings file has parse errors: %w", sm.globalLoadErr)
	}
	return sm.updateLocked(&sm.global, "global", sm.globalPath(), fn)
}

// UpdateProject applies fn to the project settings, saves the change to
// <cwd>/.wopr/settings.json, and refreshes the merged view.
func (sm *SettingsManager) UpdateProject(fn func(*Settings)) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if !sm.projectTrusted {
		return errors.New("Project is not trusted; refusing to write project settings")
	}
	if sm.projectLoadErr != nil {
		return fmt.Errorf("project settings file has parse errors: %w", sm.projectLoadErr)
	}
	return sm.updateLocked(&sm.project, "project", sm.projectPath(), fn)
}

func (sm *SettingsManager) updateLocked(layer *Settings, scope, path string, fn func(*Settings)) error {
	before := layer.clone()
	after := layer.clone()
	fn(&after)
	if err := saveSettingsPatch(path, before, after); err != nil {
		sm.errors = append(sm.errors, SettingsError{Scope: scope, Path: path, Error: err})
		return err
	}
	*layer = after.clone()
	sm.remergeLocked()
	return nil
}

// GetCompactionSettings returns compaction settings without a model
// override.
func (sm *SettingsManager) GetCompactionSettings() CompactionConfig {
	return sm.Get().GetModelCompactionSettings("", "")
}

func (sm *SettingsManager) GetModelCompactionSettings(provider, modelID string) CompactionConfig {
	return sm.Get().GetModelCompactionSettings(provider, modelID)
}

func (sm *SettingsManager) GetBranchSummarySettings() BranchSummaryConfig {
	return sm.Get().GetBranchSummarySettings()
}

func (sm *SettingsManager) GetRetrySettings() RetryConfig { return sm.Get().GetRetrySettings() }

func (sm *SettingsManager) GetProviderRetrySettings() ProviderRetryConfig {
	return sm.Get().GetProviderRetrySettings()
}

func (sm *SettingsManager) GetProviderRequestTimeoutMs() (int, error) {
	return sm.Get().GetProviderRequestTimeoutMs()
}

func (sm *SettingsManager) GetHttpIdleTimeoutMs() (int, error) {
	return sm.Get().GetHttpIdleTimeoutMs()
}

func (sm *SettingsManager) GetSteeringMode() string { return sm.Get().GetSteeringMode() }
func (sm *SettingsManager) GetFollowUpMode() string { return sm.Get().GetFollowUpMode() }
func (sm *SettingsManager) GetHashline() string     { return sm.Get().GetHashline() }
func (sm *SettingsManager) GetBlockImages() bool    { return sm.Get().GetBlockImages() }

func (sm *SettingsManager) GetImageAutoResize() bool { return sm.Get().GetImageAutoResize() }

func (sm *SettingsManager) GetDefaultThinkingLevel() string {
	return sm.Get().DefaultThinkingLevel
}

func (sm *SettingsManager) GetModelThinkingLevel(provider, modelID string) string {
	return sm.Get().GetModelThinkingLevel(provider, modelID)
}

func (sm *SettingsManager) GetThinkingBudgets() *ThinkingBudgetsSettings {
	return sm.Get().ThinkingBudgets
}

// GetEnableAttributionHeaders returns whether provider requests carry wopr's
// app-attribution headers. Default: true.
func (sm *SettingsManager) GetEnableAttributionHeaders() bool {
	return boolOr(sm.Get().EnableAttributionHeaders, true)
}

// GetDefaultProjectTrust returns the fallback project-trust mode from global
// settings only: "ask" (the default), "always", or "never".
func (sm *SettingsManager) GetDefaultProjectTrust() string {
	if v := sm.GetGlobalSettings().DefaultProjectTrust; v == "always" || v == "never" {
		return v
	}
	return "ask"
}

// GetCacheWarmingMode returns the global cache-warming mode, or "streaming"
// when it is unset or invalid. Project settings are ignored because each
// refresh costs money.
func (sm *SettingsManager) GetCacheWarmingMode() CacheWarmingMode {
	return sm.GetGlobalSettings().GetCacheWarmingMode()
}

// GetSessionDir returns the configured session directory, expanding ~ forms
// and converting a file:// URL. An invalid file URL is an error.
func (sm *SettingsManager) GetSessionDir() (string, error) {
	return normalizeSettingsPath(sm.Get().SessionDir)
}

// SetCacheWarmingMode saves the cache-warming mode to global settings.
func (sm *SettingsManager) SetCacheWarmingMode(mode CacheWarmingMode) error {
	return sm.UpdateGlobal(func(s *Settings) { s.CacheWarming = mode })
}

// SetLastChangelogVersion records that the user has seen changelog entries
// up to and including version.
func (sm *SettingsManager) SetLastChangelogVersion(version string) error {
	return sm.UpdateGlobal(func(s *Settings) { s.LastChangelogVersion = version })
}

func (sm *SettingsManager) SetModelThinkingLevel(provider, modelID, level string) error {
	return sm.UpdateGlobal(func(s *Settings) {
		if s.ModelThinkingLevels == nil {
			s.ModelThinkingLevels = map[string]string{}
		}
		s.ModelThinkingLevels[provider+"/"+modelID] = level
	})
}

func (sm *SettingsManager) RemoveModelThinkingLevel(provider, modelID string) error {
	return sm.UpdateGlobal(func(s *Settings) { delete(s.ModelThinkingLevels, provider+"/"+modelID) })
}

func (sm *SettingsManager) SetDefaultModelAndProvider(provider, model string) error {
	return sm.UpdateGlobal(func(s *Settings) {
		s.DefaultProvider = provider
		s.DefaultModel = model
	})
}

func (sm *SettingsManager) SetDefaultThinkingLevel(level string) error {
	return sm.UpdateGlobal(func(s *Settings) { s.DefaultThinkingLevel = level })
}

func (sm *SettingsManager) SetSteeringMode(mode string) error {
	return sm.UpdateGlobal(func(s *Settings) { s.SteeringMode = mode })
}

func (sm *SettingsManager) SetFollowUpMode(mode string) error {
	return sm.UpdateGlobal(func(s *Settings) { s.FollowUpMode = mode })
}

func (sm *SettingsManager) SetTheme(theme string) error {
	return sm.UpdateGlobal(func(s *Settings) { s.Theme = theme })
}

func (sm *SettingsManager) SetSkillPaths(paths []string) error {
	return sm.UpdateGlobal(func(s *Settings) { s.Skills = paths })
}

func (sm *SettingsManager) SetProjectSkillPaths(paths []string) error {
	return sm.UpdateProject(func(s *Settings) { s.Skills = paths })
}

func (sm *SettingsManager) SetPromptTemplatePaths(paths []string) error {
	return sm.UpdateGlobal(func(s *Settings) { s.Prompts = paths })
}

func (sm *SettingsManager) SetProjectPromptTemplatePaths(paths []string) error {
	return sm.UpdateProject(func(s *Settings) { s.Prompts = paths })
}

func (sm *SettingsManager) SetThemePaths(paths []string) error {
	return sm.UpdateGlobal(func(s *Settings) { s.Themes = paths })
}

func (sm *SettingsManager) SetProjectThemePaths(paths []string) error {
	return sm.UpdateProject(func(s *Settings) { s.Themes = paths })
}

// ─── Files ──────────────────────────────────────────────────────────────────

// normalizeSettingsPath expands a leading "~" or "~/" to the home directory
// and converts a file:// URL to its path, returning an error for an invalid
// URL. An empty path stays empty.
func normalizeSettingsPath(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	windows := runtime.GOOS == "windows"
	if windows {
		path = tools.NormalizeWindowsShellPath(path)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if path == "~" {
			return home, nil
		}
		if after, ok := strings.CutPrefix(path, "~/"); ok {
			return filepath.Join(home, after), nil
		}
		if after, ok := strings.CutPrefix(path, `~\`); ok && windows {
			return filepath.Join(home, after), nil
		}
	}
	if strings.HasPrefix(path, "file://") {
		return fileURLToPath(path, windows)
	}
	return path, nil
}

// fileURLToPath converts a file:// URL to a local path. On Windows a host
// names a UNC server and "/C:/x" becomes "C:\x"; elsewhere the host must be
// empty or "localhost".
func fileURLToPath(raw string, windows bool) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid file URL %q: %w", raw, err)
	}
	host := u.Host
	if host == "localhost" {
		host = ""
	}
	if !windows {
		if host != "" {
			return "", fmt.Errorf("file URL host must be \"localhost\" or empty: %s", raw)
		}
		return u.Path, nil
	}
	p := filepath.FromSlash(u.Path)
	if host != "" {
		return `\\` + host + p, nil
	}
	if len(u.Path) >= 3 && u.Path[0] == '/' && u.Path[2] == ':' {
		return p[1:], nil
	}
	return "", fmt.Errorf("file URL path must be absolute: %s", raw)
}

func loadSettingsFile(path string) (Settings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Settings{}, err
	}
	var s Settings
	if err := json.Unmarshal(text.StripBomBytes(data), &s); err != nil {
		return Settings{}, err
	}
	return s, nil
}

// saveSettingsPatch writes the keys that changed between before and after
// into the file as it is now, so other writers' changes survive.
func saveSettingsPatch(path string, before, after Settings) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	lock := flock.New(path + ".lock")
	locked, err := acquireSyncLockWithRetry(lock)
	if err != nil {
		return fmt.Errorf("acquire settings lock: %w", err)
	}
	if !locked {
		return errors.New("failed to acquire settings lock")
	}
	defer func() {
		if unlockErr := lock.Unlock(); unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("release settings lock: %w", unlockErr))
		}
	}()

	current := map[string]json.RawMessage{}
	data, readErr := os.ReadFile(path)
	if readErr == nil {
		if err := json.Unmarshal(text.StripBomBytes(data), &current); err != nil {
			return fmt.Errorf("parse current settings: %w", err)
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	beforeMap, err := settingsJSONMap(before)
	if err != nil {
		return err
	}
	afterMap, err := settingsJSONMap(after)
	if err != nil {
		return err
	}
	if err := patchJSONObject(current, beforeMap, afterMap); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o644)
}

func settingsJSONMap(settings Settings) (map[string]json.RawMessage, error) {
	encoded, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	result := map[string]json.RawMessage{}
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func patchJSONObject(current, before, after map[string]json.RawMessage) error {
	keys := make(map[string]struct{}, len(after))
	for key := range before {
		keys[key] = struct{}{}
	}
	for key := range after {
		keys[key] = struct{}{}
	}
	for key := range keys {
		oldValue, hadOld := before[key]
		newValue, hasNew := after[key]
		if hadOld == hasNew && bytes.Equal(oldValue, newValue) {
			continue
		}
		if !hasNew {
			delete(current, key)
			continue
		}
		newObject, newIsObject := rawJSONObject(newValue)
		if !newIsObject {
			current[key] = slices.Clone(newValue)
			continue
		}
		oldObject, _ := rawJSONObject(oldValue)
		currentObject, _ := rawJSONObject(current[key])
		if currentObject == nil {
			currentObject = map[string]json.RawMessage{}
		}
		if err := patchJSONObject(currentObject, oldObject, newObject); err != nil {
			return err
		}
		encoded, err := json.Marshal(currentObject)
		if err != nil {
			return err
		}
		current[key] = encoded
	}
	return nil
}

func rawJSONObject(value json.RawMessage) (map[string]json.RawMessage, bool) {
	value = bytes.TrimSpace(value)
	if len(value) == 0 || value[0] != '{' {
		return map[string]json.RawMessage{}, false
	}
	result := map[string]json.RawMessage{}
	if err := json.Unmarshal(value, &result); err != nil {
		return map[string]json.RawMessage{}, false
	}
	return result, true
}
