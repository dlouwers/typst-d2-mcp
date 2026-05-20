package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dlouwers/typst-d2-mcp/internal/authdb"
)

// fakeGitHub stands in for github.com during OAuth tests. It implements
// the /login/oauth/access_token and /user endpoints with canned responses.
func fakeGitHub(t *testing.T, code, accessToken string, login string, githubID int64, email string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostFormValue("code") != code {
			http.Error(w, `{"error":"bad_code"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + accessToken + `","token_type":"bearer","scope":"read:user"}`))
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+accessToken {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":` + strconv.FormatInt(githubID, 10) + `,"login":"` + login + `","email":"` + email + `"}`))
	})
	return httptest.NewServer(mux)
}

func newGitHubBackend(t *testing.T, fakeURL string) *GitHub {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "auth.sqlite")
	store, err := authdb.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &GitHub{
		Cfg: GitHubConfig{
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			PublicURL:    "http://localhost:9999",
			AuthorizeURL: fakeURL + "/login/oauth/authorize",
			TokenURL:     fakeURL + "/login/oauth/access_token",
			UserURL:      fakeURL + "/user",
		},
		Store: store,
	}
}

func TestGitHub_IdentifyFromRequest_MissingBearer(t *testing.T) {
	g := newGitHubBackend(t, "")
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	_, err := g.IdentifyFromRequest(r)
	if !errors.Is(err, ErrMissingBearer) {
		t.Errorf("err = %v, want ErrMissingBearer", err)
	}
	r.Header.Set("Authorization", "Basic Zm9v")
	_, err = g.IdentifyFromRequest(r)
	if !errors.Is(err, ErrMissingBearer) {
		t.Errorf("non-Bearer header err = %v, want ErrMissingBearer", err)
	}
}

func TestGitHub_IdentifyFromRequest_InvalidKey(t *testing.T) {
	g := newGitHubBackend(t, "")
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r.Header.Set("Authorization", "Bearer ttd2_does_not_exist")
	_, err := g.IdentifyFromRequest(r)
	if !errors.Is(err, authdb.ErrInvalidKey) {
		t.Errorf("err = %v, want authdb.ErrInvalidKey", err)
	}
}

func TestNone_AlwaysAnonymous(t *testing.T) {
	id, err := None{}.IdentifyFromRequest(httptest.NewRequest(http.MethodPost, "/", nil))
	if err != nil {
		t.Fatalf("None err: %v", err)
	}
	if !id.IsAnonymous() {
		t.Errorf("None returned non-anonymous identity: %+v", id)
	}
}

func TestGitHubConfig_loginAllowed(t *testing.T) {
	open := GitHubConfig{}
	if !open.loginAllowed("anyone") {
		t.Error("empty allowlist should permit any login")
	}

	restricted := GitHubConfig{AllowedLogins: map[string]bool{"dlouwers": true}}
	if !restricted.loginAllowed("dlouwers") {
		t.Error("allowlisted login should be permitted")
	}
	if !restricted.loginAllowed("DLOUWERS") {
		t.Error("allowlist match must be case-insensitive")
	}
	if !restricted.loginAllowed("  dlouwers ") {
		t.Error("allowlist match must tolerate surrounding whitespace")
	}
	if restricted.loginAllowed("octocat") {
		t.Error("non-allowlisted login must be rejected")
	}
}

// A token belonging to a non-allowlisted account must be rejected on
// every request, even though the token itself is valid in the store.
func TestGitHub_IdentifyFromRequest_Allowlist(t *testing.T) {
	g := newGitHubBackend(t, "")
	g.Cfg.AllowedLogins = map[string]bool{"dlouwers": true}
	ctx := t.Context()

	// Mint a key for an allowlisted user and a non-allowlisted one.
	okUID, _ := g.Store.UpsertGitHubUser(ctx, 1, "dlouwers", "d@example.com")
	okKey, _ := g.Store.IssueAPIKey(ctx, okUID)
	badUID, _ := g.Store.UpsertGitHubUser(ctx, 2, "octocat", "o@example.com")
	badKey, _ := g.Store.IssueAPIKey(ctx, badUID)

	okReq := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	okReq.Header.Set("Authorization", "Bearer "+okKey)
	if _, err := g.IdentifyFromRequest(okReq); err != nil {
		t.Errorf("allowlisted user rejected: %v", err)
	}

	badReq := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	badReq.Header.Set("Authorization", "Bearer "+badKey)
	if _, err := g.IdentifyFromRequest(badReq); !errors.Is(err, ErrNotAllowlisted) {
		t.Errorf("non-allowlisted user err = %v, want ErrNotAllowlisted", err)
	}
}

// A non-allowlisted account that completes GitHub sign-in must be
// bounced back to the MCP client with OAuth error=access_denied,
// never reaching the token-mint step.
func TestGitHub_Callback_AllowlistRejection(t *testing.T) {
	ts := fakeGitHub(t, "code-xyz", "gh-token", "octocat", 42, "o@example.com")
	defer ts.Close()
	g := newGitHubBackend(t, ts.URL)
	g.Cfg.AllowedLogins = map[string]bool{"dlouwers": true}

	c, _ := g.Store.RegisterClient(t.Context(), "x", []string{"https://localhost/cb"}, "none")
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", c.ClientID)
	q.Set("redirect_uri", "https://localhost/cb")
	q.Set("state", "cs")
	q.Set("code_challenge", "irrelevant-for-this-test")
	q.Set("code_challenge_method", "S256")
	azResp := httptest.NewRecorder()
	g.ServeAuthorize(azResp, httptest.NewRequest(http.MethodGet, pathAuthorize+"?"+q.Encode(), nil))
	loc, _ := url.Parse(azResp.Header().Get("Location"))
	sid := loc.Query().Get("state")

	cbResp := httptest.NewRecorder()
	g.ServeCallback(cbResp, httptest.NewRequest(http.MethodGet, pathGitHubCallback+"?code=code-xyz&state="+sid, nil))
	if cbResp.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want 302", cbResp.Code)
	}
	cbLoc, _ := url.Parse(cbResp.Header().Get("Location"))
	if got := cbLoc.Query().Get("error"); got != "access_denied" {
		t.Errorf("callback error = %q, want access_denied", got)
	}
	if cbLoc.Query().Get("code") != "" {
		t.Error("non-allowlisted user must not receive an authorization code")
	}
}

// TestOAuth_FullRoundTrip walks the full MCP-spec OAuth dance against
// a mocked GitHub: register → authorize → GitHub callback → token →
// /mcp Bearer use. It's the closest unit-level coverage we have of
// what a real MCP client (e.g. Claude.ai) would do at first connect.
func TestOAuth_FullRoundTrip(t *testing.T) {
	ts := fakeGitHub(t, "gh-code-xyz", "gh-access-abc", "octocat", 42, "octo@example.com")
	defer ts.Close()

	g := newGitHubBackend(t, ts.URL)

	// Step 1: client registers via DCR.
	regBody := `{"client_name":"mock-mcp-client","redirect_uris":["https://localhost/cb"]}`
	regReq := httptest.NewRequest(http.MethodPost, pathRegister, strings.NewReader(regBody))
	regReq.Header.Set("Content-Type", "application/json")
	regResp := httptest.NewRecorder()
	g.ServeRegister(regResp, regReq)
	if regResp.Code != http.StatusCreated {
		t.Fatalf("/register status = %d, body=%s", regResp.Code, regResp.Body.String())
	}
	var reg map[string]any
	if err := json.Unmarshal(regResp.Body.Bytes(), &reg); err != nil {
		t.Fatalf("decode register: %v", err)
	}
	clientID, _ := reg["client_id"].(string)
	if !strings.HasPrefix(clientID, "ttd2cli_") {
		t.Fatalf("unexpected client_id %q", clientID)
	}

	// Step 2: client makes the user-facing /authorize request. PKCE.
	verifier := "the-verifier-must-be-at-least-43-chars-long-okay"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", "https://localhost/cb")
	q.Set("state", "client-state-123")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("scope", "mcp")
	azReq := httptest.NewRequest(http.MethodGet, pathAuthorize+"?"+q.Encode(), nil)
	azResp := httptest.NewRecorder()
	g.ServeAuthorize(azResp, azReq)
	if azResp.Code != http.StatusFound {
		t.Fatalf("/authorize status = %d, body=%s", azResp.Code, azResp.Body.String())
	}
	loc, _ := url.Parse(azResp.Header().Get("Location"))
	if !strings.HasPrefix(loc.String(), ts.URL+"/login/oauth/authorize") {
		t.Fatalf("expected redirect to GitHub authorize, got %s", loc)
	}
	sessionID := loc.Query().Get("state")
	if sessionID == "" || !strings.HasPrefix(sessionID, "ttd2sess_") {
		t.Fatalf("github state should be the authorize session id, got %q", sessionID)
	}

	// Step 3: GitHub redirects back to /auth/github/callback with the
	// session_id as state and a code we'll exchange.
	cbReq := httptest.NewRequest(http.MethodGet, pathGitHubCallback+"?code=gh-code-xyz&state="+sessionID, nil)
	cbResp := httptest.NewRecorder()
	g.ServeCallback(cbResp, cbReq)
	if cbResp.Code != http.StatusFound {
		t.Fatalf("/auth/github/callback status = %d, body=%s", cbResp.Code, cbResp.Body.String())
	}
	cbLoc, _ := url.Parse(cbResp.Header().Get("Location"))
	if cbLoc.Scheme+"://"+cbLoc.Host+cbLoc.Path != "https://localhost/cb" {
		t.Fatalf("callback redirect target = %s, want https://localhost/cb", cbLoc)
	}
	if cbLoc.Query().Get("state") != "client-state-123" {
		t.Errorf("client_state not echoed: %q", cbLoc.Query().Get("state"))
	}
	authzCode := cbLoc.Query().Get("code")
	if authzCode == "" || !strings.HasPrefix(authzCode, "ttd2code_") {
		t.Fatalf("missing/invalid authorization code: %q", authzCode)
	}

	// Step 4: client redeems the code at /token.
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", authzCode)
	form.Set("redirect_uri", "https://localhost/cb")
	form.Set("client_id", clientID)
	form.Set("code_verifier", verifier)
	tokReq := httptest.NewRequest(http.MethodPost, pathToken, strings.NewReader(form.Encode()))
	tokReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokResp := httptest.NewRecorder()
	g.ServeToken(tokResp, tokReq)
	if tokResp.Code != http.StatusOK {
		t.Fatalf("/token status = %d, body=%s", tokResp.Code, tokResp.Body.String())
	}
	var tok map[string]any
	if err := json.Unmarshal(tokResp.Body.Bytes(), &tok); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	accessToken, _ := tok["access_token"].(string)
	if !strings.HasPrefix(accessToken, "ttd2_") {
		t.Fatalf("unexpected access_token shape: %q", accessToken)
	}
	if tt, _ := tok["token_type"].(string); !strings.EqualFold(tt, "Bearer") {
		t.Errorf("token_type = %q, want Bearer", tt)
	}

	// Step 5: the access token authenticates against /mcp.
	mcpReq := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	mcpReq.Header.Set("Authorization", "Bearer "+accessToken)
	id, err := g.IdentifyFromRequest(mcpReq)
	if err != nil {
		t.Fatalf("access token doesn't authenticate: %v", err)
	}
	if id.GitHubLogin != "octocat" || id.UserID != "gh:42" {
		t.Errorf("identity = %+v", id)
	}

	// Step 6: the code is one-shot. Second redemption must fail.
	tok2 := httptest.NewRecorder()
	g.ServeToken(tok2, httptest.NewRequest(http.MethodPost, pathToken, strings.NewReader(form.Encode())))
	tok2.Header().Set("Content-Type", "application/x-www-form-urlencoded")
	if tok2.Code == http.StatusOK {
		t.Errorf("code replay should fail, got 200")
	}
}

func TestOAuth_PKCEMismatch(t *testing.T) {
	ts := fakeGitHub(t, "c", "t", "octocat", 7, "")
	defer ts.Close()
	g := newGitHubBackend(t, ts.URL)

	// Register, authorize, callback with one verifier, then redeem
	// with a different one. /token should reject.
	c, _ := g.Store.RegisterClient(t.Context(), "x", []string{"https://localhost/cb"}, "none")
	sum := sha256.Sum256([]byte("verifier-A-and-must-be-at-least-43-chars-long"))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", c.ClientID)
	q.Set("redirect_uri", "https://localhost/cb")
	q.Set("state", "s")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	azResp := httptest.NewRecorder()
	g.ServeAuthorize(azResp, httptest.NewRequest(http.MethodGet, pathAuthorize+"?"+q.Encode(), nil))
	loc, _ := url.Parse(azResp.Header().Get("Location"))
	sid := loc.Query().Get("state")
	cb := httptest.NewRecorder()
	g.ServeCallback(cb, httptest.NewRequest(http.MethodGet, pathGitHubCallback+"?code=c&state="+sid, nil))
	cbLoc, _ := url.Parse(cb.Header().Get("Location"))
	code := cbLoc.Query().Get("code")

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", "https://localhost/cb")
	form.Set("client_id", c.ClientID)
	form.Set("code_verifier", "verifier-B-totally-different-but-also-43-chars-of-text")
	tokResp := httptest.NewRecorder()
	tokReq := httptest.NewRequest(http.MethodPost, pathToken, strings.NewReader(form.Encode()))
	tokReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	g.ServeToken(tokResp, tokReq)
	if tokResp.Code == http.StatusOK {
		t.Errorf("PKCE mismatch should fail, got 200: %s", tokResp.Body.String())
	}
}

func TestOAuth_Register_RejectsBadRedirectURI(t *testing.T) {
	g := newGitHubBackend(t, "")
	cases := []string{
		`{"client_name":"x","redirect_uris":["http://example.com/cb"]}`,         // plain http, not localhost
		`{"client_name":"x","redirect_uris":["not-a-url"]}`,                    // not absolute
		`{"client_name":"x","redirect_uris":[]}`,                                // empty
		`{"client_name":"x","redirect_uris":["https://x"],"token_endpoint_auth_method":"client_secret_basic"}`, // confidential client
	}
	for _, body := range cases {
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, pathRegister, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		g.ServeRegister(resp, req)
		if resp.Code/100 != 4 {
			t.Errorf("expected 4xx for %s, got %d: %s", body, resp.Code, resp.Body.String())
		}
	}
}

func TestOAuth_WellKnown(t *testing.T) {
	g := newGitHubBackend(t, "")
	g.Cfg.PublicURL = "https://example.test"

	w := httptest.NewRecorder()
	g.ServeWellKnownProtectedResource(w, httptest.NewRequest(http.MethodGet, pathProtectedResource, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var pr map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &pr)
	if pr["resource"] != "https://example.test" {
		t.Errorf("resource = %v", pr["resource"])
	}

	w2 := httptest.NewRecorder()
	g.ServeWellKnownAuthorizationServer(w2, httptest.NewRequest(http.MethodGet, pathAuthorizationServer, nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("status %d", w2.Code)
	}
	var as map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &as)
	want := map[string]string{
		"authorization_endpoint": "https://example.test/authorize",
		"token_endpoint":         "https://example.test/token",
		"registration_endpoint":  "https://example.test/register",
		"issuer":                 "https://example.test",
	}
	for k, v := range want {
		if as[k] != v {
			t.Errorf("%s = %v, want %v", k, as[k], v)
		}
	}
}
