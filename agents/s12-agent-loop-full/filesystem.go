package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// filesystem.go is the s11-shape sandbox FileSystem, condensed to the
// minimum surface the agent needs:
//
//   - ReadFile(ctx, relPath) → text
//   - WriteFile(ctx, relPath, content) → error
//   - Exists(ctx, relPath) → bool
//
// Two safety layers:
//
//   1. Path safety: IsSafePath rejects absolute paths, `..` segments,
//      and (lexically) anything that wouldn't end up under Root after
//      filepath.Join + Clean. We skip the symlink-resolve layer from
//      s11 since the s12 demo doesn't try to attack its own sandbox;
//      the README points readers to s11 for the production-grade
//      version.
//
//   2. Extension allow-list + binary deny-list: the LLM cannot ask
//      us to save "report.png" or "exec.dll". A small allow-list of
//      text-shaped extensions is the right default for an agent.

// FileSystem is the interface the agent's read_file / write_file
// tools consume. Same shape as s11.
type FileSystem interface {
	ReadFile(ctx context.Context, relPath string) (string, error)
	WriteFile(ctx context.Context, relPath string, content string) error
	Exists(ctx context.Context, relPath string) bool
}

// allowedExts is the text-only allow-list. Mirrors s11's
// DefaultAllowedExts minus the YAML pair (the s12 demo writes only
// txt + md).
var allowedExts = map[string]struct{}{
	".txt":   {},
	".md":    {},
	".json":  {},
	".jsonl": {},
	".csv":   {},
	".html":  {},
	".xml":   {},
}

// deniedExts is the always-refuse binary list. Defense-in-depth: even
// if a future caller pushed ".png" onto allowedExts, this map would
// block it.
var deniedExts = map[string]struct{}{
	".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".bmp": {},
	".mp3": {}, ".mp4": {}, ".wav": {},
	".exe": {}, ".dll": {}, ".so": {}, ".dylib": {}, ".bin": {},
}

// ErrUnsafePath is returned for path-traversal attempts.
var ErrUnsafePath = errors.New("unsafe path")

// ErrDisallowedExt is returned for extensions that aren't on the
// allow-list (or are on the deny-list).
var ErrDisallowedExt = errors.New("disallowed extension")

// LocalFileSystem is the on-disk implementation. Root is the sandbox
// directory; all operations are confined under it.
type LocalFileSystem struct {
	Root string
}

// NewLocalFileSystem makes the root directory if needed and returns a
// fresh FS rooted at its absolute path.
func NewLocalFileSystem(root string) (*LocalFileSystem, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("filesystem: mkdir %q: %w", root, err)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("filesystem: abs %q: %w", root, err)
	}
	return &LocalFileSystem{Root: abs}, nil
}

// ReadFile is the read path. Safety layers run before any disk IO so
// a malicious path never reaches os.Open.
func (l *LocalFileSystem) ReadFile(ctx context.Context, relPath string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := isSafePath(l.Root, relPath); err != nil {
		return "", err
	}
	if err := isAllowedExt(relPath); err != nil {
		return "", err
	}
	full := filepath.Join(l.Root, filepath.Clean(relPath))
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// WriteFile is the write path. Safety + ext checks run before any
// disk IO; the write itself is plain (non-atomic) for teaching
// simplicity — s11 documents the atomic-rename variant.
func (l *LocalFileSystem) WriteFile(ctx context.Context, relPath string, content string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := isSafePath(l.Root, relPath); err != nil {
		return err
	}
	if err := isAllowedExt(relPath); err != nil {
		return err
	}
	full := filepath.Join(l.Root, filepath.Clean(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("filesystem: mkdir parent: %w", err)
	}
	return os.WriteFile(full, []byte(content), 0o644)
}

// Exists is the non-erroring presence check.
func (l *LocalFileSystem) Exists(ctx context.Context, relPath string) bool {
	if err := isSafePath(l.Root, relPath); err != nil {
		return false
	}
	full := filepath.Join(l.Root, filepath.Clean(relPath))
	_, err := os.Stat(full)
	return err == nil
}

// isSafePath enforces the lexical safety layer. The full s11 version
// also resolves symlinks; we omit that because the s12 demo doesn't
// exercise hostile FS topology.
func isSafePath(root, rel string) error {
	if rel == "" {
		return fmt.Errorf("%w: empty path", ErrUnsafePath)
	}
	if filepath.IsAbs(rel) {
		return fmt.Errorf("%w: absolute path %q", ErrUnsafePath, rel)
	}
	cleaned := filepath.Clean(rel)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: traversal %q", ErrUnsafePath, rel)
	}
	for _, seg := range strings.Split(cleaned, string(filepath.Separator)) {
		if seg == ".." {
			return fmt.Errorf("%w: traversal segment in %q", ErrUnsafePath, rel)
		}
	}
	return nil
}

// isAllowedExt returns nil if the extension is on the allow-list and
// NOT on the deny-list. Both checks run because defense-in-depth.
func isAllowedExt(name string) error {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return fmt.Errorf("%w: no extension on %q", ErrDisallowedExt, name)
	}
	if _, denied := deniedExts[ext]; denied {
		return fmt.Errorf("%w: %q is binary", ErrDisallowedExt, ext)
	}
	if _, ok := allowedExts[ext]; !ok {
		return fmt.Errorf("%w: %q not in allow-list", ErrDisallowedExt, ext)
	}
	return nil
}
