package authdb

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPDFLink_MintAndLookup(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	tok, err := s.MintPDFLink(ctx, "gh:42", "doc.pdf", time.Hour)
	if err != nil {
		t.Fatalf("MintPDFLink: %v", err)
	}
	if tok == "" || len(tok) < 32 {
		t.Errorf("token looks too short: %q", tok)
	}

	link, err := s.LookupPDFLink(ctx, tok)
	if err != nil {
		t.Fatalf("LookupPDFLink: %v", err)
	}
	if link.UserID != "gh:42" || link.FilePath != "doc.pdf" {
		t.Errorf("link fields off: %+v", link)
	}
	if time.Until(link.ExpiresAt) < 30*time.Minute {
		t.Errorf("expires_at too close: %v", link.ExpiresAt)
	}
}

func TestPDFLink_Lookup_Unknown(t *testing.T) {
	s := newStore(t)
	_, err := s.LookupPDFLink(context.Background(), "no-such-token")
	if !errors.Is(err, ErrPDFLinkNotFound) {
		t.Errorf("err = %v, want ErrPDFLinkNotFound", err)
	}
	_, err = s.LookupPDFLink(context.Background(), "")
	if !errors.Is(err, ErrPDFLinkNotFound) {
		t.Errorf("empty token err = %v, want ErrPDFLinkNotFound", err)
	}
}

func TestPDFLink_Expired(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// Mint with a positive TTL, then backdate the row past now via a
	// direct UPDATE so we don't need to sleep.
	tok, err := s.MintPDFLink(ctx, "gh:99", "old.pdf", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE pdf_links SET expires_at = ? WHERE token = ?`,
		time.Now().UTC().Add(-time.Minute), tok); err != nil {
		t.Fatal(err)
	}
	_, err = s.LookupPDFLink(ctx, tok)
	if !errors.Is(err, ErrPDFLinkNotFound) {
		t.Errorf("expired link err = %v, want ErrPDFLinkNotFound", err)
	}
	// Expired row should be pruned on access.
	var n int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pdf_links WHERE token = ?`, tok).Scan(&n)
	if n != 0 {
		t.Errorf("expired row not pruned (count=%d)", n)
	}
}

func TestPDFLink_BadInputs(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.MintPDFLink(ctx, "", "doc.pdf", time.Hour); err == nil {
		t.Errorf("empty userID should fail")
	}
	if _, err := s.MintPDFLink(ctx, "u", "", time.Hour); err == nil {
		t.Errorf("empty filePath should fail")
	}
	if _, err := s.MintPDFLink(ctx, "u", "doc.pdf", 0); err == nil {
		t.Errorf("zero ttl should fail")
	}
}
