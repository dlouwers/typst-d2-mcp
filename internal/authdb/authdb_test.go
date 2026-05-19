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
