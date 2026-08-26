package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dlouwers/typst-d2-mcp/internal/workspace"
)

// FontsDir is the workspace subdirectory a tenant puts its own typefaces
// in. Passing it to typst as a --font-path is what makes an organisation
// template an organisation template: a brand that cannot use its own
// typeface is a house style with a different accent colour, and
// typography carries more identity than palette does.
//
// Licensing sits with the tenant, which is the right place for it — the
// server ships only faces it may redistribute.
const FontsDir = "fonts"

// workspaceFontPath returns the tenant's font directory when it exists
// and is usable, or "" otherwise. Absent is the normal case and not an
// error: most workspaces have no fonts of their own.
func workspaceFontPath(r workspace.Resolver) string {
	b, ok := r.(workspace.Bounded)
	if !ok {
		return "" // stdio mode: system fonts are already the user's own
	}
	dir := filepath.Join(b.WorkspaceRoot(), FontsDir)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return ""
	}
	return dir
}

// probeTimeout bounds the version/font probes. They shell out to the
// same binaries a compile uses, and workspace_info must not be the tool
// that hangs.
const probeTimeout = 5 * time.Second

// fontFamilies lists the font families typst can resolve, including any
// the tenant supplied. The list is what a caller needs to avoid the
// failure this exists to prevent: typst substitutes silently for an
// unknown family, so the PDF looks fine and is wrong.
func fontFamilies(fontPath string) []string {
	args := []string{"fonts"}
	if fontPath != "" {
		args = append(args, "--font-path", fontPath)
	}
	out, err := runProbe("typst", args...)
	if err != nil {
		return nil
	}
	var families []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			families = append(families, line)
		}
	}
	return families
}

var (
	versionOnce sync.Once
	typstVer    string
	d2Ver       string
)

// toolVersions reports the typst and d2 versions this image actually
// runs. They cannot change while the process does, so probe once.
func toolVersions() (typstVersion, d2Version string) {
	versionOnce.Do(func() {
		typstVer = firstLine(mustProbe("typst", "--version"))
		d2Ver = firstLine(mustProbe("d2", "--version"))
	})
	return typstVer, d2Ver
}

func mustProbe(name string, args ...string) string {
	out, err := runProbe(name, args...)
	if err != nil {
		return ""
	}
	return out
}

func runProbe(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.Output()
		close(done)
	}()
	select {
	case <-done:
		return string(out), err
	case <-time.After(probeTimeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		return "", os.ErrDeadlineExceeded
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
