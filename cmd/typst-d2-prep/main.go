package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dlouwers/typst-d2-mcp/internal/preprocessor"
	"github.com/dlouwers/typst-d2-mcp/internal/typst"
	"github.com/dlouwers/typst-d2-mcp/internal/prerequisites"
)

var version = "0.1.0" // Overridden by ldflags during release builds

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "compile":
		if err := compileCommand(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "version", "--version", "-v":
		fmt.Printf("typst-d2-prep version %s\n", version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		printUsage()
		os.Exit(1)
	}
}

func compileCommand(args []string) error {
	// Check prerequisites first
	if err := prerequisites.CheckAll(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(args) < 1 {
		return fmt.Errorf("usage: typst-d2-prep compile input.typ [output.pdf]")
	}

	inputFile := args[0]
	var outputFile string

	if len(args) > 1 {
		outputFile = args[1]
	} else {
		// Default: replace .typ extension with .pdf
		ext := filepath.Ext(inputFile)
		outputFile = inputFile[:len(inputFile)-len(ext)] + ".pdf"
	}

	// Check input file exists
	if _, err := os.Stat(inputFile); os.IsNotExist(err) {
		return fmt.Errorf("input file not found: %s", inputFile)
	}

	// Preprocess the file
	fmt.Fprintf(os.Stderr, "Processing %s...\n", inputFile)
	processedContent, err := preprocessor.PreprocessFile(inputFile)
	if err != nil {
		return fmt.Errorf("preprocessing failed: %w", err)
	}

	// Compile with Typst
	fmt.Fprintf(os.Stderr, "Compiling with Typst...\n")
	if err := typst.Compile(processedContent, outputFile); err != nil {
		return fmt.Errorf("compilation failed: %w", err)
	}

	fmt.Fprintf(os.Stderr, "✅ Created: %s\n", outputFile)
	return nil
}

func printUsage() {
	usage := `typst-d2-prep: Preprocessor for D2 diagrams in Typst documents

Usage:
    typst-d2-prep compile input.typ [output.pdf]
    typst-d2-prep version
    typst-d2-prep help

Commands:
    compile    Process D2 diagrams and compile to PDF
    version    Show version information
    help       Show this help message

Examples:
    typst-d2-prep compile document.typ
    typst-d2-prep compile document.typ output.pdf
`
	fmt.Fprint(os.Stderr, usage)
}
