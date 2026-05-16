---
title: "s10 · Token cost tracking"
chapter: 10
slug: s10-token-cost
est_read_min: 12
---

# s10 · Token cost tracking

> Teaching focus: s02 gave us a `Provider.Invoke` interface that returns `InputTok` / `OutputTok` on every call. Nobody recorded those numbers. s10 puts a ledger next to the agent — `TokenCost` struct, pricing table baked in via `//go:embed`, plus an optional TTL-cached remote refresher. About 350 lines of Go in total; the load-bearing `RegisterInvocation` path is under 30.

---

## Problem / 问题

After s02, the typical call site looks like this:

```go
resp, err := provider.Invoke(ctx, msgs, schema)
if err != nil { return err }
// resp.InputTok / resp.OutputTok / resp.Model all populated
// ... and then nothing happens with them
```

The token counts evaporate. Concrete pain:

1. **No visibility into spend.** One agent run — how much did it cost in cents? Nobody can answer. Finance and ops will ask before any production rollout.
2. **No per-model breakdown.** You probably have gpt-4o (expensive, accurate) and gpt-4o-mini (cheap, fine for grunt work) wired in parallel. If you can't see which model burned how much, "cost optimization" is guesswork.
3. **Where does pricing data come from?** Model rates change a handful of times per year. Doing an HTTP GET to LiteLLM's raw GitHub URL on every startup makes your average startup latency hostage to a CDN.
4. **What about a brand-new model?** Nobody's submitted a pricing PR for it yet — the agent must not panic.

s10 solves all four. The shape is unsurprising for Go: one struct that holds the ledger, methods that expose the read and write paths.

## Solution / 解决方案

Introduce `TokenCost`:

```go
type TokenCost struct {
    History []Usage
    Total   TotalCost
    pricing map[string]Pricing
    byModel map[string]TotalCost
    clock   func() time.Time
}
```

Four methods carry the whole load:

| Method | Responsibility | Upstream analog |
|---|---|---|
| `RegisterInvocation(model, in, out) Usage` | Write one row; bump Total and byModel | `add_usage` + `calculate_cost` merged |
| `Summary() string`                          | Render human-readable report (sorted by model) | Distilled `log_usage_summary` |
| `TotalUSD() float64`                        | Overall total | `UsageSummary.total_cost` |
| `PerModel(model) TotalCost`                 | Rollup for one model | `UsageSummary.by_model[model]` |

The pricing table is compiled into the binary via `//go:embed pricing_data.json`:

```json
{
  "gpt-4o": {"input_per_1k": 0.0025, "output_per_1k": 0.01},
  "gpt-4o-mini": {"input_per_1k": 0.00015, "output_per_1k": 0.0006},
  ...
}
```

An optional `Refresher` provides the "remote override" path — but `Source` is a function, not a URL:

```go
type Refresher struct {
    Source    func() (map[string]Pricing, error)
    CacheTTL  time.Duration
    lastFetch time.Time
    cached    map[string]Pricing
}
```

The default `Source` returns a hardcoded map ("stubbed remote"). Want a real HTTP fetch? Swap one closure.

## How It Works / 工作原理

```
   provider.Invoke(ctx, msgs)
   ─────────────────────────►
        resp.Model, resp.InputTok, resp.OutputTok
                  │
                  ▼
   cost.RegisterInvocation(resp.Model, resp.InputTok, resp.OutputTok)
                  │
                  ├──► pricing[model] lookup
                  │       └─ ok=false → cost=$0, HasPricing=false
                  ├──► inputCost  = in/1000  * inputPer1k
                  ├──► outputCost = out/1000 * outputPer1k
                  ├──► append History  (forensic detail)
                  ├──► Total.* incremented
                  └──► byModel[model].* incremented  (O(1) Summary)

   ────────────────────────────────────────────────────────────────

   refresher.Get("gpt-4o")
   ─────────────────────────►
        clock.Now() - lastFetch < CacheTTL ?
              ├── yes → return cached["gpt-4o"]            (cache hit)
              └── no  → Source() → cache the map → return  (refresh)
```

Core code (~50 lines):

```go
// token_cost.go
func (tc *TokenCost) RegisterInvocation(model string, inputTok, outputTok int) Usage {
    p, ok := tc.pricing[model]

    inputCost := float64(inputTok) / 1000.0 * p.InputPer1k
    outputCost := float64(outputTok) / 1000.0 * p.OutputPer1k
    if !ok {
        inputCost, outputCost = 0, 0          // ← unknown model: tokens only, no $
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

    sub := tc.byModel[model]                  // ← read this model's rollup
    sub.InputUSD  += inputCost
    sub.OutputUSD += outputCost
    sub.InputTok  += inputTok
    sub.OutputTok += outputTok
    sub.Invocations++
    tc.byModel[model] = sub                   // ← write it back
    return row
}
```

**4 non-obvious points**:

1. **Why is pricing embedded instead of fetched?** Upstream Python does an HTTP GET against LiteLLM's raw GitHub URL on every startup and caches to `~/.cache/browser_use/token_cost/`. Problem: model prices change a handful of times per year, but that HTTP path is the single biggest source of "the program hung on startup" bugs. Invert it: bake a known-good snapshot into the binary, make refresh opt-in. That's the right default for a CLI; a long-running server can layer the refresher on top.
2. **Why TTL cache instead of an invalidation event?** An event-based scheme needs an upstream that pushes notifications. LiteLLM doesn't ping you when "gpt-4o got cheaper today". Bounded staleness is also fine — 24h cache lag means you bill yesterday's rate at worst, not last month's. TTL wins on every axis.
3. **Why dollars-per-1k-tokens, not dollars-per-token?** Because OpenAI/Anthropic/Google quote their pricing pages in that unit. Sharing the denominator means `pricing_data.json` is directly diffable against a screenshot of those pages. 0.0025 reads better than 0.0000025, and sub-cent float multiplication is friendlier on the precision side too.
4. **Why does an unknown model return `(zero, false)` instead of an error?** Because upstream does the same (`get_model_pricing` returns None, doesn't raise). The reason: on the day a new model ships, the pricing PR hasn't merged. A user who pulls main shouldn't get a panic; they should get an entry with cost=0 and HasPricing=false. That degrade path makes "yesterday's code stops working today" a non-event.

## What Changed / 与上一节的变化

s09 style (DOM service — no cost tracking anywhere):

```diff
- resp, err := provider.Invoke(ctx, msgs, schema)
- if err != nil { return err }
- // resp.InputTok, resp.OutputTok evaporated
```

After s10:

```diff
+ cost := NewTokenCost()                              // grab a ledger
+ ...
+ resp, err := provider.Invoke(ctx, msgs, schema)
+ if err != nil { return err }
+ cost.RegisterInvocation(resp.Model, resp.InputTok, resp.OutputTok)
+ ...
+ fmt.Println(cost.Summary())                         // at run end, print report
```

The crucial increment: **"how much did one agent run cost" becomes an observable signal for the first time**. This is the last non-functional requirement before production rollout — s12's full agent loop will wire it head-and-tail, and the s_full integration doc lists it among the end-to-end pieces.

Downstream uses:
- s12's agent loop calls `cost.RegisterInvocation` once after every `Provider.Invoke`, then logs `Summary()` before `agent.Run()` returns.
- A real deployment would add a prometheus / OpenTelemetry exporter on top of the same ledger, but the concept is identical.

## Try It / 动手试一试

```bash
cd agents/s10-token-cost

# See the cost report for 5 fake invocations
GOWORK=off go run .

# 6 tests
GOWORK=off go test -v ./...
```

`GOWORK=off` because the root `go.work` doesn't list s10 yet; the module is self-contained.

Expected output (excerpt):

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

Test coverage:

- `TestRegistrationAccumulates` — two invocations on the same model: `History` len = 2, `Total` and `PerModel` agree.
- `TestCostComputation` — gpt-4o on 1000 in + 500 out should give exactly $0.0075 (hardcoded assertion: changing the pricing must change this test, on purpose).
- `TestCacheTTL` — Refresher calls Source once within TTL; after stepping the clock past TTL, the next Get refreshes.
- `TestUnknownModelReturnsZero` — unknown model: no error; cost=0; tokens still accumulate into Total.
- `TestEmbeddedPricingLoaded` — all four documented models look up to non-zero rates; Snapshot is a real copy.
- `TestSummaryIsDeterministic` (bonus) — `Summary()` is stable across map insertion orders (model keys are sorted).

## Upstream Source Reading / 上游源码阅读

Upstream's `browser_use/tokens/service.py` `TokenCost` class runs ~500 lines: the bulk is colored logging, Anthropic prompt-cache bucket math, HTTP fetch + disk TTL cache. s10 takes the load-bearing three: ledger + cost math + TTL.

```python
# Source: browser_use/tokens/service.py#L48-L120

class TokenCost:
    """Service for tracking token usage and calculating costs"""

    CACHE_DIR_NAME = 'browser_use/token_cost'
    CACHE_DURATION = timedelta(days=1)
    DEFAULT_PRICING_URL = 'https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json'

    def __init__(self, include_cost: bool = False, pricing_url: str | None = None):
        # ↓ Maps to our Go NewTokenCost. Upstream gates cost math on a
        #   feature flag; our Go version always computes because pricing
        #   is embedded and the lookup is O(1).
        self.include_cost = include_cost or os.getenv('BROWSER_USE_CALCULATE_COST', 'false').lower() == 'true'

        # ↓ Our History field.
        self.usage_history: list[TokenUsageEntry] = []

        # ↓ Upstream monkey-patches the .ainvoke method of every
        #   registered LLM to inject "after the call, write usage to
        #   history". Our Go version doesn't monkey-patch — callers
        #   call RegisterInvocation explicitly. Simpler and more
        #   honest: every side effect lives at the call site.
        self.registered_llms: dict[str, BaseChatModel] = {}

        # ↓ pricing is lazily loaded — first .initialize() call
        #   populates it. Our Go init() does eager parsing of the
        #   embedded JSON instead.
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
        return None                                   # ← unknown model: None, not raise

    # ↓ Upstream splits prompt_tokens into three buckets:
    #     (a) uncached = actual newly-sent tokens (input rate)
    #     (b) cached_read = Anthropic cache hits (cache_read rate)
    #     (c) cache_creation = writes to Anthropic cache (cache_creation rate)
    #   Our Go version has one bucket (InputTok). Anthropic prompt
    #   caching is a deliberate omission.
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
    # ↓ Note that add_usage and calculate_cost are separate methods.
    #   The latter is called only if include_cost=True. Our
    #   RegisterInvocation collapses both into one step — the pricing
    #   lookup is a map access (nanoseconds), no point splitting it.
    entry = TokenUsageEntry(
        model=model,
        timestamp=datetime.now(),
        usage=usage,
    )
    self.usage_history.append(entry)
    return entry
```

The full 60-100 line annotated extract lives at `upstream-readings/s10-token-cost.py`, including `_load_pricing_data` / `_find_valid_cache` — the disk-backed TTL cache path our in-memory `Refresher.fresh()` mirrors.
