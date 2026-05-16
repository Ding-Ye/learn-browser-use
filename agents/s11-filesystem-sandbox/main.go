package main

// CLI demo for s11. We mkdir a fresh tmp dir, instantiate a
// LocalFileSystem rooted there, and walk through four canonical
// interactions:
//
//   (a) write+read of an allowed extension (notes.txt)
//   (b) refused write of a binary extension (screenshot.png)
//   (c) refused read of a path-traversal attempt (../../etc/passwd)
//   (d) listing what's actually inside the sandbox
//
// Run with: GOWORK=off go run .

import (
	"context"
	"errors"
	"fmt"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	// Step 0: fresh tmp dir. Real callers pass a long-lived path.
	root, err := os.MkdirTemp("", "s11-demo-*")
	if err != nil {
		return fmt.Errorf("mkdir tmp: %w", err)
	}
	defer os.RemoveAll(root)
	fmt.Println("# s11 — filesystem-sandbox demo")
	fmt.Println("Sandbox root:", root)

	fs, err := NewLocalFileSystem(root)
	if err != nil {
		return fmt.Errorf("init fs: %w", err)
	}

	// (a) Write + read a .txt file. Happy path: extension on the
	//     allow-list, content within MaxBytes, no traversal.
	fmt.Println("\n(a) write+read notes.txt — should succeed")
	if err := fs.WriteFile(ctx, "notes.txt", "TODO: learn sandboxing\nstep 1: read this demo\n"); err != nil {
		return fmt.Errorf("write notes.txt: %w", err)
	}
	got, err := fs.ReadFile(ctx, "notes.txt")
	if err != nil {
		return fmt.Errorf("read notes.txt: %w", err)
	}
	fmt.Printf("  read back %d bytes; first line: %q\n", len(got), firstLine(got))

	// Subdirectory write — also fine, MkdirAll handles it.
	if err := fs.WriteFile(ctx, "subdir/plan.md", "# plan\n- s11\n- s12\n"); err != nil {
		return fmt.Errorf("write subdir/plan.md: %w", err)
	}
	fmt.Println("  also wrote subdir/plan.md")

	// (b) Refused write of a binary extension. The LLM might say "save
	//     this base64 string as image.png"; the right response is no.
	fmt.Println("\n(b) write screenshot.png — should be refused")
	err = fs.WriteFile(ctx, "screenshot.png", "fake png payload")
	if err == nil {
		return fmt.Errorf("expected refusal but write succeeded")
	}
	fmt.Println("  refused:", err)
	if !errors.Is(err, ErrDisallowedExt) {
		return fmt.Errorf("expected ErrDisallowedExt, got %v", err)
	}

	// (c) Refused read of a path-traversal attempt. /etc/passwd is the
	//     classic; we ask via three rendering styles to show the
	//     layered checks catch each one.
	fmt.Println("\n(c) read traversal attempts — all should be refused")
	for _, attempt := range []string{
		"../../etc/passwd",
		"subdir/../../etc/passwd",
		"/etc/passwd",
	} {
		_, err := fs.ReadFile(ctx, attempt)
		if err == nil {
			return fmt.Errorf("traversal %q unexpectedly succeeded", attempt)
		}
		fmt.Printf("  %-32s -> refused: %v\n", attempt, err)
	}

	// (d) List the sandbox. Should see notes.txt and subdir/.
	fmt.Println("\n(d) list . — see what's in the sandbox")
	names, err := fs.List(ctx, ".")
	if err != nil {
		return fmt.Errorf("list .: %w", err)
	}
	for _, n := range names {
		fmt.Printf("  - %s\n", n)
	}

	fmt.Println("\nDone. All four steps behaved as expected.")
	return nil
}

func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' {
			return s[:i]
		}
	}
	return s
}
