package main

import (
	"context"
	"fmt"
	"strings"
)

// Action is the smallest "thing the agent can do" abstraction.
// In the real browser-use codebase this is the Tool interface in
// browser_use/tools/registry/service.py + browser_use/tools/service.py.
// Each Action has a name + a Run(input) → result.
type Action interface {
	Name() string
	Run(ctx context.Context, input string) (string, error)
}

// SearchAction simulates a browser search. It does NOT touch the network.
// It hard-codes 3 fake "results" so the loop has something to observe.
//
// In a real browser agent, search would: open browser → navigate to the
// engine → fill the search box → press Enter → wait for results → return
// the result list as a DOM snapshot. We compress all of that to a one-line
// fixture so the loop's flow stays the star of this session.
type SearchAction struct{}

func (SearchAction) Name() string { return "search" }

func (SearchAction) Run(ctx context.Context, input string) (string, error) {
	query := strings.TrimSpace(input)
	if query == "" {
		return "", fmt.Errorf("search requires a non-empty query")
	}
	// Always returns the same 3 stub results. Tests can rely on this output.
	return fmt.Sprintf(
		"RESULT: top 3 hits for %q:\n  1. https://example.com/%s\n  2. https://en.wikipedia.org/wiki/%s\n  3. https://github.com/search?q=%s",
		query, query, query, query,
	), nil
}

// NavigateAction simulates loading a URL. Like Search, it does not actually
// fetch anything — it just confirms what the agent would have done.
type NavigateAction struct{}

func (NavigateAction) Name() string { return "navigate" }

func (NavigateAction) Run(ctx context.Context, input string) (string, error) {
	url := strings.TrimSpace(input)
	if url == "" {
		return "", fmt.Errorf("navigate requires a URL")
	}
	return fmt.Sprintf("RESULT: page loaded %s (200 OK)", url), nil
}

// DoneAction is the sentinel "task is finished" action. The loop sees it
// and terminates. In browser-use, `browser_use/tools/service.py` ships a
// real `done(text=...)` action that's how the LLM signals success.
type DoneAction struct{}

func (DoneAction) Name() string { return "done" }

func (DoneAction) Run(ctx context.Context, input string) (string, error) {
	return "RESULT: agent finished", nil
}
