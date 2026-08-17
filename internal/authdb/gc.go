package authdb

// Garbage-collection queries backing the background sweeper, plus the
// per-user workspace usage cache it populates.
//
// LookupPDFLink already prunes an expired row when someone fetches that
// token, but a link that is minted and never clicked is never read, so
// nothing ever deletes it. DeleteExpiredPDFLinks is the pass that
// catches those.
//
// Callers pass `now` explicitly rather than having these read the clock,
// matching IncrementCompile's utcDate parameter: it lets tests cover
// expiry without sleeping.

import (
	"context"
	"fmt"
	"path/filepath"
	"time"
)

// WorkspaceUsage is a cached measurement of one user's workspace size.
// The admin UI reads these rather than walking the filesystem on page
// load; ComputedAt is rendered alongside so a stale number reads as
// stale rather than as truth.
type WorkspaceUsage struct {
	UserID     string
	Bytes      int64
	ComputedAt time.Time
}

// DeleteExpiredPDFLinks removes every link whose expires_at has passed,
// returning the number deleted. Safe to run concurrently with
// LookupPDFLink's opportunistic delete of the same row — both are
// single-statement DELETEs against the primary key, so the loser simply
// affects zero rows.
func (s *Store) DeleteExpiredPDFLinks(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM pdf_links WHERE expires_at < ?`, now.UTC())
	if err != nil {
		return 0, fmt.Errorf("delete expired pdf links: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil // driver did not report; the delete still happened
	}
	return n, nil
}

// LivePersistedPaths returns, per user id, the set of workspace-relative
// file paths that still have an unexpired download link. The file purge
// consults this so a PDF someone is about to fetch is never deleted out
// from under a live URL, however old the file is.
//
// Paths are cleaned so comparisons against walked filesystem paths are
// exact ("./out.pdf" and "out.pdf" are the same file).
func (s *Store) LivePersistedPaths(ctx context.Context, now time.Time) (map[string]map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT user_id, file_path FROM pdf_links WHERE expires_at >= ?`, now.UTC())
	if err != nil {
		return nil, fmt.Errorf("query live pdf links: %w", err)
	}
	defer func() { _ = rows.Close() }()

	live := make(map[string]map[string]bool)
	for rows.Next() {
		var userID, path string
		if err := rows.Scan(&userID, &path); err != nil {
			return nil, fmt.Errorf("scan live pdf link: %w", err)
		}
		if live[userID] == nil {
			live[userID] = make(map[string]bool)
		}
		live[userID][filepath.Clean(path)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate live pdf links: %w", err)
	}
	return live, nil
}

// RecordWorkspaceUsage upserts the cached size for one user.
func (s *Store) RecordWorkspaceUsage(ctx context.Context, userID string, bytes int64, at time.Time) error {
	if userID == "" {
		return fmt.Errorf("userID is required")
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO workspace_usage(user_id, bytes, computed_at) VALUES(?, ?, ?)
ON CONFLICT(user_id) DO UPDATE SET bytes = excluded.bytes, computed_at = excluded.computed_at
`, userID, bytes, at.UTC()); err != nil {
		return fmt.Errorf("record workspace usage: %w", err)
	}
	return nil
}

// WorkspaceUsageByUser returns every cached measurement, keyed by user
// id. A user absent from the map has not been measured yet — the admin
// UI renders that as "not yet computed" rather than as zero.
func (s *Store) WorkspaceUsageByUser(ctx context.Context) (map[string]WorkspaceUsage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT user_id, bytes, computed_at FROM workspace_usage`)
	if err != nil {
		return nil, fmt.Errorf("query workspace usage: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]WorkspaceUsage)
	for rows.Next() {
		var u WorkspaceUsage
		if err := rows.Scan(&u.UserID, &u.Bytes, &u.ComputedAt); err != nil {
			return nil, fmt.Errorf("scan workspace usage: %w", err)
		}
		out[u.UserID] = u
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspace usage: %w", err)
	}
	return out, nil
}
