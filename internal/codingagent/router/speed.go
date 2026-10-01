package router

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Speed learning: per model, time to first token is modeled as
//
//	ttft ≈ base + perK × (uncached input tokens / 1000)
//
// Both terms are exponential moving averages of observed requests, so a
// slow-prefill server (a large model on a laptop) and a fast one (vLLM on a GPU box) sort
// themselves out after a request or two. The whole state is two floats per
// model in one small JSON file.

// StatsFileName holds learned speeds under the agent directory.
const StatsFileName = "router-speed.json"

const (
	speedAlpha = 0.3
	// Requests with at least this many uncached tokens update the prefill
	// rate; smaller ones update the fixed overhead.
	prefillSampleTokens = 1000
)

// SpeedStat is one model's learned latency.
type SpeedStat struct {
	BaseSeconds float64 `json:"baseSeconds"`
	PerKSeconds float64 `json:"perKSeconds"`
	Samples     int     `json:"samples"`
	// OutputTokensPerSecond is the decode rate /setup measured; the Basic
	// rules' speed mode uses it.
	OutputTokensPerSecond float64 `json:"outputTokensPerSecond,omitempty"`
}

type speedBook struct {
	mu    sync.Mutex
	path  string
	stats map[string]SpeedStat
}

func loadSpeedBook(dir string) *speedBook {
	b := &speedBook{stats: map[string]SpeedStat{}}
	if dir == "" {
		return b
	}
	b.path = filepath.Join(dir, StatsFileName)
	if data, err := os.ReadFile(b.path); err == nil {
		_ = json.Unmarshal(data, &b.stats)
	}
	return b
}

// estimate returns the expected time to first token, or ok=false when the
// model has never been observed.
func (b *speedBook) estimate(spec string, uncachedTokens int) (float64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.stats[spec]
	if !ok || s.Samples == 0 {
		return 0, false
	}
	return s.BaseSeconds + s.PerKSeconds*float64(uncachedTokens)/1000, true
}

// decodeRate is the model's measured output tokens per second, or 0.
func (b *speedBook) decodeRate(spec string) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stats[spec].OutputTokensPerSecond
}

func (b *speedBook) observe(spec string, uncachedTokens int, ttft time.Duration) {
	seconds := ttft.Seconds()
	if seconds <= 0 {
		return
	}
	k := float64(uncachedTokens) / 1000
	b.mu.Lock()
	s := b.stats[spec]
	switch {
	case s.Samples == 0 && uncachedTokens >= prefillSampleTokens:
		// First sighting of a long prompt: split evenly until a short
		// request pins the overhead down.
		s.BaseSeconds = seconds / 2
		s.PerKSeconds = seconds / 2 / k
	case s.Samples == 0:
		s.BaseSeconds = seconds
	case uncachedTokens >= prefillSampleTokens:
		sample := max(0, seconds-s.BaseSeconds) / k
		s.PerKSeconds += speedAlpha * (sample - s.PerKSeconds)
	default:
		sample := max(0, seconds-s.PerKSeconds*k)
		s.BaseSeconds += speedAlpha * (sample - s.BaseSeconds)
	}
	s.Samples++
	b.stats[spec] = s
	snapshot, err := json.MarshalIndent(b.stats, "", "  ")
	path := b.path
	b.mu.Unlock()
	if err == nil && path != "" {
		_ = os.WriteFile(path, append(snapshot, '\n'), 0o600)
	}
}

// ExpectedTTFT is provider/model's expected time to first token for a
// request with uncachedTokens new tokens, or ok=false before it has been
// measured.
func (r *Router) ExpectedTTFT(spec string, uncachedTokens int) (time.Duration, bool) {
	if r == nil || r.speed == nil {
		return 0, false
	}
	seconds, ok := r.speed.estimate(spec, uncachedTokens)
	return time.Duration(seconds * float64(time.Second)), ok
}

func (b *speedBook) snapshot() map[string]SpeedStat {
	b.mu.Lock()
	defer b.mu.Unlock()
	return maps.Clone(b.stats)
}
