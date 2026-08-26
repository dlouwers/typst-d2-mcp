package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dlouwers/typst-d2-mcp/internal/authdb"
	"github.com/dlouwers/typst-d2-mcp/internal/identity"
	"github.com/dlouwers/typst-d2-mcp/internal/workspace"
	"github.com/mark3labs/mcp-go/mcp"
)

// writePackage puts a minimal typst package into a store, exporting a
// mark() the test can look for in a compile.
func writePackage(t *testing.T, dataDir, namespace, version string) {
	t.Helper()
	dir := filepath.Join(dataDir, "typst", "packages", namespace, "templates", version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	toml := "[package]\nname = \"templates\"\nversion = \"" + version + "\"\nentrypoint = \"lib.typ\"\n"
	if err := os.WriteFile(filepath.Join(dir, "typst.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	lib := "#let mark() = [" + namespace + "-template]\n"
	if err := os.WriteFile(filepath.Join(dir, "lib.typ"), []byte(lib), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The built-in namespace is the house style — the default the templates
// exist to provide. Gating it on membership would break every document
// that already imports it and leave a new user with nothing.
// newTestStore opens a throwaway auth database.
func newTestStore(t *testing.T) *authdb.Store {
	t.Helper()
	store, err := authdb.Open(filepath.Join(t.TempDir(), "auth.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// seedUser registers a GitHub user and returns the identity a request
// from them would carry.
func seedUser(t *testing.T, store *authdb.Store, login string, githubID int64) identity.Identity {
	t.Helper()
	if _, err := store.UpsertGitHubUser(t.Context(), githubID, login, login+"@example.com"); err != nil {
		t.Fatalf("UpsertGitHubUser: %v", err)
	}
	return identity.Identity{UserID: fmt.Sprintf("gh:%d", githubID)}
}

func TestAllowedNamespaces_BuiltinAlwaysPresent(t *testing.T) {
	got, err := allowedNamespaces(context.Background(), nil, identity.Identity{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != builtinNamespace {
		t.Errorf("allowedNamespaces = %v, want just %q", got, builtinNamespace)
	}
}

// An anonymous caller has no organisations to resolve, store or not.
func TestAllowedNamespaces_AnonymousGetsBuiltinOnly(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.CreateOrg(ctx, "admin", "acme", "Acme"); err != nil {
		t.Fatal(err)
	}
	got, err := allowedNamespaces(ctx, store, identity.Identity{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != builtinNamespace {
		t.Errorf("allowedNamespaces = %v, want just %q", got, builtinNamespace)
	}
}

// A member resolves their organisations; a non-member does not. This is
// the isolation boundary rung 4 exists to draw.
func TestAllowedNamespaces_MembershipDecides(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	for _, slug := range []string{"acme", "globex"} {
		if err := store.CreateOrg(ctx, "admin", slug, slug); err != nil {
			t.Fatal(err)
		}
	}
	member := seedUser(t, store, "member", 1)
	outsider := seedUser(t, store, "outsider", 2)
	if err := store.AddOrgMember(ctx, "admin", "acme", "member"); err != nil {
		t.Fatal(err)
	}

	got, err := allowedNamespaces(ctx, store, member)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"acme", builtinNamespace}; !equalStrings(got, want) {
		t.Errorf("member sees %v, want %v", got, want)
	}

	got, err = allowedNamespaces(ctx, store, outsider)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{builtinNamespace}; !equalStrings(got, want) {
		t.Errorf("outsider sees %v, want %v", got, want)
	}
}

// The view exposes exactly the permitted namespaces and nothing else.
func TestPackageView_ExposesOnlyAllowed(t *testing.T) {
	data := t.TempDir()
	writePackage(t, data, "house", "0.1.0")
	writePackage(t, data, "acme", "1.0.0")
	writePackage(t, data, "globex", "1.0.0")

	view, cleanup, err := packageView(data, []string{"house", "acme"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	entries, err := os.ReadDir(filepath.Join(view, "typst", "packages"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{"acme", "house"}; !equalStrings(names, want) {
		t.Errorf("view contains %v, want %v", names, want)
	}
}

// An organisation exists in the schema before anyone publishes to it
// (rung 5). That is not an error, and must not fail the compile.
func TestPackageView_UnpublishedNamespaceIsSkipped(t *testing.T) {
	data := t.TempDir()
	writePackage(t, data, "house", "0.1.0")

	view, cleanup, err := packageView(data, []string{"house", "brand-new-org"})
	if err != nil {
		t.Fatalf("an org with nothing published broke the view: %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(view, "typst", "packages", "house")); err != nil {
		t.Errorf("published namespace missing: %v", err)
	}
}

func TestPackageView_CleanupRemovesIt(t *testing.T) {
	data := t.TempDir()
	writePackage(t, data, "house", "0.1.0")
	view, cleanup, err := packageView(data, []string{"house"})
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := os.Stat(view); !os.IsNotExist(err) {
		t.Errorf("view survived cleanup: %v", err)
	}
}

// compileEnv must replace an inherited XDG_DATA_HOME rather than append
// a second one — the child would otherwise take whichever the OS
// resolves first, which is not something to leave to chance on an
// isolation boundary.
func TestCompileEnv_ReplacesInheritedDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/inherited/should/not/win")
	env := compileEnv("/the/view")

	var seen []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "XDG_DATA_HOME=") {
			seen = append(seen, kv)
		}
	}
	if len(seen) != 1 {
		t.Fatalf("XDG_DATA_HOME appears %d times: %v", len(seen), seen)
	}
	if seen[0] != "XDG_DATA_HOME=/the/view" {
		t.Errorf("XDG_DATA_HOME = %q, want the view", seen[0])
	}
}

// End to end through the real typst binary: a permitted namespace
// imports, and one the caller is not a member of does not resolve at
// all — with the control showing the package itself is fine.
func TestCompile_NamespaceIsolation(t *testing.T) {
	requireTypst(t)

	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	writePackage(t, data, "house", "0.1.0")
	writePackage(t, data, "acme", "1.0.0")

	root := t.TempDir()
	factory := workspace.TenantFactory{Root: root}
	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: "user123"})

	// The caller belongs to no organisation, so acme is out of reach.
	if res := putFile(t, ctx, factory, "denied.typ",
		"#import \"@acme/templates:1.0.0\": mark\n#mark()\n"); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	res := compile(t, ctx, factory, "denied.typ")
	if !res.IsError {
		t.Fatal("a caller compiled against an organisation it does not belong to")
	}
	if got := resultText(res); !strings.Contains(got, "package not found") {
		t.Errorf("expected an unresolved package, got: %s", got)
	}

	// The built-in still resolves for the same caller.
	if res := putFile(t, ctx, factory, "ok.typ",
		"#import \"@house/templates:0.1.0\": mark\n#mark()\n"); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	if res := compile(t, ctx, factory, "ok.typ"); res.IsError {
		t.Fatalf("the built-in namespace stopped resolving: %s", resultText(res))
	}

	// Control: acme compiles when the whole store is visible, proving
	// the package is sound and the view is what excluded it.
	in := filepath.Join(t.TempDir(), "control.typ")
	out := strings.TrimSuffix(in, ".typ") + ".pdf"
	if err := os.WriteFile(in, []byte("#import \"@acme/templates:1.0.0\": mark\n#mark()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("typst", "compile", in, out)
	cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+data)
	if outBytes, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("control compile failed, so the isolation test proves nothing: %v\n%s", err, outBytes)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// writePackageWithAsset builds a template package that ships a logo and
// references it from lib.typ by a path relative to the package root.
func writePackageWithAsset(t *testing.T, dataDir, namespace, version string) {
	t.Helper()
	dir := filepath.Join(dataDir, "typst", "packages", namespace, "templates", version)
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"typst.toml": "[package]\nname = \"templates\"\nversion = \"" + version +
			"\"\nentrypoint = \"lib.typ\"\n",
		"assets/logo.svg": `<svg xmlns="http://www.w3.org/2000/svg" width="20" height="20">` +
			`<rect width="20" height="20" fill="#006566"/></svg>`,
		"lib.typ": "#let branded(body) = {\n  image(\"assets/logo.svg\", width: 10mm)\n  body\n}\n",
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A branded template needs its mark, so a template package must be able
// to read files it ships. Two things could have broken that and neither
// is obvious: the compile passes --root at the workspace (#91), and the
// package reaches typst through a symlinked view (#63 rung 4). Neither
// does — typst treats the package directory as its own root — but that
// is worth a test rather than an assumption, because the failure would
// only appear once someone shipped a template with a logo.
func TestCompile_TemplateAssetResolves(t *testing.T) {
	requireTypst(t)

	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	writePackageWithAsset(t, data, builtinNamespace, "9.9.9")

	root := t.TempDir()
	factory := workspace.TenantFactory{Root: root}
	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: "user123"})

	src := "#import \"@" + builtinNamespace + "/templates:9.9.9\": branded\n" +
		"#show: branded\n= Title\nBody.\n"
	if res := putFile(t, ctx, factory, "doc.typ", src); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}

	res := compile(t, ctx, factory, "doc.typ")
	if res.IsError {
		t.Fatalf("a template could not read its own bundled asset: %s", resultText(res))
	}
	assertPDF(t, filepath.Join(root, "user123", "doc.pdf"))
}

// The same, for a template reached through organisation membership
// rather than the built-in namespace — the asset must survive the
// symlink hop too.
func TestCompile_OrgTemplateAssetResolves(t *testing.T) {
	requireTypst(t)

	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	writePackageWithAsset(t, data, "acme", "1.0.0")

	store := newTestStore(t)
	if err := store.CreateOrg(t.Context(), "admin", "acme", "Acme"); err != nil {
		t.Fatal(err)
	}
	member := seedUser(t, store, "member", 1)
	if err := store.AddOrgMember(t.Context(), "admin", "acme", "member"); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	factory := workspace.TenantFactory{Root: root}
	ctx := identity.WithIdentity(context.Background(), member)

	src := "#import \"@acme/templates:1.0.0\": branded\n#show: branded\n= Title\n"
	if res := putFileStore(t, ctx, factory, store, "doc.typ", src); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}

	req := mcp.CallToolRequest{}
	req.Params.Name = "compile_typst_with_d2"
	req.Params.Arguments = map[string]any{"file_path": "doc.typ"}
	res, err := handleCompileTypst(factory, store)(ctx, req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("an org template could not read its own asset: %s", resultText(res))
	}
	assertPDF(t, filepath.Join(root, member.UserID, "doc.pdf"))
}

// The package view is a font path, so a template can ship the typeface
// it is designed in. Unit-level, so it runs everywhere.
func TestTypstArgs_PackageViewIsAFontPath(t *testing.T) {
	root := t.TempDir()
	r, err := workspace.NewScopedFS(root)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(typstArgs(r, "in.typ", "out.pdf", packageFontPath("/the/view")), " ")
	if want := "--font-path " + filepath.Join("/the/view", "typst", "packages"); !strings.Contains(got, want) {
		t.Errorf("args = %s, want it to contain %s", got, want)
	}

	// An empty extra path must not produce a bare --font-path.
	got = strings.Join(typstArgs(r, "in.typ", "out.pdf", ""), " ")
	if strings.Contains(got, "--font-path ") && strings.Contains(got, "--font-path  ") {
		t.Errorf("empty font path leaked into args: %s", got)
	}
}

func TestPackageFontPath_EmptyViewIsEmpty(t *testing.T) {
	if got := packageFontPath(""); got != "" {
		t.Errorf("packageFontPath(\"\") = %q, want empty", got)
	}
}

// End to end: a font shipped inside a template package is resolvable
// when compiling against that template, and typst does not fall back.
func TestCompile_TemplateFontResolves(t *testing.T) {
	requireTypst(t)
	src := findSystemFont(t) // skips when the host has no fonts

	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	pkg := filepath.Join(data, "typst", "packages", builtinNamespace, "templates", "9.9.9")
	if err := os.MkdirAll(filepath.Join(pkg, "fonts"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "fonts", filepath.Base(src)), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	// Which family does that file provide, ignoring everything else?
	families := familiesFromPathOnly(t, filepath.Join(pkg, "fonts"))
	if len(families) == 0 {
		t.Skip("could not determine the family of the stand-in font")
	}

	toml := "[package]\nname = \"templates\"\nversion = \"9.9.9\"\nentrypoint = \"lib.typ\"\n"
	if err := os.WriteFile(filepath.Join(pkg, "typst.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	lib := "#let branded(body) = {\n  set text(font: \"" + families[0] + "\")\n  body\n}\n"
	if err := os.WriteFile(filepath.Join(pkg, "lib.typ"), []byte(lib), 0o644); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	factory := workspace.TenantFactory{Root: root}
	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: "user123"})

	doc := "#import \"@" + builtinNamespace + "/templates:9.9.9\": branded\n#show: branded\n= Title\n"
	if res := putFile(t, ctx, factory, "doc.typ", doc); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	res := compile(t, ctx, factory, "doc.typ")
	if res.IsError {
		t.Fatalf("compile failed: %s", resultText(res))
	}
	if got := resultText(res); strings.Contains(strings.ToLower(got), "unknown font family") {
		t.Errorf("a font shipped with the template was not resolved:\n%s", got)
	}
}
