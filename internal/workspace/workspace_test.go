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

func TestDirBytes(t *testing.T) {
	root := t.TempDir()
	// 100 + 200 bytes across a nested layout.
	if err := os.WriteFile(filepath.Join(root, "a.bin"), make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "b.bin"), make([]byte, 200), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := DirBytes(root)
	if err != nil {
		t.Fatalf("DirBytes: %v", err)
	}
	if got != 300 {
		t.Errorf("DirBytes = %d, want 300", got)
	}

	// A symlink must not be followed or counted.
	if err := os.Symlink(filepath.Join(root, "a.bin"), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	got, err = DirBytes(root)
	if err != nil {
		t.Fatalf("DirBytes after symlink: %v", err)
	}
	if got != 300 {
		t.Errorf("DirBytes with symlink = %d, want 300 (symlink not counted)", got)
	}
}

func TestDirBytes_MissingDirIsZero(t *testing.T) {
	got, err := DirBytes(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("DirBytes on missing dir: %v", err)
	}
	if got != 0 {
		t.Errorf("DirBytes on missing dir = %d, want 0", got)
	}
}

func TestUsage(t *testing.T) {
	// LocalFS is unbounded: usage is not tracked.
	if bytes, tracked, err := Usage(LocalFS{}); err != nil || tracked || bytes != 0 {
		t.Errorf("Usage(LocalFS) = (%d, %v, %v), want (0, false, nil)", bytes, tracked, err)
	}

	// ScopedFS is bounded: usage is the measured size of its root.
	root := t.TempDir()
	s, err := NewScopedFS(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "f.bin"), make([]byte, 42), 0o600); err != nil {
		t.Fatal(err)
	}
	bytes, tracked, err := Usage(s)
	if err != nil {
		t.Fatalf("Usage(ScopedFS): %v", err)
	}
	if !tracked {
		t.Error("Usage(ScopedFS) tracked = false, want true")
	}
	if bytes != 42 {
		t.Errorf("Usage(ScopedFS) bytes = %d, want 42", bytes)
	}
}

func TestAppendFile(t *testing.T) {
	root := t.TempDir()
	s, err := NewScopedFS(root)
	if err != nil {
		t.Fatal(err)
	}

	// Append to a non-existent file creates it.
	if _, err := AppendFile(s, "a.bin", []byte("hello ")); err != nil {
		t.Fatalf("first AppendFile: %v", err)
	}
	// A second append concatenates rather than truncating.
	if _, err := AppendFile(s, "a.bin", []byte("world")); err != nil {
		t.Fatalf("second AppendFile: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(root, "a.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello world" {
		t.Errorf("file content = %q, want %q", got, "hello world")
	}
}

func TestAppendFile_CreatesParentDirs(t *testing.T) {
	root := t.TempDir()
	s, err := NewScopedFS(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AppendFile(s, "nested/deep/a.bin", []byte("x")); err != nil {
		t.Fatalf("AppendFile into a new subtree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "nested", "deep", "a.bin")); err != nil {
		t.Errorf("appended file not created: %v", err)
	}
}

// A file staged by an in-flight compile is server scratch, not tenant
// data: counting it would let a large document briefly inflate reported
// usage, or push its own compile over a byte budget.
func TestDirBytes_SkipsStagedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real.txt"), make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := DirBytes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != 100 {
		t.Fatalf("baseline = %d, want 100", got)
	}

	staged := filepath.Join(dir, StagePrefix+"123.typ")
	if err := os.WriteFile(staged, make([]byte, 5000), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = DirBytes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != 100 {
		t.Errorf("with a staged file DirBytes = %d, want 100", got)
	}

	// A nested one, too — documents live in subdirectories.
	sub := filepath.Join(dir, "docs")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, StagePrefix+"456.typ"), make([]byte, 5000), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = DirBytes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != 100 {
		t.Errorf("with a nested staged file DirBytes = %d, want 100", got)
	}
}
