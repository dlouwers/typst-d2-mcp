package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

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

	got := readResource(t, handleReadPage(f), ctx, pageURIPrefix+"doc.typ/2")
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
	again := readResource(t, handleReadPage(f), ctx, pageURIPrefix+"doc.typ/2")
	if again[0].(mcp.BlobResourceContents).Blob == blob.Blob {
		t.Error("a stale page was served after the source changed")
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
		if _, err := handleReadPage(f)(ctx, req); err == nil {
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
