package authdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// A namespace owns templates. It has an id that never changes and never
// appears in an import, and one or more names that do.
//
// Separating the two is what keeps growth from being a migration. Under
// the earlier design the name WAS the identity, so renaming meant moving
// content and a personal namespace could only become a shared one by
// changing what kind of thing it was — GitHub's model, whose "convert
// your account to an organization" is a real migration for exactly that
// reason. Here a rename inserts a name row, and sharing inserts member
// rows. Nothing that holds content ever moves.
//
// Many names may point at one namespace, so an old name keeps resolving
// after a rename and documents that already import it never break.

// BuiltinNamespaceID is the namespace holding the templates the server
// ships. Its id and its name are both "house": the id matches the
// directory the image seeds, so the built-in needs no special case in
// resolution.
const BuiltinNamespaceID = "house"

// derivedNamePrefix marks a name generated for a personal namespace at
// sign-in. It is derived from the GitHub numeric id, which — unlike the
// login — never changes when someone renames themselves, so a document
// importing it cannot be broken by a rename elsewhere.
//
// Nobody may claim a name with this prefix: without that rule, claiming
// "gh-12345" before that user first signs in would hand you their
// namespace.
const derivedNamePrefix = "gh-"

// Audit actions for namespace management.
const (
	ActionCreateNamespace = "create_namespace"
	ActionDeleteNamespace = "delete_namespace"
	ActionAddMember       = "add_namespace_member"
	ActionRemoveMember    = "remove_namespace_member"
	ActionAddName         = "add_namespace_name"
)

// Roles on a membership. Only an owner may publish.
const (
	RoleOwner  = "owner"
	RoleMember = "member"
)

var (
	// ErrInvalidName is returned when a proposed namespace name is
	// malformed or reserved.
	ErrInvalidName = errors.New("invalid namespace name")
	// ErrNameTaken is returned when a name already points somewhere.
	ErrNameTaken = errors.New("namespace name already taken")
	// ErrNoSuchNamespace is returned when a name does not resolve.
	ErrNoSuchNamespace = errors.New("no such namespace")
	// ErrAlreadyMember is returned when a user is already a member.
	ErrAlreadyMember = errors.New("already a member")
	// ErrNotOwner is returned when an action needs the owner role.
	ErrNotOwner = errors.New("not an owner of this namespace")
)

// namePattern is the grammar typst accepts, narrowed to what reads
// cleanly in an import and a URL. typst rejects a purely numeric
// namespace, which is why a derived name carries a prefix rather than
// being the bare GitHub id.
var namePattern = regexp.MustCompile(`^[a-z0-9]*[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// reservedNames cannot be claimed: "preview" is typst's own package
// namespace and "house" is the built-in.
var reservedNames = map[string]bool{"preview": true, BuiltinNamespaceID: true}

// ValidateName reports whether name is a well-formed, claimable
// namespace name. Exposed so a handler can validate before a round trip.
func ValidateName(name string) error {
	switch {
	case len(name) < 2 || len(name) > 39:
		return fmt.Errorf("%w: must be 2–39 characters", ErrInvalidName)
	case !namePattern.MatchString(name):
		return fmt.Errorf("%w: use lowercase letters, digits and hyphens, and include at least one letter", ErrInvalidName)
	case reservedNames[name]:
		return fmt.Errorf("%w: %q is reserved", ErrInvalidName, name)
	case strings.HasPrefix(name, derivedNamePrefix):
		return fmt.Errorf("%w: %q is reserved for personal namespaces", ErrInvalidName, derivedNamePrefix+"…")
	}
	return nil
}

// DerivedName is the name a personal namespace starts with, from the
// stable GitHub numeric id rather than the mutable login.
func DerivedName(githubID int64) string {
	return fmt.Sprintf("%s%d", derivedNamePrefix, githubID)
}

// newNamespaceID mints an opaque id. It is deliberately not the name:
// nothing should be tempted to read meaning out of it, and it has to
// survive every rename the namespace ever has.
func newNamespaceID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate namespace id: %w", err)
	}
	return "ns-" + hex.EncodeToString(b[:]), nil
}

// NamespaceRef is a name together with what it points at.
type NamespaceRef struct {
	Name        string
	NamespaceID string
	DisplayName string
	IsPrimary   bool
	Role        string
}

// EnsurePersonalNamespace gives a user their own namespace if they do
// not have one, returning its id. Called at sign-in: everyone always has
// a namespace, so there is never a moment where someone has nowhere to
// put a template.
//
// The namespace is created with the user as its owner, which is what
// makes publishing to your own namespace need no permission model at
// all — and lets the multi-party version wait for a real second party.
func (s *Store) EnsurePersonalNamespace(ctx context.Context, userID string, githubID int64) (string, error) {
	name := DerivedName(githubID)

	var existing string
	err := s.db.QueryRowContext(ctx,
		`SELECT namespace_id FROM namespace_names WHERE name = ?`, name).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("look up personal namespace: %w", err)
	}

	id, err := newNamespaceID()
	if err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO namespaces (id, created_by) VALUES (?, ?)`, id, userID); err != nil {
		return "", fmt.Errorf("create namespace: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO namespace_names (name, namespace_id, is_primary, created_by)
		 VALUES (?, ?, 1, ?)`, name, id, userID); err != nil {
		return "", fmt.Errorf("name namespace: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO namespace_members (namespace_id, user_id, role, created_by)
		 VALUES (?, ?, ?, ?)`, id, userID, RoleOwner, userID); err != nil {
		return "", fmt.Errorf("add owner: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return id, nil
}

// NamespacesForUser returns every name this user may import, mapped to
// the namespace it points at. This is the whole of what a compile may
// resolve, and the same answer list_templates reports, so the two cannot
// disagree.
//
// The built-in is not included here — it is readable by everyone and is
// added by the caller, so this function stays about membership.
func (s *Store) NamespacesForUser(ctx context.Context, userID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT nn.name, nn.namespace_id
		   FROM namespace_names nn
		   JOIN namespace_members nm ON nm.namespace_id = nn.namespace_id
		  WHERE nm.user_id = ?
		  ORDER BY nn.name`, userID)
	if err != nil {
		return nil, fmt.Errorf("namespaces for user: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]string{}
	for rows.Next() {
		var name, id string
		if err := rows.Scan(&name, &id); err != nil {
			return nil, fmt.Errorf("scan namespace: %w", err)
		}
		out[name] = id
	}
	return out, rows.Err()
}

// RoleFor reports a user's role in a namespace, or "" if not a member.
func (s *Store) RoleFor(ctx context.Context, namespaceID, userID string) (string, error) {
	var role string
	err := s.db.QueryRowContext(ctx,
		`SELECT role FROM namespace_members WHERE namespace_id = ? AND user_id = ?`,
		namespaceID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("role lookup: %w", err)
	}
	return role, nil
}

// ResolveName maps a name to the namespace it points at.
func (s *Store) ResolveName(ctx context.Context, name string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT namespace_id FROM namespace_names WHERE name = ?`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoSuchNamespace
	}
	if err != nil {
		return "", fmt.Errorf("resolve name: %w", err)
	}
	return id, nil
}

// AddName points another name at an existing namespace and makes it the
// primary one. This is the whole of "upgrade a personal namespace to a
// shared one with a proper name": the id does not change, the templates
// do not move, and the previous name keeps resolving so documents that
// already import it go on working.
func (s *Store) AddName(ctx context.Context, actor, namespaceID, name, displayName string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	if err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM namespaces WHERE id = ?`, namespaceID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoSuchNamespace
		}
		return fmt.Errorf("check namespace: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE namespace_names SET is_primary = 0 WHERE namespace_id = ?`, namespaceID); err != nil {
		return fmt.Errorf("demote names: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO namespace_names (name, namespace_id, is_primary, display_name, created_by)
		 VALUES (?, ?, 1, ?, ?)`, name, namespaceID, displayName, actor); err != nil {
		// SQLite reports a PK violation as a constraint error; the name
		// is the only unique key here.
		return ErrNameTaken
	}
	if err := auditTx(ctx, tx, actor, ActionAddName, name, namespaceID); err != nil {
		return err
	}
	return tx.Commit()
}
