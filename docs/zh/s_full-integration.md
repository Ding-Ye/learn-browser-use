---
title: "s_full · 端到端集成"
chapter: "_full"
slug: s_full-integration
est_read_min: 15
---

# s_full · 端到端集成

> 没有新代码。这一章是把 s01 → s12 全部 12 个模块组装起来，跟着一个真实的 user task 走完 16 步轨迹，并明确列出"我们故意没做"的清单。

---

## 总览 · Architecture

到这一章为止，前面 12 章像是 12 块独立的拼图：每一章都是一个**自足的 Go module**（自己的 `go.mod`、自己的 `main.go`、自己的测试），且**互不 import**。这种"自足"对学习是好事——读者可以从任意一章切进去，不用先理解前面的所有铺垫。但代价是：直到 s12 才出现一个真正的 `Agent.Run(task)`，把 11 章造出来的零件首次拼成一个可执行的 agent。

s_full 不写代码——它的目的是回答两个集成层面的问题：

1. **"这些零件实际上是怎么协同工作的？"** —— 给出一张 12 节点的架构图，每个组件标注它的归属章节、对外接口、依赖关系。
2. **"上游真实的 user task 一路上是怎么穿过这些组件的？"** —— 用 16 步追踪（来自 research-notes.md A3 节）把每一步的"上游位置 / 我们的 Go 位置 / 这步在干什么"做精确对应。

读完这一章后，读者应该能在脑中**同时**持有两张地图：

- **静态地图**：哪个 struct 持有哪些字段、谁能调用谁。
- **动态地图**：一次 `agent.run(task)` 会按什么顺序点亮哪些方法。

下面是静态地图。8 大顶层组件，依赖箭头从上到下、从左到右；同一行的组件互不依赖：

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
        │                     │  (5 个)  │  │ Watchdogs  │
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

**几条不显然的边**：

- **Provider → Message-Manager**：是单向的——MessageManager 不知道 Provider 的存在，Provider 拿到的是一个 `[]Message`，从 MessageManager `Get()` 出来。MessageManager 只管"管历史"，不管"发给谁"。
- **Registry → BrowserSession**：通过 tool 间接连接。`SearchTool` / `ClickTool` 等 5 个 tool（s12 的 `tools.go`）持有 `*BrowserSession` 指针，在 `Run()` 里通过 `Session.Client.Send(...)` 调 CDP。
- **DOMService → EventBus**：DOMService 在 `NewDOMService(bus)` 时**订阅** `NavigationEvent`，下一次 `Get()` 会因为缓存失效重算。这是 s09 教的 "subscribe-at-construction" 模式的生产兑现。
- **Element-Actor 在 s12 没有直接出现**：s05 的 `Element` 是 CDP 操作的抽象，但 s12 没有再用 `Element` struct——它直接用 `Session.Client.Send("Input.dispatchMouseEvent", ...)` 走 CDP。这是教学决策：保持 s12 焦点在"集成"，把 Element 留给读者自己接回去（见"如果要继续扩展"）。
- **FileSystem 在 s12 是悬挂的**：`Agent.FS` 字段存在，但 5 个内建 tool 没有一个调用它。这是**故意**的——`TestFilesystemSandboxRejectsAbsolutePath` 证明接缝是真的，但 demo 不写文件以保持输出干净。读者要加一个 `write_note(content)` tool 时，5 行 Go 就能接进来。

## 端到端追踪：agent.run("find latest news on Hacker News")

这一节把 research-notes.md (A3 节) 里 16 步追踪逐步具体化。每步给出四个字段：

- **Who**：哪个组件在执行
- **Where (上游)**：Python 上游的具体文件 + 行号
- **Where (mini)**：我们 Go 复现里的具体文件 + 行号
- **What**：1-2 句话描述这一步实际在做什么

### Step 1: 用户代码实例化 Agent + await agent.run()

**Who**: 用户代码 / Agent
**Where (上游)**: `browser_use/agent/service.py#L131-L160` (`Agent.__init__`)
**Where (mini)**: `agents/s12-agent-loop-full/main.go#L71-L84` (Agent struct literal) + `agents/s12-agent-loop-full/agent.go#L63-L107`
**What**: 用户把 task 字符串、`ChatBrowserUse()`/`ChatAnthropic()` 等 LLM、`Browser()` 实例传给 `Agent(...)`，然后调 `await agent.run()`。在我们 mini 版里这是 `&Agent{Provider: mock, ..., DOM: dom, ...}` 加 `agent.Run(ctx, "...")`。Python 用 keyword-only 参数 + 默认值；Go 用 struct literal + 显式默认（`applyDefaults`）。

### Step 2: Agent 构造时拉起 MessageManager / BrowserSession / Tools / DomService / EventBus

**Who**: Agent
**Where (上游)**: `browser_use/agent/service.py#L131-L160` (`Agent.__init__`) + `browser_use/agent/service.py#L1023-L1073` (`Agent.step` 的依赖项)
**Where (mini)**: `agents/s12-agent-loop-full/main.go#L46-L84` (依赖一次性构造)
**What**: 上游在 `__init__` 里 chain 起 `MessageManager`、`BrowserSession`、`Tools`、`DomService`、`bubus.EventBus`，并用 `model_rebuild()` 处理 Pydantic 前向引用。我们 Go 版本不需要 `model_rebuild`——`NewBrowserSession`、`NewDOMService`、`NewRegistry`、`NewMessageManager`、`NewTokenCost` 各自负责自己的初始化，main.go 显式 wire。

### Step 3: Agent.run() 注册 signal handler + 发送 CreateAgentSessionEvent / CreateAgentTaskEvent

**Who**: Agent
**Where (上游)**: `browser_use/agent/service.py#L2483-L2540` (`run()` 的 signal + telemetry 段)
**Where (mini)**: 故意省略（见"Deliberate omissions"中的 Cloud sync / Telemetry 行）
**What**: 上游 `run()` 第一件事是 `SignalHandler.register()`（Ctrl+C → pause/resume），第二件是 `eventbus.dispatch(CreateAgentSessionEvent.from_agent(self))`（cloud sync 用）。这两件事我们都没做：mini 把 `context.Context` 当成 cancellation 唯一通道；telemetry 是被"故意没做"列表里的一行。

### Step 4: BrowserSession.start() 启动 Chromium + 挂上所有 watchdog

**Who**: BrowserSession
**Where (上游)**: `browser_use/browser/session.py#L673-L678` (`BrowserSession.start()`) + `browser_use/browser/session.py#L1562-L1596` (`attach_all_watchdogs()`)
**Where (mini)**: `agents/s12-agent-loop-full/session.go#L1-L160` + `agents/s07-browser-session/session.go#L47-L196` (s07 的完整版)
**What**: 上游 `start()` 会启动 Chromium 进程（或连到已有 CDP URL）、attach 12 个 watchdog（Downloads、Popups、Security、DOM、Captcha、AboutBlank、Screenshot、HarRecording、LocalBrowser、Permissions、Recording、StorageState）。我们 mini 的 `BrowserSession.Start(ctx)` 调 stub `RecordingCDPClient.Connect()` + 在 `NewBrowserSession` 时挂 1 个 `NavigationWatchdog`，发 `SessionStartedEvent`。**没有真实进程被启动**。

### Step 5: Message 准备 / Agent._prepare_context()

**Who**: Agent / MessageManager
**Where (上游)**: `browser_use/agent/service.py#L1075-L1148` (`_prepare_context`)
**Where (mini)**: `agents/s12-agent-loop-full/agent.go#L132-L139` (system + task 注入) + `agents/s12-agent-loop-full/agent.go#L161-L175` (Phase 2 观察)
**What**: 上游每步开头调 `_prepare_context` —— 拿 `browser_state_summary`、调 `_message_manager.prepare_step_state(...)`、注入 budget warning / replan nudge / exploration nudge / loop detection nudge / force-done 之类的提示。我们 mini 压缩成两次 `Messages.Add()`：一次 system prompt（`Run()` 开始时一次性），一次每步的 `[browser_state]` user 消息。Nudge 全部省略。

### Step 6: LLM 调用 / Provider.Invoke

**Who**: Provider
**Where (上游)**: `browser_use/llm/base.py#L17-L60` (Protocol) + `browser_use/agent/service.py#L1163-L1187` (`_get_next_action` 调 `asyncio.wait_for`)
**Where (mini)**: `agents/s12-agent-loop-full/agent.go#L243-L271` (`invokeWithFallback`) + `agents/s12-agent-loop-full/provider.go#L31-L33` (Provider 接口)
**What**: 上游用 `asyncio.wait_for(self._get_model_output_with_retry(input_messages), timeout=self.settings.llm_timeout)`；我们用 `context.WithTimeout(ctx, a.LLMTimeout)` + `Provider.Invoke(primaryCtx, msgs, tools)`。两者形状一致——都是"per-invocation deadline"。差别：我们加了 `Fallback Provider`（上游靠外部 retry middleware）。

### Step 7: LLM 响应解析 / 提取 actions 列表

**Who**: Agent / Provider
**Where (上游)**: `browser_use/agent/service.py#L1188-L1197` (设置 `state.last_model_output`)
**Where (mini)**: `agents/s12-agent-loop-full/agent.go#L186-L196` (追加 assistant 消息) + `agents/s12-agent-loop-full/agent.go#L198-L208` (`StopReason` 分支)
**What**: 上游用 Pydantic 的 `AgentOutput` 把 LLM JSON 强转成 `model_output.action: list[ActionModel]`。我们 mini 用三态 `StopReason`（`end_turn` / `tool_use` / `max_tokens`），由 `Response.Actions []ActionCall` 描述 tool_use 列表。两边都依赖 LLM 端 structured output。

### Step 8: Action 验证 / Pydantic 校验

**Who**: Tools / Registry
**Where (上游)**: `browser_use/tools/registry/service.py#L74-L289` (`_normalize_action_function_signature` + `RegisteredAction.param_model`)
**Where (mini)**: `agents/s12-agent-loop-full/registry.go#L1-L60` (Tool 接口 + Registry) + `agents/s04-tool-registry/schema_gen.go#L1-L175` (s04 教的 reflection-based schema 生成)
**What**: 上游用 `RegisteredAction.param_model(**params)` 把 dict 校验成强类型 Pydantic 模型。我们 mini 在 `tools.go` 里手写每个 tool 的 schema JSON + 在 `Tool.Run` 里手动 `json.Unmarshal`。s04 教过用 reflection 自动生成；s12 故意写死以让"集成"成为焦点而不是"reflection 机制"。

### Step 9: Tool 执行 / 索引到 CDP backendNodeId 的映射

**Who**: Tools / Dispatcher / Actor
**Where (上游)**: `browser_use/tools/service.py#L420-L500` + `browser_use/actor/element.py#L62-L300` (Element.click 等) + `browser_use/browser/watchdogs/default_action_watchdog.py#L906-L1180` (真实的 `Input.dispatchMouseEvent` / `Input.insertText` 调用)
**Where (mini)**: `agents/s12-agent-loop-full/agent.go#L210-L222` (Phase 7 派发) + `agents/s12-agent-loop-full/tools.go#L36-L82` (SearchTool.Run) + `agents/s05-element-actor/element.go#L45-L100` (s05 的 Click)
**What**: 上游每个 action（click / type / scroll）通过 `Element` 类调 `Input.dispatchMouseEvent` / `Input.insertText` / `Input.dispatchKeyEvent`，BackendNodeId 是 CDP 端的稳定 DOM ID。我们 mini 的 `tools.go` 直接 `Session.Client.Send("Input.dispatchMouseEvent", map[string]any{...})`，但 Client 是 stub `RecordingCDPClient`——只**记录**会发送什么帧，不真的连 WebSocket。

### Step 10: Browser 状态捕获 / DOMSnapshot.captureSnapshot

**Who**: DomService
**Where (上游)**: `browser_use/dom/service.py#L1042-L1096` (`get_serialized_dom_tree`) + `browser_use/dom/service.py#L535-L560` (`captureSnapshot` 调用)
**Where (mini)**: `agents/s12-agent-loop-full/dom_service.go#L1-L132` + `agents/s09-dom-service/dom_service.go#L1-L209` (s09 完整版) + `agents/s08-dom-serializer/serializer.go#L1-L275` (s08 的 serializer)
**What**: 上游通过 `cdp_client.send.DOMSnapshot.captureSnapshot()` 拿到完整 layout tree（包含 layoutNodes、textBoxes、computedStyles、paintOrder），再通过 `DOMTreeSerializer.serialize_accessible_elements()` 转成 LLM 友好的 indexed text + `selector_map: dict[index, DOMRect]`。我们 mini 的 `DOMService.Get(ctx)` 返回写死的 `SerializedDOM` fixture——不是真 snapshot，是按 URL 切换的硬编码字符串。

### Step 11: 截图 / Page.captureScreenshot

**Who**: BrowserSession / ScreenshotWatchdog
**Where (上游)**: `browser_use/browser/session.py#L1517-L1553` (`get_browser_state_summary(include_screenshot=True)`) + `browser_use/browser/watchdogs/screenshot_watchdog.py`
**Where (mini)**: 故意省略（见"Deliberate omissions"中的 screenshot 行）
**What**: 上游每步在 `_prepare_context` 里 `include_screenshot=True`——通过 `BrowserStateRequestEvent` 让 `ScreenshotWatchdog` 调 `Page.captureScreenshot` 拿 base64 PNG，注入到 user message 作为视觉模态。我们 mini 一行都不做截图——`use_vision` 是被故意没做列表里的一项。

### Step 12: ActionResult 聚合 / 写回 step 历史

**Who**: Agent / MessageManager
**Where (上游)**: `browser_use/agent/views.py#L307-L350` (`class ActionResult`) + `browser_use/agent/service.py#L1199-L1206` (`_execute_actions` 把 result 写进 `state.last_result`)
**Where (mini)**: `agents/s12-agent-loop-full/agent.go#L210-L223` (Phase 7 收集 tool_result + Messages.Add)
**What**: 上游每个 action 返回 `ActionResult(extracted_content=..., error=..., long_term_memory=..., is_done=...)`，被 `multi_act()` 收成 `list[ActionResult]`，写入 `state.last_result`。我们 mini 把每个 tool 的 `(string, error)` 包成 `ContentBlock{Type: "tool_result", Result: ...}`，所有 block 合到一个 `Message{Role: "tool"}` 里。

### Step 13: 循环条件检查 / done() 提早退出

**Who**: Agent
**Where (上游)**: `browser_use/agent/service.py#L2580-L2613` (`while self.state.n_steps <= max_steps` + `if is_done: break`)
**Where (mini)**: `agents/s12-agent-loop-full/agent.go#L198-L208` (`StopReason` 分支) + `agents/s12-agent-loop-full/agent.go#L218-L228` (`DoneResultPrefix` 检测)
**What**: 上游 `_execute_step` 返回 `is_done` flag，从 `done` action 的 `ActionResult.is_done=True` 提取。我们 mini 用字符串前缀哨兵：`DoneTool.Run` 返回 `"__done__:..."`，`Agent.Run` 在 Phase 8 用 `strings.HasPrefix` 检测后退出。两者都是"哨兵 + 早出"模式；上游是 typed bool，mini 是 typed prefix。

### Step 14: Step N+1 准备 / MessageManager.maybe_compact

**Who**: MessageManager
**Where (上游)**: `browser_use/agent/service.py#L1150-L1161` (`_maybe_compact_messages`) + `browser_use/agent/message_manager/service.py#L213-L350` (`maybe_compact_messages`)
**Where (mini)**: `agents/s12-agent-loop-full/message_manager.go#L58-L76` (`Get()` 里的 lazy KeepLastN) + `agents/s03-message-manager/compaction.go#L1-L141` (s03 的策略)
**What**: 上游用一个二级 LLM (`settings.compaction_llm`) 把老对话 summarize 成短文本。我们 mini 用 `KeepLastN`：当 `len(History) > MaxMessages`，`Get()` 返回 `[History[0]] ++ History[-(MaxMessages-1):]`。两者都"在 Get 时懒做、不在 Add 时做"——这个时序选择上下游一致。

### Step 15: 终止 / agent.close() → BrowserSession.close()

**Who**: Agent / BrowserSession
**Where (上游)**: `browser_use/browser/session.py#L700-L728` (`BrowserSession.stop()`)
**Where (mini)**: `agents/s12-agent-loop-full/session.go#L1-L160` (`Session.Stop`) + `agents/s12-agent-loop-full/main.go#L54` (`defer sess.Stop(ctx)`)
**What**: 上游 `stop()` 会发 `SaveStorageStateEvent`（持久化 cookies）、`BrowserStopEvent`、然后 `event_bus.stop(clear=True, timeout=5)`——所有 watchdog 收到通知后清理 CDP 资源。我们 mini 的 `Stop(ctx)` 调 `Client.Disconnect()` + 给所有 watchdog 发 `SessionStoppedEvent` + `Bus.Clear()`。没有 cookie 持久化，没有 timeout 等待。

### Step 16: 返回 AgentHistory / 包含 steps、final result、token usage

**Who**: Agent
**Where (上游)**: `browser_use/agent/service.py#L2634-L2640` (`return self.history`) + `browser_use/agent/views.py#L307-L1000` (`AgentHistoryList`)
**Where (mini)**: `agents/s12-agent-loop-full/agent.go#L200-L201` (`return resp.Text, nil`) + `agents/s12-agent-loop-full/agent.go#L226-L228` (`return finalAnswer, nil`)
**What**: 上游返回 `AgentHistoryList[AgentStructuredOutput]`——一个富对象，含 `history: list[AgentHistory]`（每步 model_output / result / state / metadata）、`usage: ChatInvokeUsage`、`final_result()` accessor。我们 mini 返回 `(string, error)`——只有最终答案。如果调用方想要全转录，要从 `Messages.History` + `Cost.History` 自己拼。这是教学简化：奖励 caller 的最小手指肌肉。

---

## 我们故意没做（Deliberate omissions）

下面 14 项功能上游都有，mini 一律没做。每一行都标出上游具体在哪、为什么我们跳过它。

| Feature | Upstream location | Why we skipped |
|---|---|---|
| 真正的 CDP WebSocket | `browser_use/browser/session.py#L1402-L1500` (`get_or_create_cdp_session`) + 依赖 `cdp-use` 第三方库 | s05 + s12 用 `RecordingCDPClient` stub。原因：教学焦点是"CDP 是个 frame 流"，不是"WebSocket 握手 + reconnect 协议"。要换真的 CDP 接入 `chromedp`，见"如果要继续扩展" |
| 真正的 Chromium 启动 (chromedp) | `browser_use/browser/watchdogs/local_browser_watchdog.py` (整个文件) | 同上。真正的 Chromium 启动牵涉 binary 检测、profile dir、`--use-gl=swiftshader` 等十几个 flag；我们的目标是"看清 agent 是怎么调动 browser 的"，不是"成为 Chromium 启动器" |
| DOM 树的 mutation observer / 增量更新 | `browser_use/browser/watchdogs/dom_watchdog.py` + `browser_use/dom/service.py#L385-L500` (`_get_all_trees`) | s09 教 cache + invalidate-on-navigation 这一对，但上游另有一套"DOM 变了就重算"机制。我们跳过：在 stub fixture 体系下，mutation 不存在 |
| DOMSnapshot 的完整 layout 字段 | `browser_use/dom/service.py#L535-L560` (`DOMSnapshot.captureSnapshot` 调用) + 返回的 `layoutNodes` / `textBoxes` / `computedStyles` 全字段 | s08 的 testdata 是 10-20 个 node 的手写 fixture；上游每帧是几千 node 的真 Chromium 输出。我们留出"shape 一致"——只是 fixture 简单很多 |
| Skill 系统 | `browser_use/skills/service.py#L1-L285` + `browser_use/agent/service.py#L1109-L1112` (`_get_unavailable_skills_info`) | Skill 是 browser-use 把"Claude skill"接成 agent 动作的机制。我们没接 Anthropic skill 协议，因为它和 agent loop 的核心机制正交 |
| Cloud sync | `browser_use/sync/service.py#L1-L161` + `browser_use/agent/cloud_events.py#L187-L260` (`CreateAgentTaskEvent` / `CreateAgentSessionEvent`) | 商业 SaaS 功能。教学 repo 不应依赖云服务 |
| MCP server / client | `browser_use/mcp/server.py#L1-L1280` + `browser_use/mcp/client.py` | MCP 是把 agent 暴露给 Claude Desktop 的协议层。完全可以独立学；我们的范围是"agent 内部如何工作"，不是"agent 怎么被外部消费" |
| Telemetry to PostHog | `browser_use/telemetry/service.py#L1-L112` + `@observe` 装饰器贯穿 `agent/service.py` | 教学 repo 不该往第三方 analytics 发数据 |
| Judge LLM (评估 agent 决策的二级 LLM) | `browser_use/agent/judge.py` (整个文件) + `browser_use/agent/views.py#L307-L320` (`JudgementResult`) | s12 已经加了 planner + fallback 两个新策略；Judge 是第三个，但要演示它需要多 wire 一个 Provider 实例，对集成主线干扰大。读者可自己加一个 `Judge Provider` 字段 |
| 真正的 captcha 检测 / 等待 | `browser_use/browser/watchdogs/captcha_watchdog.py#L1-L207` + `browser_use/agent/service.py#L1031-L1049` (Phase 0 captcha) | 上游 Phase 0 检测 hCaptcha / reCAPTCHA / Cloudflare Turnstile，最多等 60s。我们没接，因为 stub 环境永远不出 captcha |
| HAR recording | `browser_use/browser/watchdogs/har_recording_watchdog.py#L1-L779` | 把每个 CDP 请求/响应保存成 HTTP Archive 格式。生产调试有用；教学没必要——我们的 `RecordingCDPClient.FrameLog()` 已经给出最小可读形态 |
| 真的 anti-bot / stealth 措施 | `browser_use/browser/profile.py` (extension 白名单 + uBlock + canvas-fingerprint randomization) | 见 Appendix A 第二节。教学需要 stub 环境保持确定性；anti-bot 的代价是引入随机化 |
| Sensitive_data 字典格式 | `browser_use/agent/message_manager/service.py#L196-L211` (`prepare_step_state` 的 `sensitive_data` 参数) + `browser_use/agent/service.py#L1117-L1123` | 上游接受 `{key: value}` 替换字典 + 域级别白名单。我们 s03 只演示 regex-based 屏蔽，因为 dict-based 替换没有教学独特性 |
| 多 actions per step 并发 dispatch | `browser_use/tools/service.py#L420-L450` (`multi_act` 的并发分支) + `browser_use/tools/service.py#L77-L200` (action timeout guard) | 上游一次 step 可以并发跑多个 action（例如同时 type + 点 submit）。我们 mini 串行：循环里 `for _, ac := range resp.Actions { ... }`。并发会让追踪变模糊；串行让"哪一步触发了哪次 CDP 帧"是清楚的 |

## 如果要继续扩展

读完前面 12 章 + 这一章后，下面 5 条扩展按"工作量从小到大"列出：

**1. 把 Anthropic provider 加到 s02。** s02 的 `Provider` 接口已经是 provider-agnostic 的（`Invoke(ctx, msgs, tools)`）。新加一个 `AnthropicProvider` 只要：(a) 把 `Message` 转成 Anthropic 的 `messages` 数组（system message 是单独字段，OpenAI 是数组第一项），(b) 把 `tools` 转成 `input_schema` 字段（OpenAI 用 `parameters`），(c) 解析 response 的 `content` block list 而不是 `choices[0].message.tool_calls`。预计 150 行 Go，参考上游 `browser_use/llm/anthropic/chat.py#L1-L100`。

**2. 把 s05 的 stub CDP 换成真的 chromedp。** 步骤：(a) `go get github.com/chromedp/chromedp`，(b) 写一个 `ChromedpClient` 实现 s05/s07 的 `CDPClient` 接口（`Connect() / Disconnect() / Send(method, params) (json.RawMessage, error)`），(c) 在 `Send` 里把 method string 路由到 chromedp 的 typed calls。注意 chromedp 不提供 raw `Send`——你要么改 interface，要么用 chromedp 的 `cdp.Execute(ctx, method, params, &result)`。预计 250 行 Go。

**3. 把 s12 接到一个真的 httptest server，测一个真实 HTML 解析流程。** 现在 demo 的 `httptest.Server` 只是个 placeholder（它返回的 HTML 没人读）。可以扩展：(a) `DOMService.Snapshot` 改成发 HTTP GET 到 `ts.URL` + 拿 HTML + 用 `golang.org/x/net/html` 跑 tokenizer，(b) 把 tokenizer 输出转成 `SerializedDOM.LLMText`。这会让 demo 真的"看到"页面变化。预计 200 行 Go，主要是 HTML parser → SerializedDOM 的适配。

**4. 把 s12 与 s11 的 sandbox 一起用，做"agent 写文件"的演示。** 现在 `Agent.FS` 字段悬挂着没人用。加一个 `WriteFileTool`：(a) struct 持有 `FS FileSystem`，(b) `Schema()` 接受 `{"path": "...", "content": "..."}`，(c) `Run()` 调 `fs.WriteFile(ctx, args.Path, []byte(args.Content))`。然后改 scripted MockProvider 加一步 `write_file` action。`TestFilesystemSandboxRejectsAbsolutePath` 已经证明接缝正确——这步只是把它接到主循环。预计 50 行 Go。

**5. 加一个 Judge LLM 做决策评估。** 在 `Agent` struct 加 `Judge Provider`，每 step 7 之后调一次 `Judge.Invoke(ctx, history, judgePrompt)`，把 `JudgementResult{Score, Reasoning}` 写进 `Cost.History`（或一个新的 `Judgements []JudgementResult` 字段）。`judgePrompt` 是"评估上一个 step 的 action 是否合理"。参考 `browser_use/agent/judge.py`。预计 100 行 Go。

## 上游进一步阅读 · Reading map for going deeper

按"从最简单的入口往复杂处穿"的顺序，下面是 12 章 → 上游对应文件的读 path。先读浅层，再追深处：

1. **s01 → `browser_use/agent/service.py#L1023-L1073` (`Agent.step` 主干)**：上游 220 行的 `step()` 方法，我们用 80 行 Go 镜像了 7-phase 结构。先读这个，看清楚 phase 切分。

2. **s02 → `browser_use/llm/base.py#L17-L60` + `browser_use/llm/openai/chat.py#L1-L100`**：先读 Protocol（59 行），再读 OpenAI 实现（前 100 行就够看清楚 wire shape）。

3. **s03 → `browser_use/agent/message_manager/service.py#L104-L350`**：MessageManager 的 `__init__`、`prepare_step_state`、`maybe_compact_messages` 三个方法。前 250 行覆盖了核心策略。

4. **s04 → `browser_use/tools/registry/service.py#L32-L500`**：Registry 的 `_normalize_action_function_signature` 是核心——上游用 `inspect.signature` + Pydantic 动态建 ActionModel。我们用 Go 反射做的事和这个等价。

5. **s05 → `browser_use/actor/element.py#L62-L300`**：Element class 的 `__init__` + `click` + `fill`。前 300 行能看清楚 BackendNodeId 如何流过 CDP 调用。

6. **s06 → `browser_use/browser/watchdog_base.py#L15-L321`** (整个文件)：321 行，BaseWatchdog 的 `attach_to_session` 用反射检查 `on_EventName` 方法名是核心机制。

7. **s07 → `browser_use/browser/session.py#L101-L800`**：BrowserSession 前 800 行覆盖 `__init__`、`start`、`stop`、`attach_all_watchdogs`。后面 3000 行是 cloud + profile + multi-target 等复杂工程性内容，第一次读可以跳过。

8. **s08 → `browser_use/dom/serializer/serializer.py#L43-L500`**：DOMTreeSerializer 的 `serialize_accessible_elements` + paint-order filter。前 500 行覆盖核心算法，后面是细节优化。

9. **s09 → `browser_use/dom/service.py#L35-L500`**：DomService 的 `__init__` + `get_dom_tree` + `_get_all_trees`。前 500 行覆盖 snapshot 触发 + iframe 处理；后面 700 行是 pagination detection + dropdown handling 等专门概念。

10. **s10 → `browser_use/tokens/service.py#L48-L400`**：TokenCost 的 `initialize`（pricing 拉取）+ `add_usage`（写入）+ `calculate_cost`（账单）。LiteLLM 的 pricing fetch 在 `_fetch_pricing` 里。

11. **s11 → `browser_use/filesystem/file_system.py#L1-L500`**：先读 abstract base `FileSystem` (L353-L505)，再读 `LocalFileSystem` 子类 (L78-L350)，最后读 `write_file` / `read_file` 方法 (L715-L760)。

12. **s12 → `browser_use/agent/service.py#L1023-L2480`**：整合阅读。从 `step()` 开始读到 `_handle_step_error`，把每个 `_xxx` helper 跟着读一遍。这是真实 agent 的全貌——220 行 step + 上千行 helper。

---

读到这里，读者已经看完了一个生产级 browser-use agent 的所有架构层、所有数据流、所有"我们故意没做"的边界。如果还有第 13 件想做的事——把这 12 章里某一章的 mini 实现升级成生产级——上面"如果要继续扩展"那 5 条覆盖了最常见的入口。

12 章一对一镜像上游 `Agent.step()` 的 7 个 phase，整章共用一份 `Provider` / `Tool` / `Event` / `Watchdog` 接口，这不是巧合：LLM-driven agent loop 有一个**标准形状**，我们做的事是把它一块一块拆出来，然后再拼回去。如果有第 13 个 browser-using agent 项目要读，建议先在脑里架起这副骨架，然后把它的代码往这副骨架上挂——大概率挂得上。
