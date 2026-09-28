package codingagent

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/router"
)

// Setup measures each model it adds with a handful of tiny requests. An
// OpenAI-compatible endpoint is probed directly over HTTP, so every quirk
// (a rejected reasoning_effort, a server that answers all at once) is seen
// as the server reports it; a catalog model is timed with one request
// through its provider.

// setupRequestTimeout bounds one probe request; a slow local model thinking
// at its default level can take a while even for a one-word reply.
const setupRequestTimeout = 90 * time.Second

// setupEndpoint is an OpenAI-compatible server being added.
type setupEndpoint struct {
	// ID is the models.json provider id; Name its display name.
	ID, Name string
	// BaseURL ends in the API root, usually /v1.
	BaseURL string
	// APIKey is sent as a bearer token when set.
	APIKey string
	// Existing reports that models.json already has this provider.
	Existing bool
}

// remoteModel is one entry of an endpoint's model list.
type remoteModel struct {
	ID, Name  string
	Context   int
	MaxOutput int
}

// modelMeasure is what setup learned about one model by calling it.
type modelMeasure struct {
	Err string
	// Tools reports a tool call came back; nil means untested.
	Tools *bool
	// Streaming reports the reply arrived in pieces; nil means untested.
	Streaming *bool
	// UsageRefused reports the server refused a request for usage in the
	// stream, so wopr must not ask for it.
	UsageRefused bool
	// Effort reports the server validates reasoning_effort; Accepted lists
	// the values it took and LevelMap maps the rest.
	Effort   bool
	Accepted []string
	LevelMap ai.ThinkingLevelMap
	// Reasoning reports the model returned reasoning text.
	Reasoning bool
	// TTFT is the time to first token of a short prompt; PerK the added
	// seconds per 1K prompt tokens; TPS the decode rate.
	TTFT time.Duration
	PerK float64
	TPS  float64
}

// ok reports whether the model answered at all.
func (m *modelMeasure) ok() bool { return m != nil && m.Err == "" }

// setupHTTP is the probe client; keep-alives stay off so no pooled
// connection outlives setup.
var setupHTTP = &http.Client{Transport: &http.Transport{DisableKeepAlives: true, Proxy: http.ProxyFromEnvironment}}

// normalizeBaseURL trims the base URL and adds a scheme when missing.
func normalizeBaseURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw != "" && !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	return strings.TrimSuffix(raw, "/models")
}

// endpointRequest sends one JSON request to the endpoint.
func endpointRequest(ctx context.Context, ep setupEndpoint, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, ep.BaseURL+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if ep.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+ep.APIKey)
	}
	return setupHTTP.Do(req)
}

// httpError is a non-2xx answer with the server's message.
type httpError struct {
	status  int
	message string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.status, e.message)
}

// readError turns a failed response into an httpError with the server's
// own message when it sent one.
func readError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	message := strings.TrimSpace(string(data))
	var parsed struct {
		Error any `json:"error"`
	}
	if json.Unmarshal(data, &parsed) == nil {
		switch e := parsed.Error.(type) {
		case string:
			message = e
		case map[string]any:
			if text, ok := e["message"].(string); ok {
				message = text
			}
		}
	}
	if len(message) > 300 {
		message = message[:297] + "..."
	}
	return &httpError{status: resp.StatusCode, message: message}
}

// listEndpointModels fetches the endpoint's model list. A base URL without
// /v1 that 404s is retried with it, and the working root is returned.
func listEndpointModels(ctx context.Context, ep setupEndpoint) ([]remoteModel, string, error) {
	// A root without /v1 (http://host:8000) usually means the OpenAI API
	// under /v1; some gateways answer at both, and /v1 is the one every
	// server has.
	if !strings.HasSuffix(ep.BaseURL, "/v1") {
		withV1 := ep
		withV1.BaseURL += "/v1"
		if models, err := fetchModelList(ctx, withV1); err == nil {
			return models, withV1.BaseURL, nil
		}
	}
	models, err := fetchModelList(ctx, ep)
	return models, ep.BaseURL, err
}

func fetchModelList(ctx context.Context, ep setupEndpoint) ([]remoteModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := endpointRequest(ctx, ep, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, readError(resp)
	}
	var list struct {
		Data []map[string]any `json:"data"`
		// Ollama's native shape.
		Models []map[string]any `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&list); err != nil {
		return nil, fmt.Errorf("not an OpenAI-compatible model list: %w", err)
	}
	var out []remoteModel
	for _, entry := range append(list.Data, list.Models...) {
		id, _ := entry["id"].(string)
		if id == "" {
			id, _ = entry["name"].(string)
		}
		if id == "" {
			continue
		}
		name, _ := entry["name"].(string)
		model := remoteModel{ID: id, Name: name}
		// Servers report the served window under different keys.
		for _, key := range []string{"context_length", "context_window", "max_model_len", "max_context_length"} {
			if n := jsonInt(entry[key]); n > 0 {
				model.Context = n
				break
			}
		}
		if top, ok := entry["top_provider"].(map[string]any); ok {
			model.Context = cmp.Or(model.Context, jsonInt(top["context_length"]))
			model.MaxOutput = jsonInt(top["max_completion_tokens"])
		}
		out = append(out, model)
	}
	if len(out) == 0 {
		return nil, errors.New("the server listed no models")
	}
	return out, nil
}

func jsonInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case string:
		i, _ := strconv.Atoi(n)
		return i
	}
	return 0
}

// chatResult is the part of a chat completion the probes read.
type chatResult struct {
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []any  `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}

// chat sends a non-streaming chat completion and decodes the reply.
func chat(ctx context.Context, ep setupEndpoint, body map[string]any) (chatResult, error) {
	ctx, cancel := context.WithTimeout(ctx, setupRequestTimeout)
	defer cancel()
	var out chatResult
	resp, err := endpointRequest(ctx, ep, http.MethodPost, "/chat/completions", body)
	if err != nil {
		return out, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return out, readError(resp)
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out)
	return out, err
}

// rejected reports a 4xx answer: the server refused the request's shape.
func rejected(err error) bool {
	var status *httpError
	return errors.As(err, &status) && status.status >= 400 && status.status < 500
}

// probeLevels are the reasoning_effort values tried, in wopr's order; "none"
// is how a thinking-off request is sent.
var probeLevels = []string{"none", "minimal", "low", "medium", "high", "xhigh"}

// probeEndpointModel measures one endpoint model. step reports progress in
// a few words.
func probeEndpointModel(ctx context.Context, ep setupEndpoint, model string, step func(string)) modelMeasure {
	var m modelMeasure
	user := func(text string) []map[string]any { return []map[string]any{{"role": "user", "content": text}} }
	request := func(extra map[string]any) map[string]any {
		body := map[string]any{"model": model, "messages": user("Reply with the word OK."), "max_tokens": 1}
		maps.Copy(body, extra)
		return body
	}

	step("connecting")
	if _, err := chat(ctx, ep, request(nil)); err != nil {
		m.Err = err.Error()
		return m
	}

	// A server that validates reasoning_effort refuses a made-up value;
	// one that ignores the field takes it, and its levels can't be probed.
	step("probing thinking levels")
	if _, err := chat(ctx, ep, request(map[string]any{"reasoning_effort": "no-such-level"})); rejected(err) {
		for _, level := range probeLevels {
			if ctx.Err() != nil {
				break
			}
			if _, err := chat(ctx, ep, request(map[string]any{"reasoning_effort": level})); err == nil {
				m.Accepted = append(m.Accepted, level)
			}
		}
		m.Effort = slices.ContainsFunc(m.Accepted, func(l string) bool { return l != "none" })
		m.LevelMap = thinkingLevelMapFor(m.Accepted)
	}
	// Later probes ask for as little thinking as the server allows.
	quiet := map[string]any{}
	switch {
	case slices.Contains(m.Accepted, "none"):
		quiet["reasoning_effort"] = "none"
	case len(m.Accepted) > 0:
		quiet["reasoning_effort"] = m.Accepted[0]
	}

	step("testing tool calls")
	toolBody := request(quiet)
	toolBody["messages"] = user("What is the weather in Paris? Use the get_weather tool.")
	toolBody["max_tokens"] = 400
	toolBody["tools"] = []map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name":        "get_weather",
			"description": "Get the current weather for a city.",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"city": map[string]any{"type": "string"}},
				"required":   []string{"city"},
			},
		},
	}}
	reply, err := chat(ctx, ep, toolBody)
	tools := err == nil && len(reply.Choices) > 0 && len(reply.Choices[0].Message.ToolCalls) > 0
	m.Tools = &tools
	if err == nil && len(reply.Choices) > 0 {
		msg := reply.Choices[0].Message
		m.Reasoning = msg.ReasoningContent != "" || msg.Reasoning != ""
	}

	step("timing a short reply")
	short, err := streamProbe(ctx, ep, model, "Count from one to twenty in words, separated by spaces.", 64, quiet)
	if err != nil {
		m.Err = "streaming: " + err.Error()
		return m
	}
	m.TTFT, m.TPS, m.UsageRefused = short.ttft, short.tps, short.usageRefused
	streaming := short.chunks > 2
	m.Streaming = &streaming
	m.Reasoning = m.Reasoning || short.reasoning

	// A long uncached prompt measures prefill: the nonce defeats a prefix
	// cache left by an earlier run.
	step("timing a long prompt")
	long := fmt.Sprintf("Probe %d. ", time.Now().UnixNano()) + strings.Repeat("The quick brown fox jumps over the lazy dog. ", 180) + "Reply with OK."
	if timed, err := streamProbe(ctx, ep, model, long, 1, quiet); err == nil && timed.promptTokens > 0 {
		k := float64(timed.promptTokens) / 1000
		m.PerK = max(0, (timed.ttft-short.ttft).Seconds()) / k
	}
	return m
}

// thinkingLevelMapFor maps the levels a server refused: minimal is dropped,
// and low, medium, or high go to the nearest accepted level above (else
// below), as with a server that takes xhigh but not high. Off maps to null
// when "none" is refused, so thinking-off omits the field.
func thinkingLevelMapFor(accepted []string) ai.ThinkingLevelMap {
	efforts := slices.DeleteFunc(slices.Clone(accepted), func(l string) bool { return l == "none" })
	if len(efforts) == 0 {
		return nil
	}
	out := ai.ThinkingLevelMap{}
	if !slices.Contains(accepted, "none") {
		out[ai.ThinkingOff] = nil
	}
	levels := probeLevels[1:5] // minimal, low, medium, high
	for i, level := range levels {
		if slices.Contains(accepted, level) {
			continue
		}
		if level == "minimal" {
			out[ai.ThinkingLevel(level)] = nil
			continue
		}
		var target *string
		for _, above := range probeLevels[i+2:] {
			if slices.Contains(accepted, above) {
				target = &above
				break
			}
		}
		if target == nil {
			for j := i; j >= 1; j-- {
				if below := probeLevels[j]; slices.Contains(accepted, below) {
					target = &below
					break
				}
			}
		}
		out[ai.ThinkingLevel(level)] = target
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// streamTiming is one streamed reply's timing.
type streamTiming struct {
	ttft         time.Duration
	tps          float64
	chunks       int
	promptTokens int
	// usageRefused reports the server refused stream_options.
	usageRefused bool
	reasoning    bool
}

// streamProbe streams one reply and times it. Usage reporting is asked for
// and dropped if the server refuses it.
func streamProbe(ctx context.Context, ep setupEndpoint, model, prompt string, maxTokens int, extra map[string]any) (streamTiming, error) {
	body := map[string]any{
		"model":          model,
		"messages":       []map[string]any{{"role": "user", "content": prompt}},
		"max_tokens":     maxTokens,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	maps.Copy(body, extra)
	timing, err := streamOnce(ctx, ep, body)
	if rejected(err) {
		delete(body, "stream_options")
		timing, err = streamOnce(ctx, ep, body)
		timing.usageRefused = err == nil
	}
	if err == nil && timing.promptTokens == 0 {
		// No usage: estimate four characters a token.
		timing.promptTokens = len(prompt) / 4
	}
	return timing, err
}

func streamOnce(ctx context.Context, ep setupEndpoint, body map[string]any) (streamTiming, error) {
	ctx, cancel := context.WithTimeout(ctx, setupRequestTimeout)
	defer cancel()
	var t streamTiming
	start := time.Now()
	resp, err := endpointRequest(ctx, ep, http.MethodPost, "/chat/completions", body)
	if err != nil {
		return t, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return t, readError(resp)
	}
	var first, last time.Time
	completion := 0
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string `json:"reasoning"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		if chunk.Usage != nil && (chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0) {
			t.promptTokens, completion = chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens
		}
		for _, choice := range chunk.Choices {
			delta := choice.Delta
			if delta.Content == "" && delta.ReasoningContent == "" && delta.Reasoning == "" {
				continue
			}
			t.reasoning = t.reasoning || delta.ReasoningContent != "" || delta.Reasoning != ""
			now := time.Now()
			if first.IsZero() {
				first = now
			}
			last = now
			t.chunks++
		}
	}
	if err := scanner.Err(); err != nil {
		return t, err
	}
	if first.IsZero() {
		// Nothing streamed (a one-token reply may carry no text): the whole
		// request stands in for the first token.
		first, last = time.Now(), time.Now()
	}
	t.ttft = first.Sub(start)
	if completion == 0 {
		completion = t.chunks
	}
	// A reply that arrived in one burst is timed over the whole request.
	if span := generationTime(start, first, last).Seconds(); span > 0 && completion > 1 {
		t.tps = float64(completion) / span
	}
	return t, nil
}

// probeCatalogModel times one tiny request to a signed-in or API-key model
// through its provider: the time to first token and the decode rate.
func probeCatalogModel(ctx context.Context, model *ai.Model) modelMeasure {
	var m modelMeasure
	ctx, cancel := context.WithTimeout(ctx, setupRequestTimeout)
	defer cancel()
	transcript := ai.NormalizeContext(ai.Context{Messages: []ai.Message{
		ai.UserMessage{Content: ai.UserText("Reply with the word OK."), Timestamp: time.Now().UnixMilli()},
	}})
	start := time.Now()
	stream, err := model.Provider.Stream(ctx, transcript, ai.StreamOptions{MaxTokens: 16, Thinking: ai.ThinkingOff, IsReasoning: model.Capabilities.MaxThinking != ""})
	if err != nil {
		m.Err = err.Error()
		return m
	}
	var first, last time.Time
	chunks := 0
	for event := range stream.Events(ctx) {
		switch e := event.(type) {
		case ai.TextDeltaEvent, ai.ThinkingDeltaEvent:
			now := time.Now()
			if first.IsZero() {
				first = now
			}
			last = now
			chunks++
		case ai.ErrorEvent:
			if e.Error != nil {
				m.Err = cmp.Or(e.Error.ErrorMessage, "request failed")
			} else {
				m.Err = "request failed"
			}
			return m
		}
	}
	if first.IsZero() {
		first, last = time.Now(), time.Now()
	}
	m.TTFT = first.Sub(start)
	if result := stream.Result(); result != nil && result.Usage.Output > 1 {
		// A reply that arrived in one burst is timed over the whole request.
		if span := generationTime(start, first, last).Seconds(); span > 0 {
			m.TPS = float64(result.Usage.Output) / span
		}
	}
	streaming := chunks > 2
	m.Streaming = &streaming
	return m
}

// speedStat seeds the router's learned speed from a measurement.
func speedStat(m *modelMeasure) (router.SpeedStat, bool) {
	if !m.ok() || m.TTFT <= 0 {
		return router.SpeedStat{}, false
	}
	return router.SpeedStat{BaseSeconds: m.TTFT.Seconds(), PerKSeconds: m.PerK, Samples: 1, OutputTokensPerSecond: m.TPS}, true
}

// inferTier guesses a cost class from how a model is reached: an endpoint
// on this machine is free-local, one on a private or tailnet address is
// free-remote, and a public endpoint or API key is paid.
func inferTier(kind setupKind, baseURL, provider string) string {
	switch kind {
	case setupViaSubscription:
		return router.CostSubscription
	case setupViaAPIKey:
		if provider == "kimi-coding" {
			return router.CostSubscription
		}
		return router.CostPaid
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return router.CostPaid
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return router.CostFreeLocal
	}
	ip := net.ParseIP(host)
	switch {
	case ip == nil:
		if strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".lan") || strings.HasSuffix(host, ".ts.net") || strings.HasSuffix(host, ".internal") || !strings.Contains(host, ".") {
			return router.CostFreeRemote
		}
		return router.CostPaid
	case ip.IsLoopback():
		return router.CostFreeLocal
	case ip.IsPrivate() || ip.IsLinkLocalUnicast() || tailnet.Contains(ip):
		return router.CostFreeRemote
	}
	return router.CostPaid
}

// tailnet is Tailscale's CGNAT range, where a user's own machines live.
var tailnet = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}
