package serve

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ─── DTOs ────────────────────────────────────────────────────────────────

type sessionDTO struct {
	ID        string         `json:"id"`
	Slug      string         `json:"slug"`
	ProjectID string         `json:"projectID"`
	Directory string         `json:"directory"`
	Path      string         `json:"path"`
	Title     string         `json:"title"`
	Agent     string         `json:"agent"`
	Model     sessionModel   `json:"model"`
	Version   string         `json:"version"`
	Summary   sessionSummary `json:"summary"`
	Cost      float64        `json:"cost"`
	Tokens    sessionTokens  `json:"tokens"`
	Time      sessionTime    `json:"time"`
	Project   projectDTO     `json:"project"`
}

// sessionModel is the session's {id,providerID,variant} object. It is never
// null: an unknown model marshals as empty strings, which the client can decode.
type sessionModel struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant"`
}

type sessionSummary struct {
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
	Files     int `json:"files"`
}

type sessionTime struct {
	Created  int64  `json:"created"`
	Updated  int64  `json:"updated"`
	Archived *int64 `json:"archived,omitempty"`
}

type sessionTokens struct {
	Input     int        `json:"input"`
	Output    int        `json:"output"`
	Reasoning int        `json:"reasoning"`
	Total     int        `json:"total"`
	Cache     tokenCache `json:"cache"`
}

type tokenCache struct {
	Read  int `json:"read"`
	Write int `json:"write"`
}

// messageDTO is opencode's MessageWithParts: the four required Message fields
// under info, and the parts beside it. The flat id/sessionID/role/time half is
// gone because the client's decoder reads them from info.
type messageDTO struct {
	Info  messageInfoDTO `json:"info"`
	Parts []partDTO      `json:"parts"`
}

type messageInfoDTO struct {
	ID         string        `json:"id"`
	SessionID  string        `json:"sessionID"`
	Role       string        `json:"role"`
	ParentID   string        `json:"parentID"`
	Mode       string        `json:"mode"`
	Agent      string        `json:"agent"`
	Path       messagePath   `json:"path"`
	Cost       float64       `json:"cost"`
	Tokens     messageTokens `json:"tokens"`
	ModelID    string        `json:"modelID"`
	ProviderID string        `json:"providerID"`
	Time       messageTime   `json:"time"`
	Finish     string        `json:"finish"`
	// System is the per-prompt system text the client attached, echoed only on
	// user messages. omitempty keeps it off every message that has none, so the
	// persisted transcript shape is unchanged for non-serve turns.
	System string `json:"system,omitempty"`
	// omitSummary is a stream-phase flag, never wire data. When set, the user
	// branch of MarshalJSON omits the summary key, which is how the stream's
	// start frame differs from its settle frame. The zero value false leaves
	// every read-path caller emitting summary, so entriesToMessages and the
	// persisted transcript shape are unchanged.
	omitSummary bool
}

// messageModel is the nested model object opencode's user Message carries.
type messageModel struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}

// messageSummary is the user Message's file-diff summary. wopr records no
// per-message diffs, so Diffs is always the empty array the client requires,
// never null.
type messageSummary struct {
	Diffs []json.RawMessage `json:"diffs"`
}

// MarshalJSON emits opencode's user Message shape for role "user" and the flat
// assistant shape for every other role. The shapes are structurally different:
// the client reads userMessage.model.providerID, which the flat shape cannot
// supply. The receiver is a value receiver because the SSE bus and
// TestMessageDTODecode marshal non-addressable messageInfoDTO values, where a
// pointer-receiver MarshalJSON would be skipped.
func (m messageInfoDTO) MarshalJSON() ([]byte, error) {
	if m.Role == "user" {
		// A non-nil pointer marshals summary:{"diffs":[]}; a nil pointer is
		// omitted by omitempty. The start frame sets omitSummary, the settle
		// frame and every read-path message do not.
		var summary *messageSummary
		if !m.omitSummary {
			summary = &messageSummary{Diffs: []json.RawMessage{}}
		}
		return json.Marshal(struct {
			ID        string          `json:"id"`
			SessionID string          `json:"sessionID"`
			Role      string          `json:"role"`
			Time      messageTime     `json:"time"`
			Summary   *messageSummary `json:"summary,omitempty"`
			Agent     string          `json:"agent"`
			Model     messageModel    `json:"model"`
			System    string          `json:"system,omitempty"`
		}{
			ID:        m.ID,
			SessionID: m.SessionID,
			Role:      m.Role,
			Time:      m.Time,
			Summary:   summary,
			Agent:     m.Agent,
			Model:     messageModel{ProviderID: m.ProviderID, ModelID: m.ModelID},
			System:    m.System,
		})
	}
	type plain messageInfoDTO
	return json.Marshal(plain(m))
}

// messagePath is the message's {cwd,root} object. cwd is the session's own
// working directory, or the resolved server root when the session has none.
type messagePath struct {
	CWD  string `json:"cwd"`
	Root string `json:"root"`
}

type messageTokens struct {
	Total     int        `json:"total"`
	Input     int        `json:"input"`
	Output    int        `json:"output"`
	Reasoning int        `json:"reasoning"`
	Cache     tokenCache `json:"cache"`
}

type messageTime struct {
	Created int64 `json:"created"`
	// Completed is set only on the final assistant message.updated of a turn.
	// It is a pointer so the placeholder frame and every read-path message omit
	// it, keeping the persisted transcript shape unchanged.
	Completed *int64 `json:"completed,omitempty"`
}

// partTime is a part's {start,end} clock. End is omitted while the part is
// still open, so the create frame carries time:{start} and the final frame
// time:{start,end}, matching opencode.
type partTime struct {
	Start int64 `json:"start"`
	End   int64 `json:"end,omitempty"`
}

type partDTO struct {
	ID        string `json:"id"`
	MessageID string `json:"messageID"`
	SessionID string `json:"sessionID"`
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Tool      string `json:"tool,omitempty"`
	CallID    string `json:"callID,omitempty"`
	Mime      string `json:"mime,omitempty"`
	URL       string `json:"url,omitempty"`
	// Time is set only on streamed text/reasoning parts. It is nil for every
	// part built by entriesToMessages, so the POST/GET message bodies are
	// unchanged, and for tool parts, whose clock lives under state.
	Time  *partTime       `json:"time,omitempty"`
	State json.RawMessage `json:"state,omitempty"`
	// Snapshot follows fileNodeDTO.Ignored: wopr has no snapshot, so it is
	// always "", present so a strict client decoding the part finds it.
	Snapshot string `json:"snapshot"`
}

type sessionStatusDTO struct {
	SessionID string `json:"sessionID"`
	Type      string `json:"type"`
}

// agentInfoDTO is the GET /agent item. The null scalars are pointer fields left
// nil on purpose: opencode omits no key here, and a null scalar is safe for the
// client to Object.keys. model is deliberately absent (see the spec §5).
type agentInfoDTO struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Mode        string         `json:"mode"`
	Native      bool           `json:"native"`
	Hidden      *string        `json:"hidden"`
	TopP        *float64       `json:"topP"`
	Temperature *float64       `json:"temperature"`
	Color       *string        `json:"color"`
	Permission  []any          `json:"permission"`
	Variant     *string        `json:"variant"`
	Prompt      *string        `json:"prompt"`
	Options     map[string]any `json:"options"`
	Steps       *int           `json:"steps"`
}

type projectDTO struct {
	ID       string `json:"id"`
	Worktree string `json:"worktree"`
	// opencode emits time and sandboxes on every project; a client that
	// Object.keys/entries either one throws on absence, so they are always
	// present. wopr tracks no project metadata, so time is the worktree's
	// mtime and sandboxes is empty.
	Time      projectTime `json:"time"`
	Sandboxes []string    `json:"sandboxes"`
	VCS       string      `json:"vcs,omitempty"`
}

type projectTime struct {
	Created int64 `json:"created"`
	Updated int64 `json:"updated"`
}

type fileNodeDTO struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Absolute is the entry's absolute path; opencode emits it and clients
	// (KKCode) call string methods on it, so its absence is fatal, not cosmetic.
	Absolute string `json:"absolute"`
	Type     string `json:"type"`
	// Ignored mirrors opencode's field; wopr has no ignore rules, so it is
	// always false. Present so a strict client decoding the object finds it.
	Ignored bool `json:"ignored"`
}

type fileStatusDTO struct {
	Path string `json:"path"`
}

// providerModelDTO is the full captured model object. wopr fills
// id/providerID/name and leaves the rest an empty composite or zero scalar, so
// no nested obj is undefined for a client that decodes it.
type providerModelDTO struct {
	ID           string         `json:"id"`
	ProviderID   string         `json:"providerID"`
	API          map[string]any `json:"api"`
	Name         string         `json:"name"`
	Family       string         `json:"family"`
	Capabilities map[string]any `json:"capabilities"`
	Cost         map[string]any `json:"cost"`
	Limit        map[string]any `json:"limit"`
	Status       string         `json:"status"`
	Options      map[string]any `json:"options"`
	Headers      map[string]any `json:"headers"`
	ReleaseDate  string         `json:"release_date"`
	Variants     map[string]any `json:"variants"`
}

type providerDTO struct {
	ID      string                      `json:"id"`
	Name    string                      `json:"name"`
	Source  string                      `json:"source"`
	Env     []string                    `json:"env"`
	Key     string                      `json:"key"`
	Options map[string]any              `json:"options"`
	Models  map[string]providerModelDTO `json:"models"`
}

// providersResponseDTO is the GET /config/providers body. Providers is
// allocated non-nil by the handler so it marshals [] and never null.
type providersResponseDTO struct {
	Providers []providerDTO     `json:"providers"`
	Default   map[string]string `json:"default"`
}

type errorDTO struct {
	Error string `json:"error"`
}

// configDTO is opencode's GET /config object. Every obj/arr field is
// initialized to an empty composite by the handler so it marshals {} or [],
// never null. $schema/username/subagent_depth stay zero values.
type configDTO struct {
	Schema        string         `json:"$schema"`
	Command       map[string]any `json:"command"`
	Plugin        []any          `json:"plugin"`
	Model         string         `json:"model"`
	SubagentDepth int            `json:"subagent_depth"`
	Username      string         `json:"username"`
	Mode          map[string]any `json:"mode"`
	Agent         map[string]any `json:"agent"`
	Provider      map[string]any `json:"provider"`
	MCP           map[string]any `json:"mcp"`
	Instructions  []any          `json:"instructions"`
	Permission    map[string]any `json:"permission"`
}

// apiCommandDTO is the GET /api/command body. Data is initialized to []any{} in
// the handler so it marshals [], never null; there is no element type because
// built-in slash commands are out of contract.
type apiCommandDTO struct {
	Location commandLocationDTO `json:"location"`
	Data     []any              `json:"data"`
}

type commandLocationDTO struct {
	Directory string                    `json:"directory"`
	Project   commandLocationProjectDTO `json:"project"`
}

type commandLocationProjectDTO struct {
	ID        string `json:"id"`
	Directory string `json:"directory"`
}

// ─── Helpers ──────────────────────────────────────────────────────────────

const (
	tailBlockBytes       = 64 << 10
	maxTailBytes         = 16 << 20
	metaProbeBytes       = 32 << 10
	titleProbeBytes      = 8 << 10
	maxBodyBytes         = 16 << 20
	maxFileContentBytes  = 8 << 20
	maxFileEntries       = 500
	maxWalkEntries       = 20000
	maxFileStatusEntries = 500
	maxListFanOut        = 100 // cap concurrent goroutines for listSessions (MAJOR 4)
)

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// partID is the single source of part identity. The SSE mapper and
// entriesToMessages both call it, so a streamed part ID and a transcript part
// ID cannot drift.
//
// index is the position in the message's full content slice, enumerated before
// any block is filtered out. A block that yields no part still consumes its
// index, so the transcript's index equals ai.*Event.ContentIndex even where
// entriesToMessages has no case (ai.ServerToolContent) and the stream does.
// callID is the tool form's identity and is ignored by the other kinds.
func partID(messageID, kind string, index int, callID string) string {
	if kind == "tool" {
		return messageID + "-tool-" + callID
	}
	return messageID + "-" + kind + "-" + strconv.Itoa(index)
}

// projectID returns the first 12 hex chars of sha256(root).
func projectID(root string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(root))
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// resolveInRoot resolves want against root, refusing anything that leaves it.
// Symlinks are resolved on both sides first, so a link out of the project is a
// refusal rather than a read.
func resolveInRoot(root, want string) (string, error) {
	// Reject absolute paths immediately (MAJOR: absolute paths silently reinterpreted as root-relative).
	if filepath.IsAbs(want) {
		return "", errOutsideRoot
	}

	cleaned := filepath.Clean(want)
	cleaned = strings.TrimPrefix(cleaned, "/")
	candidate := filepath.Join(root, cleaned)

	if _, err := os.Lstat(candidate); err != nil {
		if os.IsNotExist(err) {
			return "", errMissingPath
		}
		return "", errUnreadableLink
	}

	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", errUnreadableLink
	}

	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", errOutsideRoot
	}
	// MAJOR: strings.HasPrefix(rel, "..") false-positives on legitimate names like "..foo".
	// Use exact match or prefix with separator.
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errOutsideRoot
	}

	return resolved, nil
}

var (
	errMissingPath    = errors.New("path not found")
	errUnreadableLink = errors.New("unreadable or broken symlink")
	errOutsideRoot    = errors.New("path escapes root")
)
