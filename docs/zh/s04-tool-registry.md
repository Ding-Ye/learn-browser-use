---
title: "s04 · 工具注册表"
chapter: 4
slug: s04-tool-registry
est_read_min: 14
---

# s04 · 工具注册表

> 教什么：s01 的 `byName := map[string]Action{...}` 在循环里被现做现用，加一个工具就要改一处代码；上游 `browser_use/tools/` 把这件事抽出来变成可注册、可注解、可超时的 `Registry`。本节用 ~450 行 Go 把 Registry + 反射版 JSON Schema 生成 + Dispatcher 超时守卫做出来。

---

## Problem / 问题

s01 的 `Agent.Run` 里有这么一段：

```go
byName := map[string]Action{}
for _, act := range a.Actions {
    byName[act.Name()] = act
}
// ... 后面用 byName[ac.Name] 查找
```

只有三个 action 时这毫无问题；可一旦：

- 工具数量到了 ~20 个（browser-use 上游 `tools/service.py` 一共注册 30+ 个 action），
- 每个工具的入参类型不一样（`click(index int)` vs `extract(query string, schema dict)` vs `done(text string, success bool)`），
- LLM 需要在 prompt 里看到每个工具的 **JSON Schema** 才能正确发起 `tool_use`，
- 每次执行都得有"卡死 → 超时 → 报错"兜底（上游默认 180s），

——这套需求就把"循环内的临时 map"逼成了一个独立的子系统。

更要命的是 schema：手写 JSON Schema 字符串一定会和真实的 Go struct 漂移。上游用 pydantic 的 `model_json_schema()`，我们要在 Go 里用反射做等价的事。

s04 解决三件事：

1. **谁来管"已注册的工具"**？ → `Registry`。
2. **谁来产出工具的 JSON Schema**？ → `SchemaFromStruct`，从带 `json` + `desc` tag 的 struct 反射出来。
3. **谁来跑工具且不会卡死**？ → `Dispatcher`，每次 `Act` 都包一层 `context.WithTimeout`。

## Solution / 解决方案

把 s01 那块循环内的 map 上提为三个组件：

| 角色 | 类型 | 上游对照 |
|---|---|---|
| 工具单体 | `Tool` interface | `browser_use/tools/registry/views.py::RegisteredAction` |
| 工具集合 | `Registry` struct | `browser_use/tools/registry/service.py::Registry` |
| 调度执行 | `Dispatcher` struct | `browser_use/tools/service.py::Tools.act()` |

`Tool` interface 一共 4 个方法，照着工具的"生命周期"切：

```go
type Tool interface {
    Name() string                                                  // 注册键
    Description() string                                           // 给 LLM 的一句话
    Schema() ToolSchema                                            // 给 LLM 的入参 schema
    Run(ctx context.Context, input json.RawMessage) (string, error) // 执行
}
```

`Registry` 用 `map[string]Tool` 做存储，但暴露三个方法时都保证**确定性顺序**：

```go
func (r *Registry) Register(t Tool) error
func (r *Registry) Lookup(name string) (Tool, bool)
func (r *Registry) All() []Tool      // 按 Name 排序
func (r *Registry) Schemas() []ToolSchema
```

`Dispatcher` 是个 5 行字段的小 struct，但承担"全 agent 共享的 action 超时策略"：

```go
type Dispatcher struct {
    Registry *Registry
    Timeout  time.Duration // 0 ⇒ DefaultTimeout (180s)
}
func (d *Dispatcher) Act(ctx context.Context, call ActionCall) (ContentBlock, error)
```

`SchemaFromStruct(zero interface{}) json.RawMessage` 是这一节最有 Go 味道的部分：通过 `reflect.Type` 遍历字段，按 Kind 派发出 JSON Schema 基础类型 (`string`/`integer`/`number`/`boolean`/`array`/`object`)，并读 struct tag：

- `json:"foo"` → schema 里的字段名
- `json:"foo,omitempty"` → 不进 `required`
- `desc:"..."` → 进 schema 字段的 `description`

## How It Works / 工作原理

```
┌────────────────────────────────────────────────────────────────────┐
│                            startup                                 │
│                                                                    │
│   reg := NewRegistry()                                             │
│   reg.MustRegister(SearchTool{})  ──┐                              │
│   reg.MustRegister(TypeTool{})    ──┼─ tools map[name]→Tool        │
│   reg.MustRegister(ScrollTool{})  ──┘                              │
│                                                                    │
│   reg.Schemas() ────────────────────────────► []ToolSchema         │
│         │                          ▲                               │
│         │                          │                               │
│         │           (per-tool Schema() reflects struct tags)       │
│         │                                                          │
│         └──────────── handed to Provider/LLM in s12                │
│                                                                    │
├────────────────────────────────────────────────────────────────────┤
│                            run loop                                │
│                                                                    │
│   LLM picks action ──► ActionCall{Name, Input(JSON)} ──────┐       │
│                                                            ▼       │
│                                            ┌────────────────────┐  │
│                                            │  Dispatcher.Act    │  │
│                                            │  ┌──────────────┐  │  │
│                                            │  │ Lookup(name) │  │  │
│                                            │  └──────┬───────┘  │  │
│                                            │         ▼          │  │
│                                            │  ctx, cancel :=    │  │
│                                            │  WithTimeout(...)  │  │
│                                            │         │          │  │
│                                            │         ▼          │  │
│                                            │  tool.Run(ctx,raw) │  │
│                                            │         │          │  │
│                                            │         ▼          │  │
│                                            │  ContentBlock{     │  │
│                                            │    Type: "tool_     │  │
│                                            │    result", ... }  │  │
│                                            └────────────────────┘  │
└────────────────────────────────────────────────────────────────────┘
```

核心代码（约 50 行）：

```go
// registry.go
func (r *Registry) Register(t Tool) error {
    name := t.Name()
    if name == "" {
        return fmt.Errorf("registry: empty Name()")
    }
    if _, exists := r.tools[name]; exists {
        return fmt.Errorf("registry: tool %q already registered", name)
    }
    r.tools[name] = t
    return nil
}

func (r *Registry) Schemas() []ToolSchema {
    out := make([]ToolSchema, 0, len(r.tools))
    for _, t := range r.All() { // All() 是按 name 排序的
        out = append(out, t.Schema())
    }
    return out
}

// schema_gen.go (核心循环)
for i := 0; i < t.NumField(); i++ {
    f := t.Field(i)
    if !f.IsExported() { continue }
    name, omitempty := jsonFieldName(f)
    fieldSchema := buildFieldSchema(f.Type)
    if desc, ok := f.Tag.Lookup("desc"); ok && desc != "" {
        fieldSchema["description"] = desc
    }
    props[name] = fieldSchema
    if !omitempty { required = append(required, name) }
}

// dispatcher.go
func (d *Dispatcher) Act(ctx context.Context, call ActionCall) (ContentBlock, error) {
    tool, ok := d.Registry.Lookup(call.Name)
    if !ok {
        msg := fmt.Sprintf("unknown action %q", call.Name)
        return ContentBlock{Type: "tool_result", Name: call.Name, Result: msg},
            fmt.Errorf("%s", msg)
    }
    runCtx, cancel := context.WithTimeout(ctx, d.timeout())
    defer cancel()

    raw := json.RawMessage(call.Input)
    if len(raw) == 0 { raw = json.RawMessage("{}") }
    out, err := tool.Run(runCtx, raw)
    if err != nil { /* 翻译 timeout, 包装 tool_result */ }
    return ContentBlock{Type: "tool_result", Name: call.Name, Result: out}, nil
}
```

**4 个非显然之处**：

1. **为什么 `desc` 放在 struct tag，而不是在 `Tool.Description()` 里写一大段**？因为 LLM 看的 schema 必须有 **per-field 描述** 才能区分 `index` 是元素索引还是分页索引。把描述放在字段旁边，rename 安全；gofmt 不会拆散；tests 也能 grep。
2. **为什么 Dispatcher 用 `context.WithTimeout` 而不是 `time.After` + select**？因为真实的工具内部会自己再调 HTTP、CDP、文件 IO——这些库都接 `context.Context`。父 ctx 一被 cancel，全链路自动短路；如果用 `time.After` 在外面 race，inner goroutine 会泄漏。
3. **`Schemas()` 强制按字母排序**。Go 的 map 迭代是 randomized。如果不排序，每次 `go run .` 输出顺序都变，prompts 也不再可复现，testdata 直接全废。
4. **dispatcher 永远返回 ContentBlock，即使是错**：上层（s12 的 Agent 循环）需要把"tool 不存在 / tool 报错 / tool 超时"都喂回 LLM——让模型自己看错误自己改。如果 Dispatcher 在 unknown action 时只 return error 而不返回 block，循环还得自己 re-synthesize 一个 tool_result，逻辑就分裂了。

## What Changed / 与上一节的变化

s01-s03 都用 `[]Action` + 临时 `byName` map：

```diff
- // s01-s03 风格
- byName := map[string]Action{}
- for _, act := range a.Actions { byName[act.Name()] = act }
- for {
-     resp, _ := a.Provider.Invoke(ctx, msgs)
-     for _, ac := range resp.Actions {
-         tool, ok := byName[ac.Name]
-         if !ok {
-             results = append(results, ContentBlock{Type: "tool_result", Result: "unknown"})
-             continue
-         }
-         out, _ := tool.Run(ctx, ac.Input)   // 没有超时
-         // 没有 schema 给 LLM
-         results = append(results, ContentBlock{Type: "tool_result", Result: out})
-     }
- }
```

s04 之后：

```diff
+ reg := NewRegistry()
+ reg.MustRegister(SearchTool{})
+ reg.MustRegister(TypeTool{})
+ reg.MustRegister(ScrollTool{})
+
+ schemas := reg.Schemas()                 // 自动从 struct tag 反射出来
+ // ... 把 schemas 喂给 Provider，告诉 LLM 它有哪些工具
+
+ d := &Dispatcher{Registry: reg, Timeout: 180 * time.Second}
+ block, err := d.Act(ctx, call)           // 一行执行 + 超时守卫 + 错误归一
```

关键性增量：**LLM 第一次拿得到"工具菜单"**。s02/s03 只能"告诉 LLM 你有这些 action 名"，s04 之后能"告诉 LLM 这些 action 的入参形状"。这一步是从"约定俗成的 prompt 工程"到"schema-driven tool use"的分水岭。

## Try It / 动手试一试

```bash
cd agents/s04-tool-registry

# 看自动生成的 schemas + 一个示例 dispatch
go run .

# 6 个测试
go test -v ./...
```

期望输出（节选）：

```
# registered tools and their auto-generated schemas

{
  "name": "scroll",
  "description": "Scroll the page in the given direction.",
  "parameters": {
    "properties": {
      "direction": {
        "description": "scroll direction: up | down | top | bottom",
        "type": "string"
      }
    },
    "required": ["direction"],
    "type": "object"
  }
}
...
# example dispatch
call: search({"query":"hacker news"})
result: top 3 hits for "hacker news": example.com/hacker news, ...
```

测试覆盖：

- `TestRegisterAndLookup` — 注册/查找的 round-trip + 重复注册被拒绝 + All() 按字母排序。
- `TestSchemaGenerationFromStruct` — string / int / bool 三种 Kind 反射出对的 JSON Schema 类型。
- `TestSchemaGenerationWithTags` — `desc:"..."` 进 description；`json:"...,omitempty"` 不进 required。
- `TestDispatchTimeoutFires` — 一个 sleep 1s 的工具在 100ms 超时下 100ms 内返错。
- `TestDispatchUnknownActionReturnsError` — 未知工具名给出清晰错误 + tool_result block。
- `TestDispatchHappyPath` — 正常调用返回 tool_result，Result 包含 query 回显。

## Upstream Source Reading / 上游源码阅读

上游 `browser_use/tools/registry/service.py` 第 32-326 行是真实 Registry。下面的节选展示 `Registry.action()` 装饰器与 `execute_action()` 入口——这是我们 Go `Register` + `Dispatcher.Act` 的 1:1 对应。

```python
# Source: browser_use/tools/registry/service.py#L32-L75
# License: MIT

class Registry(Generic[Context]):
    """Service for registering and managing actions"""

    def __init__(self, exclude_actions: list[str] | None = None):
        self.registry = ActionRegistry()
        self.telemetry = ProductTelemetry()
        # Create a new list to avoid mutable default argument issues
        self.exclude_actions = list(exclude_actions) if exclude_actions is not None else []

    def exclude_action(self, action_name: str) -> None:
        """Exclude an action from the registry after initialization."""
        if action_name not in self.exclude_actions:
            self.exclude_actions.append(action_name)
        if action_name in self.registry.actions:
            del self.registry.actions[action_name]
            logger.debug(f'Excluded action "{action_name}" from registry')

    def _get_special_param_types(self) -> dict[str, type | UnionType | None]:
        """Get the expected types for special parameters from SpecialActionParameters."""
        # 这些"特殊参数"是 agent 在每次调用前注入给 tool 的：
        # browser_session, page_url, cdp_client, file_system 等。
        # 我们 s04 不做这层注入，s07+ 加了 BrowserSession 之后再扩展 Dispatcher。
        return {
            'context': None,
            'browser_session': BrowserSession,
            'page_url': str,
            'cdp_client': None,
            'page_extraction_llm': BaseChatModel,
            'available_file_paths': list,
            'has_sensitive_data': bool,
            'file_system': FileSystem,
            'extraction_schema': None,
        }
```

```python
# Source: browser_use/tools/registry/service.py#L290-L326

def action(
    self,
    description: str,
    param_model: type[BaseModel] | None = None,
    domains: list[str] | None = None,
    allowed_domains: list[str] | None = None,
    terminates_sequence: bool = False,
):
    """Decorator for registering actions"""

    def decorator(func: Callable):
        if func.__name__ in self.exclude_actions:
            return func

        # Normalize the function signature → 把任意签名 wrap 成 (params, **special)。
        normalized_func, actual_param_model = self._normalize_action_function_signature(
            func, description, param_model
        )

        action = RegisteredAction(
            name=func.__name__,
            description=description,
            function=normalized_func,
            param_model=actual_param_model,        # ← pydantic model，相当于我们的 ToolSchema.Parameters
            domains=final_domains,
            terminates_sequence=terminates_sequence,
        )
        self.registry.actions[func.__name__] = action
        return normalized_func

    return decorator
```

```python
# Source: browser_use/tools/registry/service.py#L328-L395 (节选)

async def execute_action(
    self,
    action_name: str,
    params: dict,
    browser_session: BrowserSession | None = None,
    ...
) -> Any:
    if action_name not in self.registry.actions:
        raise ValueError(f'Action {action_name} not found')   # ← 我们的 unknown action 错

    action = self.registry.actions[action_name]
    try:
        validated_params = action.param_model(**params)        # ← 等价于我们 json.Unmarshal 进 typed struct
    except Exception as e:
        raise ValueError(f'Invalid parameters {params} for action {action_name}: {e}') from e

    special_context = {
        'browser_session': browser_session,
        'page_extraction_llm': page_extraction_llm,
        'file_system': file_system,
        ...
    }
    return await action.function(params=validated_params, **special_context)
```

**对照阅读要点**：

1. **`@action(description=...)` 装饰器 ↔ `Tool` 接口**。Python 的装饰器在 import 时跑，inspect 函数签名生成 pydantic model；Go 没有装饰器，所以我们把"生成 schema"的责任交给 `Tool.Schema()` 方法，每个 tool 自己写一行 `SchemaFromStruct(myArgs{})` 就行。等价但更显式。
2. **`_normalize_action_function_signature` 那 200 行**做的是"把 `def click(index: int, browser_session: BrowserSession) -> None` 改写成 `def click(*, params: ClickParams, browser_session=None)`"——也就是把"位置参数"统一成"关键字参数 + 特殊参数注入"。s04 还没有 special params；我们把这一层 normalization 推到 s07 引入 BrowserSession 时再做。
3. **`param_model = create_model(...)` 是 pydantic 的运行时建模**——本质就是反射函数签名生成一个 JSON Schema 兼容的类型。我们的 Go `SchemaFromStruct` 是它的反射版静态对应：编译期 struct 已经存在，运行期只需要 walk 它。
4. **超时在哪儿？** 上游 Registry 自己不管超时，超时在 `browser_use/tools/service.py::Tools.act()` 的 `_DEFAULT_ACTION_TIMEOUT_S = 180.0` 兜底 +`_coerce_valid_action_timeout` 防御性校验。我们 s04 直接把超时合并进 Dispatcher，简化层级。
5. **`terminates_sequence=True`** 在 LLM 一次性返回多个 action 时表示"跑到这一个就停"——典型例子是 `done` 工具。s04 还没引入多 action 序列，留待 s12。
6. **special_context 那一坨**（browser_session / file_system / cdp_client / sensitive_data ...）是真实 agent 跑通的关键基础设施——s04 故意为零，确保这一节只讲 Registry 本身。后续 s07/s11/s12 会把它们一个一个加回 Dispatcher。

**想读更多**：从 `browser_use/tools/registry/service.py::Registry.action()` 入手，跟着 `_normalize_action_function_signature` 看清"如何把任意签名归一成 kwargs-only"；再跳进 `browser_use/tools/service.py` 看 `Tools.act()` 怎么把超时、telemetry、错误统一起来。这条线就是 s04 → s07 → s12 的真实代码地图。

---

**下一节预告**：s05 引入 CDP 抽象——`Element` + 录制式的 stub `CDPClient`。s04 的工具到那时会从"纯文本 stub"升级为"调用 CDP frames"，但 Registry / Dispatcher / Schema 这一层一行都不用改。
