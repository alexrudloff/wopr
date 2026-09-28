package efficiency

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// ObservationPack keeps large tool results reachable without replaying
// them. A large result is sent in full for its first FullSends provider
// requests, then replaced with a short stable placeholder. The original bytes
// are archived by observation id and the agent pulls exact pages back with the
// obs_recall tool.

const (
	// ThresholdBytes is the size above which a tool result participates.
	ThresholdBytes = 10 * 1024
	// FullSends is how many provider requests carry the full payload.
	FullSends = 2
	// PlaceholderExcerptBytes is split between head and tail, whole lines only.
	PlaceholderExcerptBytes = 1024

	recallMaxBytes           = 16 * 1024
	recallMaxLines           = 400
	recallHeaderReserveBytes = 512
	recallHeaderLines        = 2
)

// Observation is one archived tool result.
type Observation struct {
	ID          string
	ContentHash string
	FilePath    string
	ToolName    string
	Text        string
	Bytes       int
	Lines       int
	Tokens      int
	// Excerpt is the placeholder's head-and-tail budget in bytes.
	Excerpt int
}

// Pack projects large tool results into placeholders for one session.
type Pack struct {
	root   string
	mu     sync.Mutex
	sent   map[string]int
	stored map[string]bool
	ledger *ledger
	// excerpt keeps each observation's placeholder budget once it has been
	// replaced, so its placeholder stays byte-stable for the prompt cache.
	excerpt map[string]int
	// Notify reports the first measured saving for an observation.
	Notify func(mechanism, saving string, tokens int)
	// Params returns the numbers for the model serving the request; nil
	// means the globals.
	Params func() PackParams
	// OnPlaced and OnCut report the first time an observation is replaced
	// by its placeholder or cut by half-life.
	OnPlaced func(id string)
	OnCut    func(id string)
}

// NewPack creates a pack archiving under root (the session runtime root).
func NewPack(root string) *Pack {
	return &Pack{root: root, sent: map[string]int{}, stored: map[string]bool{}, excerpt: map[string]int{}, ledger: newLedger(filepath.Join(root, "observation-pack", "ledger.jsonl"))}
}

// Root returns the archive root.
func (p *Pack) Root() string { return p.root }

func countLines(text string) int {
	if text == "" {
		return 0
	}
	n := strings.Count(text, "\n")
	if !strings.HasSuffix(text, "\n") {
		n++
	}
	return n
}

// ObservationPath is the content-addressed object path.
func ObservationPath(root, id string) string {
	return filepath.Join(root, "observation-pack", "objects", id+".txt")
}

// IsObservationID reports whether id has the obs_<24 hex> shape.
func IsObservationID(id string) bool {
	if !strings.HasPrefix(id, "obs_") || len(id) != 4+24 {
		return false
	}
	for _, r := range id[4:] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// pureText reports whether a tool result is a non-error, text-only result.
func pureText(m *agent.ToolResultMessage) bool {
	if m == nil || m.IsError || len(m.Content) == 0 {
		return false
	}
	for _, block := range m.Content {
		if _, ok := block.(ai.TextContent); !ok {
			return false
		}
	}
	return true
}

func (p *Pack) params() PackParams {
	if p.Params == nil {
		return DefaultPackParams()
	}
	return p.Params()
}

// NewObservation returns the observation for a tool result, or nil when it
// is too small or holds a reducer receipt. A result the pack already tracks
// stays tracked when the threshold rises, so it is never re-inflated.
func (p *Pack) NewObservation(m *agent.ToolResultMessage) *Observation {
	text := m.Text()
	if ContainsReceipt(text) || len(text) <= packThresholdMin {
		return nil
	}
	contentHash := SHA256(text)
	id := "obs_" + SHA256(m.ToolName + "\x00" + m.ToolCallID + "\x00" + contentHash)[:24]
	params := p.params()
	p.mu.Lock()
	_, tracked := p.sent[id]
	excerpt, fixed := p.excerpt[id]
	p.mu.Unlock()
	if len(text) <= params.Threshold && !tracked {
		return nil
	}
	if !fixed {
		excerpt = params.Excerpt
	}
	return &Observation{
		Excerpt:     excerpt,
		ID:          id,
		ContentHash: contentHash,
		FilePath:    ObservationPath(p.root, id),
		ToolName:    m.ToolName,
		Text:        text,
		Bytes:       len(text),
		Lines:       countLines(text),
		Tokens:      ai.EstimateTextTokens(text),
	}
}

// Archive stores text as an observation obs_recall can page and returns its
// id. name labels the source and key distinguishes its records.
func (p *Pack) Archive(name, key, text string) (string, error) {
	contentHash := SHA256(text)
	id := "obs_" + SHA256(name + "\x00" + key + "\x00" + contentHash)[:24]
	o := &Observation{ID: id, ContentHash: contentHash, FilePath: ObservationPath(p.root, id), ToolName: name, Text: text, Bytes: len(text), Lines: countLines(text), Tokens: ai.EstimateTextTokens(text)}
	if err := EnsureStored(o); err != nil {
		return "", err
	}
	p.mu.Lock()
	p.stored[id] = true
	p.mu.Unlock()
	return id, nil
}

// EnsureStored writes the payload once, refusing symlinks and verifying an
// existing object byte for byte before reusing it.
func EnsureStored(o *Observation) error {
	dir := filepath.Dir(o.FilePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("observation directory is not a regular directory for %s", o.ID)
	}
	f, err := os.OpenFile(o.FilePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_, werr := f.WriteString(o.Text)
		cerr := f.Close()
		if werr != nil {
			return werr
		}
		return cerr
	}
	if !errors.Is(err, os.ErrExist) {
		return err
	}
	existing, err := os.Lstat(o.FilePath)
	if err != nil {
		return err
	}
	if !existing.Mode().IsRegular() {
		return fmt.Errorf("content-addressed observation is not a regular file for %s", o.ID)
	}
	if existing.Size() != int64(o.Bytes) {
		return fmt.Errorf("content-addressed observation size mismatch for %s", o.ID)
	}
	data, err := os.ReadFile(o.FilePath)
	if err != nil {
		return err
	}
	if SHA256(string(data)) != o.ContentHash {
		return fmt.Errorf("content-addressed observation hash mismatch for %s", o.ID)
	}
	return nil
}

func completeLineExcerpt(text string, budget int, fromEnd bool) string {
	lines := strings.SplitAfter(text, "\n")
	var selected []string
	used := 0
	if fromEnd {
		for _, line := range slices.Backward(lines) {
			if used+len(line) > budget {
				break
			}
			selected = append([]string{line}, selected...)
			used += len(line)
		}
	} else {
		for _, line := range lines {
			if used+len(line) > budget {
				break
			}
			selected = append(selected, line)
			used += len(line)
		}
	}
	return strings.Join(selected, "")
}

// Placeholder is the stable replacement text.
func Placeholder(o *Observation) string {
	budget := cmp.Or(o.Excerpt, PlaceholderExcerptBytes)
	headBudget := budget / 2
	tailBudget := budget - headBudget
	return strings.Join([]string{
		fmt.Sprintf("[large tool result replaced after its first %d provider requests]", FullSends),
		"id: " + o.ID,
		"tool: " + o.ToolName,
		fmt.Sprintf("original_bytes: %d", o.Bytes),
		fmt.Sprintf("original_lines: %d", o.Lines),
		fmt.Sprintf("estimated_tokens: %d", o.Tokens),
		fmt.Sprintf(`retrieve: call obs_recall with {"id":"%s","offset":0}; continue with returned next_offset`, o.ID),
		fmt.Sprintf("[first complete lines, up to %d bytes]", headBudget),
		completeLineExcerpt(o.Text, headBudget, false),
		fmt.Sprintf("[middle omitted; last complete lines, up to %d bytes]", tailBudget),
		completeLineExcerpt(o.Text, tailBudget, true),
		fmt.Sprintf("[%d original bytes omitted]", o.Bytes),
	}, "\n")
}

// Project rewrites the request context: every large text tool result that
// has already been sent FullSends times becomes its placeholder. Messages are
// copied, never mutated, so the stored session stays intact. It fails open:
// a packing failure leaves the observation in place.
func (p *Pack) Project(messages []agent.AgentMessage) []agent.AgentMessage {
	projected := messages
	copied := false
	// How many provider requests each message has already been part of,
	// counted by the assistant messages that follow it.
	priorAssistant := make([]int, len(messages))
	count := 0
	for i := len(messages) - 1; i >= 0; i-- {
		priorAssistant[i] = count
		if messages[i].Assistant != nil {
			count++
		}
	}
	requestIndex := count + 1
	for i, message := range messages {
		if !pureText(message.ToolResult) {
			continue
		}
		observation := p.NewObservation(message.ToolResult)
		if observation == nil {
			continue
		}
		p.mu.Lock()
		stored := p.stored[observation.ID]
		p.mu.Unlock()
		if !stored {
			if err := EnsureStored(observation); err != nil {
				continue
			}
			p.mu.Lock()
			p.stored[observation.ID] = true
			p.mu.Unlock()
		}
		p.mu.Lock()
		previous, known := p.sent[observation.ID]
		if !known {
			previous = priorAssistant[i]
		}
		if previous < FullSends {
			p.ledger.append(map[string]any{"event": "full", "id": observation.ID, "request": requestIndex, "tool": observation.ToolName, "originalBytes": observation.Bytes, "originalLines": observation.Lines, "originalTokens": observation.Tokens, "contentHash": observation.ContentHash})
			p.sent[observation.ID] = previous + 1
			p.mu.Unlock()
			continue
		}
		placeholder := Placeholder(observation)
		placeholderTokens := ai.EstimateTextTokens(placeholder)
		removed := max(0, observation.Tokens-placeholderTokens)
		p.ledger.append(map[string]any{"event": "placeholder", "id": observation.ID, "request": requestIndex, "sendNumber": previous + 1, "tool": observation.ToolName, "originalBytes": observation.Bytes, "originalLines": observation.Lines, "originalTokens": observation.Tokens, "placeholderBytes": len(placeholder), "placeholderTokens": placeholderTokens, "removedTokens": removed})
		first := previous == FullSends
		p.sent[observation.ID] = previous + 1
		if _, ok := p.excerpt[observation.ID]; !ok {
			p.excerpt[observation.ID] = observation.Excerpt
		}
		p.mu.Unlock()
		if first && p.Notify != nil {
			p.Notify("Observation Pack", fmt.Sprintf("%d context tokens avoided", removed), removed)
		}
		if first && p.OnPlaced != nil {
			p.OnPlaced(observation.ID)
		}
		if !copied {
			projected = append([]agent.AgentMessage(nil), messages...)
			copied = true
		}
		replacement := *message.ToolResult
		replacement.Content = []ai.ToolResultMessageContent{ai.TextContent{Text: placeholder}}
		projected[i] = agent.AgentMessage{ToolResult: &replacement}
	}
	return projected
}

// Half-life truncation: for a model with a small context window, text tool
// results older than the newest few are cut to a short head and tail
// excerpt, and obs_recall pages the archived original back. The cut
// frontier advances in steps of the kept count, so the cached prompt prefix
// changes only once every few requests instead of on every one.

const (
	// halfLifeMaxWindow is the largest context window that truncates.
	halfLifeMaxWindow = 131072
	// halfLifeExcerptBytes is what an old result keeps, head and tail.
	halfLifeExcerptBytes = 1024
)

// HalfLifeKeep is how many of the newest tool results stay whole for a
// model with the given context window, or 0 when the window is large enough
// that truncation does not pay.
func HalfLifeKeep(contextWindow int) int {
	switch {
	case contextWindow <= 0 || contextWindow > halfLifeMaxWindow:
		return 0
	case contextWindow <= 65536:
		return 4
	}
	return 6
}

// ProjectHalfLife cuts text tool results older than the newest keep
// (rounded down to a multiple of keep) to an excerpt. Messages are copied,
// never mutated. It fails open: an archive failure leaves the result whole.
func (p *Pack) ProjectHalfLife(messages []agent.AgentMessage, keep int) []agent.AgentMessage {
	if keep <= 0 {
		return messages
	}
	var results []int
	for i, message := range messages {
		if message.ToolResult != nil {
			results = append(results, i)
		}
	}
	frontier := (len(results) - keep) / keep * keep
	if frontier <= 0 {
		return messages
	}
	projected := slices.Clone(messages)
	for _, i := range results[:frontier] {
		result := projected[i].ToolResult
		if !pureText(result) {
			continue
		}
		text := result.Text()
		if len(text) <= 2*halfLifeExcerptBytes || strings.HasPrefix(text, "[large tool result replaced") {
			continue
		}
		id, err := p.Archive(result.ToolName, result.ToolCallID, text)
		if err != nil {
			continue
		}
		if p.OnCut != nil {
			p.OnCut(id)
		}
		stub := strings.Join([]string{
			fmt.Sprintf("[older tool output cut to an excerpt: %d bytes, %d lines; the full text is %s, read it with obs_recall {\"id\":\"%s\",\"offset\":0}, or re-run the tool if the state may have changed]", len(text), countLines(text), id, id),
			completeLineExcerpt(text, halfLifeExcerptBytes/2, false),
			"[...]",
			completeLineExcerpt(text, halfLifeExcerptBytes/2, true),
		}, "\n")
		replacement := *result
		replacement.Content = []ai.ToolResultMessageContent{ai.TextContent{Text: stub}}
		projected[i] = agent.AgentMessage{ToolResult: &replacement}
	}
	return projected
}

// RecallChunk is one page of an archived observation.
type RecallChunk struct {
	Text       string
	Bytes      int
	Lines      int
	NextOffset int
	EOF        bool
}

// ReadRecallChunk reads up to maxBytes/maxLines from the object at offset,
// refusing symlinks and never splitting a UTF-8 sequence.
func ReadRecallChunk(path string, offset, maxBytes, maxLines int) (RecallChunk, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return RecallChunk{}, err
	}
	if !info.Mode().IsRegular() {
		return RecallChunk{}, errors.New("stored observation is not a regular file")
	}
	size := int(info.Size())
	if offset > size {
		return RecallChunk{}, fmt.Errorf("offset %d exceeds observation size %d", offset, size)
	}
	f, err := os.Open(path)
	if err != nil {
		return RecallChunk{}, err
	}
	defer func() { _ = f.Close() }()
	available := size - offset
	buf := make([]byte, min(available, maxBytes+4))
	n, err := f.ReadAt(buf, int64(offset))
	if err != nil && n == 0 && available > 0 {
		return RecallChunk{}, err
	}
	end := min(n, maxBytes)
	newlines := 0
	for i := 0; i < end; i++ {
		if buf[i] == '\n' {
			newlines++
			if newlines == maxLines {
				end = i + 1
				break
			}
		}
	}
	for end > 0 && end < len(buf) && buf[end]&0xC0 == 0x80 {
		end--
	}
	chunk := buf[:end]
	next := offset + len(chunk)
	return RecallChunk{Text: string(chunk), Bytes: len(chunk), Lines: countLines(string(chunk)), NextOffset: next, EOF: next >= size}, nil
}

// RecallTool is the obs_recall tool.
type RecallTool struct {
	pack *Pack
	// Notify reports a recall as an avoided replay.
	Notify func(mechanism, saving string, tokens int)
}

// NewRecallTool creates the tool for a pack.
func NewRecallTool(pack *Pack) *RecallTool { return &RecallTool{pack: pack} }

func (t *RecallTool) Name() string  { return "obs_recall" }
func (t *RecallTool) Label() string { return "Recall Observation" }

func (t *RecallTool) Schema() ai.ToolSchema {
	return ai.ToolSchema{
		Name:        "obs_recall",
		Description: "Read a stored large tool result by observation id and byte offset.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":     map[string]any{"type": "string", "description": "Observation id from a placeholder"},
				"offset": map[string]any{"type": "integer", "minimum": 0, "description": "Byte offset, default 0"},
			},
			"required": []string{"id"},
		},
		PromptGuidelines: []string{
			"A tool result replaced by a placeholder can be read back exactly with obs_recall using the placeholder's id; page with next_offset.",
		},
	}
}

func (t *RecallTool) ExecutionMode() agent.ToolExecutionMode { return agent.ToolModeParallel }

func (t *RecallTool) Execute(_ context.Context, _ string, params json.RawMessage, _ agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	var in struct {
		ID     string `json:"id"`
		Offset int    `json:"offset"`
	}
	if err := json.Unmarshal(params, &in); err != nil {
		return agent.AgentToolResult{}, err
	}
	if !IsObservationID(in.ID) {
		return agent.AgentToolResult{}, fmt.Errorf("unknown observation id: %s", in.ID)
	}
	if in.Offset < 0 {
		return agent.AgentToolResult{}, errors.New("offset must be non-negative")
	}
	chunk, err := ReadRecallChunk(ObservationPath(t.pack.root, in.ID), in.Offset, recallMaxBytes-recallHeaderReserveBytes, recallMaxLines-recallHeaderLines)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return agent.AgentToolResult{}, fmt.Errorf("unknown observation id: %s", in.ID)
		}
		return agent.AgentToolResult{}, err
	}
	header := fmt.Sprintf("[obs_recall id=%s offset=%d next_offset=%d eof=%t]\n[chunk_bytes=%d chunk_lines=%d; use next_offset to continue]", in.ID, in.Offset, chunk.NextOffset, chunk.EOF, chunk.Bytes, chunk.Lines)
	content := header + "\n" + chunk.Text
	if len(content) > recallMaxBytes || countLines(content) > recallMaxLines {
		return agent.AgentToolResult{}, errors.New("recall output exceeded its hard limit")
	}
	t.pack.ledger.append(map[string]any{"event": "recall", "id": in.ID, "offset": in.Offset, "bytes": chunk.Bytes, "lines": chunk.Lines, "nextOffset": chunk.NextOffset, "eof": chunk.EOF})
	if t.Notify != nil {
		t.Notify("Observation Pack", "full observation replay avoided", 0)
	}
	return agent.AgentToolResult{
		Content: content,
		Details: map[string]any{"id": in.ID, "offset": in.Offset, "bytes": chunk.Bytes, "lines": chunk.Lines, "nextOffset": chunk.NextOffset, "eof": chunk.EOF},
		Preview: fmt.Sprintf("Recalled %d bytes across %d lines", chunk.Bytes, chunk.Lines),
	}, nil
}

// ledger is an append-only JSONL record of what a mechanism did.
type ledger struct {
	mu   sync.Mutex
	path string
}

func newLedger(path string) *ledger { return &ledger{path: path} }

func (l *ledger) append(entry map[string]any) {
	record := make(map[string]any, len(entry)+1)
	record["timestamp"] = time.Now().UTC().Format(time.RFC3339Nano)
	maps.Copy(record, entry)
	line, err := json.Marshal(record)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(bytes.TrimSpace(line), '\n'))
}
