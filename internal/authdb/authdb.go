// Package authdb persists users and their API keys in a small SQLite
// database. It is intentionally tiny: a CGO-free driver
// (modernc.org/sqlite), two tables, and just enough verbs to issue and
// validate keys minted via GitHub OAuth.
//
// Keys are stored as sha256 hashes only; the plaintext is returned to
// the user exactly once at IssueAPIKey time, so a leaked database file
// does not let an attacker authenticate.
package authdb

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/dlouwers/typst-d2-mcp/internal/identity"

	_ "modernc.org/sqlite"
)

// ErrInvalidKey is returned by IdentityForKey when the supplied
// plaintext does not match any stored hash.
var ErrInvalidKey = errors.New("invalid api key")

// ErrQuotaExceeded is returned by IncrementCompile when the caller
// has already reached the configured per-day quota.
var ErrQuotaExceeded = errors.New("quota exceeded")

// keyPrefix tags every plaintext key so support / logging code can
// recognise leaks at a glance and operators can grep for them.
const keyPrefix = "ttd2_"

// Store wraps the SQLite connection and exposes typed operations.
type Store struct {
	db *sql.DB
}

// Open returns a Store backed by the SQLite file at path. The schema is
// created in place if missing; opening an existing file is idempotent.
// Foreign keys are enabled for referential integrity on api_keys.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the underlying SQLite connection.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS users (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  github_id    INTEGER NOT NULL UNIQUE,
  github_login TEXT    NOT NULL,
  email        TEXT,
  created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS api_keys (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  key_hash     BLOB    NOT NULL UNIQUE,
  created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_used_at TIMESTAMP
);
CREATE TABLE IF NOT EXISTS compiles (
  user_id  TEXT    NOT NULL,
  utc_date TEXT    NOT NULL,
  count    INTEGER NOT NULL,
  PRIMARY KEY(user_id, utc_date)
);
`
	_, err := s.db.Exec(ddl)
	if err != nil {
		return fmt.Errorf("migrate schema: %w", err)
	}
	return nil
}

// UpsertGitHubUser inserts or updates a user keyed by their GitHub
// numeric ID and returns the local user.id. Login/email are refreshed
// on each call so the local cache stays close to GitHub's truth.
func (s *Store) UpsertGitHubUser(ctx context.Context, githubID int64, login, email string) (int64, error) {
	const stmt = `
INSERT INTO users (github_id, github_login, email)
VALUES (?, ?, ?)
ON CONFLICT(github_id) DO UPDATE SET
  github_login = excluded.github_login,
  email        = excluded.email
RETURNING id;
`
	var id int64
	if err := s.db.QueryRowContext(ctx, stmt, githubID, login, email).Scan(&id); err != nil {
		return 0, fmt.Errorf("upsert user: %w", err)
	}
	return id, nil
}

// IssueAPIKey mints a fresh opaque key for userID, stores only its
// sha256 hash, and returns the plaintext. The plaintext is visible to
// the caller exactly once and must be surfaced to the user immediately.
func (s *Store) IssueAPIKey(ctx context.Context, userID int64) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	plaintext := keyPrefix + base64.RawURLEncoding.EncodeToString(raw[:])
	hash := sha256.Sum256([]byte(plaintext))
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO api_keys(user_id, key_hash) VALUES(?, ?)`,
		userID, hash[:],
	); err != nil {
		return "", fmt.Errorf("insert api key: %w", err)
	}
	return plaintext, nil
}

// IdentityForKey returns the identity bound to plaintext, or
// ErrInvalidKey if no such key exists. The last_used_at column is
// updated on hit; the update is best-effort and does not affect the
// return value.
func (s *Store) IdentityForKey(ctx context.Context, plaintext string) (identity.Identity, error) {
	if plaintext == "" {
		return identity.Identity{}, ErrInvalidKey
	}
	hash := sha256.Sum256([]byte(plaintext))
	var (
		userID      int64
		githubID    int64
		githubLogin string
		email       sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
SELECT u.id, u.github_id, u.github_login, u.email
FROM api_keys k
JOIN users    u ON u.id = k.user_id
WHERE k.key_hash = ?
`, hash[:]).Scan(&userID, &githubID, &githubLogin, &email)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.Identity{}, ErrInvalidKey
	}
	if err != nil {
		return identity.Identity{}, fmt.Errorf("lookup api key: %w", err)
	}
	_, _ = s.db.ExecContext(ctx,
		`UPDATE api_keys SET last_used_at = CURRENT_TIMESTAMP WHERE key_hash = ?`,
		hash[:],
	)
	_ = userID
	return identity.Identity{
		UserID:      fmt.Sprintf("gh:%d", githubID),
		GitHubLogin: githubLogin,
		Email:       email.String,
	}, nil
}

// IncrementCompile atomically reads the user's compile count for
// utcDate and increments it. It returns ErrQuotaExceeded if the count
// already equals or exceeds limit before the increment; in that case
// no row is changed. A limit <= 0 is treated as "no quota" and the
// call returns nil without touching the database.
//
// utcDate is the caller-supplied YYYY-MM-DD string for the day the
// compile is attributed to. Passing it explicitly (rather than
// computing it inside the store) lets tests cover day-rollover
// without waiting for midnight.
func (s *Store) IncrementCompile(ctx context.Context, userID, utcDate string, limit int) error {
	if limit <= 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var count int
	err = tx.QueryRowContext(ctx,
		`SELECT count FROM compiles WHERE user_id = ? AND utc_date = ?`,
		userID, utcDate,
	).Scan(&count)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read compile count: %w", err)
	}
	if count >= limit {
		return ErrQuotaExceeded
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO compiles(user_id, utc_date, count) VALUES(?, ?, 1)
ON CONFLICT(user_id, utc_date) DO UPDATE SET count = count + 1
`, userID, utcDate); err != nil {
		return fmt.Errorf("upsert compile count: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}
