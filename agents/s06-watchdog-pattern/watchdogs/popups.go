package watchdogs

import (
	"context"

	s06 "learn-browser-use/s06-watchdog-pattern"
)

// PopupsWatchdog mirrors browser_use/browser/watchdogs/popups_watchdog.py
// at sharply reduced scope: it records each JavaScript dialog and
// (notionally) "accepts" it.
//
// Upstream uses CDP Page.handleJavaScriptDialog with accept=true/false
// based on dialog type (alert / confirm → accept; prompt → cancel). We
// keep that decision logic so the file is more than a copy-paste of
// downloads.go, but we don't actually call CDP — we just record the
// chosen disposition.
type PopupsWatchdog struct {
	Handled []s06.JSDialogOpenedEvent
	// Accepted parallels Handled but stores the bool the real watchdog
	// would have passed to Page.handleJavaScriptDialog. alert/confirm/
	// beforeunload → true, prompt → false.
	Accepted []bool
}

// OnJSDialogOpenedEvent is the auto-discovered handler for
// JSDialogOpenedEvent. The shape and naming are what AutoAttach matches
// on; the body is local to this concern.
func (w *PopupsWatchdog) OnJSDialogOpenedEvent(ctx context.Context, e *s06.JSDialogOpenedEvent) error {
	_ = ctx
	w.Handled = append(w.Handled, *e)
	switch e.Type {
	case "alert", "confirm", "beforeunload":
		w.Accepted = append(w.Accepted, true)
	default:
		// "prompt" and unknown types → click Cancel.
		w.Accepted = append(w.Accepted, false)
	}
	return nil
}
