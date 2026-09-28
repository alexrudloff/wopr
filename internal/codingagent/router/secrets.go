package router

import "regexp"

// Secret scanning runs locally before any text reaches Jev, whose endpoint
// may relay to a third party. Asking Jev whether text is sensitive would
// already have sent it. A hit is redacted in what Jev sees, and a subagent
// brief with a hit stays on owned hardware and never reaches Jev at all.

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,}\b`),
	regexp.MustCompile(`\bsk-(?:ant-|proj-|or-)?[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
	regexp.MustCompile(`\b[rs]k_(?:live|test)_[0-9A-Za-z]{16,}\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
	regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.-]*://[^/\s:@]+:[^/\s@]+@`),
	regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|secret|api[_-]?key|access[_-]?token|auth[_-]?token|client[_-]?secret|private[_-]?key)\b["']?\s*[:=]\s*(?:"[^"\s]{8,}"|'[^'\s]{8,}'|[A-Za-z0-9_\-+/=.]{12,})`),
}

// ScanSecrets returns text with likely credentials replaced by
// [REDACTED], and whether any were found. It is deterministic and local.
func ScanSecrets(text string) (string, bool) {
	hit := false
	for _, re := range secretPatterns {
		if re.MatchString(text) {
			hit = true
			text = re.ReplaceAllString(text, "[REDACTED]")
		}
	}
	return text, hit
}
