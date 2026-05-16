package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// token_cost.go is a stripped-down s10 ledger: just enough to track
// cost across the integration demo. We hardcode ONE model pricing
// row (the "mock" model the demo's MockProvider stamps onto its
// Response.Model field) plus a few well-known models so the README
// can show a non-trivial Summary().
//
// What we drop vs s10:
//   - No `//go:embed pricing_data.json`. Three rows is below the
//     threshold where a JSON file pays for itself.
//   - No Refresher / TTL cache. The integration story is about the
//     agent loop calling RegisterInvocation in the right place, not
//     about price discovery.
//   - No prompt-cached / cache-creation token bucketing.

// Usage is one row of the per-invocation ledger.
type Usage struct {
	Model       string
	InputTok    int
	OutputTok   int
	InputCost   float64
	OutputCost  float64
	Timestamp   time.Time
	HasPricing  bool
}

// TotalCost is the absolute cost across the whole ledger (or a subset
// when used in byModel). Reusing the Pricing shape would conflate
// "rate" with "absolute"; a separate struct keeps the units honest.
type TotalCost struct {
	InputUSD    float64
	OutputUSD   float64
	InputTok    int
	OutputTok   int
	Invocations int
}

// USD returns input + output in dollars.
func (t TotalCost) USD() float64 { return t.InputUSD + t.OutputUSD }

// TokenCost is the in-process ledger an Agent owns.
type TokenCost struct {
	History []Usage
	Total   TotalCost
	pricing map[string]Pricing
	byModel map[string]TotalCost
	clock   func() time.Time
}

// NewTokenCost returns a ledger populated with the s12-builtin pricing
// table. Models the demo touches: "mock" (free), "gpt-4o", "gpt-4o-mini".
func NewTokenCost() *TokenCost {
	return &TokenCost{
		pricing: map[string]Pricing{
			"mock":         {InputPer1k: 0, OutputPer1k: 0},
			"gpt-4o":       {InputPer1k: 0.0025, OutputPer1k: 0.01},
			"gpt-4o-mini":  {InputPer1k: 0.00015, OutputPer1k: 0.0006},
		},
		byModel: make(map[string]TotalCost),
		clock:   time.Now,
	}
}

// RegisterInvocation appends one Usage row and rolls the per-model +
// global totals in O(1). Mirrors s10's signature exactly; the s12
// agent loop calls this once per Provider.Invoke return.
func (tc *TokenCost) RegisterInvocation(model string, inputTok, outputTok int) Usage {
	p, ok := tc.pricing[model]
	inputCost := float64(inputTok) / 1000.0 * p.InputPer1k
	outputCost := float64(outputTok) / 1000.0 * p.OutputPer1k
	if !ok {
		// Unknown model: tokens still recorded, $ stays zero. Matches
		// s10's policy — a brand-new model that isn't in the table
		// shouldn't crash the agent.
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

// TotalUSD is the convenience scalar accessor — same field as
// Total.USD(), kept as a method for parity with s10.
func (tc *TokenCost) TotalUSD() float64 { return tc.Total.USD() }

// Summary renders a human-readable report. Sorted by model name so
// the output is deterministic across map-iteration orders.
func (tc *TokenCost) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Token cost — %d invocation(s)\n", tc.Total.Invocations)
	fmt.Fprintf(&b, "  Total: in=%d tok  out=%d tok  cost=$%.4f\n",
		tc.Total.InputTok, tc.Total.OutputTok, tc.Total.USD())
	if len(tc.byModel) == 0 {
		return b.String()
	}
	b.WriteString("  Per model:\n")
	names := make([]string, 0, len(tc.byModel))
	for n := range tc.byModel {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		c := tc.byModel[n]
		fmt.Fprintf(&b, "    %-15s invocations=%d  in=%d  out=%d  cost=$%.4f\n",
			n, c.Invocations, c.InputTok, c.OutputTok, c.USD())
	}
	return b.String()
}
