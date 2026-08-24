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
	res, err := handleWorkspaceInfo(factory, nil)(ctx, mcp.CallToolRequest{})
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
	// No budget configured: budget/available absent.
	if info.BudgetBytes != nil || info.AvailableBytes != nil {
		t.Errorf("budget fields present without a budget: budget=%v available=%v",
			info.BudgetBytes, info.AvailableBytes)
	}
}

func TestHandleWorkspaceInfo_BudgetFields(t *testing.T) {
	root := t.TempDir()
	factory := workspace.TenantFactory{Root: root}
	id := identity.Identity{UserID: "u"}
	ctx := identity.WithIdentity(context.Background(), id)
	t.Setenv(envWorkspaceBudget, "1000")

	if err := os.MkdirAll(filepath.Join(root, id.UserID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, id.UserID, "f.bin"), make([]byte, 200), 0o600); err != nil {
		t.Fatal(err)
	}
	info := callWorkspaceInfo(t, ctx, factory)

	if info.BudgetBytes == nil || *info.BudgetBytes != 1000 {
		t.Fatalf("budget_bytes = %v, want 1000", info.BudgetBytes)
	}
	if info.AvailableBytes == nil || *info.AvailableBytes != 800 {
		t.Fatalf("available_bytes = %v, want 800 (1000-200)", info.AvailableBytes)
	}
	if info.BudgetHuman == "" || info.AvailableHuman == "" {
		t.Error("human budget/available labels are empty")
	}
}

func TestHandleWorkspaceInfo_AvailableFlooredAtZero(t *testing.T) {
	root := t.TempDir()
	factory := workspace.TenantFactory{Root: root}
	id := identity.Identity{UserID: "u"}
	ctx := identity.WithIdentity(context.Background(), id)
	t.Setenv(envWorkspaceBudget, "100")

	// Usage already over budget (e.g. budget lowered after the fact):
	// available must floor at 0, never go negative.
	if err := os.MkdirAll(filepath.Join(root, id.UserID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, id.UserID, "f.bin"), make([]byte, 250), 0o600); err != nil {
		t.Fatal(err)
	}
	info := callWorkspaceInfo(t, ctx, factory)
	if info.AvailableBytes == nil || *info.AvailableBytes != 0 {
		t.Fatalf("available_bytes = %v, want 0 (floored)", info.AvailableBytes)
	}
}
