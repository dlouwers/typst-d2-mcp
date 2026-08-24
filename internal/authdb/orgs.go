package authdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Organisations (owner kind='org') and their membership. Rung 3 of #63:
// the schema plus the admin verbs to create organisations and assign
// members. Additive — nothing here touches per-user workspaces or quota,
// and compile-time namespace resolution is deliberately left to rung 4.

// Audit actions for organisation management.
const (
	ActionCreateOrg       = "create_org"
	ActionDeleteOrg       = "delete_org"
	ActionAddOrgMember    = "add_org_member"
	ActionRemoveOrgMember = "remove_org_member"
)

var (
	// ErrInvalidSlug is returned when a proposed owner slug is malformed
	// or reserved.
	ErrInvalidSlug = errors.New("invalid owner slug")
	// ErrSlugTaken is returned when an owner with that slug already exists.
	ErrSlugTaken = errors.New("owner slug already taken")
	// ErrNoSuchOrg is returned when an organisation slug does not resolve.
	ErrNoSuchOrg = errors.New("no such organisation")
	// ErrAlreadyMember is returned when a user is already in the org.
	ErrAlreadyMember = errors.New("already a member")
)

// slugPattern is the namespace grammar: it becomes @<slug>/... in a Typst
// import, so keep it to what reads cleanly there and in a URL — lowercase
// letters, digits and hyphens, not leading/trailing a hyphen.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// reservedSlugs cannot be claimed as owners: "preview" is typst's own
// package namespace, and "house" is the built-in shipped template owner
// (formalising it as an owner row waits for rung 4's resolution work).
var reservedSlugs = map[string]bool{"preview": true, "house": true}

// ValidateSlug reports whether slug is a well-formed, non-reserved owner
// namespace. Exposed so the admin handler can validate before a round trip.
func ValidateSlug(slug string) error {
	switch {
	case len(slug) < 2 || len(slug) > 39:
		return fmt.Errorf("%w: must be 2–39 characters", ErrInvalidSlug)
	case !slugPattern.MatchString(slug):
		return fmt.Errorf("%w: use lowercase letters, digits and hyphens", ErrInvalidSlug)
	case reservedSlugs[slug]:
		return fmt.Errorf("%w: %q is reserved", ErrInvalidSlug, slug)
	}
	return nil
}

// Org is one organisation owner, with its current member count.
type Org struct {
	Slug        string
	DisplayName string
	CreatedBy   string
	CreatedAt   time.Time
	MemberCount int
}

// OrgMember is one member of an organisation, joined to their user row.
type OrgMember struct {
	UserID      string
	GitHubLogin string
	Email       string
	CreatedAt   time.Time
}

// CreateOrg creates an organisation owner. The slug must be valid and free.
func (s *Store) CreateOrg(ctx context.Context, actor, slug, displayName string) error {
	if err := ValidateSlug(slug); err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO owners (slug, kind, display_name, created_by) VALUES (?, 'org', ?, ?)`,
			slug, displayName, actor)
		if err != nil {
			// SQLite reports a PK violation as a constraint error; the slug
			// is the only unique key here.
			return ErrSlugTaken
		}
		detail := slug
		if displayName != "" {
			detail = fmt.Sprintf("%s (%s)", slug, displayName)
		}
		return auditTx(ctx, tx, actor, ActionCreateOrg, slug, detail)
	})
}

// DeleteOrg removes an organisation and, by cascade, its membership rows.
func (s *Store) DeleteOrg(ctx context.Context, actor, slug string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM owners WHERE slug = ? AND kind = 'org'`, slug)
		if err != nil {
			return fmt.Errorf("delete org: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNoSuchOrg
		}
		return auditTx(ctx, tx, actor, ActionDeleteOrg, slug, "")
	})
}

// ListOrgs returns every organisation with its member count, slug-sorted.
func (s *Store) ListOrgs(ctx context.Context) ([]Org, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT o.slug, o.display_name, o.created_by, o.created_at,
       (SELECT COUNT(*) FROM org_members m WHERE m.org_slug = o.slug)
  FROM owners o
 WHERE o.kind = 'org'
 ORDER BY o.slug`)
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

// AddOrgMember adds a signed-in user (by login) to an organisation. The
// user must have a users row — there is no identity key to store
// otherwise, the same constraint as SetQuota.
func (s *Store) AddOrgMember(ctx context.Context, actor, slug, login string) error {
	target := NormalizeLogin(login)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := orgMustExist(ctx, tx, slug); err != nil {
			return err
		}
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
		res, err := tx.ExecContext(ctx, `
INSERT INTO org_members (org_slug, user_id, created_by) VALUES (?, ?, ?)
ON CONFLICT (org_slug, user_id) DO NOTHING`, slug, userID, actor)
		if err != nil {
			return fmt.Errorf("add member: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrAlreadyMember
		}
		return auditTx(ctx, tx, actor, ActionAddOrgMember, target, slug)
	})
}

// RemoveOrgMember removes a user (by login) from an organisation.
func (s *Store) RemoveOrgMember(ctx context.Context, actor, slug, login string) error {
	target := NormalizeLogin(login)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
DELETE FROM org_members
 WHERE org_slug = ?
   AND user_id = (SELECT 'gh:' || github_id FROM users WHERE LOWER(github_login) = ?)`,
			slug, target)
		if err != nil {
			return fmt.Errorf("remove member: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// Either no such org/user or not a member; a no-op delete is not
			// worth a hard error, but nothing to audit either.
			return ErrNoSuchUser
		}
		return auditTx(ctx, tx, actor, ActionRemoveOrgMember, target, slug)
	})
}

// ListOrgMembers returns an organisation's members, joined to their user
// row for display, login-sorted.
func (s *Store) ListOrgMembers(ctx context.Context, slug string) ([]OrgMember, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT m.user_id, u.github_login, COALESCE(u.email, ''), m.created_at
  FROM org_members m
  JOIN users u ON 'gh:' || u.github_id = m.user_id
 WHERE m.org_slug = ?
 ORDER BY u.github_login`, slug)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []OrgMember
	for rows.Next() {
		var m OrgMember
		if err := rows.Scan(&m.UserID, &m.GitHubLogin, &m.Email, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// OrgsForUser returns the slugs of every organisation a user belongs to.
// This is the resolution primitive rung 4 will build compile-time
// namespace visibility on; exposed now so the data model is exercised.
func (s *Store) OrgsForUser(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT org_slug FROM org_members WHERE user_id = ? ORDER BY org_slug`, userID)
	if err != nil {
		return nil, fmt.Errorf("orgs for user: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, fmt.Errorf("scan org slug: %w", err)
		}
		out = append(out, slug)
	}
	return out, rows.Err()
}

// orgMustExist returns ErrNoSuchOrg unless slug names an existing org.
func orgMustExist(ctx context.Context, tx *sql.Tx, slug string) error {
	var one int
	err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM owners WHERE slug = ? AND kind = 'org'`, slug).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNoSuchOrg
	}
	if err != nil {
		return fmt.Errorf("check org: %w", err)
	}
	return nil
}
