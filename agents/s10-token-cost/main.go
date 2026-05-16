package main

import (
	"fmt"
	"os"
)

// main.go is the demo entry point. It does three things:
//
//   1. Build a fresh TokenCost ledger backed by the embedded pricing
//      table.
//   2. Simulate five LLM invocations across three different models —
//      varying token counts so the per-model breakdown shows real
//      differences.
//   3. Print Summary() to stdout.
//
// The shape mimics what s12's full agent loop will do: every
// `Provider.Invoke` returns a Response with InputTok/OutputTok fields,
// and the loop body just calls `cost.RegisterInvocation(resp.Model,
// resp.InputTok, resp.OutputTok)` once per step. The cost ledger has
// zero coupling to the rest of the agent — it's a side-channel
// observer.

func main() {
	cost := NewTokenCost()

	// Five fake invocations. Token counts chosen so the cheapest
	// model (gpt-4o-mini) and the priciest (claude-3-5-sonnet) both
	// show up, and so the per-model totals aren't all identical.
	type fakeCall struct {
		model    string
		inTok    int
		outTok   int
		note     string
	}
	calls := []fakeCall{
		{"gpt-4o", 1500, 320, "step 1: planning"},
		{"gpt-4o", 2100, 180, "step 2: refine"},
		{"claude-3-5-sonnet", 1800, 240, "step 3: validation"},
		{"gpt-4o-mini", 9500, 850, "step 4: bulk summarization (cheap model)"},
		{"gpt-4o-mini", 8200, 720, "step 5: more bulk work"},
	}

	fmt.Println("# Registering 5 fake invocations across 3 models")
	for i, c := range calls {
		row := cost.RegisterInvocation(c.model, c.inTok, c.outTok)
		fmt.Printf("  [%d] %-19s  in=%d  out=%d  -> $%.4f  (%s)\n",
			i, row.Model, row.InputTok, row.OutputTok,
			row.InputCost+row.OutputCost, c.note)
	}

	fmt.Println()
	fmt.Println(cost.Summary())

	// Demonstrate the Refresher path too. Spec calls for "optional
	// refresh from a remote source (stubbed)" — this is the stub.
	fmt.Println("# Refresher (stubbed remote) snapshot")
	r := NewStubRefresher()
	gpt4o, err := r.Get("gpt-4o")
	if err != nil {
		fmt.Fprintf(os.Stderr, "refresher.Get error: %v\n", err)
		os.Exit(1)
	}
	embedded, _ := LookupPricing("gpt-4o")
	fmt.Printf("  embedded gpt-4o:    in=$%.5f/1k  out=$%.5f/1k\n",
		embedded.InputPer1k, embedded.OutputPer1k)
	fmt.Printf("  refreshed gpt-4o:   in=$%.5f/1k  out=$%.5f/1k\n",
		gpt4o.InputPer1k, gpt4o.OutputPer1k)
	fmt.Println("  (refreshed values are intentionally a hair different to make")
	fmt.Println("   'embedded vs refreshed' visible in this demo)")
}
