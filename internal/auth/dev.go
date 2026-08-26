package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/dlouwers/typst-d2-mcp/internal/identity"
)

// Dev identifies a request by a header naming a user, with no
// credentials at all. It exists so a developer — or an agent under
// test — can BE a particular person without a browser OAuth round trip,
// which is otherwise impossible to automate.
//
// The identities are real. Dev upserts the named login into the users
// table, so the caller gets a genuine user row, a personal namespace,
// their own workspace and their own quota. That is the point: an auth
// mode that hands out a fake identity would exercise a code path
// nothing in production uses, and would prove nothing about the paths
// that matter — membership, ownership, publishing.
//
// It is not a "bypass" in the sense of weakening the GitHub backend.
// It is a separate backend selected by configuration, which is what the
// Backend interface is for; nothing about GitHub sign-in changes.
//
// Two guards keep it where it belongs. Selecting it is explicit
// (TYPST_D2_MCP_AUTH=dev — never a fallback, never a default), and
// RequireLoopback refuses a non-loopback bind, so leaving it on cannot
// quietly expose a server to a network.
type Dev struct {
	// Store upserts and looks up the named user. Required.
	Store DevStore

	// DefaultLogin is used when a request names nobody. Empty means an
	// unnamed request is anonymous, which keeps the "no header" case
	// honest rather than silently privileged.
	DefaultLogin string

	// AdminLogins may reach the admin UI, mirroring the GitHub backend's
	// allowlist so the same deployment config works in both modes.
	AdminLogins map[string]bool
}

// DevStore is the slice of authdb.Store that Dev needs.
type DevStore interface {
	UpsertGitHubUser(ctx context.Context, githubID int64, login, email string) (int64, error)
}

// DevUserHeader names the identity a request should be treated as.
const DevUserHeader = "X-Dev-User"

// IdentifyFromRequest resolves the header (or the default) to a real
// user row.
//
// A login maps to a stable synthetic GitHub id, so "octocat" is the
// same person across restarts and across passes — workspaces, quota and
// namespaces all persist for them the way they would for a real
// account. Without that stability every run would be a new tenant and
// nothing about returning users could be tested.
func (d *Dev) IdentifyFromRequest(r *http.Request) (identity.Identity, error) {
	login := strings.ToLower(strings.TrimSpace(r.Header.Get(DevUserHeader)))
	if login == "" {
		login = strings.ToLower(strings.TrimSpace(d.DefaultLogin))
	}
	if login == "" {
		return identity.Anonymous(), nil
	}
	if !validDevLogin(login) {
		return identity.Identity{}, fmt.Errorf("invalid %s header %q", DevUserHeader, login)
	}
	if d.Store == nil {
		return identity.Identity{}, fmt.Errorf("dev auth requires a store")
	}

	id := devGitHubID(login)
	if _, err := d.Store.UpsertGitHubUser(r.Context(), id, login, login+"@dev.invalid"); err != nil {
		return identity.Identity{}, fmt.Errorf("upsert dev user %q: %w", login, err)
	}
	return identity.Identity{
		UserID:      "gh:" + strconv.FormatInt(id, 10),
		GitHubLogin: login,
		Email:       login + "@dev.invalid",
	}, nil
}

// Name returns the backend's startup label. It says "dev" loudly on
// purpose: this string ends up in the startup log an operator reads.
func (d *Dev) Name() string { return "dev (NO AUTHENTICATION — identity from " + DevUserHeader + ")" }

// IsAdmin reports whether a login administers this server.
func (d *Dev) IsAdmin(login string) bool {
	return d.AdminLogins[strings.ToLower(strings.TrimSpace(login))]
}

// devGitHubID maps a login to a stable id in a range real GitHub
// accounts do not occupy, so a dev user can never collide with a real
// one in a database that has seen both.
func devGitHubID(login string) int64 {
	const devIDBase = 1 << 40
	var h int64 = 1469598103934665603 // FNV-1a offset basis, 64-bit
	for i := 0; i < len(login); i++ {
		h ^= int64(login[i])
		h *= 1099511628211
	}
	if h < 0 {
		h = -h
	}
	return devIDBase + h%(1<<30)
}

// validDevLogin keeps the header to GitHub's own login grammar, so a
// dev identity cannot be something a real one could never be.
func validDevLogin(s string) bool {
	if len(s) == 0 || len(s) > 39 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && i != 0 && i != len(s)-1:
		default:
			return false
		}
	}
	return true
}

// RequireLoopback reports an error unless addr binds only the loopback
// interface.
//
// Dev auth grants any identity to anyone who can reach the port, so a
// non-loopback bind is not a configuration to warn about — it is one to
// refuse. The check is on the address rather than on a promise in the
// documentation, because the failure mode is somebody leaving
// AUTH=dev set in an environment where it stops being harmless.
func RequireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// No port separator: treat the whole thing as a host.
		host = addr
	}
	host = strings.TrimSpace(host)
	switch host {
	case "localhost":
		return nil
	case "", "0.0.0.0", "[::]", "::":
		return fmt.Errorf("refusing to serve dev auth on %q: it accepts connections from "+
			"any interface, and dev auth grants any identity without credentials. "+
			"Bind 127.0.0.1 instead", addr)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("refusing to serve dev auth on %q: dev auth grants any identity "+
			"without credentials, so it may only bind the loopback interface", addr)
	}
	return nil
}
