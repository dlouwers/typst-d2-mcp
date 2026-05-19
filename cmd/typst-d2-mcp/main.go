package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dlouwers/typst-d2-mcp/internal/preprocessor"
	"github.com/dlouwers/typst-d2-mcp/internal/workspace"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/yosida95/uritemplate/v3"
)

const (
	serverName    = "typst-d2-mcp"
	serverVersion = "1.0.0"

	envTransport = "TYPST_D2_MCP_TRANSPORT"
	envAddr      = "TYPST_D2_MCP_ADDR"
	envPath      = "TYPST_D2_MCP_PATH"
	envWorkspace = "TYPST_D2_MCP_WORKSPACE"

	defaultAddr = ":8080"
	defaultPath = "/mcp"

	// pdfURIPrefix is the scheme + host used by the compile tool when it
	// returns a ResourceLink for the produced PDF. Clients can fetch the
	// bytes via the standard MCP resources/read call against this URI.
	pdfURIPrefix = "typst-d2://pdf/"
)

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
	resolver, err := selectResolver()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Resolver setup: %v\n", err)
		os.Exit(1)
	}

	s := server.NewMCPServer(
		serverName,
		serverVersion,
		server.WithToolCapabilities(false),
		server.WithResourceCapabilities(false, false),
		server.WithInstructions(serverInstructions),
	)

	registerTools(s, resolver)
	registerResources(s, resolver)

	if err := serve(s); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

// selectResolver picks the active workspace.Resolver:
//
//   - If TYPST_D2_MCP_WORKSPACE is set, a ScopedFS rooted there is used
//     regardless of transport. This is the "hosted" / sandboxed shape and
//     is required for HTTP mode in any deployment that isn't a personal
//     localhost experiment.
//
//   - Otherwise, stdio mode keeps the existing LocalFS behavior so the
//     installed CLI experience is unchanged.
//
//   - HTTP without a workspace env falls back to a per-process default
//     under the system tmp dir. Suitable for "I just want to try the HTTP
//     transport on my laptop"; real deployments should set the env.
func selectResolver() (workspace.Resolver, error) {
	if root := os.Getenv(envWorkspace); root != "" {
		return workspace.NewScopedFS(root)
	}
	if isHTTPTransport() {
		fallback := filepath.Join(os.TempDir(), "typst-d2-mcp-workspace")
		fmt.Fprintf(os.Stderr, "%s: no %s set, using temporary workspace %s\n",
			serverName, envWorkspace, fallback)
		return workspace.NewScopedFS(fallback)
	}
	return workspace.LocalFS{}, nil
}

func isHTTPTransport() bool {
	t := strings.ToLower(os.Getenv(envTransport))
	return t == "http" || t == "streamable-http"
}

func serve(s *server.MCPServer) error {
	switch transport := strings.ToLower(os.Getenv(envTransport)); transport {
	case "", "stdio":
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
		// Stateless: each request stands on its own. Per-tenant state is
		// not derived from the MCP session — it will come from auth in a
		// follow-up sub-issue. Stateless avoids forcing clients to thread
		// a Mcp-Session-Id header through every call.
		httpSrv := server.NewStreamableHTTPServer(s,
			server.WithEndpointPath(path),
			server.WithStateLess(true),
		)
		fmt.Fprintf(os.Stderr, "%s listening on http://%s%s\n", serverName, addr, path)
		return httpSrv.Start(addr)
	default:
		return fmt.Errorf("unknown %s=%q (expected stdio or http)", envTransport, transport)
	}
}

func registerTools(s *server.MCPServer, resolver workspace.Resolver) {
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
	s.AddTool(compileTypstTool, handleCompileTypst(resolver))

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
	s.AddTool(putFileTool, handlePutFile(resolver))
}

func registerResources(s *server.MCPServer, resolver workspace.Resolver) {
	tmpl := mcp.ResourceTemplate{
		URITemplate: &mcp.URITemplate{Template: uritemplate.MustNew(pdfURIPrefix + "{+path}")},
		Name:        "pdf",
		Description: "Compiled Typst PDF produced by compile_typst_with_d2.",
		MIMEType:    "application/pdf",
	}
	s.AddResourceTemplate(tmpl, handleReadPDF(resolver))
}

func handleCompileTypst(resolver workspace.Resolver) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		filePath, err := request.RequireString("file_path")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		resolvedIn, err := workspace.MustExist(resolver, filePath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		processed, err := preprocessor.Preprocess(resolver, filePath)
		if err != nil {
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

		cmd := exec.CommandContext(ctx, "typst", "compile", tmpFile.Name(), resolvedOut)
		output, err := cmd.CombinedOutput()
		if err != nil {
			errMsg := fmt.Sprintf("Typst compilation failed: %s\nOutput: %s", err.Error(), string(output))
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

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{Type: "text", Text: successMsg},
				newPDFLink(toolVisibleOut),
			},
		}, nil
	}
}

func handlePutFile(resolver workspace.Resolver) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		path, err := request.RequireString("path")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		content, err := request.RequireString("content")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		encoding := strings.ToLower(request.GetString("encoding", "utf8"))

		var data []byte
		switch encoding {
		case "utf8", "utf-8", "":
			data = []byte(content)
		case "base64":
			d, decErr := base64.StdEncoding.DecodeString(content)
			if decErr != nil {
				return mcp.NewToolResultErrorFromErr("base64 decode", decErr), nil
			}
			data = d
		default:
			return mcp.NewToolResultError(fmt.Sprintf("unknown encoding %q (expected utf8 or base64)", encoding)), nil
		}

		if _, err := workspace.WriteFile(resolver, path, data); err != nil {
			return mcp.NewToolResultErrorFromErr("write file", err), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("wrote %d bytes to %s", len(data), path)), nil
	}
}

func handleReadPDF(resolver workspace.Resolver) server.ResourceTemplateHandlerFunc {
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
