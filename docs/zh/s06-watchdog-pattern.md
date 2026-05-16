---
title: "s06 · 看门狗与事件总线"
chapter: 6
slug: s06-watchdog-pattern
est_read_min: 14
---

# s06 · 看门狗与事件总线

> 教学焦点：s05 的 Element actor 直接调用（桩）CDP 客户端。当一个 `BrowserSession` 只关心一件事时这种风格还好；可一旦同时要处理 10 件事——下载、弹窗、安全、DOM 快照、截图、录制——所有责任挤进同一个 controller，文件立刻膨胀到无人敢碰。上游用 `BaseWatchdog` + 事件总线 + 反射自动注册解决了这个问题。本节把这两件事翻译成约 250 行 Go：一个轻量的 `EventBus` 和反射式 `AutoAttach`，扫描 watchdog struct 上的 `OnXxxEvent` 方法集。

---

## Problem / 问题

一个 `BrowserSession` 要同时承担多种职责：

- **下载（Downloads）**——监听 `Browser.downloadWillBegin`，把 PDF / zip / 图片落盘，触发 `FileDownloadedEvent`。
- **弹窗（Popups）**——监听 `Page.javascriptDialogOpening`，对 alert/confirm 点 OK，对 prompt 点 Cancel。
- **安全（Security）**——拦截到私网或不允许域名的跳转。
- **DOM**——每次导航后抓快照、做 hash、截图。

把这些塞进一个 Python class，就会得到我们最爱"围观"的那种文件：4000 行，三层 `try/except`，每个职责互相抢命名空间。上游 `browser_use/browser/session.py` 在已经拆出 watchdogs 之后仍然有 4000 LOC。不拆，翻倍。

每个职责共有的约束：

1. 它要对浏览器**发出的事件**做出反应（CDP 帧、生命周期、Agent 动作）。
2. 它需要维护其它系统不该碰的**私有状态**。
3. 它必须**独立运行**——弹窗 handler 抛错不能拖垮下载 handler。

翻译成模式：一个发布-订阅总线，每个职责自己当订阅者。上游选用 Python 的 `bubus` 库；我们需要 Go 等价物。

s06 回答三个问题：

1. **总线的最小 API 是什么？** → `Subscribe(eventName, handler)` + `Emit(ctx, event)`。
2. **watchdog 怎么"零仪式"地注册？** → 反射自动发现 `OnXxxEvent(ctx, *XxxEvent) error`。
3. **handler 需要什么语义？** → 同步派发，按注册顺序，错误全量收集（不短路）。

## Solution / 解决方案

| 角色 | 类型 | 上游对应 |
|---|---|---|
| 事件载荷 | `Event` 接口（仅一个 `EventName()` 方法） | `bubus.BaseEvent` |
| 发布-订阅核心 | `*EventBus`（`sync.RWMutex` + `map[string][]Handler`） | `bubus.EventBus` |
| watchdog 标记 | `Watchdog interface{}` | `BaseWatchdog`（Pydantic BaseModel） |
| 自动发现 | `AutoAttach(w, bus) ([]string, error)`（反射） | `BaseWatchdog.attach_to_session()` |

总线本体足够短，可以贴进来：

```go
type Handler func(ctx context.Context, e Event) error

type EventBus struct {
    mu       sync.RWMutex
    handlers map[string][]Handler
}

func (b *EventBus) Subscribe(eventName string, handler Handler) {
    b.mu.Lock(); defer b.mu.Unlock()
    b.handlers[eventName] = append(b.handlers[eventName], handler)
}

func (b *EventBus) Emit(ctx context.Context, e Event) error {
    b.mu.RLock()
    hs := make([]Handler, len(b.handlers[e.EventName()]))
    copy(hs, b.handlers[e.EventName()])
    b.mu.RUnlock()
    var errs []error
    for _, h := range hs {
        if err := h(ctx, e); err != nil { errs = append(errs, err) }
    }
    return errors.Join(errs...)
}
```

反射式 auto-attach 是真正的看点：

```go
func AutoAttach(w Watchdog, bus *EventBus) ([]string, error) {
    v := reflect.ValueOf(w); t := v.Type()
    ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()
    errType := reflect.TypeOf((*error)(nil)).Elem()
    var registered []string
    for i := 0; i < t.NumMethod(); i++ {
        m := t.Method(i)
        if !strings.HasPrefix(m.Name, "On") || !strings.HasSuffix(m.Name, "Event") { continue }
        eventName := strings.TrimPrefix(m.Name, "On")
        mt := m.Type
        if mt.NumIn() != 3 || mt.In(1) != ctxType { continue }
        ev := mt.In(2)
        if ev.Kind() != reflect.Ptr || ev.Elem().Name() != eventName { continue }
        if mt.NumOut() != 1 || mt.Out(0) != errType { continue }
        // ... 闭包捕获 m，注册一个做类型断言的 adaptor
    }
    return registered, nil
}
```

实现一个具体 watchdog，只需要五行：

```go
type DownloadsWatchdog struct{ Handled []s06.DownloadStartedEvent }

func (w *DownloadsWatchdog) OnDownloadStartedEvent(ctx context.Context, e *s06.DownloadStartedEvent) error {
    w.Handled = append(w.Handled, *e)
    return nil
}
```

## How It Works / 工作原理

```
┌──────────────────────────────────────────────────────────────────────┐
│                            startup                                   │
│                                                                      │
│   bus := NewEventBus()                                               │
│   dl  := &DownloadsWatchdog{}                                        │
│   pp  := &PopupsWatchdog{}                                           │
│                                                                      │
│   AutoAttach(dl, bus)  ─── reflect ─→  Subscribe("DownloadStarted... │
│   AutoAttach(pp, bus)  ─── reflect ─→  Subscribe("JSDialogOpened...  │
│                                                                      │
│   handlers map:                                                      │
│     "DownloadStartedEvent" → [adaptor → dl.OnDownloadStartedEvent]   │
│     "JSDialogOpenedEvent"  → [adaptor → pp.OnJSDialogOpenedEvent]    │
├──────────────────────────────────────────────────────────────────────┤
│                            run loop                                  │
│                                                                      │
│   bus.Emit(ctx, DownloadStartedEvent{URL: "x"})                      │
│         │                                                            │
│         ▼                                                            │
│   EventBus.Emit                                                      │
│     ┌──────────────────────────────┐                                 │
│     │ RLock                        │                                 │
│     │ snapshot handlers["Down..."] │  (在锁内拷一份切片，handler   │
│     │ RUnlock                      │   的实际工作放到锁外)         │
│     │ for h in snapshot:           │                                 │
│     │   err := h(ctx, e)           │  ← 反射 adaptor 调到           │
│     │   collect err                │     dl.OnDownloadStartedEvent   │
│     │ return errors.Join(errs)     │                                 │
│     └──────────────────────────────┘                                 │
│                                                                      │
│   bus.Emit(ctx, NavigationEvent{...})                                │
│         │                                                            │
│         ▼ (没有订阅者)                                              │
│   EventBus.Emit 返回 nil —— 无监听者的事件直接丢弃。                │
└──────────────────────────────────────────────────────────────────────┘
```

**四个非显然的点**：

1. **为什么是反射 auto-attach，而不是显式 `bus.Subscribe(...)`？** 三个原因。(a) **防拼写错**——方法名后缀必须等于参数 struct 名（`OnFooEvent(ctx, *FooEvent)`）；总线拒绝注册 `OnFooEvent(ctx, *BarEvent)`。显式订阅只能在 Emit 真正派发时才暴露这种错配。(b) **零样板**——加一个新事件订阅就是"加一个方法"，不是"加一个方法 + 记得订阅 + 记得反订阅"。(c) **与上游 Python 直接对应**——Python 同样以 `on_EventName` 形状为契约。读者同时阅读两个仓库时心智模型一致。

2. **为什么 Emit 内同步调用 handler，而不是异步丢给 worker goroutine 或 channel？** 调用者经常需要 handler **跑完**才能继续——上游 Agent 触发 `ClickEvent` 后要等 DOM watchdog 抓完点击后的快照，再读那份快照。缓冲 channel 会立即返回，于是 Agent 跟快照赛跑。同步派发 + 单 handler 错误把契约说清楚："Emit 返回后，每个 handler 都已结束，错误也都拿到了"。

3. **为什么 `sync.RWMutex` 守护 `map[string][]Handler`，而不是给每个 topic 开一个 `chan Event`？** 两点。(a) **snapshot-then-invoke** 让 handler 的工作跑在锁**外**——一个 3 秒的下载 handler 不会卡住其它人订阅。每个 topic 一条 channel + goroutine 扇出也能给同样性质，但要额外管理 goroutine 生命周期。(b) **没有队列就没有 backpressure 选择**——也就没有缓冲溢出的问题。每次 Emit 都是"扇出 + 等待"，节奏由调用方控制（你别比消费者快就行）。对教学范围来说，这是优点。

4. **为什么用 `errors.Join` 而不是遇到第一个错误就返回？** 两个 watchdog 订阅同一事件，每个都应该有机会。一个偶发出错的下载 handler 不能把待接受的弹窗悄悄吞了——弹窗 handler 仍然得拿到事件。`errors.Join` 保留所有失败，调用方可以用 `errors.Is` 在 join 里找特定 sentinel error。

### 约 50 行核心代码

热路径很小：`Subscribe`（4 行）、`Emit`（12 行）、`AutoAttach` 匹配循环（约 25 行），再加闭包体（约 10 行）。AutoAttach 末尾的反射 adaptor 是最绕的一段：

```go
bus.Subscribe(eventName, func(ctx context.Context, e Event) error {
    ev := reflect.ValueOf(e)
    var ptr reflect.Value
    if ev.Kind() == reflect.Ptr {
        ptr = ev
    } else {
        ptr = reflect.New(ev.Type())
        ptr.Elem().Set(ev)
    }
    out := methodVal.Call([]reflect.Value{reflect.ValueOf(ctx), ptr})
    if errVal := out[0]; !errVal.IsNil() { return errVal.Interface().(error) }
    return nil
})
```

那个"把值类型升格成指针"的小动作让调用者既能 `Emit(ctx, DownloadStartedEvent{...})` 又能 `Emit(ctx, &DownloadStartedEvent{...})`——两种都行，因为 handler 方法签名一定收指针。便利性，不是正确性。

## What Changed / 与上一节的变化

s05 的 Element actor 直接对（桩）CDP 客户端说话：

```diff
- // s05 风格：副作用内联在 actor 方法里
- func (e *Element) Click(ctx context.Context) error {
-     // 记一帧 CDP 调用
-     // ... 但是怎么"顺便"通知"下载可能开始了"？
-     // ... 又怎么"顺便"通知"弹窗可能弹了"？
-     return e.client.DispatchMouseEvent(...)
- }
```

s06 之后，副作用搬到 **watchdog 订阅者**：

```diff
+ // s06 风格
+ bus := NewEventBus()
+ AutoAttach(&DownloadsWatchdog{}, bus)
+ AutoAttach(&PopupsWatchdog{}, bus)
+
+ // 点击 action 只需要发一个事件：
+ bus.Emit(ctx, ClickEvent{Index: 5})
+ // 下载 handler 自己反应。弹窗 handler 自己反应。它们彼此
+ // 不需要互知——而点击代码也完全不知道它们的存在。
```

关键新增能力：**通过发射解耦**。s05 的 actor 必须知道它所有事件的消费者；s06 的 actor 只认识"总线"。新增一个 watchdog 不动一行 actor 代码；移除一个也不会留下悬挂引用。

s07（browser-session）会复用这两件东西：一个 `BrowserSession` 持有总线和 watchdog 列表，启动时对每个 watchdog 调 `AutoAttach`。形状一直贯穿。

## Try It / 动手试一试

```bash
cd agents/s06-watchdog-pattern

# 自动注册打印 + 5 次 emit + 摘要
GOWORK=off go run ./cmd/demo

# 6 个测试
GOWORK=off go test -v ./...
```

（`GOWORK=off` 是因为父仓库 `go.work` 在并行写入的兄弟节里被多个 agent 共享；本模块自己有 `go.mod`，不进 workspace 也跑得动。）

期望输出（与 `testdata/expected.txt` 一致）：

```
# auto-registered handlers
DownloadsWatchdog → [DownloadStartedEvent]
PopupsWatchdog    → [JSDialogOpenedEvent]

# emit one event of each type
emitted DownloadStartedEvent
emitted DownloadStartedEvent
emitted JSDialogOpenedEvent
emitted JSDialogOpenedEvent
emitted NavigationEvent

# summary
downloads handled: 2
  - foo.pdf (https://example.com/foo.pdf)
  - bar.zip (https://example.com/bar.zip)
dialogs handled:   2
  - [confirm] "save changes?" → OK
  - [prompt] "your name?" → Cancel
navigation handlers: 0 (unsubscribed events drop silently)
```

测试覆盖：

- `TestEmitCallsHandler`——单个订阅 handler 触发并拿到事件负载。
- `TestAutoRegisterByMethodName`——形状正确的 `OnDownloadStartedEvent` 被注册；无关辅助方法 `LoggerHelper()` 被忽略；签名错配的 `OnNavigationEvent(*DownloadStartedEvent)` 被"后缀-与-参数-struct-名 必须一致"那条规则挡住。
- `TestConcurrentEmitsNoDeadlock`——100 个 goroutine 并发对同一事件 Emit；两个 handler 各被精确触发 100 次，不死锁。
- `TestUnknownEventIgnored`——发射没有订阅者的 `NavigationEvent` 返回 nil。
- `TestMultipleHandlersOrderedRegistration`——同一事件的三个 handler 按订阅顺序 `first → second → third` 触发。
- `TestHandlerErrorsAggregated`——三个 handler 里两个返回错误时，`errors.Is` 在 join 里能找到两个 sentinel。

## Upstream Source Reading / 上游源码阅读

下面是上游 `BaseWatchdog.attach_to_session()` 的节选（`browser_use/browser/watchdog_base.py#L243-L281`）。这就是我们 `AutoAttach` 直译的那段 Python 反射循环。

```python
def attach_to_session(self) -> None:
    """Attach watchdog to its browser session and start monitoring."""
    assert self.browser_session is not None, '...'
    from browser_use.browser import events

    event_classes = {}
    for name in dir(events):
        obj = getattr(events, name)
        if inspect.isclass(obj) and issubclass(obj, BaseEvent) and obj is not BaseEvent:
            event_classes[name] = obj

    # Find all handler methods (on_EventName)
    registered_events = set()
    for method_name in dir(self):
        if method_name.startswith('on_') and callable(getattr(self, method_name)):
            # Extract event name from method name (on_EventName -> EventName)
            event_name = method_name[3:]  # Remove 'on_' prefix

            if event_name in event_classes:
                event_class = event_classes[event_name]

                # ASSERTION: If LISTENS_TO is defined, enforce it
                if self.LISTENS_TO:
                    assert event_class in self.LISTENS_TO, (...)

                handler = getattr(self, method_name)
                self.attach_handler_to_session(self.browser_session, event_class, handler)
                registered_events.add(event_class)
```

**六条阅读笔记**：

1. **`dir(events)` 在 attach 时构建事件查找表**——`browser_use/browser/events.py` 里所有 `BaseEvent` 子类都自动入选。我们 Go 版完全跳过这张映射表：直接从 handler 的参数类型推断它监听的事件 struct。这是"按名字发现"换成"按签名发现"——后者多一点编译期保障。

2. **`method_name[3:]` 即 `on_EventName → EventName`。** Go 版用 `strings.TrimPrefix(name, "On")` 替代。差别只是 CamelCase 与 snake_case。

3. **`if event_name in event_classes` 静默跳过不认识的名字。** 像 `_log_pretty_path` 这样的辅助方法自然被排除——根本没有 `_log_pretty_path` 事件类。Go 版同样静默：形状不匹配的方法不警告、直接略过。

4. **`LISTENS_TO` 声明后会被强校验。** 这是 Python 的"文档必须和现实一致"——如果你声明监听 `[FooEvent]` 却没写 `on_FooEvent`，assert 会炸。Go 版我们**丢掉**这一层：声明 `LISTENS_TO` 切片只多仪式、不多保障。Go 的形状本身就把"文档"绑在了"现实"上——你的方法参数**就是**你监听的事件。

5. **`attach_handler_to_session` 把绑定方法包进 `unique_handler`**（上游 L93-L207）。这个包装做了 (a) CDP 断开时的电路断路、(b) 父事件追踪供调试日志、(c) handler 失败时尝试修复 CDP session。Go 版三件都不做：(a) 是"副作用-vs-副作用"那种工程关切，我们不模拟；(b) 是观测噪音；(c) 属于 s07 的会话生命周期，不是总线职责。

6. **`browser_use/browser/watchdogs/*.py` 里没有任何显式 `event_bus.on(...)` 调用。** 每个 watchdog 文件只定义 `on_XxxEvent` 方法，框架自己找。这种"零仪式"形状正是这个模式存在的全部务实理由——见上文 §3 第 1 点。Go 版精确复刻：`watchdogs/downloads.go` 和 `watchdogs/popups.go` 里没有一行 `Subscribe`，只有 handler 方法。

完整带注释的节选（含 class 外壳与完整的 attach 循环）参见 `upstream-readings/s06-watchdog-pattern.py`。
