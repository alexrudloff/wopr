package efficiency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/internal/text"
)

// Evidence-Preserving Reducer: delegate the first read of a long build or
// test log to a cheap model, then verify what comes back. The raw log is
// archived, the reducer returns a JSON receipt, and the receipt replaces the
// log only when every quoted line is found byte for byte in the archive. A
// receipt that cannot be checked is discarded and the original output reaches
// the primary model untouched.
//
// In this harness the reducer model comes from the router's cheapest tier
// that fits, so the first read normally happens on the free local model.

// ReducerResponse is what the completion callback returns.
type ReducerResponse struct {
	Provider    string
	Model       string
	Text        string
	OK          bool
	StopReason  string
	Error       string
	TotalTokens int
}

// ReducerCompleter runs the reducer request. It returns an error when no
// reducer model is available or the call fails.
type ReducerCompleter func(ctx context.Context, system, user string, maxTokens int, timeout time.Duration) (ReducerResponse, error)

// Reducer reduces diagnostic tool results for one session.
type Reducer struct {
	root     string
	complete ReducerCompleter
	journal  *ledger
	// Notify reports bytes removed from future prompts.
	Notify func(mechanism, saving string, tokens int)
	// MinBytes returns the smallest log to reduce for the model serving
	// the conversation; nil means ReducerMinBytes.
	MinBytes func() int
	// OnVerdict reports whether a reducer model's receipt verified, and
	// OnApplied the archive path of each receipt applied.
	OnVerdict func(provider, model string, ok bool)
	OnApplied func(path string)
}

// NewReducer creates a reducer archiving under root.
func NewReducer(root string, complete ReducerCompleter) *Reducer {
	return &Reducer{root: root, complete: complete, journal: newLedger(filepath.Join(root, "evidence-preserving-reducer", "journal.jsonl"))}
}

// Reduced is the replacement for a tool result.
type Reduced struct {
	Content string
	Details map[string]any
}

var fullOutputPattern = regexp.MustCompile(`Full output:\s*([^\]\r\n]+)`)

// candidate identifies the log inside a tool result: a plain bash result, or
// the command output appended by a fused edit/write.
type candidate struct {
	command string
	body    string
	// project puts the receipt back where the raw output was.
	project func(receipt string) string
}

func detailsFullOutputPath(details any) string {
	if d, ok := details.(map[string]any); ok {
		if path, ok := d["fullOutputPath"].(string); ok {
			return path
		}
		if path, ok := d["FullOutputPath"].(string); ok {
			return path
		}
	}
	// Typed bash details are read through JSON to stay independent of the
	// tools package.
	raw, err := json.Marshal(details)
	if err != nil {
		return ""
	}
	var probe struct {
		FullOutputPath string `json:"FullOutputPath"`
		Lower          string `json:"fullOutputPath"`
	}
	_ = json.Unmarshal(raw, &probe)
	if probe.FullOutputPath != "" {
		return probe.FullOutputPath
	}
	return probe.Lower
}

// safeBashTempPath accepts only the harness's own bash output files in the
// system temporary directory.
func safeBashTempPath(path string) bool {
	if path == "" {
		return false
	}
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "wopr-bash-") || !strings.HasSuffix(base, ".log") || strings.ContainsAny(base, `/\`) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	root, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return filepath.Dir(resolved) == root
}

// exactBody prefers the untruncated file the bash tool wrote, so evidence is
// checked against the exact bytes the command produced.
func exactBody(inline string, details any) string {
	path := detailsFullOutputPath(details)
	if path == "" {
		if m := fullOutputPattern.FindStringSubmatch(inline); m != nil {
			path = strings.TrimSpace(m[1])
		}
	}
	if !safeBashTempPath(path) {
		return inline
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return inline
	}
	return string(data)
}

func findCandidate(toolName string, args json.RawMessage, result agent.AgentToolResult) *candidate {
	switch toolName {
	case "bash":
		var in struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal(args, &in)
		if in.Command == "" {
			return nil
		}
		return &candidate{command: in.Command, body: exactBody(result.Content, result.Details), project: func(receipt string) string { return receipt }}
	case "edit", "write":
		command := ThenRunCommand(args)
		if command == "" {
			return nil
		}
		marker := ThenRunSucceeded
		if result.IsError {
			marker = ThenRunFailed
		}
		idx := strings.Index(result.Content, marker)
		if idx < 0 {
			return nil
		}
		suffixStart := idx + len(marker)
		suffix := result.Content[suffixStart:]
		separator := "\n"
		trimmed := strings.TrimLeft(suffix, "\r\n")
		if len(trimmed) < len(suffix) {
			separator = suffix[:len(suffix)-len(trimmed)]
		}
		inline := trimmed
		prefix := result.Content[:suffixStart]
		return &candidate{command: command, body: exactBody(inline, nil), project: func(receipt string) string { return prefix + separator + receipt }}
	}
	return nil
}

func archiveBody(root, body string) (Archive, error) {
	hash := SHA256(body)
	dir := filepath.Join(root, "evidence-preserving-reducer", "objects", hash[:2])
	path := filepath.Join(dir, hash+".txt")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Archive{}, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	switch {
	case err == nil:
		_, werr := f.WriteString(body)
		cerr := f.Close()
		if werr != nil {
			return Archive{}, werr
		}
		if cerr != nil {
			return Archive{}, cerr
		}
	case errors.Is(err, os.ErrExist):
		existing, rerr := os.ReadFile(path)
		if rerr != nil || string(existing) != body {
			return Archive{}, fmt.Errorf("reducer archive integrity failure: %s", path)
		}
	default:
		return Archive{}, err
	}
	lines := 0
	if body != "" {
		lines = strings.Count(body, "\n") + 1
	}
	return Archive{Hash: hash, Bytes: len(body), Lines: lines, Path: path}, nil
}

// Reduce decides whether a tool result is a diagnostic log worth reducing
// and, when the receipt verifies, returns the replacement. nil means keep the
// original.
func (r *Reducer) Reduce(ctx context.Context, toolCallID, toolName string, args json.RawMessage, result agent.AgentToolResult) *Reduced {
	c := findCandidate(toolName, args, result)
	if c == nil || !DiagnosticCommand.MatchString(c.command) {
		return nil
	}
	body := c.body
	minBytes := ReducerMinBytes
	if r.MinBytes != nil {
		minBytes = r.MinBytes()
	}
	if len(body) < minBytes {
		return nil
	}
	if len([]rune(body)) > ReducerMaxChars {
		r.journal.append(map[string]any{"kind": "fallback", "reason": "source-over-max-chars", "sourceChars": len([]rune(body)), "maxChars": ReducerMaxChars})
		return nil
	}
	if LikelySecret.MatchString(body) {
		r.journal.append(map[string]any{"kind": "fallback", "reason": "likely-secret"})
		return nil
	}
	archive, err := archiveBody(r.root, body)
	if err != nil {
		r.journal.append(map[string]any{"kind": "fallback", "reason": "archive-failure", "error": err.Error()})
		return nil
	}
	fallback := func(reason string, extra map[string]any) {
		entry := map[string]any{"kind": "fallback", "toolCallId": toolCallID, "sourceSha256": archive.Hash, "reason": reason}
		maps.Copy(entry, extra)
		r.journal.append(entry)
	}
	r.journal.append(map[string]any{"kind": "candidate", "toolCallId": toolCallID, "commandSha256": SHA256(c.command), "isError": result.IsError, "sourceSha256": archive.Hash, "sourceBytes": archive.Bytes, "sourceLines": archive.Lines, "sourcePath": archive.Path})

	response, err := r.complete(ctx, ReducerInstructions(), ReducerInput(c.command, result.IsError, archive, body), ReducerMaxOutputTokens, time.Duration(ReducerTimeoutMs)*time.Millisecond)
	if err != nil {
		reason := "model-call-exception"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "model-call-timeout"
		} else if errors.Is(err, ErrReducerUnavailable) {
			reason = "reducer-model-unavailable"
		}
		fallback(reason, map[string]any{"error": err.Error()})
		return nil
	}
	r.journal.append(map[string]any{"kind": "provider_response", "toolCallId": toolCallID, "sourceSha256": archive.Hash, "provider": response.Provider, "model": response.Model, "stopReason": response.StopReason, "errorMessage": response.Error, "totalTokens": response.TotalTokens})
	if !response.OK {
		fallback("model-response-error", map[string]any{"stopReason": response.StopReason, "errorMessage": response.Error})
		return nil
	}
	receipt, reason := ValidateReceipt(response.Text, archive, body, result.IsError)
	if r.OnVerdict != nil {
		r.OnVerdict(response.Provider, response.Model, receipt != nil)
	}
	if receipt == nil {
		fallback(reason, map[string]any{"outputHead": text.Clip(response.Text, 600)})
		return nil
	}
	text := ReceiptText(c.command, archive, receipt, response.Provider, response.Model, response.TotalTokens)
	if len(text) >= archive.Bytes {
		fallback("receipt-not-smaller", map[string]any{"receiptBytes": len(text), "sourceBytes": archive.Bytes})
		return nil
	}
	r.journal.append(map[string]any{"kind": "applied", "toolCallId": toolCallID, "commandSha256": SHA256(c.command), "sourceSha256": archive.Hash, "sourceBytes": archive.Bytes, "receiptSha256": SHA256(text), "receiptBytes": len(text), "evidenceCount": len(receipt.Evidence), "uncertain": receipt.Uncertain})
	if r.OnApplied != nil {
		r.OnApplied(archive.Path)
	}
	if r.Notify != nil {
		r.Notify("Evidence Reducer", formatBytesSaved(archive.Bytes-len(text)), max(0, archive.Bytes-len(text))/4)
	}
	details := map[string]any{}
	if existing, ok := result.Details.(map[string]any); ok {
		maps.Copy(details, existing)
	} else if result.Details != nil {
		details["original"] = result.Details
	}
	details["evidencePreservingReducer"] = map[string]any{
		"schema": ReceiptSchema, "sourceSha256": archive.Hash, "sourceBytes": archive.Bytes,
		"receiptSha256": SHA256(text), "receiptBytes": len(text), "evidenceCount": len(receipt.Evidence), "uncertain": receipt.Uncertain,
		"reducerProvider": response.Provider, "reducerModel": response.Model,
	}
	return &Reduced{Content: c.project(text), Details: details}
}

// ErrReducerUnavailable reports that no reducer model could be resolved.
var ErrReducerUnavailable = errors.New("efficiency: reducer model unavailable")

func formatBytesSaved(n int) string {
	n = max(n, 0)
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1f MiB removed from future prompts", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.1f KiB removed from future prompts", float64(n)/1024)
	default:
		return fmt.Sprintf("%d B removed from future prompts", n)
	}
}
