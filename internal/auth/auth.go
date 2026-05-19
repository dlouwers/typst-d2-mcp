// Package auth defines how a request is mapped to an identity.Identity
// and provides the two concrete backends the server currently knows
// about: a permissive None backend (always anonymous) and a GitHub
// OAuth-backed backend that mints API keys after GitHub sign-in.
//
// The split lets the rest of the server stay oblivious to the auth
// mechanism: handlers always read identity.FromContext(ctx), and the
// HTTP layer is responsible for putting an Identity there.
package auth

import (
	"net/http"

	"github.com/dlouwers/typst-d2-mcp/internal/identity"
)

// Backend identifies the principal behind an incoming HTTP request.
// Implementations should not perform expensive work for unauthenticated
// requests — the server uses the returned identity (and whether one
// could be established) to decide whether to admit the request.
type Backend interface {
	// IdentifyFromRequest returns the principal behind r, or an error
	// if the request lacks valid credentials. Backends that admit
	// anonymous traffic should return identity.Anonymous() with a nil
	// error.
	IdentifyFromRequest(r *http.Request) (identity.Identity, error)

	// Name is a short identifier used in startup logs.
	Name() string
}

// None is the trivial backend used in stdio and when
// TYPST_D2_MCP_AUTH=none. Every request gets identity.Anonymous().
type None struct{}

// IdentifyFromRequest always succeeds with the anonymous identity.
func (None) IdentifyFromRequest(*http.Request) (identity.Identity, error) {
	return identity.Anonymous(), nil
}

// Name returns the backend's startup label.
func (None) Name() string { return "none" }
