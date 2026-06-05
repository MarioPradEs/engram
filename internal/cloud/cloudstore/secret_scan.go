package cloudstore

import (
	"encoding/json"
	"regexp"
)

// secretPattern pairs a human-readable name with a compiled regex.
// Each pattern is CONSERVATIVE and HIGH-CONFIDENCE — only patterns that match
// a specific key format are included. A prior false-positive incident (W9) showed
// that loose "long token" regexes match ordinary prose; every pattern here must
// describe a precise, vendor-specific format.
type secretPattern struct {
	name string
	re   *regexp.Regexp
}

// secretPatterns is the curated, ordered list of patterns checked by redactSecrets.
// Order is stable across calls (slice, not map) so multi-hit replacements are
// deterministic. Patterns are compiled once at package init.
var secretPatterns = []secretPattern{
	// PEM private key block — -----BEGIN [ANY UPPERCASE WORDS] PRIVATE KEY-----
	// Uses (?s) so dot matches newline: catches multi-line keys in a single string.
	{
		name: "pem_private_key",
		re:   regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----[A-Za-z0-9+/=\s]*-----END [A-Z ]*PRIVATE KEY-----`),
	},
	// AWS access key ID — always exactly AKIA followed by 16 uppercase alphanumerics.
	{
		name: "aws_access_key_id",
		re:   regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	},
	// GitHub tokens — gh followed by one of p/o/u/s/r, then underscore + 36+ alphanum.
	// Covers ghp_ (PAT), gho_ (OAuth), ghu_ (user), ghs_ (server), ghr_ (refresh).
	{
		name: "github_token",
		re:   regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`),
	},
	// Anthropic secret key — sk-ant- prefix followed by 20+ chars (alphanum, dash, underscore).
	// Listed BEFORE the openai_key pattern so "sk-ant-..." matches here and not there.
	{
		name: "anthropic_key",
		re:   regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{20,}\b`),
	},
	// OpenAI secret key — sk- or sk-proj- prefix followed by 20+ alphanumerics.
	// Listed AFTER anthropic_key so "sk-ant-" is already consumed above.
	{
		name: "openai_key",
		re:   regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9]{20,}\b`),
	},
	// Google API key — AIza followed by exactly 35 url-safe chars.
	{
		name: "google_api_key",
		re:   regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`),
	},
	// Slack tokens — xox[baprs]- prefix followed by 10+ alphanum/dash chars.
	{
		name: "slack_token",
		re:   regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9\-]{10,}\b`),
	},
	// JWT — three base64url-encoded segments separated by dots.
	// Each segment starts with eyJ (base64 for '{"') which is highly distinctive.
	{
		name: "jwt",
		re:   regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]+\.eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+`),
	},
	// Credentials embedded in a URL — ://user:password@host
	// Matches after :// any non-whitespace/@ chars as user, colon, then password (no @/space), then @.
	// Does NOT match ://user@host (no password) or bare ://host.
	{
		name: "url_credentials",
		re:   regexp.MustCompile(`://[^:/\s@]+:[^@/\s]+@`),
	},
}

// redactedPlaceholder replaces every matched secret substring.
const redactedPlaceholder = "[REDACTED-SECRET]"

// redactSecrets scans content for high-confidence secret patterns and replaces
// each match with [REDACTED-SECRET]. It returns the (possibly modified) string
// and a boolean indicating whether any redaction occurred.
//
// The function is pure (no side-effects) and safe to call concurrently.
func redactSecrets(content string) (redacted string, found bool) {
	result := content
	for _, p := range secretPatterns {
		replaced := p.re.ReplaceAllString(result, redactedPlaceholder)
		if replaced != result {
			found = true
			result = replaced
		}
	}
	return result, found
}

// redactSecretsInObservationPayload scans a JSON payload's "content" and "title"
// fields for secrets. If any are found, those fields are replaced with the redacted
// values and the modified JSON bytes are returned with found=true.
//
// If the payload is empty or not valid JSON, the original bytes are returned
// unchanged with found=false — the caller handles those cases separately.
func redactSecretsInObservationPayload(payload []byte) (redacted []byte, found bool) {
	if len(payload) == 0 {
		return payload, false
	}

	var m map[string]interface{}
	if err := json.Unmarshal(payload, &m); err != nil {
		return payload, false
	}

	if content, ok := m["content"].(string); ok {
		if replaced, hit := redactSecrets(content); hit {
			m["content"] = replaced
			found = true
		}
	}

	if title, ok := m["title"].(string); ok {
		if replaced, hit := redactSecrets(title); hit {
			m["title"] = replaced
			found = true
		}
	}

	if !found {
		return payload, false
	}

	out, err := json.Marshal(m)
	if err != nil {
		// Defensive: return original on marshal error (should never happen).
		return payload, false
	}
	return out, true
}
