package auth

import (
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
)

// A refused account used to learn nothing at all.
//
// The server already refuses correctly: ServeCallback sends
// access_denied with an accurate error_description, which is the only
// channel the OAuth redirect gives us. The MCP client then discards the
// description and renders a generic failure. We cannot fix the client.
//
// So the person — who is sitting in a browser, mid-OAuth, looking at
// our domain — is told nothing, and behaves accordingly: one real user
// tried four times over five and a half hours and then reported it to
// the operator, whose first hypothesis was a GitHub outage. Nobody
// involved could see the one fact the server had logged plainly.
//
// The fix is to say it where the person is, before continuing the
// protocol. access_denied is terminal — unlike server_error, no amount
// of retrying changes it — so an interstitial costs a redirect that was
// never going to help anyone, and the client still observes the same
// outcome when the person continues.

// refusalPage is deliberately self-contained: no external stylesheet,
// no script, no font. It renders on a phone, in a webview, and in
// whatever embedded browser an MCP client opened, and it cannot fail
// because a CDN did.
var refusalPage = template.Must(template.New("refusal").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Not authorised</title>
<style>
  :root { color-scheme: light dark; }
  body {
    margin: 0; padding: 2rem 1.25rem;
    font: 16px/1.55 system-ui, -apple-system, "Segoe UI", sans-serif;
    display: flex; justify-content: center;
  }
  main { max-width: 34rem; }
  h1 { font-size: 1.4rem; margin: 0 0 1rem; }
  p { margin: 0 0 1rem; }
  .account { font-weight: 600; }
  .where { font-weight: 600; word-break: break-all; }
  .continue { display: inline-block; margin-top: .5rem; }
  footer { margin-top: 2rem; font-size: .875rem; opacity: .75; }
</style>
</head>
<body>
<main>
  <h1>This account is not invited here</h1>
  <p>
    You signed in to GitHub as <span class="account">{{ .Login }}</span>,
    but that account is not authorised to use
    <span class="where">{{ .Deployment }}</span>.
  </p>
  <p>
    Nothing is wrong with your GitHub account, and signing in again will
    not change the outcome — access is granted per account, per
    deployment. Ask whoever runs this server to invite
    <span class="account">{{ .Login }}</span>.
  </p>
  <p>
    <a class="continue" href="{{ .ContinueURL }}">Return to the application</a>
  </p>
  <footer>
    The application that sent you here will report this as a failed
    authorisation. That is expected.
  </footer>
</main>
</body>
</html>
`))

type refusalData struct {
	Login       string
	Deployment  string
	ContinueURL template.URL
}

// serveRefusal renders the interstitial for a terminal access_denied.
//
// The caller decides when this is appropriate; serveRefusal only
// renders. A failure to build the continue URL falls back to the plain
// redirect, because being told why is worth less than the protocol
// completing.
func (g *GitHub) serveRefusal(w http.ResponseWriter, r *http.Request,
	login, redirectURI, state, description string) {
	target, err := oauthErrorURL(redirectURI, state, "access_denied", description)
	if err != nil {
		slog.Error("refusal page: unusable redirect_uri, falling back to redirect", "err", err)
		redirectOAuthError(w, r, redirectURI, state, "access_denied", description)
		return
	}

	data := refusalData{
		Login:      login,
		Deployment: g.deploymentName(r),
		// Not filtered by html/template's URL sanitiser, which allows
		// only a handful of schemes and would rewrite anything else to
		// "#ZgotmplZ" — MCP clients routinely register custom-scheme
		// callbacks. Safe because this URI is not user input: it was
		// matched against the client's registered redirect_uris at
		// authorize time (see ServeAuthorize) and stored in the
		// session, which is where it has just come from.
		ContinueURL: template.URL(target),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Nothing here should be replayed from a cache: it names an account
	// and a decision that can change the moment an invite is issued.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if err := refusalPage.Execute(w, data); err != nil {
		// The header is already written, so this can only be logged.
		slog.Error("refusal page: render failed", "err", err)
	}
}

// deploymentName is what the page calls this server.
//
// The distinction matters more than it looks: the user in the report
// was refused by the test deployment while production would have
// admitted them, so "not authorised" and "not authorised for
// test.example.com" are a dead end and a next step respectively.
//
// PublicURL is the configured external identity and is preferred. The
// request's Host is the fallback, and is attacker-controlled in
// principle — it is rendered as escaped text, never used to build a
// link, so the worst it can do is name the wrong host on a page the
// attacker would have had to send you to anyway.
func (g *GitHub) deploymentName(r *http.Request) string {
	if u, err := url.Parse(g.Cfg.PublicURL); err == nil && u.Host != "" {
		return u.Host
	}
	if r != nil && r.Host != "" {
		return r.Host
	}
	return "this server"
}
