package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dlouwers/typst-d2-mcp/internal/authdb"
	"github.com/dlouwers/typst-d2-mcp/internal/identity"
	"github.com/mark3labs/mcp-go/mcp"
)

func listTemplates(t *testing.T, ctx context.Context, store *authdb.Store) listTemplatesResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = "list_templates"
	res, err := handleListTemplates(store)(ctx, req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("list_templates failed: %s", resultText(res))
	}
	var out listTemplatesResult
	if err := json.Unmarshal([]byte(resultText(res)), &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, resultText(res))
	}
	return out
}

// The built-in templates are listed for anyone, with the exact import
// string — the one thing a caller cannot guess.
func TestListTemplates_BuiltinIsAlwaysListed(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	writePackage(t, data, builtinNamespace, "0.1.0")

	got := listTemplates(t, context.Background(), nil)
	if len(got.Templates) != 1 {
		t.Fatalf("got %d templates, want 1: %+v", len(got.Templates), got.Templates)
	}
	e := got.Templates[0]
	if e.Import != "@"+builtinNamespace+"/templates:0.1.0" {
		t.Errorf("import = %q", e.Import)
	}
	if !e.Builtin {
		t.Error("the house namespace should be marked builtin")
	}
	if got.Note != "" {
		t.Errorf("unexpected note when templates exist: %q", got.Note)
	}
}

// The listing must be exactly what a compile would resolve — a caller
// told about a template they cannot import is worse than not being told.
func TestListTemplates_MatchesWhatTheCallerCanCompile(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	writePackage(t, data, builtinNamespace, "0.1.0")

	store := newTestStore(t)
	ctx := context.Background()
	for _, slug := range []string{"acme", "globex"} {
		if err := store.CreateOrg(ctx, "admin", slug, slug); err != nil {
			t.Fatal(err)
		}
	}
	writePackageForName(t, store, data, "acme", "1.0.0")
	writePackageForName(t, store, data, "globex", "2.0.0")
	member := seedUser(t, store, "member", 1)
	if err := store.AddOrgMember(ctx, "admin", "acme", "member", authdb.RoleMember); err != nil {
		t.Fatal(err)
	}

	memberCtx := identity.WithIdentity(ctx, member)
	got := listTemplates(t, memberCtx, store)

	var namespaces []string
	for _, e := range got.Templates {
		namespaces = append(namespaces, e.Namespace)
	}
	for _, ns := range namespaces {
		if ns == "globex" {
			t.Errorf("listed globex, which is not this caller's: %v", namespaces)
		}
	}
	if !containsString(namespaces, "acme") || !containsString(namespaces, builtinNamespace) {
		t.Errorf("listed %v, want it to include acme and %s", namespaces, builtinNamespace)
	}

	// Every listed namespace must be one the compile would resolve, so
	// the listing and the package view cannot drift apart.
	allowed, err := allowedNamespaces(memberCtx, store, member)
	if err != nil {
		t.Fatal(err)
	}
	for _, ns := range namespaces {
		if allowed[ns] == "" {
			t.Errorf("listed %q, which the compile would not resolve (%v)", ns, allowed)
		}
	}
}

// An outsider sees only the built-in.
func TestListTemplates_NonMemberSeesNoOrgTemplates(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	writePackage(t, data, builtinNamespace, "0.1.0")

	store := newTestStore(t)
	if err := store.CreateOrg(context.Background(), "admin", "acme", "Acme"); err != nil {
		t.Fatal(err)
	}
	writePackageForName(t, store, data, "acme", "1.0.0")
	outsider := seedUser(t, store, "outsider", 7)

	got := listTemplates(t, identity.WithIdentity(context.Background(), outsider), store)
	for _, e := range got.Templates {
		if e.Namespace == "acme" {
			t.Errorf("a non-member was told about %s", e.Import)
		}
	}
}

// An organisation exists before anything is published to it. Listing
// nothing for it is right; erroring is not.
func TestListTemplates_EmptyNamespaceIsNotAnError(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)

	got := listTemplates(t, context.Background(), nil)
	if len(got.Templates) != 0 {
		t.Errorf("expected no templates, got %+v", got.Templates)
	}
	if got.Note == "" {
		t.Error("an empty listing should say so rather than being a bare empty array")
	}
}

// Exports tell a caller what to import, not just where from.
func TestExportedNames(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "lib.typ")
	content := `// A comment mentioning #let decoy(
#let _accent = rgb("#1f5673")
#let _page-setup(body) = { body }
#let report(title: "Untitled", body) = { body }
#let adr(title: "Untitled decision", body) = { body }
`
	if err := os.WriteFile(lib, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got := exportedNames(lib)
	if want := []string{"adr", "report"}; !equalStrings(got, want) {
		t.Errorf("exportedNames = %v, want %v (underscored internals are not exports)", got, want)
	}
}

func TestExportedNames_MissingFileIsEmpty(t *testing.T) {
	if got := exportedNames(filepath.Join(t.TempDir(), "nope.typ")); got != nil {
		t.Errorf("exportedNames on a missing file = %v, want nil", got)
	}
}

// The real house template must produce usable exports — this is the
// listing a caller actually receives.
func TestListTemplates_RealHouseTemplateExports(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	seedBundledTemplates()

	got := listTemplates(t, context.Background(), nil)
	if len(got.Templates) != 1 {
		t.Fatalf("got %d templates, want 1", len(got.Templates))
	}
	if want := []string{"adr", "report"}; !equalStrings(got.Templates[0].Exports, want) {
		t.Errorf("exports = %v, want %v", got.Templates[0].Exports, want)
	}
}

// #108: a caller who has published nothing must still be able to find
// out which namespace is theirs. Two agents independently guessed
// names, were refused, and concluded they owned nothing — while owning
// a namespace the listing never mentioned.
func TestListTemplates_NamesYourOwnEmptyNamespace(t *testing.T) {
	// Nothing published anywhere: the case a new caller actually meets.
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)

	store := newTestStore(t)
	user := seedUser(t, store, "newcomer", 5)
	ctx := identity.WithIdentity(context.Background(), user)

	got := listTemplates(t, ctx, store)

	var mine []string
	for _, ns := range got.Namespaces {
		if ns.Writable {
			mine = append(mine, ns.Name)
		}
	}
	if len(mine) != 1 {
		t.Fatalf("writable namespaces = %v, want exactly the caller's own", mine)
	}
	if mine[0] != authdb.DerivedName(5) {
		t.Errorf("writable namespace = %q, want %q", mine[0], authdb.DerivedName(5))
	}

	// The built-in is listed but not writable.
	var sawBuiltin bool
	for _, ns := range got.Namespaces {
		if ns.Name == builtinNamespace {
			sawBuiltin = true
			if ns.Writable {
				t.Error("the built-in namespace is advertised as writable")
			}
			if !ns.Builtin {
				t.Error("the built-in is not flagged as such")
			}
		}
	}
	if !sawBuiltin {
		t.Error("the built-in namespace is missing from the listing")
	}

	// With nothing published, the note points somewhere actionable
	// rather than saying "no templates" and stopping.
	if !strings.Contains(got.Note, authdb.DerivedName(5)) {
		t.Errorf("note does not name a namespace to publish to: %q", got.Note)
	}
}

// A namespace you are only a member of is visible but not writable —
// otherwise the listing would advertise a publish that will be refused.
func TestListTemplates_MembershipIsNotWritable(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	writePackage(t, data, builtinNamespace, "0.1.0")

	store := newTestStore(t)
	if err := store.CreateOrg(context.Background(), "admin", "acme", "Acme"); err != nil {
		t.Fatal(err)
	}
	user := seedUser(t, store, "member", 6)
	if err := store.AddOrgMember(context.Background(), "admin", "acme", "member", authdb.RoleMember); err != nil {
		t.Fatal(err)
	}

	got := listTemplates(t, identity.WithIdentity(context.Background(), user), store)
	for _, ns := range got.Namespaces {
		if ns.Name == "acme" && ns.Writable {
			t.Error("a namespace the caller is only a member of is marked writable")
		}
	}
}

// A refusal must carry its own remedy rather than delegating to a tool.
func TestPublish_RefusalNamesWhereYouCanPublish(t *testing.T) {
	f := newPublishFixture(t)
	f.stage(t, "tpl", map[string]string{"typst.toml": goodTOML, "lib.typ": goodLib})

	res := f.publish(t, "tpl", "a-name-that-is-not-yours", "1.0.0")
	if !res.IsError {
		t.Fatal("published into a namespace the caller cannot see")
	}
	got := resultText(res)
	if !strings.Contains(got, f.nsName) {
		t.Errorf("refusal does not name the caller's own namespace:\n%s", got)
	}
	if strings.Contains(got, "call list_templates to see yours") {
		t.Errorf("refusal still delegates instead of answering:\n%s", got)
	}
}
