---
title: "s10 · Token 计费"
chapter: 10
slug: s10-token-cost
est_read_min: 12
---

# s10 · Token 计费

> 教什么：s02 给我们一个 `Provider.Invoke` 接口，每次调用都吐 `InputTok` / `OutputTok` 字段，但没人把这些数字记下来。s10 在 agent 旁边挂一本账：`TokenCost` 结构 + `//go:embed` 进二进制的定价表 + 一个可选的 TTL 缓存远端刷新器。整个模块 ~350 行 Go，核心 RegisterInvocation 路径不到 30 行。

---

## Problem / 问题

s02 之后调用方手里典型用法是这样：

```go
resp, err := provider.Invoke(ctx, msgs, schema)
if err != nil { return err }
// resp.InputTok / resp.OutputTok / resp.Model 都在
// ... 然后没下文了
```

token 数字像水蒸气一样散掉了。具体痛点：

1. **看不见钱在烧**：跑一次 agent，到底花了几分钱？没人能答。生产环境上线之前，财务和运维一定会问。
2. **看不见 per-model 分布**：你可能同时挂了 gpt-4o（贵但准）和 gpt-4o-mini（便宜但能干粗活）。如果不知道每个模型分别烧了多少，做"成本优化"就是瞎猜。
3. **定价数据从哪来**：模型一年改几次价。如果每次启动都去 LiteLLM 的 GitHub raw URL 拉一次，平均故障率会被 CDN 拖累。
4. **新模型刚出怎么办**：还没人提交它的定价 PR，agent 不能因此 panic。

s10 解决这四件事，办法非常直接：用一个 struct 把账本拢起来，方法签名暴露写入路径和读取路径。

## Solution / 解决方案

引入 `TokenCost`：

```go
type TokenCost struct {
    History []Usage
    Total   TotalCost
    pricing map[string]Pricing
    byModel map[string]TotalCost
    clock   func() time.Time
}
```

四个方法承担全部职责：

| 方法 | 责任 | 上游对照 |
|---|---|---|
| `RegisterInvocation(model, in, out) Usage` | 写一行账，更新 Total 和 byModel | `add_usage` + `calculate_cost` 合并 |
| `Summary() string`                          | 渲染人类可读的报告（按模型排序） | `log_usage_summary` 精简版 |
| `TotalUSD() float64`                        | 全局总成本 | `UsageSummary.total_cost` |
| `PerModel(model) TotalCost`                 | 某模型的累计 | `UsageSummary.by_model[model]` |

定价表通过 `//go:embed pricing_data.json` 直接编译进二进制：

```json
{
  "gpt-4o": {"input_per_1k": 0.0025, "output_per_1k": 0.01},
  "gpt-4o-mini": {"input_per_1k": 0.00015, "output_per_1k": 0.0006},
  ...
}
```

可选的 `Refresher` 提供"远端覆盖"路径——但 `Source` 是一个函数，不是 URL：

```go
type Refresher struct {
    Source    func() (map[string]Pricing, error)
    CacheTTL  time.Duration
    lastFetch time.Time
    cached    map[string]Pricing
}
```

默认 `Source` 返回一份硬编码 map（"stubbed remote"）。要接真 HTTP？换个 `Source` 闭包就行。

## How It Works / 工作原理

```
   provider.Invoke(ctx, msgs)
   ─────────────────────────►
        resp.Model, resp.InputTok, resp.OutputTok
                  │
                  ▼
   cost.RegisterInvocation(resp.Model, resp.InputTok, resp.OutputTok)
                  │
                  ├──► pricing[model] 查 rate
                  │       └─ ok=false → cost=$0, HasPricing=false
                  ├──► 计算 inputCost = in/1000 * inputPer1k
                  ├──► 计算 outputCost = out/1000 * outputPer1k
                  ├──► append History  (forensic detail)
                  ├──► Total.* 累加
                  └──► byModel[model].* 累加  (O(1) summary)

   ────────────────────────────────────────────────────────────────

   refresher.Get("gpt-4o")
   ─────────────────────────►
        clock.Now() - lastFetch < CacheTTL ?
              ├── 是 → 返回 cached["gpt-4o"]            (cache hit)
              └── 否 → Source() → cache 整张表 → 返回    (refresh)
```

核心代码（约 50 行）：

```go
// token_cost.go
func (tc *TokenCost) RegisterInvocation(model string, inputTok, outputTok int) Usage {
    p, ok := tc.pricing[model]

    inputCost := float64(inputTok) / 1000.0 * p.InputPer1k
    outputCost := float64(outputTok) / 1000.0 * p.OutputPer1k
    if !ok {
        inputCost, outputCost = 0, 0          // ← 未知模型只记 token,不算钱
    }

    row := Usage{
        Model: model, InputTok: inputTok, OutputTok: outputTok,
        InputCost: inputCost, OutputCost: outputCost,
        Timestamp: tc.clock(), HasPricing: ok,
    }
    tc.History = append(tc.History, row)

    tc.Total.InputUSD  += inputCost
    tc.Total.OutputUSD += outputCost
    tc.Total.InputTok  += inputTok
    tc.Total.OutputTok += outputTok
    tc.Total.Invocations++

    sub := tc.byModel[model]                  // ← 取出当前模型的累计
    sub.InputUSD  += inputCost
    sub.OutputUSD += outputCost
    sub.InputTok  += inputTok
    sub.OutputTok += outputTok
    sub.Invocations++
    tc.byModel[model] = sub                   // ← 写回
    return row
}
```

**4 个非显然之处**：

1. **为什么定价表是 embed 不是 fetch？** 上游 Python 每次启动 HTTP GET LiteLLM 的 raw GitHub URL，缓存到 `~/.cache/browser_use/token_cost/`。问题是：模型一年才改几次价，但这条 HTTP 路径是"程序启动卡住"故障的最大来源之一。倒过来：把已知好的版本编译进二进制，refresh 变成可选项。CLI 场景这是合理默认；server 场景再叠 refresher。
2. **为什么 TTL 缓存而不是事件驱动失效？** 事件驱动需要上游 push 通知；LiteLLM 不会主动告诉你"今天 gpt-4o 又降价了"。bounded staleness 又可以接受——24h 误差最多让你按昨天的价格算账，不是"按上个月的价格算账"。TTL 在每个轴上都赢。
3. **为什么单位是 per-1k tokens 而不是 per-token？** 因为 OpenAI/Anthropic/Google 的官方定价页就是这个单位。保持同一分母意味着 `pricing_data.json` 可以直接和公开定价页截图对照。0.0025 比 0.0000025 好读得多；sub-cent 级别的乘法精度也更友好。
4. **为什么未知模型返回 `(zero, false)` 而不是 error？** 上游就是这么做的（`get_model_pricing` 返回 None，不抛异常）。理由是：新模型发布当天，定价 PR 还没合，用户拉 main 跑就 panic 太凶。usage 记上账，cost 算 0，HasPricing=false——这个降级路径让"昨天能跑的代码今天不能跑"的概率降到零。

## What Changed / 与上一节的变化

s09 风格（DOM 服务，没有任何成本追踪）：

```diff
- resp, err := provider.Invoke(ctx, msgs, schema)
- if err != nil { return err }
- // resp.InputTok, resp.OutputTok 散掉了
```

s10 之后：

```diff
+ cost := NewTokenCost()                              // 拿一本账
+ ...
+ resp, err := provider.Invoke(ctx, msgs, schema)
+ if err != nil { return err }
+ cost.RegisterInvocation(resp.Model, resp.InputTok, resp.OutputTok)
+ ...
+ fmt.Println(cost.Summary())                         // run 结束时打报告
```

关键性增量：**第一次把"agent 跑了一圈到底烧了多少钱"做成可观测信号**。这是生产部署前最后一个非功能性需求——s12 的完整 agent loop 会在头尾把它接好，s_full 文档会把它列在端到端集成里。

后续会反复用到这一节的东西：
- s12 的 agent loop 在每次 `Provider.Invoke` 后调一次 `cost.RegisterInvocation`，最后 `agent.Run()` 返回前 log 一次 `Summary()`。
- 真实部署里这个 ledger 可以再加一层 prometheus / OpenTelemetry 导出器，但概念是一样的。

## Try It / 动手试一试

```bash
cd agents/s10-token-cost

# 看 5 次 fake invocations 的成本报告
GOWORK=off go run .

# 6 个测试
GOWORK=off go test -v ./...
```

`GOWORK=off` 只是因为根目录 `go.work` 还没把 s10 加进去；模块自身是自洽的。

期望输出（节选）：

```
# Registering 5 fake invocations across 3 models
  [0] gpt-4o               in=1500  out=320  -> $0.0069  (step 1: planning)
  ...
Token cost summary — 5 invocation(s)
  Total: in=23100 tok  out=2310 tok  cost=$0.0266

  Per model:
    claude-3-5-sonnet       invocations=1  in=1800  out=240  cost=$0.0090
    gpt-4o                  invocations=2  in=3600  out=500  cost=$0.0140
    gpt-4o-mini             invocations=2  in=17700  out=1570  cost=$0.0036
```

测试覆盖：

- `TestRegistrationAccumulates` — 两次同一模型的调用，History 长度 = 2，Total 与 PerModel 一致。
- `TestCostComputation` — gpt-4o 跑 1000 in + 500 out 应该恰好 = $0.0075（写死断言，定价改了必须改这个测试）。
- `TestCacheTTL` — Refresher 在 TTL 内只调一次 Source；时间步进过 TTL 后再次调用。
- `TestUnknownModelReturnsZero` — 未知模型不报错；cost=0；token 仍累加进 Total。
- `TestEmbeddedPricingLoaded` — 四个文档化模型都能 lookup 到非零费率；Snapshot 是真拷贝。
- `TestSummaryIsDeterministic`（加分）— Summary() 跨插入顺序输出稳定（map 遍历需排序）。

## Upstream Source Reading / 上游源码阅读

上游 `browser_use/tokens/service.py` 的 `TokenCost` 类有 500+ 行，绝大多数是彩色日志、Anthropic prompt-caching 分桶、HTTP 拉取 + 磁盘 TTL 缓存。s10 只取最骨架的 ledger + 算钱 + TTL 三件事。

```python
# Source: browser_use/tokens/service.py#L48-L120

class TokenCost:
    """Service for tracking token usage and calculating costs"""

    CACHE_DIR_NAME = 'browser_use/token_cost'
    CACHE_DURATION = timedelta(days=1)
    DEFAULT_PRICING_URL = 'https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json'

    def __init__(self, include_cost: bool = False, pricing_url: str | None = None):
        # ↓ 对应我们 Go 的 NewTokenCost。上游有一个 feature flag
        #   include_cost 控制是否做成本计算；我们的 Go 版本永远算，
        #   因为 pricing 是 embed 进二进制的，lookup 是 O(1)。
        self.include_cost = include_cost or os.getenv('BROWSER_USE_CALCULATE_COST', 'false').lower() == 'true'

        # ↓ 对应我们的 History 字段。
        self.usage_history: list[TokenUsageEntry] = []

        # ↓ 上游会 monkey-patch 已注册 LLM 的 .ainvoke 方法，
        #   注入"调完之后把 usage 写进 history"的逻辑。
        #   我们的 Go 版本不做 monkey-patch——调用方显式调 RegisterInvocation。
        #   更简单，也更诚实：副作用都在调用点可见。
        self.registered_llms: dict[str, BaseChatModel] = {}

        # ↓ pricing 是 lazy load 的。第一次 .initialize() 调用时才填。
        #   我们的 Go 版本在 init() 里 eager 解析 embedded JSON。
        self._pricing_data: dict[str, Any] | None = None
        self._cache_dir = xdg_cache_home() / self.CACHE_DIR_NAME
```

```python
# Source: browser_use/tokens/service.py#L212-L240

async def calculate_cost(self, model: str, usage: ChatInvokeUsage) -> TokenCostCalculated | None:
    if not self.include_cost:
        return None

    data = await self.get_model_pricing(model)
    if data is None:
        return None                                   # ← 未知模型返回 None,不是 error

    # ↓ 上游把 prompt_tokens 分成三桶:
    #     (a) uncached = 实际新发上去的（按 input rate 收费）
    #     (b) cached_read = Anthropic 缓存命中（按 cache_read rate）
    #     (c) cache_creation = 写入 Anthropic 缓存（按 cache_creation rate）
    #   我们的 Go 只有一桶（InputTok）,把 Anthropic prompt cache 留给后续扩展。
    uncached_prompt_tokens = usage.prompt_tokens - (usage.prompt_cached_tokens or 0)

    return TokenCostCalculated(
        new_prompt_tokens=usage.prompt_tokens,
        new_prompt_cost=uncached_prompt_tokens * (data.input_cost_per_token or 0),
        prompt_read_cached_tokens=usage.prompt_cached_tokens,
        prompt_read_cached_cost=usage.prompt_cached_tokens * data.cache_read_input_token_cost
        if usage.prompt_cached_tokens and data.cache_read_input_token_cost
        else None,
        prompt_cached_creation_tokens=usage.prompt_cache_creation_tokens,
        prompt_cache_creation_cost=usage.prompt_cache_creation_tokens * data.cache_creation_input_token_cost
        if data.cache_creation_input_token_cost and usage.prompt_cache_creation_tokens
        else None,
        completion_tokens=usage.completion_tokens,
        completion_cost=usage.completion_tokens * float(data.output_cost_per_token or 0),
    )

def add_usage(self, model: str, usage: ChatInvokeUsage) -> TokenUsageEntry:
    """Add token usage entry to history (without calculating cost)"""
    # ↓ 注意 add_usage 和 calculate_cost 是分开的两个方法。
    #   只有 include_cost=True 时才会调后者。
    #   我们的 RegisterInvocation 把两步合并 —— pricing 查表是
    #   map 访问,几纳秒,没必要分两步。
    entry = TokenUsageEntry(
        model=model,
        timestamp=datetime.now(),
        usage=usage,
    )
    self.usage_history.append(entry)
    return entry
```

完整 60-100 行带注释的抽取版在 `upstream-readings/s10-token-cost.py`，包括 `_load_pricing_data` / `_find_valid_cache` 的 TTL 缓存路径——我们的 Go `Refresher.fresh()` 是它的内存版镜像。
