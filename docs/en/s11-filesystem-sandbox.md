---
title: "s11 · Filesystem sandbox"
chapter: 11
slug: s11-filesystem-sandbox
est_read_min: 12
---

# s11 · Filesystem sandbox

> Teaching focus: tools like `read_file` and `write_file` give the LLM disk access. Without a sandbox, the agent will at some point be asked to read `~/.ssh/id_rsa` or dump a Base64 binary to `screenshot.png`. s11 puts a `FileSystem` interface in front of every disk operation and an implementation (`LocalFileSystem`) that enforces three independent safety layers plus a size cap.

---

## Problem / 问题

After s04 we have a working tool registry. After s12 we'll have an agent loop that hands LLM-proposed inputs straight to tool implementations. The natural — and wrong — first implementation of `read_file` looks like:

```go
// DON'T DO THIS
func readFile(path string) (string, error) {
    b, err := os.ReadFile(path)
    return string(b), err
}
```

Three concrete attack-or-bug scenarios this enables, in order of decreasing exoticism:

1. **Direct path traversal.** The LLM, given an ambiguous task, asks `read_file("/etc/passwd")` or `read_file("../../home/yeding/.ssh/id_rsa")`. The host disk is fully addressable; the agent process inherits the user's file permissions; nothing stops the read.
2. **Binary file confusion.** The LLM proposes `write_file("photo.png", "<base64 blob>")`. We do the write. Now the agent's "working directory" has an apparent PNG that isn't a PNG. Worse, if the agent later does `read_file("photo.png")`, the LLM gets a bytewise garbage string in its context window. Garbage in, hallucination out.
3. **Memory blow-up.** The LLM does `read_file("very_large.log")`. We `io.ReadAll` a 2GB file into a string. The process OOMs — or worse, doesn't, and the next prompt is 2GB of log lines that costs $40 to send to the model.

s11 closes all three. The shape: an interface that every tool consumes, an implementation that enforces the rules, and a deny-list that no permitted configuration can defeat.

## Solution / 解决方案

Introduce `FileSystem`:

```go
type FileSystem interface {
    ReadFile(ctx context.Context, relPath string) (string, error)
    WriteFile(ctx context.Context, relPath string, content string) error
    Exists(ctx context.Context, relPath string) bool
    List(ctx context.Context, relPath string) ([]string, error)
}
```

And `LocalFileSystem`:

```go
type LocalFileSystem struct {
    Root        string
    MaxBytes    int64
    AllowedExts []string
}
```

Four methods carry the whole load:

| Method | Responsibility | Refuses when |
|---|---|---|
| `ReadFile(ctx, rel)`         | Read file content as string | path unsafe / ext denied / payload > MaxBytes |
| `WriteFile(ctx, rel, body)`  | Atomic write (tempfile + rename) | path unsafe / ext denied / body > MaxBytes |
| `Exists(ctx, rel)`            | Non-erroring path check | (returns false on unsafe; never errors) |
| `List(ctx, rel)`              | Sorted listing of a relative dir | path unsafe |

Two helpers in `safety.go` do the actual work:

- `IsSafePath(root, rel) error` — three-layer check: (1) any `..` segment in the raw input ⇒ refuse; (2) after `filepath.Clean`, the prefix of the joined path must still be under root; (3) `filepath.EvalSymlinks` on the deepest existing ancestor must also stay under root. Each layer catches an attack class the others can miss.
- `IsAllowedExt(name, allowed) bool` — extension lower-cased, compared against `allowed` after the hard-coded binary deny-list has had its veto. `DefaultAllowedExts` is `{txt, md, json, jsonl, csv, html, xml, yaml, yml}`; the deny-list always includes `{png, jpg, jpeg, gif, bmp, svg, webp, ico, mp3, mp4, wav, avi, mov, zip, tar, gz, rar, exe, bin, dll, so, dylib}`.

## How It Works / 工作原理

```
   tool.run(input)                                       (s12)
   ────────────────►
        e.g. {"path": "../../etc/passwd", "content": ""}
                  │
                  ▼
   fs.ReadFile(ctx, input.path)
                  │
                  ├── ctx.Err()              ── cancelled? bail
                  ├── IsSafePath(Root, rel)  ── layered check
                  │     ├── Layer 1: "../etc/passwd" has `..` segment   → REJECT
                  │     ├── Layer 2: filepath.Join+Clean, prefix check
                  │     └── Layer 3: filepath.EvalSymlinks on deepest existing ancestor
                  ├── IsAllowedExt(name)     ── .png? always-deny list
                  ├── os.Open(full)
                  ├── io.LimitReader(f, MaxBytes+1)
                  ├── io.ReadAll(limited)
                  └── if len(buf) > MaxBytes: REJECT (size cap)

   ────────────────────────────────────────────────────────────────

   fs.WriteFile(ctx, rel, body)
   ─────────────────────────►
        same prefix checks +
                  ├── len(body) > MaxBytes → REJECT
                  ├── os.CreateTemp(dir, ".tmp-NAME-*")
                  ├── tmp.WriteString(body)
                  ├── tmp.Close()
                  └── os.Rename(tmp, full)             ← atomic
```

Core code (~50 lines):

```go
// safety.go (excerpt)
func IsSafePath(root, rel string) error {
    if rel == "" || filepath.IsAbs(rel) {
        return fmt.Errorf("%w: %q", ErrUnsafePath, rel)
    }
    // Layer 1: any `..` segment in raw input.
    for _, seg := range strings.Split(rel, "/") {
        if seg == ".." {
            return fmt.Errorf("%w: traversal in %q", ErrUnsafePath, rel)
        }
    }
    // Layer 2: anchored prefix check.
    absRoot, _ := filepath.Abs(filepath.Clean(root))
    joined := filepath.Join(absRoot, filepath.Clean(rel))
    if !strings.HasPrefix(joined, absRoot+string(filepath.Separator)) {
        return fmt.Errorf("%w: escapes root", ErrUnsafePath)
    }
    // Layer 3: walk up to deepest existing ancestor, EvalSymlinks, re-check.
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

**4 non-obvious points**:

1. **Lexical `..` checks are not enough — `EvalSymlinks` is the load-bearing third layer.** An attacker can place `notes.txt` as a symlink to `/etc/passwd` inside the sandbox, then ask us to read `notes.txt`. Layer 1 (raw segments) doesn't see `..` because there isn't one in `notes.txt`. Layer 2 (`filepath.Join` + prefix check) also passes — `notes.txt` looks like an in-root path. Only Layer 3 catches it. We walk up to the deepest existing ancestor before calling `EvalSymlinks` because the function errors on missing paths, and we still want to validate WRITE targets (where the file doesn't exist yet).
2. **`io.LimitReader` not `os.Stat` + `os.Open`.** Stat-then-open is a TOCTOU race: the file size at Stat may differ from the size at Open. A hostile writer can grow the file between the two calls. `os.Stat` is also unreliable for procfs / pipes / sockets — they report `Size=0` while emitting megabytes when read. The pattern is `io.ReadAll(io.LimitReader(f, cap+1))` then check `len(buf) > cap`. Reading one extra byte and testing the slice is the cheapest possible signal; we never even allocate the over-budget tail.
3. **The binary deny-list is hard-coded inside `IsAllowedExt`, not configured.** If a caller passes `AllowedExts: []string{".png", ".txt"}`, our deny-list still wins and refuses `.png`. The reason is defense-in-depth: misconfiguration (typo, copy-paste from the wrong tutorial, a test fixture leaking into production) cannot turn `.png` writes back on. Upstream's `UNSUPPORTED_BINARY_EXTENSIONS` lives at module scope for the same reason.
4. **Writes are atomic via tempfile + Rename, in the same directory.** `os.WriteFile` opens-truncates-writes a single fd, so a crash mid-write leaves a half-finished file. The next `ReadFile` sees garbage. The fix is `os.CreateTemp(dir, ".tmp-NAME-*")` → write → close → `os.Rename(tmp, full)`. POSIX guarantees `rename` is atomic ONLY within a single filesystem; the "in the same directory" part is what makes this work across mounts (e.g., `/tmp` being a tmpfs while the sandbox is on `/Users`).

## What Changed / 与上一节的变化

s10 style (token-cost — disk untouched):

```diff
- // tool implementations call os.ReadFile / os.WriteFile directly
- func readFile(path string) (string, error) {
-     return os.ReadFile(path)   // no sandbox, no ext check, no size cap
- }
```

After s11:

```diff
+ // every disk-touching tool takes a FileSystem dependency
+ type ReadFileTool struct {
+     fs FileSystem                          // injected at construction
+ }
+
+ func (t *ReadFileTool) Run(ctx context.Context, in json.RawMessage) (string, error) {
+     var args struct{ Path string `json:"path"` }
+     _ = json.Unmarshal(in, &args)
+     return t.fs.ReadFile(ctx, args.Path)    // all safety in fs
+ }
```

The crucial increment: **the tool no longer owns the safety policy**. `ReadFile` could be backed by `LocalFileSystem`, by an in-memory map for tests, by a remote object store — the tool doesn't care. Safety lives in exactly one place. That's the whole point of putting the boundary on the interface, not on the call site.

Downstream uses:
- s12's agent constructs `fs := NewLocalFileSystem("/tmp/agent-data")` once, then passes it to every tool. Five tools, one safety policy.
- A multi-agent deployment would create one `LocalFileSystem` per agent (different roots) and the safety guarantees compose: agent A's tools cannot reach agent B's sandbox by construction.

## Try It / 动手试一试

```bash
cd agents/s11-filesystem-sandbox

# The 4-step demo
GOWORK=off go run .

# 7 tests (5 required + 2 adjacencies)
GOWORK=off go test -v ./...
```

`GOWORK=off` because the root `go.work` doesn't list s11 yet; the module is self-contained.

Expected output (excerpt):

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

Test coverage:

- `TestPathTraversalRejected` — every `..`-bearing variant, plus absolute paths, comes back with an error containing "unsafe" and matches `errors.Is(err, ErrUnsafePath)`.
- `TestBinaryExtensionBlocked` — `.png`, `.jpg`, `.gif`, `.exe`, `.so`, `.bin`, `.svg` all bounce; an instance with relaxed `AllowedExts: []string{".png"}` STILL refuses (deny-list wins).
- `TestValidTextFileWorks` — write+read roundtrip on `notes.txt` with UTF-8 (Chinese) content; subdirectory roundtrip; every allowed text extension accepted.
- `TestConcurrentWritesSafe` — 100 goroutines write to 100 distinct paths (spread across 5 subdirs to exercise `MkdirAll` racing); all succeed, all readback values match.
- `TestMaxSizeEnforced` — at-limit write succeeds; +1-byte write returns `errors.Is(err, ErrTooLarge)`; reading a 200-byte file with a 50-byte read cap also fails with `ErrTooLarge`.

## Upstream Source Reading / 上游源码阅读

Upstream's `browser_use/filesystem/file_system.py` is ~700 lines: ~150 lines of `BaseFile` + `MarkdownFile` / `TxtFile` / `JsonFile` / `CsvFile` (with RFC 4180 normalization) / `PdfFile` (via reportlab) / `DocxFile` (via python-docx); the rest is the `FileSystem` orchestrator. s11 takes the load-bearing two slices: the deny-list and the sanitize-then-basename pattern.

```python
# Source: browser_use/filesystem/file_system.py#L15-L73

UNSUPPORTED_BINARY_EXTENSIONS = {
    'png', 'jpg', 'jpeg', 'gif', 'bmp', 'svg', 'webp', 'ico',
    'mp3', 'mp4', 'wav', 'avi', 'mov',
    'zip', 'tar', 'gz', 'rar',
    'exe', 'bin', 'dll', 'so',
}
# ↑ Maps 1:1 to our defaultBinaryDenylist in safety.go. Note this is a
#   module-scope constant, NOT per-instance configurable — same defense
#   in depth rule we apply.


def _build_filename_error_message(file_name: str, supported_extensions: list[str]) -> str:
    """Build a specific error message explaining why the filename was rejected."""
    base = os.path.basename(file_name)
    # ↑ os.path.basename strips ALL directory components. This is upstream's
    #   path-traversal defense: by the time you've taken basename, "../../etc/passwd"
    #   becomes "passwd". Our IsSafePath is stricter — we REJECT the input
    #   rather than silently rewriting it, because rewriting hides the
    #   attack from logs.

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
        # ↑ The error message is intentionally LLM-facing. It explains WHY
        #   the operation failed AND offers a workflow alternative ("the
        #   browser captures screenshots automatically"). Our Go errors
        #   are terser because the tool layer (in s12) translates them
        #   into LLM-readable text.
```

```python
# Source: browser_use/filesystem/file_system.py#L451-L475

def _resolve_filename(self, file_name: str) -> tuple[str, bool]:
    """Resolve a filename, attempting sanitization if the original is invalid.

    Normalizes to basename first to prevent directory traversal (e.g. ../secret.md).
    """
    base_name = os.path.basename(file_name)
    was_changed = base_name != file_name
    # ↑ Upstream's strategy: take the basename and PROCEED. Our Go strategy:
    #   detect the `..` and REFUSE. Two different philosophies — upstream
    #   silently sanitizes (LLM-friendly, "we'll fix your input"), we
    #   reject (security-friendly, "we'll surface the bug"). For a teaching
    #   repo the rejection path is more instructive: tests can assert the
    #   error precisely; in production a user-facing message can be
    #   layered on top either way.

    if self._is_valid_filename(base_name):
        return base_name, was_changed

    sanitized = self.sanitize_filename(base_name)
    if sanitized != base_name and self._is_valid_filename(sanitized):
        return sanitized, True

    return base_name, was_changed
```

The full 60-100 line annotated extract lives at `upstream-readings/s11-filesystem-sandbox.py`, including the `_is_valid_filename` regex (which restricts characters to `[a-zA-Z0-9_\-\.\(\) 一-鿿]` plus the extension) and the `FileSystem.__init__` that wipes the data dir on construction (the cleanup-on-startup posture our `MkdirAll` mirrors without the destructive part).
