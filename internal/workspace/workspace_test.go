package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
