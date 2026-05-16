---
title: "s01 · 最小 agent 循环"
chapter: 1
slug: s01-minimum-loop
est_read_min: 12
---

# s01 · 最小 agent 循环

> 教什么：browser-use 的内核是一个 `loop(观察 → 思考 → 执行)`。本节用 200 行 Go 把"loop 本体"剥出来——不接真的 LLM、不接真的浏览器，让循环的形状自己说话。

---

## Problem / 问题

像 browser-use 这种"会自己用浏览器的 agent"，看上去很神秘：它能读网页、决定点哪个按钮、填表、翻页。然而上游的 `browser_use/agent/service.py` 有 4131 行——直接读会被一堆 Pydantic 模型、async task、watchdog 事件、message 压缩、planner 回路淹没。

但内核出奇地朴素：**一个 for 循环**。每一轮：

- 从上一步的浏览器状态出发（observe）
- 让 LLM 决定下一步动作（think）
- 执行那个动作，把新状态喂回循环（act）

s01 的目标是把这 3 行 mental model 落地成 200 行能跑能测的 Go，把所有非核心的东西（真 LLM、真 CDP、DOM 树、压缩、watchdog）全部砍掉。读者在这一节就能完全看懂 agent 的骨架，后面 11 节都是给这副骨架加肉。

## Solution / 解决方案

把 agent 拆成 3 个角色：

1. **Provider**：负责"思考"——给我一段对话历史，返回下一步要做的动作。s01 用 `FakeProvider`：按 task 里的关键字硬选 action。
2. **Action**：负责"执行"——给我一个 input 字符串，返回执行结果。s01 用 3 个 stub：`SearchAction` / `NavigateAction` / `DoneAction`。
3. **Agent.Run**：负责"循环"——把 Provider 的输出送给对应的 Action，把 Action 的结果塞回 Provider，直到 `end_turn`。

关键决策点：

1. **`StopReason` 三态**：`end_turn`（终止）、`tool_use`（执行 action 后继续）、`max_tokens`（截断报错）。这是真实 LLM API 的协议形状，s02 接 OpenAI 时一比一对得上。
2. **消息历史是 `[]Message`，不是 string**：每个 turn 是 `Message{Role, Content[]}`，content 是 `text` / `tool_use` / `tool_result` 三种 block。这是 Anthropic / OpenAI Chat Completions 都接受的统一格式。
3. **Provider 与 Action 是 interface，循环不知情**：`Agent` 字段是 `Provider Provider; Actions []Action`，循环本体只对接口编程。s02 把 `FakeProvider` 换成 `OpenAIProvider` 时，循环代码一行都不用改。

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

核心 60 行（节选自 `agents/s01-minimum-loop/loop.go`）：

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

**4 个非显然之处**：

1. **assistant 消息一定要进历史，哪怕里面只有 tool_use**：很多入门实现会"等 tool 跑完再合并到一条消息"。但 Anthropic / OpenAI 的协议都要求 assistant 消息单独成 turn，否则下一轮 provider 会拒识 tool_use_id。
2. **tool_result 用 `role: "tool"` 还是 `role: "user"`？** Anthropic 用 `user`，OpenAI 用 `tool`。我们这里选 `tool`，s02 适配真 API 时会做映射；选哪个不影响 loop 逻辑，所以 s01 不纠结。
3. **`MaxSteps` 是 last-resort 保险**：FakeProvider 是确定性的，2 步内必然 end_turn；但当 Provider 是真 LLM 时，可能因为 prompt 写崩、模型抽风导致死循环。MaxSteps 让最坏情况以 "exceeded" 错误退出。
4. **`byName` 在循环开始时一次性建好**：每步重新 build map 是常见性能 bug。注册一次、查找 O(1)，是 s04 把 Registry 抽象成显式组件的"前因"。

## What Changed / 与上一节的变化

s01 是第一节，没有"上一节"。这里展示**与"传统的同步函数"对比**，凸显 loop 形态：

```diff
- // 传统：一次性的 task runner
- func DoTask(task string) string {
-     return process(task)  // 一次输入 → 一次输出
- }

+ // browser-agent：循环的状态机
+ func (a *Agent) Run(ctx context.Context, task string) (string, error) {
+     msgs := []Message{{Role: "user", ...}}
+     for step := 0; step < a.MaxSteps; step++ {
+         resp, _ := a.Provider.Invoke(ctx, msgs)
+         // ... 把动作执行结果再喂回去
+     }
+ }
```

差别不只是"加了 for"，而是**控制反转**：传统函数自己决定要做什么；agent 函数把"做什么"交给 Provider，自己只做"调度 + 执行 + 喂回去"。

## Try It / 动手试一试

```bash
cd agents/s01-minimum-loop

# 基础：触发 search → 看到 stub 结果 → end_turn
go run . "search hacker news"

# verbose 模式：每一步的内部状态都打出来
go run . -v "search hacker news"

# 触发 navigate
go run . -v "navigate https://example.com"

# 测试
go test -v ./...
```

期望输出形态：

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

由于 FakeProvider 是确定性的，所有输出**逐字节可复现**。这是测试友好的关键。

## Upstream Source Reading / 上游源码阅读

上游 `browser_use/agent/service.py` 第 1023-1142 行是 `Agent.step()` 方法——一次循环迭代的真实实现。比我们的 60 行多了 80 多行，加的全是**生产级关切**：captcha 等待、screenshot、watchdog 事件、message 压缩、planner 回路。

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
            # Phase 0: captcha 检查（真 agent 要处理人机验证，我们 mini 不做）
            try:
                captcha_wait = await self.browser_session.wait_if_captcha_solving()
                if captcha_wait and captcha_wait.waited:
                    # ...inject captcha outcome into LLM context
                    captcha_result = ActionResult(long_term_memory=msg)
                    ...
            except Exception as e:
                self.logger.warning(f'Phase 0 captcha wait failed (non-fatal): {e}')

        # Phase 1: 准备上下文（截屏 + DOM 快照 + 消息构造），对应我们的 latestUserText
        browser_state_summary = await self._prepare_context(step_info)
        self.state.last_model_output = None
        self.state.last_result = None

        # Phase 2: 调 LLM + 执行 actions（对应我们的 provider.Invoke + runTools）
        await self._get_next_action(browser_state_summary)
        await self._execute_actions()

        # Phase 3: post-process（更新历史、telemetry、cost 追踪）
        await self._post_process()

    except Exception as e:
        # 全部异常归一到一个 handler；我们的 mini 直接 return err
        await self._handle_step_error(e)
    finally:
        await self._finalize(browser_state_summary)
```

**对照阅读要点**：

- **Phase 0 → Phase 3 的分段**：上游把一次 step 显式切成 4 个 phase，便于在 Phase 1 失败时跳过 Phase 2 / 3。我们的 60 行把这 4 个 phase 压缩在一个 switch 里，靠 `StopReason` 早出。
- **截屏 + DOM 快照**：上游 `_prepare_context` 调 `self.browser_session.get_browser_state_summary(include_screenshot=True)`——这步在 s07-s09 才会出现，我们 s01 不做任何浏览器观察。
- **消息压缩**：`await self._maybe_compact_messages(step_info)` 是 s03 的内容。s01 的 `[]Message` 会无限增长，但因为 MaxSteps 限制不会爆炸。
- **planner 回路**：`plan_description = self._render_plan_description()` 注入 planner 输出——s12 的"每 5 步规划一次"就是这个机制的下放。
- **异常归一**：上游用一个 `_handle_step_error` 兜底；我们 mini 直接 `return err`。是简化也是教学清晰度选择。
- **故意保留**：我们 mini 的 `MaxSteps` 安全网在上游叫 `agent.settings.max_actions_per_step` 与 `max_steps`，分两个层级各自管"单步最多几个动作"与"总步数"。我们合并成一个。

**想读更多**：从 `browser_use/agent/service.py` 的 `Agent.step` 入手，跟着 `_get_next_action` 进 `browser_use/llm/base.py`，再读 `_execute_actions` 跳进 `browser_use/tools/service.py`。这条线就是 s01 → s02 → s04 → s12 的真实代码地图。

---

**下一节预告**：s02 把 `FakeProvider` 换成真的 LLM——`OpenAIProvider`，纯 `net/http`，不依赖任何 SDK。同时定义稳定的 `Provider` 接口形状，为后面的多 model 适配（Phase G addendum）打下接口基础。
