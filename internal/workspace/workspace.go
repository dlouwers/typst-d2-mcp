// Package workspace abstracts the mapping from tool-visible paths to
// concrete on-disk paths the server can read and write.
//
// In stdio (local) mode the server shares a filesystem with its client, so
// paths are passed through unchanged. In a future HTTP (hosted) mode a
// TenantWorkspace resolver will scope every path under a per-user root,
// allowing the same compile_typst_with_d2(file_path) tool API to work
// transparently in either deployment.
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
)

// Resolver maps a tool-visible path to a concrete filesystem path the
// server may access. Implementations are responsible for any sandboxing
// or traversal-prevention rules that apply to their deployment.
type Resolver interface {
	// Resolve returns the concrete filesystem path corresponding to the
	// caller-supplied path. It must reject paths that escape the
	// resolver's permitted region.
	Resolve(path string) (string, error)
}

// LocalFS is the trivial resolver used in stdio (local) mode: the
// server and client share a filesystem, so the path is returned as-is
// after cleaning. Absolute and relative paths are both accepted.
type LocalFS struct{}

// Resolve cleans the path and returns it unchanged. It does not
// require the file to exist; callers should stat the returned path if
// they need that guarantee.
func (LocalFS) Resolve(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	return filepath.Clean(path), nil
}

// MustExist is a small helper that resolves the path and verifies it
// points to an existing regular file. It is intended for tool handlers
// that need a clear, structured error when the input file is missing.
func MustExist(r Resolver, path string) (string, error) {
	resolved, err := r.Resolve(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file not found: %s", path)
		}
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("path is a directory, not a file: %s", path)
	}
	return resolved, nil
}
