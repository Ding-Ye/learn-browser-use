package main

// filesystem_test.go — 5 required tests + a couple of small adjacencies
// for sanity. All tests use t.TempDir() so they leave no on-disk litter.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// --- helpers ---------------------------------------------------------

// newFS builds a LocalFileSystem on a per-test tmp dir. We don't go
// through NewLocalFileSystem because t.TempDir already mkdir'd it for us,
// and using the constructor would double-resolve the path with no gain.
func newFS(t *testing.T, maxBytes int64) *LocalFileSystem {
	t.Helper()
	root := t.TempDir()
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return &LocalFileSystem{
		Root:     abs,
		MaxBytes: maxBytes,
	}
}

// --- the five required tests ----------------------------------------

// TestPathTraversalRejected — every `..` styled path the LLM might
// concoct must come back as an error whose text contains "unsafe".
// The README and docs both lean on this guarantee.
func TestPathTraversalRejected(t *testing.T) {
	fs := newFS(t, 0)
	ctx := context.Background()

	traversals := []string{
		"../etc/passwd",
		"../../etc/passwd",
		"sub/../../etc/passwd",
		"a/b/c/../../../etc/passwd",
	}
	for _, p := range traversals {
		_, err := fs.ReadFile(ctx, p)
		if err == nil {
			t.Errorf("ReadFile(%q): want error, got nil", p)
			continue
		}
		if !strings.Contains(err.Error(), "unsafe") {
			t.Errorf("ReadFile(%q): err=%q, want substring %q", p, err.Error(), "unsafe")
		}
		// errors.Is should also work — the README documents this.
		if !errors.Is(err, ErrUnsafePath) {
			t.Errorf("ReadFile(%q): want errors.Is ErrUnsafePath, got %v", p, err)
		}
	}

	// Absolute paths are a sibling concern (no `..` but still unsafe).
	if _, err := fs.ReadFile(ctx, "/etc/passwd"); err == nil {
		t.Errorf("ReadFile(absolute): want error, got nil")
	} else if !strings.Contains(err.Error(), "unsafe") {
		t.Errorf("ReadFile(absolute): err=%q, want substring 'unsafe'", err.Error())
	}
}

// TestBinaryExtensionBlocked — png/exe/so/dll/etc. must all bounce off
// the deny-list even at the WriteFile boundary.
func TestBinaryExtensionBlocked(t *testing.T) {
	fs := newFS(t, 0)
	ctx := context.Background()

	binaries := []string{
		"data.png", "shot.jpg", "art.gif", "tool.exe",
		"libfoo.so", "kernel.bin", "logo.svg",
	}
	for _, name := range binaries {
		err := fs.WriteFile(ctx, name, "anything")
		if err == nil {
			t.Errorf("WriteFile(%q): want error, got nil", name)
			continue
		}
		if !errors.Is(err, ErrDisallowedExt) {
			t.Errorf("WriteFile(%q): want errors.Is ErrDisallowedExt, got %v", name, err)
		}
	}

	// Defense in depth: even if a caller relaxes AllowedExts to include
	// ".png", the binary deny-list still wins.
	loose := newFS(t, 0)
	loose.AllowedExts = []string{".png", ".txt"}
	if err := loose.WriteFile(ctx, "x.png", "fake"); err == nil {
		t.Error("relaxed AllowedExts: png write unexpectedly succeeded")
	}
}

// TestValidTextFileWorks — the happy path: write + read .txt with the
// same content we wrote in, on the same path.
func TestValidTextFileWorks(t *testing.T) {
	fs := newFS(t, 0)
	ctx := context.Background()

	want := "line 1\nline 2\n中文测试\n"
	if err := fs.WriteFile(ctx, "notes.txt", want); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !fs.Exists(ctx, "notes.txt") {
		t.Fatal("Exists: want true after write")
	}
	got, err := fs.ReadFile(ctx, "notes.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got != want {
		t.Errorf("ReadFile roundtrip mismatch:\n want %q\n got  %q", want, got)
	}

	// Subdirectory roundtrip — MkdirAll is supposed to create parents.
	if err := fs.WriteFile(ctx, "a/b/c.txt", "deep"); err != nil {
		t.Fatalf("WriteFile subdir: %v", err)
	}
	if got, err := fs.ReadFile(ctx, "a/b/c.txt"); err != nil || got != "deep" {
		t.Errorf("subdir roundtrip: got=%q err=%v", got, err)
	}

	// All other allowed text extensions should also work.
	for _, ext := range []string{".md", ".json", ".csv", ".html", ".yaml"} {
		name := "test" + ext
		if err := fs.WriteFile(ctx, name, "ok"); err != nil {
			t.Errorf("WriteFile(%q): %v", name, err)
		}
	}
}

// TestConcurrentWritesSafe — 100 goroutines each write a distinct file.
// We don't test interleaved writes to the SAME file (that's a "last
// writer wins" semantics question, not a safety question); we test that
// the directory-creation race is closed.
func TestConcurrentWritesSafe(t *testing.T) {
	fs := newFS(t, 0)
	ctx := context.Background()

	const N = 100
	var wg sync.WaitGroup
	errs := make(chan error, N)

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Spread across several subdirs so MkdirAll fires on each.
			path := fmt.Sprintf("bucket%d/file%d.txt", i%5, i)
			content := fmt.Sprintf("worker %d wrote here\n", i)
			if err := fs.WriteFile(ctx, path, content); err != nil {
				errs <- fmt.Errorf("worker %d: %w", i, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	var failures []error
	for e := range errs {
		failures = append(failures, e)
	}
	if len(failures) > 0 {
		t.Fatalf("got %d failures, first: %v", len(failures), failures[0])
	}

	// Verify all 100 files exist and have the right content.
	for i := 0; i < N; i++ {
		path := fmt.Sprintf("bucket%d/file%d.txt", i%5, i)
		got, err := fs.ReadFile(ctx, path)
		if err != nil {
			t.Errorf("readback %s: %v", path, err)
			continue
		}
		want := fmt.Sprintf("worker %d wrote here\n", i)
		if got != want {
			t.Errorf("readback %s: got %q want %q", path, got, want)
		}
	}
}

// TestMaxSizeEnforced — a payload larger than MaxBytes must be rejected
// at the WriteFile boundary, and reading a too-big file must also fail.
func TestMaxSizeEnforced(t *testing.T) {
	fs := newFS(t, 64) // 64 bytes max
	ctx := context.Background()

	// Write right at the boundary: OK.
	atLimit := strings.Repeat("a", 64)
	if err := fs.WriteFile(ctx, "edge.txt", atLimit); err != nil {
		t.Fatalf("at-limit write should succeed: %v", err)
	}

	// One byte over: refused.
	tooBig := strings.Repeat("a", 65)
	err := fs.WriteFile(ctx, "big.txt", tooBig)
	if err == nil {
		t.Fatal("over-limit write should be refused")
	}
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("want errors.Is ErrTooLarge, got %v", err)
	}

	// Read-side enforcement: scribble a too-big file by going around
	// the API, then verify ReadFile catches it. (A real-world way this
	// happens: another process writes to the sandbox.)
	loose := newFS(t, 0)             // unlimited
	loose.MaxBytes = 1024 * 1024 * 1024
	if err := loose.WriteFile(ctx, "monster.txt", strings.Repeat("x", 200)); err != nil {
		t.Fatalf("loose write: %v", err)
	}
	// Now read with a tight cap.
	tight := &LocalFileSystem{Root: loose.Root, MaxBytes: 50}
	_, err = tight.ReadFile(ctx, "monster.txt")
	if err == nil {
		t.Fatal("tight read over a 200-byte file should be refused")
	}
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("read: want errors.Is ErrTooLarge, got %v", err)
	}
}

// --- a couple of small adjacencies that catch regressions -----------

// TestListReturnsSorted — the demo banks on alphabetical order.
func TestListReturnsSorted(t *testing.T) {
	fs := newFS(t, 0)
	ctx := context.Background()
	for _, name := range []string{"c.txt", "a.txt", "b.txt"} {
		if err := fs.WriteFile(ctx, name, "x"); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	names, err := fs.List(ctx, ".")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{"a.txt", "b.txt", "c.txt"}
	if len(names) != len(want) {
		t.Fatalf("len=%d want=%d (%v)", len(names), len(want), names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("idx %d: got %q want %q", i, names[i], want[i])
		}
	}
}

// TestExistsIsQuietOnUnsafe — Exists must not leak info via a
// structured error; it just returns false.
func TestExistsIsQuietOnUnsafe(t *testing.T) {
	fs := newFS(t, 0)
	ctx := context.Background()
	if fs.Exists(ctx, "../../etc/passwd") {
		t.Error("unsafe Exists returned true")
	}
}
