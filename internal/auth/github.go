package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dlouwers/typst-d2-mcp/internal/authdb"
	"github.com/dlouwers/typst-d2-mcp/internal/identity"
)

// ErrMissingBearer is returned when a request to a GitHub-backed
// endpoint omits the Authorization: Bearer header.
var ErrMissingBearer = errors.New("missing or malformed Authorization header")

// GitHubConfig holds the deployment-specific values needed for the
// OAuth dance. Endpoints default to GitHub's production URLs and can
// be overridden for testing.
type GitHubConfig struct {
	ClientID     string
	ClientSecret string
	// PublicURL is this server's externally reachable base URL
	// (scheme + host + optional port). The OAuth redirect URL is
	// derived from it: <PublicURL>/auth/github/callback.
	PublicURL string

	// Endpoints override the upstream URLs used to talk to GitHub.
	// Leave the defaults zero for production; tests inject httptest
	// server URLs here.
	AuthorizeURL string
	TokenURL     string
	UserURL      string

	// HTTPClient is used for outbound calls to GitHub's token and
	// user endpoints. Nil means http.DefaultClient with a 10s
	// timeout.
	HTTPClient *http.Client
}

func (c GitHubConfig) authorizeURL() string {
	if c.AuthorizeURL != "" {
		return c.AuthorizeURL
	}
	return "https://github.com/login/oauth/authorize"
}
func (c GitHubConfig) tokenURL() string {
	if c.TokenURL != "" {
		return c.TokenURL
	}
	return "https://github.com/login/oauth/access_token"
}
func (c GitHubConfig) userURL() string {
	if c.UserURL != "" {
		return c.UserURL
	}
	return "https://api.github.com/user"
}
func (c GitHubConfig) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}
func (c GitHubConfig) redirectURL() string {
	return strings.TrimRight(c.PublicURL, "/") + "/auth/github/callback"
}

// GitHub is a Backend that accepts Bearer API keys minted via GitHub
// OAuth and validated against an authdb.Store.
type GitHub struct {
	Cfg   GitHubConfig
	Store *authdb.Store
}

// IdentifyFromRequest reads the Authorization: Bearer header and looks
// the token up in the Store. Missing or malformed headers return
// ErrMissingBearer (so the HTTP layer can map them to 401); unknown
// tokens return authdb.ErrInvalidKey.
func (g *GitHub) IdentifyFromRequest(r *http.Request) (identity.Identity, error) {
	token := bearerToken(r)
	if token == "" {
		return identity.Identity{}, ErrMissingBearer
	}
	return g.Store.IdentityForKey(r.Context(), token)
}

// Name returns the backend's startup label.
func (*GitHub) Name() string { return "github" }

// ServeLogin handles GET /login. It mints a random state, stashes it
// in a short-lived HttpOnly cookie, and redirects to GitHub's OAuth
// authorize URL.
func (g *GitHub) ServeLogin(w http.ResponseWriter, r *http.Request) {
	state, err := randomState()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "ttd2_oauth_state",
		Value:    state,
		Path:     "/auth/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   300, // 5 minutes
	})
	q := url.Values{}
	q.Set("client_id", g.Cfg.ClientID)
	q.Set("redirect_uri", g.Cfg.redirectURL())
	q.Set("scope", "read:user user:email")
	q.Set("state", state)
	http.Redirect(w, r, g.Cfg.authorizeURL()+"?"+q.Encode(), http.StatusFound)
}

// ServeCallback handles GET /auth/github/callback. It verifies the
// state cookie, exchanges the auth code for a GitHub access token,
// fetches the user, upserts them in authdb, mints an API key, and
// shows the key to the user exactly once.
func (g *GitHub) ServeCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		http.Error(w, "missing state or code", http.StatusBadRequest)
		return
	}
	cookie, err := r.Cookie("ttd2_oauth_state")
	if err != nil || cookie.Value != state {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	// Consume the cookie either way.
	http.SetCookie(w, &http.Cookie{Name: "ttd2_oauth_state", Path: "/auth/", MaxAge: -1})

	ghToken, err := g.exchangeCode(r.Context(), code)
	if err != nil {
		http.Error(w, "code exchange failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	gu, err := g.fetchUser(r.Context(), ghToken)
	if err != nil {
		http.Error(w, "github user fetch failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	userID, err := g.Store.UpsertGitHubUser(r.Context(), gu.ID, gu.Login, gu.Email)
	if err != nil {
		http.Error(w, "user upsert failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	key, err := g.Store.IssueAPIKey(r.Context(), userID)
	if err != nil {
		http.Error(w, "key issue failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeKeyPage(w, gu.Login, key)
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(h) > len(p) && strings.EqualFold(h[:len(p)], p) {
		return strings.TrimSpace(h[len(p):])
	}
	return ""
}

func randomState() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

type githubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Email string `json:"email"`
}

func (g *GitHub) exchangeCode(ctx context.Context, code string) (string, error) {
	form := url.Values{}
	form.Set("client_id", g.Cfg.ClientID)
	form.Set("client_secret", g.Cfg.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", g.Cfg.redirectURL())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.Cfg.tokenURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := g.Cfg.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("github token endpoint %d: %s", resp.StatusCode, string(body))
	}
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if out.Error != "" {
		return "", fmt.Errorf("github oauth error: %s (%s)", out.Error, out.ErrorDesc)
	}
	if out.AccessToken == "" {
		return "", errors.New("github returned empty access_token")
	}
	return out.AccessToken, nil
}

func (g *GitHub) fetchUser(ctx context.Context, ghToken string) (githubUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.Cfg.userURL(), nil)
	if err != nil {
		return githubUser{}, err
	}
	req.Header.Set("Authorization", "Bearer "+ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := g.Cfg.httpClient().Do(req)
	if err != nil {
		return githubUser{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return githubUser{}, fmt.Errorf("github user endpoint %d: %s", resp.StatusCode, string(body))
	}
	var u githubUser
	if err := json.Unmarshal(body, &u); err != nil {
		return githubUser{}, fmt.Errorf("decode user response: %w", err)
	}
	if u.ID == 0 || u.Login == "" {
		return githubUser{}, errors.New("github user response missing id or login")
	}
	return u, nil
}

func writeKeyPage(w http.ResponseWriter, login, key string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>typst-d2-mcp API key</title></head>
<body style="font-family:system-ui,sans-serif;max-width:40rem;margin:2rem auto;padding:0 1rem;line-height:1.5">
<h1>Hello, %s</h1>
<p>Your typst-d2-mcp API key is shown below. <strong>Copy it now — it will not be displayed again.</strong></p>
<pre style="background:#f4f4f4;padding:1rem;border-radius:6px;font-size:1rem;overflow-x:auto">%s</pre>
<p>Configure your MCP client to send this key as <code>Authorization: Bearer &lt;key&gt;</code> on the MCP HTTP endpoint.</p>
</body></html>`, html.EscapeString(login), html.EscapeString(key))
}
