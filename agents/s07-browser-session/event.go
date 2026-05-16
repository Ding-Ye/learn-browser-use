package main

// event.go holds the minimal event types that flow through s07's
// EventBus during a session lifecycle. The three events here are
// deliberately tiny — just enough to demonstrate the "Session.Start()
// announces itself, watchdogs react" choreography without dragging
// in the 30+ event types the upstream Python ships (BrowserLaunchEvent,
// BrowserConnectedEvent, AgentFocusChangedEvent, SaveStorageStateEvent,
// etc.).
//
// Why so few events? In s06 we already established the Event/EventBus
// pattern. The novelty of s07 is the *lifecycle that owns the bus* —
// Start → fire SessionStartedEvent → emit some work-related events
// (NavigationEvent stands in for the dozens of mid-run events upstream)
// → Stop → fire SessionStoppedEvent. Three events is the smallest set
// that lets a watchdog see all three phases.
//
// Public fields are JSON-tagged so that AutoAttach's JSON-bridge can
// faithfully convert these into the equally-named structs declared in
// the `watchdogs` subpackage. The two struct families don't share a
// Go type; they share their *shape*. JSON is the lingua franca.

// SessionStartedEvent is emitted after Session.Start successfully
// opens the (stub) CDP connection and attaches every Watchdog. The
// canonical upstream analog is BrowserConnectedEvent, which fires once
// the CDP WebSocket is alive and watchdogs may safely send commands.
//
// A watchdog that wants to "do something on startup" subscribes to
// this event rather than being called explicitly — the loose coupling
// is the whole point of using a bus.
type SessionStartedEvent struct {
	// CDPURL echoes the (stubbed) endpoint we "connected" to. Upstream
	// supplies a ws://… URL; we use a fake string so the demo run
	// produces a deterministic line in the recorder log.
	CDPURL string `json:"cdp_url"`
}

// EventName implements the Event interface declared in eventbus.go.
// Returning the type name (not a hand-typed string literal) keeps the
// subscription string in sync with refactors — rename the struct, the
// bus key renames with it (you'd just edit one string here).
func (SessionStartedEvent) EventName() string { return "SessionStartedEvent" }

// SessionStoppedEvent fires after Session.Stop emits its stop signal
// and before the bus is told to clear its subscriber table. Watchdogs
// use this to flush state — a real DownloadsWatchdog would close any
// open file handles here.
//
// Reason is a freeform string ("user requested", "ctx cancelled",
// "restart") so watchdogs that care about *why* the session ended can
// branch on it.
type SessionStoppedEvent struct {
	Reason string `json:"reason"`
}

func (SessionStoppedEvent) EventName() string { return "SessionStoppedEvent" }

// NavigationEvent is the single mid-run event we ship in s07: it
// represents "the page navigated to URL". Upstream has separate
// NavigationStartedEvent and NavigationCompleteEvent, plus a slew of
// TabCreatedEvent/SwitchTabEvent friends — we collapse the family to
// one event so the demo and tests have *something* to fire between
// Start and Stop.
//
// In s09 the DOMService will subscribe to navigation events to
// invalidate its snapshot cache; here it's just a demonstration that
// watchdogs see in-flight events.
type NavigationEvent struct {
	URL string `json:"url"`
}

func (NavigationEvent) EventName() string { return "NavigationEvent" }
