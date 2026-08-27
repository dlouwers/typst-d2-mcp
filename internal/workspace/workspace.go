// Package workspace abstracts the mapping from tool-visible paths to
// concrete on-disk paths the server can read and write.
//
// LocalFS is used in stdio (local) mode where the server and client
// share a filesystem; paths pass through unchanged. ScopedFS confines
// every path under a configured root and rejects traversal, used in
// HTTP (hosted) mode so the same file_path API works against a
// server-managed workspace.
package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/dlouwers/typst-d2-mcp/internal/identity"
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

// ScopedFS confines every resolved path under Root. Absolute inputs are
// rejected; traversal sequences ("..") that climb above Root are rejected
// even when the textual path itself looks benign. Used in HTTP/hosted
// mode where the same file_path API must operate on a server-managed
// workspace rather than the client's filesystem.
type ScopedFS struct {
	// Root is the absolute filesystem path that bounds every resolution.
	// Callers should pass an already-cleaned absolute path; NewScopedFS
	// handles that for them.
	Root string
}

// NewScopedFS prepares a ScopedFS rooted at root, creating the directory
// (mode 0o700) if it does not yet exist. The stored Root is the cleaned
// absolute form.
func NewScopedFS(root string) (*ScopedFS, error) {
	if root == "" {
		return nil, fmt.Errorf("empty workspace root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("abs(%q): %w", root, err)
	}
	abs = filepath.Clean(abs)
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("create workspace root: %w", err)
	}
	return &ScopedFS{Root: abs}, nil
}

// Resolve joins path under Root and rejects any input that escapes it.
// Absolute paths are rejected so that the same tool surface ("a relative
// path inside the workspace") works regardless of transport.
func (s *ScopedFS) Resolve(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("absolute paths are not allowed in scoped workspace: %s", path)
	}
	joined := filepath.Join(s.Root, path)
	cleaned := filepath.Clean(joined)
	rel, err := filepath.Rel(s.Root, cleaned)
	if err != nil {
		return "", fmt.Errorf("rel(%q, %q): %w", s.Root, cleaned, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes workspace: %s", path)
	}
	return cleaned, nil
}

// WorkspaceRoot returns the absolute directory that bounds this resolver,
// satisfying Bounded. It is the measurable extent of a scoped workspace.
func (s *ScopedFS) WorkspaceRoot() string { return s.Root }

// Bounded is implemented by resolvers whose workspace is confined to a
// single on-disk root, making its total size a meaningful quantity.
// LocalFS deliberately does not implement it: in stdio mode the
// "workspace" is the shared filesystem and has no size worth reporting.
type Bounded interface {
	WorkspaceRoot() string
}

// Usage reports the total bytes stored in r's workspace and whether that
// number is meaningful. Bounded resolvers (ScopedFS) return their measured
// size; unbounded ones (LocalFS) return tracked=false with zero bytes.
func Usage(r Resolver) (bytes int64, tracked bool, err error) {
	b, ok := r.(Bounded)
	if !ok {
		return 0, false, nil
	}
	total, err := DirBytes(b.WorkspaceRoot())
	if err != nil {
		return 0, true, err
	}
	return total, true, nil
}

// StagePrefix marks a file the server staged inside a workspace for the
// duration of one compile. The preprocessed source has to live next to
// the document's assets for typst to resolve their relative paths, which
// means it lands in the workspace — but it is server scratch, not the
// tenant's data, so it does not count towards usage and must not be
// mistaken for something the caller put there.
const StagePrefix = ".typst-d2-stage-"

// DirBytes returns the summed size of the regular files under dir,
// walking recursively. Symlinks and other irregular entries are skipped
// (their size is meaningless and WalkDir does not follow them), matching
// how the sweeper measures a workspace. Files staged by an in-flight
// compile are skipped too — counting them would let a large document
// briefly inflate reported usage, or push its own compile over a byte
// budget. A dir that does not exist counts as zero: a user who has
// written nothing yet has an empty workspace, not an error.
func DirBytes(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // vanished mid-walk (or absent root); skip
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if strings.HasPrefix(d.Name(), StagePrefix) {
			return nil
		}
		total += info.Size()
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return total, err
	}
	return total, nil
}

// Factory picks the workspace.Resolver for a given identity. It lets
// the server route per-tenant requests to per-tenant filesystems
// without each handler knowing whether the deployment is local,
// shared-scoped, or per-user.
type Factory interface {
	// Resolver returns the active resolver for id. Stateless
	// deployments may ignore id; tenant deployments return a
	// ScopedFS rooted under id.UserID.
	Resolver(id identity.Identity) (Resolver, error)
}

// LocalFactory always returns LocalFS{}, regardless of identity. Used
// in stdio mode and for self-hosted deployments without auth.
type LocalFactory struct{}

// Resolver implements Factory.
func (LocalFactory) Resolver(identity.Identity) (Resolver, error) {
	return LocalFS{}, nil
}

// TenantFactory issues a per-identity ScopedFS rooted at Root/UserID.
// Roots are created on demand with 0o700 permissions; concurrent
// requests for the same user race on MkdirAll harmlessly.
type TenantFactory struct {
	Root string
}

// Resolver implements Factory. id.UserID must be non-empty.
func (f TenantFactory) Resolver(id identity.Identity) (Resolver, error) {
	if id.UserID == "" {
		return nil, fmt.Errorf("tenant factory requires non-empty UserID")
	}
	dir := filepath.Join(f.Root, DirName(id.UserID))
	// A workspace created before DirName existed sits under the raw user
	// id. Move it once, rather than leaving the old path to be found by
	// some code paths and not others.
	if err := migrateLegacyDir(filepath.Join(f.Root, id.UserID), dir); err != nil {
		return nil, err
	}
	return NewScopedFS(dir)
}

// migrateLegacyDir renames a pre-DirName workspace into place.
//
// Idempotent and safe to race: two servers starting together may both
// try, and the loser sees the destination already exists, which is the
// correct outcome rather than an error. If both exist — a half-finished
// move, or something hand-made — the new one wins and the old is left
// alone for a person to look at, because silently merging two
// directories of someone's documents is not a decision code should make.
func migrateLegacyDir(legacy, current string) error {
	if legacy == current {
		return nil
	}
	if _, err := os.Stat(current); err == nil {
		return nil // already migrated, or never needed
	}
	info, err := os.Stat(legacy)
	if err != nil || !info.IsDir() {
		return nil //nolint:nilerr // nothing to migrate is the normal case
	}
	if err := os.Rename(legacy, current); err != nil {
		if _, statErr := os.Stat(current); statErr == nil {
			return nil // someone else won the race
		}
		return fmt.Errorf("migrate workspace %s: %w", legacy, err)
	}
	slog.Info("migrated a workspace to a path-safe directory name",
		"from", filepath.Base(legacy), "to", filepath.Base(current))
	return nil
}

// WriteFile resolves path through r and writes content, creating parent
// directories as needed and truncating any existing file. It is the
// back-end for the put_file MCP tool's default (overwrite) mode. The
// returned string is the resolved on-disk path (useful for logging /
// tests); callers should not echo it back to clients in HTTP mode.
func WriteFile(r Resolver, path string, content []byte) (string, error) {
	resolved, err := r.Resolve(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o700); err != nil {
		return "", fmt.Errorf("create parent dirs: %w", err)
	}
	if err := os.WriteFile(resolved, content, 0o600); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}
	return resolved, nil
}

// AppendFile resolves path through r and appends content to the file,
// creating it (and parent directories) if absent. It is the back-end for
// put_file's append mode, which lets a client stream a file larger than
// the per-call size cap in bounded chunks. Callers should not echo the
// resolved path back to clients in HTTP mode.
func AppendFile(r Resolver, path string, content []byte) (string, error) {
	resolved, err := r.Resolve(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o700); err != nil {
		return "", fmt.Errorf("create parent dirs: %w", err)
	}
	f, err := os.OpenFile(resolved, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("open for append: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(content); err != nil {
		return "", fmt.Errorf("append to file: %w", err)
	}
	return resolved, nil
}
