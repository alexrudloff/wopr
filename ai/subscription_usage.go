package ai

import (
	"cmp"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"
)

// Subscription usage: ChatGPT (Codex) and Anthropic subscription logins report
// their rate-limit windows on every response, in headers (and, for the Codex
// WebSocket transport, in codex.rate_limits frames). The latest report per
// plan is kept here for display; nothing is requested to obtain it.

// Subscription plan names as reported by SubscriptionUsage.
const (
	SubscriptionChatGPT = "ChatGPT"
	SubscriptionClaude  = "Claude"
)

// UsageWindow is one rate-limit window of a subscription plan.
type UsageWindow struct {
	// Label names the window length ("5h", "wk", "7d").
	Label string
	// UsedPercent is the share of the window's allowance used, 0..100.
	UsedPercent float64
	// ResetsAt is when the window resets; zero when not reported.
	ResetsAt time.Time
}

// PlanUsage is the latest usage report of one subscription plan.
type PlanUsage struct {
	Plan    string
	Windows []UsageWindow
	At      time.Time
}

var subscriptionUsage = struct {
	mu    sync.Mutex
	plans map[string]PlanUsage
}{plans: map[string]PlanUsage{}}

// SubscriptionUsage returns the latest usage report of each plan that has
// reported one, ordered by plan name.
func SubscriptionUsage() []PlanUsage {
	subscriptionUsage.mu.Lock()
	defer subscriptionUsage.mu.Unlock()
	out := slices.Collect(maps.Values(subscriptionUsage.plans))
	slices.SortFunc(out, func(a, b PlanUsage) int { return cmp.Compare(a.Plan, b.Plan) })
	return out
}

func storePlanUsage(usage PlanUsage) {
	if len(usage.Windows) == 0 {
		return
	}
	subscriptionUsage.mu.Lock()
	subscriptionUsage.plans[usage.Plan] = usage
	subscriptionUsage.mu.Unlock()
}

// recordSubscriptionHeaders stores the usage windows found in a response's
// headers. Responses without subscription headers are ignored.
func recordSubscriptionHeaders(header http.Header, now time.Time) {
	if header == nil {
		return
	}
	if usage, ok := parseCodexUsageHeaders(header, now); ok {
		storePlanUsage(usage)
	}
	if usage, ok := parseAnthropicUsageHeaders(header, now); ok {
		storePlanUsage(usage)
	}
}

// parseCodexUsageHeaders reads x-codex-{primary,secondary}-used-percent with
// the window length (-window-minutes) and reset (-reset-at in Unix seconds, or
// -reset-after-seconds).
func parseCodexUsageHeaders(header http.Header, now time.Time) (PlanUsage, bool) {
	usage := PlanUsage{Plan: SubscriptionChatGPT, At: now}
	for _, window := range []struct{ name, label string }{{"primary", "5h"}, {"secondary", "wk"}} {
		prefix := "x-codex-" + window.name + "-"
		used, ok := headerFloat(header, prefix+"used-percent")
		if !ok {
			continue
		}
		label := window.label
		if minutes, ok := headerFloat(header, prefix+"window-minutes"); ok && minutes > 0 {
			label = windowLabel(time.Duration(minutes) * time.Minute)
		}
		var resets time.Time
		if at, ok := headerFloat(header, prefix+"reset-at"); ok && at > 0 {
			resets = time.Unix(int64(at), 0)
		} else if after, ok := headerFloat(header, prefix+"reset-after-seconds"); ok && after >= 0 {
			resets = now.Add(time.Duration(after) * time.Second)
		}
		usage.Windows = append(usage.Windows, UsageWindow{Label: label, UsedPercent: clampPercent(used), ResetsAt: resets})
	}
	return usage, len(usage.Windows) > 0
}

// parseAnthropicUsageHeaders reads anthropic-ratelimit-unified-{5h,7d}-utilization
// (a 0..1 fraction) and the matching -reset (Unix seconds).
func parseAnthropicUsageHeaders(header http.Header, now time.Time) (PlanUsage, bool) {
	usage := PlanUsage{Plan: SubscriptionClaude, At: now}
	for _, label := range []string{"5h", "7d"} {
		prefix := "anthropic-ratelimit-unified-" + label + "-"
		used, ok := headerFloat(header, prefix+"utilization")
		if !ok {
			continue
		}
		var resets time.Time
		if at, ok := headerFloat(header, prefix+"reset"); ok && at > 0 {
			resets = time.Unix(int64(at), 0)
		}
		usage.Windows = append(usage.Windows, UsageWindow{Label: label, UsedPercent: clampPercent(used * 100), ResetsAt: resets})
	}
	return usage, len(usage.Windows) > 0
}

// recordCodexRateLimitsFrame stores the windows of a codex.rate_limits
// WebSocket frame: {"rate_limits":{"primary":{"used_percent","window_minutes","reset_at"|"reset_after_seconds"},"secondary":{…}}}.
func recordCodexRateLimitsFrame(event map[string]any, now time.Time) {
	limits, _ := event["rate_limits"].(map[string]any)
	if limits == nil {
		return
	}
	usage := PlanUsage{Plan: SubscriptionChatGPT, At: now}
	for _, window := range []struct{ name, label string }{{"primary", "5h"}, {"secondary", "wk"}} {
		record, _ := limits[window.name].(map[string]any)
		used, ok := record["used_percent"].(float64)
		if !ok {
			continue
		}
		label := window.label
		if minutes, ok := record["window_minutes"].(float64); ok && minutes > 0 {
			label = windowLabel(time.Duration(minutes) * time.Minute)
		}
		var resets time.Time
		if at, ok := record["reset_at"].(float64); ok && at > 0 {
			resets = time.Unix(int64(at), 0)
		} else if after, ok := record["reset_after_seconds"].(float64); ok && after >= 0 {
			resets = now.Add(time.Duration(after) * time.Second)
		}
		usage.Windows = append(usage.Windows, UsageWindow{Label: label, UsedPercent: clampPercent(used), ResetsAt: resets})
	}
	storePlanUsage(usage)
}

func headerFloat(header http.Header, name string) (float64, bool) {
	value := header.Get(name)
	if value == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(value, 64)
	return f, err == nil
}

func clampPercent(p float64) float64 { return min(100, max(0, p)) }

// windowLabel names a window length: "5h", "wk", "7d"-style.
func windowLabel(d time.Duration) string {
	switch {
	case d == 7*24*time.Hour:
		return "wk"
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	}
	return fmt.Sprintf("%dm", d/time.Minute)
}
