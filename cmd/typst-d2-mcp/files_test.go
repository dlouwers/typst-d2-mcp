package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/dlouwers/typst-d2-mcp/internal/identity"
	"github.com/dlouwers/typst-d2-mcp/internal/workspace"
	"github.com/mark3labs/mcp-go/mcp"
)

func fileFixture(t *testing.T) (workspace.Factory, context.Context) {
	t.Helper()
	return workspace.TenantFactory{Root: t.TempDir()},
		identity.WithIdentity(context.Background(), identity.Identity{UserID: "gh:4242"})
}

func callTool(t *testing.T, h func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error),
	ctx context.Context, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	res, err := h(ctx, req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return res
}

// The point of get_file: edit what you wrote without re-uploading it.
func TestGetFile_RoundTripsText(t *testing.T) {
	f, ctx := fileFixture(t)
	const body = "= Title\n\nBody text with a línea and an emoji 🌊.\n"
	if res := putFile(t, ctx, f, "doc.typ", body); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	got := callTool(t, handleGetFile(f), ctx, map[string]any{"path": "doc.typ"})
	if got.IsError {
		t.Fatalf("get_file: %s", resultText(got))
	}
	if resultText(got) != body {
		t.Errorf("round trip changed the content:\n%q", resultText(got))
	}
}

// Bytes you cannot read are not an answer, so binary is opt-in.
func TestGetFile_BinaryNeedsBase64(t *testing.T) {
	f, ctx := fileFixture(t)
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"path": "dot.png", "content": base64.StdEncoding.EncodeToString([]byte(testPNG)),
		"encoding": "base64",
	}
	if res, err := handlePutFile(f, nil)(ctx, req); err != nil || res.IsError {
		t.Fatalf("put_file: %v", err)
	}

	plain := callTool(t, handleGetFile(f), ctx, map[string]any{"path": "dot.png"})
	if !plain.IsError {
		t.Error("binary returned as text without being asked for")
	}
	if !strings.Contains(resultText(plain), "base64") {
		t.Errorf("refusal does not say how to get the bytes: %s", resultText(plain))
	}

	b64 := callTool(t, handleGetFile(f), ctx, map[string]any{"path": "dot.png", "encoding": "base64"})
	if b64.IsError {
		t.Fatalf("get_file base64: %s", resultText(b64))
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(resultText(b64)))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != testPNG {
		t.Error("binary did not survive the round trip")
	}
}

// Artefacts belong on the resource side whatever their size — the
// client streams them to a file, so they cost the caller no context.
// Refusing only when a file happens to be large would send a small PDF
// through the worse mechanism for no reason.
func TestGetFile_ArtefactsGoToResourcesRegardlessOfSize(t *testing.T) {
	f, ctx := fileFixture(t)
	if res := putFile(t, ctx, f, "tiny.pdf", "not really a pdf"); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	got := callTool(t, handleGetFile(f), ctx, map[string]any{"path": "tiny.pdf"})
	if !got.IsError {
		t.Fatal("a small PDF was returned inline rather than pointed at a resource")
	}
	msg := resultText(got)
	for _, want := range []string{"resources/read", pdfURIPrefix, pageURIPrefix} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "at most") {
		t.Errorf("refusal justified by size rather than purpose:\n%s", msg)
	}
}

// Source over the cap is still a size refusal, and points at the
// source resource rather than at a PDF.
func TestGetFile_LargeSourcePointsAtTheSourceResource(t *testing.T) {
	f, ctx := fileFixture(t)
	if res := putFile(t, ctx, f, "big.typ", strings.Repeat("x", maxGetFileBytes+1)); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	got := callTool(t, handleGetFile(f), ctx, map[string]any{"path": "big.typ"})
	if !got.IsError {
		t.Fatal("a file over the cap was returned inline")
	}
	if !strings.Contains(resultText(got), sourceURIPrefix) {
		t.Errorf("refusal does not point at the source resource: %s", resultText(got))
	}
}

// Reclaiming space is the whole point — an agent's probe files sat in a
// workspace for a whole session with no way to remove them.
func TestDeleteFile_ReclaimsBytes(t *testing.T) {
	f, ctx := fileFixture(t)
	if res := putFile(t, ctx, f, "scratch/t1.typ", strings.Repeat("x", 500)); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	resolver, err := f.Resolver(identity.Identity{UserID: "gh:4242"})
	if err != nil {
		t.Fatal(err)
	}
	before, _, _ := workspace.Usage(resolver)

	got := callTool(t, handleDeleteFile(f), ctx, map[string]any{"path": "scratch/t1.typ"})
	if got.IsError {
		t.Fatalf("delete_file: %s", resultText(got))
	}
	after, _, _ := workspace.Usage(resolver)
	if after >= before {
		t.Errorf("usage did not drop: %d then %d", before, after)
	}

	if again := callTool(t, handleDeleteFile(f), ctx, map[string]any{"path": "scratch/t1.typ"}); !again.IsError {
		t.Error("deleting a missing file succeeded")
	}
}

// Deletion must not become a way out of the workspace, nor a way to
// remove a directory by accident.
func TestDeleteFile_Bounded(t *testing.T) {
	f, ctx := fileFixture(t)
	if res := putFile(t, ctx, f, "docs/a.typ", "x"); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	for _, p := range []string{"../escape.txt", "../../etc/passwd", "/etc/passwd"} {
		if res := callTool(t, handleDeleteFile(f), ctx, map[string]any{"path": p}); !res.IsError {
			t.Errorf("delete escaped the workspace: %q", p)
		}
	}
	if res := callTool(t, handleDeleteFile(f), ctx, map[string]any{"path": "docs"}); !res.IsError {
		t.Error("deleted a directory")
	}
}

func decodeSearch(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	if res.IsError {
		t.Fatalf("search_file: %s", resultText(res))
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(resultText(res)), &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, resultText(res))
	}
	return out
}

func TestSearchFile_ByNameAndContent(t *testing.T) {
	f, ctx := fileFixture(t)
	for path, body := range map[string]string{
		"reports/q3-review.typ": "= Q3 Review\nStormlantern engagement.\n",
		"reports/q4-plan.typ":   "= Q4 Plan\nNothing notable.\n",
		"notes/scratch.md":      "Stormlantern colours\n",
	} {
		if res := putFile(t, ctx, f, path, body); res.IsError {
			t.Fatalf("put_file %s: %s", path, resultText(res))
		}
	}

	all := decodeSearch(t, callTool(t, handleSearchFile(f), ctx, map[string]any{}))
	if n := int(all["count"].(float64)); n != 3 {
		t.Errorf("bare search found %d, want 3", n)
	}

	byName := decodeSearch(t, callTool(t, handleSearchFile(f), ctx, map[string]any{"name": "reports/"}))
	if n := int(byName["count"].(float64)); n != 2 {
		t.Errorf("name search found %d, want 2", n)
	}

	byContent := decodeSearch(t, callTool(t, handleSearchFile(f), ctx, map[string]any{"contains": "Stormlantern"}))
	if n := int(byContent["count"].(float64)); n != 2 {
		t.Errorf("content search found %d, want 2", n)
	}
	m := byContent["matches"].([]any)[0].(map[string]any)
	if line, _ := m["line"].(string); line == "" {
		t.Error("content match does not show the matching line")
	}

	both := decodeSearch(t, callTool(t, handleSearchFile(f), ctx,
		map[string]any{"name": ".typ", "contains": "Stormlantern"}))
	if n := int(both["count"].(float64)); n != 1 {
		t.Errorf("combined search found %d, want 1", n)
	}

	none := decodeSearch(t, callTool(t, handleSearchFile(f), ctx, map[string]any{"name": "nothing-like-this"}))
	if none["note"] == nil {
		t.Error("an empty result says nothing about why")
	}
}

// Server scratch is not the caller's business, and a byte sequence
// inside a PDF is not a content match.
func TestSearchFile_SkipsScratchAndBinary(t *testing.T) {
	f, ctx := fileFixture(t)
	if res := putFile(t, ctx, f, workspace.StagePrefix+"leftover.typ", "Stormlantern"); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"path": "img.png", "content": base64.StdEncoding.EncodeToString([]byte(testPNG)),
		"encoding": "base64",
	}
	if _, err := handlePutFile(f, nil)(ctx, req); err != nil {
		t.Fatal(err)
	}

	got := decodeSearch(t, callTool(t, handleSearchFile(f), ctx, map[string]any{}))
	for _, m := range got["matches"].([]any) {
		if p := m.(map[string]any)["path"].(string); strings.HasPrefix(p, workspace.StagePrefix) {
			t.Errorf("server scratch surfaced in a search: %s", p)
		}
	}
	binary := decodeSearch(t, callTool(t, handleSearchFile(f), ctx, map[string]any{"contains": "PNG"}))
	if n := int(binary["count"].(float64)); n != 0 {
		t.Errorf("content search matched inside a binary file (%d hits)", n)
	}
}

// One tenant's workspace is not another's.
func TestFileVerbs_ArePerTenant(t *testing.T) {
	f, ctx := fileFixture(t)
	if res := putFile(t, ctx, f, "mine.typ", "secret"); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	other := identity.WithIdentity(context.Background(), identity.Identity{UserID: "gh:9999"})

	if res := callTool(t, handleGetFile(f), other, map[string]any{"path": "mine.typ"}); !res.IsError {
		t.Error("another tenant read the file")
	}
	if res := callTool(t, handleDeleteFile(f), other, map[string]any{"path": "mine.typ"}); !res.IsError {
		t.Error("another tenant deleted the file")
	}
	got := decodeSearch(t, callTool(t, handleSearchFile(f), other, map[string]any{}))
	if n := int(got["count"].(float64)); n != 0 {
		t.Errorf("another tenant's search returned %d of my files", n)
	}
}

// A PNG the caller pushed is their asset, not a rendered artefact.
// Refusing it would be telling someone their own logo belongs on the
// resource side.
func TestGetFile_UploadedImagesAreNotArtefacts(t *testing.T) {
	if uri, isArtefact := artefactURI("assets/logo.png"); isArtefact {
		t.Errorf("an uploaded PNG was classed as an artefact (%s)", uri)
	}
	if _, isArtefact := artefactURI("docs/" + previewDir + "/report/page-1.png"); !isArtefact {
		t.Error("a rendered page was not classed as an artefact")
	}
	if _, isArtefact := artefactURI("report.pdf"); !isArtefact {
		t.Error("a PDF was not classed as an artefact")
	}
}

// One tool, one matching rule. `name` was case-insensitive and
// `contains` was not, so the same search behaved differently depending
// which half you used.
func TestSearchFile_ContentMatchIsCaseInsensitive(t *testing.T) {
	f, ctx := fileFixture(t)
	if res := putFile(t, ctx, f, "doc.typ", "The Stormlantern engagement\n"); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	for _, q := range []string{"stormlantern", "STORMLANTERN", "StOrMlAnTeRn"} {
		got := decodeSearch(t, callTool(t, handleSearchFile(f), ctx, map[string]any{"contains": q}))
		if n := int(got["count"].(float64)); n != 1 {
			t.Errorf("contains %q found %d, want 1", q, n)
		}
	}
}

// A file too large to scan must be reported as unsearched, not quietly
// treated as a non-match — an answer that looks complete and is not is
// the failure this tool exists to avoid.
func TestSearchFile_ReportsWhatItCouldNotSearch(t *testing.T) {
	f, ctx := fileFixture(t)
	// A file this large can only be built by chunked append in normal
	// use; raise the per-call cap so the fixture is one write.
	t.Setenv(envMaxInputBytes, "8388608")
	big := strings.Repeat("padding line\n", (maxSearchBytes/13)+64)
	if res := putFile(t, ctx, f, "huge.typ", big); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	if res := putFile(t, ctx, f, "small.typ", "findme\n"); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}

	got := decodeSearch(t, callTool(t, handleSearchFile(f), ctx, map[string]any{"contains": "findme"}))
	skipped, ok := got["not_searched"].([]any)
	if !ok || len(skipped) != 1 {
		t.Fatalf("a file too large to scan was not reported: %v", got)
	}
	if skipped[0].(string) != "huge.typ" {
		t.Errorf("reported %v as unsearched, want huge.typ", skipped[0])
	}
	if int(got["count"].(float64)) != 1 {
		t.Errorf("the searchable file was not matched: %v", got)
	}
}

// A binary file is a genuine non-match, not something we failed to
// search — it must not appear in not_searched.
func TestSearchFile_BinaryIsANonMatchNotASkip(t *testing.T) {
	f, ctx := fileFixture(t)
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"path": "img.png", "content": base64.StdEncoding.EncodeToString([]byte(testPNG)),
		"encoding": "base64",
	}
	if _, err := handlePutFile(f, nil)(ctx, req); err != nil {
		t.Fatal(err)
	}
	got := decodeSearch(t, callTool(t, handleSearchFile(f), ctx, map[string]any{"contains": "PNG"}))
	if got["not_searched"] != nil {
		t.Errorf("a binary file was reported as unsearched: %v", got["not_searched"])
	}
	if int(got["count"].(float64)) != 0 {
		t.Errorf("a binary file matched a content search")
	}
}

// Deleting a document must take its rendered pages with it. An agent
// deleted four probe documents, tried to tidy up, and left ten orphaned
// previews charged to its byte budget in a directory it was never told
// about.
func TestDeleteFile_TakesRenderedPagesWithIt(t *testing.T) {
	requireTypst(t)
	f, ctx := fileFixture(t)
	if res := putFile(t, ctx, f, "doc.typ", "= One\n#pagebreak()\n= Two\n"); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	// Render, the way a caller does — by reading a page.
	req := mcp.ReadResourceRequest{}
	req.Params.URI = pageURIPrefix + "doc.typ/1"
	if _, err := handleReadPage(f, nil)(ctx, req); err != nil {
		t.Fatalf("render pages: %v", err)
	}

	resolver, err := f.Resolver(identity.Identity{UserID: "gh:4242"})
	if err != nil {
		t.Fatal(err)
	}
	withPreviews, _, _ := workspace.Usage(resolver)

	got := callTool(t, handleDeleteFile(f), ctx, map[string]any{"path": "doc.typ"})
	if got.IsError {
		t.Fatalf("delete_file: %s", resultText(got))
	}
	if !strings.Contains(resultText(got), "rendered page") {
		t.Errorf("deletion did not mention the pages it removed: %s", resultText(got))
	}

	after, _, _ := workspace.Usage(resolver)
	if after >= withPreviews {
		t.Errorf("usage did not drop: %d then %d", withPreviews, after)
	}
	dir, err := resolver.Resolve(previewDirFor("doc.typ"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("the preview directory survived the document")
	}
}

// Deleting the PDF leaves the source, and pages rendered from that
// source are still current — so they must NOT be swept away.
func TestDeleteFile_PDFDeletionKeepsPages(t *testing.T) {
	requireTypst(t)
	f, ctx := fileFixture(t)
	if res := putFile(t, ctx, f, "doc.typ", "= One\n"); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	if res := compile(t, ctx, f, "doc.typ"); res.IsError {
		t.Fatalf("compile: %s", resultText(res))
	}
	req := mcp.ReadResourceRequest{}
	req.Params.URI = pageURIPrefix + "doc.typ/1"
	if _, err := handleReadPage(f, nil)(ctx, req); err != nil {
		t.Fatalf("render: %v", err)
	}

	if res := callTool(t, handleDeleteFile(f), ctx, map[string]any{"path": "doc.pdf"}); res.IsError {
		t.Fatalf("delete pdf: %s", resultText(res))
	}
	resolver, _ := f.Resolver(identity.Identity{UserID: "gh:4242"})
	dir, _ := resolver.Resolve(previewDirFor("doc.typ"))
	if _, err := os.Stat(dir); err != nil {
		t.Error("deleting the PDF removed pages that are still current")
	}
}

// A document that shrinks must not keep a preview for a page it no
// longer has — charged to the budget, and readable as content the
// document does not contain.
func TestPageResource_ShrinkingADocumentDropsStalePages(t *testing.T) {
	requireTypst(t)
	f, ctx := fileFixture(t)
	if res := putFile(t, ctx, f, "doc.typ", "= One\n#pagebreak()\n= Two\n#pagebreak()\n= Three\n"); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	req := mcp.ReadResourceRequest{}
	req.Params.URI = pageURIPrefix + "doc.typ/3"
	if _, err := handleReadPage(f, nil)(ctx, req); err != nil {
		t.Fatalf("render three pages: %v", err)
	}

	// Now it is one page.
	if res := putFile(t, ctx, f, "doc.typ", "= Only one\n"); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	req.Params.URI = pageURIPrefix + "doc.typ/1"
	if _, err := handleReadPage(f, nil)(ctx, req); err != nil {
		t.Fatalf("re-render: %v", err)
	}

	resolver, _ := f.Resolver(identity.Identity{UserID: "gh:4242"})
	dir, _ := resolver.Resolve(previewDirFor("doc.typ"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("previews after shrinking: %v, want only page-1", names)
	}
}
