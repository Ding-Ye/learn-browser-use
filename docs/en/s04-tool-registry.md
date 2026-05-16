---
title: "s04 · Tool registry & dispatcher"
chapter: 4
slug: s04-tool-registry
est_read_min: 14
---

# s04 · Tool registry & dispatcher

> Teaching focus: s01's `byName := map[string]Action{...}` worked when there were 3 actions; once you have 30, parameterized inputs, and an LLM that needs to see JSON Schemas, that ad-hoc map has to grow up into a real `Registry`. This session ports `browser_use/tools/` into ~450 lines of Go: Registry + reflection-based JSON Schema generator + Dispatcher with a per-action timeout guard.

---

## Problem / 问题

s01's `Agent.Run` contains this block:

```go
byName := map[string]Action{}
for _, act := range a.Actions {
    byName[act.Name()] = act
}
// ... later: tool, ok := byName[ac.Name]
```

Fine with three actions. Painful once any of the following kicks in:

- The tool count grows toward ~30 (upstream `tools/service.py` registers 30+ actions).
- Each tool takes different typed inputs (`click(index int)` vs `extract(query string, schema dict)` vs `done(text string, success bool)`).
- The LLM must see each tool's **JSON Schema** in the prompt before it can produce a valid `tool_use` block.
- Every execution needs a "hang → timeout → error" guard (upstream defaults to 180s).

That list shoves the inline map up into its own subsystem.

The schema requirement is the sharpest pressure: hand-written JSON Schema strings will inevitably drift away from the Go structs they're supposed to describe. Upstream uses pydantic's `model_json_schema()`; we want the same thing in Go, via reflection.

s04 answers three questions:

1. **Who owns "the set of registered tools"?** → `Registry`.
2. **Who generates each tool's JSON Schema?** → `SchemaFromStruct`, walking a struct with `json` + `desc` tags.
3. **Who runs a tool without letting it hang the agent?** → `Dispatcher`, wrapping every `Act` call in `context.WithTimeout`.

## Solution / 解决方案

Promote the inline map into three components:

| Role | Type | Upstream counterpart |
|---|---|---|
| Tool unit | `Tool` interface | `browser_use/tools/registry/views.py::RegisteredAction` |
| Tool collection | `Registry` struct | `browser_use/tools/registry/service.py::Registry` |
| Execution dispatch | `Dispatcher` struct | `browser_use/tools/service.py::Tools.act()` |

`Tool` is a four-method interface that slices the tool lifecycle:

```go
type Tool interface {
    Name() string                                                  // registry key
    Description() string                                           // one-line nudge for the LLM
    Schema() ToolSchema                                            // JSON Schema for the args
    Run(ctx context.Context, input json.RawMessage) (string, error)
}
```

`Registry` is backed by a `map[string]Tool` but always exposes **deterministic order**:

```go
func (r *Registry) Register(t Tool) error
func (r *Registry) Lookup(name string) (Tool, bool)
func (r *Registry) All() []Tool      // name-sorted
func (r *Registry) Schemas() []ToolSchema
```

`Dispatcher` is a tiny struct that owns the cross-tool action timeout policy:

```go
type Dispatcher struct {
    Registry *Registry
    Timeout  time.Duration // 0 ⇒ DefaultTimeout (180s)
}
func (d *Dispatcher) Act(ctx context.Context, call ActionCall) (ContentBlock, error)
```

The Go-flavoured piece of this session is `SchemaFromStruct(zero interface{}) json.RawMessage`: it walks `reflect.Type`, dispatches by Kind into JSON Schema primitives (`string` / `integer` / `number` / `boolean` / `array` / `object`), and honours struct tags:

- `json:"foo"` → field name in the schema
- `json:"foo,omitempty"` → not in `required`
- `desc:"..."` → field `description` in the schema

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
│         │       (per-tool Schema() reflects struct tags)           │
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

Core ~50 lines:

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
    for _, t := range r.All() { // All() is sorted by name
        out = append(out, t.Schema())
    }
    return out
}

// schema_gen.go (core loop)
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
    if err != nil { /* translate timeout, wrap into tool_result */ }
    return ContentBlock{Type: "tool_result", Name: call.Name, Result: out}, nil
}
```

**Four non-obvious points**:

1. **Why put `desc:"..."` in struct tags instead of a single big `Tool.Description()` blob?** The LLM needs **per-field** descriptions to tell `index` (element index) from `index` (page index, etc.). Keeping the description next to the field makes it rename-safe; gofmt won't tear them apart; tests can grep them.
2. **Why does the Dispatcher use `context.WithTimeout` rather than `time.After` racing against a result channel?** Real tools will call HTTP / CDP / file-IO internally, and those libraries already accept `context.Context`. Cancelling the parent ctx cascades through the call chain for free; a `time.After`-and-select race leaks the inner goroutine when the deadline fires.
3. **`Schemas()` enforces alphabetical order.** Go's map iteration is randomised. Without the sort, every `go run .` produces a different prompt and `testdata/expected.txt` would flake immediately.
4. **The dispatcher always returns a ContentBlock, even on error.** The upper layer (s12's Agent loop) wants to feed "unknown tool / tool error / tool timeout" back to the LLM — let the model see the error and self-correct. If `Act` returned only an error on unknown action, the loop would have to re-synthesise a tool_result, splitting the protocol across two places.

## What Changed / 与上一节的变化

s01–s03 all use `[]Action` + an inline `byName` map:

```diff
- // s01-s03 style
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
-         out, _ := tool.Run(ctx, ac.Input)   // no timeout
-         // no schema handed to the LLM
-         results = append(results, ContentBlock{Type: "tool_result", Result: out})
-     }
- }
```

s04 onward:

```diff
+ reg := NewRegistry()
+ reg.MustRegister(SearchTool{})
+ reg.MustRegister(TypeTool{})
+ reg.MustRegister(ScrollTool{})
+
+ schemas := reg.Schemas()                 // auto-reflected from struct tags
+ // ... hand `schemas` to the Provider so the LLM knows which tools exist
+
+ d := &Dispatcher{Registry: reg, Timeout: 180 * time.Second}
+ block, err := d.Act(ctx, call)           // one line for execute + timeout + unified error
```

The crucial new capability: **the LLM finally sees a "tools menu"**. s02/s03 could only tell the model "you have these names". From s04, you can tell it "here's the shape of the input each name expects". That's the dividing line between "prompt-engineered tool use" and "schema-driven tool use".

## Try It / 动手试一试

```bash
cd agents/s04-tool-registry

# auto-generated schemas + one example dispatch
go run .

# 6 tests
go test -v ./...
```

Expected output (excerpt):

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

Test coverage:

- `TestRegisterAndLookup` — register/lookup round-trip + duplicate-register rejection + `All()` sorted alphabetically.
- `TestSchemaGenerationFromStruct` — string / int / bool fields reflect to the right JSON Schema primitive types.
- `TestSchemaGenerationWithTags` — `desc:"..."` lands in `description`; `json:"...,omitempty"` keeps the field out of `required`.
- `TestDispatchTimeoutFires` — a tool that sleeps 1s under a 100ms timeout returns an error within ~100ms.
- `TestDispatchUnknownActionReturnsError` — unknown tool name produces a clear error and a `tool_result` block.
- `TestDispatchHappyPath` — happy call returns a `tool_result` containing the query echo.

## Upstream Source Reading / 上游源码阅读

Upstream `browser_use/tools/registry/service.py` lines 32–326 is the real Registry. The excerpt below shows `Registry.action()` (the decorator) and `execute_action()` (the entry point) — these are the 1-to-1 mirrors of our Go `Register` + `Dispatcher.Act`.

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
        # These "special params" are injected by the agent on every call:
        # browser_session, page_url, cdp_client, file_system, etc.
        # We don't ship that layer in s04; once s07 introduces BrowserSession
        # we'll extend Dispatcher to populate it.
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

        # Normalize the function signature → rewrite arbitrary signatures
        # into (params, **special).
        normalized_func, actual_param_model = self._normalize_action_function_signature(
            func, description, param_model
        )

        action = RegisteredAction(
            name=func.__name__,
            description=description,
            function=normalized_func,
            param_model=actual_param_model,        # ← pydantic model — analog of our ToolSchema.Parameters
            domains=final_domains,
            terminates_sequence=terminates_sequence,
        )
        self.registry.actions[func.__name__] = action
        return normalized_func

    return decorator
```

```python
# Source: browser_use/tools/registry/service.py#L328-L395 (excerpt)

async def execute_action(
    self,
    action_name: str,
    params: dict,
    browser_session: BrowserSession | None = None,
    ...
) -> Any:
    if action_name not in self.registry.actions:
        raise ValueError(f'Action {action_name} not found')   # ← our "unknown action" error

    action = self.registry.actions[action_name]
    try:
        validated_params = action.param_model(**params)        # ← equivalent of our json.Unmarshal into the typed struct
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

**Reading notes**:

1. **`@action(description=...)` decorator ↔ the `Tool` interface.** Python decorators run at import time and inspect the function signature to synthesize a pydantic model. Go has no decorators, so we shift the "generate schema" responsibility into the `Tool.Schema()` method — each tool writes one line, `SchemaFromStruct(myArgs{})`. Equivalent power, more explicit.
2. **The 200-line `_normalize_action_function_signature`** is rewriting `def click(index: int, browser_session: BrowserSession) -> None` into `def click(*, params: ClickParams, browser_session=None)` — i.e. collapsing positional args into "kwargs + special-param injection". s04 has no special params yet; we'll push that normalization into s07 when BrowserSession arrives.
3. **`param_model = create_model(...)` is runtime model-building in pydantic** — essentially reflecting a function signature into a JSON-Schema-compatible type. Our `SchemaFromStruct` is the static-typing counterpart: the struct already exists at compile time, so we only walk it at runtime.
4. **Where's the timeout?** Upstream Registry doesn't handle timeouts itself; the 180s default lives in `browser_use/tools/service.py::Tools.act()` as `_DEFAULT_ACTION_TIMEOUT_S`, plus a defensive `_coerce_valid_action_timeout` for env-var overrides. We folded that into the Dispatcher in s04 to keep layering simple.
5. **`terminates_sequence=True`** means "when the LLM produces multiple actions in one turn, stop after running this one" — the canonical example is `done`. s04 doesn't have multi-action sequences yet; that lands in s12.
6. **That blob of special_context** (browser_session / file_system / cdp_client / sensitive_data / ...) is the real infrastructure a working agent needs. s04 keeps it empty on purpose, so the chapter is about the Registry itself. s07/s11/s12 add the special params back, one at a time.

**Read further**: start at `browser_use/tools/registry/service.py::Registry.action()`, follow `_normalize_action_function_signature` to see how arbitrary signatures get coerced into kwargs-only, then jump to `browser_use/tools/service.py::Tools.act()` to see how timeout, telemetry, and error handling glue together. That trail is the live code map of s04 → s07 → s12.

---

**Next session preview**: s05 introduces a CDP abstraction — `Element` + a recorder-style stub `CDPClient`. By then, the tools registered here will graduate from "pure text stubs" to "tools that emit CDP frames", but Registry / Dispatcher / Schema won't need a single line of change.
