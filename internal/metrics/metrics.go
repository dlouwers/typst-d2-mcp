// Package metrics exposes the Prometheus collectors used to observe
// typst-d2-mcp in production. Counters and histograms are
// package-level and registered eagerly via promauto so the /metrics
// endpoint serves a stable schema even before the first request.
//
// Label cardinality is deliberately kept low: result-class labels
// (ok, quota_exceeded, …) instead of per-user labels, since
// per-tenant counters would blow up the Prometheus index in any
// non-trivial deployment.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Result label values for the compile and put_file counters. Using
// constants keeps the set closed so a typo in a handler turns into
// a build failure rather than a quietly mis-attributed counter.
const (
	ResultOK            = "ok"
	ResultFail          = "fail"
	ResultQuotaExceeded = "quota_exceeded"
	ResultTimeout       = "timeout"
	ResultTooLarge      = "too_large"
	ResultDecodeError   = "decode_error"
)

var (
	// CompileTotal counts every compile_typst_with_d2 invocation,
	// labelled by the terminal result. Quota-exceeded compiles are
	// counted here too so an alert on "fail rate" can be written
	// against this single series.
	CompileTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "typst_d2_mcp_compile_total",
		Help: "Total compile attempts by terminal result.",
	}, []string{"result"})

	// CompileDuration measures the wall-clock duration of a successful
	// compile (preprocess + d2 render + typst compile). Failed
	// compiles are not recorded here — they get their own counter and
	// would skew the success-path latency view.
	CompileDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "typst_d2_mcp_compile_duration_seconds",
		Help:    "Wall-clock duration of successful compile_typst_with_d2 calls.",
		Buckets: prometheus.ExponentialBucketsRange(0.05, 30, 12),
	})

	// PutFileTotal counts every put_file invocation, labelled by
	// result. ok = file written; too_large = exceeded
	// TYPST_D2_MCP_MAX_INPUT_BYTES; decode_error = base64 input
	// malformed; fail = any other write/resolve failure.
	PutFileTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "typst_d2_mcp_put_file_total",
		Help: "Total put_file invocations by terminal result.",
	}, []string{"result"})

	// CompileInputBytes is a histogram of the resolved input .typ
	// size at the moment compile started (i.e. after the size cap
	// check). Helps capacity planning for the per-tenant workspace
	// volume.
	CompileInputBytes = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "typst_d2_mcp_compile_input_bytes",
		Help:    "Distribution of compile_typst_with_d2 input file sizes (bytes).",
		Buckets: prometheus.ExponentialBucketsRange(256, 1<<20, 10),
	})

	// AuthRejectedTotal counts HTTP requests rejected by the Bearer
	// middleware. A spike here is either a misconfigured client or
	// a probe; pair with a rate alert.
	AuthRejectedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "typst_d2_mcp_auth_rejected_total",
		Help: "HTTP requests rejected by the Bearer-auth middleware.",
	})
)
