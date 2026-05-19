package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dlouwers/typst-d2-mcp/internal/identity"
)

func TestLocalFS_Resolve(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"absolute path", "/tmp/foo.typ", "/tmp/foo.typ", false},
		{"relative path", "./a/b.typ", "a/b.typ", false},
		{"redundant slashes", "/tmp//foo.typ", "/tmp/foo.typ", false},
		{"empty rejected", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LocalFS{}.Resolve(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Resolve(%q) err=%v, wantErr=%v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("Resolve(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestScopedFS_Resolve(t *testing.T) {
	root := t.TempDir()
	s, err := NewScopedFS(root)
	if err != nil {
		t.Fatalf("NewScopedFS: %v", err)
	}

	tests := []struct {
		name    string
		input   string
		wantErr string // substring; "" means success
	}{
		{"relative ok", "a/b.typ", ""},
		{"redundant slashes", "a//b.typ", ""},
		{"dot prefix ok", "./a/b.typ", ""},
		{"interior dotdot ok", "a/b/../c.typ", ""},
		{"empty rejected", "", "empty path"},
		{"absolute rejected", "/etc/passwd", "absolute"},
		{"traversal direct", "../escape.typ", "escapes workspace"},
		{"traversal nested", "a/../../escape.typ", "escapes workspace"},
		{"traversal trailing", "a/b/../../..", "escapes workspace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.Resolve(tt.input)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Resolve(%q) err=%v, want substring %q", tt.input, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve(%q): unexpected err %v", tt.input, err)
			}
			if !strings.HasPrefix(got, s.Root+string(os.PathSeparator)) && got != s.Root {
				t.Errorf("Resolve(%q) = %q, want path under %q", tt.input, got, s.Root)
			}
		})
	}
}

func TestNewScopedFS_CreatesRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "ws")
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("precondition: %s should not exist yet", root)
	}
	s, err := NewScopedFS(root)
	if err != nil {
		t.Fatalf("NewScopedFS: %v", err)
	}
	info, err := os.Stat(s.Root)
	if err != nil {
		t.Fatalf("workspace root not created: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("workspace root is not a directory")
	}
}

func TestWriteFile_LocalFS(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", "doc.typ")
	if _, err := WriteFile(LocalFS{}, target, []byte("hello")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q want %q", got, "hello")
	}
}

func TestLocalFactory_IgnoresIdentity(t *testing.T) {
	r1, err := LocalFactory{}.Resolver(identity.Anonymous())
	if err != nil {
		t.Fatal(err)
	}
	r2, err := LocalFactory{}.Resolver(identity.Identity{UserID: "u_42"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r1.(LocalFS); !ok {
		t.Errorf("anonymous resolver is not LocalFS: %T", r1)
	}
	if _, ok := r2.(LocalFS); !ok {
		t.Errorf("user resolver is not LocalFS: %T", r2)
	}
}

func TestTenantFactory_PerUserRoot(t *testing.T) {
	root := t.TempDir()
	f := TenantFactory{Root: root}

	a, err := f.Resolver(identity.Identity{UserID: "user-a"})
	if err != nil {
		t.Fatalf("resolver A: %v", err)
	}
	b, err := f.Resolver(identity.Identity{UserID: "user-b"})
	if err != nil {
		t.Fatalf("resolver B: %v", err)
	}
	if a.(*ScopedFS).Root == b.(*ScopedFS).Root {
		t.Errorf("two users got the same root: %s", a.(*ScopedFS).Root)
	}
	wantPrefix := filepath.Join(root, "user-a")
	if a.(*ScopedFS).Root != wantPrefix {
		t.Errorf("user-a root = %s, want %s", a.(*ScopedFS).Root, wantPrefix)
	}
	if _, err := f.Resolver(identity.Identity{}); err == nil {
		t.Error("empty UserID should be rejected")
	}
}

func TestWriteFile_ScopedFS_TraversalRejected(t *testing.T) {
	root := t.TempDir()
	s, err := NewScopedFS(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteFile(s, "../escape.typ", []byte("nope")); err == nil {
		t.Fatal("expected traversal error, got nil")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.typ")); err == nil {
		t.Errorf("file was written outside workspace root")
	}
}

func TestMustExist(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "ok.typ")
	if err := os.WriteFile(existing, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := MustExist(LocalFS{}, existing); err != nil {
		t.Errorf("MustExist on existing file: %v", err)
	}

	missing := filepath.Join(dir, "missing.typ")
	_, err := MustExist(LocalFS{}, missing)
	if err == nil || !strings.Contains(err.Error(), "file not found") {
		t.Errorf("expected 'file not found' for missing path, got %v", err)
	}

	_, err = MustExist(LocalFS{}, dir)
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Errorf("expected directory-rejection error, got %v", err)
	}
}
