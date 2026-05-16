// Package s06 — Event types for the watchdog event bus.
//
// Upstream parallel: browser_use/browser/events.py defines a swarm of
// BaseEvent subclasses (TabCreatedEvent, DownloadStartedEvent, ...). In
// Python the Event class is a Pydantic model carrying validation,
// parent_id chain, and dispatch metadata. We need almost none of that for
// teaching purposes — what matters is:
//
//  1. Every event has a stable string name (its type name).
//  2. The bus dispatches on that name string.
//
// So the canonical Go shape is a tiny interface plus plain struct events.
package s06

// Event is the interface every published event must satisfy.
//
// We deliberately keep this minimal. The bus only needs a string handle
// for routing; everything else (payload fields, validation, parent links)
// is each event struct's own business. Compare upstream:
//
//	class BaseEvent(BaseModel):
//	    event_id: str = Field(default_factory=uuid7str)
//	    event_type: str = Field(default_factory=...)
//	    event_parent_id: str | None = None
//	    ...
//
// Upstream's event_type comes from the Pydantic class name; we replicate
// that by convention — each EventName() returns the Go type name.
type Event interface {
	EventName() string
}

// DownloadStartedEvent fires when the browser observes a download
// beginning. Mirrors browser_use/browser/events.py::DownloadStartedEvent.
type DownloadStartedEvent struct {
	URL      string
	Filename string
}

// EventName returns the dispatch key for this event.
func (DownloadStartedEvent) EventName() string { return "DownloadStartedEvent" }

// JSDialogOpenedEvent fires when a Page.javascriptDialogOpening signal
// arrives (alert / confirm / prompt). Mirrors the upstream popups
// watchdog's input from CDP. We collapse the CDP event shape to two
// string fields — type ("alert" | "confirm" | "prompt") and message.
type JSDialogOpenedEvent struct {
	Message string
	Type    string // "alert" | "confirm" | "prompt" | "beforeunload"
}

// EventName returns the dispatch key for this event.
func (JSDialogOpenedEvent) EventName() string { return "JSDialogOpenedEvent" }

// NavigationEvent fires after a top-level frame navigates. We emit it
// from main.go so the demo shows the bus correctly drops unsubscribed
// events on the floor (no watchdog subscribes to it).
type NavigationEvent struct {
	URL string
}

// EventName returns the dispatch key for this event.
func (NavigationEvent) EventName() string { return "NavigationEvent" }
