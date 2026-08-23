package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoTemplates is the package tree as it sits in the repo, which the
// Dockerfile copies into the image's typst package directory.
func repoTemplates(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "templates"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("templates dir missing: %v", err)
	}
	return dir
}

// The instructions are only honest when the package is actually
// installed. A stdio user has no package directory, and telling the
// model to import something unresolvable costs them a compile — and,
// against a metered server, possibly their whole day's quota.
func TestTemplateInstructions_OnlyWhenInstalled(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", t.TempDir())
		if got := templateInstructions(); got != "" {
			t.Errorf("instructions advertised templates that are not installed:\n%s", got)
		}
	})

	t.Run("present", func(t *testing.T) {
		data := t.TempDir()
		pkg := filepath.Join(data, "typst", "packages",
			templateNamespace, templateName, templateVersion)
		if err := os.MkdirAll(pkg, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		t.Setenv("XDG_DATA_HOME", data)

		got := templateInstructions()
		for _, want := range []string{
			`@house/templates:0.1.0`,
			"report",
			"adr",
			"background", // not "context" — reserved word in Typst
		} {
			if !strings.Contains(got, want) {
				t.Errorf("instructions missing %q", want)
			}
		}
	})
}

// Compiles both document types through the real typst binary, against
// the package exactly as the repo ships it. This is what catches a
// template that is valid Typst but broken as an API — a parameter typst
// reserves, or a body that a show rule cannot pass.
func TestHouseTemplates_Compile(t *testing.T) {
	if _, err := exec.LookPath("typst"); err != nil {
		t.Skip("typst not installed")
	}

	cases := map[string]string{
		"report": `#import "@house/templates:0.1.0": report
#show: report.with(title: "A report", subtitle: "sub", author: "someone")
= Heading
Body text with #emph[emphasis] and a #link("https://example.test")[link].
`,
		"adr": `#import "@house/templates:0.1.0": adr
#show: adr.with(
  title: "A decision",
  number: 1,
  status: "Accepted",
  background: [Why.],
  decision: [What.],
  consequences: [So what.],
)
== Appendix
Trailing content.
`,
		// Defaults must work: the model will omit optional arguments.
		"report with defaults": `#import "@house/templates:0.1.0": report
#show: report.with(title: "Minimal")
= Only a title
`,
	}

	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			in := filepath.Join(dir, "doc.typ")
			out := filepath.Join(dir, "doc.pdf")
			if err := os.WriteFile(in, []byte(src), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}

			// The repo keeps the tree at <repo>/templates/<ns>/..., while
			// typst wants <data>/typst/packages/<ns>/..., so stage a
			// directory in that shape rather than fighting the layout.
			staged := t.TempDir()
			pkgRoot := filepath.Join(staged, "typst", "packages")
			if err := os.MkdirAll(pkgRoot, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.Symlink(filepath.Join(repoTemplates(t), templateNamespace),
				filepath.Join(pkgRoot, templateNamespace)); err != nil {
				t.Fatalf("symlink: %v", err)
			}

			cmd := exec.Command("typst", "compile", in, out)
			cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+staged)

			if outBytes, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("typst compile failed: %v\n%s", err, outBytes)
			}
			if info, err := os.Stat(out); err != nil || info.Size() == 0 {
				t.Errorf("no PDF produced: %v", err)
			}
		})
	}
}
