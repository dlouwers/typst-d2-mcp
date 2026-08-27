package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

// A PDF pulled through a tool call is the wrong move; point at the
// resource URI rather than filling the caller's context.
func TestGetFile_LargeFileRefusedWithAPointer(t *testing.T) {
	f, ctx := fileFixture(t)
	big := strings.Repeat("x", maxGetFileBytes+1)
	if res := putFile(t, ctx, f, "big.pdf", big); res.IsError {
		t.Fatalf("put_file: %s", resultText(res))
	}
	got := callTool(t, handleGetFile(f), ctx, map[string]any{"path": "big.pdf"})
	if !got.IsError {
		t.Fatal("a file over the cap was returned inline")
	}
	if !strings.Contains(resultText(got), "resources/read") {
		t.Errorf("refusal does not point at the right mechanism: %s", resultText(got))
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
