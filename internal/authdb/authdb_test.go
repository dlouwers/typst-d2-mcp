package authdb

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestUpsertAndIssue_Roundtrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	uid, err := s.UpsertGitHubUser(ctx, 42, "octocat", "octo@github.com")
	if err != nil {
		t.Fatalf("UpsertGitHubUser: %v", err)
	}
	if uid == 0 {
		t.Error("UpsertGitHubUser returned 0 id")
	}

	key, err := s.IssueAPIKey(ctx, uid)
	if err != nil {
		t.Fatalf("IssueAPIKey: %v", err)
	}
	if !strings.HasPrefix(key, "ttd2_") {
		t.Errorf("key missing ttd2_ prefix: %q", key)
	}

	id, err := s.IdentityForKey(ctx, key)
	if err != nil {
		t.Fatalf("IdentityForKey: %v", err)
	}
	if id.UserID != "gh:42" {
		t.Errorf("UserID = %q, want %q", id.UserID, "gh:42")
	}
	if id.GitHubLogin != "octocat" || id.Email != "octo@github.com" {
		t.Errorf("identity fields off: %+v", id)
	}
}

func TestIdentityForKey_Invalid(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	_, err := s.IdentityForKey(ctx, "ttd2_unknown")
	if !errors.Is(err, ErrInvalidKey) {
		t.Errorf("err = %v, want ErrInvalidKey", err)
	}
	_, err = s.IdentityForKey(ctx, "")
	if !errors.Is(err, ErrInvalidKey) {
		t.Errorf("empty key err = %v, want ErrInvalidKey", err)
	}
}

func TestIncrementCompile_UnderAtOver(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	const u, day = "gh:42", "2026-05-19"

	// Two successful increments under a limit of 3.
	for i := 0; i < 2; i++ {
		if err := s.IncrementCompile(ctx, u, day, 3); err != nil {
			t.Fatalf("increment %d under limit: %v", i, err)
		}
	}

	// Third hits the limit exactly — still allowed.
	if err := s.IncrementCompile(ctx, u, day, 3); err != nil {
		t.Fatalf("third increment (at limit boundary): %v", err)
	}

	// Fourth is over the limit.
	err := s.IncrementCompile(ctx, u, day, 3)
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Errorf("over-limit err = %v, want ErrQuotaExceeded", err)
	}

	// Same user, next day — fresh counter.
	if err := s.IncrementCompile(ctx, u, "2026-05-20", 3); err != nil {
		t.Errorf("next-day fresh counter: %v", err)
	}

	// Different user on the same day — independent counter.
	if err := s.IncrementCompile(ctx, "gh:99", day, 1); err != nil {
		t.Errorf("different user same day: %v", err)
	}
}

func TestIncrementCompile_LimitZeroDisabled(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if err := s.IncrementCompile(ctx, "gh:1", "2026-05-19", 0); err != nil {
			t.Fatalf("limit=0 should be a no-op, got %v", err)
		}
	}
}

func TestUpsertRefreshesProfile(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	uid1, _ := s.UpsertGitHubUser(ctx, 7, "first", "first@example.com")
	uid2, err := s.UpsertGitHubUser(ctx, 7, "renamed", "renamed@example.com")
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if uid1 != uid2 {
		t.Errorf("upsert returned different ids %d vs %d for same github_id", uid1, uid2)
	}

	key, _ := s.IssueAPIKey(ctx, uid2)
	id, _ := s.IdentityForKey(ctx, key)
	if id.GitHubLogin != "renamed" || id.Email != "renamed@example.com" {
		t.Errorf("profile fields not refreshed: %+v", id)
	}
}
