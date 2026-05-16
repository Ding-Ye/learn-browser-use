# learn-browser-use

> 用 Go 从零渐进重写一个 [browser-use](https://github.com/browser-use/browser-use) 的核心。每节加一个机制，12 节读完你能完全看懂 browser-use 的骨架。
>
> Build a [browser-use](https://github.com/browser-use/browser-use)-style browser agent from scratch in Go, one mechanism per chapter. After 12 chapters you'll have the whole skeleton in your head.

[English README](./README.en.md) · 上游 / Upstream: <https://github.com/browser-use/browser-use> (MIT, SHA `933e28c5`)

---

## 这是什么 / What this is

[browser-use](https://github.com/browser-use/browser-use) 是一个 94K stars 的库，让 LLM agent 自己开浏览器、点按钮、填表、读 DOM、做研究。生产实现 ~98K 行 Python，依赖 Playwright、CDP、16 种 LLM provider、event-driven 的 watchdog 系统。直接读源码会被淹没。

这个 repo 把它的内核**用 Go 渐进重写一遍**。每一节只加一个机制，每一节都能独立跑、独立测、独立读。读完 s12 你应该能脱口而出：

- agent loop 的协议是什么（`StopReason` 三态）
- Provider 抽象的边界在哪里（多 model 怎么对接）
- DOM 怎么序列化给 LLM（selector_map 是什么）
- watchdog 事件总线为什么存在（解耦下载/弹窗/安全的副作用）
- 完整 agent 怎么把这一切拼起来（s12 = s01 + 11 个补丁）

learn-browser-use is a 94K-star, ~98K-LOC Python library that lets LLM agents drive a real browser. This repo re-implements the core in Go, one mechanism per chapter, so the architecture stops being black magic.

---

## 快速开始 / Quickstart

```bash
git clone https://github.com/Ding-Ye/learn-browser-use
cd learn-browser-use

# 跑 s01：最小 agent 循环（无真实 LLM、无浏览器）
cd agents/s01-minimum-loop
go run . "search hacker news"
go run . -v "navigate https://example.com"
go test -v ./...
```

需要 Go ≥ 1.24。s01-s06 仅依赖标准库，s07+ 也是零外部依赖。
Requires Go ≥ 1.24. Stdlib only.

---

## 课程 / Curriculum

| # | 章节 / Chapter | 上游机制 / Upstream mechanism | 状态 |
|---|---|---|---|
| s01 | [最小 agent 循环 / Minimum agent loop](./docs/zh/s01-minimum-loop.md) ([en](./docs/en/s01-minimum-loop.md)) | `browser_use/agent/service.py` Agent.step | ✅ |
| s02 | LLM Provider 抽象 / LLM Provider abstraction | `browser_use/llm/base.py` + `openai/chat.py` | ⏳ |
| s03 | 消息管理与压缩 / Message manager + compaction | `browser_use/agent/message_manager/service.py` | ⏳ |
| s04 | 工具注册表 / Tool registry & dispatcher | `browser_use/tools/registry/service.py` + `tools/service.py` | ⏳ |
| s05 | 元素操作 (CDP) / Element actor (CDP) | `browser_use/actor/element.py` | ⏳ |
| s06 | 看门狗事件总线 / Watchdog & event bus | `browser_use/browser/watchdog_base.py` | ⏳ |
| s07 | 浏览器会话 / Browser session | `browser_use/browser/session.py` | ⏳ |
| s08 | DOM 序列化 / DOM serializer | `browser_use/dom/serializer/serializer.py` | ⏳ |
| s09 | DOM 服务 / DOM service | `browser_use/dom/service.py` | ⏳ |
| s10 | Token 计费 / Token cost tracking | `browser_use/tokens/service.py` | ⏳ |
| s11 | 文件系统沙箱 / Filesystem sandbox | `browser_use/filesystem/file_system.py` | ⏳ |
| s12 | 完整 agent loop / Full agent loop | `browser_use/agent/service.py` (full) | ⏳ |
| s_full | 端到端集成 / End-to-end integration | (doc only) | ⏳ |
| A | 附录 A · LLM-as-driver 哲学 / Appendix A | mental model | ⏳ |
| B | 附录 B · 上游源码导读地图 / Appendix B | reference | ⏳ |

✅ = 已发布 · ⏳ = 计划中（this repo is being generated chapter-by-chapter）

---

## 每一节的形态 / Per-chapter shape

每章包括 6 段（双语对照）：

1. **Problem / 问题** — 上一节解决了什么，还缺什么
2. **Solution / 解决方案** — 心智模型（先于代码）
3. **How It Works / 工作原理** — ASCII 图 + 30-100 行核心代码
4. **What Changed / 与上一节的变化** — diff（明确"加了什么"）
5. **Try It / 动手试一试** — 可复制粘贴的 shell + 期望输出
6. **Upstream Source Reading / 上游源码阅读** — 真实的 browser-use Python 节选，带注解

每节是一个独立的 Go module（`agents/sNN-<slug>/`），有自己的 `go.mod`、`main.go`、测试、README。**节与节之间不互相 import**——这是为了让每一节都能单独读。

---

## 项目结构 / Layout

```
learn-browser-use/
├── agents/                   每节一个独立 Go module
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
├── docs/                     双语文档（zh + en）
│   ├── zh/
│   └── en/
├── upstream-readings/        从上游 browser-use 节选并注解的 Python 片段
├── web/                      Next.js 静态文档站
├── .github/workflows/        go.yml + web.yml + docs.yml CI
├── go.work                   workspace 文件
└── README.md / README.en.md
```

---

## 上游致谢 / Upstream credit

learn-browser-use 是 [browser-use/browser-use](https://github.com/browser-use/browser-use) 的教学伴侣 repo，由 [@Gregor Zunic](https://github.com/gregpr07) 和 browser-use 团队创造。所有引用的 Python 代码片段版权归原作者（MIT 许可）。

This is a teaching companion for [browser-use/browser-use](https://github.com/browser-use/browser-use), created by Gregor Zunic and the browser-use team. All cited Python snippets remain under the original MIT copyright.

## License

MIT (same as upstream).
