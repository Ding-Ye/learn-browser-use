---
title: "s_full · End-to-end integration"
chapter: "_full"
slug: s_full-integration
est_read_min: 15
---

# s_full · End-to-end integration

> No new code in this chapter. The job here is to assemble all 12 modules from s01 → s12, follow one real user task across 16 trace steps, and spell out exactly what we deliberately did not build.

---

## Architecture

Up to this point every chapter has been a self-contained Go module — its own `go.mod`, its own `main.go`, its own tests — and **none of them import each other**. That self-containment is good for learning: a reader can drop into any chapter without first understanding everything that came before, run its tests in isolation, modify its internals without breaking distant code. The cost is that not until s12 does a real `Agent.Run(task)` materialize, weaving the eleven previous pieces into one executable agent. Up to that point the chapters have been like a stack of well-machined cogs in their own boxes; this chapter is about the gearbox.

s_full does not add code. Its job is to answer two integration-level questions:

1. **"How do these pieces actually cooperate?"** — by giving a single 12-node architecture diagram that names each component, its owning chapter, the methods it exposes, and the edges it depends on. Once you can see the whole shape on one screen, the eleven chapters stop feeling like a list of independent topics and start feeling like a coordinated whole.
2. **"How does an upstream user task actually travel through these components?"** — by following the 16-step trace from research-notes.md (section A3) and pinning each step to a precise upstream line and a precise mini-line. This is the dynamic counterpart to the static diagram: the same 12 components, but rendered as a control-flow sequence rather than a dependency graph.

After reading this chapter, the reader should hold two maps in their head simultaneously:

- **The static map**: which struct owns which fields, who is allowed to call whom, what the surface API of each subsystem looks like. Useful when reading a stack trace or designing a change.
- **The dynamic map**: in what order a single `agent.run(task)` lights up each method, where the data flows in and out, where the timing budget gets spent. Useful when debugging a specific test case or wondering why an agent stalled at step 7.

Both maps point at the same underlying code. They differ in projection — static is "the codebase laid out in space"; dynamic is "one run laid out in time". A good engineering instinct keeps both available and switches between them as the situation demands.

Here is the static map. Eight top-level components, dependency arrows top-to-bottom and left-to-right; components on the same row don't depend on each other:

```
                          ┌───────────────────┐
                          │   Agent (s12)     │   agent.go#L63-L107
                          │   .Run(ctx, task) │   agent.go#L123-L231
                          └────────┬──────────┘
                                   │  composes
        ┌──────────┬───────────────┼───────────────┬───────────┐
        ▼          ▼               ▼               ▼           ▼
   ┌──────────┐ ┌───────────┐ ┌───────────┐ ┌──────────┐ ┌──────────┐
   │ Provider │ │ Message-  │ │ Registry  │ │ Browser- │ │ Token-   │
   │   (s02)  │ │  Manager  │ │   (s04)   │ │ Session  │ │  Cost    │
   │          │ │   (s03)   │ │           │ │   (s07)  │ │  (s10)   │
   │ .Invoke  │ │ .Add/.Get │ │ .Schemas/ │ │ .Start/  │ │ .Register│
   │          │ │           │ │ Dispatch  │ │  .Stop   │ │  Invoc.  │
   └────┬─────┘ └───────────┘ └────┬──────┘ └────┬─────┘ └──────────┘
        │                          │ uses        │ uses
        │                          ▼             ▼
        │                     ┌──────────┐  ┌────────────┐
        │                     │  Tools   │  │ EventBus + │
        │                     │  (5 of)  │  │ Watchdogs  │
        │                     │  (s12)   │  │ (s06)      │
        │                     └────┬─────┘  └─────┬──────┘
        │                          │              │ feeds
        │                          ▼              ▼
        │                     ┌───────────────────────────┐
        │                     │  DOMService (s09)         │
        │                     │  .Get(ctx) → DOM snapshot │
        │                     └────────────┬──────────────┘
        │                                  │ delegates
        │                                  ▼
        │                          ┌───────────────────┐
        │                          │  DOMSerializer    │
        │                          │  (s08)            │
        │                          └───────────────────┘
        │
        ▼
   ┌──────────────┐                       ┌────────────────┐
   │  Element-    │                       │  FileSystem    │
   │  Actor (s05) │                       │   (s11)        │
   └──────────────┘                       └────────────────┘
```

**A few edges that aren't obvious from the diagram alone**:

- **Provider → MessageManager is one-way.** MessageManager never knows about the Provider; the Provider receives a `[]Message` slice that came from `MessageManager.Get()`. MessageManager's only job is "manage history", not "talk to anyone in particular". This separation is the reason the chapter tests can compose `MessageManager` with `MockProvider` in s03 without dragging in OpenAI plumbing, and is also the reason a future "switch to Anthropic" project would not touch MessageManager at all.
- **Registry → BrowserSession through tools.** The 5 tools (`SearchTool` / `ClickTool` / `TypeTool` / `ScrollTool` / `DoneTool` in s12's `tools.go`) each hold a `*BrowserSession` pointer; inside their `Run()` they reach the CDP layer via `Session.Client.Send(...)`. The Registry itself doesn't know about BrowserSession — it just iterates over registered `Tool` interface values. This is why s04 could ship reflection-based registration without any browser concepts leaking in.
- **DOMService → EventBus by subscription.** `NewDOMService(bus)` subscribes its invalidation handler to `NavigationEvent` at construction time, so the next `Get()` after a navigation recomputes the snapshot. This is the production cash-out of s09's "subscribe-at-construction" idiom — if a navigation event arrives between construction and the first `Get()`, the cache invalidates correctly anyway.
- **The Element-Actor doesn't appear directly in s12.** s05's `Element` struct is the CDP-operation abstraction, but s12 does not use `Element` anywhere — it goes straight to `Session.Client.Send("Input.dispatchMouseEvent", ...)`. This is a deliberate teaching choice: keep s12's focus on integration; leave reusing `Element` as an exercise (see "Where to go next"). If we'd used Element, the s12 wiring diagram would have an extra node and the chapter's spotlight would split.
- **FileSystem hangs unused in s12.** The `Agent.FS` field exists, but none of the 5 built-in tools call it. This is intentional — `TestFilesystemSandboxRejectsAbsolutePath` proves the seam is real, but the demo writes no files so the output stays clean. Adding a `write_note(content)` tool that does use it is five lines of Go.

The diagram is intentionally small — fits on one screen, fits in working memory, names every load-bearing component. Anything that doesn't appear here (signal handlers, telemetry, cloud sync, captcha detection, HAR recording, MCP server) is in the "Deliberate omissions" table for a reason: keeping the architecture small is itself a teaching value. Production browser-use's diagram would have ~35 nodes and would not fit on a phone screen.

## End-to-end trace: agent.run("find latest news on Hacker News")

This section walks through the 16-step trace from research-notes.md (section A3), one step at a time. Each step gives four fields:

- **Who**: which component is executing
- **Where (upstream)**: precise Python file + line range
- **Where (mini)**: precise Go file + line range in our companion
- **What**: a sentence or two on what this step is actually doing

The trace narrates the **first agent run from a fresh process** — no resumed state, no cached snapshot, no pre-authenticated browser. Steps 1-4 are setup, steps 5-12 are the inner work of step 1 of the loop body, steps 13-14 are the iteration boundary, and steps 15-16 are teardown. When you read a longer run (say, 20 steps), only 13-14 keep cycling — the rest happens once.

### Step 1: User code instantiates Agent + await agent.run()

**Who**: user code / Agent
**Where (upstream)**: `browser_use/agent/service.py#L131-L160` (`Agent.__init__`)
**Where (mini)**: `agents/s12-agent-loop-full/main.go#L71-L84` (Agent struct literal) + `agents/s12-agent-loop-full/agent.go#L63-L107`
**What**: the user hands `Agent(...)` a task string, an LLM (`ChatBrowserUse()` / `ChatAnthropic()` / etc.), and a `Browser()`, then awaits `agent.run()`. In the mini version this is `&Agent{Provider: mock, ..., DOM: dom, ...}` followed by `agent.Run(ctx, "...")`. Python uses keyword-only args with defaults; Go uses a struct literal with an explicit `applyDefaults()` call.

### Step 2: Agent constructor wires MessageManager / BrowserSession / Tools / DomService / EventBus

**Who**: Agent
**Where (upstream)**: `browser_use/agent/service.py#L131-L160` (`Agent.__init__`) + `browser_use/agent/service.py#L1023-L1073` (`Agent.step`'s implicit dependencies)
**Where (mini)**: `agents/s12-agent-loop-full/main.go#L46-L84` (one-shot wiring)
**What**: upstream's `__init__` chains together `MessageManager`, `BrowserSession`, `Tools`, `DomService`, and a `bubus.EventBus`, with `model_rebuild()` calls to placate Pydantic forward-references. Our Go version needs no `model_rebuild` — each of `NewBrowserSession`, `NewDOMService`, `NewRegistry`, `NewMessageManager`, and `NewTokenCost` initializes itself, and `main.go` wires them explicitly.

### Step 3: Agent.run() registers signal handler + emits CreateAgentSessionEvent / CreateAgentTaskEvent

**Who**: Agent
**Where (upstream)**: `browser_use/agent/service.py#L2483-L2540` (the signal-handler + telemetry block at the top of `run()`)
**Where (mini)**: omitted on purpose (see "Deliberate omissions" Cloud sync / Telemetry rows)
**What**: upstream `run()` first thing does is `SignalHandler.register()` (Ctrl+C → pause/resume), then `eventbus.dispatch(CreateAgentSessionEvent.from_agent(self))` (consumed by cloud sync). We do neither: the mini version treats `context.Context` as the single cancellation channel, and telemetry is a deliberate omission.

### Step 4: BrowserSession.start() launches Chromium + attaches all watchdogs

**Who**: BrowserSession
**Where (upstream)**: `browser_use/browser/session.py#L673-L678` (`BrowserSession.start()`) + `browser_use/browser/session.py#L1562-L1596` (`attach_all_watchdogs`)
**Where (mini)**: `agents/s12-agent-loop-full/session.go#L1-L160` + `agents/s07-browser-session/session.go#L47-L196` (s07's fuller version)
**What**: upstream `start()` launches the Chromium process (or connects to an existing CDP URL) and attaches 12 watchdogs (Downloads, Popups, Security, DOM, Captcha, AboutBlank, Screenshot, HarRecording, LocalBrowser, Permissions, Recording, StorageState). Our mini's `BrowserSession.Start(ctx)` calls the stub `RecordingCDPClient.Connect()` and attaches a single `NavigationWatchdog` registered at `NewBrowserSession` time, then emits `SessionStartedEvent`. **No real process is launched.**

### Step 5: Message preparation / Agent._prepare_context()

**Who**: Agent / MessageManager
**Where (upstream)**: `browser_use/agent/service.py#L1075-L1148` (`_prepare_context`)
**Where (mini)**: `agents/s12-agent-loop-full/agent.go#L132-L139` (system + task injection) + `agents/s12-agent-loop-full/agent.go#L161-L175` (Phase 2 observation)
**What**: upstream calls `_prepare_context` at the top of every step — fetches the `browser_state_summary`, calls `_message_manager.prepare_step_state(...)`, injects budget warnings, replan nudges, exploration nudges, loop-detection nudges, force-done banners. The mini version compresses this to two `Messages.Add()` calls: one system prompt (once, at the top of `Run()`), and one `[browser_state]` user message per step. The nudges are skipped.

### Step 6: LLM invocation / Provider.Invoke

**Who**: Provider
**Where (upstream)**: `browser_use/llm/base.py#L17-L60` (Protocol) + `browser_use/agent/service.py#L1163-L1187` (`_get_next_action` calling `asyncio.wait_for`)
**Where (mini)**: `agents/s12-agent-loop-full/agent.go#L243-L271` (`invokeWithFallback`) + `agents/s12-agent-loop-full/provider.go#L31-L33` (Provider interface)
**What**: upstream uses `asyncio.wait_for(self._get_model_output_with_retry(input_messages), timeout=self.settings.llm_timeout)`; the mini version uses `context.WithTimeout(ctx, a.LLMTimeout)` plus `Provider.Invoke(primaryCtx, msgs, tools)`. Same shape — both impose a per-invocation deadline. The difference: we ship a `Fallback Provider` (upstream relies on external retry middleware) so the chapter can demo the policy directly.

### Step 7: LLM response parsing / action list extraction

**Who**: Agent / Provider
**Where (upstream)**: `browser_use/agent/service.py#L1188-L1197` (`state.last_model_output` assignment)
**Where (mini)**: `agents/s12-agent-loop-full/agent.go#L186-L196` (assistant message append) + `agents/s12-agent-loop-full/agent.go#L198-L208` (`StopReason` switch)
**What**: upstream coerces the LLM JSON into a typed `AgentOutput` whose `action: list[ActionModel]` carries the planned tool calls. The mini version uses a three-state `StopReason` (`end_turn` / `tool_use` / `max_tokens`) and a `Response.Actions []ActionCall` slice. Both rely on structured output at the LLM end.

### Step 8: Action validation / Pydantic schema enforcement

**Who**: Tools / Registry
**Where (upstream)**: `browser_use/tools/registry/service.py#L74-L289` (`_normalize_action_function_signature` and `RegisteredAction.param_model`)
**Where (mini)**: `agents/s12-agent-loop-full/registry.go#L1-L60` (Tool interface + Registry) + `agents/s04-tool-registry/schema_gen.go#L1-L175` (s04's reflection-based schema generator)
**What**: upstream uses `RegisteredAction.param_model(**params)` to validate a `dict` into a strongly-typed Pydantic model. The mini version hand-writes each tool's schema JSON in `tools.go` and `json.Unmarshal`s by hand inside `Tool.Run`. s04 already taught the reflection-based version; s12 deliberately spells it out so the chapter's spotlight stays on integration, not reflection.

### Step 9: Tool execution / DOM-index to CDP BackendNodeId mapping

**Who**: Tools / Dispatcher / Actor
**Where (upstream)**: `browser_use/tools/service.py#L420-L500` (`Tools` class) + `browser_use/actor/element.py#L62-L300` (`Element.click` etc.) + `browser_use/browser/watchdogs/default_action_watchdog.py#L906-L1180` (the real `Input.dispatchMouseEvent` / `Input.insertText` calls)
**Where (mini)**: `agents/s12-agent-loop-full/agent.go#L210-L222` (Phase 7 dispatch) + `agents/s12-agent-loop-full/tools.go#L36-L82` (SearchTool.Run) + `agents/s05-element-actor/element.go#L45-L100` (s05 Click)
**What**: upstream routes each action (click / type / scroll) through `Element` to `Input.dispatchMouseEvent` / `Input.insertText` / `Input.dispatchKeyEvent`, with BackendNodeId as the CDP-stable DOM ID. Our mini's `tools.go` directly calls `Session.Client.Send("Input.dispatchMouseEvent", map[string]any{...})`, but Client is the stub `RecordingCDPClient` — it **records** what frames would have been sent, without opening a real WebSocket.

### Step 10: Browser-state capture / DOMSnapshot.captureSnapshot

**Who**: DomService
**Where (upstream)**: `browser_use/dom/service.py#L1042-L1096` (`get_serialized_dom_tree`) + `browser_use/dom/service.py#L535-L560` (`captureSnapshot` invocation)
**Where (mini)**: `agents/s12-agent-loop-full/dom_service.go#L1-L132` + `agents/s09-dom-service/dom_service.go#L1-L209` (s09 fuller version) + `agents/s08-dom-serializer/serializer.go#L1-L275` (s08 serializer)
**What**: upstream calls `cdp_client.send.DOMSnapshot.captureSnapshot()` to get a full layout tree (layoutNodes + textBoxes + computedStyles + paintOrder), then `DOMTreeSerializer.serialize_accessible_elements()` produces LLM-friendly indexed text plus a `selector_map: dict[index, DOMRect]`. The mini's `DOMService.Get(ctx)` returns a hand-coded `SerializedDOM` fixture — not a real snapshot, but a string switched by URL.

### Step 11: Screenshot / Page.captureScreenshot

**Who**: BrowserSession / ScreenshotWatchdog
**Where (upstream)**: `browser_use/browser/session.py#L1517-L1553` (`get_browser_state_summary(include_screenshot=True)`) + `browser_use/browser/watchdogs/screenshot_watchdog.py`
**Where (mini)**: omitted on purpose (see "Deliberate omissions" screenshot row)
**What**: upstream calls `include_screenshot=True` every step inside `_prepare_context` — that fires a `BrowserStateRequestEvent` which `ScreenshotWatchdog` turns into `Page.captureScreenshot`, returning a base64 PNG that's grafted into the user message as a vision modality. The mini version does no screenshots at all — vision is a deliberate omission.

### Step 12: ActionResult aggregation / write back to step history

**Who**: Agent / MessageManager
**Where (upstream)**: `browser_use/agent/views.py#L307-L350` (`class ActionResult`) + `browser_use/agent/service.py#L1199-L1206` (`_execute_actions` writing to `state.last_result`)
**Where (mini)**: `agents/s12-agent-loop-full/agent.go#L210-L223` (Phase 7 tool_result collection + Messages.Add)
**What**: each upstream action returns `ActionResult(extracted_content=..., error=..., long_term_memory=..., is_done=...)`; `multi_act()` collects these into `list[ActionResult]`, written to `state.last_result`. The mini wraps each tool's `(string, error)` into a `ContentBlock{Type: "tool_result", Result: ...}`, and all blocks merge into one `Message{Role: "tool"}`.

### Step 13: Loop-condition check / done() early exit

**Who**: Agent
**Where (upstream)**: `browser_use/agent/service.py#L2580-L2613` (`while self.state.n_steps <= max_steps` + `if is_done: break`)
**Where (mini)**: `agents/s12-agent-loop-full/agent.go#L198-L208` (`StopReason` switch) + `agents/s12-agent-loop-full/agent.go#L218-L228` (`DoneResultPrefix` sentinel check)
**What**: upstream `_execute_step` returns an `is_done` flag derived from the `done` action's `ActionResult.is_done=True`. The mini uses a string prefix sentinel: `DoneTool.Run` returns `"__done__:..."` and `Agent.Run`'s Phase 8 uses `strings.HasPrefix` to detect and return. Both are "sentinel + early exit" patterns; upstream uses a typed bool, mini uses a typed prefix.

### Step 14: Step N+1 preparation / MessageManager.maybe_compact

**Who**: MessageManager
**Where (upstream)**: `browser_use/agent/service.py#L1150-L1161` (`_maybe_compact_messages`) + `browser_use/agent/message_manager/service.py#L213-L350` (`maybe_compact_messages`)
**Where (mini)**: `agents/s12-agent-loop-full/message_manager.go#L58-L76` (lazy KeepLastN in `Get()`) + `agents/s03-message-manager/compaction.go#L1-L141` (s03 strategy)
**What**: upstream invokes a second LLM (`settings.compaction_llm`) to summarize old turns into short text. The mini uses `KeepLastN`: when `len(History) > MaxMessages`, `Get()` returns `[History[0]] ++ History[-(MaxMessages-1):]`. Both do the work lazily in Get, not eagerly in Add — that timing choice matches upstream.

### Step 15: Termination / agent.close() → BrowserSession.close()

**Who**: Agent / BrowserSession
**Where (upstream)**: `browser_use/browser/session.py#L700-L728` (`BrowserSession.stop()`)
**Where (mini)**: `agents/s12-agent-loop-full/session.go#L1-L160` (`Session.Stop`) + `agents/s12-agent-loop-full/main.go#L54` (`defer sess.Stop(ctx)`)
**What**: upstream `stop()` dispatches `SaveStorageStateEvent` (persists cookies), then `BrowserStopEvent`, then `event_bus.stop(clear=True, timeout=5)` — every watchdog gets notified and cleans up its CDP resources. The mini's `Stop(ctx)` calls `Client.Disconnect()` plus emits `SessionStoppedEvent` to all watchdogs plus `Bus.Clear()`. No cookie persistence, no timeout wait.

### Step 16: Return AgentHistory / steps + final result + token usage

**Who**: Agent
**Where (upstream)**: `browser_use/agent/service.py#L2634-L2640` (`return self.history`) + `browser_use/agent/views.py#L307-L1000` (`AgentHistoryList`)
**Where (mini)**: `agents/s12-agent-loop-full/agent.go#L200-L201` (`return resp.Text, nil`) + `agents/s12-agent-loop-full/agent.go#L226-L228` (`return finalAnswer, nil`)
**What**: upstream returns an `AgentHistoryList[AgentStructuredOutput]` — a rich object holding `history: list[AgentHistory]` (per step: model_output / result / state / metadata), `usage: ChatInvokeUsage`, and a `final_result()` accessor. The mini returns plain `(string, error)` — just the final answer. If the caller wants the full transcript they have to stitch it from `Messages.History` and `Cost.History`. This is the teaching simplification: a smaller surface forces the caller to keep their own muscle in shape.

---

## Deliberate omissions

The 14 rows below are features upstream has and the mini does not. Each line names the precise upstream location and explains why we skipped it.

| Feature | Upstream location | Why we skipped |
|---|---|---|
| Real CDP WebSocket | `browser_use/browser/session.py#L1402-L1500` (`get_or_create_cdp_session`) + the `cdp-use` third-party library | s05 and s12 use a `RecordingCDPClient` stub. The teaching focus is "CDP is a frame stream", not "WebSocket handshake + reconnect protocol". Swapping in real CDP via `chromedp` is sketched in "Where to go next" |
| Real Chromium launch (chromedp) | `browser_use/browser/watchdogs/local_browser_watchdog.py` (whole file) | Same reason. Real Chromium launch drags in binary detection, profile dirs, `--use-gl=swiftshader` and a dozen other flags; our goal is "see how the agent drives a browser", not "become a Chromium launcher" |
| DOM tree mutation observer / incremental update | `browser_use/browser/watchdogs/dom_watchdog.py` + `browser_use/dom/service.py#L385-L500` (`_get_all_trees`) | s09 teaches cache + invalidate-on-navigation as the pair, but upstream has a separate "DOM changed so recompute" mechanism. Skipped — in a stub-fixture world, mutations never happen |
| Full DOMSnapshot layout fields | `browser_use/dom/service.py#L535-L560` (`DOMSnapshot.captureSnapshot` call) + the returned `layoutNodes` / `textBoxes` / `computedStyles` payloads | s08's testdata is a 10-20-node hand-written fixture; upstream gets thousands of nodes per frame from real Chromium. We keep the **shape** consistent — only the fixture is much smaller |
| Skill system | `browser_use/skills/service.py#L1-L285` + `browser_use/agent/service.py#L1109-L1112` (`_get_unavailable_skills_info`) | Skills are upstream's mechanism for wiring Claude skills into agent actions. We didn't implement the Anthropic-skill protocol because it's orthogonal to the agent-loop core |
| Cloud sync | `browser_use/sync/service.py#L1-L161` + `browser_use/agent/cloud_events.py#L187-L260` (`CreateAgentTaskEvent` / `CreateAgentSessionEvent`) | Commercial SaaS feature. A teaching repo shouldn't depend on cloud services |
| MCP server / client | `browser_use/mcp/server.py#L1-L1280` + `browser_use/mcp/client.py` | MCP exposes the agent to Claude Desktop. Worth learning independently — but this repo's scope is "how the agent works internally", not "how the agent is consumed externally" |
| Telemetry to PostHog | `browser_use/telemetry/service.py#L1-L112` + the `@observe` decorator scattered across `agent/service.py` | A teaching repo shouldn't ship third-party analytics |
| Judge LLM (secondary LLM evaluating agent decisions) | `browser_use/agent/judge.py` (whole file) + `browser_use/agent/views.py#L307-L320` (`JudgementResult`) | s12 already added two new policies (planner + fallback); the Judge would be a third, but demoing it requires wiring a third Provider instance, which dilutes the integration message. Easy to bolt on yourself — add a `Judge Provider` field |
| Real captcha detection / wait | `browser_use/browser/watchdogs/captcha_watchdog.py#L1-L207` + `browser_use/agent/service.py#L1031-L1049` (Phase 0 captcha) | Upstream's Phase 0 detects hCaptcha / reCAPTCHA / Cloudflare Turnstile and waits up to 60s. We don't, because the stub environment never serves a captcha |
| HAR recording | `browser_use/browser/watchdogs/har_recording_watchdog.py#L1-L779` | Saves every CDP request/response as HTTP Archive format. Production debugging gold; teaching irrelevance — `RecordingCDPClient.FrameLog()` is already the minimum readable form |
| Real anti-bot / stealth measures | `browser_use/browser/profile.py` (extension whitelist + uBlock + canvas-fingerprint randomization) | See Appendix A section 2. Teaching needs a deterministic stub environment; anti-bot trades determinism for randomization |
| Sensitive_data dict format | `browser_use/agent/message_manager/service.py#L196-L211` (`prepare_step_state`'s `sensitive_data` arg) + `browser_use/agent/service.py#L1117-L1123` | Upstream accepts a `{key: value}` substitution dict with per-domain whitelisting. The s03 mini only does regex-based masking, because dict substitution has no teaching novelty |
| Concurrent dispatch of multiple actions per step | `browser_use/tools/service.py#L420-L450` (`multi_act` parallel branch) + `browser_use/tools/service.py#L77-L200` (action timeout guard) | Upstream can dispatch multiple actions in one step concurrently (e.g. simultaneous type + click-submit). The mini runs them serially: `for _, ac := range resp.Actions { ... }`. Concurrency blurs the trace; serial keeps "which step caused which CDP frame" obvious |

## Where to go next

Once the static and dynamic maps are both in your head, the natural follow-up is "where would I start customizing?" The 14-row omissions table is the first answer — anything there is by definition a place where the mini diverges from production behavior and could be re-anchored to upstream's choice. But chasing all 14 at once is a lot. Below are five concrete extensions, sorted by effort smallest first, each one chosen because it exercises a different layer of the architecture and teaches a different lesson. None of them require touching more than two of the twelve chapter modules.

**1. Add an Anthropic provider to s02.** s02's `Provider` interface is already provider-agnostic (`Invoke(ctx, msgs, tools)`). Adding `AnthropicProvider` is three pieces: (a) convert `Message` into Anthropic's `messages` array (system message is a separate field, not array index 0 as in OpenAI), (b) convert `tools` into Anthropic's `input_schema` field (OpenAI uses `parameters`), (c) parse the response's `content` block list rather than `choices[0].message.tool_calls`. About 150 lines of Go. Reference: upstream `browser_use/llm/anthropic/chat.py#L1-L100`.

**2. Swap s05's stub CDP for real chromedp.** Steps: (a) `go get github.com/chromedp/chromedp`, (b) write a `ChromedpClient` implementing s05/s07's `CDPClient` interface (`Connect() / Disconnect() / Send(method, params) (json.RawMessage, error)`), (c) inside `Send` route method strings to chromedp's typed calls. Note that chromedp doesn't expose a raw `Send` — either change the interface, or use chromedp's `cdp.Execute(ctx, method, params, &result)`. About 250 lines of Go.

**3. Wire s12 to a real httptest server so it tests a real HTML-parse flow.** Right now the demo's `httptest.Server` is decorative — it returns HTML nobody reads. Extend: (a) `DOMService.Snapshot` fires an HTTP GET to `ts.URL` and parses the result with `golang.org/x/net/html`, (b) the tokenizer output becomes the `SerializedDOM.LLMText`. This makes the demo actually "see" page changes. About 200 lines of Go, mostly HTML parser → SerializedDOM adapter.

**4. Combine s12 with s11's sandbox to demo "agent writing files".** Currently `Agent.FS` hangs unused. Add a `WriteFileTool`: (a) struct holds `FS FileSystem`, (b) `Schema()` accepts `{"path": "...", "content": "..."}`, (c) `Run()` calls `fs.WriteFile(ctx, args.Path, []byte(args.Content))`. Then extend the scripted MockProvider to add a `write_file` action step. `TestFilesystemSandboxRejectsAbsolutePath` already proves the seam works — this step just connects it to the main loop. About 50 lines of Go.

**5. Add a Judge LLM for decision evaluation.** Add `Judge Provider` to the `Agent` struct, call `Judge.Invoke(ctx, history, judgePrompt)` after each Phase 7, and write `JudgementResult{Score, Reasoning}` into `Cost.History` (or a new `Judgements []JudgementResult` slice). The `judgePrompt` is "evaluate whether the previous step's action was reasonable". Reference: `browser_use/agent/judge.py`. About 100 lines of Go.

## Reading map for going deeper

Once the architecture and trace are clear, the natural next move is to read the upstream Python for chapters you found surprising. The reading order below moves from "shallowest entry point" outward, mapping each of the 12 chapters to the precise upstream file(s) to study next. You don't have to read them in order — pick whichever chapter you most want to deepen — but if you do read them in order you'll naturally retrace the dependency hierarchy that the curriculum was designed around. Most upstream files are bigger than their mini counterpart by roughly a factor of 10x; the first few hundred lines of each typically cover the core idea, while the long tail is engineering hygiene (logging, error paths, parameter overrides) you can skim.

1. **s01 → `browser_use/agent/service.py#L1023-L1073` (`Agent.step` main body).** The upstream `step()` method is 220 lines; our 80-line Go mirror keeps the same 7-phase shape. Start here to see the phase split.

2. **s02 → `browser_use/llm/base.py#L17-L60` + `browser_use/llm/openai/chat.py#L1-L100`.** First the Protocol (59 lines total), then the OpenAI implementation (first 100 lines cover the wire shape).

3. **s03 → `browser_use/agent/message_manager/service.py#L104-L350`.** MessageManager's `__init__`, `prepare_step_state`, `maybe_compact_messages`. The first 250 lines cover the core policies.

4. **s04 → `browser_use/tools/registry/service.py#L32-L500`.** The `_normalize_action_function_signature` is the core — upstream uses `inspect.signature` + Pydantic to build ActionModel dynamically. We do the equivalent with Go reflection in s04.

5. **s05 → `browser_use/actor/element.py#L62-L300`.** Element's `__init__` + `click` + `fill`. The first 300 lines make clear how BackendNodeId flows through CDP calls.

6. **s06 → `browser_use/browser/watchdog_base.py#L15-L321`** (whole file). 321 lines. The reflection-based `attach_to_session` that inspects `on_EventName` method names is the core mechanism.

7. **s07 → `browser_use/browser/session.py#L101-L800`.** The first 800 lines cover `__init__`, `start`, `stop`, `attach_all_watchdogs`. The remaining 3000 lines are cloud + profile + multi-target — engineering complexity worth skipping on first read.

8. **s08 → `browser_use/dom/serializer/serializer.py#L43-L500`.** DOMTreeSerializer's `serialize_accessible_elements` + paint-order filter. The first 500 lines cover the core algorithm; the rest is detail optimization.

9. **s09 → `browser_use/dom/service.py#L35-L500`.** DomService's `__init__` + `get_dom_tree` + `_get_all_trees`. The first 500 lines cover snapshot triggering + iframe handling; the remaining 700 lines are pagination-detection + dropdown-handling — specialized concerns.

10. **s10 → `browser_use/tokens/service.py#L48-L400`.** TokenCost's `initialize` (pricing fetch), `add_usage` (write path), `calculate_cost` (billing). The LiteLLM pricing-fetch lives in `_fetch_pricing`.

11. **s11 → `browser_use/filesystem/file_system.py#L1-L500`.** Read the abstract base `FileSystem` (L353-L505) first, then `LocalFileSystem` subclass (L78-L350), then `write_file` / `read_file` (L715-L760).

12. **s12 → `browser_use/agent/service.py#L1023-L2480`.** The integration read. Start from `step()`, follow each `_xxx` helper through. That's the full real agent — 220 lines of step + 1000s of lines of helpers.

---

At this point a reader has seen all the architectural layers, all the data flows, and every "deliberately not built" boundary of a production browser-use agent. If you want to do a 13th thing — turn one of the chapter's mini implementations into a production-grade one — the five paths above cover the most common entry points.

The fact that 12 chapters of Go can mirror upstream's `Agent.step()` 7-phase shape isn't a coincidence: LLM-driven agent loops have a **canonical shape**, and what we did was decompose it brick by brick and put it back together. If a 13th browser-using agent project crosses your desk, hold this skeleton in your head first, then start mapping its code onto the skeleton — chances are good it'll fit.
