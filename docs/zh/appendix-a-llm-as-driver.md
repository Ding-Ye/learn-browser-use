---
title: "附录 A · LLM-as-driver 哲学与浏览器 agent 工程学"
chapter: "appendix-a"
slug: appendix-a-llm-as-driver
est_read_min: 18
---

# 附录 A · LLM-as-driver 哲学与浏览器 agent 工程学

> 这一章没有代码。讨论的是"为什么 browser-use 要这么设计"——LLM 在浏览器 agent 中扮演什么角色、反检测的现实限制、部署模式、上下文窗口管理策略、CDP 协议的语义假设、watchdog 模式作为解耦工具。

读完前面 12 节，你已经在 Go 里造过一遍 Provider、MessageManager、Registry、Watchdog、BrowserSession、DOMService、TokenCost、FileSystem 这一整套零件，也亲手在 s12 把它们串起来跑过一次循环。每一节里我们都在做"工程"——选数据结构、写接口、加测试。但每一节背后还有一个"哲学问题"：browser-use 为什么这么设计？换种方式会不会更好？这一章把这些问题拉出来谈一谈，不写代码，只谈选择背后的思路。

如果说前面 12 节回答的是"How"，这一章回答的是"Why"。读完之后你应该能用一两句话向同事解释："browser-use 把 LLM 放在驱动位、不是 UI 位，所以它必须把 DOM 序列化做好"，或者"watchdog 模式是 browser-use 把副作用解耦的方式，不是因为事件驱动天然更优雅"。

## LLM-as-driver vs LLM-as-UI

把 LLM 接到浏览器自动化上，有两种截然不同的范式。第一种是 **LLM-as-UI**：框架早就把动作定义好了（`click(selector)`、`type(selector, text)`、`scroll_to(selector)`），LLM 的工作只是从一组预先准备好的 selector 中选一个。这种范式背后的代表是 Playwright + 自然语言 wrapper 这类项目：开发者先用 Playwright 写一套 Page Object，每个按钮、每个表单都对应一个 Python 函数，然后让 LLM 看一份"可用动作清单"，它从清单里挑。

第二种是 **LLM-as-driver**：框架不预先定义任何 selector，只提供一份当前页面的 DOM 序列化快照 + 一个抽象动作 `click(index)`，由 LLM 自己读 DOM、自己决定要点哪个 index。browser-use 走的是这条路。它在 `dom/serializer/serializer.py` 里把页面所有可交互元素打上 0..N 的索引，喂给 LLM；LLM 输出 `{action: "click", index: 7}` 这样的 JSON，框架再把 index 7 反查回 BackendNodeID 去派发 CDP 事件。

两条路的 trade-off 很明确。LLM-as-UI 可预测：动作清单是静态的，测试容易，selector 是工程师写的所以稳定。代价是要为每个网站维护一份 Page Object——这违反了"通用浏览 agent"的初衷。LLM-as-driver 灵活：理论上任何网页都能跑，不用任何前期工程。代价是 DOM 序列化必须做得很好——要去掉不可见的、被遮挡的、嵌套在更大的 interactive 元素里的、视口外的——否则 LLM 看到的 index 列表会膨胀到 token 预算外、或者点不到真正想点的按钮。

browser-use 的 1290 行 `dom/serializer/serializer.py` 和 1174 行 `dom/service.py` 都是在为这件事服务：让 LLM 看到的页面"足够小、足够干净、足够稳定"，使得它能把"语义意图"翻译成"index 数字"而不需要框架介入。这是这个项目最核心的工程取舍。如果未来某一天 LLM 能直接读 raw HTML 而不爆 token，这两个模块大概都可以删掉——但今天还不行，因为 GPT-4 / Claude / Gemini 看到一份 10000 token 的页面就会开始幻觉、错点、漏元素。

值得一提的是，s08 我们让你亲手实现了一遍这套 serializer：DOMNode 树 → SelectorMap[index]DOMRect → LLM 文本。当时你可能没意识到，这正是 LLM-as-driver 范式所有重量的承载点。

## Anti-bot 与 stealth

任何浏览器自动化项目最终都会遇到 anti-bot 检测。`navigator.webdriver = true`、缺失的浏览器扩展、奇怪的 CanvasRenderingContext2D fingerprint、TLS ClientHello 顺序、鼠标移动的 timing 分布——这些信号合起来就足以让 Cloudflare / DataDome / PerimeterX 一类的服务在 3 秒内判定你是机器人，然后弹 CAPTCHA 或者直接 403。

browser-use 在开源版本里只做了最基础的工作。`browser/profile.py` 里通过 CDP 设置 `--disable-blink-features=AutomationControlled` 干掉 `navigator.webdriver`，安装 uBlock Origin 让请求模式看起来更像真人，提供自定义 user-data-dir 让 cookie 和缓存可持久化（很多检测会基于"这个浏览器是不是新装的"做判断）。但更深层的对抗——canvas noise、WebGL spoofing、字体 fingerprint、TLS 重排、住宅 IP 代理——一律没做。

这是有意为之。深度 stealth 是一个永无止境的军备竞赛：每次你绕过一种检测，反检测公司就会更新指纹。把这部分放进开源代码会有两个问题：一是维护成本高到压垮项目，二是法律风险（很多网站的 ToS 明确禁止自动化）。browser-use 把这部分留给 Cloud 版本（`api.browser-use.com`），那里有一个 stealth proxy 层，会自动注入 canvas noise、轮换住宅代理 IP、用 CAPTCHA 求解服务过 reCAPTCHA。开源版本则定位为"开发 + 内部工具 + 友好网站"。

LLM agent 时代还引入了一个新的指纹面：**timing fingerprint**。传统 bot 的鼠标移动是直线或者 Bezier 曲线，时间间隔是常数；真人的移动是颤抖的、暂停的、有思考延迟的。LLM agent 的延迟模式介于两者之间——每个动作前都有一个 1-5 秒的"思考停顿"（等待 LLM 返回 JSON），且这个停顿与页面复杂度不强相关（因为 LLM 看到的是序列化后的 DOM，不是 DOM 大小）。一些前沿的反检测算法已经开始用这个 timing 模式做识别。对抗手段也很反直觉：让 LLM agent **故意慢下来**，并在停顿期间发一些 noise 鼠标移动事件。这是开源项目目前还没解决的问题。

从工程角度来说，stealth 是一个"不应该在第一版代码里做"的东西。在你的 learn-browser-use 里没有任何 stealth 代码——这是对的，因为本仓库不是为了过 Cloudflare 检测设计的。但读完这一节你应该清楚：当你把这套 agent 拿去打真实网站的时候，stealth 是你会撞上的下一个大坑。

## 部署模式

browser-use 实际上有 4 种部署形态，每种背后的工程取舍都不一样。

**本地 Chromium（开发模式）**：这是最常见的开发场景。`browser-use install` 下载一个 Chromium binary 到 `~/.cache/ms-playwright/`，然后你写一段 Python `await Agent(...).run()` 就在自己机器上弹出一个真实浏览器窗口跑任务。优点是调试方便（你能亲眼看到 agent 在干什么）；缺点是有 GUI、占资源、关机就停。教学时这是最适合的形态，因为 LLM 决策的"具体性"和 DOM 操作的"物理性"都可见。

**Docker（CI 模式）**：把开发模式塞进容器。需要 `--use-gl=swiftshader` 让 Chromium 在没有 GPU 的 Linux 容器里渲染（不然 Canvas / WebGL 元素会爆），还要 `--shm-size=2g` 防止共享内存不足导致 tab crash。这是 CI 跑 e2e 测试的标准姿势，但要小心：headless Chromium 的 fingerprint 与 headful 不同，所以 CI 通过的网站不代表生产能跑——很多 anti-bot 服务会专门拒绝 headless。

**Cloud SaaS（生产模式）**：`api.browser-use.com` 是 browser-use 自己的托管版本。你的客户端只发 task 文本，云端跑 agent，结果通过 REST API 返回。云端的好处是有 stealth proxy、住宅 IP、CAPTCHA solver、自动重试，且不用维护 Chromium binary。这是生产部署的现实选择——尤其当你的任务是"每天定时去 N 个网站抓数据"这种长跑场景，自己维护一堆 headless Chrome 太贵了。

**MCP server 模式（Claude Desktop 集成）**：`browser_use/mcp/server.py` 把整个 agent 包成一个 MCP server，通过 stdio 跟 Claude Desktop 通信。用户在 Claude 对话框里说"帮我看看 Hacker News 头条"，Claude Desktop 通过 MCP 调用 browser-use 的工具，browser-use 在用户本机弹出浏览器跑任务，结果回填到对话。这是把"agent 能力"暴露给已有 LLM UI 的方式——agent 不再有自己的 LLM 调用，而是被另一个 LLM driver 使用。架构上这意味着 `mcp/server.py` 内部需要一个 lazy LLM 配置（因为它不知道 Claude Desktop 会不会想用 Anthropic 还是 OpenAI 来做 page extraction 这种子任务）。

四种模式背后的核心问题是**谁来出 LLM、谁来出浏览器、谁来出网络**。本地模式三件都是用户的；Docker 模式三件都在 CI 容器；Cloud 模式三件都在云端；MCP 模式 LLM 来自 Claude Desktop，浏览器和网络来自用户本机。读 `browser_use/__init__.py` 时你会发现 `Agent` 的构造函数能接受所有这些场景的组合——`llm=ChatBrowserUse()` 走云端，`llm=ChatOpenAI()` 走自己 API，`browser=CloudBrowser(...)` 把浏览器也放云端。这是 browser-use 的另一个工程深度：**抽象掉部署形态**。

## 消息压缩与上下文窗口

agent 跑久了会怎样？每一步都往 history 里塞一段 DOM 序列化（动辄 2000 token）、一段 LLM thinking（500 token）、一段 action 结果（200 token）。20 步之后 history 就 50000+ token——任何模型都会撞墙。

直觉的解决方案是 **sliding window**：只保留最近 N 步。这是最简单的策略，但有一个致命缺陷：agent 经常需要回溯。"第 3 步我看到的价格"、"第 7 步用户名输入框的 index"、"刚才那个登录 form 还没提交"——这些信息一旦从 history 里滑出去，agent 就开始重复做事或者陷入循环。sliding window 适合"短任务、强动作"场景，不适合 browser agent。

第二种是 **RAG-style retrieval**：把每一步的 history 切片向量化存进 vector store，每次 LLM 调用前先 retrieve top-K 相关 chunk。这个方案理论上漂亮，实际工程上很贵：每步都要 embed 一次 DOM 序列化（贵）、要维护一个 vector DB（运维负担）、retrieval 质量对 query 模板敏感（调参痛苦）。browser-use 没采用。

browser-use 选的是第三种：**summarization-based compaction**。`agent/message_manager/service.py` 在 history 长度超过阈值时，把最早的 K 步打包成一段摘要——"第 1-5 步：用户搜索了 'browser-use stars'，进入 GitHub repo 页面，定位到 star count 元素"——然后用这段 200 token 的摘要替换原本 10000 token 的具体内容。最近 N 步保留原样，让 agent 仍然能精确读到刚才发生了什么。s03 我们把这套策略缩减到一个 `Summarize` 接口让你亲手实现过。

为什么不在 mid-loop 压缩？也就是说，为什么不每一步都压缩一下？答案是 **compaction 不是越早越好**。摘要会丢失细节——LLM 在被摘要后无法读到具体的 DOM index 数字、具体的 URL 参数、具体的报错文本。如果在还没用上这些细节之前就压缩，agent 会从"知道但没用上"变成"不知道"。browser-use 的策略是设置一个高阈值（比如 50000 token），只在快撞墙时才压。这本质上是个 lazy strategy：能不忘就不忘。

s03 里我们让你实现了一个 `Summarize` strategy stub，但没接真实 LLM 做摘要——你看到的是接口形状，没看到压缩质量。在生产中，摘要的 LLM 选择是一个独立的工程问题：用便宜的小模型摘要可能掉信息，用大模型摘要又贵又慢。browser-use 在 `agent/service.py` 里允许配 `summary_llm` 单独指定，默认复用主 LLM。这是一个值得记住的工程模式——同一个流水线里的不同子任务，可以用不同的模型。

## CDP 语义与 BackendNodeID 稳定性

LLM 输出 "click index 7"，框架怎么把它翻译成"点击具体的那个 DOM 元素"？这里有个微妙的稳定性问题，是 LLM agent 比传统浏览器自动化更难的地方。

传统自动化（Selenium、Playwright）用 **CSS selector**：`button#submit`、`div.product > a:nth-child(3)`。selector 是 stateless 的——只要 DOM 还在，selector 仍然能定位。代价是 selector 是脆弱的：网站改一行 CSS 类名，你的脚本就全挂了。

CDP 里有两种 node id：**NodeId** 是 JS 层的 `document.getElementById` 的等价物，在同一个 V8 isolate 内稳定，但跨 navigation 失效，跨 frame 不通用。**BackendNodeId** 是 Chromium C++ 层的对象指针 hash，跨 frame 稳定，且只要那个 DOM 节点没被 GC 就一直有效。browser-use 选 BackendNodeId 是有道理的：LLM 决策延迟通常是 2-5 秒，这段时间内页面可能跳转 frame 或者刷新（比如 SPA 路由变化），用 NodeId 会失效，用 selector 又不稳定，BackendNodeId 是最稳的中间层。

但 BackendNodeId 不是万能的。**跨 snapshot 失效的场景**：1. 页面 navigate 到新 URL，整棵 DOM 树被 Chromium 销毁重建——所有 BackendNodeId 失效。2. 一段 JS 调用 `element.remove()`，那个特定节点的 BackendNodeId 失效（即使别的没动）。3. 极端情况下 Chromium 内部做 layout 重建，理论上也会变。browser-use 在 `actor/element.py` 里的 `Element.click()` 失败重试逻辑就是为这种场景写的：先按 BackendNodeId 点；点不到（element not found），重新抓一份 snapshot，按 index 反查 BackendNodeId，再点。

最难的场景是 **LLM 决策延迟 vs DOM mutation 的 race condition**。LLM 在 timestamp T0 看到一份 snapshot 决定点 index 7；T0+3s LLM 返回 "click 7"；T0+3s 的时候页面已经 mutate 了（一个 popup 弹出来盖在 index 7 上面，或者 index 7 的 button disable 了，或者 index 7 的容器 collapse 了）。CDP 派发 click event 给原来的 BackendNodeId 可能：a) 点不到（被盖住）、b) 点到了但无效（disabled）、c) 点到了但触发了意料之外的事件（容器变了）。browser-use 没有完美方案——它在 s12 里加了 action timeout（180s）作为最后的安全网，超时就报错回到 LLM 让它重新看页面。

s09 我们让你实现的 `Cache + Invalidate` 模式正是为了控制这个 race。每次 navigation event 触发，DOMService cache 就失效，agent 下一步会重抓 snapshot。这能减少但不能消除 race——LLM 思考期间发生的 mutation 仍然会导致问题。这是 LLM-as-driver 范式的固有税。

## Watchdog 模式作为解耦工具

读 `browser/session.py` 4000 行你可能会想：为什么 popup 处理、download 拦截、security 检查、CAPTCHA 检测都没在 session 里写，反而是一堆 watchdog 类挂在 EventBus 上？这是个值得展开的设计决策。

最朴素的设计是 **中央调度器**：BrowserSession 内部维护所有副作用逻辑，`session.handle_popup()`、`session.handle_download()`、`session.check_security()`。优点是直观、IDE 跳转方便。缺点是 BrowserSession 会膨胀——browser-use 现在有 13 个 watchdog，如果都塞进 session.py，文件会从 4000 行涨到 12000 行，且每个 watchdog 都要污染 session 的 API 表面。

第二种是 **回调注册**：`session.on_popup(callback)`、`session.on_download(callback)`。比中央调度器解耦一些，但回调之间无法相互订阅（popup watchdog 想监听 download 事件就要绕一圈），且回调注册顺序敏感（先注册的先跑，可能不是想要的优先级）。

browser-use 用的是 **事件总线 + 自动注册**：所有副作用都是独立的类，继承 `BaseWatchdog`，声明 `LISTENS_TO = [PopupOpenedEvent]`，写一个 `async def on_PopupOpenedEvent(self, event)` 方法。watchdog 在 attach 到 session 时通过反射自动注册到 EventBus。任何模块——包括另一个 watchdog——`event_bus.emit(PopupOpenedEvent(...))`，就会触发所有订阅者。s06 你亲手在 Go 里实现过 channel 版的 EventBus + 反射注册。

这种模式的几个 trade-off：
- **优点**：watchdog 之间天然解耦。新增一个 SecurityWatchdog 完全不需要改 session.py。watchdog 之间也能互相 emit 事件——DOMWatchdog 发现"页面 navigate 了"，emit `NavigationEvent`，DownloadsWatchdog 听到这个事件就清理上一次的下载状态。
- **代价 1**：调试链路变长。当一个 event 触发 5 个 handler，每个 handler 又 emit 新 event，调用栈在 EventBus 那里断开了——你看不到"谁调谁"，需要靠 logging 还原顺序。
- **代价 2**：反射注册是隐式契约。`on_PopupOpenedEvent` 命名一旦写错（`on_PopupOpenEvent`），不会报错，只是这个 handler 不会被调到。这是 s06 里我们建议你把"显式注册"作为对比方案保留的原因。

事件驱动天然适合**副作用类**：popup 弹出来、文件下载完了、网络请求失败了、page crash 了——这些都是浏览器**异步告诉你**的事件，不是 agent **主动查询**的状态。把它们写成 watchdog 反映了底层的事件本质。但对于**主流程**——agent 决定下一步动作、执行 CDP 命令、抓 DOM snapshot——这些是同步串行的，写成事件反而过度工程。这就是为什么 browser-use 的主循环（`agent/service.py`）是直接调用 `tools.act(...)`，而不是 `event_bus.emit(ActionRequestedEvent(...))`。

**反例**：什么情况下 watchdog 反而是过度工程？小项目、单进程、副作用少（比如只有 popup 处理一个场景）的情况下，直接在 session 里写一个 `_handle_popup()` 方法比起搭一套 EventBus 容易理解多了。我们 s06 里让你写 channel-based EventBus 是因为"教学价值"——理解事件总线模式是有用的——但如果你只是要做一个小爬虫工具，channel + 回调就够了，不用 watchdog 体系。

## 推荐阅读

- **browser-use README** (`https://github.com/browser-use/browser-use`) —— 项目自己的快速上手与定位说明，看完就理解"为什么是 LLM-as-driver"。
- **Chromium DevTools Protocol** (`https://chromedevtools.github.io/devtools-protocol/`) —— 完整的 CDP domain 列表与 method 参考。读 `DOM`、`DOMSnapshot`、`Input`、`Page`、`Target` 这几个 domain 就能理解 browser-use 大半。
- **cdp-use docs** (`https://pypi.org/project/cdp-use/`) —— browser-use 依赖的 typed CDP wrapper。看它如何把 CDP 的 JSON-RPC 包成 Python type stub，再回头读 `browser/session.py` 就轻松很多。
- **Playwright Selector Engines** (`https://playwright.dev/docs/other-locators#xpath-locator`) —— 对比 LLM-as-UI 视角下"selector 优先"的设计哲学，看完更能体会 LLM-as-driver 的选择背景。
- **bubus** (`https://pypi.org/project/bubus/`) —— browser-use 用的 async event bus 库。50 行 Python 的实现，读完就明白 Python 版 EventBus 长什么样、s06 的 channel 实现是怎么对应过去的。
- **"WebArena: A Realistic Web Environment for Building Autonomous Agents"** (Zhou et al., 2024) —— browser agent 评测基准的代表论文，了解"agent 跑通哪些任务才叫真有用"。
- **"VisualWebArena"** (Koh et al., 2024) —— 进一步把视觉理解加入 web agent 评测，理解 LLM agent 的下一步演进方向（多模态、视觉驱动）。
- **MCP Specification** (`https://spec.modelcontextprotocol.io/`) —— Anthropic 推出的 model context protocol，browser-use 的 `mcp/` 模块即其客户端实现。读完才能理解"agent 暴露成 MCP server"的协议层。

读完这 8 条，加上前面 12 节亲手写的代码，你对 browser-use 这个项目的"为什么"应该已经完整了。下一附录 B 给一份具体的源码导读地图，告诉你 8 个模块按什么顺序读上游 Python 最有效。
