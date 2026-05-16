---
title: "多模型接入指南"
slug: multi-model
est_read_min: 10
---

# 多模型接入指南

> learn-browser-use 的 s02 Provider 抽象设计是**provider-agnostic** 的——只要 LLM 服务暴露 OpenAI-compatible 接口（`/v1/chat/completions`），不用改一行代码就能切换。这一页列出我们测过的 8 个 profile + 配置方式。

---

## 心智模型

s02 的 `OpenAIProvider` 接受三个参数：

```go
type OpenAIProvider struct {
    APIKey  string
    BaseURL string  // 留空 → https://api.openai.com/v1
    Model   string  // 留空 → gpt-4o-mini
}
```

所有"OpenAI-compatible"的服务（DeepSeek、Moonshot、Qwen DashScope、Groq、OpenRouter、自托管 vLLM/SGLang…）都只需要换 `BaseURL` + `Model` + `APIKey`。完整国产 + 海外 + 自托管覆盖。

s12 的 `Agent` 同理：把 `Provider` 字段从 `MockProvider` 换成 `OpenAIProvider` 加上对应 profile 即可。

---

## 8 个内置 profile

| Provider | BaseURL | 默认 Model | API Key env var |
|---|---|---|---|
| `openai` | `https://api.openai.com/v1` | `gpt-4o-mini` | `OPENAI_API_KEY` |
| `deepseek` | `https://api.deepseek.com/v1` | `deepseek-chat` | `DEEPSEEK_API_KEY` |
| `moonshot` | `https://api.moonshot.cn/v1` | `moonshot-v1-8k` | `MOONSHOT_API_KEY` |
| `qwen` | `https://dashscope.aliyuncs.com/compatible-mode/v1` | `qwen-plus` | `DASHSCOPE_API_KEY` |
| `groq` | `https://api.groq.com/openai/v1` | `llama-3.3-70b-versatile` | `GROQ_API_KEY` |
| `openrouter` | `https://openrouter.ai/api/v1` | `openai/gpt-4o-mini` | `OPENROUTER_API_KEY` |
| `local` (vLLM/SGLang/Ollama) | `http://localhost:8000/v1` | `local-model` | `OPENAI_API_KEY`（占位即可） |
| `anthropic` | （需要适配层，见下） | `claude-sonnet-4-5` | `ANTHROPIC_API_KEY` |

---

## 在 s02 / s12 里切换

### 方法 A：直接改代码

```go
// s02/main.go 或 s12/main.go 里
provider := &OpenAIProvider{
    APIKey:  os.Getenv("DEEPSEEK_API_KEY"),
    BaseURL: "https://api.deepseek.com/v1",
    Model:   "deepseek-chat",
}
```

### 方法 B：env 变量驱动

把 `main.go` 里 hard-code 的 BaseURL/Model 改成读 env：

```go
provider := &OpenAIProvider{
    APIKey:  os.Getenv("LLM_API_KEY"),
    BaseURL: os.Getenv("LLM_BASE_URL"),
    Model:   os.Getenv("LLM_MODEL"),
}
```

然后跑：

```bash
LLM_API_KEY=sk-... LLM_BASE_URL=https://api.deepseek.com/v1 LLM_MODEL=deepseek-chat \
  go run ./agents/s02-llm-provider "search hacker news"
```

---

## Anthropic（需要适配层）

Anthropic Claude 不走 `/v1/chat/completions`，它的端点是 `/v1/messages`，消息结构也不同（`tool_use` block 是顶级，不是嵌进 `tool_calls`）。我们在 s02 没附 Anthropic provider 是为了让 OpenAI HTTP 实现保持最小；要加可以参考下面的 sketch：

```go
// anthropic_provider.go (新增到 s02 或 s12)
type AnthropicProvider struct {
    APIKey string
    Model  string
}

func (p *AnthropicProvider) Invoke(ctx context.Context, msgs []Message, tools []ToolSchema) (Response, error) {
    // 1. Translate our Message → Anthropic messages array
    //    - role "tool" → role "user" with content_block type=tool_result
    //    - ContentBlock{Type: "tool_use"} → unchanged (Anthropic uses same shape)
    //    - System prompt is a top-level "system" field, not a message
    // 2. POST https://api.anthropic.com/v1/messages with header x-api-key + anthropic-version: 2023-06-01
    // 3. Translate Anthropic response.content[] → our Response.Actions
    // ...
}
```

上游 browser-use 的 `browser_use/llm/anthropic/chat.py` 是完整版可作参考（MIT license）。

---

## 国内用户推荐路径

如果你在国内 + 没有海外卡：

1. **DeepSeek**：`deepseek-chat` 便宜、推理强，全程跑 s02-s12 demo 单次成本 < ¥0.01。
2. **Qwen via DashScope**：阿里云直连，`qwen-plus` 速度快、tool use 支持好。
3. **Moonshot (Kimi)**：128K 上下文，适合 s03 message-manager 演示长对话。

避免 OpenAI 直连（需要海外卡 + 网络）；避免 Anthropic 直连（需要海外卡 + IP 限制）。

---

## 故障排查

| 症状 | 可能原因 |
|---|---|
| `401 Unauthorized` | API key 错或前缀少了 `Bearer ` |
| `404 Not Found on /chat/completions` | BaseURL 末尾多了 `/` 或缺了 `/v1` |
| `model not found` | 该 provider 不支持你传的 model id —— 检查 provider 文档 |
| `tool_calls field unknown` | provider 不支持 OpenAI function calling（罕见，主流都支持） |
| 国内出现网络超时 | 用国内 provider（deepseek / qwen / moonshot） |

---

## 上游对照

browser-use 上游有 16 个 provider impl（每个一个文件）：
- `browser_use/llm/openai/chat.py`
- `browser_use/llm/anthropic/chat.py`
- `browser_use/llm/google/chat.py`
- `browser_use/llm/groq/chat.py`
- `browser_use/llm/mistral/chat.py`
- `browser_use/llm/cerebras/chat.py`
- `browser_use/llm/deepseek/chat.py`
- `browser_use/llm/ollama/chat.py`
- `browser_use/llm/litellm/chat.py`
- `browser_use/llm/oci_raw/chat.py`
- `browser_use/llm/azure/chat.py`
- `browser_use/llm/openrouter/chat.py`
- `browser_use/llm/vercel/chat.py`
- `browser_use/llm/aws/chat.py`
- `browser_use/llm/browser_use/chat.py` (browser-use 自家云模型)

我们用一个 OpenAI HTTP provider + BaseURL/Model 参数就够覆盖 11/16，剩下 5 个是 Anthropic / Google / AWS Bedrock / Vercel / browser-use cloud — 各需要少量适配代码。
