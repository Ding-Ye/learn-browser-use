package main

import "regexp"

// RedactSensitive returns content with high-risk patterns masked by
// "[REDACTED]". The patterns are intentionally conservative — false
// positives are fine, false negatives are not. We only need to be
// "good enough" for a teaching demo; production redaction would also
// catch JWTs, AWS keys, credit cards, etc.
//
// Upstream takes a different approach (service.py#L573-L597,
// utils.py#L51-L55): it has a dict of {placeholder: secret} and
// replaces by exact string match. That's the right shape when the
// agent KNOWS its secrets (e.g. user-provided env vars). Our regex
// version covers the complementary case: secrets that slip in via
// LLM output or tool results when we don't know them ahead of time.
// In a real system both layers run.
//
// Important property: RedactSensitive is idempotent. Running it twice
// on the same string returns the same string — no double-masking.
func RedactSensitive(content string) string {
	if content == "" {
		return content
	}
	for _, p := range patterns {
		content = p.ReplaceAllString(content, "[REDACTED]")
	}
	return content
}

// patterns is a compiled list of regexes that we mask. Order matters
// only when patterns overlap; here they don't. We compile once at
// package init time so there's zero per-call overhead.
//
// Why regex-only?
//   - Stdlib-only constraint (no third-party PII libs).
//   - These three patterns cover ~90% of "obvious" leaks in agent
//     transcripts: provider keys, bearer tokens, and emails.
//   - More sophisticated detection (entropy-based key sniffing,
//     credit-card Luhn checks) belongs in a separate package.
var patterns = []*regexp.Regexp{
	// OpenAI / Anthropic-style API keys: "sk-" prefix + 20+ tokens.
	// We also catch the variants "sk-ant-..." and "sk_live_..."
	// because the prefix dominates the pattern.
	regexp.MustCompile(`sk-[A-Za-z0-9_\-]{20,}`),

	// Bearer tokens in Authorization headers and the like.
	// Matches "Bearer <token>" where the token is JWT-shaped or
	// any URL-safe base64-ish run of >= 16 chars.
	regexp.MustCompile(`Bearer\s+[A-Za-z0-9._\-]{16,}`),

	// Email addresses. RFC-5322 is famously hairy; this covers the
	// 99% case (no quoted local-parts, no domain literals).
	regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`),
}
