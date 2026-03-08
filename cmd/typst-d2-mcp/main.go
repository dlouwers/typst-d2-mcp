package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/dlouwers/typst-d2-mcp/internal/d2"
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
	// Tool 1: Render D2 diagram to SVG
	renderD2Tool := mcp.NewTool("render_d2",
		mcp.WithDescription("Render D2 diagram code to SVG format"),
		mcp.WithString("d2_code",
			mcp.Required(),
			mcp.Description("D2 diagram source code to render"),
		),
		mcp.WithString("layout",
			mcp.Description("Layout engine to use"),
			mcp.DefaultString("elk"),
			mcp.Enum("elk", "dagre", "tala"),
		),
		mcp.WithString("theme",
			mcp.Description("Theme ID (0-200)"),
			mcp.DefaultString(""),
		),
		mcp.WithString("sketch",
			mcp.Description("Enable hand-drawn sketch mode"),
			mcp.DefaultString("false"),
			mcp.Enum("true", "false"),
		),
	)
	s.AddTool(renderD2Tool, handleRenderD2)

	// Tool 2: Render D2 diagram to base64-encoded SVG
	renderD2Base64Tool := mcp.NewTool("render_d2_base64",
		mcp.WithDescription("Render D2 diagram code to base64-encoded SVG (for embedding in Typst)"),
		mcp.WithString("d2_code",
			mcp.Required(),
			mcp.Description("D2 diagram source code to render"),
		),
		mcp.WithString("layout",
			mcp.Description("Layout engine to use"),
			mcp.DefaultString("elk"),
			mcp.Enum("elk", "dagre", "tala"),
		),
		mcp.WithString("theme",
			mcp.Description("Theme ID (0-200)"),
			mcp.DefaultString(""),
		),
		mcp.WithString("sketch",
			mcp.Description("Enable hand-drawn sketch mode"),
			mcp.DefaultString("false"),
			mcp.Enum("true", "false"),
		),
	)
	s.AddTool(renderD2Base64Tool, handleRenderD2Base64)

	// Tool 3: Compile Typst document with D2 preprocessing
	compileTypstTool := mcp.NewTool("compile_typst_with_d2",
		mcp.WithDescription("Preprocess and compile Typst document with embedded D2 diagrams"),
		mcp.WithString("file_path",
			mcp.Required(),
			mcp.Description("Path to the Typst source file (.typ)"),
		),
	)
	s.AddTool(compileTypstTool, handleCompileTypst)

	// Tool 4: Preprocess Typst content (without compiling)
	preprocessTypstTool := mcp.NewTool("preprocess_typst",
		mcp.WithDescription("Preprocess Typst content with D2 diagrams, returning the processed Typst code"),
		mcp.WithString("typst_content",
			mcp.Required(),
			mcp.Description("Typst source code containing #d2[...] blocks"),
		),
	)
	s.AddTool(preprocessTypstTool, handlePreprocessTypst)
}

func handleRenderD2(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	d2Code, err := request.RequireString("d2_code")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	layout := request.GetString("layout", "elk")
	theme := request.GetString("theme", "")
	sketch := request.GetString("sketch", "false")

	// Build D2 options
	opts := make(map[string]string)
	opts["layout"] = layout
	if theme != "" {
		opts["theme"] = theme
	}
	if sketch == "true" {
		opts["sketch"] = "true"
	}

	// Render D2 diagram
	svg, err := d2.Render(d2Code, opts)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("D2 rendering failed", err), nil
	}

	return mcp.NewToolResultText(svg), nil
}

func handleRenderD2Base64(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	d2Code, err := request.RequireString("d2_code")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	layout := request.GetString("layout", "elk")
	theme := request.GetString("theme", "")
	sketch := request.GetString("sketch", "false")

	// Build D2 options
	opts := make(map[string]string)
	opts["layout"] = layout
	if theme != "" {
		opts["theme"] = theme
	}
	if sketch == "true" {
		opts["sketch"] = "true"
	}

	// Render D2 diagram
	svg, err := d2.Render(d2Code, opts)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("D2 rendering failed", err), nil
	}

	// Encode to base64
	b64 := base64.StdEncoding.EncodeToString([]byte(svg))

	// Return both base64 and Typst snippet
	result := fmt.Sprintf("Base64: %s\n\nTypst code:\n#image(decode64(\"%s\"), format: \"svg\")", b64, b64)
	return mcp.NewToolResultText(result), nil
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

func handlePreprocessTypst(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	typstContent, err := request.RequireString("typst_content")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// For preprocessing content directly, we need a helper function
	// Since PreprocessFile only works with files, create a temp file
	tmpFile, err := os.CreateTemp("", "typst-d2-*.typ")
	if err != nil {
		return mcp.NewToolResultErrorFromErr("Failed to create temp file", err), nil
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(typstContent); err != nil {
		return mcp.NewToolResultErrorFromErr("Failed to write temp file", err), nil
	}
	tmpFile.Close()

	// Preprocess the temp file
	processed, err := preprocessor.PreprocessFile(tmpFile.Name())
	if err != nil {
		return mcp.NewToolResultErrorFromErr("Preprocessing failed", err), nil
	}

	return mcp.NewToolResultText(processed), nil
}
