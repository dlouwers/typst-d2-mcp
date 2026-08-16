package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testCodec(t *testing.T) *SessionCodec {
	t.Helper()
	key, err := RandomKey()
	if err != nil {
		t.Fatalf("RandomKey: %v", err)
	}
	return NewSessionCodec(key, time.Hour, true)
}

func TestSession_RoundTrip(t *testing.T) {
	c := testCodec(t)
	now := time.Now()

	login, ok := c.decode(c.encode("dlouwers", now), now)
	if !ok {
		t.Fatal("freshly encoded session did not decode")
	}
	if login != "dlouwers" {
		t.Errorf("login = %q, want dlouwers", login)
	}
}

func TestSession_RejectsTampering(t *testing.T) {
	c := testCodec(t)
	now := time.Now()
	value := c.encode("dlouwers", now)

	payload, mac, _ := strings.Cut(value, ".")
	forged := map[string]string{
		"different payload, original mac": "ZGxvdXdlcnN8OTk5OTk5OTk5OQ." + mac,
		"payload with no mac":             payload,
		"empty":                           "",
		"garbage":                         "not.a.session",
		"mac from another key":            payload + "." + testCodec(t).sign(payload),
	}
	for name, value := range forged {
		if _, ok := c.decode(value, now); ok {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSession_Expires(t *testing.T) {
	c := testCodec(t)
	now := time.Now()
	value := c.encode("dlouwers", now)

	if _, ok := c.decode(value, now.Add(time.Hour-time.Minute)); !ok {
		t.Error("session rejected before its expiry")
	}
	if _, ok := c.decode(value, now.Add(time.Hour+time.Minute)); ok {
		t.Error("expired session accepted")
	}
}

// SameSite=Lax is the CSRF defence for the admin actions, so the cookie
// attributes are load-bearing rather than cosmetic.
func TestSession_CookieAttributes(t *testing.T) {
	c := testCodec(t)
	rec := httptest.NewRecorder()
	c.Issue(rec, "dlouwers")

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	got := cookies[0]
	if got.Name != SessionCookie {
		t.Errorf("name = %q, want %q", got.Name, SessionCookie)
	}
	if !got.HttpOnly {
		t.Error("cookie is not HttpOnly; script could read the session")
	}
	if !got.Secure {
		t.Error("cookie is not Secure")
	}
	if got.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax (the CSRF defence)", got.SameSite)
	}
}

// Insecure mode exists so plain-http local development can sign in at
// all; browsers drop Secure cookies over http.
func TestSession_InsecureModeDropsSecureFlag(t *testing.T) {
	key, _ := RandomKey()
	c := NewSessionCodec(key, time.Hour, false)
	rec := httptest.NewRecorder()
	c.Issue(rec, "dlouwers")

	if rec.Result().Cookies()[0].Secure {
		t.Error("Secure set despite insecure mode")
	}
}

func TestSession_ClearExpiresCookie(t *testing.T) {
	c := testCodec(t)
	rec := httptest.NewRecorder()
	c.Clear(rec)

	got := rec.Result().Cookies()[0]
	if got.MaxAge >= 0 {
		t.Errorf("MaxAge = %d, want negative to expire the cookie", got.MaxAge)
	}
	if got.Value != "" {
		t.Errorf("value = %q, want empty", got.Value)
	}
}

func TestState_PrefixDistinguishesAdminLogins(t *testing.T) {
	state, err := RandomState()
	if err != nil {
		t.Fatalf("RandomState: %v", err)
	}
	if !IsAdminState(state) {
		t.Errorf("%q not recognised as an admin state", state)
	}
	// The MCP authorization-server flow's state is an authorize-session
	// id, which must not be mistaken for an admin login.
	if IsAdminState("ttd2sess_abc123") {
		t.Error("an MCP authorize-session id was taken for an admin login")
	}
	if IsAdminState("") {
		t.Error("empty state treated as an admin login")
	}
}

func TestState_CheckMatchesCookieOnce(t *testing.T) {
	c := testCodec(t)
	state, _ := RandomState()

	issue := httptest.NewRecorder()
	c.IssueState(issue, state)
	stateCookie := issue.Result().Cookies()[0]

	r := httptest.NewRequest(http.MethodGet, "/auth/github/callback", nil)
	r.AddCookie(stateCookie)
	if !c.CheckState(httptest.NewRecorder(), r, state) {
		t.Error("matching state rejected")
	}

	// Wrong state, right cookie.
	r2 := httptest.NewRequest(http.MethodGet, "/auth/github/callback", nil)
	r2.AddCookie(stateCookie)
	if c.CheckState(httptest.NewRecorder(), r2, "ttd2adm_someoneelse") {
		t.Error("mismatched state accepted")
	}

	// No cookie at all — the forged-callback case.
	r3 := httptest.NewRequest(http.MethodGet, "/auth/github/callback", nil)
	if c.CheckState(httptest.NewRecorder(), r3, state) {
		t.Error("state accepted with no cookie present")
	}
}
