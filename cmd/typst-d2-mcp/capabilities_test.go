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

	"github.com/dlouwers/typst-d2-mcp/internal/identity"
	"github.com/dlouwers/typst-d2-mcp/internal/workspace"
	"github.com/mark3labs/mcp-go/mcp"
)

func wsInfo(t *testing.T, ctx context.Context, factory workspace.Factory) workspaceInfo {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = "workspace_info"
	res, err := handleWorkspaceInfo(factory, nil)(ctx, req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("workspace_info failed: %s", resultText(res))
	}
	var info workspaceInfo
	if err := json.Unmarshal([]byte(resultText(res)), &info); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return info
}

// Nothing reported what this environment could do, so the only way to
// discover the font situation was to spend a compile on a probe
// document — and typst substitutes silently, so a caller who did not
// think to probe shipped a PDF with the wrong faces and no warning.
func TestWorkspaceInfo_ReportsCapabilities(t *testing.T) {
	requireTypst(t)

	root := t.TempDir()
	factory := workspace.TenantFactory{Root: root}
	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: "user123"})

	info := wsInfo(t, ctx, factory)

	if info.TypstVersion == "" {
		t.Error("typst_version not reported")
	}
	if _, err := exec.LookPath("d2"); err == nil && info.D2Version == "" {
		t.Error("d2_version not reported")
	}
	if len(info.FontFamilies) == 0 {
		t.Fatal("font_families not reported")
	}
	if info.FontsDir != FontsDir {
		t.Errorf("fonts_dir = %q, want %q", info.FontsDir, FontsDir)
	}
}

// The reason this issue exists: typst's bundled set has no proportional
// sans at all, so a document asking for one silently got a serif or a
// monospace. Guard the property rather than a package list, so the test
// still means something if the chosen faces change.
func TestRenderEnvironment_HasProportionalSans(t *testing.T) {
	requireTypst(t)

	families := fontFamilies("")
	if len(families) == 0 {
		t.Fatal("typst reported no font families at all")
	}

	for _, f := range families {
		lower := strings.ToLower(f)
		if strings.Contains(lower, "mono") || strings.Contains(lower, "math") {
			continue
		}
		if strings.Contains(lower, "sans") {
			return // found one
		}
	}
	t.Skipf("no proportional sans in this environment (have %v); "+
		"the runtime image installs one, this host may not", families)
}

// A tenant's own typefaces have to reach the compiler, or an
// organisation template cannot use the organisation's typeface.
func TestTypstArgs_IncludesWorkspaceFontPath(t *testing.T) {
	root := t.TempDir()
	tenant := filepath.Join(root, "user123")
	if err := os.MkdirAll(tenant, 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := workspace.NewScopedFS(tenant)
	if err != nil {
		t.Fatal(err)
	}

	// Absent is the normal case: no flag, no error.
	if got := strings.Join(typstArgs(r, "in.typ", "out.pdf"), " "); strings.Contains(got, "--font-path") {
		t.Errorf("font path passed for a workspace with no fonts dir: %s", got)
	}

	if err := os.MkdirAll(filepath.Join(tenant, FontsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(typstArgs(r, "in.typ", "out.pdf"), " ")
	want := "--font-path " + filepath.Join(tenant, FontsDir)
	if !strings.Contains(got, want) {
		t.Errorf("args = %s, want it to contain %s", got, want)
	}
}

// One tenant's fonts must not be visible to another. The font path is
// derived from the resolver's own root, so this is a property of the
// construction rather than of a check somewhere.
func TestWorkspaceFontPath_IsPerTenant(t *testing.T) {
	root := t.TempDir()
	factory := workspace.TenantFactory{Root: root}

	mine, err := factory.Resolver(identity.Identity{UserID: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := factory.Resolver(identity.Identity{UserID: "theirs"})
	if err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(root, "theirs", FontsDir), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := workspaceFontPath(mine); got != "" {
		t.Errorf("one tenant saw another's font directory: %s", got)
	}
	if got := workspaceFontPath(theirs); got == "" {
		t.Error("the owning tenant did not see its own font directory")
	}
}

// stdio mode has no bounded workspace; the user's system fonts are
// already their own, so there is nothing to add and nothing to report.
func TestWorkspaceFontPath_UnboundedIsEmpty(t *testing.T) {
	if got := workspaceFontPath(workspace.LocalFS{}); got != "" {
		t.Errorf("font path in local mode = %q, want empty", got)
	}
}

// End to end: push a font, name it, compile. The warning path matters
// as much as the success — typst exits 0 on an unknown family, and
// those warnings are the only signal a caller gets.
func TestCompile_WorkspaceFontIsUsable(t *testing.T) {
	requireTypst(t)

	src := findSystemFont(t)
	root := t.TempDir()
	factory := workspace.TenantFactory{Root: root}
	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: "user123"})

	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = "put_file"
	req.Params.Arguments = map[string]any{
		"path":     FontsDir + "/" + filepath.Base(src),
		"content":  base64.StdEncoding.EncodeToString(raw),
		"encoding": "base64",
	}
	res, err := handlePutFile(factory, nil)(ctx, req)
	if err != nil {
		t.Fatalf("put_file handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("put_file font: %s", resultText(res))
	}

	resolver, err := factory.Resolver(identity.Identity{UserID: "user123"})
	if err != nil {
		t.Fatal(err)
	}
	fontPath := workspaceFontPath(resolver)
	if fontPath == "" {
		t.Fatal("pushing into fonts/ did not produce a font path")
	}

	// Ask typst what the pushed file alone provides, with system and
	// embedded fonts excluded. Comparing against the full list would
	// prove nothing when the face is also installed system-wide, which
	// it usually is on a machine with any fonts at all.
	own := familiesFromPathOnly(t, fontPath)
	if len(own) == 0 {
		t.Fatalf("typst read no families from the pushed font at %s", fontPath)
	}

	// And the workspace's full list must include them.
	all := fontFamilies(fontPath)
	for _, want := range own {
		if !contains(all, want) {
			t.Errorf("family %q from the pushed font is missing from the workspace list %v", want, all)
		}
	}

	// Finally, a document setting that family must compile without an
	// unknown-family warning.
	if res := putFile(t, ctx, factory, "doc.typ",
		"#set text(font: \""+own[0]+"\")\n= Title\nBody.\n"); res.IsError {
		t.Fatalf("put_file doc: %s", resultText(res))
	}
	res = compile(t, ctx, factory, "doc.typ")
	if res.IsError {
		t.Fatalf("compile failed: %s", resultText(res))
	}
	if got := resultText(res); strings.Contains(strings.ToLower(got), "unknown font family") {
		t.Errorf("pushed font %q was not resolved:\n%s", own[0], got)
	}
}

// familiesFromPathOnly lists the families a directory provides, with
// system and typst-embedded fonts excluded.
func familiesFromPathOnly(t *testing.T, dir string) []string {
	t.Helper()
	out, err := exec.Command("typst", "fonts",
		"--font-path", dir,
		"--ignore-system-fonts",
		"--ignore-embedded-fonts",
	).Output()
	if err != nil {
		t.Skipf("typst fonts --ignore-*-fonts unavailable: %v", err)
	}
	var families []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			families = append(families, line)
		}
	}
	return families
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// An unknown family must still surface as a warning on a successful
// compile. That behaviour is what made diagnosing the font situation
// possible at all, and is worth pinning.
func TestCompile_UnknownFontStillWarns(t *testing.T) {
	requireTypst(t)

	root := t.TempDir()
	factory := workspace.TenantFactory{Root: root}
	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: "user123"})

	if res := putFile(t, ctx, factory, "doc.typ",
		"#set text(font: \"No Such Family At All\")\n= Title\n"); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	res := compile(t, ctx, factory, "doc.typ")
	if res.IsError {
		t.Fatalf("compile failed: %s", resultText(res))
	}
	if got := resultText(res); !strings.Contains(strings.ToLower(got), "unknown font family") {
		t.Errorf("no unknown-font warning surfaced:\n%s", got)
	}
}

// findSystemFont picks any font file on the host to stand in for a
// tenant's own face.
func findSystemFont(t *testing.T) string {
	t.Helper()
	roots := []string{"/usr/share/fonts", "/usr/local/share/fonts"}
	var found string
	for _, root := range roots {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || found != "" || info == nil || info.IsDir() {
				return nil //nolint:nilerr // an unreadable font dir just means "look elsewhere"
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".ttf", ".otf":
				found = path
			}
			return nil
		})
		if found != "" {
			return found
		}
	}
	t.Skip("no system font file to use as a stand-in")
	return ""
}
