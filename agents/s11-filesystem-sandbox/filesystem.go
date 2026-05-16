// s11 — filesystem sandbox.
//
// FileSystem is the interface the rest of the agent (specifically, tools
// like read_file / write_file in s12) consume. Everything that touches
// the host disk goes through this surface so two invariants can be
// enforced in one place:
//
//  1. Paths the LLM proposes are constrained to a single sandbox root —
//     no `../../etc/passwd`, no `/Users/you/.ssh/id_rsa`.
//  2. Content is text. Binary uploads (png, exe, so, dll, ...) are
//     refused at the boundary; size is capped at MaxBytes.
//
// The concrete implementation in this chapter is LocalFileSystem (local.go).
// Tests in filesystem_test.go cover the five guarantees listed in plan.md.
package main

import "context"

// FileSystem is the sandbox surface. All paths are RELATIVE to a hidden
// root — callers must never pass absolute paths.
//
// Methods take context.Context so the s12 agent loop can cancel a stuck
// write (e.g. the LLM proposed a 5MB JSON blob and we want to bail).
type FileSystem interface {
	// ReadFile returns the file content as a string. Errors when the
	// path is unsafe (escapes root), the extension is unsupported, or
	// the file would exceed MaxBytes on read.
	ReadFile(ctx context.Context, relPath string) (string, error)

	// WriteFile creates parent directories as needed and writes content
	// atomically. Errors when path is unsafe, extension is unsupported,
	// or len(content) > MaxBytes.
	WriteFile(ctx context.Context, relPath string, content string) error

	// Exists is a non-erroring path check. Returns false for unsafe
	// paths (treats them as not-present rather than leaking info).
	Exists(ctx context.Context, relPath string) bool

	// List returns the names of entries in the relPath directory
	// (relative to root). Symlinks are NOT followed. Errors when
	// the directory itself escapes the root.
	List(ctx context.Context, relPath string) ([]string, error)
}
