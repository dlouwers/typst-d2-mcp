package main

import (
	"bufio"
	"bytes"
	"log/slog"
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
// missingBinary is what a version field reads when the binary is not
// there. It is a value rather than an omission on purpose: an omitted
// field is indistinguishable from one the server does not implement, so
// a server that cannot render a single diagram looked healthy. An agent
// wrote a whole document with diagrams before finding out (#110).
const missingBinary = "NOT INSTALLED"

func toolVersions() (typstVersion, d2Version string) {
	versionOnce.Do(func() {
		typstVer = probeVersion("typst")
		d2Ver = probeVersion("d2")
	})
	return typstVer, d2Ver
}

func probeVersion(name string) string {
	out, err := runProbe(name, "--version")
	if err != nil {
		return missingBinary
	}
	if v := firstLine(out); v != "" {
		return v
	}
	return missingBinary
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

// pdfPageCount reads the page count straight out of the PDF, so a
// caller learns how long its document is without needing to open it.
//
// Counting "/Type /Page" objects is crude but has no dependency and
// cannot fail the compile: a zero return simply omits the figure. The
// alternative — shelling out to a PDF tool — would add a binary the
// image does not ship, which is the shape of problem #110 is about.
func pdfPageCount(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	// "/Type/Page" with optional whitespace, not followed by "s"
	// (which would be /Pages, the tree node rather than a leaf).
	n := 0
	for i := 0; i+9 < len(raw); i++ {
		if raw[i] != '/' || !bytes.HasPrefix(raw[i:], []byte("/Type")) {
			continue
		}
		j := i + 5
		for j < len(raw) && (raw[j] == ' ' || raw[j] == '\n' || raw[j] == '\r') {
			j++
		}
		if !bytes.HasPrefix(raw[j:], []byte("/Page")) {
			continue
		}
		k := j + 5
		if k < len(raw) && (raw[k] == 's' || isPDFNameByte(raw[k])) {
			continue
		}
		n++
	}
	return n
}

func isPDFNameByte(c byte) bool {
	return c == '-' || c == '_' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// familiesUnder lists the font families a directory provides, ignoring
// system and typst-embedded fonts so the answer is attributable to that
// directory alone. An unreadable or absent path yields nothing, which
// is the normal case for a tenant with no fonts of their own.
func familiesUnder(dir string) []string {
	if dir == "" {
		return nil
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil
	}
	// workspace.DirName guarantees tenant paths carry no list separator,
	// so a path can simply be named. This is the assertion that the
	// guarantee holds: an unsafe path is refused LOUDLY rather than
	// quietly returning nothing. Silence was the actual harm in #107 —
	// the fonts were missing and everything said they were fine.
	if !safeFontPath(dir) {
		slog.Error("refusing to probe a font path containing the list separator; "+
			"typst would read nothing from it", "path", dir)
		return nil
	}
	out, err := runProbe("typst", "fonts", "--font-path", dir,
		"--ignore-system-fonts", "--ignore-embedded-fonts")
	if err != nil {
		return nil
	}
	return nonEmptyLines(out)
}

// embeddedFamilies lists what typst carries itself.
func embeddedFamilies() []string {
	out, err := runProbe("typst", "fonts", "--ignore-system-fonts")
	if err != nil {
		return nil
	}
	return nonEmptyLines(out)
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}
