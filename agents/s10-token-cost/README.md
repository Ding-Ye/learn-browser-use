# s10 · Token cost tracking (token-cost)

> Every `Provider.Invoke` from s02 returns token counts. Without a ledger they evaporate. s10 layers a `TokenCost` struct on top — embedded pricing table, per-model rollup, optional remote refresher with TTL cache.
> s02 的 `Provider.Invoke` 每次都返回 token 数，但没有人记。s10 在它旁边挂一本账：`TokenCost` 结构 + 内嵌定价表 + 可选的 TTL 缓存远端刷新器。

## What this teaches / 教什么

- **Side-channel observability**: the cost ledger is a struct the agent owns, not a wrapper around `Provider.Invoke`. The loop body calls `cost.RegisterInvocation(...)` once per step. Zero coupling to the rest of the agent.
- **观测器是侧通道**：cost ledger 是 agent 拥有的一个普通 struct，不是 `Provider.Invoke` 的包装。循环里每步调一次 `cost.RegisterInvocation(...)`。和 agent 其余部分零耦合。
- **Embed your pricing table.** Public model rates change a handful of times per year. Build-time `//go:embed` is the right tool; runtime HTTP fetch is the wrong tool with extra failure modes.
- **定价表用 embed 进二进制**。公开模型的费率一年只变几次。`//go:embed` 是合适的工具，启动时 HTTP fetch 是错的工具，外加额外的失败路径。
- **TTL cache, not invalidation event.** When the upstream source can't push notifications and bounded staleness is fine (we're talking dollars-per-day error, not real-time finance), TTL beats event-driven cache busting on every axis.
- **TTL 缓存而不是失效事件**。上游不能 push 通知，bounded staleness 又能接受（误差以"天"为单位、不是金融实时），TTL 在每个维度都赢。
- **Unknown model = $0, not error.** A brand-new model that's not yet in the pricing table must still get its usage logged. Loud panic on first invocation would be hostile to users who pull mainline the day a new model ships.
- **未知模型 = $0，不是 error**。新发布、还没进定价表的模型，usage 还是要记上账，cost 算 0。上来就 panic 对刚拉 main 的用户太凶。

## Run / 运行

```bash
GOWORK=off go run .              # demo: 5 fake invocations across 3 models, print Summary()
GOWORK=off go test -v ./...      # 6 tests (5 required + 1 determinism bonus)
```

(`GOWORK=off` because the repo's `go.work` doesn't include s10 yet; the module is self-contained.)

## Files / 文件

| File | Purpose / 作用 |
|---|---|
| `pricing_data.json`  | Embedded JSON with 4 model rates. Format: `{"gpt-4o": {"input_per_1k": 0.0025, "output_per_1k": 0.01}, ...}`. |
| `pricing.go`         | `//go:embed` loader. Exposes `Pricing` struct and `LookupPricing(model) (Pricing, bool)`. |
| `token_cost.go`      | `TokenCost`, `Usage`, `TotalCost`. Methods: `RegisterInvocation`, `Summary`, `TotalUSD`, `PerModel`. |
| `refresher.go`       | Optional remote-source path. `Refresher{Source, CacheTTL, ...}.Get(model)`. Default `Source` returns a hardcoded map (stubbed remote). |
| `main.go`            | CLI demo: register 5 invocations across 3 models, print `Summary()`, show refresher rate diff. |
| `pricing_test.go`    | 6 tests: accumulation, cost math, TTL cycle, unknown-model graceful degrade, embedded data loaded, summary determinism. |
| `testdata/expected.txt` | Captured `go run .` + `go test -v` output. |

## Key teaching points / 关键学习点

1. **Why is `pricing_data.json` embedded instead of fetched?** Upstream Python downloads from a LiteLLM GitHub URL with a 24h on-disk cache. We invert that: ship a known-good table in the binary; treat refresh as opt-in. Two reasons — (a) model prices change a handful of times per year, so the "live" version is overkill, and (b) the network dependency is the single biggest source of "the program hung at startup" bugs. The embedded table is the right default for a CLI; a server might layer the refresher on top.

2. **Why dollars-per-1k-tokens and not dollars-per-token?** Because that's the unit OpenAI/Anthropic/Google quote on their public pricing pages. Keeping the same denominator means the JSON file is directly diffable against a screenshot of those pages. Sub-cent token-level rates (0.0000025/tok for gpt-4o input) lose readability fast.

3. **Why a `Source` function and not a URL on the Refresher?** Because dependency-injecting a fake source is one closure literal — no `httptest.Server`, no mock library. The s10 chapter is about cost math and cache TTL, not HTTP plumbing; that lives in s02-llm-provider. Production code can plug in an HTTP-fetcher trivially.

4. **Why does `TotalCost` exist when `Pricing` has the same shape?** It doesn't — `Pricing` carries *rates* (USD per 1k tokens), `TotalCost` carries *absolutes* (USD total). Sharing a struct would conflate units, which is exactly the kind of bug a type system is supposed to catch. The four-field duplication is the price of unit safety.

5. **Why is the unknown-model row still appended to History?** Because token counts are useful even when cost is unknown. If you run a new model, you still want to know "we burned 100k tokens on it" — the answer is in `Total.InputTok`, not `TotalUSD()`. Dropping the row would erase that signal.

## What this is NOT / 这一节"故意不做"什么

- **No Anthropic prompt-caching breakdown.** Upstream tracks `prompt_cached_tokens` / `prompt_cache_creation_tokens` separately (Anthropic charges different rates for read-cached vs cache-creation tokens). We have only `InputTok`/`OutputTok`. Adding the cached path is mechanical — extend `Usage` and `Pricing`, mirror the math.
- **No persistence.** Upstream writes a JSON cache to `~/.cache/browser_use/token_cost/pricing_YYYYMMDD_HHMMSS.json`. We hold it in memory for the session. Persistence is one `os.WriteFile` away but not load-bearing for the teaching point.
- **No real HTTP refresh.** `defaultStubSource` returns a hardcoded map. A real impl would `http.Get` against a LiteLLM-style URL. The `Source func()` shape is exactly what you'd plug an HTTP client into.
- **No concurrency safety.** `RegisterInvocation` writes to `History`/`byModel`/`Total` without a mutex. The s12 integration loop is single-threaded, so this is fine in context. A multi-agent setup would need a `sync.Mutex` around the write path.

## Pointer to upstream / 上游对照

- `browser_use/tokens/service.py` — `TokenCost` class. ~500 lines covering pricing fetch + cache + per-model rollup + colored logging. Our 6 Go files compress the load-bearing parts to ~350 lines.
- `browser_use/tokens/views.py` — Pydantic models: `TokenUsageEntry`, `ModelPricing`, `UsageSummary`. Our `Usage`, `Pricing`, `TotalCost` map onto these one-to-one minus the cache fields.
- `browser_use/tokens/custom_pricing.py` — local override table for models not in LiteLLM. Conceptually the same as our `Refresher` returning custom values; we collapse the two paths into one.

The README in `docs/{zh,en}/s10-token-cost.md` walks through the chapter narrative; the file you're reading is the operational guide.
