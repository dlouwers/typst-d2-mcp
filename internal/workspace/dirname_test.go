package workspace

import (
	"os"

	"github.com/dlouwers/typst-d2-mcp/internal/identity"
	"strings"
	"testing"
)

// The bug this exists to end: a tenant path containing the OS list
// separator. Tools that take a directory list split on it and read
// nothing, silently.
func TestDirName_NeverContainsThePathListSeparator(t *testing.T) {
	sep := string(os.PathListSeparator)
	for _, id := range []string{
		"gh:12345", "gh:1", "anonymous", "user123",
		"weird:id/with\\stuff", "spaces here", "semi;colon", "quote'apos",
		strings.Repeat("gh:9", 40),
	} {
		got := DirName(id)
		if strings.Contains(got, sep) {
			t.Errorf("DirName(%q) = %q, which contains %q", id, got, sep)
		}
		for _, bad := range []string{"/", "\\", ":", " ", ";", "'", "\"", "*", "?"} {
			if strings.Contains(got, bad) {
				t.Errorf("DirName(%q) = %q, which contains %q", id, got, bad)
			}
		}
	}
}

// Sanitising alone is not injective — "file?" and "*file*" both reduce
// to "file". Two identities must never share a workspace.
func TestDirName_IsInjective(t *testing.T) {
	ids := []string{
		"gh:1", "gh-1", "gh_1", "gh 1", "gh?1", "gh*1",
		"gh:12345", "gh-12345", "a:b", "a-b", "a/b", "a\\b",
		"", "anonymous", ".", "..",
	}
	seen := map[string]string{}
	for _, id := range ids {
		got := DirName(id)
		if prev, clash := seen[got]; clash {
			t.Errorf("%q and %q both map to %q", prev, id, got)
		}
		seen[got] = id
	}
}

// An identity that is already safe keeps its name, so a volume stays
// readable and nothing existing has to move for no reason.
func TestDirName_LeavesSafeIdentitiesAlone(t *testing.T) {
	for _, id := range []string{"anonymous", "user123", "gh-12345", "a_b-c"} {
		if got := DirName(id); got != id {
			t.Errorf("DirName(%q) = %q, want it unchanged", id, got)
		}
	}
}

// The readable part survives, so an operator can still tell whose
// workspace a directory is.
func TestDirName_StaysRecognisable(t *testing.T) {
	got := DirName("gh:12345")
	if !strings.HasPrefix(got, "gh-12345-") {
		t.Errorf("DirName(gh:12345) = %q, want a readable gh-12345 prefix", got)
	}
	if len(got) != len("gh-12345-")+8 {
		t.Errorf("DirName(gh:12345) = %q, want an 8-character digest suffix", got)
	}
}

// Stability matters more than beauty: a workspace must be found again
// after a restart, a redeploy, and a year.
func TestDirName_IsStable(t *testing.T) {
	first := DirName("gh:12345")
	for i := 0; i < 100; i++ {
		if got := DirName("gh:12345"); got != first {
			t.Fatalf("DirName is not stable: %q then %q", first, got)
		}
	}
}

// The sweeper and the admin UI walk the tenant root and must find
// exactly the directories the resolvers create. A migration that left
// two names, or a name the walker cannot match back to a user, would
// make usage accounting silently wrong.
func TestDirName_MigratesALegacyWorkspaceOnce(t *testing.T) {
	root := t.TempDir()
	legacy := root + "/gh:12345"
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy+"/doc.typ", []byte("= Existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := TenantFactory{Root: root}
	for i := 0; i < 3; i++ { // idempotent
		if _, err := f.Resolver(identity.Identity{UserID: "gh:12345"}); err != nil {
			t.Fatalf("resolve: %v", err)
		}
	}

	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Error("the legacy directory is still there; two names for one workspace")
	}
	moved := root + "/" + DirName("gh:12345")
	if _, err := os.Stat(moved + "/doc.typ"); err != nil {
		t.Errorf("the document did not survive the move: %v", err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("tenant root holds %v, want exactly one directory", names)
	}
}

// If someone has already been migrated and a stale legacy directory
// reappears, the current workspace wins and the old one is left for a
// person to look at — merging two directories of somebody's documents
// is not a decision code should make silently.
func TestDirName_MigrationDoesNotClobberAnExistingWorkspace(t *testing.T) {
	root := t.TempDir()
	legacy := root + "/gh:7"
	current := root + "/" + DirName("gh:7")
	for _, d := range []string{legacy, current} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(current+"/keep.typ", []byte("current\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := TenantFactory{Root: root}
	if _, err := f.Resolver(identity.Identity{UserID: "gh:7"}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := os.Stat(current + "/keep.typ"); err != nil {
		t.Errorf("the current workspace was clobbered: %v", err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Errorf("the legacy directory was removed rather than left for inspection: %v", err)
	}
}
