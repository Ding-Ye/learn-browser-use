---
title: "s07 · 浏览器会话"
chapter: 7
slug: s07-browser-session
est_read_min: 15
---

# s07 · 浏览器会话

> 教什么：s05 给了我们 stub `CDPClient`，s06 给了我们 `EventBus` + `Watchdog`，但这两条线还互不相干。s07 把它们焊在一个 `BrowserSession` 里——一个容器，拥有 client、拥有 bus、拥有一组 watchdog，并提供 `Start` / `Stop` / `Restart` / `IsRunning` 这套生命周期 API。这一节大约 500 行 Go，里面 80% 是测试和文档；核心 session.go 只有 ~80 行。

---

## Problem / 问题

在 s06 结束时我们手里的东西是这样的：

```go
// s06 的典型用法
bus := NewEventBus()
wd1 := &DownloadsWatchdog{...}
wd2 := &PopupsWatchdog{...}
AutoAttach(bus, wd1)
AutoAttach(bus, wd2)
// ... 调用方自己记得后续清理
```

`bus` 是裸的，`wd1/wd2` 是散养的，连接 CDP 的逻辑（s05 的 `RecordingCDPClient`）和这两个东西没有关系。三件事散在三个地方，调用方要自己拼成一个完整的"会话"。

更具体的痛点：

1. **没人拥有 lifecycle**：bus 和 watchdog 都不知道 CDP 连了没；CDP client 也不知道 watchdog 是否已经订阅。如果谁先死了，另外两个会泄漏。
2. **没有幂等的 Start**：调用方要自己实现"已经启动了就别重复连"的判断——上游 Python 的 `on_BrowserStartEvent` 直接在 docstring 里写"This method is idempotent"，意味着这是真实世界会反复踩的坑。
3. **没有显式的 Stop**：watchdog handler 永远挂在 bus 上，下次 `Start` 时会和新的 watchdog 重复订阅，事件被处理两遍。
4. **缺一个 `Restart`**：调用方真的会需要"出错后重置一切再来一次"，但散养版本里这个动作是手动 6 行代码。

s07 解决这四件事，办法非常 Go：用一个 struct 把三个组件焊在一起，方法签名暴露 lifecycle。

## Solution / 解决方案

引入 `BrowserSession`，一个组合容器：

```go
type BrowserSession struct {
    Client    CDPClient   // stub CDP（来自 s05 概念，本节本地重声明）
    Bus       *EventBus   // 事件总线（来自 s06 概念，本节本地重声明）
    Watchdogs []Watchdog  // 一组 watchdog
    Started   bool
}
```

并暴露四个生命周期方法：

| 方法 | 责任 | 上游对照 |
|---|---|---|
| `Start(ctx)`     | 打开 stub CDP（送 `Target.attachToTarget` 帧）+ 把所有 watchdog AutoAttach 到 bus + 发 `SessionStartedEvent`。**幂等**。 | `BrowserSession.start()` + `on_BrowserStartEvent` |
| `Stop(ctx)`      | 发 `SessionStoppedEvent` + 送 `Target.detachFromTarget` + 清空 bus 订阅。**幂等**。 | `BrowserSession.stop()` |
| `Restart(ctx)`   | Stop 然后 Start。整个状态机重置一次。 | 调用方自己用 `await session.stop(); await session.start()` 组合 |
| `IsRunning()`    | `Started` 的读取入口。预留出未来加锁/加 ctx 的空间。 | `is_cdp_connected` 属性的精简版 |

为了 self-contained，本节本地重声明了三个上一节才出现的东西——这是 learn-browser-use 的硬性约束（无跨 session import）：

| 本地文件 | 重声明的概念 | 上一节出处 |
|---|---|---|
| `eventbus.go`     | `EventBus`（Subscribe/Emit/HandlerCount/Clear）            | s06 |
| `watchdog.go`     | `Watchdog` 接口 + `AutoAttach` 反射                         | s06 |
| `cdp_client.go`   | `CDPClient` 接口 + `RecordingCDPClient` 录制器              | s05 |

注意 `AutoAttach` 比 s06 的版本多了一层 **JSON 桥接**——因为 `watchdogs/example.go` 是子包，不能 import 父 main 包，所以它声明的事件 struct 和父包的不是同一个 Go 类型。AutoAttach 用 `json.Marshal` + `json.Unmarshal` 把发出的事件复制到 watchdog 期望的参数类型里。两边只要字段名一致就能跑通。

## How It Works / 工作原理

```
┌─────────────────────────────────────────────────────────────────┐
│                      session = NewBrowserSession                │
│   ┌─────────────┐    ┌─────────┐    ┌────────────────────┐      │
│   │ CDPClient   │    │ EventBus│    │ Watchdogs []        │     │
│   │ (Recording) │    │         │    │   LoggingWatchdog   │     │
│   └─────────────┘    └─────────┘    └────────────────────┘      │
└─────────────────────────────────────────────────────────────────┘

  session.Start(ctx)
  ─────────────────►
                                                                       Started=false→true
       1. Client.Send("Target.attachToTarget", {...})   ──► Frames[0]
       2. for w in Watchdogs: AutoAttach(Bus, w)
            ├─ 反射找 "On*" 方法
            ├─ 用方法名后缀作为 event key
            └─ 给每个 handler 注册一个 JSON-bridge 闭包
       3. Bus.Emit(ctx, SessionStartedEvent{CDPURL:...}) ─► watchdog.OnSessionStartedEvent()

  session.Navigate(ctx, "https://example.com")
  ───────────────────────────────────────────►
       Bus.Emit(ctx, NavigationEvent{URL:...})         ─► watchdog.OnNavigationEvent()

  session.Stop(ctx)
  ─────────────────►                                                   Started=true→false
       1. Bus.Emit(ctx, SessionStoppedEvent{...})       ─► watchdog.OnSessionStoppedEvent()
       2. Client.Send("Target.detachFromTarget", {...}) ──► Frames[1]
       3. Bus.Clear()                                                  handlers = {}
```

核心代码（约 50 行）：

```go
// session.go
func (s *BrowserSession) Start(ctx context.Context) error {
    s.mu.Lock()
    if s.Started { s.mu.Unlock(); return nil }     // ← 幂等

    if _, err := s.Client.Send("Target.attachToTarget", map[string]any{
        "targetId": "stub-target-0",
        "flatten":  true,
    }); err != nil {
        s.mu.Unlock()
        return fmt.Errorf("session start: %w", err)
    }
    for _, w := range s.Watchdogs {                // ← 一次性 attach 所有 watchdog
        AutoAttach(s.Bus, w)
    }
    s.Started = true
    s.mu.Unlock()
    return s.Bus.Emit(ctx, SessionStartedEvent{CDPURL: "stub://recorder"})
}

func (s *BrowserSession) Stop(ctx context.Context) error {
    s.mu.Lock()
    if !s.Started { s.mu.Unlock(); return nil }    // ← 幂等
    s.Started = false
    s.mu.Unlock()

    emitErr := s.Bus.Emit(ctx, SessionStoppedEvent{Reason: "Stop() called"})
    _, sendErr := s.Client.Send("Target.detachFromTarget", map[string]any{
        "sessionId": "stub-session-0",
    })
    s.Bus.Clear()                                  // ← 上游 self.event_bus = EventBus() 的对应

    if emitErr != nil { return emitErr }
    if sendErr != nil { return fmt.Errorf("session stop: %w", sendErr) }
    return nil
}
```

**4 个非显然之处**：

1. **为什么是 `Started` 字段而不是 sync.Once？** `sync.Once` 只能跑一次；session 要支持 Start → Stop → Start 循环（即 Restart）。我们要的是"已经启动了就跳过"，不是"一辈子只能启动一次"。
2. **为什么 Emit `SessionStoppedEvent` 在 `Bus.Clear()` 之前？** 因为 Clear 之后再 Emit，没有任何 watchdog 能收到——watchdog 的清理 handler 就永远不跑。顺序是：先广播"我要停了，你们清理"，再清空 bus。
3. **为什么 AutoAttach 在循环里反复调用而不在 NewBrowserSession 里做一次？** 因为 `Stop` 会 Clear bus（清掉所有订阅）。如果 attach 只在 New 时做一次，下次 Start 时 bus 空空如也，watchdog 全部失联。每次 Start 重新 attach 是保证状态机干净循环的代价，也是上游 `attach_all_watchdogs()` 反复跑的原因。
4. **为什么用 JSON 桥接而不是同一个事件类型？** 因为 `watchdogs/example.go` 是子包，不能 import 父 main 包（Go 的循环依赖禁令）。两边各声明一份字段名相同的 struct，AutoAttach 在中间做一次 marshal/unmarshal。代价是每个事件多一次 JSON 序列化（亚微秒级），收益是子包真的能独立 build。在生产代码里你会把 event 类型提到一个共享子包，但 s07 的教学目标是讲清楚"session 拥有这套东西"，所以我们接受这点 overhead 来换文件可读性。

## What Changed / 与上一节的变化

s06 风格：

```diff
- // s06 用法
- bus := NewEventBus()
- wd := &MyWatchdog{...}
- AutoAttach(bus, wd)
- bus.Emit(ctx, MyEvent{...})
- // ... 调用方自己拼 lifecycle / 自己记得清理
```

s07 之后：

```diff
+ session := NewBrowserSession(client, wd1, wd2)
+ defer session.Stop(ctx)            // ← 一行兜底
+ if err := session.Start(ctx); err != nil { return err }
+ session.Navigate(ctx, url)         // ← 通过 session 路由事件
+ // session.IsRunning() 暴露状态
+ // session.Restart(ctx) 一行重置
```

关键性增量：**lifecycle 第一次被显式封装**。s01-s06 的所有 mock 例子里，调用方都得自己拼"启动→工作→关闭"，且每次都拼得不一样。s07 后这件事变成 4 行 API 的事，且每次拼法相同。这就是从"事件总线模式"到"会话对象模式"的分水岭。

后续会反复用到这一节的东西：
- s09 的 `DOMService` 会订阅 `NavigationEvent` 来失效 snapshot cache。
- s12 的 Agent 会拥有一个 `BrowserSession` 实例并在 `agent.Run()` 头尾包 `session.Start` / `session.Stop`。

## Try It / 动手试一试

```bash
cd agents/s07-browser-session

# 看完整的 session lifecycle 演示
GOWORK=off go run .

# 6 个测试
GOWORK=off go test -v ./...
```

`GOWORK=off` 只是因为根目录 `go.work` 还没把 s07 加进去；模块本身是自洽的。

期望输出（节选）：

```
# session lifecycle demo

Initial state: IsRunning=false
After Start:   IsRunning=true  handlers(SessionStartedEvent)=1
After Stop:    IsRunning=false  handlers(SessionStartedEvent)=0

# CDP frame log (recorded by the stub client)
[0] Target.attachToTarget map[flatten:true targetId:stub-target-0]
[1] Target.detachFromTarget map[sessionId:stub-session-0]

# Watchdog event log (recorded by LoggingWatchdog)
[0] started: cdp=stub://recorder
[1] navigate: url=https://example.com
[2] stopped: reason=Stop() called
```

测试覆盖：

- `TestStartOpensStubCDP` — Start 必须送 `Target.attachToTarget` 帧。
- `TestWatchdogsAttachOnStart` — Start 之后 bus 上每种事件都恰好 1 个 handler。
- `TestStopDisconnectsCleanly` — Stop 必须广播 `SessionStoppedEvent` 并送 detach 帧。
- `TestRestartWorks` — Stop+Start 之后状态机回到 running，bus 重新有所有 handler。
- `TestStartIdempotent` — 调两次 Start 不会让 attach 帧或 handler 数翻倍。
- `TestNavigateRoutesThroughBus`（加分）— `session.Navigate` 在 Start 之前要拒绝，Start 之后要能被 watchdog 看到。

## Upstream Source Reading / 上游源码阅读

上游 `browser_use/browser/session.py` 的 `BrowserSession` 类本身 ~4000 行——绝大多数都是 cloud 集成、profile 处理、target 多路复用、reconnect 逻辑等。s07 只取最骨架的 lifecycle 部分。

```python
# Source: browser_use/browser/session.py#L101-L130, L502-L535

class BrowserSession(BaseModel):
    """Event-driven browser session with backwards compatibility."""

    model_config = ConfigDict(
        arbitrary_types_allowed=True,
        validate_assignment=True,
        extra='forbid',
        revalidate_instances='never',
    )

    # 这是 s07 Session.Bus 字段对应的上游字段。bus 是 session 拥有的，不是 agent 拥有的。
    event_bus: EventBus = Field(default_factory=EventBus)

    # Watchdog 槽位——上游每个 watchdog 一个具名字段；我们用 []Watchdog 切片。
    _crash_watchdog: Any | None = PrivateAttr(default=None)
    _downloads_watchdog: Any | None = PrivateAttr(default=None)
    _dom_watchdog: Any | None = PrivateAttr(default=None)
    # ... 共 12 个 watchdog 字段 ...
    _watchdogs_attached: bool = PrivateAttr(default=False)
```

```python
# Source: browser_use/browser/session.py#L672-L725

@observe_debug(ignore_input=True, ignore_output=True, name='browser_session_start')
async def start(self) -> None:
    """Start the browser session."""
    # ↓ 对应我们 Go 的 Start: Client.Send + AutoAttach + Bus.Emit。
    #   上游把三件事拢成一次事件 dispatch。
    start_event = self.event_bus.dispatch(BrowserStartEvent())
    await start_event
    await start_event.event_result(raise_if_any=True, raise_if_none=False)

async def stop(self) -> None:
    """Stop the browser session without killing the browser process."""
    self._intentional_stop = True

    # 先存储再停。我们 s07 没有 storage 层，所以这一步省了。
    save_event = self.event_bus.dispatch(SaveStorageStateEvent())
    await save_event

    # ↓ 对应我们 Bus.Emit(SessionStoppedEvent)
    await self.event_bus.dispatch(BrowserStopEvent(force=False))
    # ↓ 对应我们 Bus.Clear()
    await self.event_bus.stop(clear=True, timeout=5)
    await self.reset()
    # ↓ 对应我们 Bus.Clear() 之后下次 Start 重新订阅。
    self.event_bus = EventBus()


async def on_BrowserStartEvent(self, event: BrowserStartEvent) -> dict[str, str]:
    """Handle browser start request.

    Note: This method is idempotent - calling start() multiple times is safe.
    """
    # ↓ 对应我们 Start 里的 `for _, w := range s.Watchdogs { AutoAttach(...) }`
    await self.attach_all_watchdogs()

    # 下面这一段在 s07 完全省略：local/cloud/cdp_url 三分支。
    # s07 的"CDP"就是 RecordingCDPClient，没有真正要连的东西。
    try:
        if not self.cdp_url:
            if self.browser_profile.use_cloud:
                ...  # cloud browser
            elif self.is_local:
                ...  # 发 BrowserLaunchEvent，等 LocalBrowserWatchdog 启动浏览器
    except CloudBrowserError:
        raise
```

```python
# Source: browser_use/browser/session.py#L1593-L1696 (节选)

async def attach_all_watchdogs(self) -> None:
    # 模式：每个 watchdog 重复以下三步：
    #   1. cls.model_rebuild()  ← pydantic 的 late-binding 修复
    #   2. self._xxx_watchdog = XxxWatchdog(event_bus=self.event_bus, browser_session=self)
    #   3. self._xxx_watchdog.attach_to_session()  ← 反射找 on_* 方法注册到 bus

    DownloadsWatchdog.model_rebuild()
    self._downloads_watchdog = DownloadsWatchdog(event_bus=self.event_bus, browser_session=self)
    self._downloads_watchdog.attach_to_session()

    LocalBrowserWatchdog.model_rebuild()
    self._local_browser_watchdog = LocalBrowserWatchdog(event_bus=self.event_bus, browser_session=self)
    self._local_browser_watchdog.attach_to_session()

    # ... 再重复 10 次 ...

    self._watchdogs_attached = True
```

**对照阅读要点**：

1. **`start()` → 派事件 → `on_BrowserStartEvent` → `attach_all_watchdogs()`**：上游用一层间接（事件）解耦了"用户调用 start"和"实际要干什么"。同一个 `start()` 入口可以支持 local 浏览器、cloud 浏览器、外部 CDP URL 三种启动方式——每种走自己的 handler 即可。我们 Go 没这层间接，直接在 `Session.Start` 里写 stub CDP 一行，因为 stub 只有一种启动方式。
2. **`@observe_debug` 装饰器**：是上游的 telemetry 钩子（laminar.so）。s10 教 token cost 时会再讲 observability；这里只是名字。
3. **`_watchdogs_attached: bool` 字段**：上游用这个标志位避免重复 attach。我们用 `Started bool` + Start 入口的 early-return 达成同样效果，少一个字段。
4. **`self.event_bus = EventBus()` in stop()**：直接换一个全新的 bus 对象。我们 Go 用 `Bus.Clear()` 而不是 `s.Bus = NewEventBus()`，因为字段被外部代码持有指针——换对象会让 demo 里 `session.Bus.HandlerCount` 那行误指向被废弃的实例。一行差异，但模型不一样。
5. **`reset()` 那 50 行**：上游 reset 要 cancel reconnect 任务、清 cdp client、清 session_manager、清 12 个 watchdog 字段。我们只要把 `Started=false` 即可——`Bus.Clear()` 已经管了订阅。重置成本不同是因为承载的状态不同。
6. **`kill()` 和 `stop()` 区别**：上游 stop 是"温柔停"，kill 是"立刻杀"（含 force=True）。s07 只做了 stop，因为 stub 没有"杀进程"这件事。

**想读更多**：从 `BrowserSession.__init__`（150 行）入手看初始字段，再跳进 `model_post_init`（line 642）看为什么有 8 个 `BaseWatchdog.attach_handler_to_session` 调用——那是 session 自己也是个隐式 watchdog 的证据；它给自己注册了 BrowserStart/Stop/Navigate 等核心事件的处理函数。这条线直接连到 s12 的 Agent，会在那里看到 `agent.session.start()` 是 `agent.run()` 的第一步。

---

**下一节预告**：s08 引入 DOM 序列化器——`DOMNode` 树 + LLM-friendly 文本输出 + selector map。s08 不直接依赖 s07 的 session（DOMNode 是个纯数据 transformer），但 s09 会把序列化器接到 session 上：navigation 事件失效缓存的那一刻，就是 s07 给 s09 的真正基础。
