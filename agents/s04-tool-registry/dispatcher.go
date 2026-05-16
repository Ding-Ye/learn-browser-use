package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// DefaultTimeout mirrors upstream's _ACTION_TIMEOUT_FALLBACK_S = 180s.
// The number is high because real tools may chain CDP calls or an
// extraction LLM (120s budget upstream) — but it has to be finite so
// a hung handler returns an error instead of wedging the agent loop.
// We expose it as a package-level constant so tests can refer to it
// without re-importing the upstream constant.
const DefaultTimeout = 180 * time.Second

// Dispatcher is the small glue between an ActionCall and the Registry.
// It owns the per-call timeout policy so individual tools don't each
// have to remember to wrap their work in context.WithTimeout.
//
// Why ctx-with-timeout instead of, say, a channel race? Two reasons:
//   1. context.Context is the canonical Go cancellation primitive;
//      a tool that calls into HTTP / CDP / a DB already accepts ctx
//      and will short-circuit on its own when Deadline fires.
//   2. ctx propagates: a tool that spawns goroutines passes ctx down,
//      and those goroutines also cancel when the dispatcher times out.
// A goroutine-and-channel solution would leak the inner goroutine.
//
// Trade-off worth naming: a tool that ignores ctx (e.g. time.Sleep
// instead of <-time.After(ctx)) will run to completion past the
// deadline. That's a tool bug, but the Dispatcher can't fix it from
// the outside without spawning a watcher goroutine. We accept this:
// in tests we use ctx-aware sleeps; in production, all real tools
// are HTTP/CDP wrappers that respect ctx.
type Dispatcher struct {
	Registry *Registry
	Timeout  time.Duration // 0 ⇒ DefaultTimeout
}

// Act runs one ActionCall against the Registry and returns a
// ContentBlock the caller can append straight into a message history.
// The contract:
//
//   - Tool not found: returns an error, no ContentBlock (the caller
//     can synthesize a tool_result if it wants to feed the LLM back).
//     We return error+block rather than swallowing because s12 will
//     want the typed error for telemetry while still feeding the
//     "tool unknown" message back to the LLM.
//   - Tool returned an error: returns a tool_result block with the
//     error stringified, plus the original error so the caller can
//     decide whether to retry or abort.
//   - Tool returned cleanly: tool_result block with the output.
//
// Notice that ToolName + Input are echoed into the returned block so
// downstream history-building doesn't have to remember which call
// produced which result.
func (d *Dispatcher) Act(ctx context.Context, call ActionCall) (ContentBlock, error) {
	if d.Registry == nil {
		return ContentBlock{}, errors.New("dispatcher: nil Registry")
	}

	tool, ok := d.Registry.Lookup(call.Name)
	if !ok {
		// Surface a clear error AND a tool_result the agent loop can
		// hand back to the LLM. Real registries see this whenever the
		// LLM hallucinates a tool name; we want both signals.
		msg := fmt.Sprintf("unknown action %q", call.Name)
		return ContentBlock{
			Type:   "tool_result",
			Name:   call.Name,
			Result: msg,
		}, fmt.Errorf("%s", msg)
	}

	timeout := d.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Normalize empty Input to "{}" so tools that don't take any
	// arguments still get a valid JSON object. Upstream does the
	// same when params is nil → pydantic empty model.
	raw := json.RawMessage(call.Input)
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}

	out, err := tool.Run(runCtx, raw)
	if err != nil {
		// Re-classify the typical timeout case so callers get a
		// stable error string ("action timed out after N") that's
		// easier to match on than ctx.Err().
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("action %q timed out after %s", call.Name, timeout)
		}
		return ContentBlock{
			Type:   "tool_result",
			Name:   call.Name,
			Input:  call.Input,
			Result: fmt.Sprintf("tool error: %v", err),
		}, err
	}

	return ContentBlock{
		Type:   "tool_result",
		Name:   call.Name,
		Input:  call.Input,
		Result: out,
	}, nil
}
