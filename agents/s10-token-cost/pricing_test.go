package main

import (
	"errors"
	"math"
	"testing"
	"time"
)

// pricing_test.go — five tests covering the four behaviors the
// chapter README promises:
//
//   - Accumulation: History grows, byModel totals update.
//   - Cost math: input/output rates multiplied correctly.
//   - Refresher TTL: stale-then-fresh-then-stale cache cycle.
//   - Unknown-model graceful degrade: cost 0, no panic.
//   - Embedded pricing actually loaded at init().

// closeEnough is the standard float-compare. We're dealing with
// dollars-per-1k-tokens that scale into the sub-cent range, so the
// tolerance has to be tighter than 1e-6 but looser than 1e-15.
func closeEnough(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

// TestRegistrationAccumulates — two invocations on the same model
// should produce History of length 2, with Total reflecting both.
//
// This is the most basic write-path test. If this fails, the rest of
// the ledger is suspect.
func TestRegistrationAccumulates(t *testing.T) {
	cost := NewTokenCost()

	cost.RegisterInvocation("gpt-4o", 1000, 200)
	cost.RegisterInvocation("gpt-4o", 500, 100)

	if got, want := len(cost.History), 2; got != want {
		t.Fatalf("History length = %d, want %d", got, want)
	}

	// Total tokens should be sum-of-rows.
	if got, want := cost.Total.InputTok, 1500; got != want {
		t.Errorf("Total.InputTok = %d, want %d", got, want)
	}
	if got, want := cost.Total.OutputTok, 300; got != want {
		t.Errorf("Total.OutputTok = %d, want %d", got, want)
	}
	if got, want := cost.Total.Invocations, 2; got != want {
		t.Errorf("Total.Invocations = %d, want %d", got, want)
	}

	// Per-model rollup should match Total since only one model was used.
	gpt4oRoll := cost.PerModel("gpt-4o")
	if got, want := gpt4oRoll.InputTok, cost.Total.InputTok; got != want {
		t.Errorf("PerModel(gpt-4o).InputTok = %d, want %d (== Total.InputTok)", got, want)
	}
	if !closeEnough(gpt4oRoll.USD(), cost.TotalUSD()) {
		t.Errorf("PerModel(gpt-4o).USD() = %v, want %v", gpt4oRoll.USD(), cost.TotalUSD())
	}
}

// TestCostComputation — known model + known token counts → known $.
//
// gpt-4o pricing in pricing_data.json is $0.0025/1k input,
// $0.01/1k output. So 1000 input + 500 output should give exactly
// 0.0025 + 0.005 = 0.0075 USD.
//
// Hardcoding the expected value is intentional: if a future PR
// changes pricing_data.json this test forces the author to update
// the assertion, which doubles as a "did you mean to change pricing?"
// guard.
func TestCostComputation(t *testing.T) {
	cost := NewTokenCost()
	row := cost.RegisterInvocation("gpt-4o", 1000, 500)

	wantInput := 0.0025  // 1000 / 1000 * 0.0025
	wantOutput := 0.005  // 500 / 1000 * 0.01
	wantTotal := wantInput + wantOutput

	if !closeEnough(row.InputCost, wantInput) {
		t.Errorf("row.InputCost = %v, want %v", row.InputCost, wantInput)
	}
	if !closeEnough(row.OutputCost, wantOutput) {
		t.Errorf("row.OutputCost = %v, want %v", row.OutputCost, wantOutput)
	}
	if !closeEnough(cost.TotalUSD(), wantTotal) {
		t.Errorf("cost.TotalUSD() = %v, want %v", cost.TotalUSD(), wantTotal)
	}
}

// TestCacheTTL — exercise the Refresher's stale→fresh→stale cycle.
//
// We don't actually sleep — that would make the test slow and flaky.
// Instead we inject a controllable clock and step it forward by hand.
// The contract under test is purely "Source was called N times after
// these advances", which is what mock-time gets us cleanly.
func TestCacheTTL(t *testing.T) {
	var calls int
	src := func() (map[string]Pricing, error) {
		calls++
		return map[string]Pricing{
			"gpt-4o": {InputPer1k: 0.0024, OutputPer1k: 0.0095},
		}, nil
	}

	now := time.Unix(1700000000, 0) // fixed reference point
	advance := func(d time.Duration) { now = now.Add(d) }

	r := &Refresher{
		Source:   src,
		CacheTTL: 1 * time.Second,
		clock:    func() time.Time { return now },
	}

	// First call → cache empty → Source must be called.
	if _, err := r.Get("gpt-4o"); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if calls != 1 {
		t.Errorf("after 1st Get: calls=%d, want 1", calls)
	}

	// Second call within TTL → cache hit → Source must NOT be called.
	advance(500 * time.Millisecond)
	if _, err := r.Get("gpt-4o"); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if calls != 1 {
		t.Errorf("after 2nd Get (within TTL): calls=%d, want still 1", calls)
	}

	// Advance past TTL → next Get should refresh.
	advance(2 * time.Second)
	if _, err := r.Get("gpt-4o"); err != nil {
		t.Fatalf("third Get: %v", err)
	}
	if calls != 2 {
		t.Errorf("after 3rd Get (past TTL): calls=%d, want 2", calls)
	}

	// And one more sanity check: a NIL source must surface ErrNoSource.
	empty := &Refresher{}
	if _, err := empty.Get("gpt-4o"); !errors.Is(err, ErrNoSource) {
		t.Errorf("empty refresher Get err = %v, want ErrNoSource", err)
	}
}

// TestUnknownModelReturnsZero — registering a model that's not in
// the pricing table should record the usage (so token counts stay
// accurate) but compute $0 cost and flag HasPricing=false.
//
// Crucially: this must NOT return an error. Upstream behavior is to
// silently degrade so a brand-new model still gets logged.
func TestUnknownModelReturnsZero(t *testing.T) {
	cost := NewTokenCost()
	row := cost.RegisterInvocation("gpt-9-unicorn", 1000, 500)

	if row.HasPricing {
		t.Errorf("row.HasPricing = true for unknown model, want false")
	}
	if !closeEnough(row.InputCost, 0) {
		t.Errorf("row.InputCost = %v for unknown model, want 0", row.InputCost)
	}
	if !closeEnough(row.OutputCost, 0) {
		t.Errorf("row.OutputCost = %v for unknown model, want 0", row.OutputCost)
	}
	// But the tokens should still count toward totals.
	if got, want := cost.Total.InputTok, 1000; got != want {
		t.Errorf("Total.InputTok = %d, want %d (unknown model tokens still tracked)", got, want)
	}

	// LookupPricing should agree that the model is unknown.
	if _, ok := LookupPricing("gpt-9-unicorn"); ok {
		t.Errorf("LookupPricing returned ok=true for unknown model")
	}
}

// TestEmbeddedPricingLoaded — verifies the //go:embed + init()
// parsing actually populated the lookup map. A regression here would
// mean init() silently produced an empty map (which would be very
// hard to debug downstream).
func TestEmbeddedPricingLoaded(t *testing.T) {
	p, ok := LookupPricing("gpt-4o-mini")
	if !ok {
		t.Fatalf("LookupPricing(gpt-4o-mini) ok=false, want true (embedded data should load)")
	}
	if p.InputPer1k <= 0 || p.OutputPer1k <= 0 {
		t.Errorf("embedded gpt-4o-mini has non-positive rates: %+v", p)
	}

	// We expect all four documented models to be loaded.
	for _, model := range []string{"gpt-4o", "gpt-4o-mini", "claude-3-5-sonnet", "gemini-1.5-pro"} {
		if _, ok := LookupPricing(model); !ok {
			t.Errorf("embedded pricing missing model %q", model)
		}
	}

	// Snapshot should be a real copy, not the underlying map.
	snap := EmbeddedPricingSnapshot()
	snap["mutation-canary"] = Pricing{InputPer1k: 99, OutputPer1k: 99}
	if _, leaked := LookupPricing("mutation-canary"); leaked {
		t.Errorf("EmbeddedPricingSnapshot returned shared map; mutation leaked into LookupPricing")
	}
}

// TestSummaryIsDeterministic — bonus check: Summary() output is
// stable across runs because we sort the per-model keys. Without
// this, the testdata/expected.txt golden would be flaky.
func TestSummaryIsDeterministic(t *testing.T) {
	cost1 := NewTokenCost()
	cost1.RegisterInvocation("claude-3-5-sonnet", 100, 50)
	cost1.RegisterInvocation("gpt-4o", 100, 50)
	cost1.RegisterInvocation("gemini-1.5-pro", 100, 50)

	cost2 := NewTokenCost()
	cost2.RegisterInvocation("gemini-1.5-pro", 100, 50)
	cost2.RegisterInvocation("gpt-4o", 100, 50)
	cost2.RegisterInvocation("claude-3-5-sonnet", 100, 50)

	if a, b := cost1.Summary(), cost2.Summary(); a != b {
		t.Errorf("Summary() not deterministic across insertion orders:\n%s\nvs\n%s", a, b)
	}
}
