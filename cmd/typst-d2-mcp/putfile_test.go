package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dlouwers/typst-d2-mcp/internal/authdb"
	"github.com/dlouwers/typst-d2-mcp/internal/identity"
	"github.com/dlouwers/typst-d2-mcp/internal/workspace"
	"github.com/mark3labs/mcp-go/mcp"
)

// putFile invokes the put_file handler with utf8 content and returns the
// raw result for inspection.
func putFile(t *testing.T, ctx context.Context, factory workspace.Factory, path, content string) *mcp.CallToolResult {
	t.Helper()
	return putFileStore(t, ctx, factory, nil, path, content)
}

// putFileStore is putFile with an explicit store, for exercising the
// per-workspace budget override.
func putFileStore(t *testing.T, ctx context.Context, factory workspace.Factory, store *authdb.Store, path, content string) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = "put_file"
	req.Params.Arguments = map[string]any{"path": path, "content": content}
	res, err := handlePutFile(factory, store)(ctx, req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return res
}

func TestPutFile_BudgetEnforced(t *testing.T) {
	root := t.TempDir()
	factory := workspace.TenantFactory{Root: root}
	id := identity.Identity{UserID: "user123"}
	ctx := identity.WithIdentity(context.Background(), id)
	t.Setenv(envWorkspaceBudget, "100")

	// A 60-byte write fits under the 100-byte budget.
	if res := putFile(t, ctx, factory, "a.bin", string(make([]byte, 60))); res.IsError {
		t.Fatalf("first write rejected unexpectedly: %+v", res.Content)
	}

	// A further 60 bytes would total 120 > 100: rejected, and not written.
	res := putFile(t, ctx, factory, "b.bin", string(make([]byte, 60)))
	if !res.IsError {
		t.Fatal("over-budget write was accepted, want rejection")
	}
	if text, ok := mcp.AsTextContent(res.Content[0]); !ok || !strings.Contains(text.Text, "budget") {
		t.Errorf("error message does not mention budget: %+v", res.Content[0])
	}
	if _, err := os.Stat(filepath.Join(root, id.UserID, "b.bin")); !os.IsNotExist(err) {
		t.Errorf("rejected file b.bin should not exist, stat err = %v", err)
	}
}

func TestPutFile_BudgetOverwriteNotDoubleCounted(t *testing.T) {
	root := t.TempDir()
	factory := workspace.TenantFactory{Root: root}
	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: "u"})
	t.Setenv(envWorkspaceBudget, "100")

	// Fill to 80 bytes.
	if res := putFile(t, ctx, factory, "a.bin", string(make([]byte, 80))); res.IsError {
		t.Fatalf("initial 80-byte write rejected: %+v", res.Content)
	}
	// Overwriting a.bin with another 80 bytes must be judged as 80 total
	// (the old 80 is discounted), not 160 — so it stays within budget.
	if res := putFile(t, ctx, factory, "a.bin", string(make([]byte, 80))); res.IsError {
		t.Fatalf("in-place overwrite wrongly counted as new bytes: %+v", res.Content)
	}
}

func TestPutFile_PerWorkspaceOverrideBeatsEnvDefault(t *testing.T) {
	// Env default is unlimited (unset). A per-workspace override of 100
	// bytes must still be enforced — proving the override is consulted,
	// not just the env.
	store, err := authdb.Open(filepath.Join(t.TempDir(), "auth.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.UpsertGitHubUser(t.Context(), 42, "octocat", "octocat@example.com"); err != nil {
		t.Fatalf("UpsertGitHubUser: %v", err)
	}
	hundred := int64(100)
	if err := store.SetWorkspaceBudget(t.Context(), "dlouwers", "octocat", &hundred); err != nil {
		t.Fatalf("SetWorkspaceBudget: %v", err)
	}

	root := t.TempDir()
	factory := workspace.TenantFactory{Root: root}
	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: "gh:42"})

	if res := putFileStore(t, ctx, factory, store, "a.bin", string(make([]byte, 60))); res.IsError {
		t.Fatalf("first write rejected unexpectedly: %+v", res.Content)
	}
	// 60 + 60 = 120 > 100 override.
	if res := putFileStore(t, ctx, factory, store, "b.bin", string(make([]byte, 60))); !res.IsError {
		t.Fatal("write over the per-workspace override was accepted, want rejection")
	}
}

func TestPutFile_BudgetInertInLocalMode(t *testing.T) {
	// LocalFS is unbounded: the budget must not apply even when configured.
	dir := t.TempDir()
	t.Setenv(envWorkspaceBudget, "10")
	res := putFile(t, context.Background(), workspace.LocalFactory{},
		filepath.Join(dir, "big.bin"), string(make([]byte, 500)))
	if res.IsError {
		t.Fatalf("budget wrongly enforced in local mode: %+v", res.Content)
	}
}

func TestPutFile_BudgetDisabledByDefault(t *testing.T) {
	root := t.TempDir()
	factory := workspace.TenantFactory{Root: root}
	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: "u"})
	// No budget env set (0 = unlimited): a large write is accepted.
	if res := putFile(t, ctx, factory, "a.bin", string(make([]byte, 10_000))); res.IsError {
		t.Fatalf("write rejected with no budget configured: %+v", res.Content)
	}
	if got := workspaceBudgetBytes(); got != 0 {
		t.Errorf("workspaceBudgetBytes default = %d, want 0", got)
	}
}
