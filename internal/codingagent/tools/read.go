package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/imageprocessing"
	"github.com/alexrudloff/wopr/internal/text"
)

// ─── Read Tool ────────────────────────────────────────────────────────────────

type readParams struct {
	Path   string   `json:"path"`
	Offset *float64 `json:"offset,omitempty"`
	Limit  *float64 `json:"limit,omitempty"`
}

// ReadTool reads file contents.
type ReadTool struct {
	CWD              string
	AutoResizeImages *bool
	ResizeOptions    *ai.ModelImageResizeOptions
	// Hashline, when set, decides per model whether text lines carry
	// hashline anchors.
	Hashline HashlineMode
}

func (t *ReadTool) Name() string  { return "read" }
func (t *ReadTool) Label() string { return "" }

func (t *ReadTool) Schema() ai.ToolSchema {
	return ai.ToolSchema{
		Name:        "read",
		Description: "Read the contents of a file. Supports text files and images (jpg, png, gif, webp, bmp). Images are sent as attachments. For text files, output is truncated to 2000 lines or 50KB (whichever is hit first). Use offset/limit for large files. When you need the full file, continue with offset until complete.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string", "description": "Path to the file to read (relative or absolute)"},
				"offset": map[string]any{"type": "number", "description": "Line number to start reading from (1-indexed)"},
				"limit":  map[string]any{"type": "number", "description": "Maximum number of lines to read"},
			},
			"required": []string{"path"},
		},
		ConstrainedSampling: strictToolSampling(),
		PromptGuidelines: []string{
			"Use read to examine files instead of cat or sed.",
		},
	}
}

func (t *ReadTool) ExecutionMode() agent.ToolExecutionMode { return agent.ToolModeParallel }

// Execute resolves the path, checks it is readable, returns a supported image
// as an attachment, and otherwise decodes the text (invalid UTF-8 becomes
// U+FFFD) and applies
// offset, limit and head truncation.
func (t *ReadTool) Execute(ctx context.Context, _ string, rawParams json.RawMessage, _ agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	var p readParams
	if err := json.Unmarshal(rawParams, &p); err != nil {
		return agent.AgentToolResult{}, fmt.Errorf("read: invalid params: %w", err)
	}
	if ctx.Err() != nil {
		return abortedResult, nil
	}

	path := resolveReadPath(p.Path, t.CWD)
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return agent.ErrorResult(directoryListing(path)), nil
	}
	if err := checkReadable(path); err != nil {
		return agent.ErrorResult(nodeFSError(err, "access", path)), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return agent.ErrorResult(nodeFSError(err, "read", "")), nil
	}
	if ctx.Err() != nil {
		return abortedResult, nil
	}
	if mime := SupportedImageMime(data); mime != "" {
		return t.readImage(ctx, data, mime), nil
	}
	var decoder utf8StreamDecoder
	result, err := readTextResult(p, decoder.decode(data, false), hashlineOn(ctx, t.Hashline))
	if err != nil {
		return agent.ErrorResult(err.Error()), nil
	}
	return result, nil
}

// checkReadable reports whether path can be opened for reading.
func checkReadable(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return f.Close()
}

// readTextResult builds the result for a text file. With hashline on, lines
// are split as edit splits them (BOM dropped, CRLF and CR normalized) and
// each carries its anchor.
func readTextResult(p readParams, textContent string, hashline bool) (agent.AgentToolResult, error) {
	allLines := strings.Split(textContent, "\n")
	if hashline {
		_, textContent = text.SplitBom(textContent)
		allLines = hashlineLines(normalizeToLF(textContent))
	}
	join := func(start, end int) string { return strings.Join(allLines[start:end], "\n") }
	if hashline {
		join = func(start, end int) string { return formatHashlines(allLines, start, end) }
	}
	totalFileLines := len(allLines)
	// offset is 1-indexed; JavaScript's slice truncates a fractional index.
	startLine := 0
	if p.Offset != nil && *p.Offset != 0 {
		startLine = int(math.Max(0, *p.Offset-1))
	}
	startLineDisplay := startLine + 1
	if startLine >= totalFileLines {
		return agent.AgentToolResult{}, fmt.Errorf("Offset %s is beyond end of file (%d lines total)", jsNumber(*p.Offset), totalFileLines)
	}

	var selected string
	userLimitedLines, hasUserLimit := 0, p.Limit != nil
	if hasUserLimit {
		// Math.min(startLine + limit, allLines.length), then slice(startLine,
		// endLine): a negative end counts from the end of the array.
		endLine := int(math.Min(float64(startLine)+*p.Limit, float64(totalFileLines)))
		sliceEnd := endLine
		if sliceEnd < 0 {
			sliceEnd = max(totalFileLines+sliceEnd, 0)
		}
		selected = join(startLine, max(startLine, sliceEnd))
		userLimitedLines = endLine - startLine
	} else {
		selected = join(startLine, totalFileLines)
	}
	tr := TruncateHead(selected, DefaultMaxBytes, DefaultMaxLines)

	var output string
	var truncation *TruncationResult
	switch {
	case tr.FirstLineExceedsLimit:
		firstLineSize := FormatSize(len(allLines[startLine]))
		output = fmt.Sprintf("[Line %d is %s, exceeds %s limit. Use bash: sed -n '%dp' %s | head -c %d]",
			startLineDisplay, firstLineSize, FormatSize(DefaultMaxBytes), startLineDisplay, p.Path, DefaultMaxBytes)
		truncation = &tr
	case tr.Truncated:
		endLineDisplay := startLineDisplay + tr.OutputLines - 1
		nextOffset := endLineDisplay + 1
		output = tr.Content
		if tr.TruncatedBy == "lines" {
			output += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Use offset=%d to continue.]",
				startLineDisplay, endLineDisplay, totalFileLines, nextOffset)
		} else {
			output += fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%s limit). Use offset=%d to continue.]",
				startLineDisplay, endLineDisplay, totalFileLines, FormatSize(DefaultMaxBytes), nextOffset)
		}
		truncation = &tr
	case hasUserLimit && startLine+userLimitedLines < totalFileLines:
		remaining := totalFileLines - (startLine + userLimitedLines)
		nextOffset := startLine + userLimitedLines + 1
		output = fmt.Sprintf("%s\n\n[%d more lines in file. Use offset=%d to continue.]", tr.Content, remaining, nextOffset)
	default:
		output = tr.Content
	}
	return agent.AgentToolResult{
		Content: output,
		Details: &ReadDetails{
			Path:       p.Path,
			StartLine:  startLineDisplay,
			TotalLines: totalFileLines,
			Truncated:  truncation != nil,
			Truncation: truncation,
		},
	}, nil
}

// readImage uses the execution model profile before the standalone fallback,
// matching createReadToolDefinition's ctx.model.inputLimits precedence.
func (t *ReadTool) readImage(ctx context.Context, data []byte, mime string) agent.AgentToolResult {
	env, _ := agent.ToolEnvironmentFrom(ctx)
	options := t.ResizeOptions
	if env.InputLimits != nil && env.InputLimits.Images != nil && env.InputLimits.Images.Resize != nil {
		options = env.InputLimits.Images.Resize
	}
	autoResize := t.AutoResizeImages == nil || *t.AutoResizeImages
	processed, processedMIME, hints, err := imageprocessing.ProcessImage(data, mime, autoResize, options)
	result := agent.AgentToolResult{}
	if err != nil {
		result.Content = fmt.Sprintf("Read image file [%s]\n%s", mime, err)
	} else {
		result.Content = fmt.Sprintf("Read image file [%s]", processedMIME)
		if hints != "" {
			result.Content += "\n" + hints
		}
		result.Images = []ai.ImageContent{{MimeType: processedMIME, Data: base64Encode(processed)}}
	}
	if env.SupportsImages != nil && !*env.SupportsImages {
		result.Content += "\n[Current model does not support images. The image will be omitted from this request.]"
	}
	return result
}

func readToolWithSettings(cwd string, settings BashSettingsView) *ReadTool {
	tool := &ReadTool{CWD: cwd}
	if images, ok := settings.(interface{ GetImageAutoResize() bool }); ok {
		autoResize := images.GetImageAutoResize()
		tool.AutoResizeImages = &autoResize
	}
	return tool
}

// directoryListing answers a read of a directory with its entries (up to
// 50, directories marked "/") so the model needn't spend a turn on ls.
func directoryListing(path string) string {
	entries, err := os.ReadDir(path)
	if err != nil {
		return path + " is a directory. Use ls to list it."
	}
	const shown = 50
	names := make([]string, 0, min(len(entries), shown))
	for _, e := range entries[:min(len(entries), shown)] {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	out := path + " is a directory, not a file. Its entries: " + strings.Join(names, ", ")
	if len(entries) > shown {
		out += fmt.Sprintf(" (and %d more; use ls)", len(entries)-shown)
	}
	return out
}
