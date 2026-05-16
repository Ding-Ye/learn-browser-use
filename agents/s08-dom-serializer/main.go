package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// main is the CLI demo for s08-dom-serializer.
//
// It does three things in order:
//
//  1. Read `testdata/snapshot.json` (a hand-crafted Chromium-shape DOM
//     with ~20 nodes including the edge cases the test golden file
//     covers: a hidden div, an off-viewport link, a nested
//     button-inside-anchor pair, a high-z modal overlay).
//  2. Run the serializer with a 1280×800 viewport.
//  3. Print:
//       - the LLMText (what the model would see),
//       - the SelectorMap (so the reader can map "click [3]" back to
//         the click target on the page).
//
// The point is that you can `go run .` and immediately see what the
// LLM-shaped output looks like for a non-trivial fixture — the docs and
// tests both reference this output, so debugging is "did the demo print
// what you expected?".
func main() {
	raw, err := os.ReadFile("testdata/snapshot.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "read snapshot: %v\n", err)
		os.Exit(1)
	}

	var root DOMNode
	if err := json.Unmarshal(raw, &root); err != nil {
		fmt.Fprintf(os.Stderr, "parse snapshot: %v\n", err)
		os.Exit(1)
	}

	ser := &Serializer{ViewportWidth: 1280, ViewportHeight: 800}
	out := ser.Serialize(&root)

	fmt.Println("# LLM-facing serialization")
	fmt.Println()
	fmt.Println(out.LLMText)
	fmt.Println()
	fmt.Printf("# SelectorMap (%d entries)\n", len(out.SelectorMap))
	keys := make([]int, 0, len(out.SelectorMap))
	for k := range out.SelectorMap {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		r := out.SelectorMap[k]
		fmt.Printf("  [%d] → x=%d y=%d w=%d h=%d\n", k, r.X, r.Y, r.W, r.H)
	}
}
