# learn-browser-use

> Build a [browser-use](https://github.com/browser-use/browser-use)-style browser agent from scratch in Go, one mechanism per chapter. After 12 chapters you'll have the whole skeleton in your head.

[中文 README](./README.md) · Upstream: <https://github.com/browser-use/browser-use> (MIT, SHA `933e28c5`)

---

## What this is

[browser-use](https://github.com/browser-use/browser-use) is a 94K-star library that lets LLM agents drive a real browser — clicking, typing, scrolling, reading DOM, completing real-world tasks. The production implementation is ~98K lines of Python on top of Playwright, CDP, 16 LLM providers, and an event-driven watchdog system. Reading it cold is overwhelming.

This repo re-implements the core in Go, one mechanism per chapter, so the architecture stops being black magic. After s12 you should be able to recite:

- The agent loop's protocol (`StopReason` three-state machine)
- Where the Provider abstraction's edge lives (how multi-model adapters fit)
- How DOM is serialized for the LLM (what `selector_map` is)
- Why the watchdog event bus exists (decoupling downloads/popups/security side effects)
- How the full agent ties it all together (s12 = s01 + 11 patches)

---

## Quickstart

```bash
git clone https://github.com/Ding-Ye/learn-browser-use
cd learn-browser-use

# Run s01: minimum agent loop (no real LLM, no browser)
cd agents/s01-minimum-loop
go run . "search hacker news"
go run . -v "navigate https://example.com"
go test -v ./...
```

Requires Go ≥ 1.24. Stdlib only — no external Go dependencies in any session.

---

## Curriculum

| # | Chapter | Upstream mechanism | Status |
|---|---|---|---|
| s01 | [Minimum agent loop](./docs/en/s01-minimum-loop.md) ([zh](./docs/zh/s01-minimum-loop.md)) | `browser_use/agent/service.py` Agent.step | ✅ |
| s02 | [LLM Provider abstraction](./docs/en/s02-llm-provider.md) ([zh](./docs/zh/s02-llm-provider.md)) | `browser_use/llm/base.py` + `openai/chat.py` | ✅ |
| s03 | [Message manager + compaction](./docs/en/s03-message-manager.md) ([zh](./docs/zh/s03-message-manager.md)) | `browser_use/agent/message_manager/service.py` | ✅ |
| s04 | [Tool registry & dispatcher](./docs/en/s04-tool-registry.md) ([zh](./docs/zh/s04-tool-registry.md)) | `browser_use/tools/registry/service.py` + `tools/service.py` | ✅ |
| s05 | [Element actor (CDP abstraction)](./docs/en/s05-element-actor.md) ([zh](./docs/zh/s05-element-actor.md)) | `browser_use/actor/element.py` | ✅ |
| s06 | [Watchdog & event bus](./docs/en/s06-watchdog-pattern.md) ([zh](./docs/zh/s06-watchdog-pattern.md)) | `browser_use/browser/watchdog_base.py` | ✅ |
| s07 | [Browser session](./docs/en/s07-browser-session.md) ([zh](./docs/zh/s07-browser-session.md)) | `browser_use/browser/session.py` | ✅ |
| s08 | DOM serializer for LLM | `browser_use/dom/serializer/serializer.py` | ⏳ |
| s09 | DOM service (snapshot + filter) | `browser_use/dom/service.py` | ⏳ |
| s10 | Token cost tracking | `browser_use/tokens/service.py` | ⏳ |
| s11 | Filesystem sandbox | `browser_use/filesystem/file_system.py` | ⏳ |
| s12 | Full agent loop | `browser_use/agent/service.py` (full) | ⏳ |
| s_full | End-to-end integration | (doc only) | ⏳ |
| A | Appendix A · LLM-as-driver philosophy | mental model | ⏳ |
| B | Appendix B · Upstream source-reading map | reference | ⏳ |

✅ = published · ⏳ = planned (this repo is being generated chapter-by-chapter)

---

## Per-chapter shape

Every chapter follows six sections (bilingual):

1. **Problem** — what's missing after the previous chapter
2. **Solution** — mental model (before any code)
3. **How It Works** — ASCII diagram + 30-100 lines of core code
4. **What Changed** — diff against the previous chapter
5. **Try It** — copy-pasteable shell + expected output
6. **Upstream Source Reading** — a real browser-use Python excerpt, annotated

Each chapter is a self-contained Go module (`agents/sNN-<slug>/`) with its own `go.mod`, `main.go`, tests, and README. **Chapters do not import each other** — this is what makes each chapter independently readable.

---

## Layout

```
learn-browser-use/
├── agents/                   one independent Go module per chapter
│   └── s01-minimum-loop/
│       ├── go.mod
│       ├── main.go
│       ├── loop.go
│       ├── fake_provider.go
│       ├── actions.go
│       ├── types.go
│       ├── loop_test.go
│       ├── README.md
│       └── testdata/
├── docs/                     bilingual docs (zh + en)
│   ├── zh/
│   └── en/
├── upstream-readings/        annotated Python excerpts from upstream
├── web/                      Next.js static doc viewer
├── .github/workflows/        go.yml + web.yml + docs.yml CI
├── go.work                   workspace file
└── README.md / README.en.md
```

---

## Upstream credit

learn-browser-use is a teaching companion for [browser-use/browser-use](https://github.com/browser-use/browser-use), created by [@Gregor Zunic](https://github.com/gregpr07) and the browser-use team. All cited Python snippets remain under the original MIT copyright.

## License

MIT (same as upstream).
