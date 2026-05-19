package metrics

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Handler returns the HTTP handler for /metrics. When bearerToken is
// non-empty, requests must supply Authorization: Bearer <token> with
// a constant-time match; otherwise /metrics is open (suitable for
// stdio/local deployments where the listener isn't reachable from
// outside the host).
func Handler(bearerToken string) http.Handler {
	promHandler := promhttp.Handler()
	if bearerToken == "" {
		return promHandler
	}
	want := []byte(bearerToken)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := bearerFromHeader(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			slog.Warn("metrics auth rejected", "remote", r.RemoteAddr)
			w.Header().Set("WWW-Authenticate", `Bearer realm="typst-d2-mcp metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		promHandler.ServeHTTP(w, r)
	})
}

func bearerFromHeader(h string) string {
	const p = "Bearer "
	if len(h) > len(p) && strings.EqualFold(h[:len(p)], p) {
		return strings.TrimSpace(h[len(p):])
	}
	return ""
}
