package main

import (
	"context"
	"fmt"
	"os"
	"time"
)

// main.go is the s09 demo binary. It shows the four behaviors the
// chapter teaches, in one linear run:
//
//   1. First Get triggers the snapshot.
//   2. Second Get returns from cache (snapshot counter does NOT
//      increment).
//   3. NavigationEvent fires; the bus subscription invalidates the
//      cache; we update CurrentURL to point at "page B".
//   4. Third Get triggers a fresh snapshot (counter increments
//      again, the returned text now reflects the new page).
//
// Run: `go run .`
//
// The point of the dual ledger is to make the cache observable.
// Without the snapshot counter you'd see consistent text and have
// no way to tell whether the cache actually worked.
func main() {
	ctx := context.Background()

	// Build the stub snapshot + its call counter.
	snap, calls := NewStubSnapshot()

	// 30-second TTL — long enough that the demo never hits the safety
	// net; we want the explicit invalidation path to be the visible
	// trigger. Production tunes this per-app.
	cache := NewCache(30 * time.Second)
	bus := NewEventBus()

	svc := NewDOMService(bus, snap, cache)
	svc.CurrentURL = "https://a.example.com"

	fmt.Println("# DOMService cache + invalidation demo")
	fmt.Println()

	// Round 1: first call — cache miss → snapshot fires.
	state1, err := svc.Get(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Get #1: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[Get #1] snapshot calls so far: %d\n", *calls)
	fmt.Printf("         serialized text:\n%s\n", indent(state1.LLMText))
	fmt.Println()

	// Round 2: same URL, no event — cache hit, snapshot counter
	// should not move.
	state2, err := svc.Get(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Get #2: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[Get #2] snapshot calls so far: %d  (expected unchanged)\n", *calls)
	fmt.Printf("         text identical to Get #1? %v\n", state2.LLMText == state1.LLMText)
	fmt.Println()

	// Navigate. The subscriber on the bus invalidates the cache;
	// we also update CurrentURL so the next Get fetches page B.
	svc.CurrentURL = "https://b.example.com"
	if err := bus.Emit(ctx, NavigationEvent{URL: "https://b.example.com"}); err != nil {
		fmt.Fprintf(os.Stderr, "Emit: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[NavigationEvent emitted; CurrentURL → %s]\n\n", svc.CurrentURL)

	// Round 3: cache is empty + URL points to page B — snapshot
	// fires again, and the resulting text is different.
	state3, err := svc.Get(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Get #3: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[Get #3] snapshot calls so far: %d  (expected to be +1 from Get #2)\n", *calls)
	fmt.Printf("         serialized text:\n%s\n", indent(state3.LLMText))
	fmt.Println()

	fmt.Printf("Selector map size for Get #3: %d entries\n", len(state3.SelectorMap))
}

// indent prepends "  " to every line — keeps the demo output
// readable when the serialized text is multi-line.
func indent(s string) string {
	out := ""
	for _, line := range splitLines(s) {
		out += "  " + line + "\n"
	}
	return out
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
