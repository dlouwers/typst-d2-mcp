package authdb

// Versioned schema migrations.
//
// The schema used to be two idempotent `CREATE TABLE IF NOT EXISTS`
// blobs run on every Open. That could create a missing table but could
// never alter an existing one — an ALTER expressed that way is a silent
// no-op against a deployed database, which is a trap rather than a
// limitation. Everything now moves through an ordered list of revisions
// recorded in schema_migrations.
//
// Rules that keep this safe on a live volume, where api_keys holds
// unrecoverable sha256 hashes:
//
//   - Each revision runs in its own transaction. A failure rolls that
//     revision back and aborts Open, so the process never serves against
//     a half-applied schema. A crashlooping pod is the correct outcome
//     when the alternative is silent corruption.
//   - A database recorded at a higher revision than this binary knows
//     about is refused (ErrSchemaTooNew). An old binary against a new
//     schema is undefined behaviour: it may read columns that moved or
//     write rows violating constraints it has never heard of.
//   - A pre-versioning database (tables present, no schema_migrations)
//     is stamped at revision 1 rather than re-running revision 1's DDL.
//
// Adding a migration: append to `migrations` with the next revision
// number and never edit a revision that has shipped — an already-migrated
// database will not re-run it, so an edit silently diverges old
// deployments from new ones.

import (
	"database/sql"
	"errors"
	"fmt"
)

// ErrSchemaTooNew reports a database written by a newer binary than
// this one. Returned by Open; the caller is expected to exit rather
// than continue.
var ErrSchemaTooNew = errors.New("database schema is newer than this binary supports")

// migration is one ordered, atomic schema revision. Statements run in
// order inside a single transaction.
type migration struct {
	revision int
	name     string
	stmts    []string
}

// migrations is the ordered revision list. Revision 1 is the schema as
// it stood before versioning existed, so pre-existing databases can be
// stamped at 1 without re-running anything.
var migrations = []migration{
	{
		revision: 1,
		name:     "initial schema",
		stmts: []string{
			`CREATE TABLE IF NOT EXISTS users (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  github_id    INTEGER NOT NULL UNIQUE,
  github_login TEXT    NOT NULL,
  email        TEXT,
  created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
)`,
			`CREATE TABLE IF NOT EXISTS api_keys (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  key_hash     BLOB    NOT NULL UNIQUE,
  created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_used_at TIMESTAMP
)`,
			`CREATE TABLE IF NOT EXISTS compiles (
  user_id  TEXT    NOT NULL,
  utc_date TEXT    NOT NULL,
  count    INTEGER NOT NULL,
  PRIMARY KEY(user_id, utc_date)
)`,
			`CREATE TABLE IF NOT EXISTS oauth_clients (
  client_id                  TEXT PRIMARY KEY,
  client_name                TEXT NOT NULL,
  redirect_uris              TEXT NOT NULL,
  token_endpoint_auth_method TEXT NOT NULL DEFAULT 'none',
  created_at                 TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
)`,
			`CREATE TABLE IF NOT EXISTS oauth_authorize_sessions (
  session_id            TEXT PRIMARY KEY,
  client_id             TEXT NOT NULL REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
  redirect_uri          TEXT NOT NULL,
  code_challenge        TEXT NOT NULL,
  code_challenge_method TEXT NOT NULL DEFAULT 'S256',
  scope                 TEXT,
  client_state          TEXT NOT NULL,
  expires_at            TIMESTAMP NOT NULL
)`,
			`CREATE TABLE IF NOT EXISTS oauth_authorization_codes (
  code                  TEXT PRIMARY KEY,
  user_id               INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  client_id             TEXT NOT NULL REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
  redirect_uri          TEXT NOT NULL,
  code_challenge        TEXT NOT NULL,
  code_challenge_method TEXT NOT NULL DEFAULT 'S256',
  scope                 TEXT,
  used                  INTEGER NOT NULL DEFAULT 0,
  expires_at            TIMESTAMP NOT NULL
)`,
			`CREATE TABLE IF NOT EXISTS pdf_links (
  token       TEXT PRIMARY KEY,
  user_id     TEXT NOT NULL,
  file_path   TEXT NOT NULL,
  created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  expires_at  TIMESTAMP NOT NULL
)`,
		},
	},
	{
		revision: 2,
		name:     "per-user quota override",
		stmts: []string{
			// NULL inherits the env default, 0 means unlimited (matching
			// IncrementCompile's existing limit <= 0 path), N caps at N.
			`ALTER TABLE users ADD COLUMN quota_per_day INTEGER NULL`,
		},
	},
	{
		revision: 3,
		name:     "invites",
		stmts: []string{
			// Keyed by login, not user id: an invite necessarily exists
			// before the person has ever signed in, when no github_id is
			// known yet. Stored lowercase; see NormalizeLogin.
			`CREATE TABLE IF NOT EXISTS invites (
  github_login TEXT PRIMARY KEY,
  invited_by   TEXT NOT NULL,
  created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
)`,
		},
	},
	{
		revision: 4,
		name:     "admin audit log",
		stmts: []string{
			`CREATE TABLE IF NOT EXISTS admin_audit (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  actor_login  TEXT NOT NULL,
  action       TEXT NOT NULL,
  target_login TEXT NOT NULL,
  detail       TEXT NOT NULL DEFAULT '',
  created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
)`,
			`CREATE INDEX IF NOT EXISTS idx_admin_audit_created_at
   ON admin_audit(created_at DESC)`,
		},
	},
	{
		revision: 5,
		name:     "per-user workspace storage cache",
		stmts: []string{
			// Written by the workspace sweeper, read by the admin UI, so
			// rendering the user list never walks the filesystem.
			`CREATE TABLE IF NOT EXISTS workspace_usage (
  user_id     TEXT PRIMARY KEY,
  bytes       INTEGER NOT NULL,
  computed_at TIMESTAMP NOT NULL
)`,
		},
	},
	{
		revision: 6,
		name:     "per-workspace storage budget override",
		stmts: []string{
			// Keyed by the tenant/workspace id — the same 'gh:'||github_id
			// string workspace_usage uses — not the users row: a storage
			// budget is a property of the workspace it caps, so it sits
			// next to the usage and stays correct if workspaces ever
			// decouple from users. NULL inherits the env default, 0 means
			// unlimited (matching the put_file budget<=0 path), N caps at
			// N bytes.
			`CREATE TABLE IF NOT EXISTS workspace_budgets (
  user_id      TEXT PRIMARY KEY,
  budget_bytes INTEGER
)`,
		},
	},
	{
		revision: 7,
		name:     "owners and organisation membership",
		stmts: []string{
			// An owner is a user or an organisation; its slug IS the typst
			// package namespace (@<slug>/...), so it is the primary key and
			// globally unique across both kinds. Templates belong to an
			// owner, not a user. Rung 3 of #63 creates org owners via the
			// admin UI; personal (user) owners and compile-time resolution
			// land in rung 4. created_by records the admin who made it.
			`CREATE TABLE IF NOT EXISTS owners (
  slug         TEXT PRIMARY KEY,
  kind         TEXT NOT NULL CHECK (kind IN ('user','org')),
  display_name TEXT NOT NULL DEFAULT '',
  created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  created_by   TEXT NOT NULL DEFAULT ''
)`,
			// Many-to-many from the start: a user may belong to several
			// organisations and vice versa. A users.org_id column would be
			// a migration away from wrong. user_id is the identity key
			// ('gh:<github_id>'), matching workspace_usage / compiles.
			`CREATE TABLE IF NOT EXISTS org_members (
  org_slug   TEXT NOT NULL REFERENCES owners(slug) ON DELETE CASCADE,
  user_id    TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  created_by TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (org_slug, user_id)
)`,
			// Resolving "which organisations does this user belong to?" is
			// the hot path for rung 4's compile-time namespace resolution.
			`CREATE INDEX IF NOT EXISTS idx_org_members_user ON org_members(user_id)`,
		},
	},
}

// latestRevision is the highest revision this binary knows how to apply.
func latestRevision() int {
	if len(migrations) == 0 {
		return 0
	}
	return migrations[len(migrations)-1].revision
}

// runMigrations brings the database up to latestRevision, or returns an
// error explaining why it cannot. Safe to call on every Open: current
// databases do no work.
func (s *Store) runMigrations() error {
	if _, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS schema_migrations (
  revision   INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	current, err := s.SchemaRevision()
	if err != nil {
		return err
	}

	// Pre-versioning database: the tables are already there from the old
	// IF NOT EXISTS blobs, so revision 1 has effectively been applied.
	// Stamp it rather than re-running the DDL, which would succeed but
	// misrepresent history.
	if current == 0 {
		legacy, err := s.tableExists("users")
		if err != nil {
			return err
		}
		if legacy {
			if _, err := s.db.Exec(
				`INSERT INTO schema_migrations(revision, name) VALUES(1, ?)`,
				"initial schema (baselined)",
			); err != nil {
				return fmt.Errorf("baseline pre-versioning database: %w", err)
			}
			current = 1
		}
	}

	if latest := latestRevision(); current > latest {
		return fmt.Errorf("%w: database is at revision %d, this binary supports %d — "+
			"roll forward or restore a backup", ErrSchemaTooNew, current, latest)
	}

	for _, m := range migrations {
		if m.revision <= current {
			continue
		}
		if err := s.applyMigration(m); err != nil {
			return err
		}
	}
	return nil
}

// applyMigration runs one revision atomically: every statement plus the
// schema_migrations bookkeeping row commit together, or nothing does.
func (s *Store) applyMigration(m migration) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("migration %d (%s): begin: %w", m.revision, m.name, err)
	}
	defer func() { _ = tx.Rollback() }()

	for i, stmt := range m.stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("migration %d (%s): statement %d: %w",
				m.revision, m.name, i+1, err)
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO schema_migrations(revision, name) VALUES(?, ?)`,
		m.revision, m.name,
	); err != nil {
		return fmt.Errorf("migration %d (%s): record revision: %w", m.revision, m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migration %d (%s): commit: %w", m.revision, m.name, err)
	}
	return nil
}

// SchemaRevision reports the highest applied revision, or 0 for a
// database that has never been migrated.
func (s *Store) SchemaRevision() (int, error) {
	var rev sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(revision) FROM schema_migrations`).Scan(&rev); err != nil {
		return 0, fmt.Errorf("read schema revision: %w", err)
	}
	if !rev.Valid {
		return 0, nil
	}
	return int(rev.Int64), nil
}

func (s *Store) tableExists(name string) (bool, error) {
	var found string
	err := s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name,
	).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check table %s: %w", name, err)
	}
	return true, nil
}
