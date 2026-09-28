// Package llama is the built-in llama.cpp integration: the router HTTP/SSE
// client, Hugging Face search and download, the dynamic provider, the manager
// UI, and the /llama command.
package llama

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// LlamaModelStatus is a llama.cpp router model status.
type LlamaModelStatus string

const (
	LlamaModelStatusUnloaded    LlamaModelStatus = "unloaded"
	LlamaModelStatusLoading     LlamaModelStatus = "loading"
	LlamaModelStatusLoaded      LlamaModelStatus = "loaded"
	LlamaModelStatusDownloading LlamaModelStatus = "downloading"
	LlamaModelStatusSleeping    LlamaModelStatus = "sleeping"
)

// LlamaModelInfoStatus is a catalog entry's status. Progress stays raw
// because only the sum of its entries is used.
type LlamaModelInfoStatus struct {
	Value    LlamaModelStatus `json:"value"`
	Args     []string         `json:"args"`
	Failed   bool             `json:"failed"`
	ExitCode *float64         `json:"exit_code"`
	Progress json.RawMessage  `json:"progress"`
}

// LlamaModelArchitecture is a catalog entry's architecture.
type LlamaModelArchitecture struct {
	InputModalities  []string `json:"input_modalities"`
	OutputModalities []string `json:"output_modalities"`
}

// LlamaModelMeta is a catalog entry's metadata. Pointers keep an absent value
// distinct from zero so readers can fall back.
type LlamaModelMeta struct {
	NCtx      *float64 `json:"n_ctx"`
	NCtxTrain *float64 `json:"n_ctx_train"`
	Size      *float64 `json:"size"`
	Ftype     string   `json:"ftype"`
}

// LlamaModelInfo is one entry of the router's /models catalog.
type LlamaModelInfo struct {
	ID           string                  `json:"id"`
	Aliases      []string                `json:"aliases"`
	Status       LlamaModelInfoStatus    `json:"status"`
	Architecture *LlamaModelArchitecture `json:"architecture"`
	Source       string                  `json:"source"`
	Meta         *LlamaModelMeta         `json:"meta"`
}

// LlamaServerProps holds the /props fields wopr reads.
type LlamaServerProps struct {
	ModelsAutoload bool   `json:"models_autoload"`
	ChatTemplate   string `json:"chat_template"`
}

// LlamaModelEvent is one /models/sse event.
type LlamaModelEvent struct {
	Model string          `json:"model"`
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// LlamaProgress is a load or download progress update. Ratio is nil when the
// progress is unknown. An update replaces the shown ratio and detail only when
// hasRatio / hasDetail are set.
type LlamaProgress struct {
	Message   string
	Ratio     *float64
	Detail    string
	hasRatio  bool
	hasDetail bool
}

// errorMessage returns the {"error":{"message"}} text of a llama.cpp error
// body, or fallback.
func errorMessage(body []byte, fallback string) string {
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Error.Message != "" {
		return payload.Error.Message
	}
	return fallback
}

func parseLoadProgress(data json.RawMessage) (LlamaProgress, bool) {
	var payload struct {
		Progress *struct {
			Current *string  `json:"current"`
			Stage   string   `json:"stage"`
			Stages  []string `json:"stages"`
			Value   *float64 `json:"value"`
		} `json:"progress"`
	}
	if json.Unmarshal(data, &payload) != nil || payload.Progress == nil {
		return LlamaProgress{}, false
	}
	progress := payload.Progress
	stage := progress.Stage
	if progress.Current != nil {
		stage = *progress.Current
	}
	var ratio *float64
	if progress.Value != nil {
		clamped := max(0, min(1, *progress.Value))
		ratio = &clamped
	}
	if stage != "" && len(progress.Stages) > 0 {
		if index := slices.Index(progress.Stages, stage); index >= 0 {
			stageRatio := 0.0
			if ratio != nil {
				stageRatio = *ratio
			}
			overall := (float64(index) + stageRatio) / float64(len(progress.Stages))
			ratio = &overall
		}
	}
	message := "Loading model"
	if stage != "" {
		message = "Loading " + strings.ReplaceAll(stage, "_", " ")
	}
	return LlamaProgress{Message: message, Ratio: ratio, hasRatio: true}, true
}

// downloadFiles returns the per-file entries of a download progress payload:
// the values of data.progress when it is an object or array, else of data.
func downloadFiles(data json.RawMessage) ([]json.RawMessage, bool) {
	var byName map[string]json.RawMessage
	if json.Unmarshal(data, &byName) == nil && byName != nil {
		if nested, ok := jsonValues(byName["progress"]); ok {
			return nested, true
		}
		return slices.Collect(maps.Values(byName)), true
	}
	return jsonValues(data)
}

// jsonValues returns the values of a JSON object or the items of an array.
func jsonValues(data json.RawMessage) ([]json.RawMessage, bool) {
	var byName map[string]json.RawMessage
	if json.Unmarshal(data, &byName) == nil && byName != nil {
		return slices.Collect(maps.Values(byName)), true
	}
	var list []json.RawMessage
	if json.Unmarshal(data, &list) == nil && list != nil {
		return list, true
	}
	return nil, false
}

func parseDownloadProgress(data json.RawMessage) (LlamaProgress, bool) {
	files, ok := downloadFiles(data)
	if !ok {
		return LlamaProgress{}, false
	}
	done, total := 0.0, 0.0
	for _, file := range files {
		var entry struct {
			Done  *float64 `json:"done"`
			Total *float64 `json:"total"`
		}
		if json.Unmarshal(file, &entry) != nil || entry.Done == nil || entry.Total == nil {
			continue
		}
		done += *entry.Done
		total += *entry.Total
	}
	if total <= 0 {
		return LlamaProgress{}, false
	}
	ratio := done / total
	return LlamaProgress{
		Message:   "Downloading model",
		Ratio:     &ratio,
		Detail:    FormatBytes(done) + " / " + FormatBytes(total),
		hasRatio:  true,
		hasDetail: true,
	}, true
}

// FormatBytes formats a byte count for display.
func FormatBytes(bytes float64) string {
	if bytes < 1024 {
		return formatNumber(bytes) + " B"
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	value := bytes / 1024
	unit := units[0]
	for index := 1; index < len(units) && value >= 1024; index++ {
		value /= 1024
		unit = units[index]
	}
	if value >= 10 {
		return strconv.FormatFloat(value, 'f', 1, 64) + " " + unit
	}
	return strconv.FormatFloat(value, 'f', 2, 64) + " " + unit
}

// formatNumber prints value with the fewest digits that round-trip.
func formatNumber(value float64) string { return strconv.FormatFloat(value, 'f', -1, 64) }

// NormalizeLlamaServerURL validates an http(s) server URL and canonicalizes
// it: default port, trailing slashes, and a trailing /v1 are removed.
func NormalizeLlamaServerURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" {
		return "", errors.New("Invalid URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("Server URL must use http or https")
	}
	if parsed.Host == "" {
		return "", errors.New("Invalid URL")
	}
	host := strings.ToLower(parsed.Host)
	if port := parsed.Port(); (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		host = strings.TrimSuffix(host, ":"+port)
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	path = strings.TrimSuffix(path, "/v1")
	userinfo := ""
	if parsed.User != nil {
		userinfo = parsed.User.String() + "@"
	}
	return strings.TrimSuffix(scheme+"://"+userinfo+host+path, "/"), nil
}

// LlamaInferenceURL returns the server's OpenAI-compatible /v1 base URL.
func LlamaInferenceURL(serverURL string) (string, error) {
	normalized, err := NormalizeLlamaServerURL(serverURL)
	if err != nil {
		return "", err
	}
	return normalized + "/v1", nil
}

// LlamaClient talks to a llama.cpp router server.
type LlamaClient struct {
	ServerURL string
	apiKey    string
}

// NewLlamaClient normalizes the server URL and returns a client, or an error
// for an invalid URL.
func NewLlamaClient(serverURL, apiKey string) (*LlamaClient, error) {
	normalized, err := NormalizeLlamaServerURL(serverURL)
	if err != nil {
		return nil, err
	}
	return &LlamaClient{ServerURL: normalized, apiKey: apiKey}, nil
}

func (c *LlamaClient) headers() http.Header {
	headers := http.Header{}
	if c.apiKey != "" {
		headers.Set("Authorization", "Bearer "+c.apiKey)
	}
	return headers
}

func (c *LlamaClient) request(ctx context.Context, method, path string, body any) ([]byte, error) {
	headers := c.headers()
	var encoded []byte
	if body != nil {
		var err error
		if encoded, err = json.Marshal(body); err != nil {
			return nil, err
		}
		headers.Set("Content-Type", "application/json")
	}
	response, err := fetch(ctx, method, c.ServerURL+path, headers, encoded)
	if err != nil {
		return nil, err
	}
	if !response.ok() {
		return nil, errors.New(errorMessage(response.body, fmt.Sprintf("llama.cpp returned HTTP %d", response.status)))
	}
	return response.body, nil
}

// List returns the /models catalog, asking the server to rescan when reload is set.
func (c *LlamaClient) List(ctx context.Context, reload bool) ([]LlamaModelInfo, error) {
	path := "/models"
	if reload {
		path += "?reload=1"
	}
	body, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var catalog struct {
		Data []json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &catalog) != nil || catalog.Data == nil {
		return nil, errors.New("llama.cpp returned an invalid model catalog")
	}
	models := make([]LlamaModelInfo, 0, len(catalog.Data))
	for _, entry := range catalog.Data {
		var model LlamaModelInfo
		if json.Unmarshal(entry, &model) != nil || model.ID == "" || model.Status.Value == "" {
			return nil, errors.New("Server is not running in llama.cpp router mode")
		}
		models = append(models, model)
	}
	return models, nil
}

// Props reads /props, for model when given, without autoloading it.
func (c *LlamaClient) Props(ctx context.Context, model string) (LlamaServerProps, error) {
	query := ""
	if model != "" {
		query = "?model=" + url.QueryEscape(model) + "&autoload=false"
	}
	body, err := c.request(ctx, http.MethodGet, "/props"+query, nil)
	if err != nil {
		return LlamaServerProps{}, err
	}
	var props LlamaServerProps
	_ = json.Unmarshal(body, &props)
	return props, nil
}

// Load asks the router to load model.
func (c *LlamaClient) Load(ctx context.Context, model string) error {
	_, err := c.request(ctx, http.MethodPost, "/models/load", map[string]string{"model": model})
	return err
}

// Unload asks the router to unload model.
func (c *LlamaClient) Unload(ctx context.Context, model string) error {
	_, err := c.request(ctx, http.MethodPost, "/models/unload", map[string]string{"model": model})
	return err
}

// UnloadAndWait unloads model and polls until it is unloaded or gone.
func (c *LlamaClient) UnloadAndWait(ctx context.Context, model string) error {
	if err := c.Unload(ctx, model); err != nil {
		return err
	}
	for {
		models, err := c.List(ctx, false)
		if err != nil {
			return err
		}
		entry := findModel(models, model)
		if entry == nil || entry.Status.Value == LlamaModelStatusUnloaded {
			return nil
		}
		if err := sleep(ctx, 100*time.Millisecond); err != nil {
			return err
		}
	}
}

// Download asks the router to download model.
func (c *LlamaClient) Download(ctx context.Context, model string) error {
	_, err := c.request(ctx, http.MethodPost, "/models", map[string]string{"model": model})
	return err
}

func findModel(models []LlamaModelInfo, id string) *LlamaModelInfo {
	for index := range models {
		if models[index].ID == id {
			return &models[index]
		}
	}
	return nil
}

func (c *LlamaClient) watch(ctx context.Context, onEvent func(LlamaModelEvent), onSent func()) error {
	response, err := openStream(ctx, c.ServerURL+"/models/sse", c.headers(), onSent)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("llama.cpp SSE returned HTTP %d", response.StatusCode)
	}
	if err := readEvents(response.Body, onEvent); err != nil && ctx.Err() != nil {
		return context.Cause(ctx)
	} else if err != nil {
		return err
	}
	return nil
}

// readEvents dispatches each SSE frame's joined data lines as a
// LlamaModelEvent. Malformed events are skipped; catalog polling remains
// authoritative.
func readEvents(body io.Reader, onEvent func(LlamaModelEvent)) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var data []string
	first := true
	for scanner.Scan() {
		line := scanner.Text()
		if first {
			line = strings.TrimPrefix(line, "\uFEFF")
			first = false
		}
		if rest, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimLeftFunc(rest, unicode.IsSpace))
			continue
		}
		if line != "" {
			continue
		}
		var event LlamaModelEvent
		if len(data) > 0 && json.Unmarshal([]byte(strings.Join(data, "\n")), &event) == nil && event.Model != "" && event.Event != "" {
			onEvent(event)
		}
		data = data[:0]
	}
	return scanner.Err()
}

// progressSink serializes progress callbacks and event state shared by the
// watcher goroutine and the polling loop.
type progressSink struct {
	mu         sync.Mutex
	onProgress func(LlamaProgress)
}

func (s *progressSink) report(progress LlamaProgress) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onProgress(progress)
}

// startWatcher runs Watch in the background, ignoring its error, under a
// context derived from ctx. It returns once the SSE request is written, so the
// watch starts before a load, and returns a stop function that aborts the
// watcher and waits for it to exit.
func (c *LlamaClient) startWatcher(ctx context.Context, onEvent func(LlamaModelEvent)) func() {
	watchCtx, cancel := context.WithCancel(ctx)
	sent := make(chan struct{})
	var group sync.WaitGroup
	group.Go(func() {
		_ = c.watch(watchCtx, onEvent, func() { close(sent) })
	})
	<-sent
	return func() {
		cancel()
		group.Wait()
	}
}

// LoadAndWait loads model and waits until it is loaded or fails, reporting
// progress from /models/sse.
func (c *LlamaClient) LoadAndWait(ctx context.Context, model string, onProgress func(LlamaProgress)) (LlamaModelInfo, error) {
	sink := &progressSink{onProgress: onProgress}
	eventLoaded := false
	eventError := ""
	stop := c.startWatcher(ctx, func(event LlamaModelEvent) {
		if event.Model != model || (event.Event != "model_status" && event.Event != "status_change") {
			return
		}
		sink.mu.Lock()
		defer sink.mu.Unlock()
		var payload struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(event.Data, &payload)
		status := payload.Status
		if status == "loaded" {
			eventLoaded = true
		}
		if status == "unloaded" {
			eventError = "Model failed to load"
		}
		if progress, ok := parseLoadProgress(event.Data); ok {
			sink.onProgress(progress)
		}
	})
	defer stop()
	if err := c.Load(ctx, model); err != nil {
		return LlamaModelInfo{}, err
	}
	sink.report(LlamaProgress{Message: "Loading model"})
	for {
		if ctx.Err() != nil {
			return LlamaModelInfo{}, context.Cause(ctx)
		}
		models, err := c.List(ctx, false)
		if err != nil {
			return LlamaModelInfo{}, err
		}
		entry := findModel(models, model)
		if entry != nil && entry.Status.Value == LlamaModelStatusLoaded {
			return *entry, nil
		}
		sink.mu.Lock()
		loaded, failure := eventLoaded, eventError
		sink.mu.Unlock()
		if loaded && entry == nil {
			return LlamaModelInfo{ID: model, Status: LlamaModelInfoStatus{Value: LlamaModelStatusLoaded}}, nil
		}
		if (entry != nil && entry.Status.Failed) || failure != "" {
			return LlamaModelInfo{}, loadFailure(entry, failure)
		}
		if err := sleep(ctx, 250*time.Millisecond); err != nil {
			return LlamaModelInfo{}, err
		}
	}
}

func loadFailure(entry *LlamaModelInfo, eventError string) error {
	if entry == nil || entry.Status.ExitCode == nil {
		if eventError != "" {
			return errors.New(eventError)
		}
		return errors.New("Model failed to load")
	}
	return fmt.Errorf("Model exited with code %s", formatNumber(*entry.Status.ExitCode))
}

// DownloadAndWait downloads model and waits until it finishes or fails,
// reporting progress from /models/sse.
func (c *LlamaClient) DownloadAndWait(ctx context.Context, model string, onProgress func(LlamaProgress)) ([]LlamaModelInfo, error) {
	sink := &progressSink{onProgress: onProgress}
	finished := false
	failure := ""
	sawDownloading := false
	stop := c.startWatcher(ctx, func(event LlamaModelEvent) {
		if event.Model != model {
			return
		}
		sink.mu.Lock()
		defer sink.mu.Unlock()
		switch event.Event {
		case "download_finished":
			finished = true
		case "download_failed":
			failure = errorMessage(event.Data, "Download failed")
		case "download_progress":
			sawDownloading = true
			if progress, ok := parseDownloadProgress(event.Data); ok {
				sink.onProgress(progress)
			}
		}
	})
	defer stop()
	if err := c.Download(ctx, model); err != nil {
		return nil, err
	}
	sink.report(LlamaProgress{Message: "Downloading model"})
	for polls := 0; ; {
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		sink.mu.Lock()
		failed := failure
		sink.mu.Unlock()
		if failed != "" {
			return nil, errors.New(failed)
		}
		models, err := c.List(ctx, false)
		if err != nil {
			return nil, err
		}
		polls++
		entry := findModel(models, model)
		sink.mu.Lock()
		done := false
		if entry != nil && entry.Status.Value == LlamaModelStatusDownloading {
			sawDownloading = true
			if progress, ok := parseDownloadProgress(entry.Status.Progress); ok {
				sink.onProgress(progress)
			}
		} else {
			done = finished || (entry != nil && (sawDownloading || polls >= 2))
		}
		sink.mu.Unlock()
		if done {
			return c.List(ctx, true)
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return nil, err
		}
	}
}
