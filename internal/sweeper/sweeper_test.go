package sweeper

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dlouwers/typst-d2-mcp/internal/authdb"
)

// The sweeper runs against the real store rather than a fake: the
// interesting behaviour is the interaction between live links in SQLite
// and files on disk, which a fake would define away.
func newStore(t *testing.T) *authdb.Store {
	t.Helper()
	s, err := authdb.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// writeFile creates a file under the tenant root with a chosen age.
func writeFile(t *testing.T, root, userID, rel string, age time.Duration) string {
	t.Helper()
	path := filepath.Join(root, userID, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("pdf bytes"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return path
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

func TestSweepOnce_PurgesOldKeepsNew(t *testing.T) {
	store := newStore(t)
	root := t.TempDir()

	old := writeFile(t, root, "gh:1", "old.pdf", 48*time.Hour)
	fresh := writeFile(t, root, "gh:1", "fresh.pdf", time.Minute)

	sw := New(store, Config{Root: root, FileTTL: 24 * time.Hour, Interval: time.Hour})
	res, err := sw.SweepOnce(t.Context(), time.Now().UTC())
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}

	if exists(t, old) {
		t.Error("file older than the TTL survived")
	}
	if !exists(t, fresh) {
		t.Error("file newer than the TTL was deleted")
	}
	if res.FilesDeleted != 1 {
		t.Errorf("FilesDeleted = %d, want 1", res.FilesDeleted)
	}
}

// A file with an unexpired download link is kept however old it is:
// a URL already in someone's hands must not break.
func TestSweepOnce_KeepsFileWithLiveLink(t *testing.T) {
	store := newStore(t)
	root := t.TempDir()
	ctx := t.Context()

	linked := writeFile(t, root, "gh:1", "linked.pdf", 48*time.Hour)
	unlinked := writeFile(t, root, "gh:1", "unlinked.pdf", 48*time.Hour)

	if _, err := store.MintPDFLink(ctx, "gh:1", "linked.pdf", time.Hour); err != nil {
		t.Fatalf("MintPDFLink: %v", err)
	}

	sw := New(store, Config{Root: root, FileTTL: 24 * time.Hour, Interval: time.Hour})
	if _, err := sw.SweepOnce(ctx, time.Now().UTC()); err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}

	if !exists(t, linked) {
		t.Error("file with a live download link was deleted")
	}
	if exists(t, unlinked) {
		t.Error("unprotected aged file survived")
	}
}

// Once the link expires, the same file becomes collectable — the
// protection is the live link, not the file.
func TestSweepOnce_CollectsAfterLinkExpires(t *testing.T) {
	store := newStore(t)
	root := t.TempDir()
	ctx := t.Context()

	linked := writeFile(t, root, "gh:1", "linked.pdf", 48*time.Hour)
	if _, err := store.MintPDFLink(ctx, "gh:1", "linked.pdf", time.Hour); err != nil {
		t.Fatalf("MintPDFLink: %v", err)
	}

	sw := New(store, Config{Root: root, FileTTL: 24 * time.Hour, Interval: time.Hour})
	// Two hours on: the link has expired, so the sweep deletes the row
	// first and the file is no longer protected.
	if _, err := sw.SweepOnce(ctx, time.Now().UTC().Add(2*time.Hour)); err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	if exists(t, linked) {
		t.Error("file survived after its link expired")
	}
}

func TestSweepOnce_TTLZeroDisablesPurgeButStillSweepsLinks(t *testing.T) {
	store := newStore(t)
	root := t.TempDir()
	ctx := t.Context()

	old := writeFile(t, root, "gh:1", "ancient.pdf", 10000*time.Hour)
	token, err := store.MintPDFLink(ctx, "gh:1", "ancient.pdf", time.Hour)
	if err != nil {
		t.Fatalf("MintPDFLink: %v", err)
	}

	sw := New(store, Config{Root: root, FileTTL: 0, Interval: time.Hour})
	res, err := sw.SweepOnce(ctx, time.Now().UTC().Add(2*time.Hour))
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}

	if !exists(t, old) {
		t.Error("TTL=0 should disable the file purge entirely")
	}
	if res.LinksDeleted != 1 {
		t.Errorf("LinksDeleted = %d, want 1 — link sweeping must run regardless of TTL", res.LinksDeleted)
	}
	if _, err := store.LookupPDFLink(ctx, token); err == nil {
		t.Error("expired link survived a TTL=0 sweep")
	}

	// Usage is still measured with purging off, so the admin UI works.
	usage, err := store.WorkspaceUsageByUser(ctx)
	if err != nil {
		t.Fatalf("WorkspaceUsageByUser: %v", err)
	}
	if usage["gh:1"].Bytes == 0 {
		t.Error("usage not recorded when TTL=0")
	}
}

func TestSweepOnce_RecordsPerUserUsage(t *testing.T) {
	store := newStore(t)
	root := t.TempDir()
	ctx := t.Context()

	// 9 bytes each ("pdf bytes"), all fresh so none are purged.
	writeFile(t, root, "gh:1", "a.pdf", time.Minute)
	writeFile(t, root, "gh:1", "sub/b.pdf", time.Minute)
	writeFile(t, root, "gh:2", "c.pdf", time.Minute)

	sw := New(store, Config{Root: root, FileTTL: 24 * time.Hour, Interval: time.Hour})
	res, err := sw.SweepOnce(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	if res.UsersMeasured != 2 {
		t.Errorf("UsersMeasured = %d, want 2", res.UsersMeasured)
	}

	usage, err := store.WorkspaceUsageByUser(ctx)
	if err != nil {
		t.Fatalf("WorkspaceUsageByUser: %v", err)
	}
	if got, want := usage["gh:1"].Bytes, int64(len("pdf bytes")*2); got != want {
		t.Errorf("gh:1 bytes = %d, want %d", got, want)
	}
	if got, want := usage["gh:2"].Bytes, int64(len("pdf bytes")); got != want {
		t.Errorf("gh:2 bytes = %d, want %d", got, want)
	}
}

// A user whose files were all purged is measured at zero, not skipped —
// otherwise the admin UI would keep showing their pre-purge size.
func TestSweepOnce_EmptiedWorkspaceRecordsZero(t *testing.T) {
	store := newStore(t)
	root := t.TempDir()
	ctx := t.Context()

	writeFile(t, root, "gh:1", "old.pdf", 48*time.Hour)

	sw := New(store, Config{Root: root, FileTTL: 24 * time.Hour, Interval: time.Hour})
	if _, err := sw.SweepOnce(ctx, time.Now().UTC()); err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}

	usage, err := store.WorkspaceUsageByUser(ctx)
	if err != nil {
		t.Fatalf("WorkspaceUsageByUser: %v", err)
	}
	u, ok := usage["gh:1"]
	if !ok {
		t.Fatal("emptied workspace was not measured at all")
	}
	if u.Bytes != 0 {
		t.Errorf("bytes = %d, want 0", u.Bytes)
	}
}

func TestSweepOnce_RemovesEmptiedSubdirsKeepsUserRoot(t *testing.T) {
	store := newStore(t)
	root := t.TempDir()

	writeFile(t, root, "gh:1", "nested/deep/old.pdf", 48*time.Hour)

	sw := New(store, Config{Root: root, FileTTL: 24 * time.Hour, Interval: time.Hour})
	if _, err := sw.SweepOnce(t.Context(), time.Now().UTC()); err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}

	if exists(t, filepath.Join(root, "gh:1", "nested")) {
		t.Error("emptied subdirectory was not pruned")
	}
	if !exists(t, filepath.Join(root, "gh:1")) {
		t.Error("user root was removed; it should be kept")
	}
}

// The purge must not follow a symlink out of the tenant root. WalkDir
// does not follow symlinks, so the link itself is seen as an irregular
// file and skipped, and the target is untouched.
func TestSweepOnce_DoesNotEscapeViaSymlink(t *testing.T) {
	store := newStore(t)
	root := t.TempDir()
	outside := t.TempDir()

	victim := filepath.Join(outside, "precious.pdf")
	if err := os.WriteFile(victim, []byte("do not delete"), 0o600); err != nil {
		t.Fatalf("write victim: %v", err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(victim, old, old); err != nil {
		t.Fatalf("chtimes victim: %v", err)
	}

	userRoot := filepath.Join(root, "gh:1")
	if err := os.MkdirAll(userRoot, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Both a file symlink and a directory symlink pointing outside.
	if err := os.Symlink(victim, filepath.Join(userRoot, "link.pdf")); err != nil {
		t.Fatalf("symlink file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(userRoot, "escape")); err != nil {
		t.Fatalf("symlink dir: %v", err)
	}

	sw := New(store, Config{Root: root, FileTTL: 24 * time.Hour, Interval: time.Hour})
	if _, err := sw.SweepOnce(t.Context(), time.Now().UTC()); err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}

	if !exists(t, victim) {
		t.Error("sweeper deleted a file outside the tenant root via a symlink")
	}
}

func TestSweepOnce_NoRootSkipsFilesystemWork(t *testing.T) {
	store := newStore(t)
	ctx := t.Context()

	if _, err := store.MintPDFLink(ctx, "gh:1", "out.pdf", time.Hour); err != nil {
		t.Fatalf("MintPDFLink: %v", err)
	}

	sw := New(store, Config{Root: "", FileTTL: 24 * time.Hour, Interval: time.Hour})
	res, err := sw.SweepOnce(ctx, time.Now().UTC().Add(2*time.Hour))
	if err != nil {
		t.Fatalf("SweepOnce with no root: %v", err)
	}
	if res.LinksDeleted != 1 {
		t.Errorf("LinksDeleted = %d, want 1", res.LinksDeleted)
	}
	if res.UsersMeasured != 0 {
		t.Errorf("UsersMeasured = %d, want 0 when no root is configured", res.UsersMeasured)
	}
}

// A missing root is the normal state before anyone has compiled.
func TestSweepOnce_MissingRootIsNotAnError(t *testing.T) {
	store := newStore(t)
	sw := New(store, Config{
		Root:     filepath.Join(t.TempDir(), "does-not-exist"),
		FileTTL:  24 * time.Hour,
		Interval: time.Hour,
	})
	if _, err := sw.SweepOnce(t.Context(), time.Now().UTC()); err != nil {
		t.Errorf("SweepOnce with missing root: %v", err)
	}
}

// A tenant's typefaces are pushed once and referenced by every document
// afterwards. Ageing them out would break a workspace's typography long
// after the last put_file, with nothing connecting cause to effect — so
// fonts/ is configuration, not scratch, and survives whatever its age.
func TestSweepOnce_KeepsWorkspaceFonts(t *testing.T) {
	store := newStore(t)
	root := t.TempDir()

	font := writeFile(t, root, "gh:1", filepath.Join("fonts", "Brand-Regular.ttf"), 30*24*time.Hour)
	nested := writeFile(t, root, "gh:1", filepath.Join("fonts", "brand", "Brand-Bold.otf"), 30*24*time.Hour)
	doc := writeFile(t, root, "gh:1", "old.pdf", 48*time.Hour)

	// A file merely NAMED like the directory is still scratch.
	decoy := writeFile(t, root, "gh:1", "fonts-notes.txt", 48*time.Hour)

	sw := New(store, Config{Root: root, FileTTL: 24 * time.Hour, Interval: time.Hour})
	res, err := sw.SweepOnce(t.Context(), time.Now().UTC())
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}

	if !exists(t, font) {
		t.Error("a workspace font was purged")
	}
	if !exists(t, nested) {
		t.Error("a font in a fonts/ subdirectory was purged")
	}
	if exists(t, doc) {
		t.Error("an aged-out PDF survived; the exemption is too broad")
	}
	if exists(t, decoy) {
		t.Error("a file merely prefixed 'fonts' was treated as configuration")
	}
	if res.FilesDeleted != 2 {
		t.Errorf("FilesDeleted = %d, want 2", res.FilesDeleted)
	}
}
