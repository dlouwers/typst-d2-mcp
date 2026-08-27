package authdb

import (
	"errors"
	"strings"
	"testing"
)

// Everyone has a namespace from their first sign-in, so there is never a
// moment where somebody has nowhere to put a template.
func TestPersonalNamespace_ProvisionedAtSignIn(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	if _, err := s.UpsertGitHubUser(ctx, 4242, "octocat", "octocat@example.com"); err != nil {
		t.Fatal(err)
	}

	ns, err := s.NamespacesForUser(ctx, "gh:4242")
	if err != nil {
		t.Fatal(err)
	}
	name := DerivedName(4242)
	if ns[name] == "" {
		t.Fatalf("no personal namespace after sign-in: %v", ns)
	}

	// And she owns it — which is what lets her publish to it without any
	// permission model existing yet.
	role, err := s.RoleFor(ctx, ns[name], "gh:4242")
	if err != nil {
		t.Fatal(err)
	}
	if role != RoleOwner {
		t.Errorf("role = %q, want %q", role, RoleOwner)
	}
}

// Signing in again must not mint a second namespace.
func TestPersonalNamespace_IsIdempotent(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	first, err := s.EnsurePersonalNamespace(ctx, "gh:7", 7)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		again, err := s.EnsurePersonalNamespace(ctx, "gh:7", 7)
		if err != nil {
			t.Fatal(err)
		}
		if again != first {
			t.Fatalf("sign-in %d minted a new namespace: %q then %q", i, first, again)
		}
	}
}

// The derived name comes from the GitHub numeric id, which survives a
// rename. Deriving from the login — the thing #63 warned against — would
// mean a rename elsewhere silently broke every document importing it.
func TestPersonalNamespace_SurvivesALoginRename(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	if _, err := s.UpsertGitHubUser(ctx, 99, "oldname", "a@example.com"); err != nil {
		t.Fatal(err)
	}
	before, err := s.NamespacesForUser(ctx, "gh:99")
	if err != nil {
		t.Fatal(err)
	}

	// Same account, new login.
	if _, err := s.UpsertGitHubUser(ctx, 99, "newname", "a@example.com"); err != nil {
		t.Fatal(err)
	}
	after, err := s.NamespacesForUser(ctx, "gh:99")
	if err != nil {
		t.Fatal(err)
	}

	if len(before) != len(after) || before[DerivedName(99)] != after[DerivedName(99)] {
		t.Errorf("a login rename changed the namespace: %v then %v", before, after)
	}
}

// The whole point of the reshape: giving a namespace a proper name is a
// pointer edit. The id does not change, so nothing on disk moves, and
// the old name keeps resolving so documents already importing it work.
func TestAddName_UpgradeKeepsTheOldNameWorking(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	id, err := s.EnsurePersonalNamespace(ctx, "gh:5", 5)
	if err != nil {
		t.Fatal(err)
	}
	derived := DerivedName(5)

	if err := s.AddName(ctx, "gh:5", id, "stormlantern", "Stormlantern"); err != nil {
		t.Fatalf("AddName: %v", err)
	}

	// Both names, one namespace.
	for _, name := range []string{derived, "stormlantern"} {
		got, err := s.ResolveName(ctx, name)
		if err != nil {
			t.Fatalf("resolve %q: %v", name, err)
		}
		if got != id {
			t.Errorf("%q resolves to %q, want %q", name, got, id)
		}
	}

	// The member sees both, so either import compiles.
	ns, err := s.NamespacesForUser(ctx, "gh:5")
	if err != nil {
		t.Fatal(err)
	}
	if ns[derived] != id || ns["stormlantern"] != id {
		t.Errorf("caller does not resolve both names: %v", ns)
	}
}

// Growth is additive: adding people does not change the namespace, its
// id, or what it is.
func TestNamespace_GrowsWithoutMigrating(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	id, err := s.EnsurePersonalNamespace(ctx, "gh:1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddName(ctx, "gh:1", id, "acme", "Acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertGitHubUser(ctx, 2, "colleague", "c@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddOrgMember(ctx, "admin", "acme", "colleague", RoleMember); err != nil {
		t.Fatal(err)
	}

	after, err := s.ResolveName(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if after != id {
		t.Errorf("namespace id changed on sharing: %q then %q", id, after)
	}

	// The original owner is still the owner; the newcomer is not.
	if role, _ := s.RoleFor(ctx, id, "gh:1"); role != RoleOwner {
		t.Errorf("creator role = %q, want %q", role, RoleOwner)
	}
	if role, _ := s.RoleFor(ctx, id, "gh:2"); role != RoleMember {
		t.Errorf("added member role = %q, want %q — publishing is not handed out by being added", role, RoleMember)
	}
}

// Nobody may claim a derived name, or claiming "gh-12345" before that
// user first signs in would hand you their namespace.
func TestValidateName_DerivedPrefixIsReserved(t *testing.T) {
	if err := ValidateName(DerivedName(12345)); !errors.Is(err, ErrInvalidName) {
		t.Errorf("ValidateName(%q) = %v, want ErrInvalidName", DerivedName(12345), err)
	}
}

// typst rejects a purely numeric namespace, so the grammar must too —
// otherwise a name passes validation and fails at every compile.
func TestValidateName_RejectsWhatTypstRejects(t *testing.T) {
	for _, name := range []string{"12345", "007", "9"} {
		if err := ValidateName(name); !errors.Is(err, ErrInvalidName) {
			t.Errorf("ValidateName(%q) = %v, want ErrInvalidName (typst rejects it)", name, err)
		}
	}
	for _, name := range []string{"acme", "acme-co", "a1", "x2y"} {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}
}

// A namespace id must never look like something to read meaning from.
func TestNewNamespaceID_IsOpaqueAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id, err := newNamespaceID()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(id, "ns-") {
			t.Errorf("id %q has no ns- prefix", id)
		}
		if seen[id] {
			t.Fatalf("duplicate namespace id %q", id)
		}
		seen[id] = true
	}
}

func TestAddName_RejectsATakenName(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	a, err := s.EnsurePersonalNamespace(ctx, "gh:1", 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.EnsurePersonalNamespace(ctx, "gh:2", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddName(ctx, "gh:1", a, "acme", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AddName(ctx, "gh:2", b, "acme", ""); !errors.Is(err, ErrNameTaken) {
		t.Errorf("second claim of acme = %v, want ErrNameTaken", err)
	}
}

// Personal namespaces must not bury the organisations an administrator
// came to manage.
func TestListOrgs_ExcludesPersonalNamespaces(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	for i := int64(1); i <= 3; i++ {
		if _, err := s.UpsertGitHubUser(ctx, i, "user", "u@example.com"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateOrg(ctx, "admin", "acme", "Acme"); err != nil {
		t.Fatal(err)
	}

	orgs, err := s.ListOrgs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(orgs) != 1 || orgs[0].Slug != "acme" {
		t.Errorf("ListOrgs = %+v, want just acme", orgs)
	}
}
