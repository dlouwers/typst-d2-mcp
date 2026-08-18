package authdb

// Administration verbs behind the /admin UI: invites, per-user quota,
// key revocation, user deletion, and the audit log that records all of
// them.
//
// Two conventions run through this file:
//
//   - Every mutation takes the acting admin's login and writes its audit
//     row inside the same transaction as the change. A change without a
//     record, or a record without a change, would both be lies.
//   - Invites are keyed by GitHub login, not user id. An invite
//     necessarily exists before its subject has ever signed in, at which
//     point no github_id is known. Logins are normalised to lowercase on
//     the way in and out so "Dlouwers" and "dlouwers" cannot both be
//     invited.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNoSuchUser is returned when an admin action names a login with
// neither a users row nor an invite.
var ErrNoSuchUser = errors.New("no such user or invite")

// Audit action names. Constants rather than inline strings so the UI and
// tests agree on spelling.
const (
	ActionInvite     = "invite"
	ActionRevoke     = "revoke_access"
	ActionRevokeKeys = "revoke_keys"
	ActionSetQuota   = "set_quota"
	ActionResetQuota = "reset_today"
	ActionDeleteUser = "delete_user"
)

// NormalizeLogin canonicalises a GitHub login for storage and
// comparison. GitHub logins are case-insensitive.
func NormalizeLogin(login string) string {
	return strings.ToLower(strings.TrimSpace(login))
}

// AdminUserRow is one line of the admin user list: an account, an
// outstanding invite, or both.
type AdminUserRow struct {
	// UserID is the identity key ("gh:42"), empty for an invite whose
	// subject has never signed in.
	UserID      string
	GitHubLogin string
	Email       string

	// SignedIn reports whether a users row exists — i.e. whether this
	// person has ever completed the OAuth flow.
	SignedIn  bool
	Invited   bool
	InvitedBy string

	// QuotaOverride is nil when the user inherits the deployment
	// default, 0 for unlimited, N for N compiles per UTC day.
	QuotaOverride *int

	UsedToday  int
	KeyCount   int
	LastUsedAt *time.Time

	// StorageBytes is nil until the sweeper has measured this user.
	StorageBytes  *int64
	StorageMeasAt *time.Time
}

// AuditEntry is one recorded admin action.
type AuditEntry struct {
	ActorLogin  string
	Action      string
	TargetLogin string
	Detail      string
	CreatedAt   time.Time
}

// AdminUsers returns the admin list: every signed-in user, plus every
// invite whose subject has not signed in yet, sorted by login.
//
// utcDate selects which day's compile counter is reported, following the
// same explicit-clock convention as IncrementCompile.
func (s *Store) AdminUsers(ctx context.Context, utcDate string) ([]AdminUserRow, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT
  'gh:' || u.github_id                                              AS user_id,
  u.github_login,
  COALESCE(u.email, ''),
  u.quota_per_day,
  (SELECT COUNT(*)   FROM api_keys k WHERE k.user_id = u.id)        AS key_count,
  -- ORDER BY ... LIMIT 1 rather than MAX(): last_used_at is written by
  -- CURRENT_TIMESTAMP, so it holds SQLite text. A plain column
  -- reference keeps the column's declared TIMESTAMP type, which is what
  -- lets the driver hand back a time.Time; wrapping it in an aggregate
  -- discards that and yields a bare string, which fails to scan into
  -- sql.NullTime. Rows where the key was never used are excluded so a
  -- NULL cannot sort above a real timestamp.
  (SELECT k.last_used_at FROM api_keys k
    WHERE k.user_id = u.id AND k.last_used_at IS NOT NULL
    ORDER BY k.last_used_at DESC LIMIT 1)                           AS last_used_at,
  (SELECT c.count FROM compiles c
     WHERE c.user_id = 'gh:' || u.github_id AND c.utc_date = ?)     AS used_today,
  (SELECT w.bytes       FROM workspace_usage w WHERE w.user_id = 'gh:' || u.github_id),
  (SELECT w.computed_at FROM workspace_usage w WHERE w.user_id = 'gh:' || u.github_id)
FROM users u
`, utcDate)
	if err != nil {
		return nil, fmt.Errorf("query admin users: %w", err)
	}
	defer func() { _ = rows.Close() }()

	byLogin := make(map[string]*AdminUserRow)
	var out []*AdminUserRow
	for rows.Next() {
		var (
			r          AdminUserRow
			quota      sql.NullInt64
			lastUsed   sql.NullTime
			usedToday  sql.NullInt64
			bytes      sql.NullInt64
			measuredAt sql.NullTime
		)
		if err := rows.Scan(&r.UserID, &r.GitHubLogin, &r.Email, &quota,
			&r.KeyCount, &lastUsed, &usedToday, &bytes, &measuredAt); err != nil {
			return nil, fmt.Errorf("scan admin user: %w", err)
		}
		r.SignedIn = true
		if quota.Valid {
			q := int(quota.Int64)
			r.QuotaOverride = &q
		}
		if lastUsed.Valid {
			t := lastUsed.Time
			r.LastUsedAt = &t
		}
		if usedToday.Valid {
			r.UsedToday = int(usedToday.Int64)
		}
		if bytes.Valid {
			b := bytes.Int64
			r.StorageBytes = &b
		}
		if measuredAt.Valid {
			t := measuredAt.Time
			r.StorageMeasAt = &t
		}
		out = append(out, &r)
		byLogin[NormalizeLogin(r.GitHubLogin)] = &r
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate admin users: %w", err)
	}

	invRows, err := s.db.QueryContext(ctx, `SELECT github_login, invited_by FROM invites`)
	if err != nil {
		return nil, fmt.Errorf("query invites: %w", err)
	}
	defer func() { _ = invRows.Close() }()
	for invRows.Next() {
		var login, invitedBy string
		if err := invRows.Scan(&login, &invitedBy); err != nil {
			return nil, fmt.Errorf("scan invite: %w", err)
		}
		key := NormalizeLogin(login)
		if existing, ok := byLogin[key]; ok {
			existing.Invited = true
			existing.InvitedBy = invitedBy
			continue
		}
		// Invited but never signed in.
		out = append(out, &AdminUserRow{
			GitHubLogin: login,
			Invited:     true,
			InvitedBy:   invitedBy,
		})
	}
	if err := invRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate invites: %w", err)
	}

	flat := make([]AdminUserRow, 0, len(out))
	for _, r := range out {
		flat = append(flat, *r)
	}
	sortRowsByLogin(flat)
	return flat, nil
}

func sortRowsByLogin(rows []AdminUserRow) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 &&
			NormalizeLogin(rows[j].GitHubLogin) < NormalizeLogin(rows[j-1].GitHubLogin); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

// IsInvited reports whether a login has an outstanding invite.
func (s *Store) IsInvited(ctx context.Context, login string) (bool, error) {
	var found string
	err := s.db.QueryRowContext(ctx,
		`SELECT github_login FROM invites WHERE github_login = ?`, NormalizeLogin(login),
	).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check invite: %w", err)
	}
	return true, nil
}

// CreateInvite adds a login to the allowlist. Idempotent: re-inviting
// someone already invited succeeds and leaves the original inviter and
// timestamp in place, so an accidental double-click is not an error.
func (s *Store) CreateInvite(ctx context.Context, actor, login string) error {
	target := NormalizeLogin(login)
	if target == "" {
		return fmt.Errorf("login is required")
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO invites(github_login, invited_by) VALUES(?, ?)
ON CONFLICT(github_login) DO NOTHING
`, target, NormalizeLogin(actor)); err != nil {
			return fmt.Errorf("insert invite: %w", err)
		}
		return auditTx(ctx, tx, actor, ActionInvite, target, "")
	})
}

// RevokeInvite removes a login from the allowlist. The allowlist is
// re-checked on every request, so access stops at once — existing API
// keys are refused rather than needing separate revocation.
func (s *Store) RevokeInvite(ctx context.Context, actor, login string) error {
	target := NormalizeLogin(login)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM invites WHERE github_login = ?`, target); err != nil {
			return fmt.Errorf("delete invite: %w", err)
		}
		return auditTx(ctx, tx, actor, ActionRevoke, target, "")
	})
}

// SetQuota sets or clears a user's per-day override. quota nil restores
// the deployment default; 0 means unlimited; N caps at N per UTC day.
// The user must have signed in — there is no row to hang an override on
// otherwise.
func (s *Store) SetQuota(ctx context.Context, actor, login string, quota *int) error {
	target := NormalizeLogin(login)
	detail := "default"
	switch {
	case quota == nil:
	case *quota == 0:
		detail = "unlimited"
	default:
		detail = fmt.Sprintf("%d/day", *quota)
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE users SET quota_per_day = ? WHERE LOWER(github_login) = ?`, quota, target)
		if err != nil {
			return fmt.Errorf("update quota: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNoSuchUser
		}
		return auditTx(ctx, tx, actor, ActionSetQuota, target, detail)
	})
}

// ResetTodayCompiles clears a user's counter for utcDate, handing them a
// fresh allowance immediately without permanently changing their quota.
func (s *Store) ResetTodayCompiles(ctx context.Context, actor, login, userID, utcDate string) error {
	target := NormalizeLogin(login)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM compiles WHERE user_id = ? AND utc_date = ?`, userID, utcDate); err != nil {
			return fmt.Errorf("reset compiles: %w", err)
		}
		return auditTx(ctx, tx, actor, ActionResetQuota, target, utcDate)
	})
}

// RevokeAPIKeys deletes every key a user holds without removing their
// access, so they can sign in again and mint a fresh one. Returns the
// number revoked.
func (s *Store) RevokeAPIKeys(ctx context.Context, actor, login string) (int64, error) {
	target := NormalizeLogin(login)
	var revoked int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
DELETE FROM api_keys
 WHERE user_id IN (SELECT id FROM users WHERE LOWER(github_login) = ?)
`, target)
		if err != nil {
			return fmt.Errorf("delete api keys: %w", err)
		}
		revoked, _ = res.RowsAffected()
		return auditTx(ctx, tx, actor, ActionRevokeKeys, target,
			fmt.Sprintf("%d key(s)", revoked))
	})
	return revoked, err
}

// DeleteUser removes every trace of a user from the database: their
// users row (cascading to api_keys), any invite, their download links,
// their compile counters, and their cached workspace size. It returns
// the identity key ("gh:42") so the caller can delete the matching
// workspace directory; empty means the login existed only as an invite
// and has no workspace.
//
// Irreversible by design — the caller is expected to have confirmed.
func (s *Store) DeleteUser(ctx context.Context, actor, login string) (string, error) {
	target := NormalizeLogin(login)
	var userID string
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var githubID sql.NullInt64
		err := tx.QueryRowContext(ctx,
			`SELECT github_id FROM users WHERE LOWER(github_login) = ?`, target).Scan(&githubID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// Invite-only: nothing but the invite to remove. Still an
			// error if there is no invite either, so a typo'd login does
			// not report success.
			var invited string
			if err := tx.QueryRowContext(ctx,
				`SELECT github_login FROM invites WHERE github_login = ?`, target).Scan(&invited); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrNoSuchUser
				}
				return fmt.Errorf("check invite: %w", err)
			}
		case err != nil:
			return fmt.Errorf("read user: %w", err)
		default:
			userID = fmt.Sprintf("gh:%d", githubID.Int64)
		}

		if userID != "" {
			for _, stmt := range []struct{ sql, what string }{
				{`DELETE FROM pdf_links       WHERE user_id = ?`, "pdf links"},
				{`DELETE FROM compiles        WHERE user_id = ?`, "compiles"},
				{`DELETE FROM workspace_usage WHERE user_id = ?`, "workspace usage"},
			} {
				if _, err := tx.ExecContext(ctx, stmt.sql, userID); err != nil {
					return fmt.Errorf("delete %s: %w", stmt.what, err)
				}
			}
			// api_keys goes with it via ON DELETE CASCADE.
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM users WHERE LOWER(github_login) = ?`, target); err != nil {
				return fmt.Errorf("delete user: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM invites WHERE github_login = ?`, target); err != nil {
			return fmt.Errorf("delete invite: %w", err)
		}
		return auditTx(ctx, tx, actor, ActionDeleteUser, target, userID)
	})
	if err != nil {
		return "", err
	}
	return userID, nil
}

// EffectiveQuota resolves the limit to apply to userID: their override
// when set, otherwise def. A user with no row falls back to def too, so
// a race between deletion and an in-flight compile fails closed on the
// default rather than erroring.
func (s *Store) EffectiveQuota(ctx context.Context, userID string, def int) (int, error) {
	var quota sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT quota_per_day FROM users WHERE 'gh:' || github_id = ?`, userID).Scan(&quota)
	if errors.Is(err, sql.ErrNoRows) {
		return def, nil
	}
	if err != nil {
		return def, fmt.Errorf("read quota override: %w", err)
	}
	if !quota.Valid {
		return def, nil
	}
	return int(quota.Int64), nil
}

// ListAudit returns the most recent admin actions, newest first.
func (s *Store) ListAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT actor_login, action, target_login, detail, created_at
  FROM admin_audit ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query audit: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ActorLogin, &e.Action, &e.TargetLogin, &e.Detail, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan audit: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit: %w", err)
	}
	return out, nil
}

// inTx runs fn in a transaction, rolling back on error.
func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// auditTx records an action on the same transaction as the change it
// describes, so the two commit together or not at all.
func auditTx(ctx context.Context, tx *sql.Tx, actor, action, target, detail string) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO admin_audit(actor_login, action, target_login, detail)
VALUES(?, ?, ?, ?)`, NormalizeLogin(actor), action, target, detail); err != nil {
		return fmt.Errorf("record audit: %w", err)
	}
	return nil
}
