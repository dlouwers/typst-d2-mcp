package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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
	"github.com/dlouwers/typst-d2-mcp/internal/sweeper"
	"github.com/dlouwers/typst-d2-mcp/internal/web"
	"github.com/dlouwers/typst-d2-mcp/internal/workspace"
	"github.com/dlouwers/typst-d2-mcp/templates"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/yosida95/uritemplate/v3"
)

// serverVersion is overridden at release time via
// `-ldflags="-X main.serverVersion=..."`. Must be var (not const) for
// the linker to rewrite it.
var serverVersion = "dev"

// gitSHA and buildTime are stamped the same way, from build-args the
// image workflow already computes. They are what let an operator tie a
// running container back to a commit — the admin UI shows them.
var (
	gitSHA    = "unknown"
	buildTime = "unknown"
)

const (
	serverName = "typst-d2-mcp"

	envTransport       = "TYPST_D2_MCP_TRANSPORT"
	envAddr            = "TYPST_D2_MCP_ADDR"
	envPath            = "TYPST_D2_MCP_PATH"
	envWorkspace       = "TYPST_D2_MCP_WORKSPACE"
	envCompileTimeout  = "TYPST_D2_MCP_COMPILE_TIMEOUT"
	envMaxInputBytes   = "TYPST_D2_MCP_MAX_INPUT_BYTES"
	envWorkspaceBudget = "TYPST_D2_MCP_WORKSPACE_BUDGET_BYTES"

	envAuth            = "TYPST_D2_MCP_AUTH"
	envDevUser         = "TYPST_D2_MCP_DEV_USER"
	envDB              = "TYPST_D2_MCP_DB"
	envPublicURL       = "TYPST_D2_MCP_PUBLIC_URL"
	envGitHubID        = "TYPST_D2_MCP_GITHUB_CLIENT_ID"
	envGitHubSecret    = "TYPST_D2_MCP_GITHUB_CLIENT_SECRET"
	envGitHubAllowlist = "TYPST_D2_MCP_GITHUB_ALLOWLIST"
	envAdmins          = "TYPST_D2_MCP_ADMINS"
	envEnvironment     = "TYPST_D2_MCP_ENV"
	envSessionKey      = "TYPST_D2_MCP_SESSION_KEY"
	envQuotaPerDay     = "TYPST_D2_MCP_QUOTA_PER_DAY"
	envLogLevel        = "TYPST_D2_MCP_LOG_LEVEL"
	envLogFormat       = "TYPST_D2_MCP_LOG_FORMAT"

	envMetricsAddr   = "TYPST_D2_MCP_METRICS_ADDR"
	envMetricsBearer = "TYPST_D2_MCP_METRICS_BEARER"
	envPDFLinkTTL    = "TYPST_D2_MCP_PDF_LINK_TTL"

	envWorkspaceTTL  = "TYPST_D2_MCP_WORKSPACE_TTL"
	envSweepInterval = "TYPST_D2_MCP_SWEEP_INTERVAL"

	defaultMetricsAddr = ":9090"
	defaultPDFLinkTTL  = time.Hour

	// A week is comfortably longer than any PDF link TTL, so nothing in
	// flight is ever at risk, while still bounding the data volume.
	// Workspaces are scratch space: their contents are reproducible.
	defaultWorkspaceTTL  = 168 * time.Hour
	defaultSweepInterval = time.Hour

	defaultAddr           = ":8080"
	defaultPath           = "/mcp"
	defaultCompileTimeout = 30 * time.Second
	defaultMaxInputBytes  = int64(1 << 20) // 1 MiB
	// 0 = no cumulative workspace budget (the historical behaviour); only
	// the per-call cap applies. A positive value bounds a tenant's total
	// stored bytes, enforced at put_file.
	defaultWorkspaceBudget = int64(0)
	defaultQuotaPerDay     = 1

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

// workspaceBudgetBytes is the cumulative cap on a tenant's total stored
// bytes, enforced at put_file. 0 disables the check (only the per-call
// limit applies), and it is inert in local stdio mode where the
// workspace is the shared filesystem rather than a bounded directory.
func workspaceBudgetBytes() int64 {
	return int64Env(envWorkspaceBudget, defaultWorkspaceBudget)
}

// humanBytes renders a byte count in binary units for human-facing text
// (the put_file description and its too-large error). Mirrors the admin
// UI's bytesLabel; kept here to avoid importing internal/web into the
// command.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
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

// workspaceTTL is how long a workspace file survives after its last
// modification. Zero disables the purge; sizes are still measured, so
// the admin UI's storage column keeps working with purging off.
func workspaceTTL() time.Duration {
	return durationEnv(envWorkspaceTTL, defaultWorkspaceTTL)
}

// sweepInterval is the gap between garbage-collection passes.
func sweepInterval() time.Duration {
	d := durationEnv(envSweepInterval, defaultSweepInterval)
	if d <= 0 {
		slog.Warn("sweep interval must be positive, using default",
			"env", envSweepInterval, "default", defaultSweepInterval.String())
		return defaultSweepInterval
	}
	return d
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

// serverInstructions is sent once at the MCP initialize handshake, and
// what it holds is chosen by what survives truncation. Clients cap the
// instructions they forward, and this string had grown long enough that
// the most consequential part of it — the house templates, appended at
// the end — was being cut before it reached the model. So: strategy
// that changes what the model DOES, near the front, and nothing that a
// tool description could carry instead.
//
// Diagram layout guidance in particular now lives on
// compile_typst_with_d2, which is never truncated and is read exactly
// when it is relevant. templateInstructions() is prepended, not
// appended, for the same reason.
const serverInstructions = `You can author Typst documents containing #d2[...] blocks and compile them
to PDF with the compile_typst_with_d2 tool. Diagram layout guidance —
page geometry, the star-topology anti-pattern, print-friendly defaults —
is on that tool's description; the notes below are what applies across a
whole session.

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

  Captioned (the common case) — inside #figure the call is in code
  context, so it carries no hash:
    #figure(
      d2(layout: "elk", theme: "0")[
        client -> server
      ],
      caption: [Request path.],
    )

VERIFYING THE RESULT:
  After a successful compile, open the produced PDF if you can. Check that
  text labels are readable and the diagram fits within page margins. If a
  diagram looks cramped, add "direction: down", split it into multiple
  diagrams, or remove non-essential nodes. If you cannot view the PDF
  yourself, advise the user to inspect it.

WORKSPACE, FILES & LIMITS (put_file / workspace_info):
  In hosted (HTTP) mode the server owns a per-tenant workspace and the
  document's own files do not reach it by themselves. Any asset a Typst
  document references — e.g. image("logo.png") — must be pushed with
  put_file BEFORE you compile. Do that rather than giving up or falling
  back to a local toolchain, which a hosted deployment may not have.

  put_file's size limit is on the DECODED bytes, roughly 3/4 of the
  base64 string you pass — NOT the string's length. An asset that fits
  under the limit (see workspace_info.per_call_limit_bytes) goes in one
  call; judge by the decoded size, not the encoded one.

  To upload a file LARGER than the per-call limit, stream it in chunks:
  decode the asset to bytes, split on BYTE boundaries (never split the
  base64 text), then send the first slice with mode "overwrite" and each
  remaining slice with mode "append". Each slice must be within the
  per-call limit; the slices concatenate verbatim on disk.

  FONTS: the render environment has a small fixed set of families, and
  typst SUBSTITUTES an unknown one silently while still exiting 0 — the
  PDF looks fine and is wrong. Check workspace_info.font_families before
  setting a font. To use your own typefaces, put_file the .ttf/.otf into
  the workspace "fonts/" directory; they are visible only to your
  workspace and are not aged out.

  A hosted workspace may also have a cumulative byte budget across all
  its files. Call workspace_info first — it returns per_call_limit_bytes,
  used_bytes, and (when a budget is set) budget_bytes / available_bytes —
  to see what will fit before pushing a large asset.`

// House templates live in a typst local package, so the model imports
// them by name rather than by path — which matters because the compile
// root is the staged file's temp directory, not the workspace.
//
// The namespace is an owner slug. There is one owner today, baked into
// the image; when owners become organisations the storage moves, the
// import path does not.
const (
	templateNamespace = "house"
	templateName      = "templates"
	templateVersion   = "0.1.0"
)

// typstDataDir mirrors how typst locates local packages.
func typstDataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share")
}

// seedBundledTemplates copies the binary's embedded template packages onto
// the typst package path (typstDataDir()/typst/packages) so a hosted
// deployment resolves them from the data volume rather than a baked-in
// image path — #63 step 2, "house style changes without an image rebuild".
//
// It writes only files that are absent, so it is safe to run on every
// startup and never clobbers a template an operator has customised on the
// volume (bump the package version to ship a change). The target sits under
// XDG_DATA_HOME, a sibling of the workspace root, so the sweeper — which
// only walks TYPST_D2_MCP_WORKSPACE — never purges it.
func seedBundledTemplates() {
	data := typstDataDir()
	if data == "" {
		slog.Warn("no data dir resolved; skipping template seed")
		return
	}
	pkgRoot := filepath.Join(data, "typst", "packages")
	var seeded int
	err := fs.WalkDir(templates.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		dst := filepath.Join(pkgRoot, path) // path e.g. house/templates/0.1.0/lib.typ
		if _, statErr := os.Stat(dst); statErr == nil {
			return nil // already present — never overwrite
		}
		content, readErr := templates.FS.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("read embedded %s: %w", path, readErr)
		}
		if mkErr := os.MkdirAll(filepath.Dir(dst), 0o755); mkErr != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(dst), mkErr)
		}
		if wErr := os.WriteFile(dst, content, 0o644); wErr != nil {
			return fmt.Errorf("write %s: %w", dst, wErr)
		}
		seeded++
		return nil
	})
	if err != nil {
		slog.Error("template seed failed", "err", err, "dir", pkgRoot)
		return
	}
	if seeded > 0 {
		slog.Info("seeded bundled templates onto the data volume", "files", seeded, "dir", pkgRoot)
	}
}

// templateInstructions describes the house templates to the model, or
// returns empty when the package is not installed.
//
// Advertising them unconditionally would be worse than not having them:
// a stdio user has no package directory, so the model would confidently
// write an import that cannot resolve and burn a compile finding out.
func templateInstructions() string {
	data := typstDataDir()
	if data == "" {
		return ""
	}
	pkg := filepath.Join(data, "typst", "packages",
		templateNamespace, templateName, templateVersion)
	if _, err := os.Stat(pkg); err != nil {
		return ""
	}
	return fmt.Sprintf(`TEMPLATES — READ THIS FIRST:
  This server ships document templates. Prefer them over styling a
  document yourself — they exist so that documents from different
  people, written at different times, come out looking the same.
  Reach for one whenever you are asked for a report, a memo, or a
  decision record, before writing any #set or #show rules of your own.

  CALL list_templates FIRST. Which templates you can import depends on
  who you are — the house ones below are available to everyone, and an
  organisation you belong to may publish its own. list_templates
  returns the exact import string and what each package exports; this
  section can only describe the built-in ones.

  If the look someone wants does not exist yet, write it once and
  publish_template it into your own namespace rather than restyling
  each document. Everyone has a namespace, so nothing needs granting —
  list_templates names it, under "namespaces", flagged writable. Do not
  guess a namespace name; the one that is yours is listed there.

    #import "@%[1]s/%[2]s:%[3]s": report, adr

  report — owns the look; you write whatever content suits.

    #show: report.with(
      title: "Deployment architecture",
      subtitle: "optional",
      author: "optional",
    )
    = Overview
    Body content, including #d2[...] blocks.

  adr — an architecture decision record. Its sections are arguments
  rather than headings, so every ADR carries the same ones:

    #show: adr.with(
      title: "Use a GitHub App for machine access",
      number: 7,
      status: "Accepted",
      background: [Why this came up.],
      decision: [What was decided.],
      consequences: [What follows.],
      alternatives: [Optional.],
    )

  Note "background", not "context": context is a reserved word in Typst.
  The rendered heading still reads "Context".

  Do not override the template's fonts, colours or page setup. A
  document that restyles itself has opted out of the house style, which
  is the one thing these templates exist to prevent.

`,
		templateNamespace, templateName, templateVersion)
}

// selfStyling marks a document as having taken its own look in hand.
// Each is something a template would otherwise own.
var selfStyling = []string{
	"#set page(",
	"#set text(",
	"#set par(",
	"#show heading",
	"#set heading(",
}

// templateNudge returns a note to append to a successful compile when a
// document styles itself but imports no house template, and "" when
// there is nothing to say.
//
// The instructions already ask for templates to be preferred, but
// instructions are advisory, arrive once, and get truncated by clients
// — a caller can finish a whole document without ever having seen them.
// A compile is the one moment the server knows both what was written
// and what was available, so it is the honest place to mention it.
// Deliberately a note on a success, not a warning or a failure: styling
// a document by hand is allowed, just rarely what was wanted.
func templateNudge(src string) string {
	if templateInstructions() == "" {
		return "" // no package installed; nothing to point at
	}
	// Any template import counts as "has seen the templates", not just
	// the built-in one: since #63 rung 4 a caller may be styling with
	// their own organisation's package, and nagging them about the
	// house style would be both wrong and unactionable.
	if strings.Contains(src, `#import "@`) {
		return ""
	}
	importPath := fmt.Sprintf("@%s/%s:%s", templateNamespace, templateName, templateVersion)
	for _, marker := range selfStyling {
		if strings.Contains(src, marker) {
			return fmt.Sprintf(
				"NOTE: this document sets its own page/text/heading rules and imports no "+
					"template. Templates are available and are what keeps documents "+
					"consistent:\n  #import \"%s\": report, adr\n"+
					"Call list_templates to see everything you can import.", importPath)
		}
	}
	return ""
}

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

	// Hosted mode seeds the house templates onto the data volume before the
	// server advertises them, so a fresh volume resolves them and an
	// operator can edit them there without an image rebuild (#63 step 2).
	// Local stdio users are left untouched: they only see templates they
	// installed themselves.
	if isHTTPTransport() {
		seedBundledTemplates()
	}

	s := server.NewMCPServer(
		serverName,
		serverVersion,
		server.WithToolCapabilities(false),
		server.WithResourceCapabilities(false, false),
		// Templates FIRST. Appended, they sat behind the longest part
		// of the instructions and were the first thing a client's
		// truncation limit cut — which made the server's main
		// consistency mechanism undiscoverable to the callers who
		// most needed it.
		server.WithInstructions(templateInstructions()+serverInstructions),
	)

	registerTools(s, factory, store)
	registerResources(s, factory)

	slog.Info("starting",
		"version", serverVersion,
		"git_sha", gitSHA,
		"build_time", buildTime,
		"env", os.Getenv(envEnvironment),
		"auth", backend.Name(),
		"admins", len(parseAllowlist(os.Getenv(envAdmins))),
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
	// backend is the same *auth.GitHub the handlers below hang off,
	// kept so the admin UI can drive its browser login through it.
	backend             *auth.GitHub
	githubCallback      http.HandlerFunc
	wellKnownResource   http.HandlerFunc
	wellKnownAuthServer http.HandlerFunc
	register            http.HandlerFunc
	authorize           http.HandlerFunc
	token               http.HandlerFunc
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
				ClientID:      clientID,
				ClientSecret:  clientSecret,
				PublicURL:     publicURL,
				AllowedLogins: parseAllowlist(os.Getenv(envGitHubAllowlist)),
				AdminLogins:   parseAllowlist(os.Getenv(envAdmins)),
			},
			Store: store,
		}
		closer := func() { _ = store.Close() }
		return gh, &gitHubHandlers{
			backend:             gh,
			githubCallback:      gh.ServeCallback,
			wellKnownResource:   gh.ServeWellKnownProtectedResource,
			wellKnownAuthServer: gh.ServeWellKnownAuthorizationServer,
			register:            gh.ServeRegister,
			authorize:           gh.ServeAuthorize,
			token:               gh.ServeToken,
		}, store, closer, nil
	case "dev":
		// Real identities, no credentials. Selected explicitly and
		// never as a fallback, and refused on a non-loopback bind (see
		// auth.RequireLoopback, enforced in serve).
		dbPath := os.Getenv(envDB)
		if dbPath == "" {
			return nil, nil, nil, nil, fmt.Errorf("%s=dev requires %s to be set", envAuth, envDB)
		}
		store, err := authdb.Open(dbPath)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("open auth db: %w", err)
		}
		dev := &auth.Dev{
			Store:        store,
			DefaultLogin: os.Getenv(envDevUser),
			AdminLogins:  parseAllowlist(os.Getenv(envAdmins)),
		}
		return dev, nil, store, func() { _ = store.Close() }, nil
	default:
		return nil, nil, nil, nil, fmt.Errorf("unknown %s=%q (expected none, github or dev)", envAuth, mode)
	}
}

// parseAllowlist turns a comma-separated TYPST_D2_MCP_GITHUB_ALLOWLIST
// value into a lowercased set of permitted GitHub logins. An empty or
// whitespace-only value yields nil — meaning "no restriction", the
// public free-tier posture.
func parseAllowlist(raw string) map[string]bool {
	set := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		login := strings.ToLower(strings.TrimSpace(part))
		if login != "" {
			set[login] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
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
		// Dev auth grants any identity to anyone who can reach the
		// port, so a non-loopback bind is not something to warn about.
		// Refusing to start is the only guard that survives somebody
		// leaving AUTH=dev set in an environment where it stops being
		// harmless.
		if _, isDev := backend.(*auth.Dev); isDev {
			if err := auth.RequireLoopback(addr); err != nil {
				return err
			}
			slog.Warn("DEV AUTH ENABLED — every request is whoever it says it is",
				"header", auth.DevUserHeader, "addr", addr)
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

			admin, err := newAdminUI(gh.backend, store, factory)
			if err != nil {
				return fmt.Errorf("admin ui: %w", err)
			}
			mux.Handle("/admin/", admin.Handler())

			// One GitHub OAuth app means one registered callback URL,
			// shared by the MCP authorization-server flow and the admin
			// browser login. The `state` parameter tells them apart:
			// admin logins carry web.StatePrefix, MCP authorizations
			// carry an authorize-session id.
			mux.HandleFunc("/auth/github/callback", func(w http.ResponseWriter, r *http.Request) {
				if web.IsAdminState(r.URL.Query().Get("state")) {
					admin.CompleteLogin(w, r)
					return
				}
				gh.githubCallback(w, r)
			})
		}

		// /metrics binds to a separate listener so it never shares
		// the public Ingress with the app port. NetworkPolicy in
		// the k8s manifests restricts the metrics port further to
		// the monitoring namespace only.
		startMetricsListener()

		// Garbage collection needs the database (expired links) and, for
		// the file purge, a server-owned workspace root. AUTH=none has
		// no store, and LocalFactory deployments do not own the
		// filesystem, so both are skipped rather than guessed at.
		if store != nil {
			startSweeper(store, factory)
		}

		slog.Info("listening", "addr", addr, "path", path)
		return http.ListenAndServe(addr, mux) //nolint:gosec // intentional plain HTTP; TLS is terminated upstream.
	default:
		return fmt.Errorf("unknown %s=%q (expected stdio or http)", envTransport, transport)
	}
}

// newAdminUI builds the /admin server. The session signing key comes
// from the environment; without one we generate a random key and warn,
// which works but logs every admin out on restart.
func newAdminUI(gh *auth.GitHub, store *authdb.Store, factory workspace.Factory) (*web.Server, error) {
	key := []byte(os.Getenv(envSessionKey))
	if len(key) == 0 {
		generated, err := web.RandomKey()
		if err != nil {
			return nil, err
		}
		key = generated
		slog.Warn("no admin session key configured; sessions will not survive a restart",
			"env", envSessionKey)
	}

	var root string
	if tf, ok := factory.(workspace.TenantFactory); ok {
		root = tf.Root
	}
	// Secure cookies are dropped by browsers over plain http, which
	// would make local development impossible to sign in to. Follow the
	// deployment's own public URL rather than guessing.
	secure := strings.HasPrefix(strings.ToLower(gh.Cfg.PublicURL), "https://")
	if !secure {
		slog.Warn("admin session cookies will not be marked Secure",
			"public_url", gh.Cfg.PublicURL)
	}

	// Naming an administrator (or an allowlist) is what closes the
	// server to uninvited accounts. Say so plainly at startup: turning
	// this on is how an open deployment becomes invite-only, and
	// existing users who were relying on the open posture will be
	// denied until they are invited.
	if len(gh.Cfg.AdminLogins) == 0 {
		slog.Warn("no administrators configured; /admin is unreachable", "env", envAdmins)
	} else {
		slog.Info("admin ui enabled",
			"admins", len(gh.Cfg.AdminLogins),
			"invite_only", gh.InviteOnly())
	}

	// Schema revision is read once: migrations only run at startup, so
	// it cannot change under a running process.
	revision, err := store.SchemaRevision()
	if err != nil {
		return nil, fmt.Errorf("read schema revision: %w", err)
	}

	return web.New(web.Config{
		Store:                  store,
		GitHub:                 gh,
		Sessions:               web.NewSessionCodec(key, 12*time.Hour, secure),
		WorkspaceRoot:          root,
		QuotaDefault:           quotaPerDay,
		WorkspaceBudgetDefault: workspaceBudgetBytes,
		Build: web.BuildInfo{
			Environment:    os.Getenv(envEnvironment),
			Version:        serverVersion,
			GitSHA:         gitSHA,
			BuildTime:      buildTime,
			SchemaRevision: revision,
		},
	})
}

// startSweeper launches periodic garbage collection in the background:
// expired pdf_links rows always, plus aged-out workspace files and
// per-user size measurement when the server owns a tenant workspace
// root. Only TenantFactory has a root to sweep — LocalFS resolves to
// paths the client owns, which are not ours to delete.
func startSweeper(store *authdb.Store, factory workspace.Factory) {
	var root string
	if tf, ok := factory.(workspace.TenantFactory); ok {
		root = tf.Root
	}
	sw := sweeper.New(store, sweeper.Config{
		Root:     root,
		FileTTL:  workspaceTTL(),
		Interval: sweepInterval(),
	})
	go sw.Run(context.Background())
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

// Layout guidance lives HERE rather than in the server instructions.
// It costs tokens on every call, which is why it used to sit in the
// instructions — but the instructions are truncated by clients and
// this is read at exactly the moment it applies, which is the better
// trade. What stays in the instructions is what spans a session.
func compileToolDescription() string {
	return `Compile a Typst document containing #d2[...] diagram blocks to PDF.

Each #d2(opts)[code] block is rendered to SVG via the d2 CLI,
base64-embedded, and the source compiled with typst. The PDF is written
next to the input .typ in the active workspace; a resource_link in the
result points at it (fetch with resources/read on its typst-d2://pdf/...
URI).

DIAGRAM LAYOUT — A4 PORTRAIT (the Typst default):
  Usable area is roughly 17cm wide × 25cm tall, so vertical layouts
  breathe while horizontal ones get cramped. Prefer 'direction: down'
  inside the D2 block for anything past a handful of nodes.

STAR-TOPOLOGY ANTI-PATTERN:
  A central node with 5+ siblings forces ELK to spread the children
  horizontally even when 'direction: down' is set. Chain them instead:

      center -> a    // BAD: renders horizontally on A4 portrait
      center -> b
      center -> c
      center -> d

      center -> a -> b -> c -> d    // GOOD

  Two or three direct reports can stay a star; four or more means a
  chain, or split into several diagrams.

A4 LANDSCAPE (#set page(flipped: true)):
  ~25cm × 17cm. Prefer 'direction: right' for wide hierarchies.

PRINT-FRIENDLY DEFAULTS:
  layout "elk" (best automatic layout), theme "0" (white background,
  good contrast on paper). Avoid dark themes (100–200) for print.

After compiling, inspect the PDF if you can; if a diagram looks cramped,
add 'direction: down', split it, or simplify it. If you cannot view the
PDF yourself, tell the user to check the layout.

A relative path in the document — #image("logo.svg"), #read("data.csv"),
#import "style.typ" — resolves against the directory of the .typ file
being compiled, so an asset pushed with put_file next to the document is
referenced by its plain name. In scoped/hosted mode a leading slash
addresses the workspace root ("/assets/logo.svg"), and typst cannot read
outside the workspace.`
}

func registerTools(s *server.MCPServer, factory workspace.Factory, store *authdb.Store) {
	compileTypstTool := mcp.NewTool("compile_typst_with_d2",
		mcp.WithDescription(compileToolDescription()),
		mcp.WithString("file_path",
			mcp.Required(),
			mcp.Description("Path to the Typst source file (.typ) containing #d2[...] blocks. Absolute in local stdio mode; workspace-relative in scoped/hosted mode."),
		),
	)
	s.AddTool(compileTypstTool, handleCompileTypst(factory, store))

	inputLimit := maxInputBytes()
	putFileTool := mcp.NewTool("put_file",
		mcp.WithDescription(fmt.Sprintf(`Write a file into the server's active workspace. Use only when your
runtime cannot write to the target filesystem directly (e.g. a hosted
MCP server over HTTP); against a local stdio server prefer your host's
Write/Edit tools.

Each call's content is capped at %d bytes (%s), measured AFTER decoding
(for base64, ~3/4 of the string — judge by the decoded size, not the
string length). To upload a LARGER file, send it in slices within that
cap: first slice mode "overwrite", the rest "append". A hosted workspace
may also have a cumulative byte budget — call workspace_info to check the
limit and remaining space before a large push.

Paths are workspace-relative in scoped/hosted mode (traversal rejected),
any path in local mode.

A file pushed here IS referenceable from a document you then compile:
put an asset beside the .typ that uses it and name it relatively —
put_file "logo.svg" then #image("logo.svg"). See the server instructions
for the full file / upload guide.`,
			inputLimit, humanBytes(inputLimit))),
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
		mcp.WithString("mode",
			mcp.Description(`"overwrite" (default) truncates and writes; "append" adds to the end of an existing file, for streaming a file larger than the per-call limit in chunks.`),
		),
	)
	s.AddTool(putFileTool, handlePutFile(factory, store))

	workspaceInfoTool := mcp.NewTool("workspace_info",
		mcp.WithDescription(`Report the active workspace's storage limits and current usage. Call
this before pushing a large asset with put_file to see what will fit.

No arguments; returns JSON:
  - per_call_limit_bytes / per_call_limit_human: max size of one put_file
    payload, on DECODED bytes.
  - usage_tracked: true in scoped/hosted mode (bounded per-tenant dir),
    false in local stdio mode.
  - used_bytes / used_human: total bytes stored, measured live (only when
    usage_tracked).
  - budget_bytes / available_bytes (+ *_human): the cumulative cap and
    remaining space, present only when a budget is configured (absent =
    no cap). A put_file over available_bytes is rejected.
  - typst_version / d2_version: the binaries this server actually runs.
  - font_families: every font family a compile can resolve, including
    any you have pushed. Typst SUBSTITUTES an unknown family silently
    and still exits 0, so a document naming one produces a PDF that
    looks fine and is wrong — check this list before setting a font.
  - fonts_dir: push .ttf/.otf files here (via put_file) to use your own
    typefaces; they are visible only to your workspace. Present only
    where the workspace is bounded.

See the server instructions for the full file / upload guide.`),
	)
	s.AddTool(workspaceInfoTool, handleWorkspaceInfo(factory, store))

	// Discovery has to be a tool rather than a line of instructions:
	// since #63 rung 4 a compile resolves only the caller's own
	// namespaces, so the answer differs per caller and no fixed string
	// can carry it.
	listTemplatesTool := mcp.NewTool("list_templates",
		mcp.WithDescription(`List the document templates you can import, for this caller.

PREFER A TEMPLATE over styling a document yourself — they exist so that
documents from different people, written at different times, come out
looking the same. Call this before writing any #set or #show rules.

No arguments; returns JSON:
  - namespaces[]: every namespace you can reach, including ones with
    nothing in them yet. "writable" marks the ones you may publish to
    with publish_template — normally your own. This is where you find
    your own namespace's name; do not guess it.
  - templates[]: namespace, name, version, and the exact "import" string
    to use, plus "exports" — the functions that package offers.
  - builtin: true for the server's house templates, available to
    everyone. Others come from organisations you belong to.
  - note: present only when nothing is available to you.

Use it like:
  #import "<the import string>": <an export>
  #show: report.with(title: "...")

An empty list means no template is installed for you — style the
document yourself, or ask an administrator to publish one.`),
	)
	s.AddTool(listTemplatesTool, handleListTemplates(store))

	publishTemplateTool := mcp.NewTool("publish_template",
		mcp.WithDescription(`Publish a document template into a namespace you own.

Everyone has their own namespace, so you can publish without anyone
granting you anything. list_templates shows which namespaces are yours.

Put the package's files in a workspace directory with put_file first:

  my-template/typst.toml       [package] name/version/entrypoint
  my-template/lib.typ          #let report(title: "Untitled", body) = { … }
  my-template/assets/logo.svg  optional — a package may ship its own files
  my-template/fonts/Brand.ttf  optional — and its own typefaces

Then publish that directory. THE TEMPLATE IS COMPILED BEFORE IT IS
ACCEPTED: if it does not compile, nothing is published and you get the
typst error. That check exists because a broken template does not fail
for you — it fails for everyone who imports it afterwards.

A published version is IMMUTABLE. Documents pin what they import, so
replacing 1.0.0 would change how already-written documents render.
Publish 1.0.1 instead; the old version keeps working.`),
		mcp.WithString("source",
			mcp.Required(),
			mcp.Description("Workspace directory holding typst.toml and the entrypoint."),
		),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("Namespace name to publish into. Must be one you own — see list_templates."),
		),
		mcp.WithString("version",
			mcp.Required(),
			mcp.Description(`Package version, major.minor.patch (e.g. "1.0.0"). typst rejects any other shape.`),
		),
		mcp.WithString("name",
			mcp.Description(`Package name within the namespace; defaults to "templates", giving @you/templates:1.0.0.`),
		),
	)
	s.AddTool(publishTemplateTool, handlePublishTemplate(factory, store))
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
			// A per-user override wins over the deployment default; an
			// admin can raise one person or exempt them entirely
			// (override 0) without moving the ceiling for everyone.
			limit, err := store.EffectiveQuota(ctx, id.UserID, quotaPerDay())
			if err != nil {
				// EffectiveQuota already fell back to the default; the
				// compile proceeds rather than failing on a read error.
				log.Warn("quota override lookup failed; using default", "err", err)
			}
			today := time.Now().UTC().Format("2006-01-02")
			if err := store.IncrementCompile(ctx, id.UserID, today, limit); err != nil {
				if errors.Is(err, authdb.ErrQuotaExceeded) {
					metrics.CompileTotal.WithLabelValues(metrics.ResultQuotaExceeded).Inc()
					log.Warn("quota exceeded", "limit", limit)
					return mcp.NewToolResultError(fmt.Sprintf(
						"quota exceeded: %d compile(s) per UTC day per user (resets at 00:00 UTC; "+
							"ask an administrator to raise your quota)",
						limit,
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

		// Stage the preprocessed source NEXT TO the input rather than in
		// /tmp. typst resolves a document's relative paths against the
		// directory of the file it is handed, so staging in /tmp made
		// every asset pushed with put_file unreachable: put_file
		// reported success, and the failure surfaced later as
		// "file not found (searched at /tmp/logo.svg)", which reads
		// like a document bug rather than a server one.
		//
		// The staged file is removed on the way out, and the workspace
		// sweeper would collect it anyway if a crash left one behind.
		// workspace.DirBytes skips the prefix so a compile in flight
		// does not inflate reported usage or eat into a byte budget.
		tmpFile, err := os.CreateTemp(filepath.Dir(resolvedIn), workspace.StagePrefix+"*.typ")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("Failed to create temp file", err), nil
		}
		defer os.Remove(tmpFile.Name())

		if _, err := tmpFile.WriteString(processed); err != nil {
			return mcp.NewToolResultErrorFromErr("Failed to write temp file", err), nil
		}
		// Checked: a failed flush here would hand typst a truncated
		// document, and the compile error that followed would point at the
		// wrong thing.
		if err := tmpFile.Close(); err != nil {
			return mcp.NewToolResultErrorFromErr("Failed to close temp file", err), nil
		}

		// Output PDF sits next to the input .typ inside the workspace.
		resolvedOut := strings.TrimSuffix(resolvedIn, ".typ") + ".pdf"

		// Capture stdout + stderr separately. Typst exits 0 with
		// warnings on stderr for things like "cannot place at top of
		// page" or oversized images that overflow the column —
		// silent on success would mean a half-rendered PDF gets
		// reported as a clean compile. We surface warnings to the
		// caller so the LLM/operator can act on them.
		// Rung 4 of #63: the typst child sees only the package
		// namespaces this caller may import. Built as a symlink tree
		// per compile rather than filtered at import time — a
		// namespace that is not in the tree cannot be reached by any
		// spelling of the import.
		allowed, err := allowedNamespaces(ctx, store, id)
		if err != nil {
			metrics.CompileTotal.WithLabelValues(metrics.ResultFail).Inc()
			log.Error("namespace resolution failed", "err", err)
			return mcp.NewToolResultErrorFromErr("resolve template namespaces", err), nil
		}
		view, cleanupView, err := packageView(typstDataDir(), allowed)
		if err != nil {
			metrics.CompileTotal.WithLabelValues(metrics.ResultFail).Inc()
			log.Error("package view failed", "err", err)
			return mcp.NewToolResultErrorFromErr("prepare template namespaces", err), nil
		}
		defer cleanupView()

		var stdoutBuf, stderrBuf bytes.Buffer
		cmd := exec.CommandContext(ctx, "typst",
			typstArgs(resolver, tmpFile.Name(), resolvedOut, packageFontPath(view))...)
		cmd.Env = compileEnv(view)
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

		// Instructions are advisory and get truncated; a warning on a
		// successful compile is neither. If the document styled itself
		// by hand while the house templates sat right there, say so
		// where it cannot be missed.
		if nudge := templateNudge(processed); nudge != "" {
			successMsg += "\n\n" + nudge
		}

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
// typstArgs builds the typst invocation for a staged source file.
//
// A bounded workspace becomes typst's root, which does two things: it
// lets a document address an asset from the workspace root ("/logo.svg")
// as well as relatively, and it stops typst reading anything outside the
// tenant's own directory. An unbounded resolver (stdio mode, where the
// "workspace" is the user's filesystem) gets no --root, leaving typst's
// default of the input file's own directory.
//
// A workspace fonts/ directory, if present, is added as a font path so
// a tenant can set documents in its own typefaces. The path is inside
// the tenant's own root, so one workspace's fonts are not visible to
// another.
//
// extraFontPaths carries font directories the caller has already
// resolved — today the package view, so a template can ship the
// typeface it is designed in. Empty strings are ignored.
func typstArgs(r workspace.Resolver, in, out string, extraFontPaths ...string) []string {
	args := []string{"compile"}
	if b, ok := r.(workspace.Bounded); ok {
		args = append(args, "--root", b.WorkspaceRoot())
	}
	if fonts := workspaceFontPath(r); fonts != "" {
		args = append(args, "--font-path", fonts)
	}
	for _, p := range extraFontPaths {
		if p != "" {
			args = append(args, "--font-path", p)
		}
	}
	return append(args, in, out)
}

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
		info, err := f.Stat()
		if err != nil {
			metrics.PDFDownloadTotal.WithLabelValues(metrics.ResultFail).Inc()
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		name := filepath.Base(link.FilePath)
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename=%q`, name))
		w.Header().Set("Cache-Control", "private, max-age=300")
		// ServeContent honours a caller-set ETag but never invents one,
		// and a compiled PDF is fully described by (mtime, size) — a
		// recompile changes both. With it set, conditional GETs and
		// Range requests are handled for us.
		w.Header().Set("ETag", fmt.Sprintf(`"%x-%x"`, info.ModTime().UnixNano(), info.Size()))
		metrics.PDFDownloadTotal.WithLabelValues(metrics.ResultOK).Inc()
		// Replaces io.Copy: gives Range (resumable downloads) and
		// If-None-Match / If-Modified-Since handling.
		http.ServeContent(w, r, name, info.ModTime(), f)
	})
}

func handlePutFile(factory workspace.Factory, store *authdb.Store) server.ToolHandlerFunc {
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
		mode := strings.ToLower(request.GetString("mode", "overwrite"))
		if mode != "overwrite" && mode != "append" {
			return mcp.NewToolResultError(fmt.Sprintf("unknown mode %q (expected overwrite or append)", mode)), nil
		}

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
				"content too large: %d bytes decoded (%s) exceeds the limit of %d bytes (%s); set %s on the server to raise it",
				len(data), humanBytes(int64(len(data))), limit, humanBytes(limit), envMaxInputBytes,
			)), nil
		}

		// Cumulative workspace budget (per-workspace override, else the env
		// default). Inert when unset (0) or in local mode, where the
		// workspace has no bounded size (tracked=false).
		id, _ := identity.FromContext(ctx)
		if budget := effectiveWorkspaceBudget(ctx, store, id); budget > 0 {
			projected, tracked, err := projectedUsage(resolver, path, int64(len(data)), mode)
			if err != nil {
				metrics.PutFileTotal.WithLabelValues(metrics.ResultFail).Inc()
				return mcp.NewToolResultErrorFromErr("measure workspace", err), nil
			}
			if tracked && projected > budget {
				metrics.PutFileTotal.WithLabelValues(metrics.ResultOverBudget).Inc()
				return mcp.NewToolResultError(fmt.Sprintf(
					"workspace budget exceeded: this write would bring the workspace to %d bytes (%s), over the %d byte (%s) budget; "+
						"delete files you no longer need, or ask an administrator to raise %s",
					projected, humanBytes(projected), budget, humanBytes(budget), envWorkspaceBudget,
				)), nil
			}
		}

		write := workspace.WriteFile
		if mode == "append" {
			write = workspace.AppendFile
		}
		if _, err := write(resolver, path, data); err != nil {
			metrics.PutFileTotal.WithLabelValues(metrics.ResultFail).Inc()
			return mcp.NewToolResultErrorFromErr("write file", err), nil
		}
		metrics.PutFileTotal.WithLabelValues(metrics.ResultOK).Inc()
		verb := "wrote"
		if mode == "append" {
			verb = "appended"
		}
		return mcp.NewToolResultText(fmt.Sprintf("%s %d bytes to %s", verb, len(data), path)), nil
	}
}

// effectiveWorkspaceBudget resolves the byte budget for the request: the
// per-workspace override when a store is present and the caller is a real
// tenant, otherwise the env default. It never fails the request — a store
// read error falls back to the default rather than blocking the write.
func effectiveWorkspaceBudget(ctx context.Context, store *authdb.Store, id identity.Identity) int64 {
	def := workspaceBudgetBytes()
	if store == nil || id.IsAnonymous() {
		return def
	}
	b, err := store.EffectiveWorkspaceBudget(ctx, id.UserID, def)
	if err != nil {
		slog.Warn("workspace budget override lookup failed; using default",
			"user", id.UserID, "err", err)
		return def
	}
	return b
}

// projectedUsage returns what the workspace would total after writing
// newSize bytes to path in the given mode, and whether the workspace's
// size is even tracked (false in local mode, where a cumulative budget is
// meaningless). In "overwrite" mode the target's current size is
// discounted, so rewriting a file never counts its bytes twice; in
// "append" mode the new bytes add to the existing file, so nothing is
// discounted.
func projectedUsage(r workspace.Resolver, path string, newSize int64, mode string) (total int64, tracked bool, err error) {
	used, tracked, err := workspace.Usage(r)
	if err != nil || !tracked {
		return 0, tracked, err
	}
	if mode == "append" {
		return used + newSize, true, nil
	}
	var existing int64
	if resolved, rerr := r.Resolve(path); rerr == nil {
		if info, serr := os.Stat(resolved); serr == nil && info.Mode().IsRegular() {
			existing = info.Size()
		}
	}
	return used - existing + newSize, true, nil
}

// workspaceInfo is the JSON payload returned by the workspace_info tool.
// Used/Budget/Available fields are pointers/omitempty so they are absent
// (not a misleading zero) when they do not apply: usage in local mode, and
// budget/available when no budget is configured.
type workspaceInfo struct {
	PerCallLimitBytes int64  `json:"per_call_limit_bytes"`
	PerCallLimitHuman string `json:"per_call_limit_human"`
	UsageTracked      bool   `json:"usage_tracked"`
	UsedBytes         *int64 `json:"used_bytes,omitempty"`
	UsedHuman         string `json:"used_human,omitempty"`
	BudgetBytes       *int64 `json:"budget_bytes,omitempty"`
	BudgetHuman       string `json:"budget_human,omitempty"`
	AvailableBytes    *int64 `json:"available_bytes,omitempty"`
	AvailableHuman    string `json:"available_human,omitempty"`

	// What this environment can actually do, as opposed to how much of
	// it is left. A caller had no way to learn any of this short of
	// spending a compile on a probe document — and typst substitutes a
	// missing font silently, so the probe was the only way to find out
	// at all.
	TypstVersion string   `json:"typst_version,omitempty"`
	D2Version    string   `json:"d2_version,omitempty"`
	FontFamilies []string `json:"font_families,omitempty"`
	FontsDir     string   `json:"fonts_dir,omitempty"`
}

func handleWorkspaceInfo(factory workspace.Factory, store *authdb.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		resolver, err := resolverFor(ctx, factory)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("workspace setup", err), nil
		}
		limit := maxInputBytes()
		used, tracked, err := workspace.Usage(resolver)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("measure workspace", err), nil
		}
		typstVersion, d2Version := toolVersions()
		info := workspaceInfo{
			PerCallLimitBytes: limit,
			PerCallLimitHuman: humanBytes(limit),
			UsageTracked:      tracked,
			TypstVersion:      typstVersion,
			D2Version:         d2Version,
			FontFamilies:      fontFamilies(workspaceFontPath(resolver)),
		}
		if _, ok := resolver.(workspace.Bounded); ok {
			info.FontsDir = FontsDir
		}
		if tracked {
			u := used
			info.UsedBytes = &u
			info.UsedHuman = humanBytes(used)
			// Budget/available only when a budget applies (per-workspace
			// override, else the env default). Unconfigured means unlimited,
			// so the fields stay absent.
			id, _ := identity.FromContext(ctx)
			if budget := effectiveWorkspaceBudget(ctx, store, id); budget > 0 {
				b := budget
				info.BudgetBytes = &b
				info.BudgetHuman = humanBytes(budget)
				avail := budget - used
				if avail < 0 {
					avail = 0
				}
				info.AvailableBytes = &avail
				info.AvailableHuman = humanBytes(avail)
			}
		}
		payload, err := json.MarshalIndent(info, "", "  ")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("encode workspace info", err), nil
		}
		return mcp.NewToolResultText(string(payload)), nil
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
