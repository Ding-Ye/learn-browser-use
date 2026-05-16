---
title: "s03 · Message manager & compaction"
chapter: 3
slug: s03-message-manager
est_read_min: 13
---

# s03 · Message manager & compaction

> Teaching focus: after ~30 steps a browser-use agent's history balloons to ~100k tokens; without compaction the next LLM call either hits the context window or torches the bill. This session lifts s01/s02's bare `[]Message` into a `MessageManager` with **clear ownership, swappable policy, and built-in redaction**.

---

## Problem / 问题

s01 / s02's `Agent.Run` contains just this line:

```go
msgs := []Message{{Role: "user", ...}}
// ... loop appends to msgs forever ...
```

As long as the loop runs, `msgs` keeps growing. After 15 steps the per-step browser observation text adds up to several KB each; by step 30 you've blown past 100k tokens — either hitting the context window or torching the bill.

**There's a second hazard**: those observation strings can carry **secrets** at any moment — Bearer tokens pasted into a log, `sk-...` API keys in a config snapshot, user emails. If they slip into history they ride along with the next provider request.

s03 fixes both:

1. **Unbounded history growth** → compaction strategy
2. **Sensitive-data leakage** → redaction filter

But the more important move is **architectural**: promote `[]Message` to a `MessageManager` so "which policy" becomes a swappable field instead of an if-else inside the loop.

## Solution / 解决方案

Introduce the `MessageManager` type with three fields and three methods:

```go
type MessageManager struct {
    History     []Message            // raw history, never trimmed
    MaxMessages int                  // soft cap
    TokenBudget int                  // reserved (s10 actually uses it)
    Strategy    Strategy             // compaction interface
    Sanitizer   func(string) string  // redaction
}
```

Methods: `Add(m)` / `Get() []Message` / `Len()`.

**Two policies, applied at different times**:

| Layer | When | Why |
|---|---|---|
| Redaction | `Add()`, **eagerly** | A debugger dump of in-memory History already leaks. No reason to wait. |
| Compaction | `Get()`, **lazily** | Raw history is the debugger's friend; compaction may call an LLM, do it on demand. |

**Two compaction strategies**, both implementing the `Strategy` interface:

1. **`KeepLastN`** — keep `History[0]` plus the most-recent N-1 messages; drop the rest. The dumbest "throw away old stuff".
2. **`Summarize`** — keep `History[0]` plus one **synthetic system summary** (`[compacted: 12 turns covering search, click actions]`) plus the most-recent N-2 messages. Our teaching summariser uses a deterministic tool-name histogram; upstream calls a real LLM.

Why is `History[0]` always pinned? Because it's the user task / system prompt — drop it and the agent silently forgets what it was trying to do. Upstream takes the same stance (`service.py#L289-L295`).

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

Core ~50 lines (excerpts from `agents/s03-message-manager/`):

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
    summary := summariseTurns(dropped)  // tool-name histogram

    out := make([]Message, 0, maxMessages)
    out = append(out, history[0])                              // pinned task
    out = append(out, Message{Role: "system", Content:
        []ContentBlock{{Type: "text", Text: summary}}})        // synthetic summary
    if tailKeep > 0 {
        out = append(out, history[tailStart:]...)              // most-recent N-2
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

**Four non-obvious points**:

1. **Why `Get()` returns a fresh copy, not a slice alias into the backing array**: if a caller's slice shares our array, their mutation silently mutates our state. This is the classic Go slice footgun. We pay one extra allocation per Get() and the trade-off is "callers can do whatever they want".
2. **Why the user task lives at `History[0]` forever and never gets folded into the summary**: a synthetic summary is by definition a *paraphrase*, and paraphrases drift. If the *task* drifts, the agent silently abandons the user's actual intent. Upstream takes the same line (`service.py#L289`).
3. **Why compaction lives in `Get()` and not in `Add()`**: compacting mid-`Add` can **chop a tool_use/tool_result pair in half** — the assistant says "I want to call `search`", the next message carries the tool_result. If compaction happens between those two and drops the tool_use, the next provider call errors out because it can't resolve `tool_use_id`. Deferring to `Get()` sidesteps the race.
4. **Why redaction is regex-only here**: this session is stdlib-only by design. Three regex patterns cover ~90% of the obvious leaks (OpenAI/Anthropic key prefix, Bearer tokens, emails). Production-grade redaction (upstream's `redact_sensitive_string`) takes a different shape: it *knows what its secrets are* and does exact-match replacement. In a real system **both layers run** — they're complementary.

## What Changed / 与上一节的变化

```diff
- // s02 Agent: history is a bare slice
- type Agent struct {
-     Provider Provider
-     Actions  []Action
-     MaxSteps int
- }
- // loop body:
-   msgs := []Message{...}
-   msgs = append(msgs, ...)

+ // s03 introduces MessageManager
+ type MessageManager struct {
+     History     []Message
+     MaxMessages int
+     Strategy    Strategy
+     Sanitizer   func(string) string
+ }
+ // Future Agent (s04+) owns a *MessageManager.
+ // loop body:
+   mm.Add(msg)              // redaction included
+   msgs := mm.Get()         // compaction included
```

The difference isn't "added a struct" — it's **separation of concerns**:

- **s02**: the loop body assembles history.
- **s03**: the loop body only schedules; **what history looks like** is decided by `MessageManager` + Strategy.

Why does the split land in s03 and not s01? Because **abstractions only earn their keep when a real variation point appears**. s01 had one shape of history; s02 added one provider; s03 is the first session where "two equivalent ways" coexist — `KeepLastN` vs `Summarize` — so the strategy interface becomes load-bearing.

## Try It / 动手试一试

```bash
cd agents/s03-message-manager

# Demo: 20 fake messages → compacted to 5; redaction in action
GOWORK=off go run .

# Tests
GOWORK=off go test -v ./...
```

Expected output shape:

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

Because both `Sanitizer` and `Summarize.Apply` are pure functions, the output is **byte-for-byte reproducible**. `testdata/expected.txt` is the pinned snapshot.

## Upstream Source Reading / 上游源码阅读

Upstream `browser_use/agent/message_manager/service.py` lines 104-300 are the `MessageManager` class. Our teaching version captures three things — compaction, redaction, history ownership — and upstream layers on a real LLM summariser, domain-scoped sensitive_data dict, file_system snapshotting, and one-time screenshot insertion.

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
        self.state = state                       # our self.History
        self.system_prompt = system_message      # pinned first item
        self.file_system = file_system           # arrives in s11
        self.sensitive_data = sensitive_data     # finer-grained than our Sanitizer
        self.max_history_items = max_history_items   # our MaxMessages
        ...
        assert max_history_items is None or max_history_items > 5, \
            'max_history_items must be None or greater than 5'
        # ^ Upstream demands >5 because compaction layout needs:
        #   first + summary + at least 3 trailing items.

    @property
    def agent_history_description(self) -> str:
        """Build agent history description from list of items,
           respecting max_history_items limit"""
        # This is the soul of our Summarize.Apply:
        # - Always keep history[0] (we do too)
        # - Replace middle omitted_count items with one
        #   <sys>[... N previous steps omitted ...]</sys>
        # - Then most-recent max_history_items - 1 items
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

        # Double gate: step cadence + char floor
        steps_since = step_info.step_number - (self.state.last_compaction_step or 0)
        if steps_since < settings.compact_every_n_steps: return False

        history_items = self.state.agent_history_items
        full_history_text = '\n'.join(item.to_string() for item in history_items).strip()
        if len(full_history_text) < (settings.trigger_char_count or 40000): return False

        # Actually call the LLM (our summariseTurns is a deterministic histogram)
        messages = [SystemMessage(content=system_prompt), UserMessage(content=compaction_input)]
        response = await llm.ainvoke(messages)
        summary = (response.completion or '').strip()
        ...
        self.state.compacted_memory = summary
        # Keep first + most-recent keep_last items (same layout as our Summarize)
```

**Reading notes**:

- **`agent_history_description` vs our `Get()`**: upstream produces a **string**, not a `[]Message`, because upstream sends history to the LLM as **one big user message**. Our teaching version preserves `[]Message` shape for clarity; the product version chooses string-in-single-message to maximise cache reuse. Both are valid.
- **Double trigger**: `compact_every_n_steps` + `trigger_char_count`. Our version only counts messages (`MaxMessages`); upstream adds a char floor so short messages don't trigger compaction prematurely. s10 can layer this on.
- **`compacted_memory` is one string field, not a message**: upstream concatenates all previously-compacted history into a single rolling string, then re-summarises it together with new middle messages next time. That's **rolling compaction** — more token-efficient than our "compact once" approach, but more complex.
- **`assert max_history_items > 5`**: this assertion teaches readers that the layout needs at least 6 slots (first + summary + N tail). Our version is permissive but the same geometry applies.
- **`sensitive_data` is a dict, not a regex**: upstream knows **exactly which secrets exist** and does exact string replacement. Our `RedactSensitive` is the complementary case — secrets that slip in from LLM output or tool results when we don't know them. In production both layers run.

**Read more**: start at `MessageManager.maybe_compact_messages` (service.py#L213), follow `MessageCompactionSettings` into `views.py`, then read `_filter_sensitive_data` to see how upstream's precise dict-based redaction actually works.

---

**Next session preview**: s04 replaces the hard-coded `switch` over actions with a `Registry` — JSON Schema generated from Go struct tags via reflection, auto-registered tools, and a unified timeout guard. That's the step that turns the agent from "I support 3 built-in actions" into "any developer can add an action".
