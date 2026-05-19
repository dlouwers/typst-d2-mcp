package authdb

// OAuth-AS storage. Three small tables that together turn the API-key
// machinery in authdb.go into a working RFC 6749 + RFC 7591 + RFC 7636
// Authorization Server:
//
//   - oauth_clients              — dynamic (RFC 7591) registration of
//                                  MCP clients. Public clients only at
//                                  v1; no client_secret column.
//   - oauth_authorize_sessions   — short-lived state for the round-trip
//                                  through GitHub. The session_id is
//                                  also our `state` param sent to
//                                  GitHub, so the callback can find its
//                                  way back to the right MCP client.
//   - oauth_authorization_codes  — one-shot codes returned from the
//                                  callback to the MCP client, redeemed
//                                  at /token. PKCE challenge stored
//                                  here so the verifier can be checked
//                                  at redemption time.
//
// Expiry is enforced by checking expires_at in each read; SQLite has
// no native TTL.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Authorize-session and authorization-code lifetimes. Both are tight by
// design — an OAuth dance that takes longer than these is almost
// certainly an attacker or a hung browser, not a legitimate user.
const (
	AuthorizeSessionTTL = 10 * time.Minute
	AuthorizationCodeTTL = 60 * time.Second
)

// Errors callers should be able to distinguish in tests and at the
// handler boundary.
var (
	ErrUnknownClient        = errors.New("unknown oauth client")
	ErrSessionNotFound      = errors.New("authorize session not found or expired")
	ErrAuthorizationCode    = errors.New("authorization code not found, expired, or already used")
	ErrPKCEMismatch         = errors.New("pkce verifier does not match challenge")
	ErrPDFLinkNotFound      = errors.New("pdf download link not found or expired")
)

// OAuthClient is the public-facing shape of a registered MCP client.
type OAuthClient struct {
	ClientID                string
	ClientName              string
	RedirectURIs            []string
	TokenEndpointAuthMethod string
	CreatedAt               time.Time
}

// AuthorizeSession is the server-side state held between the MCP
// client's /authorize request and GitHub's callback. The SessionID
// doubles as our `state` param sent to GitHub.
type AuthorizeSession struct {
	SessionID           string
	ClientID            string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	Scope               string
	ClientState         string
	ExpiresAt           time.Time
}

// AuthorizationCode is the one-shot code redeemed at /token.
type AuthorizationCode struct {
	Code                string
	UserDBID            int64
	ClientID            string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	Scope               string
}

func (s *Store) migrateOAuth() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS oauth_clients (
  client_id                  TEXT PRIMARY KEY,
  client_name                TEXT NOT NULL,
  redirect_uris              TEXT NOT NULL, -- JSON-encoded array
  token_endpoint_auth_method TEXT NOT NULL DEFAULT 'none',
  created_at                 TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS oauth_authorize_sessions (
  session_id            TEXT PRIMARY KEY,
  client_id             TEXT NOT NULL REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
  redirect_uri          TEXT NOT NULL,
  code_challenge        TEXT NOT NULL,
  code_challenge_method TEXT NOT NULL DEFAULT 'S256',
  scope                 TEXT,
  client_state          TEXT NOT NULL,
  expires_at            TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS oauth_authorization_codes (
  code                  TEXT PRIMARY KEY,
  user_id               INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  client_id             TEXT NOT NULL REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
  redirect_uri          TEXT NOT NULL,
  code_challenge        TEXT NOT NULL,
  code_challenge_method TEXT NOT NULL DEFAULT 'S256',
  scope                 TEXT,
  used                  INTEGER NOT NULL DEFAULT 0,
  expires_at            TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS pdf_links (
  token       TEXT PRIMARY KEY,
  user_id     TEXT NOT NULL,         -- identity.Identity.UserID (e.g. "gh:42")
  file_path   TEXT NOT NULL,         -- workspace-relative path to the PDF
  created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  expires_at  TIMESTAMP NOT NULL
);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate oauth schema: %w", err)
	}
	return nil
}

// RegisterClient persists a dynamic client registration and returns
// the assigned client_id. Caller is responsible for validating the
// redirect URI shapes (https + matching host policy) before calling.
func (s *Store) RegisterClient(ctx context.Context, name string, redirectURIs []string, authMethod string) (OAuthClient, error) {
	if name == "" || len(redirectURIs) == 0 {
		return OAuthClient{}, fmt.Errorf("client_name and redirect_uris are required")
	}
	if authMethod == "" {
		authMethod = "none"
	}
	clientID, err := randomToken("ttd2cli_")
	if err != nil {
		return OAuthClient{}, err
	}
	urisJSON, err := json.Marshal(redirectURIs)
	if err != nil {
		return OAuthClient{}, fmt.Errorf("marshal redirect_uris: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO oauth_clients(client_id, client_name, redirect_uris, token_endpoint_auth_method)
VALUES(?, ?, ?, ?)
`, clientID, name, string(urisJSON), authMethod); err != nil {
		return OAuthClient{}, fmt.Errorf("insert oauth client: %w", err)
	}
	return OAuthClient{
		ClientID:                clientID,
		ClientName:              name,
		RedirectURIs:            redirectURIs,
		TokenEndpointAuthMethod: authMethod,
		CreatedAt:               time.Now().UTC(),
	}, nil
}

// LookupClient returns the registered client or ErrUnknownClient.
func (s *Store) LookupClient(ctx context.Context, clientID string) (OAuthClient, error) {
	var (
		name, urisJSON, authMethod string
		createdAt                  time.Time
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT client_name, redirect_uris, token_endpoint_auth_method, created_at
		   FROM oauth_clients WHERE client_id = ?`, clientID,
	).Scan(&name, &urisJSON, &authMethod, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthClient{}, ErrUnknownClient
	}
	if err != nil {
		return OAuthClient{}, fmt.Errorf("lookup oauth client: %w", err)
	}
	var uris []string
	if err := json.Unmarshal([]byte(urisJSON), &uris); err != nil {
		return OAuthClient{}, fmt.Errorf("decode redirect_uris: %w", err)
	}
	return OAuthClient{
		ClientID:                clientID,
		ClientName:              name,
		RedirectURIs:            uris,
		TokenEndpointAuthMethod: authMethod,
		CreatedAt:               createdAt,
	}, nil
}

// CreateAuthorizeSession persists an in-flight /authorize request and
// returns the session_id (which is also used as the GitHub `state`).
// Caller validates the request shape; this method only stores.
func (s *Store) CreateAuthorizeSession(ctx context.Context, in AuthorizeSession) (string, error) {
	sid, err := randomToken("ttd2sess_")
	if err != nil {
		return "", err
	}
	if in.CodeChallengeMethod == "" {
		in.CodeChallengeMethod = "S256"
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO oauth_authorize_sessions(
  session_id, client_id, redirect_uri,
  code_challenge, code_challenge_method, scope, client_state, expires_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?)
`,
		sid, in.ClientID, in.RedirectURI,
		in.CodeChallenge, in.CodeChallengeMethod, in.Scope, in.ClientState,
		time.Now().UTC().Add(AuthorizeSessionTTL),
	); err != nil {
		return "", fmt.Errorf("insert authorize session: %w", err)
	}
	return sid, nil
}

// ConsumeAuthorizeSession reads and deletes the session in one txn —
// the GitHub callback can fire exactly once per /authorize, and we
// don't want a duplicate-replay to re-enter the flow.
func (s *Store) ConsumeAuthorizeSession(ctx context.Context, sessionID string) (AuthorizeSession, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AuthorizeSession{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var out AuthorizeSession
	err = tx.QueryRowContext(ctx, `
SELECT session_id, client_id, redirect_uri, code_challenge, code_challenge_method,
       COALESCE(scope, ''), client_state, expires_at
  FROM oauth_authorize_sessions WHERE session_id = ?
`, sessionID).Scan(
		&out.SessionID, &out.ClientID, &out.RedirectURI,
		&out.CodeChallenge, &out.CodeChallengeMethod, &out.Scope,
		&out.ClientState, &out.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthorizeSession{}, ErrSessionNotFound
	}
	if err != nil {
		return AuthorizeSession{}, fmt.Errorf("read authorize session: %w", err)
	}
	if time.Now().UTC().After(out.ExpiresAt) {
		_, _ = tx.ExecContext(ctx, `DELETE FROM oauth_authorize_sessions WHERE session_id = ?`, sessionID)
		_ = tx.Commit()
		return AuthorizeSession{}, ErrSessionNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_authorize_sessions WHERE session_id = ?`, sessionID); err != nil {
		return AuthorizeSession{}, fmt.Errorf("delete authorize session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return AuthorizeSession{}, fmt.Errorf("commit: %w", err)
	}
	return out, nil
}

// MintAuthorizationCode issues a one-shot code bound to the user +
// client + PKCE challenge from a completed authorize session.
func (s *Store) MintAuthorizationCode(ctx context.Context, in AuthorizationCode) (string, error) {
	code, err := randomToken("ttd2code_")
	if err != nil {
		return "", err
	}
	if in.CodeChallengeMethod == "" {
		in.CodeChallengeMethod = "S256"
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO oauth_authorization_codes(
  code, user_id, client_id, redirect_uri,
  code_challenge, code_challenge_method, scope, expires_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?)
`,
		code, in.UserDBID, in.ClientID, in.RedirectURI,
		in.CodeChallenge, in.CodeChallengeMethod, in.Scope,
		time.Now().UTC().Add(AuthorizationCodeTTL),
	); err != nil {
		return "", fmt.Errorf("insert authorization code: %w", err)
	}
	return code, nil
}

// ConsumeAuthorizationCode atomically marks the code as used and
// returns its bound data. Replay of the same code returns
// ErrAuthorizationCode — and per RFC 6749 §10.5 the server SHOULD
// invalidate any tokens already issued from that code; we'd handle
// that in a follow-up since v1 issues exactly one token per code.
func (s *Store) ConsumeAuthorizationCode(ctx context.Context, code string) (AuthorizationCode, int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AuthorizationCode{}, 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		out       AuthorizationCode
		used      int
		expiresAt time.Time
	)
	err = tx.QueryRowContext(ctx, `
SELECT code, user_id, client_id, redirect_uri, code_challenge, code_challenge_method,
       COALESCE(scope, ''), used, expires_at
  FROM oauth_authorization_codes WHERE code = ?
`, code).Scan(
		&out.Code, &out.UserDBID, &out.ClientID, &out.RedirectURI,
		&out.CodeChallenge, &out.CodeChallengeMethod, &out.Scope,
		&used, &expiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthorizationCode{}, 0, ErrAuthorizationCode
	}
	if err != nil {
		return AuthorizationCode{}, 0, fmt.Errorf("read authorization code: %w", err)
	}
	if used != 0 || time.Now().UTC().After(expiresAt) {
		return AuthorizationCode{}, 0, ErrAuthorizationCode
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE oauth_authorization_codes SET used = 1 WHERE code = ? AND used = 0`, code)
	if err != nil {
		return AuthorizationCode{}, 0, fmt.Errorf("mark code used: %w", err)
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return AuthorizationCode{}, 0, ErrAuthorizationCode
	}
	if err := tx.Commit(); err != nil {
		return AuthorizationCode{}, 0, fmt.Errorf("commit: %w", err)
	}
	return out, out.UserDBID, nil
}

// randomToken returns prefix + 32 bytes of URL-safe random.
func randomToken(prefix string) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}
