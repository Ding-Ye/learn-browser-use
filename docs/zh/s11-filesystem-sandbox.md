---
title: "s11 · 文件系统沙箱"
chapter: 11
slug: s11-filesystem-sandbox
est_read_min: 12
---

# s11 · 文件系统沙箱

> 教什么：`read_file` 和 `write_file` 这种工具把磁盘交给 LLM。没有沙箱，agent 早晚会被要求去读 `~/.ssh/id_rsa`，或者把一段 Base64 二进制写成 `screenshot.png`。s11 在每次磁盘操作前面架一道 `FileSystem` 接口，配合 `LocalFileSystem` 实现，强制三层独立安全检查加大小限制。

---

## Problem / 问题

s04 之后我们有了一个能跑的 tool registry。s12 之后会有一个 agent loop 把 LLM 输入直接送给工具实现。最自然——也最危险——的 `read_file` 实现长这样：

```go
// 不要这样写
func readFile(path string) (string, error) {
    b, err := os.ReadFile(path)
    return string(b), err
}
```

它打开了三类具体的攻击或 bug 场景，从最离谱到最常见：

1. **直接路径穿越**。LLM 拿到一个模糊的任务，问出 `read_file("/etc/passwd")` 或 `read_file("../../home/yeding/.ssh/id_rsa")`。主机磁盘完全可寻址，agent 进程继承了用户文件权限，没人拦得住这次读取。
2. **二进制文件混入**。LLM 提议 `write_file("photo.png", "<base64 blob>")`。我们写下去。现在 agent 的"工作目录"里多了一个看起来是 PNG、其实不是的文件。更糟的是，agent 之后如果 `read_file("photo.png")`，那段 Base64 转字符串会塞进 LLM 的 context window。垃圾进，幻觉出。
3. **内存爆炸**。LLM 执行 `read_file("very_large.log")`，我们 `io.ReadAll` 把 2GB 文件读成一个 string。进程 OOM——或者更糟，没 OOM，而下一次 prompt 携带这 2GB 日志，花掉 40 美元送给模型。

s11 把这三件事一次关掉。结构是：所有工具都消费同一个接口，由实现去执行规则，再加一个允许配置无法绕开的硬性 deny-list。

## Solution / 解决方案

引入 `FileSystem`：

```go
type FileSystem interface {
    ReadFile(ctx context.Context, relPath string) (string, error)
    WriteFile(ctx context.Context, relPath string, content string) error
    Exists(ctx context.Context, relPath string) bool
    List(ctx context.Context, relPath string) ([]string, error)
}
```

以及 `LocalFileSystem`：

```go
type LocalFileSystem struct {
    Root        string
    MaxBytes    int64
    AllowedExts []string
}
```

四个方法承担全部职责：

| 方法 | 责任 | 拒绝条件 |
|---|---|---|
| `ReadFile(ctx, rel)`         | 读文件内容为字符串 | 路径不安全 / 扩展名被拒 / payload > MaxBytes |
| `WriteFile(ctx, rel, body)`  | 原子写（tempfile + rename） | 路径不安全 / 扩展名被拒 / body > MaxBytes |
| `Exists(ctx, rel)`            | 不会出错的存在性检查 | （遇到不安全路径返回 false，绝不报错） |
| `List(ctx, rel)`              | 排序后的目录列表 | 路径不安全 |

`safety.go` 里两个帮手函数干真正的活：

- `IsSafePath(root, rel) error`——三层检查：(1) 原始输入里有任何 `..` 段都拒绝；(2) `filepath.Clean` 之后 join 出来的路径前缀必须仍在 root 之下；(3) `filepath.EvalSymlinks` 解析出最深存在的祖先后，也必须仍在 root 之下。每一层都能抓到其它层漏掉的攻击类型。
- `IsAllowedExt(name, allowed) bool`——扩展名转小写后，先过硬编码 binary deny-list 一票否决，再对照 `allowed`。`DefaultAllowedExts` 是 `{txt, md, json, jsonl, csv, html, xml, yaml, yml}`；deny-list 永远包含 `{png, jpg, jpeg, gif, bmp, svg, webp, ico, mp3, mp4, wav, avi, mov, zip, tar, gz, rar, exe, bin, dll, so, dylib}`。

## How It Works / 工作原理

```
   tool.run(input)                                       (s12)
   ────────────────►
        例如 {"path": "../../etc/passwd", "content": ""}
                  │
                  ▼
   fs.ReadFile(ctx, input.path)
                  │
                  ├── ctx.Err()              ── 已取消？退出
                  ├── IsSafePath(Root, rel)  ── 三层检查
                  │     ├── Layer 1: "../etc/passwd" 含 `..` 段          → REJECT
                  │     ├── Layer 2: filepath.Join + Clean，前缀校验
                  │     └── Layer 3: 最深存在祖先上的 filepath.EvalSymlinks
                  ├── IsAllowedExt(name)     ── .png? 永久 deny-list
                  ├── os.Open(full)
                  ├── io.LimitReader(f, MaxBytes+1)
                  ├── io.ReadAll(limited)
                  └── if len(buf) > MaxBytes: REJECT（大小上限）

   ────────────────────────────────────────────────────────────────

   fs.WriteFile(ctx, rel, body)
   ─────────────────────────►
        同样的前缀检查 +
                  ├── len(body) > MaxBytes → REJECT
                  ├── os.CreateTemp(dir, ".tmp-NAME-*")
                  ├── tmp.WriteString(body)
                  ├── tmp.Close()
                  └── os.Rename(tmp, full)             ← 原子
```

核心代码（~50 行）：

```go
// safety.go（节选）
func IsSafePath(root, rel string) error {
    if rel == "" || filepath.IsAbs(rel) {
        return fmt.Errorf("%w: %q", ErrUnsafePath, rel)
    }
    // Layer 1：原始输入里的任何 `..` 段。
    for _, seg := range strings.Split(rel, "/") {
        if seg == ".." {
            return fmt.Errorf("%w: traversal in %q", ErrUnsafePath, rel)
        }
    }
    // Layer 2：anchored prefix 检查。
    absRoot, _ := filepath.Abs(filepath.Clean(root))
    joined := filepath.Join(absRoot, filepath.Clean(rel))
    if !strings.HasPrefix(joined, absRoot+string(filepath.Separator)) {
        return fmt.Errorf("%w: escapes root", ErrUnsafePath)
    }
    // Layer 3：往上找最深存在的祖先，EvalSymlinks，再校验。
    probe := joined
    for {
        resolved, err := filepath.EvalSymlinks(probe)
        if err == nil {
            absResolvedRoot, _ := filepath.EvalSymlinks(absRoot)
            if !strings.HasPrefix(resolved, absResolvedRoot+string(filepath.Separator)) {
                return fmt.Errorf("%w: symlink escapes root", ErrUnsafePath)
            }
            return nil
        }
        parent := filepath.Dir(probe)
        if parent == probe {
            return fmt.Errorf("%w: unresolvable", ErrUnsafePath)
        }
        probe = parent
    }
}
```

**4 个非显然要点**：

1. **只看 `..` 字面不够——`EvalSymlinks` 才是承重的第三层**。攻击者可以在沙箱里放一个 `notes.txt` 软链接指向 `/etc/passwd`，然后要求读 `notes.txt`。Layer 1（原始 segment 检查）看不到 `..`，因为 `notes.txt` 里根本没有。Layer 2（`filepath.Join` + 前缀检查）也过——`notes.txt` 看起来就是 root 之内的路径。只有 Layer 3 抓得住。我们走到最深存在的祖先才调 `EvalSymlinks`，因为这个函数对不存在的路径会报错，而我们还需要校验**写入**目标（那时文件尚未创建）。
2. **`io.LimitReader` 而不是 `os.Stat` + `os.Open`**。Stat-then-open 是 TOCTOU 竞态：Stat 看到的文件大小不一定等于 Open 之后的真实大小。恶意写入者可以在两次 syscall 之间把文件撑大。`os.Stat` 对 procfs / pipe / socket 也不靠谱——它们 `Size=0`，但读起来可以吐出几 MB。模式是 `io.ReadAll(io.LimitReader(f, cap+1))` 然后判 `len(buf) > cap`。多读一个字节然后看切片长度是最便宜的信号；我们甚至不会为超出预算的那部分分配内存。
3. **binary deny-list 硬编码在 `IsAllowedExt` 里，不让外面配**。如果调用方传 `AllowedExts: []string{".png", ".txt"}`，deny-list 仍然赢——`.png` 还是被拒。理由是 defense-in-depth：配置错误（手滑、从错误的教程粘贴、测试 fixture 漏到 production）不能重新打开 `.png` 写入。上游的 `UNSUPPORTED_BINARY_EXTENSIONS` 放在模块级，也是同一个理由。
4. **写入通过同目录 tempfile + Rename 实现原子性**。`os.WriteFile` 是一个 open-truncate-write 的 fd，写到一半崩了就留下半成品。下一次 `ReadFile` 看到的就是垃圾。正确做法是 `os.CreateTemp(dir, ".tmp-NAME-*")` → 写 → 关 → `os.Rename(tmp, full)`。POSIX 保证 `rename` **仅在同一文件系统内**原子——"在同一目录"这一条是关键，能跨 mount 工作（比如 `/tmp` 是 tmpfs，沙箱在 `/Users` 上）。

## What Changed / 与上一节的变化

s10 风格（token-cost，不碰磁盘）：

```diff
- // 工具实现里直接调 os.ReadFile / os.WriteFile
- func readFile(path string) (string, error) {
-     return os.ReadFile(path)   // 没沙箱，没扩展名检查，没大小上限
- }
```

s11 之后：

```diff
+ // 每个碰磁盘的工具都接收一个 FileSystem 依赖
+ type ReadFileTool struct {
+     fs FileSystem                          // 构造时注入
+ }
+
+ func (t *ReadFileTool) Run(ctx context.Context, in json.RawMessage) (string, error) {
+     var args struct{ Path string `json:"path"` }
+     _ = json.Unmarshal(in, &args)
+     return t.fs.ReadFile(ctx, args.Path)    // 所有安全逻辑在 fs 里
+ }
```

关键变化：**工具不再持有安全策略**。`ReadFile` 背后可以是 `LocalFileSystem`，也可以是测试用的内存 map，也可以是远端对象存储——工具不关心。安全只活在一个地方。这正是把边界放在接口上、而不是放在调用点上的意义所在。

下游用法：
- s12 的 agent 一次性 `fs := NewLocalFileSystem("/tmp/agent-data")`，然后把它传给每个工具。五个工具，一份安全策略。
- 多 agent 部署可以为每个 agent 创建一个 `LocalFileSystem`（不同 root），安全保证天然组合：agent A 的工具构造上就无法到达 agent B 的沙箱。

## Try It / 动手试一试

```bash
cd agents/s11-filesystem-sandbox

# 4 步演示
GOWORK=off go run .

# 7 个测试（5 个必需 + 2 个佐证）
GOWORK=off go test -v ./...
```

`GOWORK=off` 是因为根目录的 `go.work` 还没把 s11 加进去；模块本身是自包含的。

期望输出（节选）：

```
# s11 — filesystem-sandbox demo
Sandbox root: /var/folders/.../s11-demo-XXXX

(a) write+read notes.txt — should succeed
  read back 46 bytes; first line: "TODO: learn sandboxing"
  also wrote subdir/plan.md

(b) write screenshot.png — should be refused
  refused: disallowed extension: .png

(c) read traversal attempts — all should be refused
  ../../etc/passwd                 -> refused: unsafe path: traversal segment in "../../etc/passwd" rejected
  subdir/../../etc/passwd          -> refused: unsafe path: traversal segment in "subdir/../../etc/passwd" rejected
  /etc/passwd                      -> refused: unsafe path: absolute path "/etc/passwd" rejected
```

测试覆盖：

- `TestPathTraversalRejected` —— 所有带 `..` 的变体外加绝对路径，都会返回包含 "unsafe" 的 error，且能匹配 `errors.Is(err, ErrUnsafePath)`。
- `TestBinaryExtensionBlocked` —— `.png`、`.jpg`、`.gif`、`.exe`、`.so`、`.bin`、`.svg` 全部拒掉；即便把 `AllowedExts` 放宽到 `[]string{".png"}`，deny-list 仍然赢。
- `TestValidTextFileWorks` —— `notes.txt` 上的 write+read 回路用 UTF-8（中文）内容；子目录回路；每个允许的文本扩展名都接收。
- `TestConcurrentWritesSafe` —— 100 个 goroutine 写 100 个不同路径（散布在 5 个子目录里，专门触发 `MkdirAll` 竞态）；全部成功，readback 全部对得上。
- `TestMaxSizeEnforced` —— 等于上限的写入成功；超出 1 字节返回 `errors.Is(err, ErrTooLarge)`；用 50 字节上限读一个 200 字节文件也以 `ErrTooLarge` 失败。

## Upstream Source Reading / 上游源码阅读

上游 `browser_use/filesystem/file_system.py` ~700 行：~150 行是 `BaseFile` + `MarkdownFile` / `TxtFile` / `JsonFile` / `CsvFile`（RFC 4180 规整）/ `PdfFile`（reportlab）/ `DocxFile`（python-docx）；其余是 `FileSystem` 编排器。s11 取了承重的两个切片：deny-list 和 sanitize-then-basename 模式。

```python
# 来源: browser_use/filesystem/file_system.py#L15-L73

UNSUPPORTED_BINARY_EXTENSIONS = {
    'png', 'jpg', 'jpeg', 'gif', 'bmp', 'svg', 'webp', 'ico',
    'mp3', 'mp4', 'wav', 'avi', 'mov',
    'zip', 'tar', 'gz', 'rar',
    'exe', 'bin', 'dll', 'so',
}
# ↑ 1:1 映射到我们 safety.go 里的 defaultBinaryDenylist。注意这是
#   模块级常量，**不是 per-instance** 可配置——和我们的 defense-in-depth
#   规则同源。


def _build_filename_error_message(file_name: str, supported_extensions: list[str]) -> str:
    """构造一条具体的 error message，解释为什么被拒并教 LLM 怎么修。"""
    base = os.path.basename(file_name)
    # ↑ os.path.basename 直接剥掉所有目录段。这是上游的路径穿越防线：
    #   到 basename 跑完，"../../etc/passwd" 变成 "passwd"。我们的
    #   IsSafePath 更严——我们 REJECT 输入而不是悄悄改写，因为悄悄
    #   改写会让攻击在日志里隐身。

    if '.' in base:
        _, ext = base.rsplit('.', 1)
        ext_lower = ext.lower()
        if ext_lower in UNSUPPORTED_BINARY_EXTENSIONS:
            return (
                f"Error: Cannot write binary/image file '{base}'. "
                f'The write_file tool only supports text-based files. '
                f'Supported extensions: {", ".join("." + e for e in supported_extensions)}. '
                f'For screenshots, the browser automatically captures them - do not try to save screenshots as files.'
            )
        # ↑ 错误消息是有意写给 LLM 看的。它解释**为什么**操作失败 + 提
        #   供工作流替代方案（"浏览器自动抓 screenshot"）。我们 Go 这边的
        #   error 更精简，因为 s12 工具层会把它翻译成 LLM 可读文本。
```

```python
# 来源: browser_use/filesystem/file_system.py#L451-L475

def _resolve_filename(self, file_name: str) -> tuple[str, bool]:
    """解析文件名，无效则尝试 sanitize。

    先 normalize 到 basename，防止目录穿越（比如 ../secret.md）。
    """
    base_name = os.path.basename(file_name)
    was_changed = base_name != file_name
    # ↑ 上游策略：取 basename 然后继续。我们 Go 这边策略：检测到 `..`
    #   就拒。两种不同哲学——上游悄悄改写（LLM 友好，"我帮你修"），
    #   我们拒绝（安全友好，"我把 bug 暴露出来"）。对教学仓库来说拒绝
    #   路径更有教学意义：测试可以精确断言 error；production 里要
    #   user-facing 提示词，两个方向都能在上面再叠一层。

    if self._is_valid_filename(base_name):
        return base_name, was_changed

    sanitized = self.sanitize_filename(base_name)
    if sanitized != base_name and self._is_valid_filename(sanitized):
        return sanitized, True

    return base_name, was_changed
```

完整的 60-100 行注解版在 `upstream-readings/s11-filesystem-sandbox.py`，包含 `_is_valid_filename` 的正则（限制字符为 `[a-zA-Z0-9_\-\.\(\) 一-鿿]` 加扩展名）以及 `FileSystem.__init__` 在构造时清空 data dir 的部分（我们的 `MkdirAll` 借鉴的是这种"启动即清理"的姿态，但去掉了破坏性那一步）。
