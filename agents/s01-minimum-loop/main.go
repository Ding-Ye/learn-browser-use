package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
)

// s01-minimum-loop — a 200-line browser-agent core that ties together
// fake provider + fake actions + a real loop.
//
// Run: go run . "search hacker news"
// Run: go run . -v "navigate https://example.com"
// Run: go run . -v -max-steps 3 "do nothing here"
//
// This binary intentionally does NOT call any LLM API or open any browser.
// It is the structural skeleton on which s02..s12 add real components.
func main() {
	verbose := flag.Bool("v", false, "print every step (assistant text + action results)")
	maxSteps := flag.Int("max-steps", 5, "max agent steps before giving up")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(),
			"usage: s01 [-v] [-max-steps N] <task>\n\n"+
				"  This is a synthetic browser agent: no real LLM, no real browser.\n"+
				"  Try:\n"+
				"    s01 \"search hacker news\"           # → emits a search action\n"+
				"    s01 \"navigate https://example.com\" # → emits a navigate action\n"+
				"    s01 -v \"nothing to do\"             # → ends immediately\n")
	}
	flag.Parse()

	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	task := strings.Join(flag.Args(), " ")

	var verboseW *os.File
	if *verbose {
		verboseW = os.Stderr
	}

	agent := &Agent{
		Provider: &FakeProvider{},
		Actions:  []Action{SearchAction{}, NavigateAction{}, DoneAction{}},
		MaxSteps: *maxSteps,
		Verbose:  verboseW,
	}

	final, err := agent.Run(context.Background(), task)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(final)
}
