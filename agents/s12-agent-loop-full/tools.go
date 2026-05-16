package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// tools.go ships the five tools the s12 demo registers with the
// Registry: search, click, type, scroll, done. Each tool is a small
// struct that holds a pointer to the BrowserSession (so it can drive
// the CDP recorder and the EventBus) and the DOMService (so it can
// look up SelectorMap entries).
//
// Why pointers to mutable state in tool structs? Upstream's Python
// tools take a `tools_service` arg that holds the same plumbing —
// browser session + filesystem + actions registry. In Go we match
// that by having each tool struct embed the references it needs. The
// downside is each tool is no longer a pure function; the upside is
// that tools that don't need a browser (e.g. "done") can be
// constructed without one.
//
// The schema JSON in each Schema() method is hand-written. That's a
// deliberate s12 simplification — s04 demonstrated reflection-based
// generation; here we keep things readable so the chapter is about
// integration, not about reflection mechanics.

// ---------------------------------------------------------------
//  SearchTool — "go to https://search.example/?q=..."
// ---------------------------------------------------------------

// SearchTool emits a NavigationEvent for a search URL containing the
// query. The DOMService then invalidates its cache and the next
// browser observation returns the "results" fixture.
type SearchTool struct {
	Session *BrowserSession
	DOM     *DOMService
}

// Name returns the registry key.
func (SearchTool) Name() string { return "search" }

// Description is the one-line nudge the LLM sees.
func (SearchTool) Description() string {
	return "Search the web for a query. Navigates to a results page."
}

// Schema returns the JSON-Schema for input args.
func (SearchTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "search",
		Description: "Search the web for a query. Navigates to a results page.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
	}
}

// Run accepts {"query": "..."} and emits a NavigationEvent pointing
// at "https://search.example/results?q=...". Returns a short
// confirmation string the agent can show the LLM.
func (s SearchTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("search: bad input: %w", err)
	}
	if strings.TrimSpace(args.Query) == "" {
		return "", fmt.Errorf("search: empty query")
	}
	url := fmt.Sprintf("https://search.example/results?q=%s",
		strings.ReplaceAll(args.Query, " ", "+"))
	if _, err := s.Session.Client.Send("Page.navigate", map[string]any{
		"url": url,
	}); err != nil {
		return "", fmt.Errorf("search: CDP navigate: %w", err)
	}
	if err := s.Session.Bus.Emit(ctx, NavigationEvent{URL: url}); err != nil {
		return "", fmt.Errorf("search: emit nav: %w", err)
	}
	return fmt.Sprintf("navigated to %s", url), nil
}

// ---------------------------------------------------------------
//  ClickTool — "click index N from the SelectorMap"
// ---------------------------------------------------------------

// ClickTool dispatches a CDP click against the BackendNodeID at the
// given SelectorMap index. For the demo it also emits a
// NavigationEvent if the clicked element happens to be a result link
// — that fakes the "click loads a new page" flow without needing a
// real browser.
type ClickTool struct {
	Session *BrowserSession
	DOM     *DOMService
}

// Name returns the registry key.
func (ClickTool) Name() string { return "click" }

// Description is the LLM-visible nudge.
func (ClickTool) Description() string { return "Click the element at index N." }

// Schema returns the JSON-Schema for input args.
func (ClickTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "click",
		Description: "Click the element at index N.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"index":{"type":"integer"}},"required":["index"]}`),
	}
}

// Run looks up the SelectorEntry, sends a fake DOM.dispatchMouseEvent
// frame, and if the click was on a result link emits a Navigation to
// a fake destination.
func (c ClickTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Index int `json:"index"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("click: bad input: %w", err)
	}
	dom, err := c.DOM.Get(ctx)
	if err != nil {
		return "", fmt.Errorf("click: get dom: %w", err)
	}
	entry, ok := dom.SelectorMap[args.Index]
	if !ok {
		return "", fmt.Errorf("click: no element at index %d", args.Index)
	}
	if _, err := c.Session.Client.Send("Input.dispatchMouseEvent", map[string]any{
		"type":           "mousePressed",
		"x":              entry.BBox.X + entry.BBox.W/2,
		"y":              entry.BBox.Y + entry.BBox.H/2,
		"backendNodeId":  entry.BackendNodeID,
		"button":         "left",
		"clickCount":     1,
	}); err != nil {
		return "", fmt.Errorf("click: CDP dispatch: %w", err)
	}

	// If we're on the results page and clicked a result link, fake a
	// page load to a synthetic destination so subsequent observations
	// show "the article".
	if strings.Contains(c.DOM.CurrentURL(), "results") {
		dest := fmt.Sprintf("https://article.example/%d", entry.BackendNodeID)
		if err := c.Session.Bus.Emit(ctx, NavigationEvent{URL: dest}); err != nil {
			return "", fmt.Errorf("click: emit nav: %w", err)
		}
		return fmt.Sprintf("clicked [%d] → navigated to %s", args.Index, dest), nil
	}
	return fmt.Sprintf("clicked [%d]", args.Index), nil
}

// ---------------------------------------------------------------
//  TypeTool — "type some text into an input"
// ---------------------------------------------------------------

// TypeTool dispatches Input.insertText against the element at the
// SelectorMap index. No NavigationEvent — typing alone doesn't change
// the page in our demo (we'd need a follow-up click on Search).
type TypeTool struct {
	Session *BrowserSession
	DOM     *DOMService
}

// Name returns the registry key.
func (TypeTool) Name() string { return "type" }

// Description is the LLM-visible nudge.
func (TypeTool) Description() string { return "Type text into the input at index N." }

// Schema returns the JSON-Schema for input args.
func (TypeTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "type",
		Description: "Type text into the input at index N.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"index":{"type":"integer"},"text":{"type":"string"}},"required":["index","text"]}`),
	}
}

// Run unmarshals {"index": N, "text": "..."} and sends Input.insertText.
func (t TypeTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Index int    `json:"index"`
		Text  string `json:"text"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("type: bad input: %w", err)
	}
	dom, err := t.DOM.Get(ctx)
	if err != nil {
		return "", fmt.Errorf("type: get dom: %w", err)
	}
	entry, ok := dom.SelectorMap[args.Index]
	if !ok {
		return "", fmt.Errorf("type: no element at index %d", args.Index)
	}
	if _, err := t.Session.Client.Send("Input.insertText", map[string]any{
		"text":          args.Text,
		"backendNodeId": entry.BackendNodeID,
	}); err != nil {
		return "", fmt.Errorf("type: CDP dispatch: %w", err)
	}
	return fmt.Sprintf("typed %q into [%d]", args.Text, args.Index), nil
}

// ---------------------------------------------------------------
//  ScrollTool — "scroll the viewport by N pixels"
// ---------------------------------------------------------------

// ScrollTool dispatches Input.dispatchMouseEvent of type wheel.
// Doesn't touch the DOMService — we always synthesise the same
// fixture regardless of scroll position, since the s12 demo doesn't
// need infinite-scroll behaviour to teach the integration story.
type ScrollTool struct {
	Session *BrowserSession
}

// Name returns the registry key.
func (ScrollTool) Name() string { return "scroll" }

// Description is the LLM-visible nudge.
func (ScrollTool) Description() string { return "Scroll the viewport by N pixels (positive = down)." }

// Schema returns the JSON-Schema for input args.
func (ScrollTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "scroll",
		Description: "Scroll the viewport by N pixels (positive = down).",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"dy":{"type":"integer"}},"required":["dy"]}`),
	}
}

// Run unmarshals {"dy": N} and sends a wheel event.
func (s ScrollTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		DY int `json:"dy"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("scroll: bad input: %w", err)
	}
	if _, err := s.Session.Client.Send("Input.dispatchMouseEvent", map[string]any{
		"type":      "mouseWheel",
		"deltaY":    args.DY,
		"x":         100,
		"y":         100,
	}); err != nil {
		return "", fmt.Errorf("scroll: CDP dispatch: %w", err)
	}
	return fmt.Sprintf("scrolled by %d", args.DY), nil
}

// ---------------------------------------------------------------
//  DoneTool — "we're done, here's the final answer"
// ---------------------------------------------------------------

// DoneTool returns a sentinel that the agent loop recognizes as the
// stop signal. Upstream encodes this as ActionResult(is_done=True);
// we use a magic prefix on the result string ("__done__:") because
// keeping the ContentBlock shape uniform is simpler.
//
// The "answer" field carries whatever final text the LLM wants the
// human to see. Agent.Run returns this as its (string, error) tuple.
type DoneTool struct{}

// DoneResultPrefix is the magic marker the loop scans for. Public so
// agent.go can refer to it by name instead of duplicating a string
// literal across files.
const DoneResultPrefix = "__done__:"

// Name returns the registry key.
func (DoneTool) Name() string { return "done" }

// Description is the LLM-visible nudge.
func (DoneTool) Description() string {
	return "Signal that the task is complete. Provide the final answer."
}

// Schema returns the JSON-Schema for input args.
func (DoneTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "done",
		Description: "Signal that the task is complete. Provide the final answer.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}`),
	}
}

// Run returns DoneResultPrefix + answer so the loop can detect it.
func (DoneTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("done: bad input: %w", err)
	}
	return DoneResultPrefix + args.Answer, nil
}

// RegisterDefaultTools wires all five tools into the registry. Used
// by main.go and the test helpers so the wiring is in one place.
func RegisterDefaultTools(reg *Registry, sess *BrowserSession, dom *DOMService) {
	reg.MustRegister(SearchTool{Session: sess, DOM: dom})
	reg.MustRegister(ClickTool{Session: sess, DOM: dom})
	reg.MustRegister(TypeTool{Session: sess, DOM: dom})
	reg.MustRegister(ScrollTool{Session: sess})
	reg.MustRegister(DoneTool{})
}
