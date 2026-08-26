package auth

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeDevStore struct {
	upserts []string
}

func (f *fakeDevStore) UpsertGitHubUser(_ context.Context, _ int64, login, _ string) (int64, error) {
	f.upserts = append(f.upserts, login)
	return 1, nil
}

func identifyAs(t *testing.T, d *Dev, login string) (string, error) {
	t.Helper()
	r := httptest.NewRequest("POST", "/mcp", nil)
	if login != "" {
		r.Header.Set(DevUserHeader, login)
	}
	id, err := d.IdentifyFromRequest(r)
	return id.UserID, err
}

// The header names who you are, and the answer is stable — a dev user's
// workspace, quota and namespace have to persist across restarts or
// nothing about a returning user can be tested.
func TestDev_HeaderSelectsAStableIdentity(t *testing.T) {
	d := &Dev{Store: &fakeDevStore{}}

	first, err := identifyAs(t, d, "octocat")
	if err != nil {
		t.Fatal(err)
	}
	if first == "" {
		t.Fatal("no identity returned")
	}
	again, err := identifyAs(t, d, "OCTOCAT") // case-insensitive, like GitHub
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Errorf("identity is not stable: %q then %q", first, again)
	}

	other, err := identifyAs(t, d, "hubot")
	if err != nil {
		t.Fatal(err)
	}
	if other == first {
		t.Errorf("two logins collapsed to one identity: %q", other)
	}
}

// An unnamed request must not silently become somebody. Anonymous is
// the honest answer.
func TestDev_NoHeaderIsAnonymous(t *testing.T) {
	d := &Dev{Store: &fakeDevStore{}}
	got, err := identifyAs(t, d, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "anonymous" {
		t.Errorf("unnamed request = %q, want anonymous", got)
	}
}

func TestDev_DefaultLoginApplies(t *testing.T) {
	d := &Dev{Store: &fakeDevStore{}, DefaultLogin: "octocat"}
	got, err := identifyAs(t, d, "")
	if err != nil {
		t.Fatal(err)
	}
	if got == "anonymous" || got == "" {
		t.Errorf("default login not applied: %q", got)
	}
}

// The header must not be a way to invent an identity shaped like
// something a real login could never be.
func TestDev_RejectsMalformedLogins(t *testing.T) {
	d := &Dev{Store: &fakeDevStore{}}
	for _, login := range []string{
		"has space", "under_score", "-leading", "trailing-",
		"sym$bol", strings.Repeat("a", 40), "../etc/passwd", "a/b",
	} {
		if _, err := identifyAs(t, d, login); err == nil {
			t.Errorf("login %q was accepted", login)
		}
	}
}

// A dev identity must never collide with a real GitHub account in a
// database that has seen both.
func TestDev_IDsCannotCollideWithRealAccounts(t *testing.T) {
	const highestPlausibleRealID = 1 << 39 // GitHub ids are far below this
	for _, login := range []string{"a", "octocat", "hubot", "some-long-login-name"} {
		if got := devGitHubID(login); got <= highestPlausibleRealID {
			t.Errorf("devGitHubID(%q) = %d, inside the real GitHub id range", login, got)
		}
	}
}

// The store is what makes the identity real; without one, fail rather
// than hand back something that looks authenticated.
func TestDev_WithoutAStoreItFails(t *testing.T) {
	d := &Dev{}
	if _, err := identifyAs(t, d, "octocat"); err == nil {
		t.Error("identified a user with no store")
	}
}

// The guard that matters. Dev auth grants any identity to anyone who
// can reach the port, so exposure is refused rather than warned about.
func TestRequireLoopback(t *testing.T) {
	ok := []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080", "127.0.0.1", "::1"}
	for _, addr := range ok {
		if err := RequireLoopback(addr); err != nil {
			t.Errorf("RequireLoopback(%q) = %v, want nil", addr, err)
		}
	}
	bad := []string{":8080", "0.0.0.0:8080", "[::]:8080", "192.168.1.5:8080", "10.0.0.1:80", "example.com:8080"}
	for _, addr := range bad {
		if err := RequireLoopback(addr); err == nil {
			t.Errorf("RequireLoopback(%q) = nil, want an error — this would expose dev auth", addr)
		}
	}
}

// The startup label has to be unmissable in a log.
func TestDev_NameIsLoud(t *testing.T) {
	name := (&Dev{}).Name()
	if !strings.Contains(name, "NO AUTHENTICATION") {
		t.Errorf("Name() = %q — an operator scanning logs must see what this is", name)
	}
}

func TestDev_AdminAllowlist(t *testing.T) {
	d := &Dev{AdminLogins: map[string]bool{"dlouwers": true}}
	if !d.IsAdmin("DLouwers") {
		t.Error("admin check should be case-insensitive")
	}
	if d.IsAdmin("octocat") {
		t.Error("non-admin treated as admin")
	}
}
