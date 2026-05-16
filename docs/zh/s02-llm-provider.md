---
title: "s02 · LLM Provider 抽象"
chapter: 2
slug: s02-llm-provider
est_read_min: 14
---

# s02 · LLM Provider 抽象

> 教什么：s01 的 `FakeProvider` 用 if/else 假装是 LLM，只能跑 demo。本节把"LLM"从概念变成接口，配两个实现：纯 `net/http` 直连 OpenAI 的真实客户端，以及测试用的确定性 `MockProvider`。重点是**接口形状一旦定下来，后面 10 节都不用改循环**。

---

## Problem / 问题

s01 跑通了 `loop(observe → think → act)`，但"think"那一步是个硬编码 `FakeProvider`：扫一遍 task 里有没有"search"/"navigate"，按关键字硬选 action。这种 thing 拿来教骨架很好，但稍微复杂一点的 task（"在 GitHub 上查最近 7 天 star 涨幅最快的仓库"）就完全无能为力——它不会写 query、不会理解上下文、不会判断"已经搜过 3 次还没找到答案"。

把它替换成真的 LLM 之前，要先回答 3 个问题：

1. **接口形状是什么？** OpenAI 和 Anthropic 的 API 不一样，未来还要支持 Google、Groq、Ollama……不可能每加一家就改一遍 agent 循环。需要一个跨 provider 的统一形状。
2. **怎么测？** 真 LLM 请求要钱、要 key、不确定（同一 prompt 跑两次结果不一样）。CI 里跑测试不能依赖真 API。
3. **怎么处理 429 / 网络抖动？** 真 API 偶尔会限流，简单一发请求就崩对生产不友好。

s02 的目标：定义 `Provider` 接口 + 给两个实现（OpenAI 真实 + Mock 测试），用 `httptest` 桩出 OpenAI 行为来跑全部 6 个单元测试。

## Solution / 解决方案

把 s01 的 `FakeProvider` 拆成接口 + 实现：

1. **`Provider` 接口**（`provider.go`）：单方法 `Invoke(ctx, msgs []Message, tools []ToolSchema) (Response, error)`。所有 provider 都实现这个形状。`ToolSchema` 把 JSON Schema 当做 `json.RawMessage` 直接塞，调用者手写最干净。
2. **`OpenAIProvider`**（`openai_provider.go`）：用 `net/http` + `encoding/json` 直接 POST `/chat/completions`，把响应里的 `tool_calls` 数组转成 `[]ActionCall`，把 `finish_reason` 映射回我们的 `StopReason`（`stop` → `end_turn`、`tool_calls` → `tool_use`、`length` → `length`）。**零 SDK 依赖**——OpenAI 的协议就那么几个字段，纯标准库手写一遍把每个字节都摊在阳光下。
3. **`MockProvider`**（`mock_provider.go`）：内部 `Queue []Response`，调一次出一个，队列耗尽返回 error（不是 nil、不是 panic——错就当场显出来）。`Reset()` 让 table-driven 测试可以复用。

关键决策：

1. **`ToolSchema.Parameters` 用 `json.RawMessage` 而不是 `any`/`map[string]any`**：让调用者手写严格的 JSON Schema，不用过 Go 反射。s04 会把这条路反过来走（struct tag → JSON Schema 自动生成）。
2. **`Response` 在 s01 基础上扩了三个字段**：`InputTokens`、`OutputTokens`、`Model`（server 解析后的版本号，比如客户端写 `gpt-4o-mini`、服务器返回 `gpt-4o-mini-2024-07-18`）。这些字段 FakeProvider 拿不到，只有真 API 在响应里给。s10 的 token cost tracking 直接复用这些字段。
3. **429 重试用 `time.NewTimer` + `<-ctx.Done()` 双 select**：而不是 `time.Sleep(1*time.Second)`。这样调用方 cancel 掉 ctx 时，sleep 立刻退出，不等满 1 秒——是测试能在 100ms 内跑完的关键。
4. **wire-format 类型全部 unexported**：`openAIRequest`、`openAIMessage`、`openAIToolCall` ……都是 OpenAI 一家的实现细节，不该泄漏到 `Provider` 接口里。Anthropic 进来的时候会有自己一套 unexported 类型。

## How It Works / 工作原理

```
       Agent.Run                           OpenAIProvider.Invoke
       (s01 已写)                          (s02 本节)
        │                                  │
        │ Provider.Invoke(ctx, msgs, tools)│
        ├──────────────────────────────────┤
        │                                  │ 1. buildRequestBody
        │                                  │    convertMessage:
        │                                  │    - system/user  → 1 wire msg
        │                                  │    - assistant    → 1 msg + tool_calls[]
        │                                  │    - tool         → N msgs, 每个一个 tool_call_id
        │                                  │
        │                                  │ 2. http.NewRequestWithContext(POST)
        │                                  │    Authorization: Bearer $KEY
        │                                  │    Content-Type:  application/json
        │                                  │    Body: {model, messages, tools, temperature}
        │                                  │
        │                                  │ 3. client.Do(req)
        │                                  │    if 429 && retries left:
        │                                  │      sleepCtx(retryDelay); continue
        │                                  │    if other 4xx/5xx:
        │                                  │      return err with body
        │                                  │
        │                                  │ 4. parseResponse
        │                                  │    choices[0].message.content    → Response.Text
        │                                  │    choices[0].message.tool_calls → []ActionCall
        │                                  │    finish_reason → StopReason
        │                                  │    usage → InputTokens / OutputTokens
        │                                  │
        │ Response{Text, Actions, StopReason, InputTokens, OutputTokens, Model}
        │◄─────────────────────────────────┤
```

核心 50 行（节选自 `agents/s02-llm-provider/openai_provider.go`）：

```go
func (p *OpenAIProvider) Invoke(ctx context.Context, msgs []Message, tools []ToolSchema) (Response, error) {
    url := p.BaseURL
    if url == "" { url = defaultBaseURL }
    url = url + "/chat/completions"

    body, err := p.buildRequestBody(msgs, tools)
    if err != nil { return Response{}, fmt.Errorf("build request: %w", err) }

    client := p.HTTPClient
    if client == nil { client = &http.Client{Timeout: 60 * time.Second} }

    maxRetries := p.MaxRetries
    if maxRetries == 0 { maxRetries = 1 }
    retryDelay := p.RetryDelay
    if retryDelay == 0 { retryDelay = 1 * time.Second }

    var lastErr error
    for attempt := 0; attempt <= maxRetries; attempt++ {
        respBody, status, err := p.doRequest(ctx, client, url, body)
        if err != nil {
            lastErr = err
            if attempt < maxRetries {
                if sleepErr := sleepCtx(ctx, retryDelay); sleepErr != nil {
                    return Response{}, sleepErr
                }
                continue
            }
            return Response{}, lastErr
        }
        if status == http.StatusTooManyRequests && attempt < maxRetries {
            if sleepErr := sleepCtx(ctx, retryDelay); sleepErr != nil {
                return Response{}, sleepErr
            }
            continue
        }
        if status < 200 || status >= 300 {
            return Response{}, fmt.Errorf("openai http %d: %s", status, string(respBody))
        }
        return parseResponse(respBody)
    }
    return Response{}, lastErr
}
```

**4 个非显然之处**：

1. **assistant 消息里有 `tool_use` 时必须连同 `tool_calls[]` 一起发送，且每个 `tool_call` 要有稳定的 `id`**：OpenAI 用 `tool_call_id` 把后续 `role:"tool"` 的结果消息和前面的 tool_calls 配对。我们用 `call_<idx>` 编号（见 `convertMessage`），简化但够用。
2. **`Temperature *float64` 不是 `float64`**：零值 0 是合法采样参数（贪心模式），不能用零值判断"未设置"。指针是 Go 里表达 optional 数值最干净的方式。
3. **`sleepCtx` 不是 `time.Sleep`**：retry 期间 `ctx` 被 cancel 时要立刻退出。测试里把 `RetryDelay` 设到 10ms，但更重要的是 cancel 立即生效——否则一个超时 5 秒的请求挂在 429 backoff 里，5+1=6 秒才退。
4. **`openAIMessage` 是 unexported**：暴露出去就锁死了 wire format 的形状，将来加 Anthropic 时要被迫做适配层。藏起来后，wire types 是一家 provider 的局部 detail。

## What Changed / 与上一节的变化

```diff
- // s01: FakeProvider 是具体类型，循环代码硬依赖它
- type Agent struct {
-     Provider *FakeProvider   // ← 具体类型，无法替换
-     ...
- }

+ // s02: Provider 升级为接口，循环对接口编程
+ type Provider interface {
+     Invoke(ctx context.Context, msgs []Message, tools []ToolSchema) (Response, error)
+ }
+
+ // 两个实现都满足接口:
+ var _ Provider = (*MockProvider)(nil)
+ var _ Provider = (*OpenAIProvider)(nil)
```

`Response` 也长了三个新字段：

```diff
  type Response struct {
      Text       string
      Actions    []ActionCall
      StopReason string
+     InputTokens  int   // prompt tokens (s10 cost tracking 复用)
+     OutputTokens int   // completion tokens
+     Model        string // server-resolved model id
  }
```

新增 `ToolSchema` 类型：把"工具目录"和"消息历史"分开传——大多数 LLM API 要求 `tools` 走顶层字段，混进消息里会被拒。

## Try It / 动手试一试

```bash
cd agents/s02-llm-provider

# Mock 模式（无需 API key，确定性输出）
go run . -mock "search hacker news"

# 真 OpenAI（需要 $OPENAI_API_KEY）
OPENAI_API_KEY=sk-... go run . "what is the capital of France?"
OPENAI_API_KEY=sk-... go run . -model gpt-4o-mini "navigate https://example.com"

# 兼容 OpenAI 协议的本地服务（Ollama、vLLM、LM Studio）
OPENAI_API_KEY=ignored go run . -base-url http://localhost:11434/v1 "hello"

# 全部 6 个测试（用 httptest 桩 OpenAI，无真网络）
go test -v ./...
```

期望输出形态：

```
$ go run . -mock "search hacker news"
model: mock-llm
stop_reason: tool_use
tokens: in=12 out=8
text: I will search for "search hacker news".
action[0]: search({"query":"search hacker news"})
```

由于 MockProvider 是确定性的，输出**逐字节可复现**。真 OpenAI 的输出会因模型随机性变化，但字段形状一致。

## Upstream Source Reading / 上游源码阅读

上游 `browser_use/llm/openai/chat.py` 共 306 行（用 `wc -l` 可验）。下面是 `ainvoke()` 主流程（第 152-222 行），加上若干 dataclass 字段，对应我们 Go 版 ~250 行 `openai_provider.go`：

```python
# Source: browser_use/llm/openai/chat.py#L24-L222
# License: MIT (Copyright 2024 Gregor Zunic)
@dataclass
class ChatOpenAI(BaseChatModel):
    """A wrapper around AsyncOpenAI that implements the BaseLLM protocol."""

    # 在 Go 版我们只保留 Model/APIKey/BaseURL 这些必需字段。
    # 上游的 frequency_penalty=0.3 是为绕过 4.1-mini "无限输出 \t" 的 bug。
    model: ChatModel | str
    temperature: float | None = 0.2
    frequency_penalty: float | None = 0.3
    reasoning_effort: ReasoningEffort = 'low'
    api_key: str | None = None
    base_url: str | None = None
    timeout: float | None = None
    max_retries: int = 5  # SDK 内部重试;我们 Go 版手写。

    # 推理模型(o-series, gpt-5)拒收 temperature/frequency_penalty,
    # 改用 reasoning_effort。Go 版未实现——出范围。
    reasoning_models: list[str] | None = field(default_factory=lambda: ['o3', 'o3-mini', 'gpt-5'])

    async def ainvoke(self, messages, output_format=None, **kwargs):
        # Phase 1: 把我们的 Message 转成 OpenAI wire format。
        # 对应 Go 版 convertMessage()——拆 assistant turn 里的 tool_use,
        # 每个 tool_result 单独成一条 role:"tool" 消息。
        openai_messages = OpenAIMessageSerializer.serialize_messages(messages)

        try:
            # Phase 2: 按需挂载 model_params。原则:只塞已设置的字段。
            model_params: dict[str, Any] = {}
            if self.temperature is not None: model_params['temperature'] = self.temperature
            if self.frequency_penalty is not None: model_params['frequency_penalty'] = self.frequency_penalty

            # 模型 hack:推理模型走另一套参数。
            if self.reasoning_models and any(m in str(self.model).lower() for m in self.reasoning_models):
                model_params['reasoning_effort'] = self.reasoning_effort
                model_params.pop('temperature', None)
                model_params.pop('frequency_penalty', None)

            # Phase 3: dispatch。output_format=None 分支对应我们 Go 的全部 Invoke()。
            if output_format is None:
                response = await self.get_client().chat.completions.create(
                    model=self.model, messages=openai_messages, **model_params,
                )
                choice = response.choices[0] if response.choices else None
                if choice is None:
                    # 防御:第三方 proxy 偶尔返回空 choices。
                    raise ModelProviderError(message='no choices', status_code=502)
                return ChatInvokeCompletion(
                    completion=choice.message.content or '',
                    usage=self._get_usage(response),
                    stop_reason=choice.finish_reason,
                )
            # ... else 分支(结构化输出)我们 s04 才做。

        # Phase 4: 错误归类。SDK 异常映射成统一的 ModelProviderError 系列。
        except RateLimitError as e:
            raise ModelRateLimitError(message=e.message) from e
        except APIConnectionError as e:
            raise ModelProviderError(message=str(e)) from e
        except APIStatusError as e:
            raise ModelProviderError(message=e.message, status_code=e.status_code) from e
```

**对照阅读要点**：

- **`BaseChatModel` 是 Protocol，不是基类**：见 `browser_use/llm/base.py#L17-L60`。Python Protocol 等同 Go interface 的鸭子类型。把这个形状映射到 Go interface 几乎是逐行翻译。
- **`OpenAIMessageSerializer` 是 OpenAI 专属**：上游每家 provider 都有自己的 serializer。Anthropic 进来时（Phase G）会有 `anthropicSerializer`，跟我们 Go 版 `convertMessage` 平行存在。
- **重试藏在 SDK 里**：上游 `max_retries=5` 全在 `AsyncOpenAI` 内部跑。我们手写 retry-on-429 更容易读、更难写对——比如还没尊重 `Retry-After` header，上游 SDK 是有的。
- **推理模型分支 = 协议泄漏**：`if any(m in self.model for m in reasoning_models)` 是脆弱的名字匹配。任何真实的 provider 抽象都会随时间长出 N 个这种特例分支。Go 版我们故意没实现，留作"接口能不能优雅地容纳特例"的开放题。
- **`output_format` 用 `@overload` 给类型检查器精准签名**：Go 没有等价物，要么走泛型（`Invoke[T any]`），要么单独开 `InvokeStructured` 方法。我们 s04 做这个决策。
- **`finish_reason` 不止 stop/length/tool_calls 三种**：API 实际还有 `function_call`（legacy）、`content_filter`（安全过滤）、`max_tokens` 别名。我们 Go 版 `parseResponse` 的 default 分支会原样回传，loop 层负责处理。

**想读更多**：从 `browser_use/llm/base.py#L17-L60` 入手看 Protocol，然后跳进 `browser_use/llm/openai/chat.py` 看 OpenAI 实现，最后对比 `browser_use/llm/anthropic/chat.py` 看 Anthropic 实现差异——这条线就是后面 Phase G 多 provider 支持的代码地图。

---

**下一节预告**：s03 把无限增长的 `[]Message` 升级成 `MessageManager`——支持压缩老 turn、保留最近 N 条、敏感数据屏蔽。s02 的 `Provider.Invoke(ctx, msgs, tools)` 形状原封不动，循环代码继续不动。
