# s11 · Filesystem sandbox (filesystem-sandbox)

> Tools like `read_file` and `write_file` give the LLM disk access. Without a sandbox, the LLM can ask for `~/.ssh/id_rsa`. s11 puts a `FileSystem` interface in front of every disk op — `LocalFileSystem` rejects `..`, blocks binary extensions, caps payload size, and refuses symlink escapes.
> 像 `read_file` 和 `write_file` 这种工具会把磁盘交给 LLM。没有沙箱，LLM 就能去读 `~/.ssh/id_rsa`。s11 在每次磁盘操作前面架一道 `FileSystem` 接口——`LocalFileSystem` 拒绝 `..`、屏蔽二进制扩展名、限制 payload 大小、阻断 symlink 逃逸。

## What this teaches / 教什么

- **The sandbox is a boundary, not a feature.** Every disk-touching path in the agent funnels through `FileSystem.ReadFile` / `WriteFile`. Tools don't get `*os.File` handed to them; they get the interface. One enforcement point, no leaks.
- **沙箱是边界，不是功能**。agent 里所有碰磁盘的代码都走 `FileSystem.ReadFile` / `WriteFile`。工具拿不到 `*os.File`，只拿到接口。一个执行点，没有漏洞。
- **Lexical `..` checks are not enough.** A symlink inside the sandbox can point to `/etc`. `filepath.EvalSymlinks` is the third layer of defense; the first two (raw-segment ban, post-Clean ban) catch the easy cases cheaply.
- **只检查 `..` 字面是不够的**。沙箱里可以放一个软链接指向 `/etc`。`filepath.EvalSymlinks` 是第三道防线；前两道（原始 segment + post-Clean 检查）便宜地处理常见情况。
- **`io.LimitReader`, not stat-then-open.** Stat-then-open is a TOCTOU race; the file size at Stat may differ from the size at Open. LimitReader caps bytes we actually consume, regardless of what the file does between the two calls.
- **`io.LimitReader` 而不是 stat-then-open**。stat-then-open 是 TOCTOU 竞态；Stat 看到的大小和 Open 之后真实大小可以不同。LimitReader 限制我们真正消耗的字节数，文件本身怎么变都无所谓。
- **Binary extensions are denied unconditionally.** Even if `AllowedExts` is loosened to include `.png`, the hard-coded deny-list still bounces it. Defense in depth: misconfig can't defeat the rule.
- **二进制扩展名硬拒**。即便 `AllowedExts` 被放宽到包含 `.png`，硬编码的 deny-list 还是会拒掉它。配置错了也防得住。
- **Unknown ⇒ refuse, not fall through.** Anything not on the allow-list is rejected — txt/md/json/csv/html/yaml/xml only. If the LLM invents an extension we don't know, the safer answer is no.
- **未知 ⇒ 拒绝，不放行**。不在 allow-list 上的统统拒掉——只允许 txt/md/json/csv/html/yaml/xml。LLM 编一个我们没见过的扩展名出来，安全答案是 "不"。

## Run / 运行

```bash
GOWORK=off go run .              # demo: write/read .txt, refuse .png, refuse traversal, list
GOWORK=off go test -v ./...      # 7 tests (5 required + 2 adjacencies)
```

(`GOWORK=off` because the repo's `go.work` doesn't include s11 yet; the module is self-contained.)

## Files / 文件

| File | Purpose / 作用 |
|---|---|
| `filesystem.go`        | `FileSystem` interface: `ReadFile`, `WriteFile`, `Exists`, `List`. ~40 lines. |
| `local.go`             | `LocalFileSystem` implementation with atomic writes, MaxBytes cap, and a write-mutex for the MkdirAll race. |
| `safety.go`            | `IsSafePath` (3-layer check) and `IsAllowedExt` (allow-list + binary deny-list). Sentinel errors: `ErrUnsafePath`, `ErrDisallowedExt`, `ErrTooLarge`. |
| `main.go`              | CLI demo: write/read notes.txt, refuse screenshot.png, refuse traversals, list contents. |
| `filesystem_test.go`   | 7 tests: traversal rejected, binary blocked, valid text works, 100-goroutine concurrent writes, MaxBytes enforced, list sorted, Exists quiet on unsafe. |
| `testdata/expected.txt` | Captured `go run .` + test output (tmpdir paths placeholder-replaced). |

## Key teaching points / 关键学习点

1. **Why three layers of path safety instead of one?** Each catches a different attack class. Layer 1 (`..` in raw segments) catches the LLM saying `../../etc/passwd` directly. Layer 2 (post-`Clean` + prefix check) catches the LLM trying `subdir/../../etc/passwd` and counting on us to forget normalization. Layer 3 (`filepath.EvalSymlinks`) catches the LLM creating a `notes.txt` symlink that points at `/etc/passwd` and then asking to read `notes.txt`. The first two layers are cheap (string operations); layer 3 hits the disk, so it runs last. Defense in depth means "every layer is independently sufficient against its class". If one regresses, the others still hold.

2. **Why is the binary deny-list hard-coded inside `IsAllowedExt`?** Because it's the kind of check that wants to be config-independent. If you let `AllowedExts: []string{".png", ".txt"}` actually allow `.png`, then a typo or test fixture or copy-paste from the wrong tutorial loses the property. Hard-coding the deny-list means "no permitted configuration can re-enable `.png`". Mirrors upstream's `UNSUPPORTED_BINARY_EXTENSIONS` set living module-scope, separate from the per-instance allowed list.

3. **Why `io.LimitReader` and not `os.Stat` followed by `os.Open`?** Three reasons. (a) TOCTOU: a file's stat-size at Stat time isn't necessarily its size at Open time. A hostile or buggy writer can grow the file between them. (b) Stat is unreliable for procfs/pipes/sockets — they often report Size=0 but emit megabytes. (c) Reading one byte past the limit and checking the resulting slice length is the cheapest possible implementation; we never even allocate the over-budget tail. The pattern is `io.ReadAll(io.LimitReader(f, cap+1))` then `if len(buf) > cap { reject }`.

4. **Why does `WriteFile` use a tempfile + Rename instead of just `os.WriteFile`?** Atomicity. If the tool crashes mid-write, the destination file is either the old content or the new content — never a half-finished blob. `os.WriteFile` with a single open-truncate-write call leaves a partial file on the disk on crash. Tempfile-in-same-directory + Rename guarantees the rename is a single metadata operation on POSIX. The "in-same-directory" part matters: `os.Rename` is only atomic within one filesystem; if the tmpfile is on `/tmp` and the destination is on a different mount, the rename becomes a copy.

5. **Why does `Exists` return `false` for unsafe paths instead of an error?** Because Exists is the side-channel for "should I try to read this?". If an attacker calls `Exists("../../etc/passwd")`, the safe response is "no", not "yes but also here's how the safety check failed". Errors leak information; booleans don't. The `ReadFile` path returns errors because failure there IS the loud signal — the caller asked us to do something we shouldn't.

## What this is NOT / 这一节"故意不做"什么

- **No per-file MIME sniffing.** We trust the extension. A `.txt` file containing `\x89PNG\r\n` will be accepted as text. A real production sandbox would also sniff the first 512 bytes against `http.DetectContentType`; that's an additive layer, not a different design.
- **No quota across files.** MaxBytes is per-file. A loop that writes 1000 × 1MB .txt files will succeed (no quota check). Real systems add a disk-quota daemon or a per-session byte budget. Out of scope for the teaching point.
- **No filesystem-level isolation.** This is process-internal: a separate process running as the same user can still read the sandbox dir. Real isolation needs OS primitives (chroot, namespaces, App Sandbox, etc.) — too much variance across platforms for a teaching repo.
- **No encryption-at-rest.** Files are stored as plaintext on the host. Not a sandbox concern; that's a deployment concern.
- **No CSV normalization or pdf/docx handlers.** Upstream's `FileSystem` has per-extension classes (`CsvFile` normalizes via `csv.reader`, `PdfFile` writes via `reportlab`). We treat all files as opaque strings; the agent reads/writes text and never tries to render a PDF. Deliberate omission per the chapter scope.

## Pointer to upstream / 上游对照

- `browser_use/filesystem/file_system.py` — `FileSystem` class and the `BaseFile` hierarchy. `UNSUPPORTED_BINARY_EXTENSIONS` (module-level constant) is the exact source of our deny-list. The `_resolve_filename` helper there is upstream's analog of `IsSafePath` — it normalizes to `os.path.basename` before the lookup, which is a stronger flavor of our "no `..` segments" rule (and is one of the inspirations for it).
- `browser_use/tools/service.py` — `write_file` / `read_file` tool actions. They call `self.file_system.write_file(...)` and never `open(...)` directly. That's the discipline our `FileSystem` interface encodes; s12's tool layer mirrors it.

The bilingual chapter docs at `docs/{zh,en}/s11-filesystem-sandbox.md` walk through the narrative; this file is the operational guide.
