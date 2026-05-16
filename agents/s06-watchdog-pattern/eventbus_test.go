package s06

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// -------------------------------------------------------------------------
// 1. TestEmitCallsHandler — the happy path.
// -------------------------------------------------------------------------
func TestEmitCallsHandler(t *testing.T) {
	bus := NewEventBus()
	var got DownloadStartedEvent
	bus.Subscribe("DownloadStartedEvent", func(ctx context.Context, e Event) error {
		got = e.(DownloadStartedEvent)
		return nil
	})

	want := DownloadStartedEvent{URL: "https://x/y", Filename: "y.pdf"}
	if err := bus.Emit(context.Background(), want); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if got != want {
		t.Errorf("handler did not see event: got %+v, want %+v", got, want)
	}
}

// -------------------------------------------------------------------------
// 2. TestAutoRegisterByMethodName — the reflection contract.
// A Watchdog with OnDownloadStartedEvent gets registered for
// "DownloadStartedEvent", a sibling stray method is ignored, and a typoed
// method (event-type mismatch) is silently skipped.
// -------------------------------------------------------------------------
type fakeWD struct {
	calls atomic.Int32
}

func (w *fakeWD) OnDownloadStartedEvent(ctx context.Context, e *DownloadStartedEvent) error {
	w.calls.Add(1)
	return nil
}

// Helper method that should NOT register — wrong name shape.
func (w *fakeWD) LoggerHelper() string { return "noop" }

// Helper method that should NOT register — pointer-to-wrong-struct.
// AutoAttach must catch the suffix/argument mismatch.
func (w *fakeWD) OnNavigationEvent(ctx context.Context, e *DownloadStartedEvent) error {
	w.calls.Add(1) // would be a bug if this ever fired
	return nil
}

func TestAutoRegisterByMethodName(t *testing.T) {
	bus := NewEventBus()
	wd := &fakeWD{}
	registered, err := AutoAttach(wd, bus)
	if err != nil {
		t.Fatalf("AutoAttach: %v", err)
	}
	// Only the well-shaped method registers.
	if len(registered) != 1 || registered[0] != "DownloadStartedEvent" {
		t.Fatalf("expected [DownloadStartedEvent], got %v", registered)
	}
	if got := bus.HandlerCount("DownloadStartedEvent"); got != 1 {
		t.Errorf("DownloadStartedEvent handler count = %d, want 1", got)
	}
	if got := bus.HandlerCount("NavigationEvent"); got != 0 {
		t.Errorf("NavigationEvent handler count = %d, want 0 (suffix mismatch)", got)
	}

	// And dispatching reaches the handler.
	if err := bus.Emit(context.Background(), DownloadStartedEvent{URL: "u"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if wd.calls.Load() != 1 {
		t.Errorf("OnDownloadStartedEvent calls = %d, want 1", wd.calls.Load())
	}
}

// -------------------------------------------------------------------------
// 3. TestConcurrentEmitsNoDeadlock — 100 parallel Emit() calls finish.
// -------------------------------------------------------------------------
func TestConcurrentEmitsNoDeadlock(t *testing.T) {
	bus := NewEventBus()
	var counter atomic.Int32
	bus.Subscribe("DownloadStartedEvent", func(ctx context.Context, e Event) error {
		counter.Add(1)
		return nil
	})
	bus.Subscribe("DownloadStartedEvent", func(ctx context.Context, e Event) error {
		counter.Add(1)
		return nil
	})

	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			_ = bus.Emit(context.Background(), DownloadStartedEvent{URL: "u"})
		}(i)
	}
	wg.Wait()

	// Each emit reaches both handlers → 2 * N increments.
	if got, want := counter.Load(), int32(2*N); got != want {
		t.Errorf("counter = %d, want %d", got, want)
	}
}

// -------------------------------------------------------------------------
// 4. TestUnknownEventIgnored — emitting an event nobody subscribed to is
// not an error.
// -------------------------------------------------------------------------
func TestUnknownEventIgnored(t *testing.T) {
	bus := NewEventBus()
	if err := bus.Emit(context.Background(), NavigationEvent{URL: "https://nowhere"}); err != nil {
		t.Errorf("Emit on unsubscribed event returned error: %v", err)
	}
}

// -------------------------------------------------------------------------
// 5. TestMultipleHandlersOrderedRegistration — two handlers for the same
// event fire in the order they were subscribed.
// -------------------------------------------------------------------------
func TestMultipleHandlersOrderedRegistration(t *testing.T) {
	bus := NewEventBus()
	var order []string
	bus.Subscribe("DownloadStartedEvent", func(ctx context.Context, e Event) error {
		order = append(order, "first")
		return nil
	})
	bus.Subscribe("DownloadStartedEvent", func(ctx context.Context, e Event) error {
		order = append(order, "second")
		return nil
	})
	bus.Subscribe("DownloadStartedEvent", func(ctx context.Context, e Event) error {
		order = append(order, "third")
		return nil
	})
	if err := bus.Emit(context.Background(), DownloadStartedEvent{URL: "u"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	want := []string{"first", "second", "third"}
	if len(order) != len(want) {
		t.Fatalf("order length = %d, want %d (%v)", len(order), len(want), order)
	}
	for i, s := range want {
		if order[i] != s {
			t.Errorf("order[%d] = %q, want %q", i, order[i], s)
		}
	}
}

// -------------------------------------------------------------------------
// 6. (bonus) TestHandlerErrorsAggregated — when multiple handlers
// return errors, Emit joins them all so the caller sees every failure.
// -------------------------------------------------------------------------
func TestHandlerErrorsAggregated(t *testing.T) {
	bus := NewEventBus()
	errA := errors.New("a failed")
	errB := errors.New("b failed")
	bus.Subscribe("DownloadStartedEvent", func(ctx context.Context, e Event) error { return errA })
	bus.Subscribe("DownloadStartedEvent", func(ctx context.Context, e Event) error { return nil })
	bus.Subscribe("DownloadStartedEvent", func(ctx context.Context, e Event) error { return errB })

	err := bus.Emit(context.Background(), DownloadStartedEvent{URL: "u"})
	if err == nil {
		t.Fatalf("expected aggregated error, got nil")
	}
	if !errors.Is(err, errA) || !errors.Is(err, errB) {
		t.Errorf("expected join of errA + errB, got %v", err)
	}
}
