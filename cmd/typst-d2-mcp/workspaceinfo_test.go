package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dlouwers/typst-d2-mcp/internal/identity"
	"github.com/dlouwers/typst-d2-mcp/internal/workspace"
	"github.com/mark3labs/mcp-go/mcp"
)

// callWorkspaceInfo invokes the handler and returns the decoded payload.
func callWorkspaceInfo(t *testing.T, ctx context.Context, factory workspace.Factory) workspaceInfo {
	t.Helper()
	res, err := handleWorkspaceInfo(factory)(ctx, mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool result is an error: %+v", res.Content)
	}
	text, ok := mcp.AsTextContent(res.Content[0])
	if !ok {
		t.Fatalf("first content block is not text: %T", res.Content[0])
	}
	var info workspaceInfo
	if err := json.Unmarshal([]byte(text.Text), &info); err != nil {
		t.Fatalf("decode payload %q: %v", text.Text, err)
	}
	return info
}

func TestHandleWorkspaceInfo_LocalUntracked(t *testing.T) {
	info := callWorkspaceInfo(t, context.Background(), workspace.LocalFactory{})

	if info.UsageTracked {
		t.Error("usage_tracked = true in local mode, want false")
	}
	if info.UsedBytes != nil {
		t.Errorf("used_bytes present in local mode: %d", *info.UsedBytes)
	}
	if info.PerCallLimitBytes != maxInputBytes() {
		t.Errorf("per_call_limit_bytes = %d, want %d", info.PerCallLimitBytes, maxInputBytes())
	}
	if info.PerCallLimitHuman == "" {
		t.Error("per_call_limit_human is empty")
	}
}

func TestHandleWorkspaceInfo_ScopedTracked(t *testing.T) {
	root := t.TempDir()
	factory := workspace.TenantFactory{Root: root}
	id := identity.Identity{UserID: "user123"}
	ctx := identity.WithIdentity(context.Background(), id)

	// Empty workspace: tracked, zero bytes.
	info := callWorkspaceInfo(t, ctx, factory)
	if !info.UsageTracked {
		t.Fatal("usage_tracked = false in scoped mode, want true")
	}
	if info.UsedBytes == nil || *info.UsedBytes != 0 {
		t.Fatalf("used_bytes = %v, want 0", info.UsedBytes)
	}

	// Write 128 bytes into the user's scoped root, then re-measure.
	if err := os.WriteFile(filepath.Join(root, id.UserID, "asset.bin"), make([]byte, 128), 0o600); err != nil {
		t.Fatal(err)
	}
	info = callWorkspaceInfo(t, ctx, factory)
	if info.UsedBytes == nil || *info.UsedBytes != 128 {
		t.Fatalf("used_bytes = %v, want 128", info.UsedBytes)
	}
	if info.UsedHuman == "" {
		t.Error("used_human is empty when usage is tracked")
	}
}
