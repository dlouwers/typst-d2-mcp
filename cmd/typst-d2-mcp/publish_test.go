package main

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dlouwers/typst-d2-mcp/internal/authdb"
	"github.com/dlouwers/typst-d2-mcp/internal/identity"
	"github.com/dlouwers/typst-d2-mcp/internal/workspace"
	"github.com/mark3labs/mcp-go/mcp"
)

// publishFixture is a signed-in caller with their own namespace, a
// workspace, and a template staged in it ready to publish.
type publishFixture struct {
	store   *authdb.Store
	factory workspace.Factory
	ctx     context.Context
	user    identity.Identity
	nsName  string
	data    string
	root    string
}

func newPublishFixture(t *testing.T) *publishFixture {
	t.Helper()
	requireTypst(t)

	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)

	store := newTestStore(t)
	user := seedUser(t, store, "author", 1)
	if _, err := store.EnsurePersonalNamespace(t.Context(), user.UserID, 1); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	return &publishFixture{
		store:   store,
		factory: workspace.TenantFactory{Root: root},
		ctx:     identity.WithIdentity(context.Background(), user),
		user:    user,
		nsName:  authdb.DerivedName(1),
		data:    data,
		root:    root,
	}
}

// stage writes a candidate package into the caller's workspace.
func (f *publishFixture) stage(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		if res := putFileStore(t, f.ctx, f.factory, f.store, dir+"/"+rel, content); res.IsError {
			t.Fatalf("put_file %s: %s", rel, resultText(res))
		}
	}
}

func (f *publishFixture) publish(t *testing.T, source, namespace, version string) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = "publish_template"
	req.Params.Arguments = map[string]any{
		"source": source, "namespace": namespace, "version": version,
	}
	res, err := handlePublishTemplate(f.factory, f.store)(f.ctx, req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return res
}

const goodTOML = "[package]\nname = \"templates\"\nversion = \"1.0.0\"\nentrypoint = \"lib.typ\"\n"
const goodLib = "#let report(title: \"Untitled\", body) = {\n  set page(margin: 2cm)\n  heading(title)\n  body\n}\n"

// The happy path, end to end: publish, then compile a document that
// imports what was just published.
func TestPublish_ThenImport(t *testing.T) {
	f := newPublishFixture(t)
	t.Setenv(envQuotaPerDay, "100")
	f.stage(t, "tpl", map[string]string{"typst.toml": goodTOML, "lib.typ": goodLib})

	res := f.publish(t, "tpl", f.nsName, "1.0.0")
	if res.IsError {
		t.Fatalf("publish failed: %s", resultText(res))
	}
	if got := resultText(res); !strings.Contains(got, "@"+f.nsName+"/templates:1.0.0") {
		t.Errorf("result does not give the import line: %s", got)
	}

	doc := "#import \"@" + f.nsName + "/templates:1.0.0\": report\n" +
		"#show: report.with(title: \"Hello\")\nBody.\n"
	if r := putFileStore(t, f.ctx, f.factory, f.store, "doc.typ", doc); r.IsError {
		t.Fatalf("put_file: %s", resultText(r))
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = "compile_typst_with_d2"
	req.Params.Arguments = map[string]any{"file_path": "doc.typ"}
	cres, err := handleCompileTypst(f.factory, f.store)(f.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if cres.IsError {
		t.Fatalf("a just-published template did not import: %s", resultText(cres))
	}
}

// The gate. A template that does not compile must not reach anyone
// else's documents, and the caller must be told why.
func TestPublish_BrokenTemplateIsRefused(t *testing.T) {
	f := newPublishFixture(t)
	f.stage(t, "tpl", map[string]string{
		"typst.toml": goodTOML,
		"lib.typ":    "#let report(title: \"x\", body) = {\n  this is not typst (((\n}\n",
	})

	res := f.publish(t, "tpl", f.nsName, "1.0.0")
	if !res.IsError {
		t.Fatal("a template that does not compile was published")
	}
	got := resultText(res)
	if !strings.Contains(got, "did not compile") {
		t.Errorf("error does not say what happened: %s", got)
	}
	// And nothing was installed.
	dest := filepath.Join(f.data, "typst", "packages")
	if entries, _ := os.ReadDir(dest); len(entries) != 0 {
		t.Errorf("a refused publish left files behind: %v", entries)
	}
}

// A template referencing an asset it does not ship compiles fine for
// its author and fails for everyone else. The check must catch that,
// which is only true because it compiles against the STAGED package
// rather than the workspace the files came from.
func TestPublish_MissingAssetIsCaught(t *testing.T) {
	f := newPublishFixture(t)
	f.stage(t, "tpl", map[string]string{
		"typst.toml": goodTOML,
		"lib.typ":    "#let report(body) = {\n  image(\"assets/logo.svg\", width: 10mm)\n  body\n}\n",
	})

	res := f.publish(t, "tpl", f.nsName, "1.0.0")
	if !res.IsError {
		t.Fatal("a template referencing a file it does not ship was published")
	}
}

// A template that ships its asset passes, and the asset travels with it.
func TestPublish_WithAssetSucceeds(t *testing.T) {
	f := newPublishFixture(t)
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10">` +
		`<rect width="10" height="10" fill="#006566"/></svg>`
	f.stage(t, "tpl", map[string]string{
		"typst.toml":      goodTOML,
		"assets/logo.svg": svg,
		"lib.typ":         "#let report(body) = {\n  image(\"assets/logo.svg\", width: 10mm)\n  body\n}\n",
	})

	if res := f.publish(t, "tpl", f.nsName, "1.0.0"); res.IsError {
		t.Fatalf("publish failed: %s", resultText(res))
	}
	nsID, err := f.store.ResolveName(t.Context(), f.nsName)
	if err != nil {
		t.Fatal(err)
	}
	asset := filepath.Join(f.data, "typst", "packages", nsID, "templates", "1.0.0", "assets", "logo.svg")
	if _, err := os.Stat(asset); err != nil {
		t.Errorf("the asset did not travel with the package: %v", err)
	}
}

// Immutability. Documents pin what they import, so replacing a version
// would change how already-written documents render.
func TestPublish_VersionIsImmutable(t *testing.T) {
	f := newPublishFixture(t)
	f.stage(t, "tpl", map[string]string{"typst.toml": goodTOML, "lib.typ": goodLib})

	if res := f.publish(t, "tpl", f.nsName, "1.0.0"); res.IsError {
		t.Fatalf("first publish: %s", resultText(res))
	}
	res := f.publish(t, "tpl", f.nsName, "1.0.0")
	if !res.IsError {
		t.Fatal("a published version was overwritten")
	}
	if got := resultText(res); !strings.Contains(got, "immutable") {
		t.Errorf("error does not explain why: %s", got)
	}

	// A new version is the way forward, and both then exist.
	bumped := strings.Replace(goodTOML, "1.0.0", "1.0.1", 1)
	f.stage(t, "tpl2", map[string]string{"typst.toml": bumped, "lib.typ": goodLib})
	if res := f.publish(t, "tpl2", f.nsName, "1.0.1"); res.IsError {
		t.Fatalf("publishing a new version: %s", resultText(res))
	}
	nsID, _ := f.store.ResolveName(t.Context(), f.nsName)
	for _, v := range []string{"1.0.0", "1.0.1"} {
		if _, err := os.Stat(filepath.Join(f.data, "typst", "packages", nsID, "templates", v)); err != nil {
			t.Errorf("version %s missing: %v", v, err)
		}
	}
}

// You may publish only where you are an owner.
func TestPublish_RequiresOwnership(t *testing.T) {
	f := newPublishFixture(t)
	f.stage(t, "tpl", map[string]string{"typst.toml": goodTOML, "lib.typ": goodLib})

	// A namespace the caller is merely a member of.
	if err := f.store.CreateOrg(t.Context(), "admin", "acme", "Acme"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddOrgMember(t.Context(), "admin", "acme", "author"); err != nil {
		t.Fatal(err)
	}
	res := f.publish(t, "tpl", "acme", "1.0.0")
	if !res.IsError {
		t.Fatal("a member published to a namespace they do not own")
	}
	if got := resultText(res); !strings.Contains(got, "not an owner") {
		t.Errorf("error does not explain: %s", got)
	}

	// A namespace they cannot see at all.
	if err := f.store.CreateOrg(t.Context(), "admin", "globex", "Globex"); err != nil {
		t.Fatal(err)
	}
	if res := f.publish(t, "tpl", "globex", "1.0.0"); !res.IsError {
		t.Fatal("published into a namespace the caller cannot even see")
	}
}

// The built-in is the server's, not a tenant's.
func TestPublish_BuiltinIsNotWritable(t *testing.T) {
	f := newPublishFixture(t)
	f.stage(t, "tpl", map[string]string{"typst.toml": goodTOML, "lib.typ": goodLib})
	if res := f.publish(t, "tpl", builtinNamespace, "9.9.9"); !res.IsError {
		t.Fatal("published into the built-in namespace")
	}
}

// Shapes typst rejects must be rejected here, not at every later import.
func TestPublish_RejectsBadVersions(t *testing.T) {
	f := newPublishFixture(t)
	f.stage(t, "tpl", map[string]string{"typst.toml": goodTOML, "lib.typ": goodLib})
	for _, v := range []string{"1", "1.0", "v1.0.0", "1.0.0-beta", "latest"} {
		if res := f.publish(t, "tpl", f.nsName, v); !res.IsError {
			t.Errorf("version %q was accepted", v)
		}
	}
}

func TestPublish_RequiresTypstToml(t *testing.T) {
	f := newPublishFixture(t)
	f.stage(t, "tpl", map[string]string{"lib.typ": goodLib})
	res := f.publish(t, "tpl", f.nsName, "1.0.0")
	if !res.IsError {
		t.Fatal("published a directory with no typst.toml")
	}
	if got := resultText(res); !strings.Contains(got, "typst.toml") {
		t.Errorf("error does not name what is missing: %s", got)
	}
}

// An export that cannot be applied with default arguments is reported
// but does not block: not every export is a document template.
func TestPublish_ShowRuleFailureIsAWarningNotAGate(t *testing.T) {
	f := newPublishFixture(t)
	f.stage(t, "tpl", map[string]string{
		"typst.toml": goodTOML,
		// A plain helper, not a show-rule template.
		"lib.typ": "#let accent = rgb(\"#006566\")\n#let swatch(colour) = rect(fill: colour, width: 5mm, height: 5mm)\n",
	})

	res := f.publish(t, "tpl", f.nsName, "1.0.0")
	if res.IsError {
		t.Fatalf("a package of plain helpers was refused: %s", resultText(res))
	}
	if got := resultText(res); !strings.Contains(got, "show rule") {
		t.Logf("no show-rule note (acceptable): %s", got)
	}
}

// A staged compile artefact in the source directory must not be
// published along with the template.
func TestPublish_SkipsStagedArtefacts(t *testing.T) {
	f := newPublishFixture(t)
	f.stage(t, "tpl", map[string]string{
		"typst.toml":                           goodTOML,
		"lib.typ":                              goodLib,
		workspace.StagePrefix + "leftover.typ": "= not part of the package\n",
	})

	if res := f.publish(t, "tpl", f.nsName, "1.0.0"); res.IsError {
		t.Fatalf("publish failed: %s", resultText(res))
	}
	nsID, _ := f.store.ResolveName(t.Context(), f.nsName)
	dest := filepath.Join(f.data, "typst", "packages", nsID, "templates", "1.0.0")
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), workspace.StagePrefix) {
			t.Errorf("a staged artefact was published: %s", e.Name())
		}
	}
}

// Published templates show up in list_templates, which is how a caller
// finds what they just made.
func TestPublish_AppearsInListTemplates(t *testing.T) {
	f := newPublishFixture(t)
	f.stage(t, "tpl", map[string]string{"typst.toml": goodTOML, "lib.typ": goodLib})
	if res := f.publish(t, "tpl", f.nsName, "1.0.0"); res.IsError {
		t.Fatalf("publish failed: %s", resultText(res))
	}

	got := listTemplates(t, f.ctx, f.store)
	var found bool
	for _, e := range got.Templates {
		if e.Import == "@"+f.nsName+"/templates:1.0.0" {
			found = true
			if !containsString(e.Exports, "report") {
				t.Errorf("exports = %v, want report", e.Exports)
			}
		}
	}
	if !found {
		t.Errorf("the published template is not listed: %+v", got.Templates)
	}
}

// Base64 assets survive the round trip through put_file and publish.
func TestPublish_BinaryAssetRoundTrips(t *testing.T) {
	f := newPublishFixture(t)
	f.stage(t, "tpl", map[string]string{"typst.toml": goodTOML, "lib.typ": goodLib})

	req := mcp.CallToolRequest{}
	req.Params.Name = "put_file"
	req.Params.Arguments = map[string]any{
		"path":     "tpl/assets/dot.png",
		"content":  base64.StdEncoding.EncodeToString([]byte(testPNG)),
		"encoding": "base64",
	}
	if res, err := handlePutFile(f.factory, f.store)(f.ctx, req); err != nil || res.IsError {
		t.Fatalf("put_file png: %v %v", err, res)
	}

	if res := f.publish(t, "tpl", f.nsName, "1.0.0"); res.IsError {
		t.Fatalf("publish failed: %s", resultText(res))
	}
	nsID, _ := f.store.ResolveName(t.Context(), f.nsName)
	dst := filepath.Join(f.data, "typst", "packages", nsID, "templates", "1.0.0", "assets", "dot.png")
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != testPNG {
		t.Error("the binary asset did not survive publishing byte-for-byte")
	}
}

// The rule that decides whether an export gets applied to a document.
// Getting this wrong in one direction refuses legitimate packages; in
// the other it lets a broken template through — so it is worth pinning
// against the shapes that actually occur.
func TestIsDocumentTemplate(t *testing.T) {
	cases := []struct {
		name   string
		params string
		want   bool
	}{
		{"house report", `title: "Untitled", subtitle: none, author: none, date: datetime.today().display("[year]"), body`, true},
		{"body only", "body", true},
		{"named then body", `title: "x", body`, true},
		{"a helper taking one positional", "colour", false},
		{"a helper taking several", "colour, width", false},
		{"no parameters", "", false},
		{"all named", `title: "x", subtitle: none`, false},
		{"body not last", `body, extra`, false},
		{"a default containing a comma", `date: datetime.today().display("[year]-[month]"), body`, true},
		{"a default containing brackets", `caption: [a, b], body`, true},
		{"named body is not positional", `body: []`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDocumentTemplate(tc.params); got != tc.want {
				t.Errorf("isDocumentTemplate(%q) = %v, want %v", tc.params, got, tc.want)
			}
		})
	}
}

// The real house template is the fixture that matters: report and adr
// must both be recognised, and the underscore-prefixed internals must
// not be — they are not exported and applying them would be nonsense.
func TestDocumentTemplateExports_HouseTemplate(t *testing.T) {
	lib := filepath.Join(repoTemplates(t), templateNamespace, templateName, templateVersion, "lib.typ")
	got := documentTemplateExports(lib)
	if want := []string{"adr", "report"}; !equalStrings(got, want) {
		t.Errorf("documentTemplateExports = %v, want %v", got, want)
	}
}

// Parameter lists span lines and contain nested calls; the parser has
// to survive both or it silently classifies a real template as a helper.
func TestLetDeclarations_MultilineAndNested(t *testing.T) {
	src := `// #let decoy(body) = none   <- a comment, not a declaration
#let _private(body) = body
#let report(
  title: "Untitled",
  date: datetime.today().display("[year]-[month]-[day]"),
  body,
) = {
  body
}
#let accent = rgb("#006566")
`
	decls := letDeclarations(src)
	var names []string
	for _, d := range decls {
		names = append(names, d.name)
	}
	if !containsString(names, "report") {
		t.Fatalf("multiline declaration not found: %v", names)
	}
	if containsString(names, "_private") {
		t.Errorf("an underscore-prefixed internal was treated as an export: %v", names)
	}
	if containsString(names, "accent") {
		t.Errorf("a value binding was treated as a function: %v", names)
	}
	for _, d := range decls {
		if d.name == "report" && !isDocumentTemplate(d.params) {
			t.Errorf("multiline report not recognised as a template; params = %q", d.params)
		}
	}
}

// #115: the check compiles against everything the caller could import,
// not the candidate alone. Building on the house style is the
// encouraged thing to do, and staging the candidate by itself rejected
// exactly those templates with "package not found".
func TestPublish_TemplateBuiltOnTheHouseStyle(t *testing.T) {
	f := newPublishFixture(t)
	seedBundledTemplates() // the house package, as a real server has it

	lib := "#import \"@house/templates:0.1.0\": report\n" +
		"#let incident(title: \"Untitled\", body) = {\n" +
		"  show: report.with(title: title)\n  body\n}\n"
	f.stage(t, "tpl", map[string]string{
		"typst.toml": "[package]\nname = \"templates\"\nversion = \"1.0.0\"\nentrypoint = \"lib.typ\"\n",
		"lib.typ":    lib,
	})

	res := f.publish(t, "tpl", f.nsName, "1.0.0")
	if res.IsError {
		t.Fatalf("a template built on the house style was refused:\n%s", resultText(res))
	}
}

// A namespace the publisher cannot see must still be unreachable from
// inside the check — widening what it resolves must not widen that.
func TestPublish_CheckCannotReachAnotherTenantsNamespace(t *testing.T) {
	f := newPublishFixture(t)

	// Another tenant publishes something.
	other := seedUser(t, f.store, "stranger", 77)
	otherNS, err := f.store.EnsurePersonalNamespace(t.Context(), other.UserID, 77)
	if err != nil {
		t.Fatal(err)
	}
	writePackage(t, f.data, otherNS, "1.0.0")
	strangerName := authdb.DerivedName(77)

	f.stage(t, "tpl", map[string]string{
		"typst.toml": goodTOML,
		"lib.typ": "#import \"@" + strangerName + "/templates:1.0.0\": mark\n" +
			"#let report(body) = { mark(); body }\n",
	})

	res := f.publish(t, "tpl", f.nsName, "1.0.0")
	if !res.IsError {
		t.Fatal("the check resolved a namespace the publisher cannot see")
	}
}

// A new version may build on an older one in the same namespace.
func TestPublish_NewVersionCanBuildOnAnOlderOne(t *testing.T) {
	f := newPublishFixture(t)

	f.stage(t, "base", map[string]string{
		"typst.toml": "[package]\nname = \"base\"\nversion = \"1.0.0\"\nentrypoint = \"lib.typ\"\n",
		"lib.typ":    "#let accent = rgb(\"#006566\")\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Name = "publish_template"
	req.Params.Arguments = map[string]any{
		"source": "base", "namespace": f.nsName, "version": "1.0.0", "name": "base",
	}
	res, err := handlePublishTemplate(f.factory, f.store)(f.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("publishing the base package: %s", resultText(res))
	}

	f.stage(t, "tpl", map[string]string{
		"typst.toml": goodTOML,
		"lib.typ": "#import \"@" + f.nsName + "/base:1.0.0\": accent\n" +
			"#let report(body) = { text(fill: accent, body) }\n",
	})
	if res := f.publish(t, "tpl", f.nsName, "1.0.0"); res.IsError {
		t.Fatalf("a template building on an earlier package in its own namespace was refused:\n%s",
			resultText(res))
	}
}
