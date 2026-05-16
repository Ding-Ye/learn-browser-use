---
title: "s05 · 元素操作 (CDP 抽象)"
chapter: 5
slug: s05-element-actor
est_read_min: 12
---

# s05 · 元素操作 (CDP 抽象)

> 教什么：前四节里所有 action 都止步于一段字符串（"LLM 选了 `click(index=3)`"），早晚得把它变成发给 Chromium 的协议帧。本节引入 **CDP 边界**：一个 `CDPClient` interface + 一个录像机风格的 stub。本节还不真连浏览器——录像机只把"如果连了，会发出去什么帧"录下来。我们用 ~400 行 Go 把 `browser_use/actor/element.py` 移植成 Element struct + 4 个方法 (Click / Type / Focus / Screenshot) + 录像机。

---

## Problem / 问题

到 s04 结束时，我们有了一个能给 LLM 出"打过类型菜单"的工具注册表。LLM 选 `click({"index":3})`，Dispatcher 调 `tool.Run(ctx, raw)`，工具回传一个字符串。一切看似很顺——但 `tool.Run` 的内部一直是个黑盒。`click` 内部到底干了什么？

在真正的 browser-use 里大致是这样：

```python
async def click_handler(params, browser_session):
    el = await browser_session.get_element(params.index)
    await el.click(button='left')   # ← 这个 click 究竟展开成什么？
```

`el.click(...)` 才是 CDP wire frame 真正诞生的地方。具体来说，一次"逻辑上的点击"展开为：

1. `Page.getLayoutMetrics` — 拿到视口尺寸
2. `DOM.getContentQuads`（或 `DOM.getBoxModel`）— 算出元素在屏幕上的位置
3. `DOM.scrollIntoViewIfNeeded` — 元素不在屏幕里就滚进来
4. `Input.dispatchMouseEvent type=mouseMoved` — 移动光标
5. `Input.dispatchMouseEvent type=mousePressed` — 按下
6. `Input.dispatchMouseEvent type=mouseReleased` — 释放

一次点击 = 6 个 WebSocket 帧。再加上 `Type`（每个字符一个帧）、`Focus`、`Screenshot`，整个 CDP 在 `Input` / `DOM` / `Page` / `Runtime` / `Target` / `Network` 等域里有几百个方法名要面对。

**我们需要一个"不需要 Chromium 就能开始写这层代码"的起点**。这就是 s05 的意义。

s05 解决三件事：

1. **"Element 跟谁说话"长什么样？** → `CDPClient` interface，只有一个 `Send(method, params)` 方法。
2. **不连 Chromium 怎么测 Element？** → `RecordingCDPClient`，把每次 Send 追加到 `Frames` 切片，而不是真发出去。
3. **Element 本身长什么样？** → 两个字段的 struct：`{Client, NodeID}`，加上几个构建 CDP 参数并调 `Send` 的方法。

## Solution / 解决方案

三块积木：

| 角色 | 类型 | 上游对照 |
|---|---|---|
| 协议线 | `CDPClient` interface | `cdp_use.CDPClient` |
| 测试替身 | `RecordingCDPClient` | (上游没有 — 这是我们为教学发明的) |
| 元素本体 | `Element` struct | `browser_use/actor/element.py::Element` |

`CDPClient` 就一个方法：

```go
type CDPClient interface {
    Send(ctx context.Context, method string, params map[string]any) (map[string]any, error)
}
```

没了。不按域拆方法、不生成 CDP 绑定。反正 CDP 在 wire 上就是 JSON 信封——出去 `{"id": N, "method": "Domain.method", "params": {...}}`，回来 `{"id": N, "result": {...}}`——把所有 CDP 调用拍扁成 `字符串 + map` 是诚实的抽象，不是有损的简化。

`RecordingCDPClient` 是 s05 的实现：

```go
type RecordingCDPClient struct {
    Frames    []Frame                       // append-only 日志
    Responses map[string]map[string]any     // 调用方可选 stub
}
```

每次 `Send` 都被记到 `Frames`。如果调用方提前往 `Responses` 里塞了响应就用那个，否则查内置默认表（比如 `Page.captureScreenshot` 返回 base64 编码的 PNG header）。未知方法返回空 `{}`——和真实 CDP 那些"做副作用、没载荷"的方法一致。

`Element` 两个字段、四个方法：

```go
type Element struct {
    Client CDPClient
    NodeID BackendNodeID
}

func (e Element) Click(ctx, opts ClickOptions) error
func (e Element) Type(ctx, text string) error
func (e Element) Focus(ctx) error
func (e Element) Screenshot(ctx) ([]byte, error)
```

每个方法都构造一个 `map[string]any`（一定带 `backendNodeId` + 方法特有字段），然后调 `Send`。`Click` 调三次（移/按/放）；`Type` 调一次（`Input.insertText`）；`Focus` 调一次（`DOM.focus`）；`Screenshot` 调一次（`Page.captureScreenshot`）并解码 base64 结果。

## How It Works / 工作原理

```
┌──────────────────────────────────────────────────────────────────────┐
│                              s05 数据流                              │
│                                                                      │
│   el := Element{Client: rec, NodeID: 42}                             │
│                                                                      │
│   el.Click(ctx, opts)                                                │
│       │                                                              │
│       ├── 构造 map: {type: "mouseMoved",     backendNodeId: 42, ..}  │
│       │       │                                                      │
│       │       ▼                                                      │
│       │   rec.Send(ctx, "Input.dispatchMouseEvent", params)          │
│       │       │                                                      │
│       │       ▼                                                      │
│       │   append → Frames                                            │
│       │       │                                                      │
│       │       ▼                                                      │
│       │   查默认响应表 → 返回 {}                                     │
│       │                                                              │
│       ├── 构造 map: {type: "mousePressed", button, clickCount, ...}  │
│       │   rec.Send(ctx, "Input.dispatchMouseEvent", params)          │
│       │                                                              │
│       └── 构造 map: {type: "mouseReleased", ...}                     │
│           rec.Send(ctx, "Input.dispatchMouseEvent", params)          │
│                                                                      │
│   el.Type(ctx, "hello 你好 👋")                                       │
│       │                                                              │
│       └── rec.Send(ctx, "Input.insertText",                          │
│                    {text: "...", backendNodeId: 42})                 │
│                                                                      │
│   el.Screenshot(ctx)                                                 │
│       │                                                              │
│       └── rec.Send(ctx, "Page.captureScreenshot", {format: "png"})   │
│             返回 {"data": "<base64 PNG header>"}                     │
│             解码 → 8 字节切片                                        │
│                                                                      │
│   --- 测试断言 ---                                                   │
│   rec.Frames == [ Input.dispatchMouseEvent x3,                       │
│                   Input.insertText,                                  │
│                   Page.captureScreenshot ]                           │
└──────────────────────────────────────────────────────────────────────┘
```

核心约 50 行：

```go
// cdp_client.go
func (c *RecordingCDPClient) Send(_ context.Context, method string, params map[string]any) (map[string]any, error) {
    c.Frames = append(c.Frames, Frame{Method: method, Params: params})
    if c.Responses != nil {
        if r, ok := c.Responses[method]; ok {
            return r, nil
        }
    }
    if r, ok := defaultResponses[method]; ok {
        return r, nil
    }
    return map[string]any{}, nil
}

// element.go (Click 循环)
for _, kind := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
    if _, err := e.Client.Send(ctx, "Input.dispatchMouseEvent", pressParams(kind)); err != nil {
        return fmt.Errorf("element click (%s): %w", kind, err)
    }
}

// element.go (Type)
params := map[string]any{
    "text":          text,
    "backendNodeId": int(e.NodeID),
}
_, err := e.Client.Send(ctx, "Input.insertText", params)

// element.go (修饰键 bitmask 拼装)
func modifiersToBitmask(mods []string) int {
    mask := 0
    for _, m := range mods {
        switch m {
        case "Alt":     mask |= 1
        case "Control": mask |= 2
        case "Meta":    mask |= 4
        case "Shift":   mask |= 8
        }
    }
    return mask
}
```

**四个不那么显然的点**：

1. **为什么是 Recording 而不是 Mocking？** Mock 库（gomock、testify/mock）逼你事先声明期望："期望调用某个方法、参数是某某"。学习一个协议时这是错的方向——你还不知道到底要发哪些方法，测试和被测代码会一起改。Recording 把这件事反过来：先把活干完，再回头看录到了什么。断言读起来像散文，不像合同。
2. **为什么用 `BackendNodeID` 而不是 selector / DOM `nodeId`？** Chromium 的 `nodeId` 在 frontend 重新 attach 文档时就会被重新分配——同步一次调用够用，跨两次 LLM 决策完全不够。`backendNodeId` 在多数 reflow 期间保持稳定，能撑到 LLM 下一回合；上游 `Element` 之所以握的是它，就是这个原因。selector 更糟：页面一改版就全废，而且 LLM 根本看不到 CSS。
3. **为什么 `Type` 用 `Input.insertText` 而不是逐字符 `dispatchKeyEvent`？** 上游 `Element.fill` 每字符触发一组 keyDown/char/keyUp，是因为有些页面靠键盘事件触发自动补全。这本质是*页面行为*的关切，应该交给 watchdog (s06) 处理。CDP 里"现在把这段文本塞进去"对应的原语就是 `Input.insertText`——这也是上游 `skill_cli` 做"快速 paste"时调的那个方法。在 s05 用它，Unicode 故事干净（一帧、完整 UTF-8），章节焦点也保持收敛。
4. **修饰键的 bitmask 拼装就是 OS 底层逻辑。** `{Alt:1, Control:2, Meta:4, Shift:8}` 按位或——这看起来像 OS plumbing 是因为它就是；CDP 直接继承了 Windows VK_*、X11 ModifierMask、Cocoa NSEventModifierFlags 的输入事件位编码。在录像机里直接暴露这个 bitmask，让学习者**看见**协议本来的样子，而不是用 Go enum 把它盖起来——这里没什么可抽象的，只有要去看的真协议。

## What Changed / 与上一节的变化

s04 的工具是"字符串进、字符串出"。Dispatcher 调 `tool.Run(ctx, raw)`，工具回字符串，Dispatcher 把它打包成 `ContentBlock{Type:"tool_result"}`。没浏览器、没 CDP、没元素句柄——只是 JSON 上的纯函数。

```diff
- // s04: tool.Run 是个黑盒
- type Tool interface {
-     Run(ctx context.Context, input json.RawMessage) (string, error)
- }
-
- // click 看起来是这样：
- func (SearchTool) Run(ctx context.Context, raw json.RawMessage) (string, error) {
-     // ... 纯文本结果，没浏览器副作用
- }
```

s05 引入一条**平行的关切**：工具**下面**那一层。到 s07 BrowserSession 上线后，真正的 click tool 会是这样：

```diff
+ // s07+: click_tool 委托给 BrowserSession 里的 Element
+ func (ClickTool) Run(ctx context.Context, raw json.RawMessage) (string, error) {
+     var args struct{ Index int `json:"index"` }
+     _ = json.Unmarshal(raw, &args)
+     el := session.GetElement(args.Index)             // s07
+     return "clicked", el.Click(ctx, ClickOptions{})  // s05 — 本节
+ }
```

新增的核心能力：**Element 不依赖 Chromium 就能测**。录像机给了我们一片可以在毫秒内验证"帧流（协议契约）正确性"的基底——不连浏览器、不会 flake。生产代码把录像机换成真 WebSocket 客户端时，Element 自己的代码不用动。

## Try It / 动手试一试

```bash
cd agents/s05-element-actor

# 编译 + 跑 demo（录下 click + type + screenshot）
go run .

# 6 个测试
go test -v ./...
```

预期输出（节选）：

```
# recorded CDP frames

[0] Input.dispatchMouseEvent
  {
    "backendNodeId": 42,
    "type": "mouseMoved",
    "x": 0,
    "y": 0
  }

[1] Input.dispatchMouseEvent
  {
    "backendNodeId": 42,
    "button": "left",
    "clickCount": 1,
    "modifiers": 10,
    "type": "mousePressed",
    "x": 0,
    "y": 0
  }
...
[3] Input.insertText
  {
    "backendNodeId": 42,
    "text": "hello 你好 👋"
  }

[4] Page.captureScreenshot
  {
    "backendNodeId": 42,
    "format": "png"
  }
```

测试覆盖：

- `TestClickRecordsMouseEvent` — Click 发出 3 个 move/press/release 帧、每个都带正确的 backendNodeId。
- `TestTypeEncodesUnicode` — Type `"café 你好 👋"`，UTF-8 字节在 frame 里完整往返。
- `TestModifierKeysPassed` — `Modifiers: ["Shift","Control"]` 在录到的 frame 里变成 bitmask `10`。
- `TestScreenshotReturnsDummyPNG` — Screenshot 返回 ≥8 字节，开头是 PNG signature `89 50 4E 47 0D 0A 1A 0A`。
- `TestFocusDispatchesDOMFocus` — Focus 只发一帧 `DOM.focus`，带 backendNodeId。
- `TestEmptyClickOptionsNormalises` — 零值 `ClickOptions{}` 默认是左单击、无修饰键。

## Upstream Source Reading / 上游源码阅读

上游 `Element` 在 `browser_use/actor/element.py`。下面这段 60 行节选包含 `__init__` + `click` 方法头 + 末尾的鼠标事件分派——这些恰好和我们的 Go 代码一一对应。

```python
# Source: browser_use/actor/element.py#L62-L100, L268-L325
# License: MIT

class Element:
    """Element operations using BackendNodeId."""

    def __init__(
        self,
        browser_session: 'BrowserSession',
        backend_node_id: int,
        session_id: str | None = None,
    ):
        self._browser_session = browser_session
        self._client = browser_session.cdp_client
        self._backend_node_id = backend_node_id
        self._session_id = session_id

    async def click(
        self,
        button: 'MouseButton' = 'left',
        click_count: int = 1,
        modifiers: list[ModifierType] | None = None,
    ) -> None:
        """Click the element using the advanced watchdog implementation."""
        # ... 视口度量 + quad 几何 + scrollIntoView 省略 ...

        # 计算 CDP 修饰键 bitmask
        modifier_value = 0
        if modifiers:
            modifier_map = {'Alt': 1, 'Control': 2, 'Meta': 4, 'Shift': 8}
            for mod in modifiers:
                modifier_value |= modifier_map.get(mod, 0)

        # 移动鼠标到元素
        await self._client.send.Input.dispatchMouseEvent(
            params={'type': 'mouseMoved', 'x': center_x, 'y': center_y},
            session_id=self._session_id,
        )

        # 按下
        await self._client.send.Input.dispatchMouseEvent(
            params={
                'type': 'mousePressed',
                'x': center_x, 'y': center_y,
                'button': button,
                'clickCount': click_count,
                'modifiers': modifier_value,
            },
            session_id=self._session_id,
        )

        # 抬起
        await self._client.send.Input.dispatchMouseEvent(
            params={
                'type': 'mouseReleased',
                'x': center_x, 'y': center_y,
                'button': button,
                'clickCount': click_count,
                'modifiers': modifier_value,
            },
            session_id=self._session_id,
        )
```

**阅读笔记**：

1. **`_client.send.Input.dispatchMouseEvent(...)` 这个调用形态**在上游是 `cdp-use` 库提供的*类型化* CDP wrapper——每个 CDP 域变成一个属性，每个方法变成一个 async 函数，参数 dict 会按生成的 TypedDict 做类型校验。我们的 Go `Send(method, params)` 把这一切拍成一个方法 + 一个字符串。代价是工效（不能自动补全），收益是 stdlib-only。
2. **`modifier_map` 字典和我们 Go 里的 switch 一字不差**——同样的 key、同样的 value。CDP 协议规范里就是这么定义的位编码，两边都只是在尊重它。
3. **上游 `click` 大约 250 行**；我们的 Go 版本 30 行。差的那 220 行主要是：viewport 裁剪、quad vs box-model 后备、scrollIntoView、几何拿不到时的 JS-click fallback、每个帧外面包的 `asyncio.wait_for` 超时。每一行你在 s05 看不到的代码，都对应一个生产里真会撞到的问题——撞到了就回上游源码去对。
4. **上游 `fill()`（L423）走的逐字符键盘路径**每个字符发**3 个** CDP 帧（keyDown / char / keyUp）。10 个字符就是 30 次往返。我们 s05 用 `Input.insertText` 总共一帧——对教学场景而言完全正确，而且这也是上游 `skill_cli` 在 L122、L177 做快速 paste 时用的那个方法。
5. **为什么有个 `session_id` 关键字？** 上游的 CDP 客户端在一根 WebSocket 上多路复用多个 browser target（tab、frame、popup）。每个 target 有自己的 `session_id`，每个方法都把它作为 kwarg 带上。s05 完全省略这一层——只有一个隐式 target。s07 引入 lifecycle 时会把它加回来。

**继续读**：打开 `browser_use/actor/element.py`，按顺序读 `click()` (L93–L351)、`fill()` (L353–L507)、`focus()` (L521–L526)、`screenshot()` (L682–L709)。它们共享一个形态：拿几何 → 必要时滚屏 → 分派 CDP 帧 → 可选 JS-eval 兜底。这个形态吃透了，后面的 `hover`、`check`、`select_option`、`drag_to` 都是同一主题的变奏。

---

**下一节预告**：s06 引入事件总线和 watchdog 模式。今天 Element 的方法是同步跑完的；s06 让"弹窗关闭"、"下载处理"等关切能订阅 click 期间发出来的事件，而无需在 Element 自身上长大。
