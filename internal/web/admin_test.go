package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dlouwers/typst-d2-mcp/internal/auth"
	"github.com/dlouwers/typst-d2-mcp/internal/authdb"
)

type fixture struct {
	srv       *Server
	handler   http.Handler
	store     *authdb.Store
	codec     *SessionCodec
	workspace string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWithBuild(t, BuildInfo{})
}

func newFixtureWithBuild(t *testing.T, build BuildInfo) *fixture {
	t.Helper()

	store, err := authdb.Open(filepath.Join(t.TempDir(), "auth.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	key, _ := RandomKey()
	codec := NewSessionCodec(key, time.Hour, true)
	ws := t.TempDir()

	srv, err := New(Config{
		Store: store,
		GitHub: &auth.GitHub{
			Cfg: auth.GitHubConfig{
				PublicURL:   "https://mcp.example.test",
				AdminLogins: map[string]bool{"dlouwers": true},
			},
			Store: store,
		},
		Sessions:      codec,
		WorkspaceRoot: ws,
		QuotaDefault:  func() int { return 1 },
		Build:         build,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &fixture{srv: srv, handler: srv.Handler(), store: store, codec: codec, workspace: ws}
}

// as returns a request carrying a valid session for login.
func (f *fixture) as(t *testing.T, login, method, target string, form url.Values) *http.Request {
	t.Helper()
	var r *http.Request
	if form == nil {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if login != "" {
		r.AddCookie(&http.Cookie{Name: SessionCookie, Value: f.codec.encode(login, time.Now())})
	}
	return r
}

func (f *fixture) do(t *testing.T, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, r)
	return rec
}

// seedSignedIn creates a user who has completed the OAuth flow.
func (f *fixture) seedSignedIn(t *testing.T, githubID int64, login string) {
	t.Helper()
	if _, err := f.store.UpsertGitHubUser(t.Context(), githubID, login, login+"@example.com"); err != nil {
		t.Fatalf("UpsertGitHubUser: %v", err)
	}
}

// --- access control -------------------------------------------------

func TestAccess_NonAdminIsForbidden(t *testing.T) {
	f := newFixture(t)

	for _, tc := range []struct {
		name, method, path string
		form               url.Values
	}{
		{"users page", http.MethodGet, "/admin/", nil},
		{"audit page", http.MethodGet, "/admin/audit", nil},
		{"invite", http.MethodPost, "/admin/invite", url.Values{"login": {"someone"}}},
		{"quota", http.MethodPost, "/admin/quota", url.Values{"login": {"someone"}, "mode": {"unlimited"}}},
		{"delete", http.MethodPost, "/admin/delete", url.Values{"login": {"someone"}, "confirm": {"someone"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A valid session for a login that is not an administrator.
			rec := f.do(t, f.as(t, "randomuser", tc.method, tc.path, tc.form))
			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", rec.Code)
			}
		})
	}
}

func TestAccess_UnauthenticatedGETGoesToSignIn(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, f.as(t, "", http.MethodGet, "/admin/", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/admin/signin" {
		t.Errorf("Location = %q, want /admin/signin", got)
	}
}

// A POST with no session must be refused outright rather than
// redirected — a redirect would silently drop the action.
func TestAccess_UnauthenticatedPOSTIsRejected(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, f.as(t, "", http.MethodPost, "/admin/invite", url.Values{"login": {"someone"}}))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	// And nothing happened.
	if invited, _ := f.store.IsInvited(t.Context(), "someone"); invited {
		t.Error("unauthenticated POST created an invite")
	}
}

func TestAccess_TamperedSessionIsRejected(t *testing.T) {
	f := newFixture(t)
	r := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: "forged.value"})

	rec := f.do(t, r)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303 to sign-in", rec.Code)
	}
}

// The sign-in page must be reachable without a session, or nobody could
// ever log in.
func TestAccess_SignInPageIsPublic(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, f.as(t, "", http.MethodGet, "/admin/signin", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Sign in with GitHub") {
		t.Error("sign-in page does not offer GitHub sign-in")
	}
}

// An admin who has never been invited must still get in: the operator
// cannot be locked out of their own server by an empty invites table.
func TestAccess_AdminNeedsNoInvite(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, f.as(t, "dlouwers", http.MethodGet, "/admin/", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// --- pages ----------------------------------------------------------

func TestUsersPage_RendersRows(t *testing.T) {
	f := newFixture(t)
	f.seedSignedIn(t, 42, "octocat")
	if err := f.store.CreateInvite(t.Context(), "dlouwers", "newbie"); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	rec := f.do(t, f.as(t, "dlouwers", http.MethodGet, "/admin/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"octocat", "newbie", "active", "invited"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// Storage has never been measured, so it must not claim a size.
	if strings.Contains(body, "0 B") {
		t.Error("unmeasured storage rendered as 0 B rather than a placeholder")
	}
}

func TestUsersPage_EscapesLogin(t *testing.T) {
	f := newFixture(t)
	f.seedSignedIn(t, 42, `<script>alert(1)</script>`)

	rec := f.do(t, f.as(t, "dlouwers", http.MethodGet, "/admin/", nil))
	body := rec.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("login rendered unescaped")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("escaped login not present at all")
	}
}

func TestAuditPage_RendersEntries(t *testing.T) {
	f := newFixture(t)
	if err := f.store.CreateInvite(t.Context(), "dlouwers", "newbie"); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	rec := f.do(t, f.as(t, "dlouwers", http.MethodGet, "/admin/audit", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, authdb.ActionInvite) || !strings.Contains(body, "newbie") {
		t.Error("audit page does not show the invite action")
	}
}

// Pages must not reference anything the binary does not embed.
func TestPages_ReferenceOnlyLocalAssets(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, f.as(t, "dlouwers", http.MethodGet, "/admin/", nil))

	for _, forbidden := range []string{"https://cdn", "http://cdn", "unpkg.com", "jsdelivr"} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Errorf("page references external asset host %q", forbidden)
		}
	}
	for _, asset := range []string{
		"/admin/static/vendor/stormlantern-tokens.css",
		"/admin/static/vendor/htmx.min.js",
	} {
		if !strings.Contains(rec.Body.String(), asset) {
			t.Errorf("page does not load %q", asset)
		}
		got := f.do(t, f.as(t, "", http.MethodGet, asset, nil))
		if got.Code != http.StatusOK {
			t.Errorf("asset %s: status %d, want 200", asset, got.Code)
		}
	}
}

// --- actions --------------------------------------------------------

func TestInvite_PlainFormRedirectsWithFlash(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/invite",
		url.Values{"login": {"Newbie"}}))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if loc.Path != "/admin/" {
		t.Errorf("redirect path = %q, want /admin/", loc.Path)
	}
	if !strings.Contains(loc.Query().Get("flash"), "Invited newbie") {
		t.Errorf("flash = %q, want an invite confirmation", loc.Query().Get("flash"))
	}
	if invited, _ := f.store.IsInvited(t.Context(), "newbie"); !invited {
		t.Error("invite was not created")
	}
}

func TestInvite_HTMXReturnsFragmentAndFlash(t *testing.T) {
	f := newFixture(t)
	r := f.as(t, "dlouwers", http.MethodPost, "/admin/invite", url.Values{"login": {"newbie"}})
	r.Header.Set("HX-Request", "true")

	rec := f.do(t, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="flash"`) || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Error("no out-of-band flash swap in the htmx response")
	}
	if !strings.Contains(body, `id="users"`) {
		t.Error("htmx response does not carry a refreshed users table")
	}
	if !strings.Contains(body, "newbie") {
		t.Error("refreshed table does not include the new invite")
	}
	// A fragment, not a whole page.
	if strings.Contains(body, "<html") {
		t.Error("htmx response returned a full page")
	}
}

func TestInvite_RequiresLogin(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/invite", url.Values{"login": {"  "}}))

	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Query().Get("level") != string(flashError) {
		t.Errorf("level = %q, want error", loc.Query().Get("level"))
	}
}

func TestQuota_ModesApply(t *testing.T) {
	f := newFixture(t)
	f.seedSignedIn(t, 42, "octocat")
	ctx := t.Context()

	// Fixed.
	f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/quota",
		url.Values{"login": {"octocat"}, "mode": {"fixed"}, "value": {"10"}}))
	if got, _ := f.store.EffectiveQuota(ctx, "gh:42", 1); got != 10 {
		t.Errorf("quota = %d, want 10", got)
	}

	// Unlimited.
	f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/quota",
		url.Values{"login": {"octocat"}, "mode": {"unlimited"}}))
	if got, _ := f.store.EffectiveQuota(ctx, "gh:42", 1); got != 0 {
		t.Errorf("quota = %d, want 0 (unlimited)", got)
	}

	// Back to the deployment default.
	f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/quota",
		url.Values{"login": {"octocat"}, "mode": {"default"}}))
	if got, _ := f.store.EffectiveQuota(ctx, "gh:42", 7); got != 7 {
		t.Errorf("quota = %d, want the default 7", got)
	}
}

func TestQuota_RejectsNonsenseValue(t *testing.T) {
	f := newFixture(t)
	f.seedSignedIn(t, 42, "octocat")

	for _, value := range []string{"", "0", "-3", "many"} {
		rec := f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/quota",
			url.Values{"login": {"octocat"}, "mode": {"fixed"}, "value": {value}}))
		loc, _ := url.Parse(rec.Header().Get("Location"))
		if loc.Query().Get("level") != string(flashError) {
			t.Errorf("value %q accepted, want a validation error", value)
		}
	}
	// None of that should have set an override.
	if got, _ := f.store.EffectiveQuota(t.Context(), "gh:42", 1); got != 1 {
		t.Errorf("quota = %d, want the untouched default 1", got)
	}
}

// Setting a quota on someone who has only been invited has no row to
// write to, and should say so rather than failing opaquely.
func TestQuota_OnPendingInviteExplains(t *testing.T) {
	f := newFixture(t)
	if err := f.store.CreateInvite(t.Context(), "dlouwers", "newbie"); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	rec := f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/quota",
		url.Values{"login": {"newbie"}, "mode": {"unlimited"}}))

	loc, _ := url.Parse(rec.Header().Get("Location"))
	if !strings.Contains(loc.Query().Get("flash"), "has not signed in yet") {
		t.Errorf("flash = %q, want an explanation", loc.Query().Get("flash"))
	}
}

func TestResetToday_ClearsCounter(t *testing.T) {
	f := newFixture(t)
	f.seedSignedIn(t, 42, "octocat")
	ctx := t.Context()

	if err := f.store.IncrementCompile(ctx, "gh:42", utcDate(), 1); err != nil {
		t.Fatalf("IncrementCompile: %v", err)
	}
	f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/reset",
		url.Values{"login": {"octocat"}, "user_id": {"gh:42"}}))

	if err := f.store.IncrementCompile(ctx, "gh:42", utcDate(), 1); err != nil {
		t.Errorf("counter not reset: %v", err)
	}
}

func TestRevokeAccess_RemovesInvite(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if err := f.store.CreateInvite(ctx, "dlouwers", "octocat"); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/revoke",
		url.Values{"login": {"octocat"}}))

	if invited, _ := f.store.IsInvited(ctx, "octocat"); invited {
		t.Error("invite survived revocation")
	}
}

func TestRevokeKeys_InvalidatesKeyButKeepsUser(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	uid, err := f.store.UpsertGitHubUser(ctx, 42, "octocat", "")
	if err != nil {
		t.Fatalf("UpsertGitHubUser: %v", err)
	}
	key, err := f.store.IssueAPIKey(ctx, uid)
	if err != nil {
		t.Fatalf("IssueAPIKey: %v", err)
	}

	f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/revoke-keys",
		url.Values{"login": {"octocat"}}))

	if _, err := f.store.IdentityForKey(ctx, key); err == nil {
		t.Error("revoked key still authenticates")
	}
	rows, _ := f.store.AdminUsers(ctx, utcDate())
	if len(rows) != 1 {
		t.Errorf("user row lost: %+v", rows)
	}
}

// --- delete ---------------------------------------------------------

func TestDelete_RequiresTypedConfirmation(t *testing.T) {
	f := newFixture(t)
	f.seedSignedIn(t, 42, "octocat")

	for _, confirm := range []string{"", "octocatt", "OCTOCA"} {
		rec := f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/delete",
			url.Values{"login": {"octocat"}, "confirm": {confirm}}))
		loc, _ := url.Parse(rec.Header().Get("Location"))
		if !strings.Contains(loc.Query().Get("flash"), "type the login exactly") {
			t.Errorf("confirm %q: flash = %q, want a confirmation prompt",
				confirm, loc.Query().Get("flash"))
		}
	}
	rows, _ := f.store.AdminUsers(t.Context(), utcDate())
	if len(rows) != 1 {
		t.Error("user was deleted without confirmation")
	}
}

func TestDelete_RemovesUserAndWorkspace(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.seedSignedIn(t, 42, "octocat")

	userDir := filepath.Join(f.workspace, "gh:42")
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "out.pdf"), []byte("pdf"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// A second user's files must be untouched.
	otherDir := filepath.Join(f.workspace, "gh:99")
	if err := os.MkdirAll(otherDir, 0o700); err != nil {
		t.Fatalf("mkdir other: %v", err)
	}

	rec := f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/delete",
		url.Values{"login": {"octocat"}, "confirm": {"octocat"}}))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Query().Get("level") != string(flashNotice) {
		t.Fatalf("delete failed: %q", loc.Query().Get("flash"))
	}

	rows, _ := f.store.AdminUsers(ctx, utcDate())
	if len(rows) != 0 {
		t.Errorf("rows after delete = %d, want 0", len(rows))
	}
	if _, err := os.Stat(userDir); !os.IsNotExist(err) {
		t.Error("workspace directory survived the delete")
	}
	if _, err := os.Stat(otherDir); err != nil {
		t.Error("another user's workspace was removed")
	}
}

func TestDelete_UnknownLoginReportsError(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/delete",
		url.Values{"login": {"ghost"}, "confirm": {"ghost"}}))

	loc, _ := url.Parse(rec.Header().Get("Location"))
	if !strings.Contains(loc.Query().Get("flash"), "no such user") {
		t.Errorf("flash = %q, want a not-found message", loc.Query().Get("flash"))
	}
}

// A user id from the database is still not trusted as a path segment.
func TestRemoveUserWorkspace_RefusesEscapingIDs(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	for _, id := range []string{"", ".", "..", "../outside", "gh:1/../../etc", `gh:1\..`} {
		if err := removeUserWorkspace(filepath.Join(root, "workspaces"), id); err == nil {
			t.Errorf("user id %q was accepted", id)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("a refused delete still removed a directory")
	}
}

// --- audit ----------------------------------------------------------

func TestAudit_EveryActionThroughTheUIIsRecorded(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.seedSignedIn(t, 42, "octocat")

	f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/invite", url.Values{"login": {"octocat"}}))
	f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/quota",
		url.Values{"login": {"octocat"}, "mode": {"fixed"}, "value": {"3"}}))
	f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/reset",
		url.Values{"login": {"octocat"}, "user_id": {"gh:42"}}))
	f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/revoke-keys", url.Values{"login": {"octocat"}}))
	f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/revoke", url.Values{"login": {"octocat"}}))
	f.do(t, f.as(t, "dlouwers", http.MethodPost, "/admin/delete",
		url.Values{"login": {"octocat"}, "confirm": {"octocat"}}))

	entries, err := f.store.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 6 {
		t.Fatalf("audit entries = %d, want 6: %+v", len(entries), entries)
	}
	for _, e := range entries {
		if e.ActorLogin != "dlouwers" {
			t.Errorf("actor = %q, want the signed-in admin", e.ActorLogin)
		}
	}
}

// Rendering a row that has an override exercises the pointer deref in
// the template, which the nil-override rows never reach.
func TestUsersPage_RendersQuotaOverrides(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.seedSignedIn(t, 1, "unlimiteduser")
	f.seedSignedIn(t, 2, "cappeduser")
	f.seedSignedIn(t, 3, "inheritsuser") // no override: renders the default

	zero, ten := 0, 10
	if err := f.store.SetQuota(ctx, "dlouwers", "unlimiteduser", &zero); err != nil {
		t.Fatalf("SetQuota unlimited: %v", err)
	}
	if err := f.store.SetQuota(ctx, "dlouwers", "cappeduser", &ten); err != nil {
		t.Fatalf("SetQuota fixed: %v", err)
	}

	rec := f.do(t, f.as(t, "dlouwers", http.MethodGet, "/admin/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"unlimited", "10/day", "default (1/day)"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing quota label %q", want)
		}
	}
}

// The banner exists so an operator can tell which build is answering
// without shelling into the pod — the question that made a stale image
// hard to diagnose. It is server-rendered rather than a custom element
// so it survives with JavaScript off, which is when you most need it.
func TestBanner_ShownWithBuildInfo(t *testing.T) {
	f := newFixtureWithBuild(t, BuildInfo{
		Environment:    "test",
		Version:        "sha-3a6a156",
		GitSHA:         "3a6a156",
		BuildTime:      "2026-08-18T06:19:22Z",
		SchemaRevision: 5,
	})

	rec := f.do(t, f.as(t, "dlouwers", http.MethodGet, "/admin/", nil))
	body := rec.Body.String()
	for _, want := range []string{
		"env-banner", "test", "sha-3a6a156", "3a6a156",
		"2026-08-18T06:19:22Z", "schema r5",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("banner missing %q", want)
		}
	}
}

// Production sets no environment, so the strip must not render at all —
// its presence is the signal.
func TestBanner_HiddenWithoutEnvironment(t *testing.T) {
	f := newFixtureWithBuild(t, BuildInfo{
		Version:        "v1.2.3",
		SchemaRevision: 5,
	})

	rec := f.do(t, f.as(t, "dlouwers", http.MethodGet, "/admin/", nil))
	if strings.Contains(rec.Body.String(), "env-banner") {
		t.Error("banner rendered with no environment set")
	}
}

// A local build has no ldflags, so sha and time are the "unknown"
// placeholders; showing those is worse than showing nothing.
func TestBanner_OmitsUnknownBuildFields(t *testing.T) {
	f := newFixtureWithBuild(t, BuildInfo{
		Environment:    "dev",
		Version:        "dev",
		GitSHA:         "unknown",
		BuildTime:      "unknown",
		SchemaRevision: 5,
	})

	rec := f.do(t, f.as(t, "dlouwers", http.MethodGet, "/admin/", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "env-banner") {
		t.Fatal("banner not rendered")
	}
	if strings.Contains(body, "unknown") {
		t.Error("banner shows \"unknown\" placeholders instead of omitting them")
	}
}

// The sign-in page is where an operator lands when something is wrong,
// so it needs the banner as much as the authenticated pages.
func TestBanner_ShownOnSignInPage(t *testing.T) {
	f := newFixtureWithBuild(t, BuildInfo{Environment: "test", Version: "sha-abc"})

	rec := f.do(t, f.as(t, "", http.MethodGet, "/admin/signin", nil))
	if !strings.Contains(rec.Body.String(), "env-banner") {
		t.Error("sign-in page has no banner")
	}
}

// An expired session used to make every htmx action look inert: htmx
// does not swap error responses, so a 401 produced no visible change.
// HX-Redirect turns it into a trip to the sign-in page.
func TestAccess_ExpiredSessionRedirectsHTMXAction(t *testing.T) {
	f := newFixture(t)

	r := f.as(t, "", http.MethodPost, "/admin/invite", url.Values{"login": {"someone"}})
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: "stale.value"})
	r.Header.Set("HX-Request", "true")
	rec := f.do(t, r)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("HX-Redirect"); got != "/admin/signin" {
		t.Errorf("HX-Redirect = %q, want /admin/signin", got)
	}
}

// A non-htmx POST keeps the plain 401: there is no browser to redirect,
// and a redirect would mask the failure from a script or curl.
func TestAccess_ExpiredSessionPlainPostStays401(t *testing.T) {
	f := newFixture(t)

	rec := f.do(t, f.as(t, "", http.MethodPost, "/admin/invite", url.Values{"login": {"someone"}}))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if rec.Header().Get("HX-Redirect") != "" {
		t.Error("HX-Redirect set on a non-htmx request")
	}
}
