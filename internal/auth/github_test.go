package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
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
		_, _ = w.Write([]byte(`{"id":` + intToStr(githubID) + `,"login":"` + login + `","email":"` + email + `"}`))
	})
	return httptest.NewServer(mux)
}

func intToStr(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte("-"), b...)
	}
	return string(b)
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

func TestGitHub_OAuth_RoundTrip(t *testing.T) {
	ts := fakeGitHub(t, "code-xyz", "gh-token-abc", "octocat", 42, "octo@example.com")
	defer ts.Close()

	g := newGitHubBackend(t, ts.URL)

	// Step 1: /login should redirect to the (fake) GitHub authorize URL
	// with state cookie.
	loginReq := httptest.NewRequest(http.MethodGet, "/login", nil)
	loginResp := httptest.NewRecorder()
	g.ServeLogin(loginResp, loginReq)
	if loginResp.Code != http.StatusFound {
		t.Fatalf("/login status = %d, want 302", loginResp.Code)
	}
	loc := loginResp.Header().Get("Location")
	if !strings.HasPrefix(loc, ts.URL+"/login/oauth/authorize") {
		t.Errorf("redirect location = %q, want authorize URL", loc)
	}
	u, _ := url.Parse(loc)
	state := u.Query().Get("state")
	if state == "" {
		t.Fatal("authorize URL missing state")
	}
	cookies := loginResp.Result().Cookies()
	var stateCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "ttd2_oauth_state" {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("ttd2_oauth_state cookie not set")
	}
	if stateCookie.Value != state {
		t.Errorf("cookie value = %q, state param = %q", stateCookie.Value, state)
	}

	// Step 2: simulate GitHub redirecting back to /callback with code+state.
	cbReq := httptest.NewRequest(http.MethodGet, "/auth/github/callback?code=code-xyz&state="+state, nil)
	cbReq.AddCookie(stateCookie)
	cbResp := httptest.NewRecorder()
	g.ServeCallback(cbResp, cbReq)
	if cbResp.Code != http.StatusOK {
		body, _ := io.ReadAll(cbResp.Result().Body)
		t.Fatalf("/callback status = %d, body=%s", cbResp.Code, body)
	}
	body, _ := io.ReadAll(cbResp.Result().Body)
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "octocat") {
		t.Errorf("response body missing GitHub login: %s", bodyStr)
	}
	// Extract the key from the <pre> block.
	start := strings.Index(bodyStr, "ttd2_")
	if start < 0 {
		t.Fatalf("key not present in response body: %s", bodyStr)
	}
	end := start
	for end < len(bodyStr) && bodyStr[end] != '<' && bodyStr[end] != '\n' {
		end++
	}
	key := bodyStr[start:end]

	// Step 3: the freshly minted key should authenticate via the backend.
	authReq := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	authReq.Header.Set("Authorization", "Bearer "+key)
	id, err := g.IdentifyFromRequest(authReq)
	if err != nil {
		t.Fatalf("auth with new key failed: %v", err)
	}
	if id.GitHubLogin != "octocat" || id.UserID != "gh:42" || id.Email != "octo@example.com" {
		t.Errorf("identity after callback = %+v", id)
	}
}

func TestGitHub_Callback_StateMismatch(t *testing.T) {
	ts := fakeGitHub(t, "c", "t", "octocat", 42, "")
	defer ts.Close()
	g := newGitHubBackend(t, ts.URL)

	req := httptest.NewRequest(http.MethodGet, "/auth/github/callback?code=c&state=evil", nil)
	req.AddCookie(&http.Cookie{Name: "ttd2_oauth_state", Value: "good"})
	resp := httptest.NewRecorder()
	g.ServeCallback(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Errorf("state mismatch should yield 400, got %d", resp.Code)
	}
}

func TestGitHub_Callback_BadCode(t *testing.T) {
	ts := fakeGitHub(t, "correct-code", "t", "octocat", 42, "")
	defer ts.Close()
	g := newGitHubBackend(t, ts.URL)

	req := httptest.NewRequest(http.MethodGet, "/auth/github/callback?code=wrong&state=s", nil)
	req.AddCookie(&http.Cookie{Name: "ttd2_oauth_state", Value: "s"})
	resp := httptest.NewRecorder()
	g.ServeCallback(resp, req)
	if resp.Code != http.StatusBadGateway {
		t.Errorf("bad code should yield 502, got %d", resp.Code)
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

// _ = context.Background silences linters that flag unused imports
// when the file is built in isolation.
var _ = context.Background
