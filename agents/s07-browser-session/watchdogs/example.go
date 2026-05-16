// Package watchdogs hosts example Watchdog implementations that the
// s07 demo can plug into a BrowserSession. The package is its own
// directory so that future watchdogs (Downloads, Popups, Security…)
// can land here without bloating the session module's root.
//
// Important constraint: this file does NOT import the parent module's
// EventBus / Watchdog / event types. It re-declares the *event
// structs* it cares about locally. The reason is the same as elsewhere
// in s07 — modules in this curriculum are self-contained. The price
// is duplicated structs; the payoff is `go build ./watchdogs` works
// in isolation.
//
// Wait, but the session DOES need to find this watchdog's handlers
// via reflection — and reflection matches by struct type identity.
// How does it work if the event types are duplicated?
//
//   It works because reflect.AutoAttach (in the parent package)
//   constructs the event-name *string* by calling EventName() on a
//   zero value of whatever struct the handler's signature requires.
//   As long as the *string returned* matches what the parent emits,
//   the bus routes the event correctly. The Go type identity of the
//   parameter doesn't have to be the same — only the EventName()
//   contract has to match.
//
// In s12 (or a real integration) you'd lift event types into a shared
// internal package. For teaching s07 we keep both copies inline so
// the file is readable end-to-end.
package watchdogs

import (
	"context"
	"fmt"
	"sync"
)

// LoggingWatchdog is the canonical example watchdog: it records every
// lifecycle event into Events for tests to inspect, and exposes a
// thread-safe getter. In the demo binary main() prints the slice to
// stdout, so the reader sees both "what CDP frames we sent" and "what
// the watchdog observed" side by side.
//
// A real watchdog would do something substantive — write a download
// to disk, dismiss a JS alert, block a navigation to a disallowed
// domain. LoggingWatchdog is intentionally inert because s07's job
// is to teach the *plumbing*, not the policy.
type LoggingWatchdog struct {
	mu     sync.Mutex
	Events []string
}

// NewLoggingWatchdog returns a ready-to-attach watchdog. Public
// constructor over `&LoggingWatchdog{}` for future-proofing.
func NewLoggingWatchdog() *LoggingWatchdog {
	return &LoggingWatchdog{}
}

// OnSessionStartedEvent fires when the parent BrowserSession finishes
// its Start sequence. The method name is the contract AutoAttach
// looks for: prefix "On" + the event struct name (after stripping the
// `*` pointer marker). We record the CDPURL to demonstrate that the
// watchdog can pull data off the event payload.
func (l *LoggingWatchdog) OnSessionStartedEvent(ctx context.Context, e *SessionStartedEvent) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Events = append(l.Events, fmt.Sprintf("started: cdp=%s", e.CDPURL))
	return nil
}

// OnNavigationEvent observes mid-run navigation. In s09 the real
// DOMService will subscribe to this exact event to invalidate its
// snapshot cache; here we only log so the demo can show the watchdog
// catching it.
func (l *LoggingWatchdog) OnNavigationEvent(ctx context.Context, e *NavigationEvent) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Events = append(l.Events, fmt.Sprintf("navigate: url=%s", e.URL))
	return nil
}

// OnSessionStoppedEvent is the symmetric counterpart to
// OnSessionStartedEvent — a real watchdog would flush state here. We
// record the reason so tests can assert "stop happened, and we know
// why".
func (l *LoggingWatchdog) OnSessionStoppedEvent(ctx context.Context, e *SessionStoppedEvent) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Events = append(l.Events, fmt.Sprintf("stopped: reason=%s", e.Reason))
	return nil
}

// Snapshot returns a copy of Events so callers can iterate without
// risking concurrent mutation. The watchdog itself never returns the
// raw slice — that would be a tiny but real data-race trap.
func (l *LoggingWatchdog) Snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.Events))
	copy(out, l.Events)
	return out
}

// --- Locally-declared event mirrors -----------------------------------------
//
// These structs MUST keep their EventName() strings in sync with the
// parent package's structs. They don't have to be the same Go type —
// AutoAttach matches by the EventName() return value, not by type
// identity. See the package doc comment for the reasoning.

// SessionStartedEvent mirrors the parent's event of the same name.
// The JSON tag matches the parent's so AutoAttach's marshal/unmarshal
// bridge populates CDPURL correctly.
type SessionStartedEvent struct {
	CDPURL string `json:"cdp_url"`
}

func (SessionStartedEvent) EventName() string { return "SessionStartedEvent" }

// SessionStoppedEvent mirrors the parent's event of the same name.
type SessionStoppedEvent struct {
	Reason string `json:"reason"`
}

func (SessionStoppedEvent) EventName() string { return "SessionStoppedEvent" }

// NavigationEvent mirrors the parent's event of the same name.
type NavigationEvent struct {
	URL string `json:"url"`
}

func (NavigationEvent) EventName() string { return "NavigationEvent" }
