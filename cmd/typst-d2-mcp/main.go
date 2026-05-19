package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dlouwers/typst-d2-mcp/internal/auth"
	"github.com/dlouwers/typst-d2-mcp/internal/authdb"
	"github.com/dlouwers/typst-d2-mcp/internal/identity"
	"github.com/dlouwers/typst-d2-mcp/internal/metrics"
	"github.com/dlouwers/typst-d2-mcp/internal/preprocessor"
	"github.com/dlouwers/typst-d2-mcp/internal/workspace"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/yosida95/uritemplate/v3"
)

// serverVersion is overridden at release time via
// `-ldflags="-X main.serverVersion=..."`. Must be var (not const) for
// the linker to rewrite it.
var serverVersion = "dev"

const (
	serverName = "typst-d2-mcp"

	envTransport      = "TYPST_D2_MCP_TRANSPORT"
	envAddr           = "TYPST_D2_MCP_ADDR"
	envPath           = "TYPST_D2_MCP_PATH"
	envWorkspace      = "TYPST_D2_MCP_WORKSPACE"
	envCompileTimeout = "TYPST_D2_MCP_COMPILE_TIMEOUT"
	envMaxInputBytes  = "TYPST_D2_MCP_MAX_INPUT_BYTES"

	envAuth         = "TYPST_D2_MCP_AUTH"
	envDB           = "TYPST_D2_MCP_DB"
	envPublicURL    = "TYPST_D2_MCP_PUBLIC_URL"
	envGitHubID     = "TYPST_D2_MCP_GITHUB_CLIENT_ID"
	envGitHubSecret = "TYPST_D2_MCP_GITHUB_CLIENT_SECRET"
	envQuotaPerDay  = "TYPST_D2_MCP_QUOTA_PER_DAY"
	envLogLevel     = "TYPST_D2_MCP_LOG_LEVEL"
	envLogFormat    = "TYPST_D2_MCP_LOG_FORMAT"

	envMetricsAddr   = "TYPST_D2_MCP_METRICS_ADDR"
	envMetricsBearer = "TYPST_D2_MCP_METRICS_BEARER"
	envPDFLinkTTL    = "TYPST_D2_MCP_PDF_LINK_TTL"

	defaultMetricsAddr = ":9090"
	defaultPDFLinkTTL  = time.Hour

	defaultAddr           = ":8080"
	defaultPath           = "/mcp"
	defaultCompileTimeout = 30 * time.Second
	defaultMaxInputBytes  = int64(1 << 20) // 1 MiB
	defaultQuotaPerDay    = 1

	// pdfURIPrefix is the scheme + host used by the compile tool when it
	// returns a ResourceLink for the produced PDF. Clients can fetch the
	// bytes via the standard MCP resources/read call against this URI.
	pdfURIPrefix = "typst-d2://pdf/"
)

// compileTimeout reads the configured per-compile budget. A value of zero
// disables the extra timeout and leaves the calling request context in
// charge.
func compileTimeout() time.Duration {
	return durationEnv(envCompileTimeout, defaultCompileTimeout)
}

// maxInputBytes is the upper bound on accepted file content (both the
// .typ file fed to compile_typst_with_d2 and the decoded content written
// by put_file). It bounds memory + parser work before any d2/typst exec.
func maxInputBytes() int64 {
	return int64Env(envMaxInputBytes, defaultMaxInputBytes)
}

// quotaPerDay is the per-user UTC-day ceiling on successful compile
// attempts; 0 disables the check. Only enforced for non-anonymous
// identities (i.e. authenticated users), so self-hosted single-tenant
// deployments stay unmetered.
func quotaPerDay() int {
	return int(int64Env(envQuotaPerDay, int64(defaultQuotaPerDay)))
}

// pdfLinkTTL is the lifetime of a capability URL minted by the compile
// handler. The opaque token IS the credential; the URL itself is the
// primary defence, so a tight TTL is the secondary defence in case the
// URL leaks via chat history, server logs, etc.
func pdfLinkTTL() time.Duration {
	return durationEnv(envPDFLinkTTL, defaultPDFLinkTTL)
}

func durationEnv(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		slog.Warn("invalid duration env, using default",
			"env", key, "value", v, "default", def.String())
		return def
	}
	return d
}

func int64Env(key string, def int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		slog.Warn("invalid integer env, using default",
			"env", key, "value", v, "default", def)
		return def
	}
	return n
}

// serverInstructions is sent once at the MCP initialize handshake. Moving
// this guidance out of the per-tool description keeps it available to the
// model without re-spending tokens on every tool call. Keep it focused on
// strategy and anti-patterns the model needs across multiple compiles;
// the per-call rule lives in the tool description.
const serverInstructions = `You can author Typst documents containing #d2[...] blocks and compile them
to PDF with the compile_typst_with_d2 tool. The notes below apply across
every diagram in a session — the tool description itself stays brief.

DIAGRAM LAYOUT — A4 PORTRAIT (Typst default):
  Usable area is roughly 17cm wide × 25cm tall, so vertical layouts breathe
  while horizontal layouts get cramped. Prefer "direction: down" inside the
  D2 block whenever a diagram has more than a handful of nodes.

STAR-TOPOLOGY ANTI-PATTERN:
  A central node connecting to 5+ siblings forces the ELK layout engine to
  spread the children horizontally even when "direction: down" is set. The
  fix is a vertical chain:

      // BAD (renders horizontally on A4 portrait)
      center -> a
      center -> b
      center -> c
      center -> d
      center -> e

      // GOOD
      center -> a -> b -> c -> d -> e

  Org charts with two or three direct reports can stay as a star; four or
  more children means convert to a chain or split into multiple diagrams.

A4 LANDSCAPE (#set page(flipped: true)):
  Usable area becomes ~25cm × 17cm. Prefer "direction: right" for wide
  hierarchies; vertical chains still work but waste horizontal space.

PRINT-FRIENDLY DEFAULTS:
  - layout: "elk"  (best automatic layout)
  - theme: "0"     (white background, good contrast on paper)
  - Avoid dark themes (100–200 range) for print.

SYNTAX EXAMPLES:
  Basic:
    #d2(layout: "elk", theme: "0")[
      direction: down
      client -> server -> database
    ]

  Architecture with shapes:
    #d2(layout: "elk", theme: "0")[
      direction: down
      frontend: Frontend {shape: rectangle}
      backend:  Backend  {shape: rectangle}
      database: Database {shape: cylinder}
      frontend -> backend: API calls
      backend  -> database: Queries
    ]

VERIFYING THE RESULT:
  After a successful compile, open the produced PDF if you can. Check that
  text labels are readable and the diagram fits within page margins. If a
  diagram looks cramped, add "direction: down", split it into multiple
  diagrams, or remove non-essential nodes. If you cannot view the PDF
  yourself, advise the user to inspect it.`

func main() {
	initLogger()

	factory, err := selectFactory()
	if err != nil {
		slog.Error("workspace setup failed", "err", err)
		os.Exit(1)
	}

	backend, ghHandlers, store, closer, err := selectAuth()
	if err != nil {
		slog.Error("auth setup failed", "err", err)
		os.Exit(1)
	}
	if closer != nil {
		defer closer()
	}

	s := server.NewMCPServer(
		serverName,
		serverVersion,
		server.WithToolCapabilities(false),
		server.WithResourceCapabilities(false, false),
		server.WithInstructions(serverInstructions),
	)

	registerTools(s, factory, store)
	registerResources(s, factory)

	slog.Info("starting",
		"version", serverVersion,
		"auth", backend.Name(),
		"quota_per_day", quotaPerDay(),
		"compile_timeout", compileTimeout().String(),
		"max_input_bytes", maxInputBytes(),
	)

	if err := serve(s, backend, ghHandlers, factory, store); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

// initLogger wires log/slog as the default logger before any other
// code runs. Output goes to stderr (so stdio-mode stdout stays
// reserved for the MCP protocol). HTTP mode defaults to JSON for
// container log aggregators; stdio defaults to human-readable text
// for local development.
func initLogger() {
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv(envLogLevel)) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	format := strings.ToLower(os.Getenv(envLogFormat))
	if format == "" {
		if isHTTPTransport() {
			format = "json"
		} else {
			format = "text"
		}
	}
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if format == "json" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(handler))
}

// gitHubHandlers bundles the HTTP endpoints exposed by the GitHub
// auth backend; nil when AUTH is not "github". The set covers both
// the GitHub round-trip and the MCP-spec OAuth Authorization Server
// (RFC 6749 + 7591 + 7636 + 8414 + 9728) handlers.
type gitHubHandlers struct {
	githubCallback          http.HandlerFunc
	wellKnownResource       http.HandlerFunc
	wellKnownAuthServer     http.HandlerFunc
	register                http.HandlerFunc
	authorize               http.HandlerFunc
	token                   http.HandlerFunc
}

// selectFactory picks the workspace.Factory used to mint per-request
// resolvers. Behaviour by mode:
//
//   - stdio without TYPST_D2_MCP_WORKSPACE: LocalFactory (back-compat
//     with the installed CLI experience).
//
//   - Any mode with TYPST_D2_MCP_WORKSPACE set: TenantFactory rooted
//     there. Per-user subdirectories are created on demand by the
//     factory.
//
//   - HTTP without TYPST_D2_MCP_WORKSPACE: TenantFactory rooted at a
//     per-process tmp dir. Suitable for laptop experiments; real
//     deployments should set the env.
func selectFactory() (workspace.Factory, error) {
	root := os.Getenv(envWorkspace)
	if root == "" && isHTTPTransport() {
		root = filepath.Join(os.TempDir(), "typst-d2-mcp-workspace")
		slog.Warn("no workspace configured; using tmp dir",
			"env", envWorkspace, "path", root)
	}
	if root == "" {
		return workspace.LocalFactory{}, nil
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("workspace abs: %w", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("create workspace root: %w", err)
	}
	return workspace.TenantFactory{Root: abs}, nil
}

// selectAuth picks the active auth.Backend and, for the GitHub
// backend, returns its HTTP handlers, the shared SQLite store (used by
// both auth lookups and the compile-quota counter), and a cleanup
// closure that closes the store. For AUTH=none the store is nil.
func selectAuth() (auth.Backend, *gitHubHandlers, *authdb.Store, func(), error) {
	mode := strings.ToLower(os.Getenv(envAuth))
	switch mode {
	case "", "none":
		return auth.None{}, nil, nil, nil, nil
	case "github":
		dbPath := os.Getenv(envDB)
		if dbPath == "" {
			return nil, nil, nil, nil, fmt.Errorf("%s=github requires %s to be set", envAuth, envDB)
		}
		clientID := os.Getenv(envGitHubID)
		clientSecret := os.Getenv(envGitHubSecret)
		publicURL := os.Getenv(envPublicURL)
		if clientID == "" || clientSecret == "" || publicURL == "" {
			return nil, nil, nil, nil, fmt.Errorf("%s=github requires %s, %s, and %s",
				envAuth, envGitHubID, envGitHubSecret, envPublicURL)
		}
		store, err := authdb.Open(dbPath)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("open auth db: %w", err)
		}
		gh := &auth.GitHub{
			Cfg: auth.GitHubConfig{
				ClientID:     clientID,
				ClientSecret: clientSecret,
				PublicURL:    publicURL,
			},
			Store: store,
		}
		closer := func() { _ = store.Close() }
		return gh, &gitHubHandlers{
			githubCallback:      gh.ServeCallback,
			wellKnownResource:   gh.ServeWellKnownProtectedResource,
			wellKnownAuthServer: gh.ServeWellKnownAuthorizationServer,
			register:            gh.ServeRegister,
			authorize:           gh.ServeAuthorize,
			token:               gh.ServeToken,
		}, store, closer, nil
	default:
		return nil, nil, nil, nil, fmt.Errorf("unknown %s=%q (expected none or github)", envAuth, mode)
	}
}

func isHTTPTransport() bool {
	t := strings.ToLower(os.Getenv(envTransport))
	return t == "http" || t == "streamable-http"
}

func serve(s *server.MCPServer, backend auth.Backend, gh *gitHubHandlers, factory workspace.Factory, store *authdb.Store) error {
	switch transport := strings.ToLower(os.Getenv(envTransport)); transport {
	case "", "stdio":
		// Stdio is always anonymous; the backend is irrelevant.
		return server.ServeStdio(s)
	case "http", "streamable-http":
		addr := os.Getenv(envAddr)
		if addr == "" {
			addr = defaultAddr
		}
		path := os.Getenv(envPath)
		if path == "" {
			path = defaultPath
		}
		// Stateless: each request stands on its own; identity is
		// derived from the request's Authorization header by the
		// middleware below.
		httpSrv := server.NewStreamableHTTPServer(s,
			server.WithEndpointPath(path),
			server.WithStateLess(true),
		)

		// Resource-metadata pointer is only emitted when an OAuth AS
		// is actually wired up (i.e. AUTH=github). For AUTH=none the
		// middleware skips the auth check entirely so the 401 path is
		// unreachable anyway.
		var resourceMetadataURL string
		if gh != nil {
			resourceMetadataURL = strings.TrimRight(os.Getenv(envPublicURL), "/") + "/.well-known/oauth-protected-resource"
		}

		mux := http.NewServeMux()
		mux.Handle(path, authMiddleware(backend, httpSrv, resourceMetadataURL))
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})
		// /d/{token} — capability-URL download endpoint. NOT behind
		// the Bearer middleware: the token IS the credential, by
		// design. Handler short-circuits to 404 when store == nil
		// (AUTH=none stdio/local deployments).
		mux.Handle("/d/", handlePDFDownload(factory, store))
		if gh != nil {
			// MCP-spec OAuth Authorization Server endpoints. The
			// callback URL stays at /auth/github/callback to match the
			// GitHub OAuth app registration; everything else is the
			// public AS surface that MCP clients (Claude.ai etc.)
			// discover and drive.
			mux.HandleFunc("/.well-known/oauth-protected-resource", gh.wellKnownResource)
			mux.HandleFunc("/.well-known/oauth-authorization-server", gh.wellKnownAuthServer)
			mux.HandleFunc("/register", gh.register)
			mux.HandleFunc("/authorize", gh.authorize)
			mux.HandleFunc("/token", gh.token)
			mux.HandleFunc("/auth/github/callback", gh.githubCallback)
		}

		// /metrics binds to a separate listener so it never shares
		// the public Ingress with the app port. NetworkPolicy in
		// the k8s manifests restricts the metrics port further to
		// the monitoring namespace only.
		startMetricsListener()

		slog.Info("listening", "addr", addr, "path", path)
		return http.ListenAndServe(addr, mux) //nolint:gosec // intentional plain HTTP; TLS is terminated upstream.
	default:
		return fmt.Errorf("unknown %s=%q (expected stdio or http)", envTransport, transport)
	}
}

// startMetricsListener serves Prometheus metrics on a separate port
// in a background goroutine. The Bearer gate is optional: in
// single-tenant local deployments the listener isn't reachable from
// outside the host, so an unset TYPST_D2_MCP_METRICS_BEARER leaves
// /metrics open. In Kubernetes the NetworkPolicy restricts the port
// to the monitoring namespace as defence in depth.
func startMetricsListener() {
	addr := os.Getenv(envMetricsAddr)
	if addr == "" {
		addr = defaultMetricsAddr
	}
	token := os.Getenv(envMetricsBearer)
	mux := http.NewServeMux()
	mux.Handle("/metrics", metrics.Handler(token))
	slog.Info("metrics listening", "addr", addr, "bearer_required", token != "")
	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil { //nolint:gosec // local cluster network only.
			slog.Error("metrics server stopped", "err", err)
		}
	}()
}

// authMiddleware identifies the principal behind r via backend and
// attaches the resulting Identity to the request context. Backends
// that don't admit anonymous traffic (i.e. GitHub) cause a 401 with
// a WWW-Authenticate that carries the resource_metadata URL — that
// pointer is what MCP clients (Claude.ai etc.) follow to discover
// the OAuth Authorization Server and start the dance themselves.
func authMiddleware(backend auth.Backend, h http.Handler, resourceMetadataURL string) http.Handler {
	wwwAuth := `Bearer realm="typst-d2-mcp"`
	if resourceMetadataURL != "" {
		wwwAuth += `, resource_metadata="` + resourceMetadataURL + `"`
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := backend.IdentifyFromRequest(r)
		if err != nil {
			metrics.AuthRejectedTotal.Inc()
			slog.Warn("auth rejected",
				"backend", backend.Name(),
				"remote", r.RemoteAddr,
				"err", err.Error(),
			)
			w.Header().Set("WWW-Authenticate", wwwAuth)
			http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
			return
		}
		ctx := identity.WithIdentity(r.Context(), id)
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// resolverFor returns the active resolver for the request's identity,
// or, when no identity has been threaded through (e.g. stdio), the
// resolver for the anonymous tenant.
func resolverFor(ctx context.Context, factory workspace.Factory) (workspace.Resolver, error) {
	id, _ := identity.FromContext(ctx)
	r, err := factory.Resolver(id)
	if err != nil {
		return nil, err
	}
	return r, nil
}


func registerTools(s *server.MCPServer, factory workspace.Factory, store *authdb.Store) {
	// The bulk of the layout strategy lives in server instructions above so
	// it isn't re-sent on every tool call. The description below carries
	// only the rules the model needs at the moment it decides to call.
	compileTypstTool := mcp.NewTool("compile_typst_with_d2",
		mcp.WithDescription(`Compile a Typst document containing #d2[...] diagram blocks to PDF.

The input file is preprocessed in place: each #d2(opts)[code] block is
rendered to SVG via the d2 CLI, base64-embedded, and the resulting Typst
source is compiled with the typst CLI. The output PDF is written next to
the input .typ file inside the active workspace, and a resource_link
content block in the result points at the PDF (fetch the bytes with
resources/read on its typst-d2://pdf/... URI).

Quick rules (full strategy in the server's instructions):
  - Default layout "elk", theme "0" for print-friendly diagrams.
  - On A4 portrait, add 'direction: down' inside the D2 block.
  - A central node with 4+ children renders horizontally even with
    'direction: down' — rewrite as a vertical chain.

After compiling, inspect the PDF if you can; if a diagram looks cramped,
split it, simplify it, or switch to 'direction: down'.`),
		mcp.WithString("file_path",
			mcp.Required(),
			mcp.Description("Path to the Typst source file (.typ) containing #d2[...] blocks. Absolute in local stdio mode; workspace-relative in scoped/hosted mode."),
		),
	)
	s.AddTool(compileTypstTool, handleCompileTypst(factory, store))

	putFileTool := mcp.NewTool("put_file",
		mcp.WithDescription(`Write a file into the server's active workspace.

Use this only when your runtime cannot directly write to the target
filesystem — for example when talking to a hosted MCP server over HTTP.
When running against a local stdio server, prefer your host's filesystem
tools (Write/Edit) so you don't ship the file content through this
channel.

The path is resolved through the server's active workspace. In local
mode any path is accepted; in scoped/hosted mode the path must be
relative and stay within the workspace (traversal is rejected).`),
		mcp.WithString("path",
			mcp.Required(),
			mcp.Description("Destination path. Workspace-relative in scoped/hosted mode; any path in local mode."),
		),
		mcp.WithString("content",
			mcp.Required(),
			mcp.Description("File content, decoded according to encoding."),
		),
		mcp.WithString("encoding",
			mcp.Description(`"utf8" (default) for text or "base64" for binary data.`),
		),
	)
	s.AddTool(putFileTool, handlePutFile(factory))
}

func registerResources(s *server.MCPServer, factory workspace.Factory) {
	tmpl := mcp.ResourceTemplate{
		URITemplate: &mcp.URITemplate{Template: uritemplate.MustNew(pdfURIPrefix + "{+path}")},
		Name:        "pdf",
		Description: "Compiled Typst PDF produced by compile_typst_with_d2.",
		MIMEType:    "application/pdf",
	}
	s.AddResourceTemplate(tmpl, handleReadPDF(factory))
}

func handleCompileTypst(factory workspace.Factory, store *authdb.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		filePath, err := request.RequireString("file_path")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Quota gate runs first so a quota-exceeded user pays no
		// compute cost. Only authenticated identities are metered;
		// stdio + AUTH=none stay unlimited.
		start := time.Now()
		id, _ := identity.FromContext(ctx)
		log := slog.With("user", id.UserID, "file_path", filePath)
		if store != nil && !id.IsAnonymous() {
			limit := quotaPerDay()
			today := time.Now().UTC().Format("2006-01-02")
			if err := store.IncrementCompile(ctx, id.UserID, today, limit); err != nil {
				if errors.Is(err, authdb.ErrQuotaExceeded) {
					metrics.CompileTotal.WithLabelValues(metrics.ResultQuotaExceeded).Inc()
					log.Warn("quota exceeded", "limit", limit)
					return mcp.NewToolResultError(fmt.Sprintf(
						"quota exceeded: %d compile(s) per UTC day per user (resets at 00:00 UTC; set %s to raise)",
						limit, envQuotaPerDay,
					)), nil
				}
				metrics.CompileTotal.WithLabelValues(metrics.ResultFail).Inc()
				log.Error("quota check failed", "err", err)
				return mcp.NewToolResultErrorFromErr("quota check", err), nil
			}
		}

		resolver, err := resolverFor(ctx, factory)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("workspace setup", err), nil
		}

		resolvedIn, err := workspace.MustExist(resolver, filePath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		if info, err := os.Stat(resolvedIn); err == nil {
			if limit := maxInputBytes(); info.Size() > limit {
				metrics.CompileTotal.WithLabelValues(metrics.ResultTooLarge).Inc()
				return mcp.NewToolResultError(fmt.Sprintf(
					"input file too large: %d bytes (limit %d, set %s to raise)",
					info.Size(), limit, envMaxInputBytes,
				)), nil
			} else {
				metrics.CompileInputBytes.Observe(float64(info.Size()))
			}
		}

		if tmo := compileTimeout(); tmo > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, tmo)
			defer cancel()
		}

		processed, err := preprocessor.Preprocess(ctx, resolver, filePath)
		if err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				metrics.CompileTotal.WithLabelValues(metrics.ResultTimeout).Inc()
				return mcp.NewToolResultError(fmt.Sprintf(
					"compile exceeded %s (set %s to raise)",
					compileTimeout(), envCompileTimeout,
				)), nil
			}
			metrics.CompileTotal.WithLabelValues(metrics.ResultFail).Inc()
			return mcp.NewToolResultErrorFromErr("Preprocessing failed", err), nil
		}

		tmpFile, err := os.CreateTemp("", "typst-d2-*.typ")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("Failed to create temp file", err), nil
		}
		defer os.Remove(tmpFile.Name())

		if _, err := tmpFile.WriteString(processed); err != nil {
			return mcp.NewToolResultErrorFromErr("Failed to write temp file", err), nil
		}
		tmpFile.Close()

		// Output PDF sits next to the input .typ inside the workspace.
		resolvedOut := strings.TrimSuffix(resolvedIn, ".typ") + ".pdf"

		// Capture stdout + stderr separately. Typst exits 0 with
		// warnings on stderr for things like "cannot place at top of
		// page" or oversized images that overflow the column —
		// silent on success would mean a half-rendered PDF gets
		// reported as a clean compile. We surface warnings to the
		// caller so the LLM/operator can act on them.
		var stdoutBuf, stderrBuf bytes.Buffer
		cmd := exec.CommandContext(ctx, "typst", "compile", tmpFile.Name(), resolvedOut)
		cmd.Stdout = &stdoutBuf
		cmd.Stderr = &stderrBuf
		err = cmd.Run()
		stderrStr := strings.TrimSpace(stderrBuf.String())
		if err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				metrics.CompileTotal.WithLabelValues(metrics.ResultTimeout).Inc()
				return mcp.NewToolResultError(fmt.Sprintf(
					"compile exceeded %s (set %s to raise)",
					compileTimeout(), envCompileTimeout,
				)), nil
			}
			metrics.CompileTotal.WithLabelValues(metrics.ResultFail).Inc()
			combined := strings.TrimSpace(stdoutBuf.String() + "\n" + stderrStr)
			errMsg := fmt.Sprintf("Typst compilation failed: %s\nOutput: %s", err.Error(), combined)
			return mcp.NewToolResultError(errMsg), nil
		}

		// Tool-visible path is the path the caller used, with .typ→.pdf;
		// matches what the resource link encodes.
		toolVisibleOut := strings.TrimSuffix(filePath, ".typ") + ".pdf"

		successMsg := fmt.Sprintf("Successfully compiled to %s\n\n", toolVisibleOut)
		successMsg += "NEXT STEPS:\n"
		successMsg += "1. Open the PDF to verify diagram layout and readability\n"
		successMsg += "2. Check that diagrams fit within page margins (not cramped)\n"
		successMsg += "3. Verify text labels are readable (not too small)\n"
		successMsg += "4. If diagrams are cramped or text is tiny:\n"
		successMsg += "   - Add 'direction: down' at top of D2 block (for A4 portrait)\n"
		successMsg += "   - Split large diagrams into multiple focused diagrams\n"
		successMsg += "   - Reduce number of nodes or simplify structure\n"
		successMsg += "\nIf you cannot view the PDF yourself, inform the user to check the layout."

		// Typst exits 0 with warnings on stderr. Surface them or
		// they get silently dropped — and a "successful" compile
		// with overflow warnings can produce a truncated PDF.
		if stderrStr != "" {
			log.Warn("typst compile produced warnings", "stderr", stderrStr)
			successMsg += "\n\nTypst warnings (compile succeeded but check the PDF):\n" + stderrStr
		}

		duration := time.Since(start)
		metrics.CompileTotal.WithLabelValues(metrics.ResultOK).Inc()
		metrics.CompileDuration.Observe(duration.Seconds())

		// Mint a capability URL for the PDF and append it to the
		// result text. MCP clients that don't auto-follow
		// resource_link blocks (Claude.ai web as of 2026-05) DO
		// render plain https URLs as clickable links — the user
		// opens the PDF in their browser, no bytes traverse the LLM
		// context. Spec-conformant clients (Claude Code) still get
		// the resource_link content block alongside.
		//
		// Requires a SQLite store (AUTH=github) and TYPST_D2_MCP_PUBLIC_URL.
		// Anonymous / AUTH=none deployments skip the link silently;
		// those operators have local filesystem access anyway.
		if store != nil {
			ttl := pdfLinkTTL()
			token, mintErr := store.MintPDFLink(ctx, id.UserID, toolVisibleOut, ttl)
			if mintErr == nil {
				pub := strings.TrimRight(os.Getenv(envPublicURL), "/")
				if pub != "" {
					successMsg += fmt.Sprintf(
						"\n\nDownload: %s/d/%s\n(expires in %s; share or open in a browser)",
						pub, token, ttl,
					)
				}
			} else {
				log.Warn("mint pdf link failed", "err", mintErr)
			}
		}

		log.Info("compile ok",
			"output", toolVisibleOut,
			"duration_ms", duration.Milliseconds(),
		)
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{Type: "text", Text: successMsg},
				newPDFLink(toolVisibleOut),
			},
		}, nil
	}
}

// handlePDFDownload is the capability-URL endpoint mounted at /d/.
// It reads the token, resolves it to (user, file_path) via the SQLite
// store, recreates the workspace.Resolver for that user via the
// factory, and streams the PDF bytes. There is intentionally no
// Bearer/auth check: the random token IS the credential, by design
// (RFC-7239 §1 capability URL pattern).
func handlePDFDownload(factory workspace.Factory, store *authdb.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.URL.Path, "/d/")
		if token == "" || strings.Contains(token, "/") {
			metrics.PDFDownloadTotal.WithLabelValues(metrics.ResultFail).Inc()
			http.NotFound(w, r)
			return
		}
		if store == nil {
			metrics.PDFDownloadTotal.WithLabelValues(metrics.ResultFail).Inc()
			http.NotFound(w, r)
			return
		}
		link, err := store.LookupPDFLink(r.Context(), token)
		if err != nil {
			metrics.PDFDownloadTotal.WithLabelValues(metrics.ResultNotFound).Inc()
			http.NotFound(w, r)
			return
		}
		resolver, err := factory.Resolver(identity.Identity{UserID: link.UserID})
		if err != nil {
			metrics.PDFDownloadTotal.WithLabelValues(metrics.ResultFail).Inc()
			slog.Error("pdf download: resolver", "err", err, "user", link.UserID)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		resolved, err := workspace.MustExist(resolver, link.FilePath)
		if err != nil {
			metrics.PDFDownloadTotal.WithLabelValues(metrics.ResultNotFound).Inc()
			slog.Warn("pdf download: file missing", "user", link.UserID, "path", link.FilePath)
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(resolved)
		if err != nil {
			metrics.PDFDownloadTotal.WithLabelValues(metrics.ResultFail).Inc()
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition",
			fmt.Sprintf(`inline; filename=%q`, filepath.Base(link.FilePath)))
		w.Header().Set("Cache-Control", "private, max-age=300")
		metrics.PDFDownloadTotal.WithLabelValues(metrics.ResultOK).Inc()
		_, _ = io.Copy(w, f)
	})
}

func handlePutFile(factory workspace.Factory) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		path, err := request.RequireString("path")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		content, err := request.RequireString("content")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		resolver, err := resolverFor(ctx, factory)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("workspace setup", err), nil
		}
		encoding := strings.ToLower(request.GetString("encoding", "utf8"))

		var data []byte
		switch encoding {
		case "utf8", "utf-8", "":
			data = []byte(content)
		case "base64":
			d, decErr := base64.StdEncoding.DecodeString(content)
			if decErr != nil {
				metrics.PutFileTotal.WithLabelValues(metrics.ResultDecodeError).Inc()
				return mcp.NewToolResultErrorFromErr("base64 decode", decErr), nil
			}
			data = d
		default:
			metrics.PutFileTotal.WithLabelValues(metrics.ResultDecodeError).Inc()
			return mcp.NewToolResultError(fmt.Sprintf("unknown encoding %q (expected utf8 or base64)", encoding)), nil
		}

		if limit := maxInputBytes(); int64(len(data)) > limit {
			metrics.PutFileTotal.WithLabelValues(metrics.ResultTooLarge).Inc()
			return mcp.NewToolResultError(fmt.Sprintf(
				"content too large: %d bytes (limit %d, set %s to raise)",
				len(data), limit, envMaxInputBytes,
			)), nil
		}

		if _, err := workspace.WriteFile(resolver, path, data); err != nil {
			metrics.PutFileTotal.WithLabelValues(metrics.ResultFail).Inc()
			return mcp.NewToolResultErrorFromErr("write file", err), nil
		}
		metrics.PutFileTotal.WithLabelValues(metrics.ResultOK).Inc()
		return mcp.NewToolResultText(fmt.Sprintf("wrote %d bytes to %s", len(data), path)), nil
	}
}

func handleReadPDF(factory workspace.Factory) server.ResourceTemplateHandlerFunc {
	return func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		uri := request.Params.URI
		if !strings.HasPrefix(uri, pdfURIPrefix) {
			return nil, fmt.Errorf("not a typst-d2 PDF URI: %s", uri)
		}
		raw := strings.TrimPrefix(uri, pdfURIPrefix)
		path, err := url.PathUnescape(raw)
		if err != nil {
			return nil, fmt.Errorf("decode URI path: %w", err)
		}
		resolver, err := resolverFor(ctx, factory)
		if err != nil {
			return nil, fmt.Errorf("workspace setup: %w", err)
		}
		resolved, err := workspace.MustExist(resolver, path)
		if err != nil {
			return nil, err
		}
		bytes, err := os.ReadFile(resolved)
		if err != nil {
			return nil, fmt.Errorf("read pdf: %w", err)
		}
		return []mcp.ResourceContents{
			mcp.BlobResourceContents{
				URI:      uri,
				MIMEType: "application/pdf",
				Blob:     base64.StdEncoding.EncodeToString(bytes),
			},
		}, nil
	}
}

// newPDFLink builds the ResourceLink content block returned alongside the
// compile-success text. The URI uses our private typst-d2:// scheme so
// clients route the fetch through resources/read, where the same active
// resolver re-validates the path — even a stdio client gets bytes through
// the same channel that an HTTP client uses.
func newPDFLink(path string) mcp.ResourceLink {
	return mcp.NewResourceLink(
		pdfURIPrefix+url.PathEscape(path),
		filepath.Base(path),
		"Compiled Typst PDF",
		"application/pdf",
	)
}
