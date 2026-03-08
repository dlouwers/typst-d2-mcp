// Package prerequisites provides checks for required external binaries.
package prerequisites

import (
	"fmt"
	"os/exec"
)

// CheckAll verifies that all required binaries are available.
func CheckAll() error {
	// Check for D2
	if _, err := exec.LookPath("d2"); err != nil {
		return fmt.Errorf("d2 command not found. Install from: https://d2lang.com/tour/install")
	}

	// Check for Typst
	if _, err := exec.LookPath("typst"); err != nil {
		return fmt.Errorf("typst command not found. Install from: https://github.com/typst/typst")
	}

	return nil
}
