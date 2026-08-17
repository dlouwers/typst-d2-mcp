package web

// Dual-mode replies for action handlers.
//
// Every admin action works two ways. Under htmx the response carries an
// out-of-band swap of the page's #flash element plus a fresh users
// table, so the page updates without a reload. Without htmx — JavaScript
// off, or the custom elements failing to upgrade — the same handler
// redirects back to /admin with the message in the query string, which
// the page renders as a banner. The forms are ordinary <form
// method="post"> elements either way, so nothing depends on scripting.

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
)

type ctxKey struct{}

// withAdmin stores the acting admin's login on the request context.
func withAdmin(ctx context.Context, login string) context.Context {
	return context.WithValue(ctx, ctxKey{}, login)
}

// adminLogin reads the acting admin's login. Only called from handlers
// behind requireAdmin, which always sets it.
func adminLogin(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}

// flashLevel is the visual category of an action result, mapping to the
// <sl-alert variant="..."> attribute.
type flashLevel string

const (
	flashNotice flashLevel = "notice"
	flashError  flashLevel = "error"
)

// isHTMX reports whether htmx issued the request. htmx sets HX-Request
// on everything it sends.
func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// writeFlashOOB writes an out-of-band swap of #flash. The message is
// HTML-escaped: it can contain a GitHub login typed by the admin, or an
// error string derived from one.
func writeFlashOOB(w http.ResponseWriter, level flashLevel, msg string) {
	if msg == "" {
		fmt.Fprint(w, `<div id="flash" hx-swap-oob="true"></div>`)
		return
	}
	fmt.Fprintf(w,
		`<div id="flash" hx-swap-oob="true"><sl-alert variant="%s">%s</sl-alert></div>`,
		html.EscapeString(string(level)),
		html.EscapeString(msg),
	)
}

// respondAction is the reply for every mutating admin handler: an
// updated users table plus a flash for htmx, or a redirect carrying the
// message for a plain form post.
func (s *Server) respondAction(w http.ResponseWriter, r *http.Request, level flashLevel, msg string) {
	if !isHTMX(r) {
		q := url.Values{}
		q.Set("flash", msg)
		q.Set("level", string(level))
		http.Redirect(w, r, "/admin/?"+q.Encode(), http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	writeFlashOOB(w, level, msg)
	s.renderUsersFragment(w, r)
}

// renderUsersFragment writes the users table on its own, for htmx to
// swap into the page.
func (s *Server) renderUsersFragment(w http.ResponseWriter, r *http.Request) {
	vm, err := s.usersViewModel(r)
	if err != nil {
		slog.Error("admin: build users fragment", "err", err)
		fmt.Fprint(w, `<sl-alert variant="error">could not load users</sl-alert>`)
		return
	}
	if err := s.fragments.ExecuteTemplate(w, "users-table", vm); err != nil {
		slog.Error("admin: render users fragment", "err", err)
	}
}
