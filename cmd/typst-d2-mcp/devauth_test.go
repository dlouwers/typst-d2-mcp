package main

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/dlouwers/typst-d2-mcp/internal/auth"
	"github.com/dlouwers/typst-d2-mcp/internal/authdb"
)

// The whole reason dev auth exists: a caller who never authenticates
// still gets a REAL identity, which means a real personal namespace to
// publish into. An identity that skipped that would exercise a code
// path nothing in production uses.
func TestDevAuth_GivesARealIdentityWithANamespace(t *testing.T) {
	store, err := authdb.Open(filepath.Join(t.TempDir(), "auth.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	dev := &auth.Dev{Store: store}
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set(auth.DevUserHeader, "octocat")

	id, err := dev.IdentifyFromRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	if id.IsAnonymous() {
		t.Fatal("dev auth produced an anonymous identity")
	}

	ns, err := store.NamespacesForUser(context.Background(), id.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) == 0 {
		t.Fatal("no personal namespace for a dev identity — publishing would be impossible")
	}

	// And they own it, so they can publish without anything granted.
	for _, nsID := range ns {
		role, err := store.RoleFor(context.Background(), nsID, id.UserID)
		if err != nil {
			t.Fatal(err)
		}
		if role != authdb.RoleOwner {
			t.Errorf("role = %q, want %q", role, authdb.RoleOwner)
		}
	}
}

// Two dev users are two tenants — separate namespaces, separate
// workspaces. Without this, every multi-user scenario would be
// untestable.
func TestDevAuth_UsersAreDistinctTenants(t *testing.T) {
	store, err := authdb.Open(filepath.Join(t.TempDir(), "auth.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	dev := &auth.Dev{Store: store}

	ids := map[string]string{}
	for _, login := range []string{"alice", "bob"} {
		r := httptest.NewRequest("POST", "/mcp", nil)
		r.Header.Set(auth.DevUserHeader, login)
		id, err := dev.IdentifyFromRequest(r)
		if err != nil {
			t.Fatal(err)
		}
		ids[login] = id.UserID
	}
	if ids["alice"] == ids["bob"] {
		t.Fatal("two dev users share one identity")
	}

	alice, _ := store.NamespacesForUser(context.Background(), ids["alice"])
	bob, _ := store.NamespacesForUser(context.Background(), ids["bob"])
	for name := range alice {
		if _, shared := bob[name]; shared {
			t.Errorf("namespace %q is visible to both tenants", name)
		}
	}
}

// selectAuth must not produce a dev backend by accident — it is opt-in
// by an exact value, never a fallback.
func TestSelectAuth_DevIsOptIn(t *testing.T) {
	for _, mode := range []string{"", "none"} {
		t.Setenv(envAuth, mode)
		backend, _, _, closer, err := selectAuth()
		if err != nil {
			t.Fatalf("selectAuth(%q): %v", mode, err)
		}
		if closer != nil {
			closer()
		}
		if _, isDev := backend.(*auth.Dev); isDev {
			t.Errorf("AUTH=%q produced the dev backend", mode)
		}
	}

	t.Setenv(envAuth, "dev")
	t.Setenv(envDB, filepath.Join(t.TempDir(), "auth.sqlite"))
	backend, _, store, closer, err := selectAuth()
	if err != nil {
		t.Fatalf("AUTH=dev: %v", err)
	}
	if closer != nil {
		defer closer()
	}
	if _, isDev := backend.(*auth.Dev); !isDev {
		t.Errorf("AUTH=dev produced %T", backend)
	}
	if store == nil {
		t.Error("dev auth without a store cannot provision namespaces")
	}
}

// Without a database there are no real identities to select, so this
// must fail at startup rather than degrade to anonymous.
func TestSelectAuth_DevRequiresADatabase(t *testing.T) {
	t.Setenv(envAuth, "dev")
	t.Setenv(envDB, "")
	if _, _, _, closer, err := selectAuth(); err == nil {
		if closer != nil {
			closer()
		}
		t.Error("AUTH=dev started without a database")
	}
}
