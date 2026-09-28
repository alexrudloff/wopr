package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/alexrudloff/wopr/internal/text"
)

// Jev question ids. The ids never reach the model; all meaning lives in the
// instructions and criteria.
const (
	qKind      = "task_kind"
	qComplex   = "complexity"
	qCapable   = "capability"
	qDeep      = "deep_reasoning"
	qSensitive = "sensitive"
)

// Kind is a task category used for routing policy.
type Kind string

// Kinds the classifier distinguishes.
const (
	KindPlan      Kind = "plan"
	KindImplement Kind = "implement"
	KindDebug     Kind = "debug"
	KindRefactor  Kind = "refactor"
	KindReview    Kind = "review"
	KindResearch  Kind = "research"
	KindExplain   Kind = "explain"
	KindOperate   Kind = "operate"
	KindWrite     Kind = "write"
	KindChat      Kind = "chat"
)

var kindCriteria = map[string]string{
	string(KindPlan):      "Designing an approach, architecture, migration, or multi-step plan before code is written.",
	string(KindImplement): "Writing new code or features, adding files, wiring things up.",
	string(KindDebug):     "Finding the cause of a bug, failing test, crash, or wrong output and fixing it.",
	string(KindRefactor):  "Restructuring or renaming existing code without changing behavior.",
	string(KindReview):    "Reviewing a diff, PR, or design for defects and quality.",
	string(KindResearch):  "Investigating a codebase, library, or documentation to answer a question; reading more than writing.",
	string(KindExplain):   "Explaining code, a concept, or an error message.",
	string(KindOperate):   "Running commands, builds, deployments, git operations, or configuring tools.",
	string(KindWrite):     "Writing prose: docs, commit messages, comments, or messages.",
	string(KindChat):      "Small talk, acknowledgements, or a short question with no work attached.",
}

var complexityLevels = []string{
	"trivial: one line or one obvious change",
	"small: a contained change in one file",
	"moderate: a few files or a non-obvious change",
	"large: many files, cross-cutting, or requires careful design",
	"architectural: system-wide, risky, or deep tradeoffs",
}

var capabilityLevels = []string{
	"a small local model is enough",
	"a mid-size open model is enough",
	"needs a strong general model",
	"needs a frontier model to get right",
	"needs the very best model available",
}

// Classification is the routing signal for one prompt.
type Classification struct {
	Kind           Kind    `json:"kind"`
	KindConfidence float64 `json:"kindConfidence"`
	// Complexity and Capability are on 0..1.
	Complexity    float64 `json:"complexity"`
	Capability    float64 `json:"capability"`
	DeepReasoning float64 `json:"deepReasoning"`
	Sensitive     float64 `json:"sensitive"`
	// Source is "jev" (classifications come only from Jev).
	Source string `json:"source"`
	// Cost is what the classification call cost in USD.
	Cost float64 `json:"cost,omitempty"`
}

// Demand blends the signals into one 0..1 number the tier policy compares
// against tier capability.
func (c Classification) Demand() float64 {
	return clamp01(0.5*c.Complexity + 0.4*c.Capability + 0.15*c.DeepReasoning)
}

// jevClient calls a Jev-compatible decisions endpoint. Each call gets a
// short per-attempt timeout and one retry; repeated failures open a circuit
// breaker so later turns fail fast instead of each paying the timeout.
type jevClient struct {
	cfg    JevConfig
	apiKey func() string
	http   *http.Client
	now    func() time.Time

	mu        sync.Mutex
	openUntil time.Time
	lastModel string
}

// Breaker durations: a timeout or server error rests Jev briefly; an
// authentication or payment error rests it longer.
const (
	jevBreakerShort = 30 * time.Second
	jevBreakerAuth  = 5 * time.Minute
)

func newJevClient(cfg JevConfig, apiKey func() string) *jevClient {
	cfg.Endpoint = NormalizeJevEndpoint(cfg.Endpoint)
	cfg.Model = requestModel(cfg.Endpoint, cfg.Model)
	return &jevClient{cfg: cfg, apiKey: apiKey, now: time.Now, http: &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}}
}

func (c *jevClient) attemptTimeout() time.Duration {
	if c.cfg.TimeoutMs > 0 {
		return time.Duration(c.cfg.TimeoutMs) * time.Millisecond
	}
	return 1300 * time.Millisecond
}

// errJevOpen reports that the breaker is open.
var errJevOpen = errors.New("router: jev unavailable (circuit breaker open)")

// breakerRemaining is how long the breaker stays open, or zero.
func (c *jevClient) breakerRemaining() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return max(0, c.openUntil.Sub(c.now()))
}

// httpStatusError carries a non-200 status.
type httpStatusError struct {
	status int
	body   string
}

func (e *httpStatusError) Error() string {
	switch e.status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Sprintf("router: Jev rejected the API key (HTTP %d)", e.status)
	case http.StatusPaymentRequired:
		return "router: Jev needs credits on this account (HTTP 402)"
	case http.StatusTooManyRequests:
		return "router: Jev rate limit reached (HTTP 429)"
	case 529:
		return "router: Jev is overloaded (HTTP 529)"
	}
	if msg := e.message(); msg != "" {
		return fmt.Sprintf("router: Jev HTTP %d: %s", e.status, text.Clip(msg, 300))
	}
	return fmt.Sprintf("router: Jev HTTP %d", e.status)
}

// message is the error body's message field, else the body itself.
func (e *httpStatusError) message() string {
	var parsed struct {
		Error any `json:"error"`
	}
	if json.Unmarshal([]byte(e.body), &parsed) == nil {
		switch v := parsed.Error.(type) {
		case string:
			return v
		case map[string]any:
			if m, ok := v["message"].(string); ok {
				return m
			}
		}
	}
	return strings.TrimSpace(e.body)
}

// isAuthError reports whether err is Jev refusing the key.
func isAuthError(err error) bool {
	var status *httpStatusError
	return errors.As(err, &status) && (status.status == http.StatusUnauthorized || status.status == http.StatusForbidden)
}

// post sends req with retry and breaker handling.
func (c *jevClient) post(ctx context.Context, req jevRequest) (jevResponse, error) {
	if c.breakerRemaining() > 0 {
		return jevResponse{}, errJevOpen
	}
	key := strings.TrimSpace(c.apiKey())
	if key == "" && namedKeyProvider(c.cfg) {
		return jevResponse{}, errNoJevKey
	}
	body, err := json.Marshal(req)
	if err != nil {
		return jevResponse{}, err
	}
	retries := 1
	if c.cfg.Retries != nil {
		retries = max(0, *c.cfg.Retries)
	}
	var last error
	for attempt := 0; attempt <= retries; attempt++ {
		resp, err := c.once(ctx, body, key)
		if err == nil {
			c.mu.Lock()
			c.lastModel = resp.Model
			c.mu.Unlock()
			return resp, nil
		}
		last = err
		var status *httpStatusError
		if errors.As(err, &status) && (status.status == 401 || status.status == 402 || status.status == 403) {
			c.open(jevBreakerAuth)
			return jevResponse{}, err
		}
		if ctx.Err() != nil {
			break
		}
	}
	c.open(jevBreakerShort)
	return jevResponse{}, last
}

func (c *jevClient) open(d time.Duration) {
	c.mu.Lock()
	c.openUntil = c.now().Add(d)
	c.mu.Unlock()
}

func (c *jevClient) once(ctx context.Context, body []byte, key string) (jevResponse, error) {
	actx, cancel := context.WithTimeout(ctx, c.attemptTimeout())
	defer cancel()
	httpReq, err := http.NewRequestWithContext(actx, http.MethodPost, c.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return jevResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}
	httpReq.Header.Set("HTTP-Referer", "https://github.com/alexrudloff/wopr")
	httpReq.Header.Set("X-Title", "wopr")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return jevResponse{}, fmt.Errorf("router: jev request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return jevResponse{}, fmt.Errorf("router: jev read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return jevResponse{}, &httpStatusError{status: resp.StatusCode, body: string(raw)}
	}
	var parsed jevResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return jevResponse{}, fmt.Errorf("router: jev decode: %w", err)
	}
	if parsed.Error != nil {
		return jevResponse{}, fmt.Errorf("router: jev: %s", parsed.Error.Message)
	}
	return parsed, nil
}

type jevQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria"`
}

type jevRequest struct {
	Model     string                 `json:"model"`
	State     map[string]any         `json:"state"`
	Questions map[string]jevQuestion `json:"questions"`
}

type jevAnswer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

type jevResponse struct {
	Model   string               `json:"model"`
	Answers map[string]jevAnswer `json:"answers"`
	Usage   struct {
		InputTokens  int     `json:"input_tokens"`
		OutputTokens int     `json:"output_tokens"`
		Cost         float64 `json:"cost"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// errNoJevKey reports that no API key is available for the endpoint.
var errNoJevKey = errors.New("router: no API key for the Jev endpoint")

// classify sends the prompt to Jev. contextLabel describes the conversation
// size in words, since Jev does not count.
func (c *jevClient) classify(ctx context.Context, prompt, contextLabel string) (Classification, error) {
	prompt = headTail(prompt, c.cfg.MaxPromptChars)
	state := map[string]any{
		"prompt":              prompt,
		"conversation_so_far": contextLabel,
		"setting":             "A user is talking to a coding agent that can read and edit files and run shell commands in their project.",
	}
	criteria := maps.Clone(kindCriteria)
	req := jevRequest{
		Model: c.cfg.Model,
		State: state,
		Questions: map[string]jevQuestion{
			qKind:    {Type: "choice", Instructions: "What kind of task is the prompt asking the coding agent to do?", Criteria: criteria},
			qComplex: {Type: "score", Instructions: "How complex is the work the prompt asks for?", Criteria: complexityLevels},
			qCapable: {Type: "score", Instructions: "Ignoring price, how capable a model does this prompt deserve to be answered well?", Criteria: capabilityLevels},
			qDeep: {Type: "noul", Instructions: "Does answering well require careful multi-step reasoning rather than recall or a quick edit?", Criteria: map[string]string{
				"true":  "The prompt needs planning, tradeoff analysis, or reasoning across several parts of a system.",
				"false": "The prompt is a lookup, a small edit, a command, or conversation.",
			}},
			qSensitive: {Type: "noul", Instructions: "Does the prompt contain secrets, credentials, personal data, or private business information?", Criteria: map[string]string{
				"true":  "API keys, passwords, tokens, personal identifiers, financial or medical details, or confidential documents appear in the text.",
				"false": "Only ordinary code, commands, and technical discussion appear.",
			}},
		},
	}
	parsed, err := c.post(ctx, req)
	if err != nil {
		return Classification{}, err
	}
	return classificationFromAnswers(parsed)
}

// headTail bounds text to limit bytes, keeping the head and tail: intent is
// usually at the start, and pasted errors at the end.
func headTail(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit*2/3] + "\n…\n" + text[len(text)-limit/3:]
}

func classificationFromAnswers(resp jevResponse) (Classification, error) {
	out := Classification{Source: "jev", Cost: resp.Usage.Cost, Kind: KindImplement}
	kind, ok := resp.Answers[qKind]
	if !ok {
		return out, errors.New("router: jev answer missing task kind")
	}
	if kind.Choice != "" {
		out.Kind = Kind(kind.Choice)
		out.KindConfidence = kind.Confidence
	}
	if a, ok := resp.Answers[qComplex]; ok && a.Score != nil {
		out.Complexity = scaleScore(*a.Score, len(complexityLevels))
	}
	if a, ok := resp.Answers[qCapable]; ok && a.Score != nil {
		out.Capability = scaleScore(*a.Score, len(capabilityLevels))
	}
	if a, ok := resp.Answers[qDeep]; ok && a.Noul != nil {
		out.DeepReasoning = *a.Noul
	}
	if a, ok := resp.Answers[qSensitive]; ok && a.Noul != nil {
		out.Sensitive = *a.Noul
	}
	return out, nil
}

// scaleScore maps a probability-weighted level index onto 0..1.
func scaleScore(score float64, levels int) float64 {
	if levels <= 1 {
		return 0
	}
	return clamp01(score / float64(levels-1))
}

// TestJev classifies one sample prompt with cfg, for setup to confirm an
// endpoint works before saving it. apiKey may be empty.
func TestJev(ctx context.Context, cfg JevConfig, apiKey string) (Classification, error) {
	if cfg.Endpoint == "" || cfg.Model == "" {
		return Classification{}, errors.New("router: a Jev endpoint and model are both needed")
	}
	if cfg.TimeoutMs <= 0 {
		// A cold endpoint may take longer than a warm turn's budget.
		cfg.TimeoutMs = 10000
	}
	if cfg.MaxPromptChars <= 0 {
		cfg.MaxPromptChars = DefaultConfig().Jev.MaxPromptChars
	}
	retries := 0
	cfg.Retries = &retries
	client := newJevClient(cfg, func() string { return apiKey })
	return client.classify(ctx, "Fix the failing test in parser_test.go", "new conversation")
}

func clamp01(v float64) float64 { return min(max(v, 0), 1) }
