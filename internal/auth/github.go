package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

// ErrNotAllowlisted is returned when a valid token belongs to a
// GitHub account that is not on the configured AllowedLogins set.
// Enforced on every request, so removing someone from the allowlist
// invalidates their existing tokens immediately.
var ErrNotAllowlisted = errors.New("github account not on the allowlist")

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

	// AllowedLogins, when non-empty, restricts the server to the
	// listed GitHub logins (case-insensitive). Any other account is
	// turned away both at OAuth callback time and on every /mcp
	// request. Empty means open to any GitHub account — the public
	// free-tier posture. Used to lock the test environment to the
	// operator only.
	AllowedLogins map[string]bool
}

// loginAllowed reports whether the GitHub login may use this server.
// An empty AllowedLogins set means "no restriction".
func (c GitHubConfig) loginAllowed(login string) bool {
	if len(c.AllowedLogins) == 0 {
		return true
	}
	return c.AllowedLogins[strings.ToLower(strings.TrimSpace(login))]
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
	id, err := g.Store.IdentityForKey(r.Context(), token)
	if err != nil {
		return identity.Identity{}, err
	}
	// Allowlist is authoritative on every request — a token minted
	// before the allowlist was tightened stops working at once.
	if !g.Cfg.loginAllowed(id.GitHubLogin) {
		return identity.Identity{}, ErrNotAllowlisted
	}
	return id, nil
}

// Name returns the backend's startup label.
func (*GitHub) Name() string { return "github" }

// ServeCallback handles GET /auth/github/callback. The `state`
// parameter is the authorize-session id created by ServeAuthorize in
// oauth.go — we look it up to find the MCP client we're authorizing,
// exchange the GitHub code for the user, mint a one-shot
// authorization code bound to the original PKCE challenge, and
// redirect the user's browser back to the MCP client's redirect_uri
// with that code. The MCP client then calls /token to swap it for an
// access token.
func (g *GitHub) ServeCallback(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if sessionID == "" || code == "" {
		http.Error(w, "missing state or code", http.StatusBadRequest)
		return
	}
	session, err := g.Store.ConsumeAuthorizeSession(r.Context(), sessionID)
	if err != nil {
		// Don't redirect: we have no trusted redirect_uri to bounce
		// to (the session was the source of that trust).
		slog.Warn("authorize session missing", "session_id", sessionID, "err", err)
		http.Error(w, "authorize session not found or expired", http.StatusBadRequest)
		return
	}

	ghToken, err := g.exchangeCode(r.Context(), code)
	if err != nil {
		redirectOAuthError(w, r, session.RedirectURI, session.ClientState,
			"server_error", "code exchange failed")
		return
	}
	gu, err := g.fetchUser(r.Context(), ghToken)
	if err != nil {
		redirectOAuthError(w, r, session.RedirectURI, session.ClientState,
			"server_error", "github user fetch failed")
		return
	}
	// Reject non-allowlisted accounts at sign-in time with a clean
	// OAuth access_denied — they never get a token. The check is
	// repeated on every request in IdentifyFromRequest, so this is
	// the friendly early gate rather than the authoritative one.
	if !g.Cfg.loginAllowed(gu.Login) {
		slog.Warn("oauth login rejected: not allowlisted", "login", gu.Login, "github_id", gu.ID)
		redirectOAuthError(w, r, session.RedirectURI, session.ClientState,
			"access_denied", "this GitHub account is not authorised for this server")
		return
	}
	userDBID, err := g.Store.UpsertGitHubUser(r.Context(), gu.ID, gu.Login, gu.Email)
	if err != nil {
		slog.Error("upsert user", "err", err)
		redirectOAuthError(w, r, session.RedirectURI, session.ClientState,
			"server_error", "user upsert failed")
		return
	}
	authzCode, err := g.Store.MintAuthorizationCode(r.Context(), authdb.AuthorizationCode{
		UserDBID:            userDBID,
		ClientID:            session.ClientID,
		RedirectURI:         session.RedirectURI,
		CodeChallenge:       session.CodeChallenge,
		CodeChallengeMethod: session.CodeChallengeMethod,
		Scope:               session.Scope,
	})
	if err != nil {
		slog.Error("mint authorization code", "err", err)
		redirectOAuthError(w, r, session.RedirectURI, session.ClientState,
			"server_error", "could not mint authorization code")
		return
	}
	slog.Info("oauth authorize ok",
		"client_id", session.ClientID,
		"user", gu.Login,
		"github_id", gu.ID,
	)
	u, err := url.Parse(session.RedirectURI)
	if err != nil {
		http.Error(w, "invalid redirect_uri in session", http.StatusInternalServerError)
		return
	}
	q := u.Query()
	q.Set("code", authzCode)
	if session.ClientState != "" {
		q.Set("state", session.ClientState)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(h) > len(p) && strings.EqualFold(h[:len(p)], p) {
		return strings.TrimSpace(h[len(p):])
	}
	return ""
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

