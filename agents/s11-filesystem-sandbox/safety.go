package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// DefaultAllowedExts is the safe text-shaped allow-list, mirroring the
// extensions upstream's FileSystem registers (md/txt/json/jsonl/csv/html/xml)
// plus the YAML pair our Go demo writes. We DO NOT include pdf/docx — those
// need binary handlers we deliberately omit (see README).
var DefaultAllowedExts = []string{
	".txt",
	".md",
	".json",
	".jsonl",
	".csv",
	".html",
	".xml",
	".yaml",
	".yml",
}

// defaultBinaryDenylist tracks extensions we ALWAYS refuse even if a
// caller passes a relaxed AllowedExts. The LLM will sometimes ask "save
// the screenshot as data.png", and the right answer is to refuse and
// nudge it back into the text-only sandbox.
//
// Mirrors browser_use/filesystem/file_system.py:UNSUPPORTED_BINARY_EXTENSIONS.
var defaultBinaryDenylist = map[string]struct{}{
	".png":  {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".bmp": {}, ".svg": {}, ".webp": {}, ".ico": {},
	".mp3": {}, ".mp4": {}, ".wav": {}, ".avi": {}, ".mov": {},
	".zip": {}, ".tar": {}, ".gz": {}, ".rar": {},
	".exe": {}, ".bin": {}, ".dll": {}, ".so": {}, ".dylib": {},
}

// ErrUnsafePath is the sentinel returned (wrapped) when path resolution
// fails for ANY reason — absolute path, contains "..", or symlink
// resolution lands outside the root. Tests assert error text contains
// "unsafe".
var ErrUnsafePath = errors.New("unsafe path")

// IsSafePath rejects a candidate relative path for the sandbox rooted
// at root. The check has three independent layers:
//
//  1. Reject obvious lexical traversal: empty string, absolute path,
//     any `..` segment after cleaning. Cheap and runs first.
//
//  2. Anchor to root: filepath.Join(root, rel), then re-Clean, then
//     verify the result is still prefixed by root + sep. This catches
//     things like `subdir/../../outside` that survive step 1 only on
//     poorly-normalized inputs.
//
//  3. Symlink escape: filepath.EvalSymlinks on the deepest ancestor
//     that actually exists, then re-check the prefix. This is the
//     reason a lexical-only `..` check is NOT sufficient — an attacker
//     can place a symlink inside the sandbox pointing to `/etc` and
//     then ask us to read a child of it.
//
// The function does NOT touch the file being addressed; that's the
// caller's job. We resolve the deepest existing ancestor so the check
// works for both reads (file present) and writes (file absent).
func IsSafePath(root, rel string) error {
	if rel == "" {
		return fmt.Errorf("%w: empty path", ErrUnsafePath)
	}
	if filepath.IsAbs(rel) {
		return fmt.Errorf("%w: absolute path %q rejected", ErrUnsafePath, rel)
	}

	// Layer 1a: raw-segment traversal. Reject ANY `..` in the RAW input,
	// not just the post-Clean version. `a/b/c/../../../etc/passwd`
	// cleans to `etc/passwd` (lexically safe) but the intent is
	// obviously malicious — refuse it on principle. This costs us
	// nothing legitimate; agent tools never need `..` in a path.
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." {
			return fmt.Errorf("%w: traversal segment in %q rejected", ErrUnsafePath, rel)
		}
	}
	// Also catch OS-specific separators on Windows-style inputs.
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		if seg == ".." {
			return fmt.Errorf("%w: traversal segment in %q rejected", ErrUnsafePath, rel)
		}
	}

	// Layer 1b: post-Clean sanity. `.` alone refers to the root, which
	// is not a file the caller can address.
	cleaned := filepath.Clean(rel)
	if cleaned == "." {
		return fmt.Errorf("%w: refusing root marker %q", ErrUnsafePath, rel)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: traversal segment %q rejected", ErrUnsafePath, rel)
	}

	// Layer 2: lexical join + prefix check. We MUST normalize root the
	// same way (Clean + Abs) so the prefix compare doesn't fail on
	// trailing slashes or symlinked roots.
	absRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return fmt.Errorf("%w: resolving root: %v", ErrUnsafePath, err)
	}
	joined := filepath.Join(absRoot, cleaned)
	if !strings.HasPrefix(joined, absRoot+string(filepath.Separator)) && joined != absRoot {
		return fmt.Errorf("%w: %q escapes root after join", ErrUnsafePath, rel)
	}

	// Layer 3: symlink escape. EvalSymlinks fails if the path doesn't
	// exist, so we climb to the deepest ancestor that DOES exist and
	// resolve from there. This handles the write case (target file
	// not yet created) without giving up the symlink check.
	probe := joined
	for {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			absResolvedRoot, rerr := filepath.EvalSymlinks(absRoot)
			if rerr != nil {
				// Root itself can't resolve — fall back to lexical comparison.
				absResolvedRoot = absRoot
			}
			if !strings.HasPrefix(resolved, absResolvedRoot+string(filepath.Separator)) && resolved != absResolvedRoot {
				return fmt.Errorf("%w: %q resolves outside root via symlink", ErrUnsafePath, rel)
			}
			return nil
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			// Hit the filesystem root without finding any existing
			// ancestor — that itself is suspicious. Treat as unsafe.
			return fmt.Errorf("%w: cannot resolve %q", ErrUnsafePath, rel)
		}
		probe = parent
	}
}

// IsAllowedExt returns true when the file's extension is on the
// allow-list AND not on the always-deny binary list. An empty
// allowed slice means "use DefaultAllowedExts".
//
// The deny-list short-circuits the allow-list so a caller passing
// {".png"} as AllowedExts still gets refused — defense in depth
// against misconfiguration.
func IsAllowedExt(name string, allowed []string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return false
	}
	if _, denied := defaultBinaryDenylist[ext]; denied {
		return false
	}
	if len(allowed) == 0 {
		allowed = DefaultAllowedExts
	}
	for _, a := range allowed {
		if strings.EqualFold(ext, a) {
			return true
		}
	}
	return false
}
