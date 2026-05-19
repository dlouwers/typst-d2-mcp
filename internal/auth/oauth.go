package auth

// OAuth-AS HTTP handlers. Together with github.go these implement the
// MCP-spec OAuth 2.1 + PKCE flow:
//
//   1. /.well-known/oauth-protected-resource    (RFC 9728)
//   2. /.well-known/oauth-authorization-server  (RFC 8414)
//   3. POST /register                            (RFC 7591 Dynamic
//                                                Client Registration)
//   4. GET  /authorize                           (RFC 6749 §4.1 + RFC
//                                                7636 PKCE)
//   5. GET  /auth/github/callback                (defined in github.go;
//                                                now ends in a 302 to
//                                                the MCP client's
//                                                redirect_uri with our
//                                                one-shot code)
//   6. POST /token                               (RFC 6749 §4.1.3)
//
// User authentication is delegated to GitHub. The access token issued
// at /token is the same opaque ttd2_ key shape the manual flow used,
// stored as sha256 in authdb.api_keys — the Bearer middleware
// validates both kinds the same way.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/dlouwers/typst-d2-mcp/internal/authdb"
)

const (
	pathProtectedResource     = "/.well-known/oauth-protected-resource"
	pathAuthorizationServer   = "/.well-known/oauth-authorization-server"
	pathRegister              = "/register"
	pathAuthorize             = "/authorize"
	pathToken                 = "/token"
	pathGitHubCallback        = "/auth/github/callback"

	defaultScope = "mcp"
)

// ServeWellKnownProtectedResource implements RFC 9728 §3. The
// response tells MCP clients which Authorization Server issues tokens
// for this Resource Server.
func (g *GitHub) ServeWellKnownProtectedResource(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimRight(g.Cfg.PublicURL, "/")
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 base,
		"authorization_servers":    []string{base},
		"scopes_supported":         []string{defaultScope},
		"bearer_methods_supported": []string{"header"},
	})
}

// ServeWellKnownAuthorizationServer implements RFC 8414. Advertises
// the endpoints we expose and the small subset of OAuth 2.1 features
// we support — public clients, code grant, S256 PKCE only.
func (g *GitHub) ServeWellKnownAuthorizationServer(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimRight(g.Cfg.PublicURL, "/")
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + pathAuthorize,
		"token_endpoint":                        base + pathToken,
		"registration_endpoint":                 base + pathRegister,
		"scopes_supported":                      []string{defaultScope},
		"response_types_supported":              []string{"code"},
		"response_modes_supported":              []string{"query"},
		"grant_types_supported":                 []string{"authorization_code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
	})
}

// ServeRegister implements RFC 7591 Dynamic Client Registration. v1
// accepts public clients only — no `client_secret` is ever returned.
// MCP clients (Claude.ai etc.) register on first connection and reuse
// the assigned client_id across sessions.
func (g *GitHub) ServeRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ClientName              string   `json:"client_name"`
		RedirectURIs            []string `json:"redirect_uris"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", err.Error())
		return
	}
	if req.ClientName == "" {
		req.ClientName = "unnamed mcp client"
	}
	if len(req.RedirectURIs) == 0 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris must be non-empty")
		return
	}
	for _, u := range req.RedirectURIs {
		if !isValidRedirectURI(u) {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri",
				"redirect URIs must be absolute https URLs (or http://localhost/* for development)")
			return
		}
	}
	if req.TokenEndpointAuthMethod == "" {
		req.TokenEndpointAuthMethod = "none"
	}
	if req.TokenEndpointAuthMethod != "none" {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata",
			"only public clients (token_endpoint_auth_method=none) are supported in v1")
		return
	}
	client, err := g.Store.RegisterClient(r.Context(), req.ClientName, req.RedirectURIs, req.TokenEndpointAuthMethod)
	if err != nil {
		slog.Error("register client failed", "err", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "could not register client")
		return
	}
	slog.Info("oauth client registered",
		"client_id", client.ClientID,
		"client_name", client.ClientName,
	)
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  client.ClientID,
		"client_name":                client.ClientName,
		"redirect_uris":              client.RedirectURIs,
		"token_endpoint_auth_method": client.TokenEndpointAuthMethod,
		"client_id_issued_at":        client.CreatedAt.Unix(),
		"grant_types":                []string{"authorization_code"},
		"response_types":             []string{"code"},
	})
}

// ServeAuthorize implements RFC 6749 §4.1.1 with PKCE (RFC 7636).
// Validates the MCP client's request, stores it as an in-flight
// authorize session, then redirects the user to GitHub for actual
// authentication. The GitHub `state` is the session_id, so the
// callback can find its way back to the right MCP client.
func (g *GitHub) ServeAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	clientID := q.Get("client_id")
	redirectURI := q.Get("redirect_uri")
	responseType := q.Get("response_type")
	codeChallenge := q.Get("code_challenge")
	codeChallengeMethod := q.Get("code_challenge_method")
	state := q.Get("state")
	scope := q.Get("scope")

	if clientID == "" || redirectURI == "" {
		// Per spec: if client_id or redirect_uri is invalid, do NOT
		// redirect — show the error to the user/agent directly. A
		// missing redirect_uri means we have nowhere safe to bounce.
		http.Error(w, "client_id and redirect_uri are required", http.StatusBadRequest)
		return
	}
	client, err := g.Store.LookupClient(r.Context(), clientID)
	if err != nil {
		http.Error(w, "unknown client_id", http.StatusBadRequest)
		return
	}
	if !contains(client.RedirectURIs, redirectURI) {
		http.Error(w, "redirect_uri not registered for this client", http.StatusBadRequest)
		return
	}
	// From here on, errors get redirected back to the MCP client as
	// per RFC 6749 §4.1.2.1.
	if responseType != "code" {
		redirectOAuthError(w, r, redirectURI, state, "unsupported_response_type",
			"only response_type=code is supported")
		return
	}
	if codeChallenge == "" || codeChallengeMethod != "S256" {
		redirectOAuthError(w, r, redirectURI, state, "invalid_request",
			"PKCE is required: code_challenge and code_challenge_method=S256")
		return
	}
	if scope == "" {
		scope = defaultScope
	}
	sessionID, err := g.Store.CreateAuthorizeSession(r.Context(), authdb.AuthorizeSession{
		ClientID:            clientID,
		RedirectURI:         redirectURI,
		CodeChallenge:       codeChallenge,
		CodeChallengeMethod: codeChallengeMethod,
		Scope:               scope,
		ClientState:         state,
	})
	if err != nil {
		slog.Error("create authorize session", "err", err)
		redirectOAuthError(w, r, redirectURI, state, "server_error", "could not start authorization")
		return
	}
	// Hand off to GitHub for user authentication. session_id IS the
	// GitHub `state` — the callback handler reads it directly.
	gq := url.Values{}
	gq.Set("client_id", g.Cfg.ClientID)
	gq.Set("redirect_uri", g.Cfg.redirectURL())
	gq.Set("scope", "read:user user:email")
	gq.Set("state", sessionID)
	http.Redirect(w, r, g.Cfg.authorizeURL()+"?"+gq.Encode(), http.StatusFound)
}

// ServeToken implements RFC 6749 §4.1.3 + RFC 7636 §4.6. Verifies
// the PKCE code_verifier against the stored challenge and mints an
// access token. v1 issues a non-expiring opaque token (matches the
// existing API-key shape); refresh tokens are deferred (#13).
func (g *GitHub) ServeToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "could not parse form body")
		return
	}
	grantType := r.PostFormValue("grant_type")
	code := r.PostFormValue("code")
	redirectURI := r.PostFormValue("redirect_uri")
	clientID := r.PostFormValue("client_id")
	codeVerifier := r.PostFormValue("code_verifier")

	if grantType != "authorization_code" {
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type",
			"only authorization_code is supported")
		return
	}
	if code == "" || clientID == "" || redirectURI == "" || codeVerifier == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request",
			"code, client_id, redirect_uri, and code_verifier are all required")
		return
	}
	consumed, _, err := g.Store.ConsumeAuthorizationCode(r.Context(), code)
	if err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "code not found, expired, or already used")
		return
	}
	if consumed.ClientID != clientID || consumed.RedirectURI != redirectURI {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "client_id or redirect_uri mismatch")
		return
	}
	if !verifyPKCE(codeVerifier, consumed.CodeChallenge, consumed.CodeChallengeMethod) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verifier does not match challenge")
		return
	}
	token, err := g.Store.IssueAPIKey(r.Context(), consumed.UserDBID)
	if err != nil {
		slog.Error("issue token", "err", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "could not issue token")
		return
	}
	slog.Info("oauth token issued",
		"client_id", clientID,
		"user_db_id", consumed.UserDBID,
		"scope", consumed.Scope,
	)
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": token,
		"token_type":   "Bearer",
		"scope":        consumed.Scope,
	})
}

func verifyPKCE(verifier, challenge, method string) bool {
	if method != "S256" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	expected := base64.RawURLEncoding.EncodeToString(sum[:])
	return expected == challenge
}

// isValidRedirectURI matches the MCP spec's relaxed rules: production
// clients must use https; localhost is allowed plain http for dev
// convenience (RFC 8252 §7.3).
func isValidRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		host := u.Hostname()
		return host == "localhost" || host == "127.0.0.1" || host == "::1"
	}
	return false
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func oauthError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, map[string]string{
		"error":             code,
		"error_description": description,
	})
}

// redirectOAuthError bounces an authorization-phase error back to the
// MCP client's redirect_uri carrying the original state, per
// RFC 6749 §4.1.2.1. The MCP client surfaces the error to its
// user — we don't render anything ourselves.
func redirectOAuthError(w http.ResponseWriter, r *http.Request, redirectURI, state, code, description string) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, description, http.StatusBadRequest)
		return
	}
	q := u.Query()
	q.Set("error", code)
	if description != "" {
		q.Set("error_description", description)
	}
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// Silence unused-import linter for symbols only touched in tests.
var (
	_ = errors.Is
	_ = context.Background
	_ = fmt.Sprintf
)
