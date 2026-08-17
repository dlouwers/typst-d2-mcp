package authdb

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// withMigrations swaps the package migration list for the duration of a
// test. Tests that need to exercise applying, ordering, or failing a
// migration cannot use the real list — every shipped revision is already
// applied by the time Open returns.
func withMigrations(t *testing.T, list []migration) {
	t.Helper()
	saved := migrations
	migrations = list
	t.Cleanup(func() { migrations = saved })
}

// openRaw opens the SQLite file without running migrations, so tests can
// inspect or corrupt schema state directly.
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestMigrations_FreshDatabaseReachesLatest(t *testing.T) {
	s := newStore(t)

	rev, err := s.SchemaRevision()
	if err != nil {
		t.Fatalf("SchemaRevision: %v", err)
	}
	if want := latestRevision(); rev != want {
		t.Errorf("revision = %d, want %d", rev, want)
	}
}

// Every table the pre-versioning schema created must still exist after a
// migration from empty, so revision 1 is a faithful transcription.
func TestMigrations_CreatesAllLegacyTables(t *testing.T) {
	s := newStore(t)

	for _, table := range []string{
		"users", "api_keys", "compiles",
		"oauth_clients", "oauth_authorize_sessions", "oauth_authorization_codes",
		"pdf_links",
	} {
		exists, err := s.tableExists(table)
		if err != nil {
			t.Fatalf("tableExists(%s): %v", table, err)
		}
		if !exists {
			t.Errorf("table %q missing after migration from empty", table)
		}
	}
}

// The api_keys -> users cascade is load-bearing for user deletion, and a
// transcription slip in revision 1 would silently drop it.
func TestMigrations_ForeignKeyCascadePreserved(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	uid, err := s.UpsertGitHubUser(ctx, 42, "octocat", "")
	if err != nil {
		t.Fatalf("UpsertGitHubUser: %v", err)
	}
	if _, err := s.IssueAPIKey(ctx, uid); err != nil {
		t.Fatalf("IssueAPIKey: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, uid); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	var keys int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM api_keys WHERE user_id = ?`, uid).Scan(&keys); err != nil {
		t.Fatalf("count keys: %v", err)
	}
	if keys != 0 {
		t.Errorf("api_keys rows after user delete = %d, want 0 (cascade lost)", keys)
	}
}

func TestMigrations_ReopenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	rev1, _ := first.SchemaRevision()
	_ = first.Close()

	second, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer func() { _ = second.Close() }()
	rev2, _ := second.SchemaRevision()

	if rev1 != rev2 {
		t.Errorf("revision changed across reopen: %d -> %d", rev1, rev2)
	}

	// Each revision must be recorded exactly once — a re-application
	// would either fail on the primary key or duplicate history.
	var rows int
	if err := second.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&rows); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if rows != latestRevision() {
		t.Errorf("schema_migrations rows = %d, want %d", rows, latestRevision())
	}
}

// The upgrade path that matters in production: a volume written by the
// old CREATE TABLE IF NOT EXISTS build, opened by this one.
func TestMigrations_BaselinesPreVersioningDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")

	// Stand up the legacy schema by hand, including a user row that must
	// survive, and deliberately no schema_migrations table.
	raw := openRaw(t, path)
	for _, stmt := range migrations[0].stmts {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("legacy DDL: %v", err)
		}
	}
	if _, err := raw.Exec(
		`INSERT INTO users(github_id, github_login, email) VALUES(7, 'legacy', 'l@example.com')`,
	); err != nil {
		t.Fatalf("seed legacy user: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open legacy database: %v", err)
	}
	defer func() { _ = s.Close() }()

	rev, _ := s.SchemaRevision()
	if want := latestRevision(); rev != want {
		t.Errorf("revision = %d, want %d", rev, want)
	}

	// Revision 1 must be recorded as baselined, not re-run.
	var name string
	if err := s.db.QueryRow(
		`SELECT name FROM schema_migrations WHERE revision = 1`).Scan(&name); err != nil {
		t.Fatalf("read revision 1 row: %v", err)
	}
	if !strings.Contains(name, "baselined") {
		t.Errorf("revision 1 name = %q, want it marked as baselined", name)
	}

	// The pre-existing row survives, and later revisions applied on top.
	var login string
	if err := s.db.QueryRow(
		`SELECT github_login FROM users WHERE github_id = 7`).Scan(&login); err != nil {
		t.Fatalf("legacy user lost: %v", err)
	}
	if login != "legacy" {
		t.Errorf("github_login = %q, want %q", login, "legacy")
	}
	var quota sql.NullInt64
	if err := s.db.QueryRow(
		`SELECT quota_per_day FROM users WHERE github_id = 7`).Scan(&quota); err != nil {
		t.Fatalf("quota_per_day column not applied to legacy database: %v", err)
	}
	if quota.Valid {
		t.Errorf("quota_per_day = %v, want NULL for an untouched legacy user", quota.Int64)
	}
}

// An ALTER on an existing database is exactly what the old DDL-blob
// approach could not express, so it gets a direct test.
func TestMigrations_AlterAppliesToExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite")

	base := []migration{{
		revision: 1,
		name:     "base",
		stmts:    []string{`CREATE TABLE widgets (id INTEGER PRIMARY KEY)`},
	}}
	withMigrations(t, base)

	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := first.db.Exec(`INSERT INTO widgets(id) VALUES(1)`); err != nil {
		t.Fatalf("seed widgets: %v", err)
	}
	_ = first.Close()

	// Second binary knows one more revision.
	withMigrations(t, append(base, migration{
		revision: 2,
		name:     "add colour",
		stmts:    []string{`ALTER TABLE widgets ADD COLUMN colour TEXT`},
	}))

	second, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer func() { _ = second.Close() }()

	if rev, _ := second.SchemaRevision(); rev != 2 {
		t.Errorf("revision = %d, want 2", rev)
	}
	var colour sql.NullString
	if err := second.db.QueryRow(`SELECT colour FROM widgets WHERE id = 1`).Scan(&colour); err != nil {
		t.Fatalf("ALTER did not apply to existing row: %v", err)
	}
}

// Revisions must be ascending and unique: applyMigration skips anything
// <= current, so an out-of-order or duplicated entry would be silently
// skipped rather than rejected.
func TestMigrations_OrderedAndUnique(t *testing.T) {
	seen := map[int]bool{}
	prev := 0
	for i, m := range migrations {
		if m.revision <= prev {
			t.Errorf("migrations[%d] revision %d is not greater than previous %d",
				i, m.revision, prev)
		}
		if seen[m.revision] {
			t.Errorf("duplicate revision %d", m.revision)
		}
		if m.name == "" {
			t.Errorf("migrations[%d] (revision %d) has no name", i, m.revision)
		}
		if len(m.stmts) == 0 {
			t.Errorf("migrations[%d] (revision %d) has no statements", i, m.revision)
		}
		seen[m.revision] = true
		prev = m.revision
	}
}

func TestMigrations_FailureRollsBackAndAbortsOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite")

	withMigrations(t, []migration{
		{revision: 1, name: "base", stmts: []string{`CREATE TABLE widgets (id INTEGER PRIMARY KEY)`}},
		{revision: 2, name: "half broken", stmts: []string{
			`CREATE TABLE gadgets (id INTEGER PRIMARY KEY)`, // valid, must be rolled back
			`THIS IS NOT SQL`, // fails
		}},
	})

	s, err := Open(path)
	if err == nil {
		_ = s.Close()
		t.Fatal("Open succeeded despite a failing migration")
	}
	if s != nil {
		t.Error("Open returned a non-nil Store alongside an error")
	}
	// The error must name the revision so an operator can find it.
	if !strings.Contains(err.Error(), "migration 2") {
		t.Errorf("error does not name the failing revision: %v", err)
	}
	if !strings.Contains(err.Error(), "half broken") {
		t.Errorf("error does not name the failing migration: %v", err)
	}

	// Revision stays at 1 and the partial statement is gone.
	raw := openRaw(t, path)
	var rev sql.NullInt64
	if err := raw.QueryRow(`SELECT MAX(revision) FROM schema_migrations`).Scan(&rev); err != nil {
		t.Fatalf("read revision: %v", err)
	}
	if !rev.Valid || rev.Int64 != 1 {
		t.Errorf("recorded revision = %v, want 1", rev)
	}
	var name string
	err = raw.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='gadgets'`).Scan(&name)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("gadgets table survived a failed migration (rollback did not happen): %v", err)
	}
}

func TestMigrations_RefusesNewerDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	future := latestRevision() + 1
	if _, err := s.db.Exec(
		`INSERT INTO schema_migrations(revision, name) VALUES(?, 'from the future')`, future,
	); err != nil {
		t.Fatalf("stamp future revision: %v", err)
	}
	_ = s.Close()

	reopened, err := Open(path)
	if err == nil {
		_ = reopened.Close()
		t.Fatal("Open succeeded against a newer-than-supported database")
	}
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Errorf("err = %v, want ErrSchemaTooNew", err)
	}
	// Both numbers belong in the message: the operator needs to know how
	// far ahead the volume is before choosing to roll forward or restore.
	for _, want := range []string{fmt.Sprint(future), fmt.Sprint(latestRevision())} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention revision %s", err.Error(), want)
		}
	}
}
