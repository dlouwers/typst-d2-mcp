// Package web serves the /admin UI: inviting users, setting per-user
// quota, revoking access and keys, deleting users, and reading the
// audit log.
//
// Conventions follow the sibling investor-buddy project: server-rendered
// html/template pages, htmx for interactions, vendored Stormlantern
// design-system assets embedded in the binary, and action handlers that
// reply in dual mode — htmx clients get an out-of-band flash fragment,
// plain form posts get a redirect with a banner. That dual mode is what
// makes the UI work with JavaScript disabled; nothing here depends on
// the custom elements upgrading.
package web

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"time"

	"github.com/dlouwers/typst-d2-mcp/internal/auth"
	"github.com/dlouwers/typst-d2-mcp/internal/authdb"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

// Config wires the admin UI to the rest of the server.
type Config struct {
	Store    *authdb.Store
	GitHub   *auth.GitHub
	Sessions *SessionCodec

	// WorkspaceRoot is the tenant workspace root, used to delete a
	// user's directory when their account is deleted. Empty means the
	// deployment has no server-owned workspace and the filesystem half
	// of a delete is skipped.
	WorkspaceRoot string

	// QuotaDefault reports the deployment-wide default a user inherits
	// when they have no override.
	QuotaDefault func() int

	// WorkspaceBudgetDefault reports the deployment-wide storage budget
	// (bytes) a workspace inherits when it has no override. 0 means no
	// cumulative budget.
	WorkspaceBudgetDefault func() int64

	// Build identifies the running binary in the admin UI's banner.
	Build BuildInfo
}

// BuildInfo is what the banner shows so an operator can tell which
// build is answering, without shelling into the pod. Environment is the
// switch: empty renders no banner at all, so production stays plain and
// the loud strip means "not production".
type BuildInfo struct {
	Environment    string
	Version        string
	GitSHA         string
	BuildTime      string
	SchemaRevision int
}

// Server renders and handles the admin UI.
type Server struct {
	cfg       Config
	pages     map[string]*template.Template
	fragments *template.Template
}

// New parses templates and returns a ready Server.
func New(cfg Config) (*Server, error) {
	if cfg.Store == nil || cfg.GitHub == nil || cfg.Sessions == nil {
		return nil, fmt.Errorf("web: Store, GitHub and Sessions are required")
	}
	if cfg.QuotaDefault == nil {
		cfg.QuotaDefault = func() int { return 0 }
	}
	if cfg.WorkspaceBudgetDefault == nil {
		cfg.WorkspaceBudgetDefault = func() int64 { return 0 }
	}
	s := &Server{cfg: cfg, pages: map[string]*template.Template{}}

	for _, page := range []string{"users", "audit", "login"} {
		tmpl, err := template.New("").Funcs(templateFuncs()).ParseFS(templatesFS,
			"templates/base.tmpl", "templates/partials.tmpl", "templates/"+page+".tmpl")
		if err != nil {
			return nil, fmt.Errorf("parse %s template: %w", page, err)
		}
		s.pages[page] = tmpl
	}
	// The fragment set is partials alone: htmx action responses re-render
	// the users table, which lives there precisely so both this set and
	// every page set can emit it.
	fragments, err := template.New("").Funcs(templateFuncs()).ParseFS(templatesFS,
		"templates/partials.tmpl")
	if err != nil {
		return nil, fmt.Errorf("parse fragments: %w", err)
	}
	s.fragments = fragments
	return s, nil
}

// Handler returns the /admin routes. Mount it on the main listener so
// the UI shares the existing ingress and TLS.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Unauthenticated: the login entry point and its assets, or nobody
	// could ever reach the authenticated half.
	mux.HandleFunc("GET /admin/login", s.handleLoginStart)
	mux.HandleFunc("GET /admin/signin", s.handleSignInPage)

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(fmt.Sprintf("web: static subtree: %v", err)) // embed is compile-time; this cannot fail at runtime
	}
	mux.Handle("GET /admin/static/", http.StripPrefix("/admin/static/", http.FileServerFS(static)))

	mux.Handle("GET /admin/{$}", s.requireAdmin(http.HandlerFunc(s.handleUsers)))
	mux.Handle("GET /admin/audit", s.requireAdmin(http.HandlerFunc(s.handleAudit)))
	mux.Handle("POST /admin/logout", s.requireAdmin(http.HandlerFunc(s.handleLogout)))

	for path, handler := range map[string]http.HandlerFunc{
		"POST /admin/invite":      s.handleInvite,
		"POST /admin/quota":       s.handleSetQuota,
		"POST /admin/budget":      s.handleSetBudget,
		"POST /admin/reset":       s.handleResetToday,
		"POST /admin/revoke":      s.handleRevokeAccess,
		"POST /admin/revoke-keys": s.handleRevokeKeys,
		"POST /admin/delete":      s.handleDeleteUser,
	} {
		mux.Handle(path, s.requireAdmin(handler))
	}
	return mux
}

// requireAdmin gates a handler on a valid session belonging to a
// configured admin login. A browser with no session is sent to sign in;
// anything else is refused outright rather than redirected, so a
// non-admin cannot be bounced through a login loop.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		login, ok := s.cfg.Sessions.Login(r)
		if !ok {
			// htmx does not swap error responses, so a bare 401 leaves the
			// button looking inert — which is exactly how an expired
			// session presented: "Save quota fails", with nothing on
			// screen to say why. HX-Redirect is honoured whatever the
			// status, so the browser goes to sign in instead.
			if isHTMX(r) {
				w.Header().Set("HX-Redirect", "/admin/signin")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if r.Method == http.MethodGet {
				http.Redirect(w, r, "/admin/signin", http.StatusSeeOther)
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !s.cfg.GitHub.Cfg.IsAdmin(login) {
			http.Error(w, "forbidden: not an administrator", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(withAdmin(r.Context(), login)))
	})
}

// templateFuncs are the formatting helpers the templates rely on.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"quotaLabel":  quotaLabel,
		"quotaNumber": quotaNumber,
		"quotaValue":  quotaValue,
		"budgetLabel": budgetLabel,
		"budgetValue": budgetValue,
		"bytesLabel":  bytesLabel,
		"agoLabel":    agoLabel,
		"deref":       func(n *int) int { return *n },
		"deref64":     func(n *int64) int64 { return *n },
	}
}

// budgetLabel renders a workspace's effective storage budget for display.
func budgetLabel(override *int64, def int64) string {
	if override == nil {
		return fmt.Sprintf("default (%s)", budgetNumber(def))
	}
	return budgetNumber(*override)
}

func budgetNumber(n int64) string {
	if n <= 0 {
		return "unlimited"
	}
	return bytesLabel(&n)
}

// budgetValue is the number of bytes to prefill the "fixed" edit field
// with. An override of 0 means unlimited, not a fixed budget of zero, so
// it prefills nothing — see quotaValue for why an invalid number input
// would otherwise block the form.
func budgetValue(override *int64) string {
	if override == nil || *override == 0 {
		return ""
	}
	return fmt.Sprintf("%d", *override)
}

// quotaLabel renders a user's effective quota for display.
func quotaLabel(override *int, def int) string {
	if override == nil {
		return fmt.Sprintf("default (%s)", quotaNumber(def))
	}
	return quotaNumber(*override)
}

func quotaNumber(n int) string {
	if n <= 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d/day", n)
}

// quotaValue is the number to prefill the "fixed" edit field with.
//
// An override of 0 means unlimited, not a fixed quota of zero, so it
// prefills nothing. Returning "0" here put an invalid value in a
// number input and made the browser refuse to submit the form at all —
// including when Default or Unlimited was the selected mode.
func quotaValue(override *int) string {
	if override == nil || *override == 0 {
		return ""
	}
	return fmt.Sprintf("%d", *override)
}

// bytesLabel renders a byte count, or a placeholder when the sweeper
// has not measured this user yet — "not yet computed" is honest where
// "0 B" would be a lie.
func bytesLabel(b *int64) string {
	if b == nil {
		return "—"
	}
	const unit = 1024
	n := *b
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// agoLabel renders a timestamp as a coarse relative age.
func agoLabel(t *time.Time) string {
	if t == nil {
		return "never"
	}
	d := time.Since(*t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hr ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}
