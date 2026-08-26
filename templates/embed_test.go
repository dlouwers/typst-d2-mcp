package templates

import (
	"io/fs"
	"path/filepath"
	"testing"
)

// maxEmbeddedBytes bounds the whole bundled tree. Templates now
// reasonably carry assets — a logo, a mark — and every byte of them
// lands in the binary and in each image layer. This is a generous
// ceiling meant to catch someone dropping a 40MB print-resolution logo
// in, not to police ordinary use.
const maxEmbeddedBytes = 8 << 20

// repoTree walks the package tree as it sits in the repo, returning
// slash-separated paths relative to this directory.
func repoTree(t *testing.T) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	err := filepath.WalkDir("house", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[filepath.ToSlash(path)] = info.Size()
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo templates: %v", err)
	}
	return out
}

// embeddedTree is the same set, as it exists inside the binary.
func embeddedTree(t *testing.T) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	err := fs.WalkDir(FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[path] = info.Size()
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded templates: %v", err)
	}
	return out
}

// The embedded tree must be the repo tree, entry for entry.
//
// This is not paranoia about the compiler. A bare `//go:embed house`
// silently drops every name beginning with `_` or `.` — so a template
// carrying `_helpers.typ` would build, pass every test that names
// `lib.typ` explicitly, seed an incomplete tree onto the volume, and
// fail at a user's compile on a file that is present in the repo. The
// only way to catch that is to compare the sets rather than spot-check
// members of them.
func TestEmbeddedTreeMatchesRepo(t *testing.T) {
	repo := repoTree(t)
	embedded := embeddedTree(t)

	if len(repo) == 0 {
		t.Fatal("no template files found in the repo tree")
	}

	for path, size := range repo {
		got, ok := embedded[path]
		if !ok {
			t.Errorf("%s is in the repo but not in the binary "+
				"(a leading _ or . in any path segment needs //go:embed all:)", path)
			continue
		}
		if got != size {
			t.Errorf("%s: embedded %d bytes, repo has %d", path, got, size)
		}
	}
	for path := range embedded {
		if _, ok := repo[path]; !ok {
			t.Errorf("%s is embedded but not in the repo tree", path)
		}
	}
}

// Everything embedded ships in the binary and in every image layer.
func TestEmbeddedTreeSize(t *testing.T) {
	var total int64
	for path, size := range embeddedTree(t) {
		total += size
		if size > maxEmbeddedBytes {
			t.Errorf("%s alone is %d bytes", path, size)
		}
	}
	if total > maxEmbeddedBytes {
		t.Errorf("bundled templates total %d bytes, over the %d ceiling — "+
			"they ship in the binary and in every image layer", total, maxEmbeddedBytes)
	}
}
