package llama

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

const defaultHuggingFaceURL = "https://huggingface.co"

var (
	quantizationPattern = regexp.MustCompile(`(?i)(?:^|[-_.])((?:UD-)?(?:IQ\d(?:_[A-Z0-9]+)+|Q\d(?:_[A-Z0-9]+)+|BF16|F16|F32|MXFP\d(?:_[A-Z0-9]+)*))$`)
	shardSuffixPattern  = regexp.MustCompile(`-\d{5}-of-\d{5}$`)
	rateLimitPattern    = regexp.MustCompile(`(?:^|;)t=(\d+)`)
)

// localeCollator orders names by the root-locale collation.
var localeCollator = collate.New(language.Und)

func localeCompare(left, right string) int { return localeCollator.CompareString(left, right) }

// HuggingFaceModel is one search result.
type HuggingFaceModel struct {
	ID        string
	Downloads float64
}

// HuggingFaceQuantization is one GGUF quantization. Size is nil when any
// file of the quantization has no reported size.
type HuggingFaceQuantization struct {
	Name string
	Size *float64
}

// HuggingFaceGated is "auto", "manual", or empty for an ungated model.
type HuggingFaceGated string

// HuggingFaceModelDetails holds the model details wopr reads.
type HuggingFaceModelDetails struct {
	ID            string
	Gated         HuggingFaceGated
	Quantizations []HuggingFaceQuantization
}

// payloadError returns the {"error": "..."} text of a Hub error body, or
// fallback.
func payloadError(body []byte, fallback string) string {
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Error != "" {
		return payload.Error
	}
	return fallback
}

func parseRateLimitDelay(value string) float64 {
	match := rateLimitPattern.FindStringSubmatch(value)
	if match == nil {
		return 0
	}
	delay, _ := strconv.ParseFloat(match[1], 64)
	return delay
}

func readToken(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// FindHuggingFaceToken returns the Hugging Face token from HF_TOKEN or the
// first readable token file; env reads one variable, with "" meaning unset.
func FindHuggingFaceToken(env func(string) string) string {
	if token := strings.TrimSpace(env("HF_TOKEN")); token != "" {
		return token
	}
	var paths []string
	if path := env("HF_TOKEN_PATH"); path != "" {
		paths = append(paths, path)
	}
	if home := env("HF_HOME"); home != "" {
		paths = append(paths, filepath.Join(home, "token"))
	}
	if cache := env("XDG_CACHE_HOME"); cache != "" {
		paths = append(paths, filepath.Join(cache, "huggingface", "token"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".cache", "huggingface", "token"))
	}
	seen := map[string]bool{}
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		if token := readToken(path); token != "" {
			return token
		}
	}
	return ""
}

// HuggingFaceClient queries the Hugging Face Hub for GGUF models.
type HuggingFaceClient struct {
	token   string
	baseURL string
}

// NewHuggingFaceClient returns a client; an empty baseURL selects
// https://huggingface.co.
func NewHuggingFaceClient(token, baseURL string) *HuggingFaceClient {
	baseURL = cmp.Or(baseURL, defaultHuggingFaceURL)
	return &HuggingFaceClient{token: token, baseURL: strings.TrimRight(baseURL, "/")}
}

func (c *HuggingFaceClient) request(ctx context.Context, path string) ([]byte, error) {
	headers := http.Header{}
	if c.token != "" {
		headers.Set("Authorization", "Bearer "+c.token)
	}
	response, err := fetch(ctx, http.MethodGet, c.baseURL+path, headers, nil)
	if err != nil {
		return nil, err
	}
	if response.ok() {
		return response.body, nil
	}
	fallback := "Hugging Face returned HTTP " + strconv.Itoa(response.status)
	if response.status == http.StatusTooManyRequests {
		delay, err := strconv.ParseFloat(strings.TrimSpace(response.header.Get("Retry-After")), 64)
		if err != nil || math.IsNaN(delay) {
			delay = 0
		}
		if delay == 0 {
			delay = parseRateLimitDelay(response.header.Get("Ratelimit"))
		}
		if delay != 0 {
			return nil, errors.New("Hugging Face rate limit reached; retry in " + formatNumber(delay) + "s")
		}
		return nil, errors.New("Hugging Face rate limit reached")
	}
	return nil, errors.New(payloadError(response.body, fallback))
}

// Search finds GGUF models matching query.
func (c *HuggingFaceClient) Search(ctx context.Context, query string) ([]HuggingFaceModel, error) {
	params := url.Values{
		"search":    {query},
		"filter":    {"gguf"},
		"sort":      {"downloads"},
		"direction": {"-1"},
		"limit":     {"20"},
	}
	body, err := c.request(ctx, "/api/models?"+params.Encode())
	if err != nil {
		return nil, err
	}
	var entries []struct {
		ID        *string `json:"id"`
		Downloads float64 `json:"downloads"`
	}
	if json.Unmarshal(body, &entries) != nil || entries == nil {
		return nil, errors.New("Hugging Face returned invalid search results")
	}
	models := []HuggingFaceModel{}
	for _, entry := range entries {
		if entry.ID != nil {
			models = append(models, HuggingFaceModel{ID: *entry.ID, Downloads: entry.Downloads})
		}
	}
	return models, nil
}

type quantizationSize struct {
	name     string
	total    float64
	complete bool
}

// Details returns the model's gating and GGUF quantizations.
func (c *HuggingFaceClient) Details(ctx context.Context, id string) (HuggingFaceModelDetails, error) {
	segments := strings.Split(id, "/")
	for index, segment := range segments {
		segments[index] = url.PathEscape(segment)
	}
	body, err := c.request(ctx, "/api/models/"+strings.Join(segments, "/")+"?blobs=true")
	if err != nil {
		return HuggingFaceModelDetails{}, err
	}
	var payload *modelDetailsPayload
	if json.Unmarshal(body, &payload) != nil || payload == nil {
		return HuggingFaceModelDetails{}, errors.New("Hugging Face returned invalid model details")
	}
	details := HuggingFaceModelDetails{ID: id, Quantizations: sortQuantizations(collectQuantizations(payload.Siblings))}
	if payload.ID != nil {
		details.ID = *payload.ID
	}
	// gated is false for an open model and "auto" or "manual" otherwise.
	var gated string
	if json.Unmarshal(payload.Gated, &gated) == nil && (gated == "auto" || gated == "manual") {
		details.Gated = HuggingFaceGated(gated)
	}
	return details, nil
}

// modelDetailsPayload is the part of /api/models/{id} wopr reads.
type modelDetailsPayload struct {
	ID       *string         `json:"id"`
	Gated    json.RawMessage `json:"gated"`
	Siblings []hubFile       `json:"siblings"`
}

// hubFile is one file of a Hub model repository.
type hubFile struct {
	Filename string   `json:"rfilename"`
	Size     *float64 `json:"size"`
}

// collectQuantizations groups GGUF files by quantization in first-seen order.
func collectQuantizations(files []hubFile) []quantizationSize {
	var sizes []quantizationSize
	for _, file := range files {
		name := file.Filename
		if !strings.HasSuffix(strings.ToLower(name), ".gguf") {
			continue
		}
		filename := name[strings.LastIndex(name, "/")+1:]
		if strings.HasPrefix(strings.ToLower(filename), "mmproj") {
			continue
		}
		stem := shardSuffixPattern.ReplaceAllString(filename[:len(filename)-5], "")
		match := quantizationPattern.FindStringSubmatch(stem)
		if match == nil || match[1] == "" {
			continue
		}
		quantization := strings.ToUpper(match[1])
		index := slices.IndexFunc(sizes, func(entry quantizationSize) bool { return entry.name == quantization })
		if index < 0 {
			sizes = append(sizes, quantizationSize{name: quantization, complete: true})
			index = len(sizes) - 1
		}
		if file.Size != nil {
			sizes[index].total += *file.Size
		} else {
			sizes[index].complete = false
		}
	}
	return sizes
}

func sortQuantizations(sizes []quantizationSize) []HuggingFaceQuantization {
	quantizations := make([]HuggingFaceQuantization, 0, len(sizes))
	for _, entry := range sizes {
		quantization := HuggingFaceQuantization{Name: entry.name}
		if entry.complete {
			quantization.Size = &entry.total
		}
		quantizations = append(quantizations, quantization)
	}
	sizeOrMax := func(size *float64) float64 {
		if size == nil {
			return 1<<53 - 1
		}
		return *size
	}
	slices.SortStableFunc(quantizations, func(left, right HuggingFaceQuantization) int {
		if left.Name == "Q4_K_M" {
			return -1
		}
		if right.Name == "Q4_K_M" {
			return 1
		}
		if bySize := cmp.Compare(sizeOrMax(left.Size), sizeOrMax(right.Size)); bySize != 0 {
			return bySize
		}
		return localeCompare(left.Name, right.Name)
	})
	return quantizations
}
