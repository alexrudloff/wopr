package codingagent

// session_share.go implements /share: it renders the active branch as a
// Markdown transcript and publishes it as a secret GitHub gist through the gh
// CLI. It also owns the share state entry that /bug attaches to exported
// transcripts.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"uuid"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/tui"
)

// ShareTool is one tool schema recorded in the wopr.share entry.
type ShareTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters,omitempty"`
}

// ShareState is the agent state the wopr.share entry records: the system
// prompt and the active tool schemas.
type ShareState struct {
	SystemPrompt string
	Tools        []ShareTool
}

// NewShareState captures the system prompt and the active agent tools.
func NewShareState(systemPrompt string, tools []agent.AgentTool) ShareState {
	state := ShareState{SystemPrompt: systemPrompt, Tools: make([]ShareTool, 0, len(tools))}
	for _, tool := range tools {
		schema := tool.Schema()
		shareTool := ShareTool{Name: tool.Name(), Description: schema.Description}
		if schema.Parameters != nil {
			shareTool.Parameters = schema.Parameters
		}
		state.Tools = append(state.Tools, shareTool)
	}
	return state
}

type shareEntryData struct {
	SystemPrompt string      `json:"systemPrompt"`
	Tools        []ShareTool `json:"tools"`
}

type shareEntry struct {
	Type       string         `json:"type"`
	CustomType string         `json:"customType"`
	ID         string         `json:"id"`
	ParentID   *string        `json:"parentId"`
	Timestamp  string         `json:"timestamp"`
	Data       shareEntryData `json:"data"`
}

// CreateShareTrailingEntries returns the trailing wopr.share entry carrying the
// system prompt and tool schemas of an exported transcript.
func CreateShareTrailingEntries(state ShareState, parentID *string, timestamp string) []any {
	return []any{shareEntry{
		Type:       "custom",
		CustomType: "wopr.share",
		ID:         uuid.NewV4().String()[:8],
		ParentID:   parentID,
		Timestamp:  timestamp,
		Data:       shareEntryData(state),
	}}
}

// shareTrailingEntries adapts CreateShareTrailingEntries to TrailingEntries.
func shareTrailingEntries(state ShareState) TrailingEntries {
	return func(parentID *string, timestamp string) []any {
		return CreateShareTrailingEntries(state, parentID, timestamp)
	}
}

const (
	// shareTimeout bounds the gh calls of one /share.
	shareTimeout       = 60 * time.Second
	sharePrivacyNotice = "Privacy: /share uploads the current session to a secret GitHub gist, including prompts, model responses, tool calls, and tool output. Anyone with the link can read it."
	shareNeedsGHNotice = "Session sharing needs the GitHub CLI. Install it from https://cli.github.com and run `gh auth login`."
)

// shareCommandRunner runs an external command and returns its standard output.
// Tests replace it so they never execute gh.
type shareCommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// runShareCommand runs name with args and reports stderr with a failure.
func runShareCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return out, fmt.Errorf("%w: %s", err, message)
		}
	}
	return out, err
}

// shareSession publishes the active branch as a secret gist. Each invocation
// owns its temporary directory, so concurrent shares cannot overwrite one
// another.
func shareSession(ctx context.Context, session *Session, run shareCommandRunner, showStatus func(string)) (string, error) {
	showStatus(sharePrivacyNotice)
	ctx, cancel := context.WithTimeout(ctx, shareTimeout)
	defer cancel()
	if _, err := run(ctx, "gh", "auth", "status"); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New(shareNeedsGHNotice)
	}

	shareDir, err := os.MkdirTemp("", "wopr-share-")
	if err != nil {
		return "", fmt.Errorf("Failed to export session: %w", err)
	}
	defer func() { _ = os.RemoveAll(shareDir) }()

	title := sessionShareTitle(session)
	transcript := RenderSessionMarkdown(title, agent.ConvertToLLM(session.BuildContext(nil), nil))
	path := filepath.Join(shareDir, "wopr-session.md")
	if err := os.WriteFile(path, []byte(transcript), 0o600); err != nil {
		return "", fmt.Errorf("Failed to export session: %w", err)
	}
	out, err := run(ctx, "gh", "gist", "create", "--desc", "WOPR session: "+title, path)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("Failed to create gist: %w", err)
	}
	gistURL, err := parseGistURL(out)
	if err != nil {
		return "", err
	}
	return "Share URL: " + tui.Hyperlink(gistURL, gistURL), nil
}

// sessionShareTitle names the transcript after the session name, falling back
// to the session ID.
func sessionShareTitle(session *Session) string {
	if name := strings.TrimSpace(session.GetSessionName()); name != "" {
		return name
	}
	return session.ID()
}

// parseGistURL returns the https URL gh prints as the last line of a
// successful `gh gist create`.
func parseGistURL(out []byte) (string, error) {
	fields := strings.Fields(string(out))
	if len(fields) > 0 {
		parsed, err := url.Parse(fields[len(fields)-1])
		if err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil {
			return parsed.String(), nil
		}
	}
	return "", errors.New("Failed to create gist: gh did not return a gist URL")
}

// RenderSessionMarkdown renders messages as a readable Markdown transcript.
// Thinking is omitted; tool calls and results are fenced verbatim.
func RenderSessionMarkdown(title string, messages []ai.Message) string {
	var b strings.Builder
	b.WriteString("# " + title + "\n")
	section := func(heading, body string) {
		if body = strings.TrimSpace(body); body != "" {
			b.WriteString("\n## " + heading + "\n\n" + body + "\n")
		}
	}
	for _, message := range messages {
		switch message := message.(type) {
		case ai.UserMessage:
			var text strings.Builder
			switch content := message.Content.(type) {
			case ai.UserText:
				text.WriteString(string(content))
			case ai.UserContentBlocks:
				for _, block := range content {
					switch block := block.(type) {
					case ai.TextContent:
						text.WriteString(block.Text)
					case ai.ImageContent:
						text.WriteString("\n_[image]_\n")
					}
				}
			}
			section("User", text.String())
		case ai.AssistantMessage:
			var text strings.Builder
			for _, block := range message.Content {
				switch block := block.(type) {
				case ai.TextContent:
					text.WriteString(block.Text)
				case ai.ToolCall:
					arguments, _ := json.MarshalIndent(block.Arguments, "", "  ")
					text.WriteString("\n\n**Tool call:** `" + block.Name + "`\n\n" + markdownFence(string(arguments), "json") + "\n")
				}
			}
			section("Assistant", text.String())
		case ai.ToolResultMessage:
			var text strings.Builder
			for _, block := range message.Content {
				if block, ok := block.(ai.TextContent); ok {
					text.WriteString(block.Text)
				}
			}
			heading := "Tool result: `" + message.ToolName + "`"
			if message.IsError {
				heading += " (error)"
			}
			if text.Len() > 0 {
				section(heading, markdownFence(text.String(), ""))
			}
		}
	}
	return b.String()
}

// markdownFence fences body with a backtick run longer than any inside it.
func markdownFence(body, language string) string {
	longest, run := 0, 0
	for _, r := range body {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", max(3, longest+1))
	return fence + language + "\n" + strings.TrimRight(body, "\n") + "\n" + fence
}
