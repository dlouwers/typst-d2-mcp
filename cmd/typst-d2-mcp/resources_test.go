package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	"github.com/mark3labs/mcp-go/server"
)

func readResource(t *testing.T, h server.ResourceTemplateHandlerFunc, ctx context.Context, uri string) []mcp.ResourceContents {
	t.Helper()
	req := mcp.ReadResourceRequest{}
	req.Params.URI = uri
	got, err := h(ctx, req)
	if err != nil {
		t.Fatalf("read %s: %v", uri, err)
	}
	return got
}

// An agent asking what it could read was told "No resources found"
// while holding a working typst-d2:// URI, because only templates were
// registered and templates are not what resources/list returns.
func TestIndexResource_NamesWhatIsReadable(t *testing.T) {
	f, ctx := fileFixture(t)
	if res := putFile(t, ctx, f, "docs/report.typ", "= Report\nBody.\n"); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	if res := putFile(t, ctx, f, "docs/report.pdf", "fake pdf"); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}

	req := mcp.ReadResourceRequest{}
	req.Params.URI = indexURI
	got, err := handleReadIndex(f)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	text := got[0].(mcp.TextResourceContents).Text

	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("index is not JSON: %v", err)
	}
	docs := out["documents"].([]any)
	if len(docs) != 1 {
		t.Fatalf("index listed %d documents, want 1", len(docs))
	}
	d := docs[0].(map[string]any)
	for k, want := range map[string]string{
		"source": sourceURIPrefix + "docs/report.typ",
		"pdf":    pdfURIPrefix + "docs/report.pdf",
		"pages":  pageURIPrefix + "docs/report.typ/1",
	} {
		if got, _ := d[k].(string); got != want {
			t.Errorf("index %s = %q, want %q", k, got, want)
		}
	}
}

func TestSourceResource_ReturnsText(t *testing.T) {
	f, ctx := fileFixture(t)
	const body = "= Title\nBody with an accent: café.\n"
	if res := putFile(t, ctx, f, "doc.typ", body); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	got := readResource(t, handleReadSource(f), ctx, sourceURIPrefix+"doc.typ")
	if text := got[0].(mcp.TextResourceContents).Text; text != body {
		t.Errorf("source resource returned %q", text)
	}
}

// Pages render on first read, are cached, and are re-rendered when the
// source changes — a stale preview must never be served as current.
func TestPageResource_RendersLazilyAndStaysCurrent(t *testing.T) {
	if _, err := exec.LookPath("typst"); err != nil {
		t.Skip("typst not installed")
	}
	f, ctx := fileFixture(t)
	if res := putFile(t, ctx, f, "doc.typ", "= One\n#pagebreak()\n= Two\n"); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}

	resolver, err := f.Resolver(identity.Identity{UserID: "gh:4242"})
	if err != nil {
		t.Fatal(err)
	}
	before, _, _ := workspace.Usage(resolver)

	got := readResource(t, handleReadPage(f, nil), ctx, pageURIPrefix+"doc.typ/2")
	blob := got[0].(mcp.BlobResourceContents)
	if blob.MIMEType != "image/png" {
		t.Errorf("mime = %q, want image/png", blob.MIMEType)
	}
	raw, err := base64.StdEncoding.DecodeString(blob.Blob)
	if err != nil || len(raw) < 100 || string(raw[1:4]) != "PNG" {
		t.Fatalf("page 2 is not a PNG (%d bytes)", len(raw))
	}

	// Previews live in the workspace and count against the budget —
	// deliberately, since the caller asked for them.
	after, _, _ := workspace.Usage(resolver)
	if after <= before {
		t.Errorf("previews did not appear in workspace usage: %d then %d", before, after)
	}

	// A changed source must not serve the cached page.
	if res := putFile(t, ctx, f, "doc.typ", "= Different\n#pagebreak()\n= Pages\n"); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	again := readResource(t, handleReadPage(f, nil), ctx, pageURIPrefix+"doc.typ/2")
	if again[0].(mcp.BlobResourceContents).Blob == blob.Blob {
		t.Error("a stale page was served after the source changed")
	}
}

// #145: typst names its page files from the document's page count, so
// a document of ten pages or more wrote page-01.png while every reader
// here looked for page-1.png. A 20-page deck rendered perfectly and not
// one page of it could be read.
//
// Two pages is the shape every earlier test used, and it is precisely
// the shape that cannot catch this — which is why the issue's own table
// blamed touying, #d2 and 16:9 geometry. Those were the confound; the
// page count was the cause.
func TestPageResource_RendersADocumentPastTenPages(t *testing.T) {
	requireTypst(t)
	f, ctx := fileFixture(t)

	const pages = 12
	var src strings.Builder
	for i := 1; i <= pages; i++ {
		if i > 1 {
			src.WriteString("#pagebreak()\n")
		}
		fmt.Fprintf(&src, "= Page %d\n", i)
	}
	if res := putFile(t, ctx, f, "deck.typ", src.String()); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}

	// Either side of the boundary, and both ends: padding only appears
	// once the count reaches double digits, so page 9 and page 10 are
	// the pair that matters.
	for _, n := range []int{1, 9, 10, pages} {
		got := readResource(t, handleReadPage(f, nil), ctx,
			fmt.Sprintf("%sdeck.typ/%d", pageURIPrefix, n))
		raw, err := base64.StdEncoding.DecodeString(got[0].(mcp.BlobResourceContents).Blob)
		if err != nil || len(raw) < 100 || string(raw[1:4]) != "PNG" {
			t.Errorf("page %d of %d is not a PNG (%d bytes)", n, pages, len(raw))
		}
	}
}

// The same document shape as the report — 16:9 presentation geometry
// rather than A4, and past ten pages. Page setup was one of the
// suspects; this is what rules it out rather than asserting it.
func TestPageResource_RendersNonA4Geometry(t *testing.T) {
	requireTypst(t)
	f, ctx := fileFixture(t)

	var src strings.Builder
	src.WriteString("#set page(width: 33.87cm, height: 19.05cm, margin: 2cm)\n")
	for i := 1; i <= 11; i++ {
		if i > 1 {
			src.WriteString("#pagebreak()\n")
		}
		fmt.Fprintf(&src, "= Slide %d\n", i)
	}
	if res := putFile(t, ctx, f, "wide.typ", src.String()); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	// Page 1, not page 11: with eleven pages the padded spelling of 11
	// is "11", so the last page is the one page the bug could never
	// affect. Single digits are where it bites.
	for _, n := range []int{1, 11} {
		got := readResource(t, handleReadPage(f, nil), ctx,
			fmt.Sprintf("%swide.typ/%d", pageURIPrefix, n))
		raw, err := base64.StdEncoding.DecodeString(got[0].(mcp.BlobResourceContents).Blob)
		if err != nil || len(raw) < 100 || string(raw[1:4]) != "PNG" {
			t.Fatalf("16:9 page %d did not render (%d bytes)", n, len(raw))
		}
	}
}

// A page that genuinely does not exist must say what the document has.
// The old message guessed — "was not produced — does it have that many
// pages?" — and in #145 the guess was wrong twice over: every page
// existed, and the question it posed was not the problem. A caller who
// cannot see the filesystem can do nothing with a guess.
func TestPageResource_PastTheEndNamesThePageCount(t *testing.T) {
	requireTypst(t)
	f, ctx := fileFixture(t)
	if res := putFile(t, ctx, f, "doc.typ", "= One\n#pagebreak()\n= Two\n"); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}

	req := mcp.ReadResourceRequest{}
	req.Params.URI = pageURIPrefix + "doc.typ/7"
	_, err := handleReadPage(f, nil)(ctx, req)
	if err == nil {
		t.Fatal("reading past the end succeeded")
	}
	if !strings.Contains(err.Error(), "2 page") {
		t.Errorf("error does not say how many pages the document has: %v", err)
	}
	if strings.Contains(err.Error(), "was not produced") {
		t.Errorf("error still guesses instead of counting: %v", err)
	}
}

// A workspace carrying previews from a build that wrote padded names
// must keep working. The render path no longer produces them, but a
// tenant who rendered a long document before this fix has a directory
// full of them, and re-rendering on every read would charge them for
// the server's old mistake.
func TestPageResource_FindsAPaddedPreviewFromAnOlderBuild(t *testing.T) {
	f, ctx := fileFixture(t)
	if res := putFile(t, ctx, f, "legacy.typ", "= One\n"); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	resolver, err := f.Resolver(identity.Identity{UserID: "gh:4242"})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := resolver.Resolve(previewDirFor("legacy.typ"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A marker rather than a real render: what is under test is which
	// filename gets found, not what typst draws.
	marker := []byte("\x89PNG\r\n\x1a\n padded preview from an older build")
	if err := os.WriteFile(filepath.Join(dir, "page-07.png"), marker, 0o600); err != nil {
		t.Fatal(err)
	}

	got := readResource(t, handleReadPage(f, nil), ctx, pageURIPrefix+"legacy.typ/7")
	raw, err := base64.StdEncoding.DecodeString(got[0].(mcp.BlobResourceContents).Blob)
	if err != nil || string(raw) != string(marker) {
		t.Errorf("a padded preview was not found: got %d bytes", len(raw))
	}
}

func TestPageResource_RejectsBadAddresses(t *testing.T) {
	f, ctx := fileFixture(t)
	if res := putFile(t, ctx, f, "doc.typ", "= One\n"); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	for _, uri := range []string{
		pageURIPrefix + "doc.typ",
		pageURIPrefix + "doc.typ/0",
		pageURIPrefix + "doc.typ/notanumber",
	} {
		req := mcp.ReadResourceRequest{}
		req.Params.URI = uri
		if _, err := handleReadPage(f, nil)(ctx, req); err == nil {
			t.Errorf("accepted a malformed page address: %s", uri)
		}
	}
}

// Resources are bounded by the same resolver as everything else.
func TestResources_ArePerTenant(t *testing.T) {
	f, ctx := fileFixture(t)
	if res := putFile(t, ctx, f, "mine.typ", "secret"); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	other := identity.WithIdentity(context.Background(), identity.Identity{UserID: "gh:9999"})

	req := mcp.ReadResourceRequest{}
	req.Params.URI = sourceURIPrefix + "mine.typ"
	if _, err := handleReadSource(f)(other, req); err == nil {
		t.Error("another tenant read the source")
	}
	req.Params.URI = indexURI
	got, err := handleReadIndex(f)(other, req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got[0].(mcp.TextResourceContents).Text, "mine.typ") {
		t.Error("another tenant's index listed my document")
	}
}

// An agent could not preview a document that used its organisation's
// template: the page renderer inherited the server's store, which is
// keyed by namespace ID rather than name, so @acme/templates was
// "package not found". Previews worked only for @house — the documents
// most worth looking at were the ones that could not be.
func TestPageResource_RendersADocumentUsingAnOrgTemplate(t *testing.T) {
	requireTypst(t)

	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)

	store := newTestStore(t)
	if err := store.CreateOrg(t.Context(), "admin", "acme", "Acme"); err != nil {
		t.Fatal(err)
	}
	member := seedUser(t, store, "member", 21)
	if err := store.AddOrgMember(t.Context(), "admin", "acme", "member", authdb.RoleMember); err != nil {
		t.Fatal(err)
	}
	nsID, err := store.ResolveName(t.Context(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	writePackage(t, data, nsID, "1.0.0")

	root := t.TempDir()
	f := workspace.TenantFactory{Root: root}
	ctx := identity.WithIdentity(context.Background(), member)

	src := "#import \"@acme/templates:1.0.0\": mark\n#mark()\n= Heading\n"
	if res := putFileStore(t, ctx, f, store, "doc.typ", src); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}

	req := mcp.ReadResourceRequest{}
	req.Params.URI = pageURIPrefix + "doc.typ/1"
	got, err := handleReadPage(f, store)(ctx, req)
	if err != nil {
		t.Fatalf("a document using an org template could not be previewed: %v", err)
	}
	blob := got[0].(mcp.BlobResourceContents)
	raw, err := base64.StdEncoding.DecodeString(blob.Blob)
	if err != nil || len(raw) < 100 || string(raw[1:4]) != "PNG" {
		t.Fatalf("page 1 is not a PNG (%d bytes)", len(raw))
	}
}

// And a namespace the caller cannot see stays unreachable from the
// preview path too — widening what previews resolve must not widen what
// a caller can reach.
func TestPageResource_CannotReachAnotherTenantsTemplate(t *testing.T) {
	requireTypst(t)

	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)

	store := newTestStore(t)
	stranger := seedUser(t, store, "stranger", 22)
	otherNS, err := store.EnsurePersonalNamespace(t.Context(), stranger.UserID, 22)
	if err != nil {
		t.Fatal(err)
	}
	writePackage(t, data, otherNS, "1.0.0")

	outsider := seedUser(t, store, "outsider", 23)
	root := t.TempDir()
	f := workspace.TenantFactory{Root: root}
	ctx := identity.WithIdentity(context.Background(), outsider)

	src := "#import \"@" + authdb.DerivedName(22) + "/templates:1.0.0\": mark\n#mark()\n"
	if res := putFileStore(t, ctx, f, store, "doc.typ", src); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	req := mcp.ReadResourceRequest{}
	req.Params.URI = pageURIPrefix + "doc.typ/1"
	if _, err := handleReadPage(f, store)(ctx, req); err == nil {
		t.Fatal("previewed a document importing another tenant's template")
	}
}
