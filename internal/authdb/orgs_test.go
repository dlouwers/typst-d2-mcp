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

	if err := s.AddOrgMember(ctx, "dlouwers", "acme", "alice", RoleMember); err != nil {
		t.Fatalf("AddOrgMember alice: %v", err)
	}
	if err := s.AddOrgMember(ctx, "dlouwers", "acme", "bob", RoleMember); err != nil {
		t.Fatalf("AddOrgMember bob: %v", err)
	}

	// Idempotency guard: adding the same member again is a clear signal,
	// not a silent success.
	if err := s.AddOrgMember(ctx, "dlouwers", "acme", "alice", RoleMember); !errors.Is(err, ErrAlreadyMember) {
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
	if err := s.AddOrgMember(ctx, "dlouwers", "acme", "ghost", RoleMember); !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("AddOrgMember(unknown user) = %v, want ErrNoSuchUser", err)
	}
	// Unknown org.
	seedUser(t, s, 1, "alice")
	if err := s.AddOrgMember(ctx, "dlouwers", "nope", "alice", RoleMember); !errors.Is(err, ErrNoSuchNamespace) {
		t.Errorf("AddOrgMember(unknown org) = %v, want ErrNoSuchNamespace", err)
	}
}

// Ownership is what grants publishing. Without a way to assign it, an
// organisation namespace could be created, filled with members, and
// remain permanently unpublishable by everyone — which is the state
// every org namespace was in.
func TestOrg_OwnershipCanBeAssigned(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	if err := s.CreateOrg(ctx, "dlouwers", "acme", "Acme"); err != nil {
		t.Fatal(err)
	}
	alice := seedUser(t, s, 1, "alice")

	if err := s.AddOrgMember(ctx, "dlouwers", "acme", "alice", RoleMember); err != nil {
		t.Fatal(err)
	}
	id, err := s.ResolveName(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if role, _ := s.RoleFor(ctx, id, alice); role != RoleMember {
		t.Fatalf("role = %q, want member", role)
	}

	if err := s.SetOrgMemberRole(ctx, "dlouwers", "acme", "alice", RoleOwner); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if role, _ := s.RoleFor(ctx, id, alice); role != RoleOwner {
		t.Errorf("role after promotion = %q, want owner", role)
	}
}

// A member may be added straight as an owner, so a new organisation is
// not born unpublishable.
func TestOrg_MemberCanBeAddedAsOwner(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	if err := s.CreateOrg(ctx, "dlouwers", "acme", ""); err != nil {
		t.Fatal(err)
	}
	alice := seedUser(t, s, 1, "alice")
	if err := s.AddOrgMember(ctx, "dlouwers", "acme", "alice", RoleOwner); err != nil {
		t.Fatal(err)
	}
	id, _ := s.ResolveName(ctx, "acme")
	if role, _ := s.RoleFor(ctx, id, alice); role != RoleOwner {
		t.Errorf("role = %q, want owner", role)
	}
}

// The last owner cannot be removed or demoted. A namespace with no
// owner is permanently unpublishable and there is no way back, so the
// state must be unreachable rather than merely discouraged.
func TestOrg_LastOwnerIsProtected(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	if err := s.CreateOrg(ctx, "dlouwers", "acme", ""); err != nil {
		t.Fatal(err)
	}
	seedUser(t, s, 1, "alice")
	seedUser(t, s, 2, "bob")
	if err := s.AddOrgMember(ctx, "dlouwers", "acme", "alice", RoleOwner); err != nil {
		t.Fatal(err)
	}
	if err := s.AddOrgMember(ctx, "dlouwers", "acme", "bob", RoleMember); err != nil {
		t.Fatal(err)
	}

	if err := s.SetOrgMemberRole(ctx, "dlouwers", "acme", "alice", RoleMember); !errors.Is(err, ErrLastOwner) {
		t.Errorf("demoting the only owner = %v, want ErrLastOwner", err)
	}
	if err := s.RemoveOrgMember(ctx, "dlouwers", "acme", "alice"); !errors.Is(err, ErrLastOwner) {
		t.Errorf("removing the only owner = %v, want ErrLastOwner", err)
	}
	// A non-owner is removable, and does not count as protection.
	if err := s.RemoveOrgMember(ctx, "dlouwers", "acme", "bob"); err != nil {
		t.Errorf("removing a plain member: %v", err)
	}

	// With a second owner, the first may go.
	seedUser(t, s, 3, "carol")
	if err := s.AddOrgMember(ctx, "dlouwers", "acme", "carol", RoleOwner); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveOrgMember(ctx, "dlouwers", "acme", "alice"); err != nil {
		t.Errorf("removing an owner when another remains: %v", err)
	}
	// …and now carol is protected in turn.
	if err := s.RemoveOrgMember(ctx, "dlouwers", "acme", "carol"); !errors.Is(err, ErrLastOwner) {
		t.Errorf("removing the new last owner = %v, want ErrLastOwner", err)
	}
}

// A personal namespace has exactly one owner, so the same guard keeps
// somebody from being orphaned out of their own workspace.
func TestOrg_PersonalNamespaceOwnerIsProtected(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	if _, err := s.UpsertGitHubUser(ctx, 9, "solo", "solo@example.com"); err != nil {
		t.Fatal(err)
	}
	name := DerivedName(9)
	if err := s.RemoveOrgMember(ctx, "dlouwers", name, "solo"); !errors.Is(err, ErrLastOwner) {
		t.Errorf("removing someone from their own namespace = %v, want ErrLastOwner", err)
	}
}

func TestOrg_UnknownRoleRejected(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	if err := s.CreateOrg(ctx, "dlouwers", "acme", ""); err != nil {
		t.Fatal(err)
	}
	seedUser(t, s, 1, "alice")
	if err := s.AddOrgMember(ctx, "dlouwers", "acme", "alice", "admin"); err == nil {
		t.Error("an unknown role was accepted on add")
	}
	if err := s.AddOrgMember(ctx, "dlouwers", "acme", "alice", RoleMember); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOrgMemberRole(ctx, "dlouwers", "acme", "alice", "superuser"); err == nil {
		t.Error("an unknown role was accepted on change")
	}
}
