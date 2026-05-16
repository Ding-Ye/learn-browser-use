# s04 · 工具注册表 (tool-registry)

> Hard-coded action switches don't scale: every new tool means new code in the loop. This session swaps the switch for a `Registry` that hands the LLM auto-generated JSON Schemas.
> 硬编码的 action switch 扩展不了：每加一个工具就要改循环。本节把 switch 换成 `Registry`，并通过反射自动生成 JSON Schema 喂给 LLM。

## What this teaches / 教什么

- **A `Tool` interface plus a `Registry` is the boundary between the loop and the world.**
- **`Tool` 接口 + `Registry` 是循环与外部世界之间的边界。**
- **Reflection on a Go struct → JSON Schema** lets you keep the schema next to the type.
- **反射 Go struct 生成 JSON Schema**：让 schema 和类型在一处，不会漂移。
- **The Dispatcher owns the per-action timeout**, so tools don't each reinvent it.
- **Dispatcher 统一管单次 action 的超时**，避免每个 tool 重复造轮子。

## Run / 运行

```bash
go run .                    # prints schemas + dispatches one example
go test -v ./...            # 6 tests
```

## Files / 文件

| File | Purpose / 作用 |
|---|---|
| `types.go`         | `ActionCall`, `ContentBlock`, `ToolSchema` — wire shapes. |
| `tool.go`          | `Tool` interface: Name / Description / Schema / Run. |
| `registry.go`      | `Registry` holding `map[string]Tool` + ordered All / Schemas. |
| `schema_gen.go`    | Struct reflection → JSON Schema. Honours `json` and `desc` tags. |
| `dispatcher.go`    | `Dispatcher.Act(ctx, call)` runs one tool with a per-call timeout. |
| `actions.go`       | `SearchTool`, `TypeTool`, `ScrollTool` — three example tools. |
| `main.go`          | CLI demo — prints schemas + dispatches one ActionCall. |
| `registry_test.go` | 6 tests: register/lookup, schema gen, tag handling, timeout, unknown action, happy path. |
| `testdata/expected.txt` | Captured `go run .` output. |

## Key teaching points / 关键学习点

1. **Why an interface instead of a function map?** Python uses `@registry.action` decorators that inspect signatures at registration time. Go's idiomatic equivalent is a small `Tool` interface — `Schema()` becomes the method the registration step calls in lieu of inspecting a signature.
2. **Why `desc:"..."` in struct tags?** The description ends up in the JSON Schema the LLM sees. Keeping it on the struct field means rename-safe documentation: editor and gofmt move it with the field.
3. **Why ctx-with-timeout in the dispatcher?** A tool that calls into HTTP / CDP already accepts `ctx`, so `context.WithTimeout` automatically cascades. A raw `time.After` race would leak the inner goroutine when the deadline fires.
4. **Schemas() is sorted.** Map iteration order in Go is randomized. The LLM gets a stable order regardless of registration order, so prompts and tests are reproducible.

## What this is NOT / 这一节"故意不做"什么

- No sensitive-data injection (upstream `<secret>foo</secret>` replacement).
- No "special parameters" wiring (`browser_session`, `page_extraction_llm`, etc.).
- No structured-output schema validation on the LLM side — that's s02's concern.
- No sequence termination (`terminates_sequence=True` upstream).

## Upstream / 上游对照

- `browser_use/tools/registry/service.py` — the real Registry, plus pydantic param model generation.
- `browser_use/tools/service.py#L77-L300` — the Dispatcher and the 180s timeout fallback.

See `docs/{zh,en}/s04-tool-registry.md` for the full walkthrough and `upstream-readings/s04-tool-registry.py` for the annotated upstream excerpt.
