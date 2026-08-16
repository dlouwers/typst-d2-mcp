package main

import (
	"path/filepath"
	"testing"

	"github.com/dlouwers/typst-d2-mcp/internal/auth"
)

// selectAuth is where environment configuration becomes a live backend.
// Tests elsewhere construct auth.GitHub directly and so cannot catch a
// field that is simply never populated here — which is exactly the bug
// this covers.
func TestSelectAuth_GitHubReadsEnvironment(t *testing.T) {
	t.Setenv(envAuth, "github")
	t.Setenv(envDB, filepath.Join(t.TempDir(), "auth.sqlite"))
	t.Setenv(envPublicURL, "https://mcp.example.test")
	t.Setenv(envGitHubID, "cid")
	t.Setenv(envGitHubSecret, "secret")
	t.Setenv(envGitHubAllowlist, "alloweduser")
	t.Setenv(envAdmins, "Dlouwers, seconddmin")

	backend, handlers, store, closer, err := selectAuth()
	if err != nil {
		t.Fatalf("selectAuth: %v", err)
	}
	if closer != nil {
		defer closer()
	}
	if store == nil {
		t.Fatal("store is nil")
	}
	if handlers == nil || handlers.backend == nil {
		t.Fatal("github handlers or backend missing")
	}

	gh, ok := backend.(*auth.GitHub)
	if !ok {
		t.Fatalf("backend is %T, want *auth.GitHub", backend)
	}
	if !gh.Cfg.IsAdmin("dlouwers") {
		t.Error("TYPST_D2_MCP_ADMINS was not applied to the backend")
	}
	if !gh.Cfg.IsAdmin("seconddmin") {
		t.Error("second admin from the comma-separated list was dropped")
	}
	if gh.Cfg.IsAdmin("alloweduser") {
		t.Error("an allowlisted (non-admin) login was treated as an admin")
	}
	if !gh.Cfg.AllowedLogins["alloweduser"] {
		t.Error("TYPST_D2_MCP_GITHUB_ALLOWLIST was not applied")
	}
	// Naming admins closes the server; this is the posture flip an
	// operator needs to be able to rely on.
	if !gh.InviteOnly() {
		t.Error("configuring admins did not make the deployment invite-only")
	}
}

// Without admins or an allowlist the deployment keeps the open posture
// it had before invites existed.
func TestSelectAuth_NoAdminsStaysOpen(t *testing.T) {
	t.Setenv(envAuth, "github")
	t.Setenv(envDB, filepath.Join(t.TempDir(), "auth.sqlite"))
	t.Setenv(envPublicURL, "https://mcp.example.test")
	t.Setenv(envGitHubID, "cid")
	t.Setenv(envGitHubSecret, "secret")
	t.Setenv(envGitHubAllowlist, "")
	t.Setenv(envAdmins, "")

	backend, _, _, closer, err := selectAuth()
	if err != nil {
		t.Fatalf("selectAuth: %v", err)
	}
	if closer != nil {
		defer closer()
	}
	if backend.(*auth.GitHub).InviteOnly() {
		t.Error("deployment is invite-only with neither admins nor an allowlist set")
	}
}
