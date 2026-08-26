package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestOrgs_CreateAddRemoveDelete(t *testing.T) {
	f := newFixture(t)
	f.seedSignedIn(t, 42, "octocat")
	ctx := t.Context()

	// Create.
	f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/orgs/create",
		url.Values{"slug": {"acme"}, "display_name": {"Acme Corp"}}))
	orgs, _ := f.store.ListOrgs(ctx)
	if len(orgs) != 1 || orgs[0].Slug != "acme" {
		t.Fatalf("org not created: %+v", orgs)
	}

	// Add a member.
	f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/orgs/members/add",
		url.Values{"slug": {"acme"}, "login": {"octocat"}}))
	if got, _ := f.store.OrgsForUser(ctx, "gh:42"); len(got) != 1 || got[0] != "acme" {
		t.Fatalf("member not added: %v", got)
	}

	// The orgs page shows the org and its member.
	rec := f.do(t, f.as(t, "dlouwers", http.MethodGet, "/admin/orgs", nil))
	body := rec.Body.String()
	for _, want := range []string{"@acme", "Acme Corp", "octocat"} {
		if !strings.Contains(body, want) {
			t.Errorf("orgs page missing %q", want)
		}
	}

	// Remove the member.
	f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/orgs/members/remove",
		url.Values{"slug": {"acme"}, "login": {"octocat"}}))
	if got, _ := f.store.OrgsForUser(ctx, "gh:42"); len(got) != 0 {
		t.Errorf("member not removed: %v", got)
	}

	// Delete the org.
	f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/orgs/delete",
		url.Values{"slug": {"acme"}}))
	if orgs, _ := f.store.ListOrgs(ctx); len(orgs) != 0 {
		t.Errorf("org not deleted: %+v", orgs)
	}
}

func TestOrgs_CreateRejectsBadSlug(t *testing.T) {
	f := newFixture(t)
	for _, slug := range []string{"", "Acme", "ac me", "house"} {
		rec := f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/orgs/create",
			url.Values{"slug": {slug}}))
		loc, _ := url.Parse(rec.Header().Get("Location"))
		if loc.Query().Get("level") != string(flashError) {
			t.Errorf("slug %q accepted, want a validation error", slug)
		}
	}
	if orgs, _ := f.store.ListOrgs(t.Context()); len(orgs) != 0 {
		t.Errorf("a bad slug created an org: %+v", orgs)
	}
}

func TestOrgs_AddMemberWhoHasNotSignedIn(t *testing.T) {
	f := newFixture(t)
	if err := f.store.CreateOrg(t.Context(), "dlouwers", "acme", ""); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	rec := f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/orgs/members/add",
		url.Values{"slug": {"acme"}, "login": {"newbie"}}))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if !strings.Contains(loc.Query().Get("flash"), "has not signed in yet") {
		t.Errorf("flash = %q, want an explanation", loc.Query().Get("flash"))
	}
}

func TestOrgs_NonAdminForbidden(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{
		"/admin/orgs/create", "/admin/orgs/delete",
		"/admin/orgs/members/add", "/admin/orgs/members/remove",
	} {
		rec := f.do(t, f.as(t, "randomuser", http.MethodPost, path,
			url.Values{"slug": {"acme"}, "login": {"x"}}))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", path, rec.Code)
		}
	}
	rec := f.do(t, f.as(t, "randomuser", http.MethodGet, "/admin/orgs", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("GET /admin/orgs: status = %d, want 403", rec.Code)
	}
}
