package authdb

import (
	"errors"
	"testing"
)

func TestOrg_CreateListDelete(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	if err := s.CreateOrg(ctx, "dlouwers", "acme", "Acme Corp"); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	orgs, err := s.ListOrgs(ctx)
	if err != nil {
		t.Fatalf("ListOrgs: %v", err)
	}
	if len(orgs) != 1 || orgs[0].Slug != "acme" || orgs[0].DisplayName != "Acme Corp" || orgs[0].MemberCount != 0 {
		t.Fatalf("unexpected orgs: %+v", orgs)
	}

	if err := s.DeleteOrg(ctx, "dlouwers", "acme"); err != nil {
		t.Fatalf("DeleteOrg: %v", err)
	}
	if orgs, _ := s.ListOrgs(ctx); len(orgs) != 0 {
		t.Errorf("org survived deletion: %+v", orgs)
	}
	if err := s.DeleteOrg(ctx, "dlouwers", "ghost"); !errors.Is(err, ErrNoSuchNamespace) {
		t.Errorf("DeleteOrg(unknown) = %v, want ErrNoSuchNamespace", err)
	}
}

func TestOrg_CreateRejectsBadSlug(t *testing.T) {
	s := newStore(t)
	for _, slug := range []string{"", "a", "Acme", "ac me", "-acme", "acme-", "acme_co", "house", "preview"} {
		if err := s.CreateOrg(t.Context(), "dlouwers", slug, ""); !errors.Is(err, ErrInvalidName) {
			t.Errorf("CreateOrg(%q) = %v, want ErrInvalidName", slug, err)
		}
	}
}

func TestOrg_CreateDuplicateSlug(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	if err := s.CreateOrg(ctx, "dlouwers", "acme", ""); err != nil {
		t.Fatalf("first CreateOrg: %v", err)
	}
	if err := s.CreateOrg(ctx, "dlouwers", "acme", "Other"); !errors.Is(err, ErrNameTaken) {
		t.Errorf("duplicate CreateOrg = %v, want ErrNameTaken", err)
	}
}

func TestOrg_Membership(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	alice := seedUser(t, s, 1, "alice")
	seedUser(t, s, 2, "bob")
	if err := s.CreateOrg(ctx, "dlouwers", "acme", ""); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	if err := s.AddOrgMember(ctx, "dlouwers", "acme", "alice"); err != nil {
		t.Fatalf("AddOrgMember alice: %v", err)
	}
	if err := s.AddOrgMember(ctx, "dlouwers", "acme", "bob"); err != nil {
		t.Fatalf("AddOrgMember bob: %v", err)
	}

	// Idempotency guard: adding the same member again is a clear signal,
	// not a silent success.
	if err := s.AddOrgMember(ctx, "dlouwers", "acme", "alice"); !errors.Is(err, ErrAlreadyMember) {
		t.Errorf("re-add = %v, want ErrAlreadyMember", err)
	}

	members, err := s.ListOrgMembers(ctx, "acme")
	if err != nil {
		t.Fatalf("ListOrgMembers: %v", err)
	}
	if len(members) != 2 || members[0].GitHubLogin != "alice" || members[1].GitHubLogin != "bob" {
		t.Fatalf("unexpected members: %+v", members)
	}

	// Alice resolves acme AND her own personal namespace, which exists
	// from her first sign-in.
	if ns, _ := s.NamespacesForUser(ctx, alice); ns["acme"] == "" {
		t.Errorf("NamespacesForUser(alice) = %v, want it to contain acme", ns)
	}
	if orgs, _ := s.ListOrgs(ctx); orgs[0].MemberCount != 2 {
		t.Errorf("MemberCount = %d, want 2", orgs[0].MemberCount)
	}

	if err := s.RemoveOrgMember(ctx, "dlouwers", "acme", "alice"); err != nil {
		t.Fatalf("RemoveOrgMember: %v", err)
	}
	if ns, _ := s.NamespacesForUser(ctx, alice); ns["acme"] != "" {
		t.Errorf("alice still resolves acme after removal: %v", ns)
	}

	// Deleting the org cascades to the remaining membership row.
	if err := s.DeleteOrg(ctx, "dlouwers", "acme"); err != nil {
		t.Fatalf("DeleteOrg: %v", err)
	}
	if m, _ := s.ListOrgMembers(ctx, "acme"); len(m) != 0 {
		t.Errorf("membership survived org deletion: %+v", m)
	}
}

func TestOrg_MembershipErrors(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	if err := s.CreateOrg(ctx, "dlouwers", "acme", ""); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	// Unknown user (never signed in).
	if err := s.AddOrgMember(ctx, "dlouwers", "acme", "ghost"); !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("AddOrgMember(unknown user) = %v, want ErrNoSuchUser", err)
	}
	// Unknown org.
	seedUser(t, s, 1, "alice")
	if err := s.AddOrgMember(ctx, "dlouwers", "nope", "alice"); !errors.Is(err, ErrNoSuchNamespace) {
		t.Errorf("AddOrgMember(unknown org) = %v, want ErrNoSuchNamespace", err)
	}
}
