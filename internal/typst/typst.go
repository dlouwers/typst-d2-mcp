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
//
// The temporary file is staged in the OUTPUT directory, not in /tmp.
// typst resolves a document's relative paths against the directory of
// the file it is handed, so staging elsewhere breaks every
// #image("logo.png") in the document — with an error that names /tmp
// and reads like a bug in the document rather than in this function.
func Compile(content, outputFile string) error {
	stageDir := filepath.Dir(outputFile)
	if stageDir == "" {
		stageDir = "."
	}

	// Create temporary file
	tmpFile, err := os.CreateTemp(stageDir, ".typst-d2-stage-*.typ")
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
	// Checked, unlike the close in the error path above: a failed flush
	// here means typst would compile a truncated document.
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

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
