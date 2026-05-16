---
title: "s12 · 完整 agent loop"
chapter: 12
slug: s12-agent-loop-full
est_read_min: 18
---

# s12 · 完整 agent loop

> 教学目标：前面 11 章造出了 Provider、MessageManager、Registry、BrowserSession、EventBus、DOMService、TokenCost、FileSystem，但它们从未在一个程序里互相对话。s12 把它们全部接进一个 `Agent` struct，每 N 步触发 planner，主 LLM 超时切到 fallback，跑通对 `httptest.Server` 的端到端循环。生产代码约 1,200 行 Go（不含测试）；核心 `Agent.Run` 不到 100 行。

---

## 问题 / Problem

s11 结束后，手上躺着这堆零件：

- `Provider` (s02) —— 一个接口，两份实现，从未被任何循环调用。
- `MessageManager` 带 `KeepLastN` (s03) —— 拥有 history，但没有生产代码查询它。
- `Registry + Dispatcher` (s04) —— 注册了 5 个 action，没有 agent 调用 `Dispatcher.Act`。
- `BrowserSession + EventBus` (s07) —— Start、Stop、watchdog 都接上了，但没有任务在它上面跑。
- `DOMService` (s09) —— `Get()` 返回的 serialized DOM 没人读。
- `TokenCost` (s10) —— 等待调用方调用 `RegisterInvocation` 的账本。
- `LocalFileSystem` (s11) —— 没有 tool 往里写的沙箱。

具体痛点：

1. **"agent" 这个东西还不存在**。每章结束于一个单测过的 struct。它们互不引用，也没有一个 `Run(task)` 方法能接收文字、返回文字。集成后的形状还是猜想。
2. **两个我们还没写的策略**。上游 `browser_use/agent/service.py` 做了我们 11 章都不做的两件事：
   - 每 N 步调一次 *planner*，注入"接下来 3 步"的反思。
   - 主 LLM 超时时，切换到 fallback LLM。
3. **集成可能会泄露依赖**。如果集成代码不小心要求真实网络、真实 LLM、真实 Chromium，这一章就在笔记本上跑不起来了。需要一个用 `httptest.Server`、`MockProvider`、`RecordingCDPClient` 跑通的端到端 demo —— 同时还能证明接线是真的。

s12 把三个都解决。Go 的解法不出意外：一个 `Agent` struct，所有零件作为字段挂在上面；一个 `Run(ctx, task)` 方法装下循环主体；两个新 helper（`planner.go`、`invokeWithFallback`）实现新策略。

## 解决方案 / Solution

`Agent` struct：

```go
type Agent struct {
    Provider   Provider          // 主 LLM (s02)
    Fallback   Provider          // 超时时切换的备用 LLM
    Tools      *Registry         // 注册了 5 个 tool (s04)
    Session    *BrowserSession   // stub CDP + bus + watchdogs (s07)
    DOM        *DOMService       // 页面快照 (s09)
    Messages   *MessageManager   // 历史 + KeepLastN (s03)
    Cost       *TokenCost        // 账本 (s10)
    FS         FileSystem        // 沙箱 (s11)
    MaxSteps   int
    PlanEvery  int               // 0 = 关闭
    LLMTimeout time.Duration
    Verbose    io.Writer
}
```

`Agent.Run` 循环主体每步 7 个阶段：

| 阶段 | 做什么 | 来自哪章 |
|---|---|---|
| 1 | 可能调用 planner | s12（新增） |
| 2 | DOM 快照 | s09 |
| 3 | 调用 Provider（带 fallback） | s02 + s12（新增 fallback） |
| 4 | 记录 cost | s10 |
| 5 | 追加 assistant 消息 | s03 |
| 6 | 根据 StopReason 分支 | s01 基础 |
| 7 | 派发 tool | s04 |

脚本化的 `MockProvider` 队列驱动 demo 从头到尾：

```
type[index=0, text="browser-use"]  → 把 query 输入到搜索框
search[query="browser-use"]        → 跳到搜索结果页
click[index=0]                     → 打开第一个文章
done[answer="First article on browser-use"]  → 循环返回
```

在 s01 基础上叠两个新策略：

- **Planner**（`planner.go`）：每 `PlanEvery` 步（零 = 关闭），调一次 `Plan(ctx, Provider, history)`，把返回结果当作 system 消息注入。下一轮普通 turn 能看到它。
- **Fallback**（`agent.go` 里的 `invokeWithFallback`）：用 `context.WithTimeout(LLMTimeout)` 包住 `Provider.Invoke`；遇到 `DeadlineExceeded`，用一个 **新的** `context.WithTimeout` 调 `Fallback`。

## 工作原理 / How It Works

完整架构图 —— 前面每章的贡献都接进 Agent struct：

```
┌──────────────────────────────────────────────────────────────────────┐
│                            Agent.Run(ctx, task)                       │
│                                                                       │
│   ┌─ 阶段 1: planner ───────────────────────────────┐                 │
│   │  step % PlanEvery == 0 ?                        │ ← s12 新增      │
│   │  └─ 是 → Plan(ctx, Provider, msgs)              │                 │
│   │         → 当作 system 消息注入                  │                 │
│   └─────────────────────────────────────────────────┘                 │
│                                                                       │
│   ┌─ 阶段 2: 观察 ──────────────────────────────────┐                 │
│   │  dom := DOMService.Get(ctx)         (s09)       │                 │
│   │  Messages.Add(user, "[browser_state]\n" + dom)  (s03)             │
│   └─────────────────────────────────────────────────┘                 │
│                                                                       │
│   ┌─ 阶段 3: invoke（带 fallback）──────────────────┐                 │
│   │  ctx1 = WithTimeout(ctx, LLMTimeout)            │                 │
│   │  resp, err := Provider.Invoke(ctx1, msgs)       │ ← s02           │
│   │  if DeadlineExceeded && Fallback != nil:        │ ← s12 新增      │
│   │    ctx2 = WithTimeout(ctx, LLMTimeout)          │                 │
│   │    resp, err = Fallback.Invoke(ctx2, msgs)      │                 │
│   └─────────────────────────────────────────────────┘                 │
│                                                                       │
│   ┌─ 阶段 4: 记账 ──────────────────────────────────┐                 │
│   │  Cost.RegisterInvocation(model, in, out)    (s10)                 │
│   └─────────────────────────────────────────────────┘                 │
│                                                                       │
│   ┌─ 阶段 5: 追加 assistant ────────────────────────┐                 │
│   │  Messages.Add(assistant, text + tool_use[])  (s03)                │
│   └─────────────────────────────────────────────────┘                 │
│                                                                       │
│   ┌─ 阶段 6: 根据 StopReason 分支 ──────────────────┐                 │
│   │  end_turn  → return text, nil                   │                 │
│   │  tool_use  → 进入阶段 7                         │                 │
│   │  max_tok   → 返回错误                           │                 │
│   └─────────────────────────────────────────────────┘                 │
│                                                                       │
│   ┌─ 阶段 7: 派发 tool ─────────────────────────────┐                 │
│   │  for each action:                               │                 │
│   │    block, _ := Dispatcher.Act(ctx, action) (s04)│                 │
│   │    if block.Result 以 __done__: 开头:           │                 │
│   │      finalAnswer = 后缀                         │                 │
│   │  Messages.Add(tool, results)                    │                 │
│   │  if finalAnswer != "": return finalAnswer, nil  │                 │
│   └─────────────────────────────────────────────────┘                 │
└──────────────────────────────────────────────────────────────────────┘
        ▲                              ▲                       ▲
        │                              │                       │
   BrowserSession + EventBus      RecordingCDPClient      LocalFileSystem
   (s07) + NavigationWatchdog        (s05/s07)               (s11)
```

核心代码，~80 行来自 `agent.go`：

```go
func (a *Agent) Run(ctx context.Context, task string) (string, error) {
    a.applyDefaults()
    if err := a.validate(); err != nil { return "", err }

    a.Messages.Add(Message{Role: "system",
        Content: []ContentBlock{{Type: "text", Text: systemPromptText}}})
    a.Messages.Add(Message{Role: "user",
        Content: []ContentBlock{{Type: "text", Text: "Task: " + task}}})

    for step := 0; step < a.MaxSteps; step++ {
        // 阶段 1: planner.
        if a.PlanEvery > 0 && step > 0 && step%a.PlanEvery == 0 {
            plan, err := Plan(ctx, a.Provider, a.Messages.Get())
            if err == nil {
                a.Messages.Add(Message{Role: "system", Content: []ContentBlock{
                    {Type: "text", Text: "[plan @ step " + itoa(step) + "] " + plan},
                }})
                a.logf("[step %d] planner: %s\n", step, truncate(plan, 80))
            }
        }
        // 阶段 2: 观察.
        dom, err := a.DOM.Get(ctx)
        if err != nil { return "", fmt.Errorf("step %d: dom: %w", step, err) }
        a.Messages.Add(Message{Role: "user", Content: []ContentBlock{
            {Type: "text", Text: "[browser_state]\nURL: " + a.DOM.CurrentURL() + "\n" + dom.LLMText},
        }})

        // 阶段 3: invoke (带 fallback).
        resp, err := a.invokeWithFallback(ctx, step)
        if err != nil { return "", err }

        // 阶段 4: 记账.
        a.Cost.RegisterInvocation(resp.Model, resp.InputTokens, resp.OutputTokens)

        // 阶段 5: 追加 assistant.
        assistantBlocks := []ContentBlock{{Type: "text", Text: resp.Text}}
        for _, ac := range resp.Actions {
            assistantBlocks = append(assistantBlocks, ContentBlock{
                Type: "tool_use", Name: ac.Name, Input: ac.Input,
            })
        }
        a.Messages.Add(Message{Role: "assistant", Content: assistantBlocks})

        // 阶段 6: 根据 StopReason 分支.
        switch resp.StopReason {
        case "end_turn":
            return resp.Text, nil
        case "max_tokens":
            return "", fmt.Errorf("step %d: provider truncated response", step)
        case "tool_use":
            // 落到派发分支
        default:
            return "", fmt.Errorf("step %d: unknown stop_reason %q", step, resp.StopReason)
        }

        // 阶段 7: 派发.
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

**5 个不显然的点**：

1. **Planning 通过 `PlanEvery` 选择是否开启**。设成 0 就完全关闭 planner；循环退化成普通的"观察—思考—行动"，没有反思。上游对应的是 `settings.planner_interval`。为什么要做成 opt-in？因为每次 planner 调用增加约 1k token 的开销，对每步决策的提升通常不明显；只有长程任务才看得到收益。测试可以把 `PlanEvery` 留成 0，验证其他东西不用拉 planner mock 进队列。

2. **Fallback 创建新的 context，不是原地重试**。看 `invokeWithFallback`：主 LLM 出错后，我们 **再** `context.WithTimeout(parent, LLMTimeout)`。如果复用主 LLM 那个（已经超时）的 ctx，fallback 永远没机会跑。同一个 parent ctx 还是作为 **根**，全局取消依然有效 —— 只是每次尝试的 deadline 是新鲜的。

3. **Cost 跟踪在循环主体之外**。`RegisterInvocation` 在每次成功 invoke 之后调一次，不在 MessageManager 或 Provider 里。这种解耦很重要：真实的 metrics exporter（prometheus、OpenTelemetry）在同一个接缝插入，不用动循环。这也意味着主 LLM 超时（没有产生 Response）正确地不会消耗 cost 预算。

4. **沙箱路径 per-Agent，不是全局**。`agent.FS = NewLocalFileSystem("./sandbox-task-N")` 让多租户部署能隔离每个 Agent 的文件系统状态。struct 上单个 `FS FileSystem` 字段就够了 —— agent 不试图给每轮管理子目录；filesystem 实现拥有 layout。s12 没有内置 tool 调用 `FS`（demo 不写文件），但 `TestFilesystemSandboxRejectsAbsolutePath` 证明这个接缝是真实存在的。

5. **`done()` 通过 tool_result 的魔法前缀退出**。看 `tools.go` 里的 `DoneResultPrefix = "__done__:"`。当 `Dispatcher.Act` 返回的 tool_result body 以这个前缀开头，`Agent.Run` 抽出后缀作为最终答案返回。为什么不做一个带 `IsDone() bool` 的 Action 接口？因为那样每个 tool 都得实现这个标记，哪怕它永远不会返回 true。字符串前缀哨兵让所有 `Tool.Run` 都保持 `(string, error)`；agent 只做一次 `strings.HasPrefix` 检查。

## 与上一节的变化 / What Changed

整个课程里最大的一次 diff。可视化：`Agent` struct 字段数从 s01 到 s12 的演进：

```
s01  Agent{ Provider, Actions, MaxSteps, Verbose }                                 (4 字段)
s02  Agent{ Provider(真 LLM), Tools, MaxSteps, Verbose }                           (4 字段，类型成熟)
s03  Agent{ Provider, Tools, Messages*, MaxSteps, Verbose }                        (+1: history struct)
s04  Agent{ Provider, Tools(Registry), Messages, MaxSteps, Verbose }               (Tools 变成 *Registry)
s05  Agent{ Provider, Tools, Messages, Session(CDP+Actor), MaxSteps, Verbose }     (+1: browser session)
s06  Agent{ Provider, Tools, Messages, Session(Bus+Watchdogs), MaxSteps, Verbose } (Session 长出 bus)
s07  Agent{ Provider, Tools, Messages, Session(lifecycle), MaxSteps, Verbose }     (Session 长出 Start/Stop)
s08  （Agent 无改变 —— DOM serializer 是 tool 的内部)
s09  Agent{ ..., DOM *DOMService, ... }                                            (+1: DOM)
s10  Agent{ ..., DOM, Cost *TokenCost, ... }                                       (+1: cost)
s11  （Agent 无改变 —— FS 是独立 struct，等着被接进来)
s12  Agent{ Provider, Fallback, Tools, Session, DOM, Messages, Cost, FS,
            MaxSteps, PlanEvery, LLMTimeout, Verbose }                             (12 字段)
```

s12 这一跳新增了前面 11 章都没有的三样东西：

```diff
+ Fallback   Provider          // s12 新增
+ FS         FileSystem        // s11 备好，但之前从未赋值
+ PlanEvery  int               // s12 新增
+ LLMTimeout time.Duration     // s12 新增 (s04 的 Dispatcher 里只是常量)
```

循环主体的 Go diff，对比 s11（假设有 step driver）vs s12 的 `Agent.Run`：

```diff
- // s11 没有 agent —— `FS` 独立存在
- for _, op := range scenarioOps { fs.WriteFile(ctx, op.path, op.content) }
+ for step := 0; step < a.MaxSteps; step++ {
+     if a.PlanEvery > 0 && step > 0 && step%a.PlanEvery == 0 {
+         plan, _ := Plan(ctx, a.Provider, a.Messages.Get())
+         a.Messages.Add(/* 注入 plan */)
+     }
+     dom, _ := a.DOM.Get(ctx)
+     a.Messages.Add(/* browser_state */)
+     resp, err := a.invokeWithFallback(ctx, step)
+     a.Cost.RegisterInvocation(resp.Model, resp.InputTokens, resp.OutputTokens)
+     /* 阶段 5..7 */
+ }
```

这是 "我们有真正的 agent 了" 这件事变成现实的章节。

## 动手试一试 / Try It

```bash
cd agents/s12-agent-loop-full

# 端到端 demo：脚本化 4 步对 httptest.Server
GOWORK=off go run .

# 全部 7 个测试（5 个必需 + 2 个加成）
GOWORK=off go test -v ./...
```

`GOWORK=off` 是因为根 `go.work` 还没把 s12 加进来；模块本身自足。

期望的 demo 输出（截断）：

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

测试覆盖：

- `TestFullE2EAgainstStub` —— 脚本化 4-turn 跑通：type → search → click → done。断言最终答案、CDP 录制器内容、cost 账本行数。
- `TestFallbackOnTimeout` —— 主 LLM 的 `Delay: 500ms`，LLMTimeout=200ms。fallback 的 done() 答案胜出；主 LLM 调用 1 次，fallback 调用 1 次。
- `TestPlanningEvery5Steps` —— `PlanEvery=5, MaxSteps=12`。Verbose log 包含 `[step 5] planner:` 和 `[step 10] planner:`。运行以 MaxSteps 结束（脚本里没有 done()）。
- `TestMaxStepsTermination` —— Provider 永远返回 scroll，MaxSteps=3。Run 返回 "MaxSteps=3 exceeded" 错误。
- `TestDoneExitsCleanly` —— 一次 done() turn。Run 返回答案；provider.CallCount() == 1。
- `TestKeepLastNCompaction`（加成）—— MessageManager max=8，Add 25 次 → Get() 恰好返回 8。
- `TestFilesystemSandboxRejectsAbsolutePath`（加成）—— `fs.WriteFile("/etc/passwd", ...)` 报错；`fs.WriteFile("note.md", ...)` 成功。

## 上游源码阅读 / Upstream Source Reading

上游的 `Agent.step` 是整个控制流穿过的唯一方法 —— `browser_use/agent/service.py` 里大约 220 行。这里能看到的七个阶段（captcha-wait、prepare-context、get-next-action、execute-actions、post-process、error-handle、finalize）和我们的 7 阶段（planner、observe、invoke、ledger、append、switch、dispatch）几乎一一对应。压缩比是真的，但 *形状* 没变。

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

**6 条阅读笔记**：

1. **上游的 `step()` 是个 220 行的方法，每个 Phase 都被 `try/except` 包住**。我们的 Go 版本通过 `return "", err` 从每个阶段返回错误。控制流一样，惯用法不同。Go 版本更短，是因为我们不背 Python 异常模型鼓励的那种 per-phase try-wrapper。

2. **`_prepare_context` 是上游真正构建 user 消息的地方** —— 包括 screenshot、serialized DOM，外加十几个 "提示注入"（`_inject_budget_warning`、`_inject_replan_nudge`、`_inject_exploration_nudge`、`_inject_loop_detection_nudge`、`_force_done_after_last_step`、`_force_done_after_failure`）。我们把这压缩成一次 `Messages.Add(user, browser_state)`。这些 nudge 是值得了解的生产技巧，但对教学要点不必要。

3. **`_maybe_compact_messages` 是 compaction 的调用点**。上游在 prepare-context 阶段里，恰好在 `create_state_messages` 之前，懒惰地做 compact。我们的 `MessageManager.Get()` 走同样路径 —— 懒惰地在 Get 里做，绝不在 Add 里做。形状一致；我们只是不带 LLM 驱动的 summariser（`browser_use.agent.message_manager.maybe_compact_messages`），因为 `KeepLastN` 让测试更确定。

4. **超时调用是 `asyncio.wait_for(..., timeout=self.settings.llm_timeout)`**。Python 的 `wait_for` 超时时取消底层协程 —— asyncio scheduler 回收时间片。我们的 Go 版本用 `context.WithTimeout` + `errors.Is(err, context.DeadlineExceeded)` 检查。语义一致：约束每次调用的开销。

5. **上游没有 fallback Provider**。上游从 "主 LLM 超时" 直接 raise。生产部署用自己的 retry middleware 包一层。我们的 s12 反过来：在代码里直接放一个 Fallback 字段，让 demo 把策略展示出来，而不是藏在 Retry 库后面。教学收益是 "fallback 是个概念，不是某个魔法插件"。

6. **`_handle_step_error` 是上游唯一的错误处理点**。它根据异常类型分支（InterruptedError、KeyboardInterrupt、自定义领域异常）。我们的 Go 版本直接返回错误；调用方（main.go）决定怎么对外呈现。两种形状都站得住脚；Python 惯例集中处理 recovery，Go 惯例集中处理 dispatch。

11 章 Go 能一对一镜像 Python 的 `step()` 方法 —— 这是课程分解 *确实* 对得上上游项目实际代码结构的最强证据。集成不是 "我们粘了一堆碰巧能跑的东西" —— 而是 "LLM 驱动循环有一个标准形状，我们一块一块把它还原出来了"。
