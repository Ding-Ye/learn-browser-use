package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// DefaultMaxBytes is the per-file cap when LocalFileSystem.MaxBytes is
// zero. Upstream Python has no explicit limit (Python lets you read a
// 10GB file into memory and OOM); we pick 10 MiB as a sane default that
// fits an LLM context window of text comfortably.
const DefaultMaxBytes int64 = 10 * 1024 * 1024

// ErrTooLarge is the sentinel returned when content (read or write)
// would exceed MaxBytes. Tests match against it via errors.Is.
var ErrTooLarge = errors.New("file exceeds max size")

// ErrDisallowedExt is the sentinel for extension rejection — separate
// from ErrUnsafePath so a caller can distinguish "you typoed the path"
// from "you tried to upload a png".
var ErrDisallowedExt = errors.New("disallowed extension")

// LocalFileSystem is the on-disk implementation of FileSystem.
//
//   - Root is the sandbox dir. All operations are confined under it.
//   - MaxBytes caps both read and write payload sizes. Zero ⇒ DefaultMaxBytes.
//   - AllowedExts overrides the default text-only extension list.
//     Empty/nil ⇒ DefaultAllowedExts. The hard-coded binary deny-list
//     in safety.go is ALWAYS in force regardless.
//
// LocalFileSystem holds an internal sync.Mutex used only for the write
// path so concurrent writers to DIFFERENT files don't trip on each
// other when the OS reports "directory does not exist" mid-Mkdir. The
// mutex is per-FileSystem, not per-file — for the agent workloads we
// care about, throughput is dominated by LLM latency, not disk IO.
type LocalFileSystem struct {
	Root        string
	MaxBytes    int64
	AllowedExts []string

	writeMu sync.Mutex // protects directory creation race in WriteFile
}

// NewLocalFileSystem is a convenience constructor that mkdir's the root.
// Callers who want to be explicit can construct the struct directly.
func NewLocalFileSystem(root string) (*LocalFileSystem, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create root %q: %w", root, err)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root %q: %w", root, err)
	}
	return &LocalFileSystem{Root: abs, MaxBytes: DefaultMaxBytes}, nil
}

// maxBytes returns the effective cap (handles zero-value MaxBytes).
func (l *LocalFileSystem) maxBytes() int64 {
	if l.MaxBytes <= 0 {
		return DefaultMaxBytes
	}
	return l.MaxBytes
}

// ReadFile honors all three safety layers + size cap. The size cap is
// enforced with io.LimitReader rather than os.Stat then os.Open because:
//
//   - Stat-then-open is a TOCTOU race: the file size at Stat time may
//     differ from the file size at Open time. Hostile or buggy writers
//     can grow a file between the two calls. LimitReader caps the bytes
//     we actually consume, no matter what the file does between Stat
//     and Open.
//   - Some files (procfs entries, pipes) return Size=0 from Stat even
//     though they emit megabytes when read. Stat is unreliable here.
//   - Reading one extra byte past the limit and treating that as the
//     "too big" signal is the cheapest possible implementation; we don't
//     even need to allocate the over-budget portion.
func (l *LocalFileSystem) ReadFile(ctx context.Context, relPath string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := IsSafePath(l.Root, relPath); err != nil {
		return "", err
	}
	if !IsAllowedExt(relPath, l.AllowedExts) {
		return "", fmt.Errorf("%w: %s", ErrDisallowedExt, filepath.Ext(relPath))
	}
	full := filepath.Join(l.Root, filepath.Clean(relPath))
	f, err := os.Open(full)
	if err != nil {
		return "", err
	}
	defer f.Close()

	cap := l.maxBytes()
	// Read cap+1 bytes; if we hit cap+1, the file is too big.
	limited := io.LimitReader(f, cap+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		return "", err
	}
	if int64(len(buf)) > cap {
		return "", fmt.Errorf("%w: %s > %d bytes", ErrTooLarge, relPath, cap)
	}
	return string(buf), nil
}

// WriteFile enforces ext + safety + size BEFORE touching disk, then
// writes atomically via a tempfile-in-same-dir + Rename. The atomicity
// is important: a tool that crashes mid-write must not leave a half
// finished file the next Read sees and gets confused by.
func (l *LocalFileSystem) WriteFile(ctx context.Context, relPath string, content string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := IsSafePath(l.Root, relPath); err != nil {
		return err
	}
	if !IsAllowedExt(relPath, l.AllowedExts) {
		return fmt.Errorf("%w: %s", ErrDisallowedExt, filepath.Ext(relPath))
	}
	cap := l.maxBytes()
	if int64(len(content)) > cap {
		return fmt.Errorf("%w: %d > %d bytes", ErrTooLarge, len(content), cap)
	}

	full := filepath.Join(l.Root, filepath.Clean(relPath))
	dir := filepath.Dir(full)

	l.writeMu.Lock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		l.writeMu.Unlock()
		return fmt.Errorf("mkdir %q: %w", dir, err)
	}
	l.writeMu.Unlock()

	// Atomic write: temp in same directory (so Rename is a metadata-only op)
	// then rename over the destination.
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(full)+"-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	// On any error after CreateTemp, best-effort cleanup so partial
	// files don't accumulate.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, full); err != nil {
		return fmt.Errorf("rename to %q: %w", full, err)
	}
	return nil
}

// Exists is intentionally non-erroring; unsafe paths read as "absent"
// rather than leaking a structured error to whoever is querying.
func (l *LocalFileSystem) Exists(ctx context.Context, relPath string) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	if err := IsSafePath(l.Root, relPath); err != nil {
		return false
	}
	full := filepath.Join(l.Root, filepath.Clean(relPath))
	_, err := os.Stat(full)
	return err == nil
}

// List returns sorted entries (files + dirs). The deterministic order
// matters for goldens — without it, test output flakes across
// filesystems.
func (l *LocalFileSystem) List(ctx context.Context, relPath string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// "." is a special-case: list the root.
	target := relPath
	if relPath == "." || relPath == "" {
		target = "."
	} else {
		if err := IsSafePath(l.Root, relPath); err != nil {
			return nil, err
		}
	}
	full := l.Root
	if target != "." {
		full = filepath.Join(l.Root, filepath.Clean(relPath))
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		// Skip our own temp artifacts.
		if strings.HasPrefix(e.Name(), ".tmp-") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}
