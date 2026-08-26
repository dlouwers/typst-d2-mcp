package authdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The admin verbs for shared namespaces — what an administrator calls an
// organisation. There is no separate "org" entity underneath: an
// organisation is a namespace with a chosen name and more than its
// creator in it, which is exactly why a personal namespace can become
// one without migrating anything (see namespaces.go).
//
// The names here stay organisation-shaped because that is the word the
// admin UI uses. Where they take a "slug" they mean a namespace NAME.

// Audit actions, kept under their original names so existing audit rows
// stay readable.
const (
	ActionCreateOrg       = "create_org"
	ActionDeleteOrg       = "delete_org"
	ActionAddOrgMember    = "add_org_member"
	ActionRemoveOrgMember = "remove_org_member"
)

// Org is one shared namespace, presented for the admin UI.
type Org struct {
	Slug        string
	DisplayName string
	CreatedBy   string
	CreatedAt   time.Time
	MemberCount int
}

// OrgMember is one member, joined to their user row.
type OrgMember struct {
	UserID      string
	GitHubLogin string
	Email       string
	Role        string
	CreatedAt   time.Time
}

// CreateOrg creates a namespace and points a name at it. The name must
// be valid and free.
func (s *Store) CreateOrg(ctx context.Context, actor, slug, displayName string) error {
	if err := ValidateName(slug); err != nil {
		return err
	}
	id, err := newNamespaceID()
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO namespaces (id, created_by) VALUES (?, ?)`, id, actor); err != nil {
			return fmt.Errorf("create namespace: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO namespace_names (name, namespace_id, is_primary, display_name, created_by)
			 VALUES (?, ?, 1, ?, ?)`, slug, id, displayName, actor); err != nil {
			// SQLite reports a PK violation as a constraint error; the
			// name is the only unique key here.
			return ErrNameTaken
		}
		detail := slug
		if displayName != "" {
			detail = fmt.Sprintf("%s (%s)", slug, displayName)
		}
		return auditTx(ctx, tx, actor, ActionCreateOrg, slug, detail)
	})
}

// DeleteOrg removes the namespace a name points at, and by cascade its
// other names and its membership.
func (s *Store) DeleteOrg(ctx context.Context, actor, slug string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		id, err := resolveNameTx(ctx, tx, slug)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM namespaces WHERE id = ?`, id); err != nil {
			return fmt.Errorf("delete namespace: %w", err)
		}
		return auditTx(ctx, tx, actor, ActionDeleteOrg, slug, "")
	})
}

// ListOrgs returns every shared namespace with its member count.
//
// Personal namespaces are excluded: everyone has one, so listing them
// would bury the organisations an administrator came to manage in a row
// per user. A namespace appears here once it has a name somebody chose.
func (s *Store) ListOrgs(ctx context.Context) ([]Org, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT nn.name, nn.display_name, nn.created_by, nn.created_at,
       (SELECT COUNT(*) FROM namespace_members m WHERE m.namespace_id = nn.namespace_id)
  FROM namespace_names nn
 WHERE nn.is_primary = 1
   AND nn.name NOT LIKE ?
 ORDER BY nn.name`, derivedNamePrefix+"%")
	if err != nil {
		return nil, fmt.Errorf("list orgs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Org
	for rows.Next() {
		var o Org
		if err := rows.Scan(&o.Slug, &o.DisplayName, &o.CreatedBy, &o.CreatedAt, &o.MemberCount); err != nil {
			return nil, fmt.Errorf("scan org: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// AddOrgMember adds a signed-in user (by login) to a namespace. The user
// must have a users row — there is no identity key to store otherwise,
// the same constraint as SetQuota.
//
// They join as a member, not an owner: publishing is an owner's right,
// and handing it out as a side effect of being added to an organisation
// is not something an administrator asked for.
func (s *Store) AddOrgMember(ctx context.Context, actor, slug, login string) error {
	target := NormalizeLogin(login)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		id, err := resolveNameTx(ctx, tx, slug)
		if err != nil {
			return err
		}
		var githubID sql.NullInt64
		err = tx.QueryRowContext(ctx,
			`SELECT github_id FROM users WHERE LOWER(github_login) = ?`, target).Scan(&githubID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoSuchUser
		}
		if err != nil {
			return fmt.Errorf("read user: %w", err)
		}
		userID := fmt.Sprintf("gh:%d", githubID.Int64)
		res, err := tx.ExecContext(ctx, `
INSERT INTO namespace_members (namespace_id, user_id, role, created_by) VALUES (?, ?, ?, ?)
ON CONFLICT (namespace_id, user_id) DO NOTHING`, id, userID, RoleMember, actor)
		if err != nil {
			return fmt.Errorf("add member: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrAlreadyMember
		}
		return auditTx(ctx, tx, actor, ActionAddOrgMember, target, slug)
	})
}

// RemoveOrgMember removes a user (by login) from a namespace.
func (s *Store) RemoveOrgMember(ctx context.Context, actor, slug, login string) error {
	target := NormalizeLogin(login)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		id, err := resolveNameTx(ctx, tx, slug)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
DELETE FROM namespace_members
 WHERE namespace_id = ?
   AND user_id = (SELECT 'gh:' || github_id FROM users WHERE LOWER(github_login) = ?)`,
			id, target)
		if err != nil {
			return fmt.Errorf("remove member: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// Either no such user or not a member; a no-op delete is not
			// worth a hard error, but nothing to audit either.
			return ErrNoSuchUser
		}
		return auditTx(ctx, tx, actor, ActionRemoveOrgMember, target, slug)
	})
}

// ListOrgMembers returns a namespace's members, joined to their user row
// for display, login-sorted.
func (s *Store) ListOrgMembers(ctx context.Context, slug string) ([]OrgMember, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT m.user_id, u.github_login, COALESCE(u.email, ''), m.role, m.created_at
  FROM namespace_members m
  JOIN users u ON 'gh:' || u.github_id = m.user_id
  JOIN namespace_names nn ON nn.namespace_id = m.namespace_id
 WHERE nn.name = ?
 ORDER BY u.github_login`, slug)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []OrgMember
	for rows.Next() {
		var m OrgMember
		if err := rows.Scan(&m.UserID, &m.GitHubLogin, &m.Email, &m.Role, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// resolveNameTx maps a namespace name to its id inside a transaction.
func resolveNameTx(ctx context.Context, tx *sql.Tx, name string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx,
		`SELECT namespace_id FROM namespace_names WHERE name = ?`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoSuchNamespace
	}
	if err != nil {
		return "", fmt.Errorf("resolve name: %w", err)
	}
	return id, nil
}
