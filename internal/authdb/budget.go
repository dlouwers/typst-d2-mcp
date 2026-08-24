package authdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Per-workspace storage budget overrides. Keyed by the tenant/workspace id
// (the "gh:<github_id>" string that also keys workspace_usage), not by the
// users row: a storage budget is a property of the workspace it caps, and
// keying it this way keeps it correct if workspaces ever decouple from
// users. The admin verbs still address a workspace by its owner's login,
// since that is what a human knows; the login is resolved to the workspace
// key inside the transaction.

// EffectiveWorkspaceBudget resolves the byte budget for a workspace key:
// its override when set, otherwise def. A workspace with no override row
// falls back to def, so a put_file racing a deletion fails open on the
// default rather than erroring. 0 (override or default) means unlimited.
func (s *Store) EffectiveWorkspaceBudget(ctx context.Context, userID string, def int64) (int64, error) {
	var budget sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT budget_bytes FROM workspace_budgets WHERE user_id = ?`, userID).Scan(&budget)
	if errors.Is(err, sql.ErrNoRows) {
		return def, nil
	}
	if err != nil {
		return def, fmt.Errorf("read workspace budget override: %w", err)
	}
	if !budget.Valid {
		return def, nil
	}
	return budget.Int64, nil
}

// SetWorkspaceBudget sets or clears a workspace's byte-budget override,
// addressed by its owner's login. budget nil restores the deployment
// default; 0 means unlimited; N caps at N bytes. The owner must have
// signed in — there is no workspace key to hang an override on otherwise.
func (s *Store) SetWorkspaceBudget(ctx context.Context, actor, login string, budget *int64) error {
	target := NormalizeLogin(login)
	detail := "default"
	switch {
	case budget == nil:
	case *budget == 0:
		detail = "unlimited"
	default:
		detail = fmt.Sprintf("%d bytes", *budget)
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var githubID sql.NullInt64
		err := tx.QueryRowContext(ctx,
			`SELECT github_id FROM users WHERE LOWER(github_login) = ?`, target).Scan(&githubID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoSuchUser
		}
		if err != nil {
			return fmt.Errorf("read user: %w", err)
		}
		userID := fmt.Sprintf("gh:%d", githubID.Int64)

		if budget == nil {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM workspace_budgets WHERE user_id = ?`, userID); err != nil {
				return fmt.Errorf("clear workspace budget: %w", err)
			}
		} else if _, err := tx.ExecContext(ctx, `
INSERT INTO workspace_budgets (user_id, budget_bytes) VALUES (?, ?)
ON CONFLICT(user_id) DO UPDATE SET budget_bytes = excluded.budget_bytes`,
			userID, *budget); err != nil {
			return fmt.Errorf("set workspace budget: %w", err)
		}
		return auditTx(ctx, tx, actor, ActionSetBudget, target, detail)
	})
}
