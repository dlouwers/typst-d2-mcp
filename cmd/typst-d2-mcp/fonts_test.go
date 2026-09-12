package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/dlouwers/typst-d2-mcp/internal/authdb"
	"github.com/dlouwers/typst-d2-mcp/internal/identity"
	"github.com/dlouwers/typst-d2-mcp/internal/workspace"
	"github.com/mark3labs/mcp-go/mcp"
)

func searchFonts(t *testing.T, ctx context.Context, f workspace.Factory, store *authdb.Store, query string) map[string]any {
	t.Helper()
	req := mcp.CallToolRequest{}
	args := map[string]any{}
	if query != "" {
		args["query"] = query
	}
	req.Params.Arguments = args
	res, err := handleSearchFonts(f, store)(ctx, req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("search_fonts: %s", resultText(res))
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(resultText(res)), &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, resultText(res))
	}
	return out
}

func familyNames(out map[string]any) []string {
	var names []string
	for _, f := range out["fonts"].([]any) {
		names = append(names, f.(map[string]any)["family"].(string))
	}
	return names
}

// typst's own faces must always be findable — they are what a document
// gets when nothing else is available.
func TestSearchFonts_FindsBuiltins(t *testing.T) {
	requireTypst(t)
	f, ctx := fileFixture(t)

	got := searchFonts(t, ctx, f, nil, "libertinus")
	names := familyNames(got)
	if len(names) == 0 {
		t.Fatalf("built-in family not found: %v", got)
	}
	for _, entry := range got["fonts"].([]any) {
		if src := entry.(map[string]any)["source"].(string); src != "built-in" {
			t.Errorf("Libertinus attributed to %q, want built-in", src)
		}
	}
}

// A font the caller pushed must be findable, and attributed to them —
// this is the discovery half of what #107 fixed the rendering half of.
func TestSearchFonts_FindsWorkspaceFontsAndAttributesThem(t *testing.T) {
	requireTypst(t)
	src := findSystemFont(t)
	f, ctx := fileFixture(t)

	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"path":    FontsDir + "/" + filepath.Base(src),
		"content": base64.StdEncoding.EncodeToString(raw), "encoding": "base64",
	}
	if res, err := handlePutFile(f, nil)(ctx, req); err != nil || res.IsError {
		t.Fatalf("put_file: %v", err)
	}

	resolver, err := f.Resolver(identity.Identity{UserID: "gh:4242"})
	if err != nil {
		t.Fatal(err)
	}
	pushed := familiesUnder(workspaceFontPath(resolver))
	if len(pushed) == 0 {
		t.Skip("could not determine the pushed family")
	}

	got := searchFonts(t, ctx, f, nil, strings.ToLower(pushed[0]))
	var found bool
	for _, entry := range got["fonts"].([]any) {
		m := entry.(map[string]any)
		if m["family"].(string) == pushed[0] {
			found = true
			if src := m["source"].(string); src != "workspace" {
				t.Errorf("a pushed font is attributed to %q, want workspace", src)
			}
		}
	}
	if !found {
		t.Errorf("a font pushed to fonts/ is not findable: %v", familyNames(got))
	}
}

// seedBundledCollection points the shipped-collection path at a
// directory holding one real face, and returns the family typst reports
// for it.
//
// Without this, every test touching the bundled branch is inert off the
// image: the directory does not exist on a developer machine or in CI,
// collectFonts contributes no bundled families, and a test that
// enumerates the listing enumerates built-ins only. That is how #144
// shipped past a test whose name says it covers exactly this.
func seedBundledCollection(t *testing.T) string {
	t.Helper()

	// The face has to be one typst does not already carry. Seeding a
	// family typst embeds — DejaVu Sans Mono is both a common system
	// font and an embedded one — makes every assertion below pass
	// whether or not the collection is reachable, which is the vacuous
	// shape #144 hid in.
	embedded := map[string]bool{}
	for _, fam := range embeddedFamilies() {
		embedded[strings.ToLower(fam)] = true
	}

	dir := t.TempDir()
	prev := bundledFontsPath
	bundledFontsPath = dir
	t.Cleanup(func() { bundledFontsPath = prev })

	for _, src := range systemFontCandidates(t) {
		raw, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, "seeded.ttf"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		for _, fam := range familiesUnder(dir) {
			if !embedded[strings.ToLower(fam)] {
				return fam
			}
		}
	}
	t.Skip("no system face whose family typst does not already embed")
	return ""
}

// systemFontCandidates lists usable system faces, smallest first. Plural
// because seedBundledCollection has to reject the ones typst embeds.
func systemFontCandidates(t *testing.T) []string {
	t.Helper()
	limit := maxInputBytes()
	type candidate struct {
		path string
		size int64
	}
	var found []candidate
	for _, root := range []string{"/usr/share/fonts", "/usr/local/share/fonts"} {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() || !info.Mode().IsRegular() {
				return nil //nolint:nilerr // an unreadable font dir just means "look elsewhere"
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".ttf", ".otf":
			default:
				return nil
			}
			if info.Size() > limit {
				return nil
			}
			found = append(found, candidate{path, info.Size()})
			return nil
		})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].size < found[j].size })

	var paths []string
	for _, c := range found {
		paths = append(paths, c.path)
	}
	return paths
}

// compileNaming compiles one document that names every family, through
// the server's OWN argument construction, and returns typst's output.
//
// Three things make this catch what its predecessor did not.
//
// The arguments are the production ones. The old probe shelled out to a
// bare `typst compile`, which answers "what does typst resolve by
// itself" — not "what does a compile by this server resolve", and those
// two are exactly what had drifted apart in #144.
//
// System fonts are ignored. collectFonts never lists a system-only
// family (every one of its probes passes --ignore-system-fonts), so a
// listed family resolving because the host happens to have it installed
// proves nothing about the image. Without this the test still passes
// with the fix reverted, because the seeded face is a copy of a system
// one: the bundled collection on the image sits outside fontconfig's
// search path, and this is how that is modelled off the image.
//
// And one document rather than one per family, because the warning
// names the family it could not find, so a single process localises any
// failure just as well.
func compileNaming(t *testing.T, r workspace.Resolver, families []string, extraFontPaths ...string) string {
	t.Helper()
	// Inside the workspace when there is one: a bounded resolver makes
	// the tenant root typst's --root, and a source file outside it is
	// refused before fonts are ever considered.
	dir := t.TempDir()
	if b, ok := r.(workspace.Bounded); ok {
		dir = b.WorkspaceRoot()
	}
	in := filepath.Join(dir, "probe.typ")
	out := filepath.Join(dir, "probe.pdf")

	var src strings.Builder
	src.WriteString("#set page(width: 12cm, height: auto)\n")
	for _, fam := range families {
		fmt.Fprintf(&src, "#text(font: %q)[%s]\n\n", fam, fam)
	}
	if err := os.WriteFile(in, []byte(src.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	args := typstArgs(r, in, out, extraFontPaths...)
	args = append([]string{args[0], "--ignore-system-fonts"}, args[1:]...)
	cmd := exec.Command("typst", args...)
	combined, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe compile failed: %s", combined)
	}
	return string(combined)
}

// The promise the tool makes: everything it lists actually resolves.
// The recurring failure in this area is the server saying one thing and
// typst doing another, so check the claim against a real compile — the
// server's real compile, with the collection present, which is the
// combination #144 slipped through.
func TestSearchFonts_EverythingListedActuallyResolves(t *testing.T) {
	requireTypst(t)
	bundled := seedBundledCollection(t)
	f, ctx := fileFixture(t)

	got := searchFonts(t, ctx, f, nil, "")
	names := familyNames(got)
	if len(names) == 0 {
		t.Fatal("nothing reported at all")
	}
	// The listing must actually exercise the bundled branch, or this
	// test passes by covering nothing.
	if !contains(names, bundled) {
		t.Fatalf("the seeded collection is not listed, so nothing here is proven: %v", names)
	}

	resolver, err := f.Resolver(identity.Identity{UserID: "gh:4242"})
	if err != nil {
		t.Fatal(err)
	}
	combined := compileNaming(t, resolver, names)
	if strings.Contains(strings.ToLower(combined), "unknown font family") {
		t.Errorf("search_fonts listed a family typst then substituted:\n%s", combined)
	}
}

// #144 in one assertion: a family the image ships, named by a document,
// must come out as itself. It was listed and never reachable, so a deck
// setting Inter rendered entirely in Libertinus Serif and said nothing.
func TestCompile_BundledFontIsUsable(t *testing.T) {
	requireTypst(t)
	bundled := seedBundledCollection(t)
	f, ctx := fileFixture(t)

	got := searchFonts(t, ctx, f, nil, strings.ToLower(bundled))
	var attributed string
	for _, entry := range got["fonts"].([]any) {
		if m := entry.(map[string]any); m["family"].(string) == bundled {
			attributed = m["source"].(string)
		}
	}
	if attributed == "" {
		t.Fatalf("a bundled family is not findable: %v", familyNames(got))
	}
	if attributed != "bundled" {
		t.Errorf("a bundled family is attributed to %q, want bundled", attributed)
	}

	resolver, err := f.Resolver(identity.Identity{UserID: "gh:4242"})
	if err != nil {
		t.Fatal(err)
	}
	combined := compileNaming(t, resolver, []string{bundled})
	if strings.Contains(strings.ToLower(combined), "unknown font family") {
		t.Errorf("bundled family %q was not resolved by a compile:\n%s", bundled, combined)
	}
}

// Every route that spawns typst gets the same font set, because they
// all go through one function. Named per site so a new compile path
// that builds its own command line fails here rather than shipping the
// #144 shape again.
func TestFontArgs_EveryCompileSeesTheBundledCollection(t *testing.T) {
	prev := bundledFontsPath
	bundledFontsPath = "/seeded/collection"
	defer func() { bundledFontsPath = prev }()

	for _, tc := range []struct {
		name string
		got  []string
	}{
		{"compile_typst_with_d2 and page rendering", typstArgs(nil, "in.typ", "out.pdf", "/a/view")},
		{"the publish check", append([]string{"compile"}, fontArgs("/a/data/home")...)},
		{"a compile with no other font path at all", typstArgs(nil, "in.typ", "out.pdf")},
	} {
		joined := strings.Join(tc.got, " ")
		if !strings.Contains(joined, "--font-path "+bundledFontsPath) {
			t.Errorf("%s does not name the bundled collection: %s", tc.name, joined)
		}
	}

	// Last, so a tenant's own face or a template's shadows a shipped
	// family of the same name rather than losing to it.
	args := fontArgs("/a/view")
	if got := args[len(args)-1]; got != bundledFontsPath {
		t.Errorf("bundled collection is not searched last: %v", args)
	}
}

// Nothing matching says so, and says what to do about it.
func TestSearchFonts_EmptyResultIsActionable(t *testing.T) {
	requireTypst(t)
	f, ctx := fileFixture(t)
	got := searchFonts(t, ctx, f, nil, "a-family-that-does-not-exist")
	if got["count"].(float64) != 0 {
		t.Fatalf("unexpected matches: %v", familyNames(got))
	}
	note, _ := got["note"].(string)
	if !strings.Contains(note, "fonts/") {
		t.Errorf("empty result does not say how to add one: %q", note)
	}
}

// #107's root cause is gone: workspace.DirName means a tenant path
// cannot contain the list separator, so nothing downstream has to
// compensate for one. What remains is the assertion that the guarantee
// holds — an unsafe path is refused loudly rather than quietly
// returning nothing, because silence was the actual harm.
func TestFontPaths_CannotContainTheListSeparator(t *testing.T) {
	root := t.TempDir()
	f := workspace.TenantFactory{Root: root}
	r, err := f.Resolver(identity.Identity{UserID: "gh:4242"})
	if err != nil {
		t.Fatal(err)
	}
	b, ok := r.(workspace.Bounded)
	if !ok {
		t.Fatal("tenant resolver is not bounded")
	}
	if !safeFontPath(b.WorkspaceRoot()) {
		t.Errorf("a tenant workspace root still contains the list separator: %s", b.WorkspaceRoot())
	}
	if !safeFontPath(workspaceFontPath(r)) && workspaceFontPath(r) != "" {
		t.Errorf("a tenant font path still contains the list separator: %s", workspaceFontPath(r))
	}

	// And the probe refuses an unsafe path rather than reporting no
	// fonts, which is what made the original undiagnosable.
	if got := familiesUnder("/tmp/has:colon"); got != nil {
		t.Errorf("an unsafe path was probed rather than refused: %v", got)
	}
}

// The manifest is a promise about what this image ships. A family it
// names that typst cannot resolve is the same species of lie as #107 —
// the server saying one thing and typst doing another — so the two are
// checked against each other rather than trusted.
func TestFontManifest_MatchesWhatIsInstalled(t *testing.T) {
	requireTypst(t)
	entries := loadFontManifest()
	if len(entries) == 0 {
		t.Skip("no bundled collection on this machine (manifest is installed by the image)")
	}
	resolved := map[string]bool{}
	for _, fam := range familiesUnder(bundledFontsPath) {
		resolved[strings.ToLower(fam)] = true
	}
	for key, e := range entries {
		if !resolved[key] {
			t.Errorf("manifest promises %q but typst does not resolve it", e.Family)
		}
		if e.License == "" {
			t.Errorf("%s has no recorded licence — the image is redistributed", e.Family)
		}
		if e.Origin == "" {
			t.Errorf("%s has no recorded origin", e.Family)
		}
	}
}

// The repo's manifest must be well-formed and licensed, whether or not
// the collection is installed on this machine.
func TestFontManifest_InRepoIsComplete(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "fonts", "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var entries []manifestEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("manifest is empty")
	}
	for _, e := range entries {
		if e.Family == "" || e.License == "" || e.Origin == "" {
			t.Errorf("incomplete entry: %+v", e)
		}
		// Redistribution is the constraint; record it per family so the
		// question stays answerable without archaeology.
		if !strings.Contains(e.License, "OFL") && !strings.Contains(e.License, "Apache") {
			t.Errorf("%s is licensed %q — check redistribution before shipping it",
				e.Family, e.License)
		}
	}
}
