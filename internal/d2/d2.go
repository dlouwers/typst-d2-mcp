// Package d2 provides integration with the D2 CLI for rendering diagrams.
package d2

import (
	"bytes"
	"fmt"
	"os/exec"
)

// Options represents D2 rendering options.
type Options map[string]string

// Render executes the D2 CLI to render D2 code to SVG.
// Uses stdin->stdout streaming, no temporary files.
func Render(code string, options Options) (string, error) {
	cmd := []string{"d2"}

	// Add options
	layout := options["layout"]
	if layout == "" {
		layout = "elk"
	}
	if layout != "elk" {
		cmd = append(cmd, fmt.Sprintf("--layout=%s", layout))
	}

	if theme, ok := options["theme"]; ok {
		cmd = append(cmd, fmt.Sprintf("--theme=%s", theme))
	}

	if sketch, ok := options["sketch"]; ok && sketch == "true" {
		cmd = append(cmd, "--sketch")
	}

	if center, ok := options["center"]; ok && center == "true" {
		cmd = append(cmd, "--center")
	}

	if scale, ok := options["scale"]; ok && scale != "auto" {
		cmd = append(cmd, fmt.Sprintf("--scale=%s", scale))
	}

	// stdin -> stdout
	cmd = append(cmd, "-", "-")

	// Execute D2
	d2Cmd := exec.Command(cmd[0], cmd[1:]...)
	d2Cmd.Stdin = bytes.NewBufferString(code)

	var stdout, stderr bytes.Buffer
	d2Cmd.Stdout = &stdout
	d2Cmd.Stderr = &stderr

	if err := d2Cmd.Run(); err != nil {
		if _, lookErr := exec.LookPath("d2"); lookErr != nil {
			return "", fmt.Errorf("d2 command not found. Install from: https://d2lang.com/tour/install")
		}
		return "", fmt.Errorf("d2 rendering failed: %w\nStderr: %s", err, stderr.String())
	}

	return stdout.String(), nil
}
