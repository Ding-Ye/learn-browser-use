---
title: "Appendix B · Upstream source-reading guide"
chapter: "appendix-b"
slug: appendix-b-upstream-map
est_read_min: 15
---

# Appendix B · Upstream source-reading guide

> How do you read ~98K lines of upstream browser-use Python without drowning? This chapter gives a **progressive reading path** that maps every concept you learned to specific upstream files and line numbers.

After 12 chapters of Go practice, you have hand-built Provider, MessageManager, Registry, Watchdog, Session, DOMService, TokenCost, and FileSystem. Every Go implementation maps to a piece of upstream Python — but the Python repo is 25 directories and ~98,000 lines, and opening it cold is noise. This map gives you an 8-stop tour ordered from "fewest dependencies" to "most dependencies." Each stop tells you **which file to open first**, **what to focus on**, and **which Go session you already wrote that mirrors it**. After 8 stops you understand 90% of the code; the rest (MCP, sync, skills, telemetry) is peripheral and you can browse it as needed.

A practical reading tip before you start: keep two windows open. One window shows the upstream Python file you're currently studying. The other shows the corresponding Go session you wrote. When you hit a Python construct you don't recognize, switch to the Go window — chances are you solved that same problem in a different idiom and you'll learn faster by analogizing back than by Googling the Python feature. The 12 Go sessions are not just "smaller upstream" — they are a translation that surfaces what each Python idiom is *actually doing*.

## Reading order

The 8 modules below, read in this order, never force you to flip back to a not-yet-read concept.

**Stop 1: `agent/`.** Start with `agent/views.py` (1000 lines defining `ActionResult`, `AgentHistoryList`, `AgentOutput`, `AgentSettings` — these data classes flow through everything). Then jump to the **top 200 lines** of `agent/service.py` — only read `class Agent`'s field definitions and `__init__` signature. Build the mental model of "what does an Agent hold." **Do not** dive into the 4131-line `step()` body yet. After the other 7 stops, that body becomes legible. This stop corresponds to your s01 and s12.

**Stop 2: `llm/`.** Start with `llm/base.py` (a 59-line Protocol), then `llm/messages.py` (message types) and `llm/schema.py` (structured-output schemas). Finally pick one provider — `llm/openai/chat.py` is recommended because it's what you mirrored in s02. Save Anthropic (`llm/anthropic/chat.py`) for the Phase G multi-model extension. Corresponds to s02.

**Stop 3: `tools/`.** Read `tools/registry/views.py` for the data shape (`ActionRegistry`, `RegisteredAction`), then `tools/registry/service.py` for registration logic (especially `_normalize_action_function_signature` — it builds Pydantic schemas from Python function signatures, very clever). Then `tools/service.py` for the dispatcher (notice the 180s global timeout). Corresponds to s04.

**Stop 4: `browser/`.** Open `browser/watchdog_base.py` (321 lines of reflective registration) — this is what you wrote in s06. See how the Python side uses a metaclass to check `LISTENS_TO`. Then read the first 300 lines of `browser/session.py` (constructor and `start()`). Pick two watchdogs as examples: `watchdogs/downloads_watchdog.py` (1382 lines — the most complex one) and `watchdogs/popups_watchdog.py` (145 lines — the simplest one). Corresponds to s06 and s07.

**Stop 5: `dom/`.** `dom/views.py` for the DOMNode data structures (1041 lines of Pydantic models), `dom/serializer/serializer.py` for the full pipeline from raw CDP snapshot to LLM text (1290 lines — the densest single file in the project), `dom/service.py` for cache and invalidation logic. Corresponds to s08 and s09.

**Stop 6: `actor/`.** Read `actor/element.py` (1182 lines). First 200 lines: the `class Element` fields. Middle: how `click()` / `type()` dispatch CDP events via BackendNodeId. End: retry logic. Corresponds to s05. Note: this stop can come late — it's the lowest browser layer and depends on nothing earlier.

**Stop 7: `filesystem/`.** `filesystem/file_system.py` (941 lines). Compare `LocalFileSystem` vs `CloudFileSystem` — both inherit from the `FileSystem` abstract base. Note where binary-extension blocking and path-traversal guards live. Corresponds to s11.

**Stop 8: `tokens/`.** `tokens/service.py` (605 lines). See how `TokenCost.initialize()` pulls pricing JSON from the LiteLLM repo, caches it locally with a 1-day TTL, and hooks into Provider invocations. Corresponds to s10.

After these 8 stops, return to `agent/service.py`'s `step()` body. You'll find it's just the 8 pieces strung together in sequence — no black magic.

## Upstream file → chapter mapping

The table below covers 30+ upstream files. Each row tells you what the file does, which Go chapter it maps to, and what to focus on when reading. All paths verified to exist at SHA `933e28c599ddd74c15a48568f159da95547e40dd`.

| Upstream file | Key class / function | Chapter | What to focus on |
|---|---|---|---|
| `browser_use/agent/service.py` (4131 lines) | `class Agent`, `Agent.step()`, `Agent.run()` | s01, s03, s12 | Read `__init__` fields (lines 131–300) first, then `run()` body (line 2483+), then `step()` (line 1023+) — your `Run(ctx, task)` in s12 is a stripped-down clone |
| `browser_use/agent/views.py` (1000 lines) | `ActionResult`, `AgentOutput`, `AgentHistoryList`, `AgentSettings` | s01, s12 | Almost every data flow in the project passes through these Pydantic classes — learn the data shapes before any logic |
| `browser_use/agent/message_manager/service.py` (597 lines) | `class MessageManager`, `prepare_step_state()`, compaction logic | s03 | The threshold check + summarize call + sensitive-data redaction trio |
| `browser_use/agent/message_manager/views.py` | `MessageManagerState` | s03 | Persistent state shape for the message manager |
| `browser_use/agent/system_prompts/system_prompt.md` | (static prompt) | s12 reference | See how browser-use uses a markdown file as system prompt template; your s12 uses a stripped-down version |
| `browser_use/agent/judge.py` | `class JudgeService` | s_full deliberate omission | Secondary LLM that scores agent decisions — entirely skipped in this repo, read if curious |
| `browser_use/llm/base.py` (59 lines) | `BaseChatModel` (Protocol), `ChatInvokeCompletion` | s02 | The smallest yet most important file in the project — all 16 providers unify on this Protocol |
| `browser_use/llm/openai/chat.py` (306 lines) | `class ChatOpenAI` | s02 | Compare against the `OpenAIProvider` you wrote in s02; note streaming and tool_choice handling |
| `browser_use/llm/openai/serializer.py` | message → OpenAI wire format | s02 | How the unified Message becomes OpenAI's `{role, content, tool_calls}` |
| `browser_use/llm/anthropic/chat.py` (260 lines) | `class ChatAnthropic` | Phase G multi-model | Compare to OpenAI: see the `tool_use` block format and content-array differences |
| `browser_use/llm/anthropic/serializer.py` | message → Anthropic wire format | Phase G multi-model | Anthropic system messages are passed separately; user/assistant go in the messages array |
| `browser_use/llm/messages.py` (238 lines) | `UserMessage`, `AssistantMessage`, `ToolResultMessage`, `ContentBlock` | s01, s02 | Direct counterpart to your `Message` struct in s01/s02; ContentBlock's union-type design is worth studying |
| `browser_use/llm/schema.py` | structured output schema helpers | s02 | How Pydantic models convert to OpenAI tool schemas |
| `browser_use/tools/service.py` (2252 lines) | `class Tools`, `Tools.act()`, action timeout guard | s04 | The 180s timeout wrapper around `act()`; sensitive-data redaction; error handling |
| `browser_use/tools/registry/service.py` (601 lines) | `class Registry`, `_normalize_action_function_signature()` | s04 | The reflection logic that extracts a Pydantic param model from a Python function signature — inspiration for your s04 `schema_gen.go` |
| `browser_use/tools/registry/views.py` | `RegisteredAction`, `ActionRegistry` | s04 | What the registry stores |
| `browser_use/tools/extraction/schema_utils.py` | extraction prompt builders | s04 reference | Helpers for LLM-based information extraction; s04 does not implement extraction |
| `browser_use/browser/session.py` (4000 lines) | `class BrowserSession`, `get_or_create_cdp_session()`, watchdog attachment | s07 | First 300 lines — `__init__` and `start()` — are the most important; rest is CDP-command wrappers |
| `browser_use/browser/profile.py` (1288 lines) | `BrowserProfile`, Chromium launch flags | s07 reference | Stealth flags, user-data-dir, extension loading — s07 does not implement these |
| `browser_use/browser/watchdog_base.py` (321 lines) | `BaseWatchdog`, `attach_to_session()`, reflective handler registration | s06 | Python original of the reflective registration you wrote in s06; pay special attention to `LISTENS_TO`/`EMITS` declarations and the circuit breaker |
| `browser_use/browser/watchdogs/downloads_watchdog.py` (1382 lines) | `DownloadsWatchdog` | s06 example | The most complex watchdog instance: intercepts downloads, local storage, cloud sync — extract the 200-line core to port to Go |
| `browser_use/browser/watchdogs/popups_watchdog.py` (145 lines) | `PopupsWatchdog` | s06 example | The simplest watchdog instance — direct mirror for s06 teaching |
| `browser_use/browser/watchdogs/security_watchdog.py` (278 lines) | `SecurityWatchdog` | s06 reference | Blocks dangerous domains and mixed content; see how a watchdog can actively veto an action |
| `browser_use/browser/watchdogs/dom_watchdog.py` (865 lines) | `DOMWatchdog` | s09 reference | Triggers DOM cache invalidation on navigation events — inspiration for your s09 cache invalidation |
| `browser_use/browser/events.py` | All event-class definitions | s06 | 50+ event types in one file — read this to see how an event-driven system organizes its vocabulary |
| `browser_use/dom/views.py` (1041 lines) | `DOMNode`, `EnhancedDOMTreeNode`, `SerializedDOMState`, `DOMRect` | s08 | Direct counterpart to your `DOMNode` struct in s08; Python version adds `accessibility_role`, `computed_styles`, etc. |
| `browser_use/dom/serializer/serializer.py` (1290 lines) | `DOMTreeSerializer`, `serialize_accessible_elements()` | s08 | The single heaviest file in the project — bbox filtering, paint order, interactive merging all live here |
| `browser_use/dom/serializer/paint_order.py` | paint-order computation | s08 | Z-order occlusion algorithm — source of the simplified `paint_order.go` in s08 |
| `browser_use/dom/serializer/clickable_elements.py` | clickable detection | s08 | How an element is judged interactive |
| `browser_use/dom/service.py` (1174 lines) | `class DomService`, snapshot orchestration, cache | s09 | Cache TTL, navigation invalidation, cross-origin iframe handling |
| `browser_use/actor/element.py` (1182 lines) | `class Element`, `click()`, `type()`, `screenshot()` | s05 | Full path of BackendNodeId → CDP `Input.dispatchMouseEvent`; retry logic worth reading |
| `browser_use/actor/mouse.py` | mouse event composition | s05 reference | Three-stage mouse-down/up/move construction; s05 stub does not do this |
| `browser_use/filesystem/file_system.py` (941 lines) | `FileSystem` (ABC), `LocalFileSystem`, `CloudFileSystem` | s11 | Direct counterpart to s11; note the exact location of binary-extension list and path-traversal checks |
| `browser_use/tokens/service.py` (605 lines) | `class TokenCost`, `register_llm_invocation()`, pricing cache | s10 | Direct counterpart to s10; the pricing-from-LiteLLM-repo loader logic with 1-day TTL |
| `browser_use/tokens/views.py` | `TokenUsageEntry`, `Cost` | s10 | Data shapes — direct s10 mapping |
| `browser_use/observability.py` (204 lines) | `@observe` decorator | s12 reference | Cross-function logging/tracing decorator implementation; s12 skips this |
| `browser_use/mcp/server.py` (1280 lines) | MCP server entry, tool schema | s_full deliberate omission | The protocol adapter that exposes Agent as an MCP server — entirely skipped |
| `browser_use/mcp/client.py` | MCP client | s_full deliberate omission | MCP client, useful for hitting MCP servers other than Claude Desktop |
| `browser_use/sync/service.py` | cloud sync | s_full deliberate omission | Syncs agent-run data to api.browser-use.com — skipped |
| `browser_use/skills/service.py` | Claude skill integration | s_full deliberate omission | Bridge to Claude Code skills — orthogonal to the agent loop |
| `browser_use/telemetry/service.py` | `ProductTelemetry` | s_full deliberate omission | PostHog event reporting — skipped |

## Extension exercises

Once you've read the upstream source, you'll likely want to implement things this repo skipped. Five concrete exercises below — each mapped to an upstream module, each a candidate for a future sNN chapter.

**Exercise 1: Port DownloadsWatchdog fully to s06-style Go.** The upstream `browser/watchdogs/downloads_watchdog.py` is 1382 lines, but its core is ~300 lines: listen to `Browser.downloadWillBegin` CDP events → pick a local save path → listen to `Browser.downloadProgress` → on completion, emit `DownloadCompletedEvent`. The rest is cloud sync and error handling. Build a `DownloadsWatchdog` on top of the s06 EventBus + Watchdog framework — roughly 200 lines of Go. The interesting part is simulating CDP download events (have your `RecordingCDPClient` inject fake events) and verifying the Watchdog's event routing is correct.

**Exercise 2: Add an Anthropic provider in s02.** You wrote an OpenAI provider in s02; now mirror `llm/anthropic/chat.py` and build an `AnthropicProvider`. The hard part isn't HTTP — it's **message-format translation**. Anthropic's `messages` array cannot contain system messages (system is passed separately); content is an array of `[{type: "text"|"tool_use"|"tool_result", ...}]` objects rather than strings. Roughly 200 lines of Go plus a `provider_translator.go` that maps unified `Message` to the Anthropic wire format. This is the essence of the Phase G multi-model addendum.

**Exercise 3: Replace the s05 stub CDP with chromedp for real Chromium control.** The s05 `RecordingCDPClient` only records "what CDP frames would have been sent," it doesn't actually transmit them. To run against a real browser, pull in `github.com/chromedp/chromedp` and rewrite the s05 click/type/screenshot methods to call `chromedp.Run(ctx, chromedp.Click(...))` etc. This is a substantial change — recommend doing it in a fresh module (say `agents/sNN-real-cdp`) — and it will add chromedp to your `go.mod`, the first third-party dependency in this repo.

**Exercise 4: Implement a minimal MCP server.** Expose your s12 `Agent` as an MCP server so Claude Desktop can call it via MCP. MCP is JSON-RPC over stdio. The smallest viable implementation needs: (1) an `initialize` handshake response, (2) `tools/list` returning a single `run_agent_task(task: string)` tool schema, (3) `tools/call` that starts an `Agent.Run(task)` on invocation and returns the result. Reference `mcp/server.py` for stdin/stdout handling. Roughly 300 lines of Go plus a test echo client.

**Exercise 5: Add fingerprint countermeasures.** At s07 startup (even though it's stubbed), you already have a hook for "launch arguments." Inject some stealth tricks: (1) on every page load, run `Object.defineProperty(navigator, 'webdriver', {get: ()=>false})`; (2) add Canvas API noise — perturb the last bit of `getContext('2d').getImageData()` return values; (3) spoof WebGL `getParameter(VENDOR)` returns. Roughly 150 lines of Go — mostly JS string templates and CDP `Page.addScriptToEvaluateOnNewDocument` calls.

## After you've read this

After 8 source-stops and 5 extension exercises you have a complete code map of browser-use. You should be able to:

- Open an unfamiliar piece of `agent/service.py` and immediately classify which stop it belongs to and which Go chapter it mirrors
- Estimate the blast radius of a change ("adding a new watchdog doesn't touch session, adding a new provider doesn't touch the agent loop")
- Distinguish which upstream design choices are necessary and which are over-engineered (see Appendix A's trade-off analysis)
- Spot which sub-systems are entirely orthogonal — telemetry, sync, skills, MCP all live alongside the agent loop, not inside it; knowing this lets you ignore them when reading the core
- Read a Python idiom you've never seen (a metaclass, a `Protocol`, a Pydantic `@model_validator`) and recognize that it is solving a problem you already solved differently in Go (interface, embedded struct, method receiver)

The recommended next step is to **design your own sNN** — for example s13 = browser profile management + cookie persistence (corresponding to `browser/profile.py`, 1288 lines), or s14 = sandbox mode (corresponding to `browser_use/sandbox/`), or s15 = MCP server (corresponding to `mcp/server.py`, 1280 lines). Write a chapter plan in this repo's six-section style — Problem / Solution / Code surface / Tests / Upstream Source Reading / What changed — then implement against the plan.

A good chapter-design heuristic: pick an upstream file in the 500-1500 LOC range, identify the 200-400 line core mechanism, and aim to re-express that mechanism in 300-600 lines of Go. Files below 500 lines tend to be too thin to warrant a chapter; files above 1500 lines tend to bundle multiple concerns that should be split into 2-3 chapters. The 12 sessions in this repo all picked targets in that sweet spot — and the boundaries between them are not arbitrary, they reflect where the upstream code itself splits naturally along seams.

Treat `plan.md`'s **Risks & open questions** section as a problem bank. The 7 limitations listed there (CDP stubbing scope, DOM snapshot fidelity, multi-provider parity, planning vs reactive agent, async semantics, Go version, no external deps in early sessions) are each a potential sNN topic. Each one represents a deliberate simplification this repo made — undoing one of them is itself a learning exercise.

The deepest way to learn a project is not to read its code — it is to **rewrite it using its design philosophy**. These 12 chapters plus 2 appendices have handed you the philosophy. Where you take this repo next is up to you.
