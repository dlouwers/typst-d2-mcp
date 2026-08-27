package main

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dlouwers/typst-d2-mcp/internal/identity"
	"github.com/dlouwers/typst-d2-mcp/internal/workspace"
	"github.com/mark3labs/mcp-go/mcp"
)

// compile invokes the compile_typst_with_d2 handler and returns the raw
// result for inspection.
func compile(t *testing.T, ctx context.Context, factory workspace.Factory, path string) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = "compile_typst_with_d2"
	req.Params.Arguments = map[string]any{"file_path": path}
	res, err := handleCompileTypst(factory, nil)(ctx, req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return res
}

func resultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// A tiny standalone SVG — enough for typst to embed without pulling in
// anything external.
const testSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="20" height="20">` +
	`<rect width="20" height="20" fill="#006566"/></svg>`

const testPNG = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89" +
	"\x00\x00\x00\nIDATx\x9cc\x00\x01\x00\x00\x05\x00\x01\r\n-\xb4\x00\x00\x00\x00IEND\xaeB`\x82"

// The bug in #91: a file pushed with put_file could not be referenced
// from the document compiled against it, because the source was staged
// in /tmp and every relative path resolved from there.
func TestCompile_WorkspaceAssetIsReferenceable(t *testing.T) {
	requireTypst(t)

	root := t.TempDir()
	factory := workspace.TenantFactory{Root: root}
	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: "user123"})

	if res := putFile(t, ctx, factory, "asset.svg", testSVG); res.IsError {
		t.Fatalf("put_file svg: %s", resultText(res))
	}
	if res := putFile(t, ctx, factory, "doc.typ",
		"= Title\n\n#image(\"asset.svg\", width: 20mm)\n"); res.IsError {
		t.Fatalf("put_file typ: %s", resultText(res))
	}

	res := compile(t, ctx, factory, "doc.typ")
	if res.IsError {
		t.Fatalf("compile failed: %s", resultText(res))
	}
	assertPDF(t, filepath.Join(root, workspace.DirName("user123"), "doc.pdf"))
}

// Same for a raster asset, which travels through put_file as base64.
func TestCompile_WorkspaceRasterAssetIsReferenceable(t *testing.T) {
	requireTypst(t)

	root := t.TempDir()
	factory := workspace.TenantFactory{Root: root}
	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: "user123"})

	req := mcp.CallToolRequest{}
	req.Params.Name = "put_file"
	req.Params.Arguments = map[string]any{
		"path":     "dot.png",
		"content":  base64.StdEncoding.EncodeToString([]byte(testPNG)),
		"encoding": "base64",
	}
	res, err := handlePutFile(factory, nil)(ctx, req)
	if err != nil {
		t.Fatalf("put_file handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("put_file png: %s", resultText(res))
	}

	if res := putFile(t, ctx, factory, "doc.typ",
		"#image(\"dot.png\", width: 5mm)\n"); res.IsError {
		t.Fatalf("put_file typ: %s", resultText(res))
	}

	if res := compile(t, ctx, factory, "doc.typ"); res.IsError {
		t.Fatalf("compile failed: %s", resultText(res))
	}
	assertPDF(t, filepath.Join(root, workspace.DirName("user123"), "doc.pdf"))
}

// A relative #import of another workspace file must resolve too — the
// same defect, one construct over.
func TestCompile_WorkspaceImportResolves(t *testing.T) {
	requireTypst(t)

	root := t.TempDir()
	factory := workspace.TenantFactory{Root: root}
	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: "user123"})

	if res := putFile(t, ctx, factory, "style.typ",
		"#let accent = rgb(\"#006566\")\n"); res.IsError {
		t.Fatalf("put_file style: %s", resultText(res))
	}
	if res := putFile(t, ctx, factory, "doc.typ",
		"#import \"style.typ\": accent\n#text(fill: accent)[Coloured.]\n"); res.IsError {
		t.Fatalf("put_file doc: %s", resultText(res))
	}

	if res := compile(t, ctx, factory, "doc.typ"); res.IsError {
		t.Fatalf("compile failed: %s", resultText(res))
	}
	assertPDF(t, filepath.Join(root, workspace.DirName("user123"), "doc.pdf"))
}

// An asset in a subdirectory, referenced both relatively and from the
// workspace root. The root form only works because compile passes
// --root for a bounded workspace.
func TestCompile_AssetInSubdirectory(t *testing.T) {
	requireTypst(t)

	root := t.TempDir()
	factory := workspace.TenantFactory{Root: root}
	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: "user123"})

	if res := putFile(t, ctx, factory, "assets/logo.svg", testSVG); res.IsError {
		t.Fatalf("put_file asset: %s", resultText(res))
	}
	if res := putFile(t, ctx, factory, "docs/doc.typ",
		"#image(\"../assets/logo.svg\", width: 10mm)\n"+
			"#image(\"/assets/logo.svg\", width: 10mm)\n"); res.IsError {
		t.Fatalf("put_file doc: %s", resultText(res))
	}

	if res := compile(t, ctx, factory, "docs/doc.typ"); res.IsError {
		t.Fatalf("compile failed: %s", resultText(res))
	}
	assertPDF(t, filepath.Join(root, workspace.DirName("user123"), "docs", "doc.pdf"))
}

// Moving the compile root into the workspace must not open a way out of
// it. typst is given --root at the tenant directory, so a document that
// reaches above it is refused by typst itself.
func TestCompile_CannotReadOutsideWorkspace(t *testing.T) {
	requireTypst(t)

	root := t.TempDir()
	secret := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(secret, []byte("not yours"), 0o600); err != nil {
		t.Fatal(err)
	}

	factory := workspace.TenantFactory{Root: root}
	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: "user123"})

	if res := putFile(t, ctx, factory, "doc.typ",
		"#read(\"../secret.txt\")\n"); res.IsError {
		t.Fatalf("put_file doc: %s", resultText(res))
	}

	res := compile(t, ctx, factory, "doc.typ")
	if !res.IsError {
		t.Fatal("expected the compile to fail; a document escaped its workspace")
	}
	if got := resultText(res); strings.Contains(got, "not yours") {
		t.Errorf("secret contents leaked into the error: %s", got)
	}
}

// One tenant's document must not see another tenant's files, by any
// spelling of the path.
func TestCompile_CannotReadAnotherTenant(t *testing.T) {
	requireTypst(t)

	root := t.TempDir()
	factory := workspace.TenantFactory{Root: root}

	otherCtx := identity.WithIdentity(context.Background(), identity.Identity{UserID: "other"})
	if res := putFile(t, otherCtx, factory, "private.txt", "other tenant data"); res.IsError {
		t.Fatalf("put_file other: %s", resultText(res))
	}

	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: "user123"})
	for _, ref := range []string{"../other/private.txt", "/../other/private.txt"} {
		if res := putFile(t, ctx, factory, "doc.typ",
			"#read(\""+ref+"\")\n"); res.IsError {
			t.Fatalf("put_file doc: %s", resultText(res))
		}
		res := compile(t, ctx, factory, "doc.typ")
		if !res.IsError {
			t.Fatalf("%s: expected failure; one tenant read another's file", ref)
		}
		if got := resultText(res); strings.Contains(got, "other tenant data") {
			t.Errorf("%s: another tenant's contents leaked: %s", ref, got)
		}
	}
}

// put_file itself still rejects traversal in the destination path; the
// staging change must not have loosened it.
func TestPutFile_TraversalStillRejected(t *testing.T) {
	root := t.TempDir()
	factory := workspace.TenantFactory{Root: root}
	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: "user123"})

	for _, p := range []string{"../escape.txt", "../../etc/passwd", "/etc/passwd"} {
		if res := putFile(t, ctx, factory, p, "x"); !res.IsError {
			t.Errorf("put_file %q was accepted", p)
		}
	}
}

// Preprocessing must still happen — the staging move is about where the
// file lands, not what is in it.
func TestCompile_D2StillPreprocessed(t *testing.T) {
	requireTypst(t)
	if _, err := exec.LookPath("d2"); err != nil {
		t.Skip("d2 not installed")
	}

	root := t.TempDir()
	factory := workspace.TenantFactory{Root: root}
	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: "user123"})

	if res := putFile(t, ctx, factory, "doc.typ",
		"= Diagram\n\n#d2(layout: \"elk\", theme: \"0\")[\n  a -> b\n]\n"); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	if res := compile(t, ctx, factory, "doc.typ"); res.IsError {
		t.Fatalf("compile failed: %s", resultText(res))
	}
	assertPDF(t, filepath.Join(root, workspace.DirName("user123"), "doc.pdf"))
}

// The staged source is server scratch: it must be gone once the compile
// returns, and must never have counted towards the tenant's usage.
func TestCompile_StagedFileIsCleanedUp(t *testing.T) {
	requireTypst(t)

	root := t.TempDir()
	factory := workspace.TenantFactory{Root: root}
	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: "user123"})

	if res := putFile(t, ctx, factory, "doc.typ", "= Title\n"); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	if res := compile(t, ctx, factory, "doc.typ"); res.IsError {
		t.Fatalf("compile failed: %s", resultText(res))
	}

	entries, err := os.ReadDir(filepath.Join(root, workspace.DirName("user123")))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), workspace.StagePrefix) {
			t.Errorf("staged file left behind: %s", e.Name())
		}
	}
}

func requireTypst(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("typst"); err != nil {
		t.Skip("typst not installed")
	}
}

func assertPDF(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("no PDF at %s: %v", path, err)
	}
	if info.Size() == 0 {
		t.Fatalf("empty PDF at %s", path)
	}
}
