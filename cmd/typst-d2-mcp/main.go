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

CRITICAL - DIAGRAM LAYOUT STRATEGY:
Before creating diagrams, consider the page format and diagram complexity:

For A4 Portrait (default Typst):
  - WIDTH: ~17cm usable (limited horizontal space)
  - HEIGHT: ~25cm usable (ample vertical space)
  - BEST: Use 'direction: down' (vertical) for complex diagrams with many nodes
  - AVOID: Wide horizontal layouts - they get cramped and text becomes tiny
  - RULE: If diagram has >5 nodes at same level, use vertical direction

For A4 Landscape (set with #set page(flipped: true)):
  - WIDTH: ~25cm usable (ample horizontal space)
  - HEIGHT: ~17cm usable (limited vertical space)
  - BEST: Use 'direction: right' (horizontal) for wide hierarchies

Direction control in D2:
  - Add 'direction: down' at top of diagram for vertical flow (RECOMMENDED for portrait)
  - Add 'direction: right' at top of diagram for horizontal flow (use sparingly)
  - ELK layout respects direction hints and produces best results

VISUAL VERIFICATION (STRONGLY RECOMMENDED):
After compilation, if you have access to visual tools:
  1. Open the generated PDF to verify diagram readability
  2. Check that text labels are readable (not too small)
  3. Verify diagram fits within page margins without crowding
  4. If diagram is cramped: reduce nodes, split into multiple diagrams, or use direction: down
  5. If you cannot view the PDF yourself, inform the user about potential layout issues

BEST PRACTICES:
- Use D2 diagrams for system architectures, flowcharts, ERDs, and technical illustrations
- Embed diagrams directly in Typst using #d2[...] syntax - no separate files needed
- ALWAYS use layout: "elk" (best automatic layout engine)
- ALWAYS use theme: "0" for print-friendly white backgrounds with good contrast
- For print: avoid dark themes (100-200 range) - they have poor contrast on white paper
- For complex diagrams: use 'direction: down' to maximize use of A4 portrait height
- Split very large diagrams into multiple smaller, focused diagrams rather than cramming everything
- Supports all D2 features: layouts (elk/dagre/tala), themes, sketch mode, containers
- Clean output: diagrams are base64-encoded SVGs, no filesystem clutter

SYNTAX EXAMPLES:
  Basic diagram (print-friendly, vertical for portrait):
    #d2(layout: "elk", theme: "0")[
      direction: down
      client -> server -> database
    ]

  With shapes and custom styling (optimized for A4 portrait):
    #d2(layout: "elk", theme: "0")[
      direction: down
      
      frontend: Frontend {shape: rectangle}
      backend: Backend {shape: rectangle}
      database: Database {shape: cylinder}
      
      frontend -> backend: API calls
      backend -> database: Queries
    ]

  Large organizational hierarchy (MUST use vertical direction):
    #d2(layout: "elk", theme: "0")[
      direction: down  // CRITICAL for readability in portrait
      
      board: "Board of Directors" {
        shape: rectangle
        style.fill: "#e8f4f8"
      }
      
      exec: "Executive Team" {
        ceo: "CEO"
        cto: "CTO"
        cfo: "CFO"
      }
      
      board -> exec
    ]

TYPICAL WORKFLOW:
1. Analyze diagram requirements: How many nodes? How complex?
2. Choose direction: 'down' for A4 portrait (default), 'right' only if landscape or simple
3. Write Typst content with #d2[...] blocks for diagrams
4. Save to a .typ file
5. Call this tool with the file path
6. IF POSSIBLE: Visually verify the PDF to check diagram layout and readability
7. If diagram is cramped: add 'direction: down' or split into multiple diagrams

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

	
	successMsg := fmt.Sprintf("Successfully compiled to %s\n\n", outPath)
	successMsg += "NEXT STEPS:\n"
	successMsg += "1. Open the PDF to verify diagram layout and readability\n"
	successMsg += "2. Check that diagrams fit within page margins (not cramped)\n"
	successMsg += "3. Verify text labels are readable (not too small)\n"
	successMsg += "4. If diagrams are cramped or text is tiny:\n"
	successMsg += "   - Add 'direction: down' at top of D2 block (for A4 portrait)\n"
	successMsg += "   - Split large diagrams into multiple focused diagrams\n"
	successMsg += "   - Reduce number of nodes or simplify structure\n"
	successMsg += "\nIf you cannot view the PDF yourself, inform the user to check the layout."
	
	return mcp.NewToolResultText(successMsg), nil
}
