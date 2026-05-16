---
title: "s12 · Full agent loop"
chapter: 12
slug: s12-agent-loop-full
est_read_min: 18
---

# s12 · Full agent loop

> Teaching focus: we have 11 chapters worth of pieces — Provider, MessageManager, Registry, BrowserSession, EventBus, DOMService, TokenCost, FileSystem — and none of them have ever talked to each other in one program. s12 wires the whole thing into a single `Agent` struct, adds planning every N steps, adds a fallback LLM on timeout, and runs the loop end-to-end against an `httptest.Server`. About 1,200 lines of Go (excluding tests); the load-bearing `Agent.Run` is under 100.

---

## Problem / 问题

After s11 we had this collection lying around:

- `Provider` (s02) — an interface, two implementations, never called in a loop.
- `MessageManager` with `KeepLastN` (s03) — owns history, never queried in production code.
- `Registry + Dispatcher` (s04) — has 5 actions registered, no agent calls `Dispatcher.Act`.
- `BrowserSession + EventBus` (s07) — Start, Stop, watchdogs attached, no work driven through them.
- `DOMService` (s09) — Get() returns a serialized DOM that nobody reads.
- `TokenCost` (s10) — a ledger waiting for `RegisterInvocation` callers.
- `LocalFileSystem` (s11) — a sandbox that no tool writes to.

Concrete pain:

1. **No "agent" exists yet.** Each chapter ends with a unit-tested struct. None of them imports another, and none of them has a `Run(task)` method that takes prose and returns prose. The shape of the integrated thing is still a hypothesis.
2. **Two policies we haven't built yet.** Upstream `browser_use/agent/service.py` does two things our 11 chapters don't:
   - Calls a *planner* every N steps to inject a "next 3 steps" reflection.
   - Falls back to a second LLM if the primary times out.
3. **The integration could leak.** If the integration code accidentally requires a real network, a real LLM, or a real Chromium, the chapter becomes unteachable on a laptop. We need an end-to-end demo that uses `httptest.Server`, `MockProvider`, and a `RecordingCDPClient` — and still proves the wiring is real.

s12 solves all three. The shape is unsurprising for Go: one `Agent` struct with the pieces wired in as fields, one `Run(ctx, task)` method with the loop body, two new helpers (`planner.go`, `invokeWithFallback`) for the new policies.

## Solution / 解决方案

The `Agent` struct:

```go
type Agent struct {
    Provider   Provider          // primary LLM (s02)
    Fallback   Provider          // optional second LLM on timeout
    Tools      *Registry         // 5 tools registered (s04)
    Session    *BrowserSession   // stub CDP + bus + watchdogs (s07)
    DOM        *DOMService       // page snapshots (s09)
    Messages   *MessageManager   // history + KeepLastN (s03)
    Cost       *TokenCost        // ledger (s10)
    FS         FileSystem        // sandbox (s11)
    MaxSteps   int
    PlanEvery  int               // 0 = disabled
    LLMTimeout time.Duration
    Verbose    io.Writer
}
```

The loop body in `Agent.Run` has 7 phases per step:

| Phase | What | Source chapter |
|---|---|---|
| 1 | Maybe call planner | s12 (new) |
| 2 | DOM snapshot | s09 |
| 3 | Invoke Provider (with fallback) | s02 + s12 (new fallback) |
| 4 | Register cost | s10 |
| 5 | Append assistant message | s03 |
| 6 | Switch on StopReason | s01 baseline |
| 7 | Dispatch tools | s04 |

A scripted `MockProvider` queue drives the demo end-to-end:

```
type[index=0, text="browser-use"]  → typed query into input
search[query="browser-use"]        → navigates to results page
click[index=0]                     → opens first article
done[answer="First article on browser-use"]  → loop returns
```

Two new policies layered on top of the s01 baseline:

- **Planner** (`planner.go`): every `PlanEvery` steps (zero = off), call `Plan(ctx, Provider, history)` and inject the response as a system message. The next regular turn sees it.
- **Fallback** (`invokeWithFallback` in `agent.go`): wrap `Provider.Invoke` in `context.WithTimeout(LLMTimeout)`; on `DeadlineExceeded`, retry against `Fallback` with a FRESH `context.WithTimeout`.

## How It Works / 工作原理

The full architecture, with every previous chapter's contribution wired into the Agent struct:

```
┌──────────────────────────────────────────────────────────────────────┐
│                            Agent.Run(ctx, task)                       │
│                                                                       │
│   ┌─ phase 1: planner ──────────────────────────────┐                 │
│   │  step % PlanEvery == 0 ?                        │ ← s12 new       │
│   │  └─ yes → Plan(ctx, Provider, msgs)             │                 │
│   │           → inject as system message            │                 │
│   └─────────────────────────────────────────────────┘                 │
│                                                                       │
│   ┌─ phase 2: observe ──────────────────────────────┐                 │
│   │  dom := DOMService.Get(ctx)         (s09)       │                 │
│   │  Messages.Add(user, "[browser_state]\n" + dom)  (s03)             │
│   └─────────────────────────────────────────────────┘                 │
│                                                                       │
│   ┌─ phase 3: invoke (with fallback) ───────────────┐                 │
│   │  ctx1 = WithTimeout(ctx, LLMTimeout)            │                 │
│   │  resp, err := Provider.Invoke(ctx1, msgs)       │ ← s02           │
│   │  if DeadlineExceeded && Fallback != nil:        │ ← s12 new       │
│   │    ctx2 = WithTimeout(ctx, LLMTimeout)          │                 │
│   │    resp, err = Fallback.Invoke(ctx2, msgs)      │                 │
│   └─────────────────────────────────────────────────┘                 │
│                                                                       │
│   ┌─ phase 4: ledger ───────────────────────────────┐                 │
│   │  Cost.RegisterInvocation(model, in, out)    (s10)                 │
│   └─────────────────────────────────────────────────┘                 │
│                                                                       │
│   ┌─ phase 5: append assistant ─────────────────────┐                 │
│   │  Messages.Add(assistant, text + tool_use[])  (s03)                │
│   └─────────────────────────────────────────────────┘                 │
│                                                                       │
│   ┌─ phase 6: switch StopReason ────────────────────┐                 │
│   │  end_turn  → return text, nil                   │                 │
│   │  tool_use  → goto phase 7                       │                 │
│   │  max_tok   → return error                       │                 │
│   └─────────────────────────────────────────────────┘                 │
│                                                                       │
│   ┌─ phase 7: dispatch tools ───────────────────────┐                 │
│   │  for each action:                               │                 │
│   │    block, _ := Dispatcher.Act(ctx, action) (s04)│                 │
│   │    if block.Result starts with __done__:        │                 │
│   │      finalAnswer = suffix                       │                 │
│   │  Messages.Add(tool, results)                    │                 │
│   │  if finalAnswer != "": return finalAnswer, nil  │                 │
│   └─────────────────────────────────────────────────┘                 │
└──────────────────────────────────────────────────────────────────────┘
        ▲                              ▲                       ▲
        │                              │                       │
   BrowserSession + EventBus      RecordingCDPClient      LocalFileSystem
   (s07) + NavigationWatchdog        (s05/s07)               (s11)
```

Core code, ~80 lines from `agent.go`:

```go
func (a *Agent) Run(ctx context.Context, task string) (string, error) {
    a.applyDefaults()
    if err := a.validate(); err != nil { return "", err }

    a.Messages.Add(Message{Role: "system",
        Content: []ContentBlock{{Type: "text", Text: systemPromptText}}})
    a.Messages.Add(Message{Role: "user",
        Content: []ContentBlock{{Type: "text", Text: "Task: " + task}}})

    for step := 0; step < a.MaxSteps; step++ {
        // Phase 1: planner.
        if a.PlanEvery > 0 && step > 0 && step%a.PlanEvery == 0 {
            plan, err := Plan(ctx, a.Provider, a.Messages.Get())
            if err == nil {
                a.Messages.Add(Message{Role: "system", Content: []ContentBlock{
                    {Type: "text", Text: "[plan @ step " + itoa(step) + "] " + plan},
                }})
                a.logf("[step %d] planner: %s\n", step, truncate(plan, 80))
            }
        }
        // Phase 2: observe.
        dom, err := a.DOM.Get(ctx)
        if err != nil { return "", fmt.Errorf("step %d: dom: %w", step, err) }
        a.Messages.Add(Message{Role: "user", Content: []ContentBlock{
            {Type: "text", Text: "[browser_state]\nURL: " + a.DOM.CurrentURL() + "\n" + dom.LLMText},
        }})

        // Phase 3: invoke (with fallback).
        resp, err := a.invokeWithFallback(ctx, step)
        if err != nil { return "", err }

        // Phase 4: ledger.
        a.Cost.RegisterInvocation(resp.Model, resp.InputTokens, resp.OutputTokens)

        // Phase 5: append assistant.
        assistantBlocks := []ContentBlock{{Type: "text", Text: resp.Text}}
        for _, ac := range resp.Actions {
            assistantBlocks = append(assistantBlocks, ContentBlock{
                Type: "tool_use", Name: ac.Name, Input: ac.Input,
            })
        }
        a.Messages.Add(Message{Role: "assistant", Content: assistantBlocks})

        // Phase 6: switch on StopReason.
        switch resp.StopReason {
        case "end_turn":
            return resp.Text, nil
        case "max_tokens":
            return "", fmt.Errorf("step %d: provider truncated response", step)
        case "tool_use":
            // fall through to dispatch
        default:
            return "", fmt.Errorf("step %d: unknown stop_reason %q", step, resp.StopReason)
        }

        // Phase 7: dispatch.
        disp := &Dispatcher{Registry: a.Tools, Timeout: a.LLMTimeout}
        var toolResults []ContentBlock
        var finalAnswer string
        for _, ac := range resp.Actions {
            block, _ := disp.Act(ctx, ac)
            toolResults = append(toolResults, block)
            if strings.HasPrefix(block.Result, DoneResultPrefix) {
                finalAnswer = strings.TrimPrefix(block.Result, DoneResultPrefix)
            }
        }
        a.Messages.Add(Message{Role: "tool", Content: toolResults})

        if finalAnswer != "" {
            return finalAnswer, nil
        }
    }
    return "", fmt.Errorf("MaxSteps=%d exceeded without end_turn or done()", a.MaxSteps)
}
```

**5 non-obvious points**:

1. **Planning is opt-in via `PlanEvery`.** Setting it to zero disables the planner entirely; the loop becomes a vanilla observe-think-act with no reflection. Upstream's analog is `settings.planner_interval`. Why opt-in? Each planner call adds ~1k tokens of overhead and rarely improves the per-step decision; the wins are visible only on long-horizon tasks. Tests can leave `PlanEvery: 0` and verify everything else without dragging planner mocks into their queues.

2. **Fallback creates a fresh context, not a retry-in-place.** Look at `invokeWithFallback`: after the primary errors, we construct *another* `context.WithTimeout(parent, LLMTimeout)`. If we reused the primary's ctx (already timed out) the fallback would never get to run. The same parent ctx is used as the *root* so global cancellation still works — only the per-attempt deadline is fresh.

3. **Cost tracking sits outside the loop body.** `RegisterInvocation` is called once per successful invoke, not from inside MessageManager or Provider. That decoupling matters: a real metrics exporter (prometheus, OpenTelemetry) plugs in at the same seam without touching the loop. It also means a primary timeout — where no Response was produced — correctly does NOT consume cost budget.

4. **Sandbox path is per-Agent, not global.** `agent.FS = NewLocalFileSystem("./sandbox-task-N")` lets a multi-tenant deployment isolate each Agent's filesystem state. The single `FS FileSystem` field on the struct is enough — the agent doesn't try to manage subdirectories for each turn; the filesystem implementation owns layout. In s12 no built-in tool calls `FS` (the demo doesn't write files), but `TestFilesystemSandboxRejectsAbsolutePath` proves the surface is real.

5. **The `done()` action exits via a magic prefix on the tool_result.** Look at `DoneResultPrefix = "__done__:"` in `tools.go`. When `Dispatcher.Act` returns a tool_result whose body starts with that prefix, `Agent.Run` extracts the suffix as the final answer and returns. Why not a typed Action interface with `IsDone() bool`? Because every tool would have to implement the marker, even if it never returned true. The string-prefix sentinel keeps `Tool.Run` returning `(string, error)` everywhere; the agent does one `strings.HasPrefix` check.

## What Changed / 与上一节的变化

The biggest diff in the entire curriculum. Visual: the `Agent` struct's field count from s01 to s12:

```
s01  Agent{ Provider, Actions, MaxSteps, Verbose }                                 (4 fields)
s02  Agent{ Provider(real LLM), Tools, MaxSteps, Verbose }                         (4 fields, types matured)
s03  Agent{ Provider, Tools, Messages*, MaxSteps, Verbose }                        (+1: history struct)
s04  Agent{ Provider, Tools(Registry), Messages, MaxSteps, Verbose }               (Tools became *Registry)
s05  Agent{ Provider, Tools, Messages, Session(CDP+Actor), MaxSteps, Verbose }     (+1: browser session)
s06  Agent{ Provider, Tools, Messages, Session(Bus+Watchdogs), MaxSteps, Verbose } (Session grew bus)
s07  Agent{ Provider, Tools, Messages, Session(lifecycle), MaxSteps, Verbose }     (Session grew Start/Stop)
s08  (no Agent change — DOM serializer is internal to tools)
s09  Agent{ ..., DOM *DOMService, ... }                                            (+1: DOM)
s10  Agent{ ..., DOM, Cost *TokenCost, ... }                                       (+1: cost)
s11  (no Agent change — FS is its own struct, ready to be wired)
s12  Agent{ Provider, Fallback, Tools, Session, DOM, Messages, Cost, FS,
            MaxSteps, PlanEvery, LLMTimeout, Verbose }                             (12 fields)
```

The s12 jump adds three things the previous 11 chapters never owned:

```diff
+ Fallback   Provider          // new in s12
+ FS         FileSystem        // s11 ready, never assigned before
+ PlanEvery  int               // new in s12
+ LLMTimeout time.Duration     // new in s12 (was a constant in s04's Dispatcher)
```

The actual Go diff in the loop body, comparing s11's hypothetical step driver vs s12's `Agent.Run`:

```diff
- // s11 had no agent — `FS` lived in isolation
- for _, op := range scenarioOps { fs.WriteFile(ctx, op.path, op.content) }
+ for step := 0; step < a.MaxSteps; step++ {
+     if a.PlanEvery > 0 && step > 0 && step%a.PlanEvery == 0 {
+         plan, _ := Plan(ctx, a.Provider, a.Messages.Get())
+         a.Messages.Add(/* inject plan */)
+     }
+     dom, _ := a.DOM.Get(ctx)
+     a.Messages.Add(/* browser_state */)
+     resp, err := a.invokeWithFallback(ctx, step)
+     a.Cost.RegisterInvocation(resp.Model, resp.InputTokens, resp.OutputTokens)
+     /* phase 5..7 */
+ }
```

This is the chapter where "we have a real agent" becomes true.

## Try It / 动手试一试

```bash
cd agents/s12-agent-loop-full

# E2E demo: 4-step scripted run against httptest.Server
GOWORK=off go run .

# All 7 tests (5 required + 2 bonus)
GOWORK=off go test -v ./...
```

`GOWORK=off` because the root `go.work` doesn't list s12 yet; the module is self-contained.

Expected demo output (truncated):

```
=== s12 agent run ===
backend URL: http://127.0.0.1:PORT
[step 0] assistant: I'll type the query first. (stop=tool_use)
[step 0]   type → typed "browser-use" into [0]
[step 1] assistant: Now submit search. (stop=tool_use)
[step 1]   search → navigated to https://search.example/results?q=browser-use
[step 2] assistant: Opening the first result. (stop=tool_use)
[step 2]   click → clicked [0] → navigated to https://article.example/200
[step 3] assistant: Task complete. (stop=tool_use)
[step 3]   done → __done__:First article on browser-use

Final answer: First article on browser-use

--- CDP frames ---
  [0] Target.attachToTarget ...
  [1] Input.insertText ...
  [2] Page.navigate ...
  [3] Input.dispatchMouseEvent ...

--- Token cost — 4 invocation(s)
  Total: in=1580 tok  out=150 tok  cost=$0.0003
  Per model:
    gpt-4o-mini     invocations=4  in=1580  out=150  cost=$0.0003
```

Test coverage:

- `TestFullE2EAgainstStub` — Scripted 4-turn run: type → search → click → done. Asserts final answer, CDP recorder contents, cost ledger row count.
- `TestFallbackOnTimeout` — Primary has `Delay: 500ms`, LLMTimeout=200ms. The fallback's done() answer wins; primary called once, fallback called once.
- `TestPlanningEvery5Steps` — `PlanEvery=5, MaxSteps=12`. Verbose log shows `[step 5] planner:` and `[step 10] planner:`. Run terminates with MaxSteps (no done() in the script).
- `TestMaxStepsTermination` — Provider returns scroll forever, MaxSteps=3. Run returns "MaxSteps=3 exceeded" error.
- `TestDoneExitsCleanly` — One done() turn. Run returns the answer; provider.CallCount() == 1.
- `TestKeepLastNCompaction` (bonus) — MessageManager with max=8, 25 Adds → Get() returns exactly 8.
- `TestFilesystemSandboxRejectsAbsolutePath` (bonus) — `fs.WriteFile("/etc/passwd", ...)` errors; `fs.WriteFile("note.md", ...)` succeeds.

## Upstream Source Reading / 上游源码阅读

Upstream's `Agent.step` is the single method the entire control flow flows through — about 220 lines in `browser_use/agent/service.py`. The seven phases visible there (captcha-wait, prepare-context, get-next-action, execute-actions, post-process, error-handle, finalize) map almost one-to-one onto our 7 phases (planner, observe, invoke, ledger, append, switch, dispatch). The compression ratio is real but the *shape* survives.

```python
# Source: browser_use/agent/service.py#L1023-L1110

async def step(self, step_info: AgentStepInfo | None = None) -> None:
    """Execute one step of the task"""
    # Initialize timing first, before any exceptions can occur
    self.step_start_time = time.time()
    browser_state_summary = None

    try:
        if self.browser_session:
            try:
                captcha_wait = await self.browser_session.wait_if_captcha_solving()
                if captcha_wait and captcha_wait.waited:
                    self.step_start_time = time.time()
                    duration_s = captcha_wait.duration_ms / 1000
                    outcome = captcha_wait.result  # 'success' | 'failed' | 'timeout'
                    msg = f'Waited {duration_s:.1f}s for {captcha_wait.vendor} CAPTCHA...'
                    self.logger.info(f'🔒 {msg}')
                    captcha_result = ActionResult(long_term_memory=msg)
                    if self.state.last_result:
                        self.state.last_result.append(captcha_result)
                    else:
                        self.state.last_result = [captcha_result]
            except Exception as e:
                self.logger.warning(f'Phase 0 captcha wait failed (non-fatal): {e}')

        # Phase 1: Prepare context and timing
        browser_state_summary = await self._prepare_context(step_info)

        # Clear previous step state after context preparation
        self.state.last_model_output = None
        self.state.last_result = None

        # Phase 2: Get model output and execute actions
        await self._get_next_action(browser_state_summary)
        await self._execute_actions()

        # Phase 3: Post-processing
        await self._post_process()

    except Exception as e:
        await self._handle_step_error(e)

    finally:
        await self._finalize(browser_state_summary)


async def _get_next_action(self, browser_state_summary: BrowserStateSummary) -> None:
    """Execute LLM interaction with retry logic and handle callbacks"""
    input_messages = self._message_manager.get_messages()
    self.logger.debug(
        f'🤖 Step {self.state.n_steps}: Calling LLM with '
        f'{len(input_messages)} messages (model: {self.llm.model})...'
    )

    try:
        model_output = await asyncio.wait_for(
            self._get_model_output_with_retry(input_messages),
            timeout=self.settings.llm_timeout,
        )
    except TimeoutError:
        await _log_model_input_to_lmnr(input_messages)
        raise TimeoutError(
            f'LLM call timed out after {self.settings.llm_timeout} seconds.'
        )

    self.state.last_model_output = model_output
    await self._check_stop_or_pause()
    await self._handle_post_llm_processing(browser_state_summary, input_messages)
    await self._check_stop_or_pause()
```

**6 reading notes**:

1. **Upstream's `step()` is a 220-line method that wraps every Phase in `try/except`.** Our Go version returns errors from each phase via `return "", err`. Same control flow, different idiom. The Go version is shorter because we don't carry the per-phase try-wrappers that Python's exception model encourages.

2. **`_prepare_context` is where upstream actually builds the user message** — including a screenshot, the serialized DOM, plus a dozen "nudge" injections (`_inject_budget_warning`, `_inject_replan_nudge`, `_inject_exploration_nudge`, `_inject_loop_detection_nudge`, `_force_done_after_last_step`, `_force_done_after_failure`). We compress this to one `Messages.Add(user, browser_state)` call. The nudges are real production techniques worth knowing about but not needed for the teaching point.

3. **`_maybe_compact_messages` is the compaction call site.** Upstream lazily compacts INSIDE the prepare-context phase, just before `create_state_messages`. Our `MessageManager.Get()` does it the same way — lazily inside Get, never inside Add. The shape is identical; we just don't bother with an LLM-driven summariser (`browser_use.agent.message_manager.maybe_compact_messages`) because our `KeepLastN` keeps the test deterministic.

4. **The timeout call uses `asyncio.wait_for(..., timeout=self.settings.llm_timeout)`.** Python's `wait_for` cancels the underlying coroutine on timeout — the asyncio scheduler reclaims its slot. Our Go version uses `context.WithTimeout` + checks `errors.Is(err, context.DeadlineExceeded)`. Same semantics: bound the per-call cost.

5. **No fallback Provider in upstream.** Upstream goes straight from "primary timed out" to raising. Production deployments wrap with their own retry middleware. Our s12 inverts that: ship a single Fallback field so the demo shows the policy in code, not behind a Retry library. The teaching gain is "fallback is a concept, not a magic plugin".

6. **`_handle_step_error` is upstream's single error-handling site.** It branches on exception type (InterruptedError, KeyboardInterrupt, custom domain exceptions). Our Go version returns errors directly; the caller (main.go) decides how to surface them. Both shapes are defensible; the Python idiom centralizes recovery, the Go idiom centralizes dispatch.

The fact that 11 chapters of Go can be wired to mirror the Python `step()` method one-to-one is the strongest argument that the curriculum's decomposition matches the actual code structure of the upstream project. The integration is not "we glued things in a way that happens to work" — it's "the LLM-driver loop has a canonical shape, and we recovered it piece by piece".
