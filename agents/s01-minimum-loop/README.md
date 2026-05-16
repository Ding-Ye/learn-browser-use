# s01 · 最小 agent 循环 (minimum-loop)

> The smallest browser-agent core that compiles and runs. No real LLM, no real browser.
> 最小的 browser-agent 内核，能跑能编译。没有真的 LLM，也没有真的浏览器。

## What this teaches / 教什么

- **Agent = loop(observe → think → act)**. That's the whole insight.
- 一切都围绕 `loop(观察 → 思考 → 执行)` 这条主线展开。
- The `StopReason` switch (`end_turn` / `tool_use` / `max_tokens`) is the protocol.
- `StopReason` 的三态切换就是协议本身：何时终止、何时执行工具、何时报错。
- `Provider` and `Action` are interfaces; the loop knows nothing about LLMs or browsers.
- `Provider` 与 `Action` 是接口，循环本体对 LLM / 浏览器一无所知。

## Run / 运行

```bash
go run . "search hacker news"           # 触发 search 动作
go run . "navigate https://example.com" # 触发 navigate 动作
go run . -v -max-steps 3 "nothing"      # 直接 end_turn，3 步内退出

go test -v ./...                        # 跑全部 4+ 个测试
```

## Files / 文件

| File | Purpose / 作用 |
|---|---|
| `types.go`         | `Message`, `ContentBlock`, `Response`, `ActionCall` — the wire protocol shape. |
| `fake_provider.go` | `FakeProvider` — keyword-based "LLM" stand-in. Deterministic. |
| `actions.go`       | `SearchAction`, `NavigateAction`, `DoneAction` — three stubs. |
| `loop.go`          | `Agent.Run()` — the core 60-line loop. |
| `main.go`          | CLI entry. Parses flags + runs Agent. |
| `loop_test.go`     | 4 tests covering happy path / max steps / unknown action. |
| `testdata/expected.txt` | Realistic transcript of `-v "search ..."`. |

## Key teaching points / 关键学习点

1. **What `Agent.Run` returns**: a final text + maybe an error. The loop body is just a `for step < MaxSteps` over `provider.Invoke → switch StopReason`.
2. **No cross-module imports**: `agents/s01-minimum-loop/` is self-contained. Later sessions copy & extend, they don't import.
3. **FakeProvider is deterministic**: pure function of `task` + history. Makes tests trivial.
4. **The tool_result protocol**: action results come back as a `tool` role message with `ContentBlock{Type: "tool_result"}`. This is the same shape real LLM APIs use.

## What this is NOT / 这一节"故意不做"什么

- No real LLM call (s02 adds that)
- No conversation history compaction (s03)
- No tool registry / schema generation (s04)
- No CDP / browser stub (s05–s07)
- No DOM serialization (s08–s09)
- No cost tracking, no filesystem sandbox (s10–s11)
- No planning, no fallback LLM (s12)

The whole point of s01 is: **you can see every line**.
本节的目的就是"100% 可读，0% 黑盒"。

## Upstream / 上游对照

The real agent loop lives in `browser_use/agent/service.py#L1023-L2480` (≈1500 lines).
That's the same shape — observe → think → act — wrapped in retries, planning, message
compaction, watchdog hooks, token budget tracking, and structured-output schemas.
Read this session first to see the skeleton, then `docs/zh/s01-minimum-loop.md` for the full walkthrough.
