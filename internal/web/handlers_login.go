package web

// Browser login for the admin UI.
//
// This shares the server's GitHub OAuth app — and therefore its single
// registered callback URL — with the MCP authorization-server flow. The
// two are told apart by the `state` parameter: an admin login carries
// StatePrefix, an MCP client authorization carries an authorize-session
// id. main.go routes /auth/github/callback accordingly.

import (
	"log/slog"
	"net/http"
)

// loginVM is the view model for the sign-in page.
type loginVM struct {
	pageVM
	Reason string
}

// handleSignInPage renders the "sign in with GitHub" landing page. It
// is deliberately a page rather than an immediate redirect so that an
// expired session does not silently bounce a browser to github.com.
func (s *Server) handleSignInPage(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, "login", loginVM{
		pageVM: pageVM{Title: "Sign in — typst-d2-mcp admin"},
		Reason: r.URL.Query().Get("reason"),
	})
}

// handleLoginStart begins the GitHub round trip.
func (s *Server) handleLoginStart(w http.ResponseWriter, r *http.Request) {
	state, err := RandomState()
	if err != nil {
		slog.Error("admin login: generate state", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.cfg.Sessions.IssueState(w, state)
	http.Redirect(w, r, s.cfg.GitHub.AuthCodeURL(state), http.StatusFound)
}

// CompleteLogin finishes an admin browser login. main.go calls it from
// /auth/github/callback when the state carries StatePrefix.
//
// Non-admins are refused here rather than given a session: a session is
// only ever issued to a login the deployment names as an administrator.
func (s *Server) CompleteLogin(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		http.Error(w, "missing state or code", http.StatusBadRequest)
		return
	}
	if !s.cfg.Sessions.CheckState(w, r, state) {
		// Either a forged callback or a stale tab. Send them back to
		// start rather than explaining which.
		slog.Warn("admin login: state mismatch", "remote", r.RemoteAddr)
		http.Redirect(w, r, "/admin/signin?reason=expired", http.StatusSeeOther)
		return
	}

	user, err := s.cfg.GitHub.ExchangeCodeForUser(r.Context(), code)
	if err != nil {
		slog.Error("admin login: github exchange", "err", err)
		http.Redirect(w, r, "/admin/signin?reason=github", http.StatusSeeOther)
		return
	}
	if !s.cfg.GitHub.Cfg.IsAdmin(user.Login) {
		slog.Warn("admin login rejected: not an admin", "login", user.Login)
		http.Redirect(w, r, "/admin/signin?reason=denied", http.StatusSeeOther)
		return
	}

	// Record the sign-in like any other, so an admin who has never used
	// the MCP surface still appears in the user list.
	if _, err := s.cfg.Store.UpsertGitHubUser(r.Context(), user.ID, user.Login, user.Email); err != nil {
		slog.Error("admin login: upsert user", "login", user.Login, "err", err)
		http.Redirect(w, r, "/admin/signin?reason=internal", http.StatusSeeOther)
		return
	}

	s.cfg.Sessions.Issue(w, user.Login)
	slog.Info("admin login", "login", user.Login)
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}
