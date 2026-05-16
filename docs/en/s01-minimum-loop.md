---
title: "s01 · Minimum agent loop"
chapter: 1
slug: s01-minimum-loop
est_read_min: 12
---

# s01 · Minimum agent loop

> Teaching focus: the core of browser-use is a `loop(observe → think → act)`. We extract just the loop body into ~200 lines of Go — no real LLM, no real browser — to make the shape obvious.

---

## Problem / 问题

A browser-use-style agent looks mysterious from the outside: it reads webpages, decides which button to click, fills forms, paginates. But the upstream `browser_use/agent/service.py` is 4,131 lines — diving straight in drowns the reader in Pydantic models, async tasks, watchdog events, message compaction, and planner reflection loops.

The core, however, is shockingly plain: **a `for` loop**. Each iteration:

- Start from the previous browser state (observe)
- Ask the LLM what to do next (think)
- Run the action, feed the new state back to the loop (act)

s01's goal is to turn that 3-line mental model into 200 lines of compilable, testable Go — with everything non-essential (real LLM, real CDP, DOM trees, compaction, watchdogs) cut out. After this session the reader has the entire agent skeleton in their head; the remaining 11 sessions just add muscles to the bones.

## Solution / 解决方案

Split the agent into three roles:

1. **Provider**: does the "thinking" — given conversation history, returns the next action to run. s01 uses `FakeProvider`: it greps the task for keywords and picks a hard-coded action.
2. **Action**: does the "execution" — given an input string, returns a result. s01 ships three stubs: `SearchAction` / `NavigateAction` / `DoneAction`.
3. **Agent.Run**: does the "looping" — feeds Provider's output to the right Action, sticks the Action result back into Provider, until `end_turn`.

Key decisions:

1. **Three `StopReason` states**: `end_turn` (terminate), `tool_use` (run actions and continue), `max_tokens` (fail with truncation error). This mirrors real LLM API protocols; s02 adapting OpenAI is a 1-to-1 match.
2. **History is `[]Message`, not a string**: every turn is `Message{Role, Content[]}`; content blocks are typed `text` / `tool_use` / `tool_result`. This is the unified shape both Anthropic and OpenAI Chat Completions accept.
3. **`Provider` and `Action` are interfaces; the loop is interface-only**: the `Agent` struct holds `Provider Provider; Actions []Action`. When s02 swaps `FakeProvider` for `OpenAIProvider`, not a single line in `loop.go` needs to change.

## How It Works / 工作原理

```
┌──────────────────────────────────────────────────────────────┐
│                      Agent.Run(task)                         │
│                                                              │
│      ┌──────────┐  invoke(msgs)   ┌─────────────┐            │
│  ┌─→ │ Provider │ ──────────────→ │ Response{   │            │
│  │   │  (Fake)  │                 │   Text,     │            │
│  │   └──────────┘                 │   Actions,  │            │
│  │                                │   StopReason│            │
│  │                                └──────┬──────┘            │
│  │                                       │                   │
│  │            switch StopReason          ▼                   │
│  │   ┌─────────────────────────────────────────────┐         │
│  │   │ end_turn       → return final text          │         │
│  │   │ tool_use       → run actions, append result │ ─┐      │
│  │   │ max_tokens     → error                      │  │      │
│  │   └─────────────────────────────────────────────┘  │      │
│  │                                                    │      │
│  └────────────────────────────────────────────────────┘      │
└──────────────────────────────────────────────────────────────┘
```

Core 60 lines (excerpt from `agents/s01-minimum-loop/loop.go`):

```go
func (a *Agent) Run(ctx context.Context, task string) (string, error) {
    if a.MaxSteps <= 0 { a.MaxSteps = 10 }
    byName := map[string]Action{}
    for _, act := range a.Actions { byName[act.Name()] = act }

    msgs := []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: task}}}}

    for step := 0; step < a.MaxSteps; step++ {
        resp, err := a.Provider.Invoke(ctx, msgs)
        if err != nil { return "", fmt.Errorf("step %d invoke: %w", step, err) }

        // append the assistant turn (text + planned actions)
        assistantContent := []ContentBlock{{Type: "text", Text: resp.Text}}
        for _, act := range resp.Actions {
            assistantContent = append(assistantContent, ContentBlock{
                Type: "tool_use", Name: act.Name, Input: act.Input,
            })
        }
        msgs = append(msgs, Message{Role: "assistant", Content: assistantContent})

        switch resp.StopReason {
        case "end_turn":
            return resp.Text, nil
        case "tool_use":
            var results []ContentBlock
            for _, ac := range resp.Actions {
                tool, ok := byName[ac.Name]
                if !ok {
                    results = append(results, ContentBlock{Type: "tool_result", Result: fmt.Sprintf("unknown action %q", ac.Name)})
                    continue
                }
                out, err := tool.Run(ctx, ac.Input)
                if err != nil { out = fmt.Sprintf("tool error: %v", err) }
                results = append(results, ContentBlock{Type: "tool_result", Result: out})
            }
            msgs = append(msgs, Message{Role: "tool", Content: results})
        case "max_tokens":
            return "", fmt.Errorf("step %d: max_tokens", step)
        default:
            return "", fmt.Errorf("step %d: unknown stop_reason %q", step, resp.StopReason)
        }
    }
    return "", fmt.Errorf("MaxSteps=%d exceeded", a.MaxSteps)
}
```

**Four non-obvious points**:

1. **The assistant message must be appended even when it only contains `tool_use`**: A common naive implementation merges tool execution into a single message. Both Anthropic's and OpenAI's protocols require the assistant turn to be its own message — otherwise the next provider call can't resolve `tool_use_id`.
2. **Should `tool_result` use `role: "tool"` or `role: "user"`?** Anthropic uses `user`, OpenAI uses `tool`. We pick `tool` and let s02's real API adapter translate. Either choice works for the loop logic; s01 doesn't sweat it.
3. **`MaxSteps` is the last-resort safety net**: `FakeProvider` is deterministic and ends within 2 steps, but a real LLM may loop indefinitely if the prompt drifts or the model misbehaves. `MaxSteps` ensures the worst case exits with a clear error.
4. **`byName` is built once before the loop, not per step**: rebuilding the lookup map every step is a common perf bug. Building once and doing O(1) lookups is also why s04 promotes the registry to a first-class component.

## What Changed / 与上一节的变化

s01 is the first session, so there's no "previous". Here's the contrast with a **plain synchronous function** to make the loop shape stand out:

```diff
- // Traditional: one-shot task runner
- func DoTask(task string) string {
-     return process(task)  // one input → one output
- }

+ // Browser agent: state-machine loop
+ func (a *Agent) Run(ctx context.Context, task string) (string, error) {
+     msgs := []Message{{Role: "user", ...}}
+     for step := 0; step < a.MaxSteps; step++ {
+         resp, _ := a.Provider.Invoke(ctx, msgs)
+         // ... feed action results back in
+     }
+ }
```

The difference isn't "added a for loop" but **inversion of control**: the traditional function decides what to do; the agent function delegates "what to do" to `Provider` and only handles "dispatch + execute + feed back".

## Try It / 动手试一试

```bash
cd agents/s01-minimum-loop

# Basics: trigger search → see the stub result → end_turn
go run . "search hacker news"

# Verbose mode: print every step's internal state
go run . -v "search hacker news"

# Trigger navigate
go run . -v "navigate https://example.com"

# Tests
go test -v ./...
```

Expected output shape:

```
[step 0] assistant: I will search for "hacker news".
[step 0] search -> RESULT: top 3 hits for "hacker news":   1. https://example.com/...
[step 1] assistant: Task complete. top 3 hits for "hacker news":
  1. https://example.com/hacker news
  ...
Task complete. top 3 hits for "hacker news":
  1. https://example.com/hacker news
  2. https://en.wikipedia.org/wiki/hacker news
  3. https://github.com/search?q=hacker news
```

Because `FakeProvider` is deterministic, output is **byte-for-byte reproducible**. This is the foundation for testing.

## Upstream Source Reading / 上游源码阅读

Upstream `browser_use/agent/service.py` lines 1023-1142 are the real `Agent.step()` — one iteration of the same loop. It's ~120 lines, about 80 more than ours; the extra 80 are all **production concerns**: captcha waiting, screenshot, watchdog events, message compaction, planner injection.

```python
# Source: browser_use/agent/service.py#L1023-L1073
# License: MIT
async def step(self, step_info: AgentStepInfo | None = None) -> None:
    """Execute one step of the task"""
    # Initialize timing first, before any exceptions can occur
    self.step_start_time = time.time()
    browser_state_summary = None

    try:
        if self.browser_session:
            # Phase 0: captcha check (real agent handles human verification; our mini doesn't)
            try:
                captcha_wait = await self.browser_session.wait_if_captcha_solving()
                if captcha_wait and captcha_wait.waited:
                    # ...inject captcha outcome into LLM context
                    captcha_result = ActionResult(long_term_memory=msg)
                    ...
            except Exception as e:
                self.logger.warning(f'Phase 0 captcha wait failed (non-fatal): {e}')

        # Phase 1: prepare context (screenshot + DOM snapshot + message build)
        browser_state_summary = await self._prepare_context(step_info)
        self.state.last_model_output = None
        self.state.last_result = None

        # Phase 2: call LLM + run actions (== our provider.Invoke + runTools)
        await self._get_next_action(browser_state_summary)
        await self._execute_actions()

        # Phase 3: post-process (update history, telemetry, cost tracking)
        await self._post_process()

    except Exception as e:
        # All exceptions funneled to one handler; our mini just returns err
        await self._handle_step_error(e)
    finally:
        await self._finalize(browser_state_summary)
```

**Reading notes**:

- **Phase 0 → Phase 3 split**: upstream explicitly cuts one step into 4 phases so failure in Phase 1 can skip Phase 2/3. Our 60-line version flattens this into one switch, exiting early via `StopReason`.
- **Screenshot + DOM snapshot**: upstream `_prepare_context` calls `self.browser_session.get_browser_state_summary(include_screenshot=True)` — that arrives in s07-s09. s01 takes no browser observation at all.
- **Message compaction**: `await self._maybe_compact_messages(step_info)` is s03's content. Our s01 `[]Message` grows unboundedly, but `MaxSteps` keeps it small in practice.
- **Planner loop**: `plan_description = self._render_plan_description()` injects planner output — s12's "plan every 5 steps" is the demoted version of this.
- **Unified exception handling**: upstream funnels everything to `_handle_step_error`; our mini just `return err`. Simplification, but also pedagogical clarity.
- **Deliberately kept**: our `MaxSteps` safety net is split in upstream into `agent.settings.max_actions_per_step` (actions per step cap) and `max_steps` (total steps cap). We collapse to one.

**Read more**: start at `browser_use/agent/service.py` `Agent.step`, follow `_get_next_action` into `browser_use/llm/base.py`, then `_execute_actions` into `browser_use/tools/service.py`. That trace is the real code map of s01 → s02 → s04 → s12.

---

**Next session preview**: s02 replaces `FakeProvider` with a real LLM — `OpenAIProvider`, plain `net/http`, zero SDK dependencies. We also lock in the `Provider` interface shape so Phase G's multi-model addendum is purely additive.
