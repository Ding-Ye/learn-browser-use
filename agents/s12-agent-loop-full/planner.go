package main

import (
	"context"
	"fmt"
	"strings"
)

// planner.go ships the s12 planning policy. Upstream's agent has a
// full reflection sub-loop (`Agent._update_plan_from_model_output` +
// `_render_plan_description`) that runs every N steps; we compress to
// the irreducible shape:
//
//   - Every PlanEvery steps, the loop calls Plan(ctx, provider, msgs).
//   - The planner issues ONE LLM call with a "reflect on history, list
//     the next 3 steps" prompt and returns the text it gets back.
//   - The text is injected as a system message into the history; the
//     next regular turn sees it.
//
// Why "every N steps" and not "on every step"? Because planning calls
// add ~1k tokens of overhead per step, which is wasteful on tight
// loops. Upstream defaults to 4 (`settings.planner_interval`); we use
// 5 here because the integration test wants a clean "step 5, 10"
// firing window inside MaxSteps=12.
//
// The planner uses the SAME Provider as the main loop. Upstream
// allows a `settings.planner_llm` override (e.g. cheap model for
// planning, premium for action). We keep one provider for teaching
// clarity; the seam is the function arg `p Provider`, so plugging a
// second provider in for planning is a one-line caller change.

// PlannerPromptText is the system prompt the planner injects. The
// constant is exported so tests can match against it.
const PlannerPromptText = "[PLANNER] Reflect on history so far. List the next 3 concrete steps to make progress on the task. Be terse."

// Plan executes one planner turn against p and returns the planner's
// text. The planner is NOT given the tool schemas — we want pure
// reflection, not another tool_use round. A non-empty reply is
// returned as-is; an empty reply or any error surfaces to the caller.
//
// The function is deliberately small (~25 lines) because the
// integration chapter's spotlight is on agent.go; planner.go just
// names the seam.
func Plan(ctx context.Context, p Provider, history []Message) (string, error) {
	// Construct the planner prompt: the original task (History[0]) + a
	// system reflection ask. We DO NOT include tool-call results; the
	// planner is supposed to think at a higher level than "what did
	// click[3] return". Upstream calls this "abstraction granularity"
	// and gives the planner only the user-message tail.
	plannerMsgs := []Message{}
	if len(history) > 0 {
		plannerMsgs = append(plannerMsgs, history[0])
	}
	plannerMsgs = append(plannerMsgs, Message{
		Role: "system",
		Content: []ContentBlock{{
			Type: "text",
			Text: PlannerPromptText,
		}},
	})

	resp, err := p.Invoke(ctx, plannerMsgs, nil)
	if err != nil {
		return "", fmt.Errorf("planner invoke: %w", err)
	}
	text := strings.TrimSpace(resp.Text)
	if text == "" {
		return "", fmt.Errorf("planner returned empty text")
	}
	return text, nil
}
