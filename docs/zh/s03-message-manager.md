---
title: "s03 · 消息管理与压缩"
chapter: 3
slug: s03-message-manager
est_read_min: 13
---

# s03 · 消息管理与压缩

> 教什么：browser-use 跑 30 步以后，对话历史会涨到 ~100k tokens，再不压缩就要在 LLM 那边爆掉。本节把 s01/s02 的裸 `[]Message` 升级成 `MessageManager`：一个**所有权清晰、策略可换、脱敏内建**的结构。

---

## Problem / 问题

s01 / s02 的 `Agent.Run` 里只有这么一行：

```go
msgs := []Message{{Role: "user", ...}}
// ... loop appends to msgs forever ...
```

只要循环一直跑，`msgs` 就一直涨。15 步以后每步几 KB 的浏览器观察文本加在一起，30 步就轻松突破 100k tokens，要么撞上 context window，要么账单爆炸。

**还有一个隐患**：浏览器观察文本里随时可能混进**密钥**——日志贴出来的 Bearer token、配置里的 `sk-...` API key、用户的 email。这些东西如果直接进了对话历史，下一轮调 LLM 时就连同请求体一起发出去了。

s03 要解决这两件事：

1. **历史无限增长** → 引入压缩策略
2. **敏感数据泄露** → 引入脱敏过滤

但更重要的是**架构动作**：把 `[]Message` 提升成 `MessageManager` 结构，让"加什么策略"成为一个可替换的字段而不是 loop 里硬编码的 if-else。

## Solution / 解决方案

引入 `MessageManager` 类型，三件套：

```go
type MessageManager struct {
    History     []Message            // 原始历史，永远不删
    MaxMessages int                  // 软上限
    TokenBudget int                  // 预留字段（s10 真的用）
    Strategy    Strategy             // 压缩策略接口
    Sanitizer   func(string) string  // 脱敏函数
}
```

三个方法：`Add(m)` / `Get() []Message` / `Len()`。

**两条策略是分层的，应用时机不同**：

| 层 | 时机 | 为什么 |
|---|---|---|
| 脱敏 | `Add()`，**立即** | 内存里也不能存明文密钥——debugger dump 都会漏 |
| 压缩 | `Get()`，**惰性** | 原始历史要留作调试；压缩可能要调 LLM，按需触发更省 |

**两种压缩策略**，都实现 `Strategy` 接口：

1. **`KeepLastN`** — 保留第 0 条 + 最近 N-1 条，其他丢掉。最简单的"丢掉旧消息"。
2. **`Summarize`** — 保留第 0 条 + 一条**合成的 system summary**（`[compacted: 12 turns covering search, click actions]`）+ 最近 N-2 条。教学版的合成 summary 用 tool 名字直方图（确定性，方便测试）；上游真的调 LLM 做摘要。

为什么第 0 条永远保留？因为它一般是 user task / system prompt——丢掉它，agent 会"忘了自己在干什么"。上游也这么处理（`service.py#L289-L295`）。

## How It Works / 工作原理

```
┌──────────────────────────────────────────────────────────────────┐
│ MessageManager                                                   │
│                                                                  │
│   Add(m) ─► [sanitiser] ─► append(History, m)                    │
│                                                                  │
│   Get()  ─► len(History) > MaxMessages?                          │
│              │                                                   │
│              ├─ no  ─► return copy(History)                      │
│              │                                                   │
│              └─ yes ─► Strategy.Apply(History, MaxMessages)      │
│                          │                                       │
│           ┌──────────────┼──────────────┐                        │
│           ▼                             ▼                        │
│      ┌──────────┐                ┌────────────┐                  │
│      │ KeepLastN│                │ Summarize  │                  │
│      └──────────┘                └────────────┘                  │
│      [H[0], H[-N+1:]]            [H[0], synth, H[-(N-2):]]       │
└──────────────────────────────────────────────────────────────────┘
```

核心 ~50 行（节选自 `agents/s03-message-manager/`）：

```go
// message_manager.go
func (m *MessageManager) Add(msg Message) {
    if m.Sanitizer != nil && len(msg.Content) > 0 {
        clone := make([]ContentBlock, len(msg.Content))
        copy(clone, msg.Content)
        for i := range clone {
            clone[i].Text   = m.Sanitizer(clone[i].Text)
            clone[i].Result = m.Sanitizer(clone[i].Result)
            clone[i].Input  = m.Sanitizer(clone[i].Input)
        }
        msg.Content = clone
    }
    m.History = append(m.History, msg)
}

func (m *MessageManager) Get() []Message {
    if m.Strategy == nil || m.MaxMessages <= 0 || len(m.History) <= m.MaxMessages {
        out := make([]Message, len(m.History))
        copy(out, m.History)
        return out
    }
    return m.Strategy.Apply(m.History, m.MaxMessages)
}

// compaction.go (Summarize)
func (Summarize) Apply(history []Message, maxMessages int) []Message {
    tailKeep := maxMessages - 2
    tailStart := len(history) - tailKeep
    if tailStart < 1 { tailStart = 1 }
    dropped := history[1:tailStart]
    summary := summariseTurns(dropped)  // tool 名字直方图

    out := make([]Message, 0, maxMessages)
    out = append(out, history[0])                              // 钉死任务
    out = append(out, Message{Role: "system", Content:
        []ContentBlock{{Type: "text", Text: summary}}})        // 合成 summary
    if tailKeep > 0 {
        out = append(out, history[tailStart:]...)              // 最近 N-2 条
    }
    return out
}

// redact.go
var patterns = []*regexp.Regexp{
    regexp.MustCompile(`sk-[A-Za-z0-9_\-]{20,}`),
    regexp.MustCompile(`Bearer\s+[A-Za-z0-9._\-]{16,}`),
    regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`),
}

func RedactSensitive(content string) string {
    for _, p := range patterns {
        content = p.ReplaceAllString(content, "[REDACTED]")
    }
    return content
}
```

**4 个非显然之处**：

1. **`Get()` 返回 copy 而不是 slice 别名**：调用方拿到的切片如果共享底层数组，他改一格我也跟着改——这是 Go 切片最常踩的坑。每次 Get() 多一次 alloc，换换调用方"想改就改"的安全。
2. **为什么 user task 永远是 `History[0]` 而不会被合并进 summary**：合成 summary 本质是"复述"，复述会引入失真。任务原文如果失真，agent 就开始偏离用户意图。上游也走这条路（`service.py#L289`）。
3. **为什么压缩在 `Get()` 而不是 `Add()`**：压缩在 Add 触发会**截断一个 tool_use/tool_result 对**——assistant 说"我要用 search 工具"，下一条 tool 消息回 result。如果中间发生压缩把 tool_use 干掉、tool_result 留下，下一轮 provider 会因为找不到 tool_use_id 直接报错。延迟到 Get() 就避开了这个 race。
4. **为什么 redaction 只用正则**：本节是 stdlib only 的教学版，三条正则覆盖了 90% 的"明显泄露"（OpenAI/Anthropic key、Bearer token、email）。生产版本（上游 `redact_sensitive_string`）是另一条路：知道**自己有哪些密钥**，做精确字符串替换。两条路在真实系统里**并存**，互补。

## What Changed / 与上一节的变化

```diff
- // s02 的 Agent: history 是裸数组
- type Agent struct {
-     Provider Provider
-     Actions  []Action
-     MaxSteps int
- }
- // loop 里直接：
-   msgs := []Message{...}
-   msgs = append(msgs, ...)

+ // s03 引入 MessageManager
+ type MessageManager struct {
+     History     []Message
+     MaxMessages int
+     Strategy    Strategy
+     Sanitizer   func(string) string
+ }
+ // Agent (将来 s04+) 持有一个 *MessageManager
+ // loop 里：
+   mm.Add(msg)              // 自带脱敏
+   msgs := mm.Get()         // 自带压缩
```

差别不只是"加了结构体"，而是**职责切分**：

- **s02**：循环本体负责"组装消息历史"。
- **s03**：循环本体只负责"调度"，"历史长什么样" 由 `MessageManager` + 策略决定。

为什么这次切分要在 s03 而不是 s01？因为**只有等需要变化的地方真的出现了，抽象才有意义**。s01 是裸 loop，s02 加 OpenAI，s03 这一节才第一次需要"策略可替换"——KeepLastN vs Summarize 就是第一个具体的变化点。

## Try It / 动手试一试

```bash
cd agents/s03-message-manager

# 跑 demo：20 条假消息，压缩到 5 条，看 redaction 生效
GOWORK=off go run .

# 跑测试
GOWORK=off go test -v ./...
```

期望输出形态：

```
=== before compaction ===
MessageManager{raw=20, max=5, budget=4000}
raw history length: 20

=== after Get() (Summarize strategy, max=5) ===
effective length:   5
[0] user      Find the trending repos on hacker news and email summary to [REDACTED]
[1] system    [compacted: 16 turns covering search, type, click actions]
[2] assistant Step 17: invoking click.
[3] tool      leaked authorization: [REDACTED]
[4] assistant Step 19: invoking search.

=== redaction in action ===
redacted sample: "leaked authorization: [REDACTED]"
clean sample:    "Step 19: invoking search."
```

由于 Sanitizer 和 Summarize 都是纯函数，整段输出**逐字节可复现**。`testdata/expected.txt` 就是这份输出的快照。

## Upstream Source Reading / 上游源码阅读

上游 `browser_use/agent/message_manager/service.py` 第 104-300 行是 `MessageManager` 类。教学版抓住"压缩 + 脱敏 + History 所有权"三件事；上游在这三件事之上还包了：真实的 LLM 摘要调用、按域名作用域的 sensitive_data 字典、file_system 快照、screenshot 一次性插入。

```python
# Source: browser_use/agent/message_manager/service.py#L104-L300
# License: MIT
class MessageManager:
    def __init__(
        self,
        task: str,
        system_message: SystemMessage,
        file_system: FileSystem,
        state: MessageManagerState = MessageManagerState(),
        use_thinking: bool = True,
        include_attributes: list[str] | None = None,
        sensitive_data: dict[str, str | dict[str, str]] | None = None,
        max_history_items: int | None = None,
        ...
    ):
        self.task = task
        self.state = state                       # 我们的 self.History
        self.system_prompt = system_message      # 钉死的第一条
        self.file_system = file_system           # s11 才出现
        self.sensitive_data = sensitive_data     # 比我们的 Sanitizer 更精细
        self.max_history_items = max_history_items   # 我们的 MaxMessages
        ...
        assert max_history_items is None or max_history_items > 5, \
            'max_history_items must be None or greater than 5'
        # ^ 上游硬要求 >5：因为压缩布局至少需要 first + summary + 3 个尾部消息

    @property
    def agent_history_description(self) -> str:
        """Build agent history description from list of items,
           respecting max_history_items limit"""
        # 这一段就是我们 Summarize.Apply 的灵魂:
        # - 永远保留 history[0] (我们也一样)
        # - 中间 omitted_count 条用一句 <sys>[... N previous steps omitted ...]</sys> 替代
        # - 后面接最近 max_history_items - 1 条
        compacted_prefix = ''
        if self.state.compacted_memory:
            compacted_prefix = (
                '<compacted_memory>\n'
                f'{self.state.compacted_memory}\n'
                '</compacted_memory>\n'
            )

        if self.max_history_items is None:
            return compacted_prefix + '\n'.join(item.to_string()
                                                for item in self.state.agent_history_items)

        total_items = len(self.state.agent_history_items)
        if total_items <= self.max_history_items:
            return compacted_prefix + '\n'.join(item.to_string()
                                                for item in self.state.agent_history_items)

        omitted_count = total_items - self.max_history_items
        recent_items_count = self.max_history_items - 1
        items_to_include = [
            self.state.agent_history_items[0].to_string(),
            f'<sys>[... {omitted_count} previous steps omitted...]</sys>',
        ]
        items_to_include.extend([item.to_string()
                                 for item in self.state.agent_history_items[-recent_items_count:]])
        return compacted_prefix + '\n'.join(items_to_include)

    async def maybe_compact_messages(
        self,
        llm: BaseChatModel | None,
        settings: MessageCompactionSettings | None,
        step_info: AgentStepInfo | None = None,
    ) -> bool:
        """Summarize older history into a compact memory block.
           Step interval is the primary trigger; char count is a minimum floor."""
        if not settings or not settings.enabled: return False
        if llm is None or step_info is None: return False

        # 双门限：步数 + 字符数
        steps_since = step_info.step_number - (self.state.last_compaction_step or 0)
        if steps_since < settings.compact_every_n_steps: return False

        history_items = self.state.agent_history_items
        full_history_text = '\n'.join(item.to_string() for item in history_items).strip()
        if len(full_history_text) < (settings.trigger_char_count or 40000): return False

        # 真的调 LLM 做摘要 (我们的 summariseTurns 是 deterministic 直方图)
        messages = [SystemMessage(content=system_prompt), UserMessage(content=compaction_input)]
        response = await llm.ainvoke(messages)
        summary = (response.completion or '').strip()
        ...
        self.state.compacted_memory = summary
        # 保留 first + 最近 keep_last 条（同我们的 Summarize 布局）
```

**对照阅读要点**：

- **`agent_history_description` vs 我们的 `Get()`**：上游构造的是**字符串**而不是 `[]Message`——因为上游一次只往 LLM 发**一条**大 user message，里面塞了所有历史。我们的教学版保留 `[]Message` 形状是为了清晰；产品版选择字符串塞进单条消息是为了节省 cache token。两条路都对。
- **双门限触发**：`compact_every_n_steps` + `trigger_char_count`。我们的版本只看消息条数（`MaxMessages`），上游加了字符门限作为下限——避免短消息也触发压缩。s10 可以加上字符门限。
- **`compacted_memory` 是单个字符串字段，不是消息**：上游把所有"曾经被压缩"的历史合并到一个常驻字符串里，再下次压缩时连同新的中间消息一起再交给 LLM 摘要。这是**滚动压缩**模式，比我们的"一次压缩到位"更省 token，但实现复杂度也更高。
- **`assert max_history_items > 5`**：这条断言告诉读者，压缩布局至少需要 6 个槽位（first + summary + N 个 tail）。我们的版本宽松一些，但同样的几何关系是存在的。
- **`sensitive_data` 是 dict 而不是 regex**：上游知道密钥**具体是什么**，做精确字符串替换。我们的 `RedactSensitive` 是不知情场景（密钥从 LLM 输出/tool result 飘进来）的兜底。生产里这两层并存。

**想读更多**：从 `MessageManager.maybe_compact_messages`（service.py#L213）入手，跟着 `MessageCompactionSettings` 跳进 `views.py`，再读 `_filter_sensitive_data` 看上游的脱敏精确字符串替换怎么实现的。

---

**下一节预告**：s04 把 s01/s02 里硬编码的 action `switch` 换成 `Registry`——通过反射从 Go struct tag 生成 JSON Schema、自动注册工具、统一超时守卫。这是把 agent 从"我能跑 3 个内置 action" 升级成"开发者随时加新 action"的关键一步。
