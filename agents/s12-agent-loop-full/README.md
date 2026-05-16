# s12 · Full agent loop (agent-loop-full)

> 11 previous chapters built one piece each. s12 wires them into one Agent that runs end-to-end against a stub browser, with planning every N steps and a fallback LLM on timeout. Everything is re-declared inside this module — zero cross-session imports.
> 前面 11 章每章只造一块。s12 把它们拧成一个 Agent，端到端跑通 stub 浏览器，每 N 步触发 planner，主 LLM 超时自动切换到 fallback。所有东西在本模块内重新声明 —— 不跨章引用。

## What this teaches / 教什么

- **Integration is wiring + 2 new policies.** The agent is `Provider + Tools + Session + DOM + Messages + Cost + FS`. We add planning + fallback on top. Everything else is glue.
- **集成 = 接线 + 2 个新策略**。Agent 就是 `Provider + Tools + Session + DOM + Messages + Cost + FS`。在上面叠 planning 和 fallback，剩下全是胶水。
- **Self-contained means re-declare.** This module imports zero sibling sessions. Every previous chapter's struct shape lives in this directory as well. The teaching cost is duplication; the payoff is `cd s12-agent-loop-full && go test ./...` works in isolation.
- **自给自足意味着复制声明**。本模块不引用任何兄弟章节。每个前置章节的结构在本目录都有一份。代价是冗余，回报是 `cd s12-agent-loop-full && go test ./...` 独立可跑。
- **Planning is opt-in via `PlanEvery`.** Zero = disabled. The planner uses the same `Provider`; a real impl could pass a cheap second model.
- **Planning 通过 `PlanEvery` 开关**。零 = 关闭。Planner 复用同一个 `Provider`；真实实现可以塞一个便宜的二号模型。
- **Fallback uses a FRESH context, not retry-in-place.** A wedged primary that holds the parent ctx hostage would starve the fallback. Issuing `context.WithTimeout(parent, LLMTimeout)` again gives the fallback a clean budget.
- **Fallback 用新的 context，不是原地重试**。卡住的主 LLM 会霸占 parent ctx，让 fallback 没有时间。重新 `context.WithTimeout(parent, LLMTimeout)` 给 fallback 一个干净的预算。

## Run / 运行

```bash
cd agents/s12-agent-loop-full

# E2E demo: scripted 4-turn run (type → search → click → done)
GOWORK=off go run .

# 7 tests (5 required + 2 bonus)
GOWORK=off go test -v ./...
```

`GOWORK=off` because the repo's `go.work` doesn't include s12 yet; the module is self-contained.

## Files / 文件

| File | Purpose / 作用 |
|---|---|
| `types.go`            | Canonical shapes: Message, ContentBlock, Response, ActionCall, ToolSchema, DOMNode, SerializedDOM, DOMRect, Pricing, Event. Re-declared for self-containment. |
| `provider.go`         | Provider interface + MockProvider (with per-response Delay for the timeout test) + OpenAIProvider stub. |
| `message_manager.go`  | MessageManager with KeepLastN compaction only. |
| `registry.go`         | Registry + Dispatcher with per-action timeout. |
| `tools.go`            | 5 Tool impls: SearchTool, ClickTool, TypeTool, ScrollTool, DoneTool. |
| `eventbus.go`         | EventBus + the three lifecycle Event types. |
| `cdp_client.go`       | RecordingCDPClient stub. |
| `session.go`          | BrowserSession + NavigationWatchdog. |
| `dom_service.go`      | DOMService with fixed fixtures for the two demo pages. |
| `token_cost.go`       | TokenCost ledger with 3-model hardcoded pricing table. |
| `filesystem.go`       | LocalFileSystem with path safety + ext allow-list. |
| `planner.go`          | `Plan(ctx, p, history)` one-shot planner call. |
| `system_prompt.txt`   | Small system prompt, embedded via `//go:embed`. |
| `agent.go`            | The integrated `Agent` struct and its `Run(ctx, task)` loop. **The file to read.** |
| `main.go`             | E2E demo against `httptest.Server`. |
| `agent_test.go`       | 7 tests: 5 required + KeepLastN + filesystem sandbox. |
| `testdata/expected.txt` | Captured `go run .` + `go test -v` output. |

## Key teaching points / 关键学习点

1. **Why re-declare everything?** Because the curriculum invariant is "each `sNN-*` is independent". Importing s07's `BrowserSession` would tie s12's build to s07's package layout — a future refactor of s07 would break s12. Each chapter's struct shape is small; copying them is cheaper than the import-graph debt.

2. **Why `PlanEvery int` instead of `Planner interface`?** Because the integration story is "the loop knows when to call the planner, the planner is a function". An interface would invite per-planner config to leak into Agent fields. The single integer + `Plan(ctx, p, history)` function is enough; if you want a richer planner, wrap it in a Provider stub.

3. **Why is fallback a separate Provider field, not a slice of Providers?** Because empirically you have ONE backup, not a chain. A chain invites "if all 5 fail, what now?" which is a different question (give up and surface the error). For real production you'd add jitter + circuit-breaker around each Provider; the loop's job here is to demonstrate the swap, not the cascade.

4. **Why does cost.RegisterInvocation sit outside the `if err != nil` branch?** Because tokens are spent regardless of whether the call's StopReason is end_turn / tool_use / max_tokens. A timeout error from the Provider is the one case where we DON'T register — the primary failed before producing a response. The fallback's tokens, when it succeeds, get the next RegisterInvocation call.

5. **Why is `FS` a field on Agent but not consumed by any built-in tool in s12?** Because the integration point is the Agent struct, not the tool list. A custom `ReadFileTool` that the LLM might call ("save the article to disk") would access `agent.FS` — and we want the sandbox safety in place before that happens, not bolted on later. The test `TestFilesystemSandboxRejectsAbsolutePath` proves the surface is real even without a tool that uses it.

## What this is NOT / 这一节"故意不做"什么

- **No real CDP**. RecordingCDPClient just records frames. Hooking a chromedp or cdp-use backed implementation is one interface impl away.
- **No real LLM HTTP**. The demo uses MockProvider with a scripted queue. OpenAIProvider is a tombstone that returns `ErrNotImplemented` — s02 has the working version.
- **No screenshots**. Upstream sends a base64-encoded PNG every step. We carry text-only DOM; vision tokens / image embedding are an orthogonal concern.
- **No retry-with-backoff inside one provider**. Fallback is single-shot: primary errors, fallback runs once, the loop accepts whatever it returns. Real production wraps each Provider with its own retry middleware before the loop sees it.
- **No persistence**. Cost ledger, message history, sandbox state — all live in-process. A real deployment would write Summary() to a metrics sink and store transcripts in a sqlite for replay.
- **No sub-agents / multi-agent coordination**. One Agent, one task. The s_full doc lists the multi-agent shape as a future-work item.

## Pointer to upstream / 上游对照

- `browser_use/agent/service.py:Agent.step` (L1023-L1250) — the canonical per-step orchestration. Our `Agent.Run` body is the compressed Go shape: same phases (prepare → invoke → execute → post-process), same termination semantics.
- `browser_use/agent/service.py:Agent._get_next_action` (L1163-L1198) — the LLM call with retry + timeout. Our `invokeWithFallback` is the compressed shape; upstream's `_log_model_input_to_lmnr` is a telemetry side-channel we skip.
- `browser_use/agent/message_manager/service.py` — the full MessageManager. Our `message_manager.go` is the irreducible Add/Get split.
- `browser_use/tokens/service.py` — the full TokenCost ledger. Our `token_cost.go` keeps `RegisterInvocation` + `Summary` + hardcoded pricing for 3 models.
