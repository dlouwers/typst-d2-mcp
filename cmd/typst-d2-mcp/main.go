package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/dlouwers/typst-d2-mcp/internal/preprocessor"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	serverName    = "typst-d2-mcp"
	serverVersion = "1.0.0"
)

func main() {
	// Create MCP server
	s := server.NewMCPServer(
		serverName,
		serverVersion,
		server.WithToolCapabilities(false),
	)

	// Register tools
	registerTools(s)

	// Run stdio server
	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

func registerTools(s *server.MCPServer) {
	// Single tool: Compile Typst document with embedded D2 diagrams
	compileTypstTool := mcp.NewTool("compile_typst_with_d2",
		mcp.WithDescription(`Create and compile Typst documents with embedded D2 diagrams.

This tool processes Typst documents containing #d2[...] blocks and compiles them to PDF.

BEST PRACTICES:
- Use D2 diagrams for system architectures, flowcharts, ERDs, and technical illustrations
- Embed diagrams directly in Typst using #d2[...] syntax - no separate files needed
- Supports all D2 features: layouts (elk/dagre/tala), themes, sketch mode, containers
- Clean output: diagrams are base64-encoded SVGs, no filesystem clutter

SYNTAX EXAMPLES:
  Basic diagram:
    #d2[
      client -> server -> database
    ]

  With options (layout, theme, sketch):
    #d2(layout: "elk", theme: "200", sketch: "true")[
      frontend: Frontend {shape: rectangle}
      backend: Backend {shape: rectangle}
      frontend -> backend: API calls
    ]

TYPICAL WORKFLOW:
1. Write Typst content with #d2[...] blocks for diagrams
2. Save to a .typ file
3. Call this tool with the file path
4. Receive compiled PDF with embedded diagrams

The tool encourages rich, visual documentation using D2's declarative diagram syntax.`),
		mcp.WithString("file_path",
			mcp.Required(),
			mcp.Description("Absolute path to the Typst source file (.typ) containing #d2[...] blocks"),
		),
	)
	s.AddTool(compileTypstTool, handleCompileTypst)
}
func handleCompileTypst(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	filePath, err := request.RequireString("file_path")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// Check if file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return mcp.NewToolResultError(fmt.Sprintf("File not found: %s", filePath)), nil
	}

	// Preprocess file (reads from disk)
	processed, err := preprocessor.PreprocessFile(filePath)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("Preprocessing failed", err), nil
	}

	// Create temporary file with processed content
	tmpFile, err := os.CreateTemp("", "typst-d2-*.typ")
	if err != nil {
		return mcp.NewToolResultErrorFromErr("Failed to create temp file", err), nil
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(processed); err != nil {
		return mcp.NewToolResultErrorFromErr("Failed to write temp file", err), nil
	}
	tmpFile.Close()

	// Determine output path
	outPath := strings.TrimSuffix(filePath, ".typ") + ".pdf"

	// Compile with Typst
	cmd := exec.CommandContext(ctx, "typst", "compile", tmpFile.Name(), outPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		errMsg := fmt.Sprintf("Typst compilation failed: %s\nOutput: %s", err.Error(), string(output))
		return mcp.NewToolResultError(errMsg), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("Successfully compiled to %s", outPath)), nil
}
