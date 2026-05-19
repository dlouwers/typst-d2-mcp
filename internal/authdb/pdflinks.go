package authdb

// PDF capability-URL store. A token is the only credential needed to
// fetch the linked PDF — the random ID *is* the capability, so we don't
// HMAC-sign anything. Tokens have a short TTL (operator-configurable;
// default 1h at the caller boundary) and are scoped to one specific
// user + file_path at mint time. The HTTP /d/{token} handler reads the
// row, resolves the file through the active workspace for that user,
// and streams the bytes. Anyone with the URL within the TTL can
// download — exactly the property we need for clients that can't
// follow MCP resource_link blocks but do render HTTPS links in chat.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

// PDFLink is the resolved view of a row in pdf_links.
type PDFLink struct {
	Token     string
	UserID    string
	FilePath  string
	ExpiresAt time.Time
}

// MintPDFLink generates a fresh 32-byte URL-safe token and binds it
// to (userID, filePath) for ttl. Returns the token; the caller
// composes the public URL by joining it onto PublicURL+"/d/".
func (s *Store) MintPDFLink(ctx context.Context, userID, filePath string, ttl time.Duration) (string, error) {
	if userID == "" || filePath == "" {
		return "", fmt.Errorf("userID and filePath are required")
	}
	if ttl <= 0 {
		return "", fmt.Errorf("ttl must be positive")
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO pdf_links(token, user_id, file_path, expires_at)
VALUES(?, ?, ?, ?)
`, token, userID, filePath, time.Now().UTC().Add(ttl)); err != nil {
		return "", fmt.Errorf("insert pdf link: %w", err)
	}
	return token, nil
}

// LookupPDFLink resolves a token to its bound (user_id, file_path).
// Expired rows are pruned on access and reported as ErrPDFLinkNotFound,
// which is also returned for unknown tokens — callers shouldn't be
// able to distinguish "wrong token" from "valid but expired" to keep
// the surface minimal for probing attackers.
func (s *Store) LookupPDFLink(ctx context.Context, token string) (PDFLink, error) {
	if token == "" {
		return PDFLink{}, ErrPDFLinkNotFound
	}
	var (
		link    PDFLink
		expires time.Time
	)
	err := s.db.QueryRowContext(ctx, `
SELECT token, user_id, file_path, expires_at
  FROM pdf_links WHERE token = ?
`, token).Scan(&link.Token, &link.UserID, &link.FilePath, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return PDFLink{}, ErrPDFLinkNotFound
	}
	if err != nil {
		return PDFLink{}, fmt.Errorf("lookup pdf link: %w", err)
	}
	if time.Now().UTC().After(expires) {
		// Best-effort cleanup; ignore error.
		_, _ = s.db.ExecContext(ctx, `DELETE FROM pdf_links WHERE token = ?`, token)
		return PDFLink{}, ErrPDFLinkNotFound
	}
	link.ExpiresAt = expires
	return link, nil
}
