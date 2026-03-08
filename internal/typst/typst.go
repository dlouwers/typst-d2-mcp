// Package typst provides integration with the Typst CLI for compiling documents.
package typst

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Compile takes preprocessed Typst content and compiles it to PDF.
// Creates a temporary .typ file, runs typst compile, then cleans up.
func Compile(content, outputFile string) error {
	// Create temporary file
	tmpFile, err := os.CreateTemp("", "typst-d2-*.typ")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	// Write content to temp file
	if _, err := tmpFile.WriteString(content); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	tmpFile.Close()

	// Compile with Typst
	cmd := exec.Command("typst", "compile", tmpPath, outputFile)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if _, lookErr := exec.LookPath("typst"); lookErr != nil {
			return fmt.Errorf("typst command not found. Install from: https://github.com/typst/typst")
		}
		return fmt.Errorf("typst compilation failed: %w\nStderr: %s", err, stderr.String())
	}

	// Make output path absolute for display
	absOutput, _ := filepath.Abs(outputFile)
	_ = absOutput // Use in future if needed

	return nil
}
