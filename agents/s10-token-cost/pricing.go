package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// pricing.go owns the "what does each model cost" data path.
//
// Two design choices worth flagging up front:
//
//  1. The pricing table is *embedded* into the Go binary via //go:embed.
//     Upstream Python fetches it from a LiteLLM GitHub URL at startup
//     and caches it under XDG_CACHE_HOME for 1 day. We invert that:
//     ship a known-good table inside the binary, and treat any remote
//     refresh as an optional override (see refresher.go). The teaching
//     reason is that "what does gpt-4o cost" is not actually volatile
//     enough to justify a network call on every program start, and the
//     remote dependency is the #1 source of "the program hung at boot
//     because someone's CDN was slow" bugs.
//
//  2. The unit is dollars-per-1k-tokens, NOT dollars-per-token. That's
//     what the public OpenAI/Anthropic/Google pricing pages quote
//     ($2.50 per 1M input tokens => 0.0025 per 1k => 0.0000025 per token).
//     Keeping the same denominator as the public page makes the JSON
//     file directly diffable against a screenshot of those pages.

// Pricing carries the input and output rates for one model.
//
// "per_1k" suffix is on purpose — it keeps the value readable
// (0.0025 vs 0.0000025) and matches public pricing page conventions.
type Pricing struct {
	InputPer1k  float64 `json:"input_per_1k"`
	OutputPer1k float64 `json:"output_per_1k"`
}

// pricingTableJSON is the embedded pricing snapshot.
//
//go:embed pricing_data.json
var pricingTableJSON []byte

// embeddedPricing is the parsed-once map for fast lookups.
// We parse at init() so the program fails loud at startup if the
// embedded JSON is malformed — far better than discovering it the
// first time a user asks for a cost summary.
var embeddedPricing map[string]Pricing

func init() {
	var m map[string]Pricing
	if err := json.Unmarshal(pricingTableJSON, &m); err != nil {
		// init() panic is the right shape: an embedded file that
		// doesn't parse is a build-time bug, not a runtime condition
		// the caller can recover from.
		panic(fmt.Sprintf("s10-token-cost: embedded pricing_data.json is invalid: %v", err))
	}
	embeddedPricing = m
}

// LookupPricing returns the embedded pricing entry for model, or
// (zero-value, false) if no such model is registered.
//
// Note the contract: an unknown model is NOT an error — it's a
// "we don't know how to price this, callers should record the usage
// but compute $0 cost". This matches upstream behavior where
// `get_model_pricing` returns None for unknown models instead of
// raising. Loud panics on first invocation of a new model would be
// hostile for users running mainline browser-use the day a new model
// ships before its pricing PR lands.
func LookupPricing(model string) (Pricing, bool) {
	p, ok := embeddedPricing[model]
	return p, ok
}

// EmbeddedPricingSnapshot returns a copy of the embedded pricing table
// for callers that want to walk all known models (e.g. the demo loop
// in main.go). Returning a copy means callers can't accidentally
// mutate the package-level map.
func EmbeddedPricingSnapshot() map[string]Pricing {
	out := make(map[string]Pricing, len(embeddedPricing))
	for k, v := range embeddedPricing {
		out[k] = v
	}
	return out
}
