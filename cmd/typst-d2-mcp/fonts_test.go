package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

// The promise the tool makes: everything it lists actually resolves.
// The recurring failure in this area is the server saying one thing and
// typst doing another, so check the claim against a real compile.
func TestSearchFonts_EverythingListedActuallyResolves(t *testing.T) {
	requireTypst(t)
	f, ctx := fileFixture(t)

	got := searchFonts(t, ctx, f, nil, "")
	names := familyNames(got)
	if len(names) == 0 {
		t.Fatal("nothing reported at all")
	}

	dir := t.TempDir()
	for _, family := range names {
		in := filepath.Join(dir, "probe.typ")
		out := filepath.Join(dir, "probe.pdf")
		src := "#set text(font: \"" + family + "\")\nProbe.\n"
		if err := os.WriteFile(in, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("typst", "compile", in, out)
		combined, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("%s: reported but does not compile: %s", family, combined)
			continue
		}
		if strings.Contains(strings.ToLower(string(combined)), "unknown font family") {
			t.Errorf("%s: reported available but typst substituted it", family)
		}
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

// #107 came back in new code, and this is the guard. A tenant path
// contains a colon; typst splits --font-path on it and reads nothing,
// so probing a tenant's font directory directly reported that they had
// no fonts. Caught by a skipping test, which is exactly how the
// original hid for a day.
func TestFamiliesUnder_SurvivesAColonInThePath(t *testing.T) {
	requireTypst(t)
	src := findSystemFont(t)

	root := t.TempDir()
	colon := filepath.Join(root, "gh:4242", "fonts")
	if err := os.MkdirAll(colon, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(colon, filepath.Base(src)), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	if got := familiesUnder(colon); len(got) == 0 {
		t.Error("a font directory whose path contains a colon reported no families")
	}

	plain := filepath.Join(root, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plain, filepath.Base(src)), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if a, b := familiesUnder(colon), familiesUnder(plain); len(a) != len(b) {
		t.Errorf("colon path found %d families, colon-free found %d — must agree", len(a), len(b))
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
