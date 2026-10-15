package ai

import (
	"regexp"
	"strings"
)

func buildProviderErrorPattern(patterns []string) *regexp.Regexp {
	return regexp.MustCompile("(?i)" + strings.Join(patterns, "|"))
}

var nonRetryableProviderLimitErrorPattern = buildProviderErrorPattern([]string{
	// OpenCode Go/free-tier limits returned as 429 JSON error types by
	// OpenCode's Zen API are subscription/account limits.
	"GoUsageLimitError",
	"FreeUsageLimitError",
	"Monthly usage limit reached",
	"available balance",
	// Generic quota/budget/billing exhaustion.
	"free_tier_limit",
	"insufficient_quota",
	"out of budget",
	"quota exceeded",
	"billing",
})

// providerRateLimitPattern is the quota/rate-limit vocabulary that decides
// whether a failed model is rested for the long quota backoff and advanced
// past, rather than being retried on the same model. It is deliberately not
// part of nonRetryableProviderLimitErrorPattern: a transient 429 or rate
// limit stays retryable same-model (see IsRetryableAssistantError). The
// numeric branches are anchored here because buildProviderErrorPattern joins
// alternatives without adding word boundaries, so a bare 429 would match any
// number containing it.
var providerRateLimitPattern = buildProviderErrorPattern([]string{
	`\b429\b`,
	`rate[ _-]?limit`,
	`quota`,
	`usage limit`,
	`too many requests`,
	`overloaded`,
	`capacity`,
	`\b529\b`,
})

// IsProviderLimitError reports whether a provider error message is a quota,
// rate-limit, or capacity failure that should rest the failed model and
// advance to the next configured candidate. An empty message is not one.
func IsProviderLimitError(message string) bool {
	if message == "" {
		return false
	}
	return nonRetryableProviderLimitErrorPattern.MatchString(message) ||
		providerRateLimitPattern.MatchString(message)
}

var retryableProviderErrorPattern = buildProviderErrorPattern([]string{
	// Generic provider load, HTTP status, and server-side transient failures.
	"overloaded",
	"currently experiencing high demand",
	"rate.?limit",
	"too many requests",
	"429",
	"500",
	"502",
	"503",
	"504",
	"520",
	"524",
	"service.?unavailable",
	"server.?error",
	"internal.?error",
	// Wrapper/provider text for transient upstream failures.
	"provider.?returned.?error",
	"exceeded request buffer limit while retrying upstream",
	// Network, proxy, and fetch transport failures.
	"network.?error",
	"connection.?error",
	"connection.?refused",
	"connection.?lost",
	"other side closed",
	"upstream.?connect",
	"reset before headers",
	"socket connection was closed",
	"timed? out",
	"timeout",
	// WebSocket transports can report close/error text.
	"websocket.?closed",
	"websocket.?error",
	// Premature stream endings from SDKs and transports.
	"ended without",
	"stream ended before message_stop",
	"stream ended before a terminal response event",
	"http2 request did not get a response",
	// Provider-requested retry delay cap failures flow through the outer policy.
	"retry delay",
	// Explicit retry guidance emitted mid-stream.
	"you can retry your request",
	"try your request again",
	"please retry your request",
	// gRPC based providers.
	"ResourceExhausted",
})

// DefaultMaxAgentRetryDelayMs is the default cap for one agent retry delay.
const DefaultMaxAgentRetryDelayMs = 60_000

// RetryDelayMs returns baseDelayMs * 2^(attempt-1) for a 1-based attempt,
// capped at maxAgentDelayMs (DefaultMaxAgentRetryDelayMs when nil).
func RetryDelayMs(baseDelayMs int, maxAgentDelayMs *int, attempt int) int {
	limit := DefaultMaxAgentRetryDelayMs
	if maxAgentDelayMs != nil {
		limit = *maxAgentDelayMs
	}
	shift := max(0, attempt-1)
	if baseDelayMs > 0 && (shift >= 62 || baseDelayMs > limit>>shift) {
		return limit
	}
	return min(baseDelayMs<<shift, limit)
}

// IsRetryableAssistantError classifies whether a failed assistant message looks
// like a transient provider or transport error. It does not implement retry
// policy; callers handle context overflow first.
func IsRetryableAssistantError(message AssistantMessage) bool {
	if message.StopReason != StopReasonError || message.ErrorMessage == "" {
		return false
	}
	if nonRetryableProviderLimitErrorPattern.MatchString(message.ErrorMessage) {
		return false
	}
	return retryableProviderErrorPattern.MatchString(message.ErrorMessage)
}
