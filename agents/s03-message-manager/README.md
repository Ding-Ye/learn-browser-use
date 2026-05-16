# s03 · 消息管理与压缩 (message-manager)

> A `MessageManager` that owns conversation history and applies two layered policies: compaction (bound the size) and redaction (mask secrets).
> 一个拥有对话历史并叠加两层策略的 `MessageManager`：压缩（限制大小）+ 脱敏（屏蔽密钥）。

## What this teaches / 教什么

- **History is a state, not a list.** s01/s02 kept history as a raw `[]Message`; here it becomes a struct with policy hooks.
- 历史不是数组，而是一个**有策略的状态**。s01/s02 用裸 `[]Message`，本节升级为带钩子的结构体。
- **Compaction is a view, not a mutation.** `Add()` never drops; `Get()` applies the strategy lazily. Tests can inspect both views.
- 压缩是**视图变换**，不是真删除。`Add()` 只追加，`Get()` 才按策略修剪。测试可以同时看到两个视图。
- **Two strategies, one interface.** `KeepLastN` and `Summarize` both implement `Strategy.Apply([]Message, int) []Message`. Swap by replacing one field.
- 两个策略实现同一接口 `Strategy`：`KeepLastN`（保留最后 N 个）和 `Summarize`（合并历史为一条 system summary）。
- **Redaction runs eagerly at Add().** Idempotent, regex-only, stdlib-only.
- 脱敏在 `Add()` 立即执行；幂等、纯正则、stdlib only。

## Run / 运行

```bash
cd agents/s03-message-manager
GOWORK=off go run .            # demo: 20 msgs → compacted to 5
GOWORK=off go test -v ./...    # 5 tests
```

## Files / 文件

| File | Purpose / 作用 |
|---|---|
| `types.go`          | `Message`, `ContentBlock` — self-contained copies from s01. |
| `message_manager.go`| `MessageManager{History, MaxMessages, TokenBudget}`. `Add` / `Get` / `Len`. |
| `compaction.go`     | `Strategy` interface + `KeepLastN` + `Summarize`. |
| `redact.go`         | `RedactSensitive` — 3 regex patterns (sk-keys, Bearer tokens, emails). |
| `main.go`           | CLI demo: 20 fake messages → compaction + redaction stats. |
| `manager_test.go`   | 5 tests: KeepsLastN, SummarizeReplacesOldTurns, RedactionAPIKeys, RedactionEmails, ImageContentPreserved. |
| `testdata/expected.txt` | Reference output of `go run .`. |

## Key teaching points / 关键学习点

1. **Why `Get()` returns a copy**: callers must not be able to mutate the manager's backing array. We pay one allocation per Get() for safety.
2. **Why `History[0]` is always pinned**: it's the user task; dropping it lets the agent silently drift away from the original goal. Upstream also pins the first item (`service.py#L289-L295`).
3. **Why redaction is eager but compaction is lazy**: leaked secrets in in-memory History are already a hazard (a debugger dump leaks them). Compaction has no such urgency — it's a token budget concern, not a security concern.
4. **Why we don't compact mid-loop**: real upstream gates compaction by step interval + char count; we instead expose it through `Get()` so the caller (s12+) chooses cadence. Mid-action compaction can chop a tool-call/tool-result pair in half and corrupt the next provider call.

## What this is NOT / 这一节"故意不做"什么

- No LLM-driven summarisation (upstream does this; we use a deterministic histogram instead — testable).
- No token counting (we expose `TokenBudget` as a hint but never use it; s10 adds real cost tracking).
- No domain-scoped sensitive_data dict (upstream `service.py#L388-L417`).
- No screenshot / image handling (s09).
- No persistence — restart the process, history is gone.

## Upstream / 上游对照

Real implementation: `browser_use/agent/message_manager/service.py#L104-L500` (~400 lines).
That's the same role — history + compaction + redaction — wrapped in: real LLM summariser (`maybe_compact_messages` L213), domain-aware sensitive data (`_get_sensitive_data_description` L388), file-system snapshotting (L127), and one-time screenshot inclusion (L447). Read `docs/zh/s03-message-manager.md` for an annotated walkthrough.
