// Package identity carries the authenticated principal across a request's
// lifetime via context.Context, so handlers can derive per-user
// workspaces and quota without knowing how the principal was
// established.
//
// Anonymous() is the default identity used in stdio mode and when
// TYPST_D2_MCP_AUTH=none: a single shared tenant whose UserID is the
// constant "anonymous". Real users (GitHub OAuth) get a stable UserID
// derived from their numeric GitHub account ID.
package identity

import "context"

// Identity describes the authenticated principal behind a request.
// UserID is the stable, server-side key used for workspace scoping and
// quota; GitHubLogin/Email are display-only and may be empty for the
// anonymous case.
type Identity struct {
	UserID      string
	GitHubLogin string
	Email       string
}

// IsAnonymous reports whether i is the well-known anonymous identity
// (UserID == "anonymous"). It exists so callers don't need to import a
// constant just to test for the default tenant.
func (i Identity) IsAnonymous() bool {
	return i.UserID == anonymousUserID
}

const anonymousUserID = "anonymous"

// Anonymous returns the default identity used when no auth backend is
// active. Its UserID is stable across processes so a self-hosted single
// user's workspace path is deterministic.
func Anonymous() Identity {
	return Identity{UserID: anonymousUserID}
}

type ctxKey struct{}

// WithIdentity returns a copy of ctx that carries id. Handlers attached
// to the MCP server should read identity via FromContext(ctx) rather
// than threading it through every function signature.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext returns the identity attached to ctx, falling back to the
// anonymous identity if none was set. The second return value reports
// whether an identity was actually present, which callers can use to
// distinguish "no auth backend wired" (false) from "anonymous mode"
// (true with IsAnonymous()).
func FromContext(ctx context.Context) (Identity, bool) {
	if v, ok := ctx.Value(ctxKey{}).(Identity); ok {
		return v, true
	}
	return Anonymous(), false
}
