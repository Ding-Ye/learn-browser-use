# s02 · LLM Provider 抽象 (llm-provider)

> The `Provider` interface + 2 concrete impls: a real OpenAI-over-HTTP client (stdlib only) and a deterministic `MockProvider` for tests.
> `Provider` 接口 + 两个实现：纯标准库直连 OpenAI 的真实客户端，以及测试用的确定性 `MockProvider`。

## What this teaches / 教什么

- **Interface = the seam.** Loop code never imports `OpenAIProvider`; it only sees `Provider`. Swap providers without editing the agent loop.
- **接口就是缝隙**：循环代码只依赖 `Provider`，换 OpenAI / Anthropic / Mock 都不动循环。
- **OpenAI wire format, hand-rolled.** No `openai-go` SDK — just `net/http` + `encoding/json`. Every byte on the wire is in plain sight.
- **手写 OpenAI 协议**：不依赖任何 SDK，纯 `net/http` + `encoding/json`，全部字节透明可读。
- **Retry on 429 with context-aware backoff.** Tests run in <100ms because backoff respects context cancellation.
- **429 自动重试 + 背压**：用 `time.NewTimer` + `ctx.Done()` 双 select，测试可以瞬间退出。
- **Tool-call protocol mapping.** OpenAI's `finish_reason` (`stop`/`tool_calls`/`length`) → our `StopReason` (`end_turn`/`tool_use`/`length`). This is where s01's abstract states meet a real API.

## Run / 运行

```bash
# Mock mode (no API key needed)
go run . -mock "search hacker news"

# Real OpenAI
OPENAI_API_KEY=sk-... go run . "what is the capital of France?"
OPENAI_API_KEY=sk-... go run . -model gpt-4o-mini "navigate https://example.com"

# Custom base URL (Ollama, vLLM, LM Studio, etc.)
OPENAI_API_KEY=ignored go run . -base-url http://localhost:11434/v1 "hello"

# Tests (6 total; all use httptest, no real network)
go test -v ./...
```

## Files / 文件

| File | Purpose / 作用 |
|---|---|
| `types.go`             | `Message`, `ContentBlock`, `Response{+InputTokens,+OutputTokens,+Model}`, `ActionCall`. |
| `provider.go`          | `Provider` interface + `ToolSchema{Name,Description,Parameters json.RawMessage}`. |
| `mock_provider.go`     | `MockProvider{Queue, CaptureReqs}`, FIFO + error on exhaustion. |
| `openai_provider.go`   | `OpenAIProvider`: builds wire JSON, posts to `/chat/completions`, parses tool_calls, retries on 429. |
| `main.go`              | CLI: `-mock` flag, `-model`, `-base-url`. |
| `provider_test.go`     | 6 tests (httptest-stubbed; no real network). |
| `testdata/expected.txt`| Sample CLI outputs (mock + real). |

## Key teaching points / 关键学习点

1. **`Parameters json.RawMessage` (not `any` / not `map[string]any`)** — lets callers hand-write strict JSON Schemas without round-tripping through Go reflection. s04 introduces the reverse path: struct tags → JSON Schema.
2. **`MockProvider.Queue` exhaustion errors, not panics** — surfaces "test forgot to queue enough responses" instantly. Don't return zero `Response{}` silently.
3. **Wire-format types kept private** (`openAIRequest`, `openAIMessage`, …) — they are an implementation detail of one provider; do not leak into the agent loop's API.
4. **`sleepCtx` instead of `time.Sleep`** — a context cancel during a 429 retry cancels the sleep, not waits the full delay. This is also why `RetryDelay: 10*time.Millisecond` keeps tests fast.

## What this is NOT / 这一节"故意不做"什么

- No structured-output JSON Schema response_format (s04 covers it).
- No streaming. Agent-style usage is request/response.
- No message compaction (s03).
- No real conversation loop — the `main.go` calls `Invoke` exactly once. s01's loop + s02's provider compose in s03.
- No multi-provider parity (Anthropic, Google, etc.) — Phase G addendum will add them on top of this same interface.

## Upstream / 上游对照

The real provider abstraction lives in `browser_use/llm/base.py` (60 lines — a `Protocol` with `ainvoke()`) and the real OpenAI impl in `browser_use/llm/openai/chat.py` (307 lines, async, uses the `openai-python` SDK). See `docs/zh/s02-llm-provider.md` § Upstream Source Reading for an annotated excerpt.
