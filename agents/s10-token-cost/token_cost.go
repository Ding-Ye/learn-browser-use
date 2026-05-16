package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// token_cost.go is the *ledger*: the part the rest of the agent
// pokes at. Upstream's TokenCost class has ~30 methods; we keep four:
//
//   - RegisterInvocation(model, inputTok, outputTok)  ← write path
//   - Summary() string                                 ← read path (formatted)
//   - TotalUSD() float64                               ← read path (scalar)
//   - History []Usage                                  ← raw access for tests
//
// The teaching focus is to show that "cost tracking" is just two
// nested aggregations: (a) every invocation gets appended to a slice
// for forensic detail, (b) per-model totals get incremented in a map
// for O(1) summary rendering. No async, no background flush, no
// telemetry server. The whole concern fits in a struct with three
// fields and ~80 lines of code.

// Usage is one row of the per-invocation ledger.
//
// We deliberately keep this denormalized: each Usage carries its own
// model name and its own computed cost. That lets Summary() compute
// per-model breakdowns without re-walking a separate pricing path,
// and lets tests like TestUnknownModelReturnsZero make exact
// assertions about a single row.
type Usage struct {
	Model       string
	InputTok    int
	OutputTok   int
	InputCost   float64   // USD, already-computed at registration time
	OutputCost  float64   // USD
	Timestamp   time.Time // wall-clock when RegisterInvocation was called
	HasPricing  bool      // false if the model wasn't in the pricing table
}

// TotalCost is the cost across the whole ledger. Reusing the
// Pricing-shaped fields would conflate "this is a rate" with "this is
// an absolute"; a separate struct keeps the units honest.
type TotalCost struct {
	InputUSD   float64
	OutputUSD  float64
	InputTok   int
	OutputTok  int
	Invocations int
}

// USD returns input + output in dollars.
func (t TotalCost) USD() float64 { return t.InputUSD + t.OutputUSD }

// TokenCost is the in-process ledger that an Agent owns.
//
// Concurrency note: in the upstream Python implementation, multiple
// LLM invocations can race; the equivalent Go object would need a
// mutex around History/Total/byModel. For a teaching chapter we leave
// it single-threaded — the s12 integration chapter is where a real
// agent loop appears, and that loop is sequential.
type TokenCost struct {
	History []Usage

	// Total is the bottom-line cost across all invocations.
	Total TotalCost

	// pricing is the model→rate lookup; set in NewTokenCost. Exposed
	// as a private field so tests can override it via constructor —
	// see NewTokenCostWithPricing.
	pricing map[string]Pricing

	// byModel is the rolled-up cost per model, recomputed
	// incrementally on every RegisterInvocation. Stored sorted-ready
	// to keep Summary() deterministic.
	byModel map[string]TotalCost

	// clock lets tests inject a fixed time; production uses time.Now.
	clock func() time.Time
}

// NewTokenCost builds a ledger backed by the embedded pricing snapshot.
func NewTokenCost() *TokenCost {
	return NewTokenCostWithPricing(EmbeddedPricingSnapshot())
}

// NewTokenCostWithPricing lets the caller supply a pricing table
// directly. Used by tests and by anyone wiring a Refresher in front
// (see refresher.go) of the ledger.
func NewTokenCostWithPricing(pricing map[string]Pricing) *TokenCost {
	return &TokenCost{
		pricing: pricing,
		byModel: make(map[string]TotalCost),
		clock:   time.Now,
	}
}

// RegisterInvocation records one LLM call into the ledger.
//
// Sequence:
//  1. Look up the model's pricing. Unknown model => cost 0, but the
//     row still goes in (so per-token totals stay accurate).
//  2. Compute input/output cost as (tokens / 1000) * rate.
//  3. Append a Usage row to History.
//  4. Bump Total and byModel[model] by the new costs/tokens.
//
// All four steps are O(1). The whole method is ~20 lines because
// that's all this needs to be — the upstream version is longer
// because it also handles cached tokens (Anthropic prompt caching),
// which we deliberately skip in s10. See README "What this is NOT".
func (tc *TokenCost) RegisterInvocation(model string, inputTok, outputTok int) Usage {
	p, ok := tc.pricing[model]

	inputCost := float64(inputTok) / 1000.0 * p.InputPer1k
	outputCost := float64(outputTok) / 1000.0 * p.OutputPer1k
	if !ok {
		inputCost, outputCost = 0, 0
	}

	row := Usage{
		Model:      model,
		InputTok:   inputTok,
		OutputTok:  outputTok,
		InputCost:  inputCost,
		OutputCost: outputCost,
		Timestamp:  tc.clock(),
		HasPricing: ok,
	}
	tc.History = append(tc.History, row)

	tc.Total.InputUSD += inputCost
	tc.Total.OutputUSD += outputCost
	tc.Total.InputTok += inputTok
	tc.Total.OutputTok += outputTok
	tc.Total.Invocations++

	sub := tc.byModel[model]
	sub.InputUSD += inputCost
	sub.OutputUSD += outputCost
	sub.InputTok += inputTok
	sub.OutputTok += outputTok
	sub.Invocations++
	tc.byModel[model] = sub

	return row
}

// TotalUSD is a convenience scalar for the overall cost.
func (tc *TokenCost) TotalUSD() float64 { return tc.Total.USD() }

// PerModel returns the rolled-up cost for one model.
// Zero value if the model was never invoked.
func (tc *TokenCost) PerModel(model string) TotalCost {
	return tc.byModel[model]
}

// Summary renders a human-readable report.
//
// Why deterministic ordering? Tests assert on the formatted output;
// map iteration order would make them flaky. We sort the model keys.
//
// Why $%.4f precision? Token costs are routinely sub-cent — gpt-4o-mini
// at 100 input tokens is $0.000015. Two-decimal "$0.00" hides this;
// four decimals show enough digits to verify the math on tiny samples
// without going full scientific notation.
func (tc *TokenCost) Summary() string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Token cost summary — %d invocation(s)\n", tc.Total.Invocations))
	sb.WriteString(fmt.Sprintf("  Total: in=%d tok  out=%d tok  cost=$%.4f\n",
		tc.Total.InputTok, tc.Total.OutputTok, tc.Total.USD()))

	if len(tc.byModel) == 0 {
		return sb.String()
	}

	sb.WriteString("\n  Per model:\n")
	models := make([]string, 0, len(tc.byModel))
	for m := range tc.byModel {
		models = append(models, m)
	}
	sort.Strings(models)
	for _, m := range models {
		c := tc.byModel[m]
		sb.WriteString(fmt.Sprintf("    %-22s  invocations=%d  in=%d  out=%d  cost=$%.4f\n",
			m, c.Invocations, c.InputTok, c.OutputTok, c.USD()))
	}
	return sb.String()
}
