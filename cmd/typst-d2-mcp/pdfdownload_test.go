package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dlouwers/typst-d2-mcp/internal/authdb"
	"github.com/dlouwers/typst-d2-mcp/internal/workspace"
)

// pdfBody is long enough that a Range request returns a recognisable
// slice rather than the whole thing.
const pdfBody = "%PDF-1.7 pretend this is a compiled document"

// newDownloadFixture stands up a store, a tenant workspace containing
// one PDF for gh:1, and the /d/ handler wired to both.
func newDownloadFixture(t *testing.T) (http.Handler, *authdb.Store, string) {
	t.Helper()

	store, err := authdb.Open(filepath.Join(t.TempDir(), "auth.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	root := t.TempDir()
	userDir := filepath.Join(root, "gh:1")
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "out.pdf"), []byte(pdfBody), 0o600); err != nil {
		t.Fatalf("write pdf: %v", err)
	}

	factory := workspace.TenantFactory{Root: root}
	token, err := store.MintPDFLink(t.Context(), "gh:1", "out.pdf", time.Hour)
	if err != nil {
		t.Fatalf("MintPDFLink: %v", err)
	}
	return handlePDFDownload(factory, store), store, token
}

func TestPDFDownload_FullBody(t *testing.T) {
	h, _, token := newDownloadFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/d/"+token, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != pdfBody {
		t.Errorf("body = %q, want %q", got, pdfBody)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/pdf" {
		t.Errorf("Content-Type = %q, want application/pdf", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, `"out.pdf"`) {
		t.Errorf("Content-Disposition = %q, want the filename in it", got)
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("no ETag set; conditional requests cannot work")
	}
}

// Range support is the practical win of ServeContent over io.Copy:
// large PDFs become resumable.
func TestPDFDownload_RangeRequest(t *testing.T) {
	h, _, token := newDownloadFixture(t)

	req := httptest.NewRequest(http.MethodGet, "/d/"+token, nil)
	req.Header.Set("Range", "bytes=0-7")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if got, want := rec.Body.String(), pdfBody[0:8]; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if got := rec.Header().Get("Content-Range"); got == "" {
		t.Error("no Content-Range on a 206 response")
	}
}

func TestPDFDownload_ConditionalGet(t *testing.T) {
	h, _, token := newDownloadFixture(t)

	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/d/"+token, nil))
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the first response")
	}

	req := httptest.NewRequest(http.MethodGet, "/d/"+token, nil)
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("304 carried a body of %d bytes", rec.Body.Len())
	}
}

func TestPDFDownload_UnknownAndExpiredTokensAre404(t *testing.T) {
	h, store, _ := newDownloadFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/d/nosuchtoken", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown token status = %d, want 404", rec.Code)
	}

	// Expired tokens must be indistinguishable from unknown ones, so
	// probing cannot tell a real token from a guess.
	expired, err := store.MintPDFLink(t.Context(), "gh:1", "out.pdf", time.Nanosecond)
	if err != nil {
		t.Fatalf("MintPDFLink: %v", err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/d/"+expired, nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("expired token status = %d, want 404", rec.Code)
	}
}

// A token whose file has been swept away must 404 rather than 500.
func TestPDFDownload_MissingFileIs404(t *testing.T) {
	h, store, _ := newDownloadFixture(t)

	token, err := store.MintPDFLink(t.Context(), "gh:1", "gone.pdf", time.Hour)
	if err != nil {
		t.Fatalf("MintPDFLink: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/d/"+token, nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// The token is scoped to one user's workspace; a path escaping it must
// not resolve even though the link row itself is valid.
func TestPDFDownload_LinkCannotEscapeTenantRoot(t *testing.T) {
	h, store, _ := newDownloadFixture(t)

	token, err := store.MintPDFLink(t.Context(), "gh:1", "../gh:2/secret.pdf", time.Hour)
	if err != nil {
		t.Fatalf("MintPDFLink: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/d/"+token, nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a traversing path", rec.Code)
	}
}
