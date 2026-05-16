---
title: "附录 B · 上游源码导读地图"
chapter: "appendix-b"
slug: appendix-b-upstream-map
est_read_min: 15
---

# 附录 B · 上游源码导读地图

> 怎么按顺序读懂上游 browser-use 的 ~98K 行 Python？这一章给出一条**渐进式阅读路径**，把每个章节学到的概念映射到上游的具体文件 + 行号。

在 12 节 Go 练习之后，你已经亲手实现了 Provider、MessageManager、Registry、Watchdog、Session、DOMService、TokenCost、FileSystem。每个 Go 实现都对应上游的一份 Python——但 Python 仓库总共 25 个目录、约 98000 行代码，直接打开是噪音。这份地图按"依赖最少 → 依赖最多"给你一条 8 站的阅读路线，每一站都告诉你**先读哪一个文件**、**重点看什么**、**对应你已经写过的哪一节 Go**。读完 8 站就理解了 90% 的代码，剩下的（MCP、Sync、Skills、Telemetry）是周边设施，按需翻。

开始读之前先给一个实操建议：开两个窗口。一个显示你当前在读的 upstream Python 文件，另一个显示对应的 Go 节。遇到不认识的 Python 写法时，先切到 Go 窗口——大概率你在 Go 里用不同 idiom 解过同一个问题，类比回去比 Google Python 特性快得多。这 12 节 Go 不只是"upstream 的缩小版"——它是一种翻译，把每个 Python idiom **真正在做什么**显式化出来。

## 阅读顺序

8 大模块按下面这个顺序读最省力。每一步都建立在上一步的术语和数据结构之上，不会出现"先读这个再回头跳那个"的反向依赖。

**第 1 站：`agent/`**。从 `agent/views.py` 开始（1000 行，定义 `ActionResult`、`AgentHistoryList`、`AgentOutput`、`AgentSettings` 这些贯穿整个项目的数据类）。然后跳到 `agent/service.py` 的最上面 200 行——只看 `class Agent` 的字段定义和 `__init__` 签名，先建立"agent 持有哪些东西"的心智模型，**不**要被 4131 行 `step()` 主体吓住。等读完后面 7 站再回头读 `step()` 会顺很多。这一站对应你写过的 s01、s12。

**第 2 站：`llm/`**。先 `llm/base.py`（59 行的 Protocol 定义），再 `llm/messages.py`（消息类型）和 `llm/schema.py`（结构化输出 schema），最后挑一个 provider——推荐 `llm/openai/chat.py`，因为它是你 s02 实现的对标版。Anthropic 版本（`llm/anthropic/chat.py`）放到 Phase G 多模型扩展时再读。这一站对应 s02。

**第 3 站：`tools/`**。`tools/registry/views.py` 看数据形状（`ActionRegistry`、`RegisteredAction`），`tools/registry/service.py` 看注册逻辑（特别是 `_normalize_action_function_signature` 这个函数——它从 Python 函数签名自动生成 Pydantic schema，非常巧妙）。然后 `tools/service.py` 看 dispatcher（注意 180s 全局 timeout）。这一站对应 s04。

**第 4 站：`browser/`**。从 `browser/watchdog_base.py` 开始（321 行的反射注册）——这是 s06 你写过的等价物，看看 Python 版本怎么用 metaclass 做 LISTENS_TO 检查。然后 `browser/session.py` 的前 300 行（构造和 `start()` 方法），最后挑两个 watchdog 当例子：`watchdogs/downloads_watchdog.py`（1382 行的最复杂例子）和 `watchdogs/popups_watchdog.py`（145 行的最简例子）。这一站对应 s06、s07。

**第 5 站：`dom/`**。`dom/views.py` 看 DOMNode 数据结构（1041 行 Pydantic 模型），`dom/serializer/serializer.py` 看从 raw CDP snapshot 到 LLM 文本的完整 pipeline（1290 行——这是项目里最密的一个文件），最后 `dom/service.py` 看 cache 和 invalidation。这一站对应 s08、s09。

**第 6 站：`actor/`**。`actor/element.py`（1182 行）。前 200 行看 `class Element` 字段；中间看 `click()` / `type()` 方法怎么用 BackendNodeId 派发 CDP 事件；最后看 retry 逻辑。这一站对应 s05。注意这一站可以晚一点读，它是浏览器层的最底层，不依赖前几站。

**第 7 站：`filesystem/`**。`filesystem/file_system.py`（941 行）。看 `LocalFileSystem` vs `CloudFileSystem` 两个子类怎么共享 `FileSystem` 抽象基类，以及 binary 扩展名和 path traversal 的拦截在哪里。这一站对应 s11。

**第 8 站：`tokens/`**。`tokens/service.py`（605 行）。看 `TokenCost.initialize()` 怎么从 LiteLLM 拉 pricing JSON、怎么本地 cache、怎么和 Provider 调用钩起来。这一站对应 s10。

读完这 8 站，再回头读 `agent/service.py` 的 `step()` 主体——你会发现它只是把 8 站的零件按顺序串起来，没有黑魔法。

## 上游文件 → 本仓库章节映射

下面这张表覆盖 30+ 个上游文件，每行说明这个文件做什么、对应你写过的哪一节、读这个文件时重点看什么。所有路径都已在 SHA `933e28c599ddd74c15a48568f159da95547e40dd` 下验证存在。

| 上游文件 | 主要 class / 函数 | 对应章节 | 重点看什么 |
|---|---|---|---|
| `browser_use/agent/service.py` (4131 行) | `class Agent`, `Agent.step()`, `Agent.run()` | s01, s03, s12 | 先看 `__init__` 字段（行 131-300），再看 `run()` 主体（行 2483+），最后看 `step()`（行 1023+）—— 你在 s12 的 `Run(ctx, task)` 就是它的简化版 |
| `browser_use/agent/views.py` (1000 行) | `ActionResult`, `AgentOutput`, `AgentHistoryList`, `AgentSettings` | s01, s12 | 项目里几乎所有数据都流过这些 Pydantic 类，先认数据形状再读逻辑 |
| `browser_use/agent/message_manager/service.py` (597 行) | `class MessageManager`, `prepare_step_state()`, compaction logic | s03 | 历史长度阈值检查 + summarize 调用 + 敏感信息脱敏的三件套 |
| `browser_use/agent/message_manager/views.py` | `MessageManagerState` | s03 | 持久化 message manager 的状态结构 |
| `browser_use/agent/system_prompts/system_prompt.md` | (静态 prompt) | s12 reference | 看 browser-use 怎么用一段 markdown 当 system prompt 模板；s12 里你用的是简化版 |
| `browser_use/agent/judge.py` | `class JudgeService` | s_full deliberate omission | 二次 LLM 评分 agent 决策，我们整个仓库都跳过了——感兴趣再读 |
| `browser_use/llm/base.py` (59 行) | `BaseChatModel` (Protocol), `ChatInvokeCompletion` | s02 | 全项目最小但最重要的文件，整个 16 provider 体系靠它统一接口 |
| `browser_use/llm/openai/chat.py` (306 行) | `class ChatOpenAI` | s02 | 对照你 s02 写的 `OpenAIProvider`；注意 Python 版的 streaming 和 tool_choice 处理 |
| `browser_use/llm/openai/serializer.py` | message → OpenAI wire format | s02 | unified Message 怎么变成 OpenAI 的 `{role, content, tool_calls}` |
| `browser_use/llm/anthropic/chat.py` (260 行) | `class ChatAnthropic` | Phase G multi-model | 跟 OpenAI 版本对比，看 `tool_use` block 和 `content` 数组的差异 |
| `browser_use/llm/anthropic/serializer.py` | message → Anthropic wire format | Phase G multi-model | Anthropic 的 system message 单独传，user/assistant 才进 messages 数组 |
| `browser_use/llm/messages.py` (238 行) | `UserMessage`, `AssistantMessage`, `ToolResultMessage`, `ContentBlock` | s01, s02 | 你 s01/s02 的 `Message` struct 的直接对标；ContentBlock 的 union type 设计值得注意 |
| `browser_use/llm/schema.py` | structured output schema helpers | s02 | 怎么把 Pydantic model 转成 OpenAI tool schema |
| `browser_use/tools/service.py` (2252 行) | `class Tools`, `Tools.act()`, action timeout guard | s04 | `act()` 的 180s timeout 包装；sensitive data redaction；error 处理 |
| `browser_use/tools/registry/service.py` (601 行) | `class Registry`, `_normalize_action_function_signature()` | s04 | 从 Python 函数签名抽取 Pydantic 参数 model 的反射逻辑，是 s04 schema_gen.go 的灵感源 |
| `browser_use/tools/registry/views.py` | `RegisteredAction`, `ActionRegistry` | s04 | 注册表存什么 |
| `browser_use/tools/extraction/schema_utils.py` | extraction prompt builders | s04 reference | LLM-based 信息抽取的辅助工具，s04 没实现 |
| `browser_use/browser/session.py` (4000 行) | `class BrowserSession`, `get_or_create_cdp_session()`, watchdog attachment | s07 | 前 300 行的 `__init__` + `start()` 最重要，后面是大量 CDP 命令封装 |
| `browser_use/browser/profile.py` (1288 行) | `BrowserProfile`, Chromium 启动参数 | s07 reference | 看 stealth flag、user-data-dir、扩展加载——s07 没实现这些 |
| `browser_use/browser/watchdog_base.py` (321 行) | `BaseWatchdog`, `attach_to_session()`, reflective handler registration | s06 | s06 你写过的反射注册的 Python 原版，特别看 `LISTENS_TO`/`EMITS` 声明和 circuit breaker |
| `browser_use/browser/watchdogs/downloads_watchdog.py` (1382 行) | `DownloadsWatchdog` | s06 example | 最复杂的 watchdog 实例：拦截下载、本地存储、cloud sync——可以挑出 200 行核心 port 到 Go |
| `browser_use/browser/watchdogs/popups_watchdog.py` (145 行) | `PopupsWatchdog` | s06 example | 最简单的 watchdog 实例，s06 教学时可以直接对照 |
| `browser_use/browser/watchdogs/security_watchdog.py` (278 行) | `SecurityWatchdog` | s06 reference | 拦截危险域名和混合内容，看 watchdog 怎么主动阻止动作 |
| `browser_use/browser/watchdogs/dom_watchdog.py` (865 行) | `DOMWatchdog` | s09 reference | 在 navigation 事件下触发 DOM cache invalidation——s09 cache invalidation 的灵感 |
| `browser_use/browser/events.py` | 所有事件类定义 | s06 | 50+ 事件类型一字排开，看 event-driven 系统怎么组织 |
| `browser_use/dom/views.py` (1041 行) | `DOMNode`, `EnhancedDOMTreeNode`, `SerializedDOMState`, `DOMRect` | s08 | s08 你写过的 `DOMNode` struct 的对标，注意 Python 版多了 `accessibility_role`、`computed_styles` 等字段 |
| `browser_use/dom/serializer/serializer.py` (1290 行) | `DOMTreeSerializer`, `serialize_accessible_elements()` | s08 | 项目里最重的单文件，bbox 过滤、paint order、interactive 合并都在里面 |
| `browser_use/dom/serializer/paint_order.py` | paint-order 计算 | s08 | Z-order 遮挡判定算法，s08 的 `paint_order.go` 简化版的来源 |
| `browser_use/dom/serializer/clickable_elements.py` | clickable detection | s08 | 怎么判定一个元素是不是 interactive |
| `browser_use/dom/service.py` (1174 行) | `class DomService`, snapshot orchestration, cache | s09 | 看 cache TTL、navigation invalidation、cross-origin iframe 处理 |
| `browser_use/actor/element.py` (1182 行) | `class Element`, `click()`, `type()`, `screenshot()` | s05 | BackendNodeId 派发 CDP `Input.dispatchMouseEvent` 的完整路径；retry 逻辑值得读 |
| `browser_use/actor/mouse.py` | 鼠标事件构造 | s05 reference | 鼠标 down/up/move 三段式构造，s05 stub 没做 |
| `browser_use/filesystem/file_system.py` (941 行) | `FileSystem` (ABC), `LocalFileSystem`, `CloudFileSystem` | s11 | s11 的对标，注意 binary extension 列表和 path traversal 检查的精确位置 |
| `browser_use/tokens/service.py` (605 行) | `class TokenCost`, `register_llm_invocation()`, pricing cache | s10 | s10 的对标，pricing 从 LiteLLM repo 拉的逻辑 + 1-day TTL |
| `browser_use/tokens/views.py` | `TokenUsageEntry`, `Cost` | s10 | 数据形状，s10 直接对应 |
| `browser_use/observability.py` (204 行) | `@observe` decorator | s12 reference | 跨函数日志/追踪的装饰器实现，s12 跳过了 |
| `browser_use/mcp/server.py` (1280 行) | MCP server entry, tool schema | s_full deliberate omission | 把 Agent 暴露成 MCP server 的协议适配层，整个仓库跳过 |
| `browser_use/mcp/client.py` | MCP client | s_full deliberate omission | MCP 客户端，方便接 Claude Desktop 之外的 MCP server |
| `browser_use/sync/service.py` | cloud sync | s_full deliberate omission | 跟 api.browser-use.com 同步 agent run 数据，整个仓库跳过 |
| `browser_use/skills/service.py` | Claude skill 集成 | s_full deliberate omission | Claude Code skill 的桥接层，与 agent loop 关系不大 |
| `browser_use/telemetry/service.py` | `ProductTelemetry` | s_full deliberate omission | PostHog 事件上报，s12 跳过 |

## 扩展练习

读完上游源码后，你大概率会想动手实现一些上游有、本仓库没做的功能。下面是 5 个具体的扩展练习，每个都对应一个上游模块，建议作为本仓库后续 sNN 的候选。

**练习 1：把 DownloadsWatchdog 完整 port 到 s06 风格的 Go**。上游 `browser/watchdogs/downloads_watchdog.py` 有 1382 行，但核心逻辑只有约 300 行：监听 `Browser.downloadWillBegin` CDP 事件 → 选择本地保存路径 → 监听 `Browser.downloadProgress` → 完成后 emit `DownloadCompletedEvent`。其他大部分是 cloud sync 和 error handling。你可以在 s06 的 EventBus + Watchdog 框架上加一个 `DownloadsWatchdog`，约 200 行 Go。重点是模拟 CDP 下载事件（用 `RecordingCDPClient` 注入 fake event）并验证 Watchdog 的事件路由正确。

**练习 2：在 s02 加 Anthropic provider**。s02 你写了 OpenAI provider；现在仿照 `llm/anthropic/chat.py` 加一份 `AnthropicProvider`。难点不在 HTTP 调用，而在**消息格式转换**——Anthropic 的 `messages` 数组里不能有 system 消息（system 单独传），content 是 `[{type: "text"|"tool_use"|"tool_result", ...}]` 数组而不是字符串。约 200 行 Go，外加一个 `provider_translator.go` 做 unified `Message` 到 Anthropic wire format 的映射。这是 Phase G 多模型扩展的实质。

**练习 3：把 s05 的 stub CDP 换成 chromedp 真实控制 Chromium**。s05 的 `RecordingCDPClient` 只记录"会发什么 CDP 帧"，不真发。如果你想跑真浏览器，可以引入 `github.com/chromedp/chromedp` 包，把 s05 的 click/type/screenshot 方法改成真调用 `chromedp.Run(ctx, chromedp.Click(...))` 那一套。这是个大改动，建议单独作为一个新 module（比如 `agents/sNN-real-cdp`），并且要在 `go.mod` 里加 chromedp 依赖——这是本仓库第一次引入第三方依赖。

**练习 4：实现一个最简单的 MCP server**。把 s12 的 `Agent` 暴露成 MCP server，让 Claude Desktop 可以通过 MCP 调用它。MCP 是 JSON-RPC over stdio。最小实现需要：(1) `initialize` 握手响应、(2) `tools/list` 返回一个 `run_agent_task(task: string)` tool schema、(3) `tools/call` 收到调用后启动一个 `Agent.Run(task)` 并把结果返回。可以参考 `mcp/server.py` 的 stdin/stdout 处理。约 300 行 Go，外加一个测试用 echo client。

**练习 5：加 fingerprint 对抗**。s07 启动浏览器时（即使是 stub）你已经能注入"启动参数"。试着加一些 stealth 技巧：(1) 注入一段 JS 在每个 page load 时跑 `Object.defineProperty(navigator, 'webdriver', {get: ()=>false})`；(2) 在 Canvas API 上加 noise——`getContext('2d').getImageData()` 返回值的最后一位 bit 做随机扰动；(3) 在 WebGL `getParameter(VENDOR)` 上做 spoof。约 150 行 Go——主要是 JS 字符串模板和 CDP `Page.addScriptToEvaluateOnNewDocument` 的调用。

## 读完之后

走完 8 站源码、做完 5 个扩展练习，你对 browser-use 的代码版图就基本完整了。你应该能：

- 看一段陌生的 `agent/service.py` 代码，立刻分辨它属于哪一站、对应你 Go 仓库的哪一节
- 估算一个改动的影响面（"加一个新 watchdog 不影响 session，加一个 provider 不影响 agent loop"）
- 判断哪些 upstream 设计是必要的、哪些是过度工程（参考附录 A 的 trade-off 分析）
- 辨认出哪些子系统是完全正交的——telemetry、sync、skills、MCP 都跟主 agent loop 平行，不在它内部；读核心时可以彻底跳过它们
- 看到一个你没见过的 Python idiom（metaclass、`Protocol`、Pydantic `@model_validator`）能马上意识到"这是在解一个我在 Go 里用别的方式解过的问题"（接口、嵌入 struct、方法接收者）

下一步建议你尝试**自己设计一个 sNN**——比如 s13 = browser profile 管理 + cookie 持久化（对应 `browser/profile.py` 1288 行），或者 s14 = sandbox 模式（对应 `browser_use/sandbox/`），或者 s15 = MCP server（对应 `mcp/server.py` 1280 行）。设计时按本仓库的"Problem / Solution / Code surface / Tests / Upstream Source Reading / What changed"六段式写一份 plan，然后从 plan 出发实现。

一个有用的章节设计经验法则：挑一个 500-1500 行的 upstream 文件，识别其中 200-400 行的核心机制，目标是用 300-600 行 Go 把那个机制重新表达出来。500 行以下的文件通常太薄、撑不起一节；1500 行以上的通常打包了多个关心点，应该拆成 2-3 节。本仓库 12 节都挑在这个甜区——而且节之间的边界不是随便切的，它们对应 upstream 代码自己的天然接缝。

也可以参考 `plan.md` 的 **Risks & open questions** 那一节作为题目库——里面列的 7 条限制（CDP stubbing scope、DOM snapshot fidelity、multi-provider parity、planning vs reactive agent、async semantics、Go version、no external deps in early sessions）每一条都是一个潜在的 sNN 主题。每一条都代表本仓库做出的一个有意简化——解除其中任何一个限制，本身就是一个学习练习。

学习一个项目最深的方法不是读它的代码，而是**用它的设计哲学去重写它**。这 12 节 + 2 附录已经把哲学交给你了；剩下的看你想把这个仓库往哪里推。
