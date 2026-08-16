// Package sweeper reclaims state that would otherwise grow without
// bound on the server's data volume: expired PDF download links, and
// aged-out files in per-user workspaces.
//
// Workspaces are treated as scratch space. Everything in them is
// reproducible — the source can be re-uploaded and recompiled — so files
// are purged on age alone, with no distinction between put_file uploads
// and generated PDFs. The one exception is a file with an unexpired
// download link pointing at it: those are kept however old they are, so
// a URL already in someone's hands never breaks.
//
// The same pass records each user's total workspace size. The walk is
// already happening, and caching the result means the admin UI never
// does disk I/O to render a page.
package sweeper

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// Store is the subset of authdb.Store the sweeper needs.
type Store interface {
	DeleteExpiredPDFLinks(ctx context.Context, now time.Time) (int64, error)
	LivePersistedPaths(ctx context.Context, now time.Time) (map[string]map[string]bool, error)
	RecordWorkspaceUsage(ctx context.Context, userID string, bytes int64, at time.Time) error
}

// Config controls one sweeper instance.
type Config struct {
	// Root is the tenant workspace root, whose immediate children are
	// per-user directories. Empty disables all filesystem work, leaving
	// only link sweeping — the case for LocalFactory deployments where
	// the server does not own the filesystem.
	Root string

	// FileTTL is how long a workspace file survives after its last
	// modification. Zero disables the purge; usage is still measured and
	// recorded, so the admin UI works even with purging off.
	FileTTL time.Duration

	// Interval is the gap between passes.
	Interval time.Duration
}

// Result reports what one pass did.
type Result struct {
	LinksDeleted  int64
	FilesDeleted  int
	BytesDeleted  int64
	UsersMeasured int
}

// Sweeper runs periodic garbage collection.
type Sweeper struct {
	store Store
	cfg   Config
}

// New returns a Sweeper. store must be non-nil; cfg.Root may be empty.
func New(store Store, cfg Config) *Sweeper {
	return &Sweeper{store: store, cfg: cfg}
}

// Run sweeps once immediately, then on every tick until ctx is done.
// Intended to be started in a goroutine. A failing pass is logged and
// the loop continues: transient database or filesystem trouble should
// not silently stop garbage collection for the life of the process.
func (s *Sweeper) Run(ctx context.Context) {
	slog.Info("sweeper started",
		"interval", s.cfg.Interval.String(),
		"file_ttl", s.cfg.FileTTL.String(),
		"purge_enabled", s.cfg.FileTTL > 0,
		"root", s.cfg.Root,
	)
	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()

	for {
		res, err := s.SweepOnce(ctx, time.Now().UTC())
		if err != nil {
			slog.Error("sweep failed", "err", err)
		} else if res.LinksDeleted > 0 || res.FilesDeleted > 0 {
			slog.Info("sweep complete",
				"links_deleted", res.LinksDeleted,
				"files_deleted", res.FilesDeleted,
				"bytes_deleted", res.BytesDeleted,
				"users_measured", res.UsersMeasured,
			)
		}
		select {
		case <-ctx.Done():
			slog.Info("sweeper stopped")
			return
		case <-ticker.C:
		}
	}
}

// SweepOnce runs a single pass. Exported so tests can drive it without a
// clock, and so `now` stays explicit rather than read from inside.
func (s *Sweeper) SweepOnce(ctx context.Context, now time.Time) (Result, error) {
	var res Result

	deleted, err := s.store.DeleteExpiredPDFLinks(ctx, now)
	if err != nil {
		return res, fmt.Errorf("sweep links: %w", err)
	}
	res.LinksDeleted = deleted

	if s.cfg.Root == "" {
		return res, nil
	}

	// Read the live set *after* deleting expired rows: anything still
	// present is genuinely unexpired, so a file is only ever protected
	// by a link that would actually resolve.
	live, err := s.store.LivePersistedPaths(ctx, now)
	if err != nil {
		return res, fmt.Errorf("read live links: %w", err)
	}

	entries, err := os.ReadDir(s.cfg.Root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return res, nil // nothing compiled yet
		}
		return res, fmt.Errorf("read workspace root: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		userID := entry.Name()
		userRes, err := s.sweepUser(ctx, userID, live[userID], now)
		if err != nil {
			// One user's trouble must not stop the others from being
			// swept or measured.
			slog.Error("sweep user workspace failed", "user", userID, "err", err)
			continue
		}
		res.FilesDeleted += userRes.FilesDeleted
		res.BytesDeleted += userRes.BytesDeleted
		res.UsersMeasured++
	}
	return res, nil
}

// sweepUser purges and measures one user's workspace. Paths are walked
// from the user's own directory down, and symlinks are never followed
// (fs.WalkDir does not), so the pass cannot reach outside the tenant
// root even if a symlink inside it points elsewhere.
func (s *Sweeper) sweepUser(ctx context.Context, userID string, protected map[string]bool, now time.Time) (Result, error) {
	var (
		res        Result
		totalBytes int64
	)
	userRoot := filepath.Join(s.cfg.Root, userID)
	cutoff := now.Add(-s.cfg.FileTTL)

	err := filepath.WalkDir(userRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // vanished mid-walk; nothing to do
			}
			return err
		}
		// Irregular entries (symlinks, sockets) are neither measured nor
		// purged: their size is meaningless and deleting them is not the
		// sweeper's job.
		if !info.Mode().IsRegular() {
			return nil
		}

		rel, err := filepath.Rel(userRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.Clean(rel)

		if s.cfg.FileTTL > 0 && info.ModTime().Before(cutoff) && !protected[rel] {
			size := info.Size()
			if err := os.Remove(path); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return fmt.Errorf("remove %s: %w", rel, err)
			}
			res.FilesDeleted++
			res.BytesDeleted += size
			return nil
		}
		// Survivor: counts toward the size we report for this user.
		totalBytes += info.Size()
		return nil
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return res, nil
		}
		return res, err
	}

	pruneEmptyDirs(userRoot)

	if err := s.store.RecordWorkspaceUsage(ctx, userID, totalBytes, now); err != nil {
		return res, fmt.Errorf("record usage: %w", err)
	}
	return res, nil
}

// pruneEmptyDirs removes directories left empty by the purge, deepest
// first. The user's own root is kept: its absence would be
// indistinguishable from a user who has never compiled, and the
// workspace factory would just recreate it on the next request.
// Failures are ignored — an empty directory is untidy, not harmful.
func pruneEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	})
	// Deepest first, so a directory containing only empty directories
	// becomes empty in time to be removed itself.
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // fails harmlessly when not empty
	}
}
