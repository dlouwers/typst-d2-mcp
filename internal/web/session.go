package web

// Browser sessions for the admin UI.
//
// The MCP surface authenticates with Bearer API keys, which a browser
// cannot attach to a plain navigation, so the admin UI needs its own
// credential. It is a signed cookie holding the GitHub login and an
// expiry — no server-side session table, because there is nothing to
// store beyond what fits in the cookie.
//
// SameSite=Lax is the CSRF defence: browsers do not attach Lax cookies
// to cross-site POSTs, so a form on another origin cannot drive an
// admin action. (Strict would also block the cookie on the redirect
// back from GitHub, which is the flow that sets it.)

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// SessionCookie holds the signed admin session.
	SessionCookie = "ttd2_admin_session"
	// StateCookie holds the OAuth state nonce for the duration of the
	// GitHub round trip.
	StateCookie = "ttd2_admin_state"

	// StatePrefix marks an admin login's OAuth state so the shared
	// /auth/github/callback can tell it apart from an MCP client's
	// authorize-session id (which carries the ttd2sess_ prefix).
	StatePrefix = "ttd2adm_"
)

// SessionCodec signs and verifies session cookie values.
type SessionCodec struct {
	key    []byte
	ttl    time.Duration
	secure bool
}

// NewSessionCodec returns a codec keyed by key. secure controls the
// Secure cookie attribute, which must be off for plain-http local
// development or the browser will drop the cookie entirely.
func NewSessionCodec(key []byte, ttl time.Duration, secure bool) *SessionCodec {
	return &SessionCodec{key: key, ttl: ttl, secure: secure}
}

// RandomKey returns a fresh signing key, used when the operator has not
// configured one. Sessions signed with it do not survive a restart.
func RandomKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate session key: %w", err)
	}
	return key, nil
}

// RandomState returns an OAuth state nonce carrying StatePrefix.
func RandomState() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate state: %w", err)
	}
	return StatePrefix + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// IsAdminState reports whether an OAuth state value belongs to an admin
// browser login rather than an MCP client authorization.
func IsAdminState(state string) bool {
	return strings.HasPrefix(state, StatePrefix)
}

// encode returns a signed "<payload>.<mac>" value for login, expiring
// ttl from now.
func (c *SessionCodec) encode(login string, now time.Time) string {
	payload := fmt.Sprintf("%s|%d", login, now.Add(c.ttl).Unix())
	b := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return b + "." + c.sign(b)
}

// decode verifies a cookie value and returns the login it carries.
// Returns ok=false for anything tampered with, malformed, or expired —
// the caller cannot distinguish, and does not need to.
func (c *SessionCodec) decode(value string, now time.Time) (string, bool) {
	b, mac, found := strings.Cut(value, ".")
	if !found {
		return "", false
	}
	if !hmac.Equal([]byte(mac), []byte(c.sign(b))) {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(b)
	if err != nil {
		return "", false
	}
	login, expiry, found := strings.Cut(string(raw), "|")
	if !found || login == "" {
		return "", false
	}
	unix, err := strconv.ParseInt(expiry, 10, 64)
	if err != nil {
		return "", false
	}
	if now.After(time.Unix(unix, 0)) {
		return "", false
	}
	return login, true
}

func (c *SessionCodec) sign(payload string) string {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Issue sets the session cookie for login.
func (c *SessionCodec) Issue(w http.ResponseWriter, login string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    c.encode(login, time.Now()),
		Path:     "/",
		MaxAge:   int(c.ttl.Seconds()),
		HttpOnly: true,
		Secure:   c.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// Clear expires the session cookie.
func (c *SessionCodec) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   c.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// Login reads and verifies the session cookie, returning the GitHub
// login it carries.
func (c *SessionCodec) Login(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(SessionCookie)
	if err != nil {
		return "", false
	}
	return c.decode(cookie.Value, time.Now())
}

// IssueState stores the OAuth state nonce in a short-lived cookie, to
// be compared against the state GitHub echoes back.
func (c *SessionCodec) IssueState(w http.ResponseWriter, state string) {
	http.SetCookie(w, &http.Cookie{
		Name:     StateCookie,
		Value:    state,
		Path:     "/",
		MaxAge:   int((10 * time.Minute).Seconds()),
		HttpOnly: true,
		Secure:   c.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// CheckState compares the echoed state against the cookie and clears
// the cookie either way — a state is good for exactly one attempt.
func (c *SessionCodec) CheckState(w http.ResponseWriter, r *http.Request, state string) bool {
	defer http.SetCookie(w, &http.Cookie{
		Name:     StateCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   c.secure,
		SameSite: http.SameSiteLaxMode,
	})
	cookie, err := r.Cookie(StateCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	return hmac.Equal([]byte(cookie.Value), []byte(state))
}
