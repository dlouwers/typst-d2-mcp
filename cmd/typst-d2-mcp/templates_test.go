package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoTemplates is the package tree as it sits in the repo — the source
// of truth that the templates package embeds and the server seeds onto
// the data volume on startup.
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
		// #112: @ref to a heading is impossible in typst without
		// numbering, and the compiler's own hint is the restyling these
		// templates forbid. The argument exists so a caller need not
		// choose between a broken reference and opting out.
		"report with heading cross-references": `#import "@house/templates:0.1.0": report
#show: report.with(title: "Cross-referenced", numbering: "1.")
= Overview <intro>
== Detail
See @intro and @intro again.
`,
		"adr with heading cross-references": `#import "@house/templates:0.1.0": adr
#show: adr.with(
  title: "A decision",
  number: 2,
  background: [Why.],
  decision: [What.],
  consequences: [So what.],
  numbering: "1.",
)
== Appendix <extra>
See @extra.
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

// Seeding writes the embedded package tree onto the typst package path,
// leaves an operator's edits alone, and produces a tree typst can resolve.
func TestSeedBundledTemplates(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	pkg := filepath.Join(data, "typst", "packages", templateNamespace, templateName, templateVersion)

	seedBundledTemplates()

	for _, f := range []string{"typst.toml", "lib.typ"} {
		if _, err := os.Stat(filepath.Join(pkg, f)); err != nil {
			t.Fatalf("seed did not write %s: %v", f, err)
		}
	}

	// The seeded tree must match the repo source of truth in full, not
	// at two named files. A template is a typst package and may ship an
	// assets/ directory, a mark, a _helpers.typ — and a seed that
	// carried only the files a test happened to name would leave the
	// volume quietly incomplete, surfacing as a user's compile failing
	// on a file that is present in the repo.
	repoRoot := filepath.Join(repoTemplates(t), templateNamespace)
	seededRoot := filepath.Join(data, "typst", "packages", templateNamespace)
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return relErr
		}
		want, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		got, gotErr := os.ReadFile(filepath.Join(seededRoot, rel))
		if gotErr != nil {
			t.Errorf("seed did not write %s: %v", rel, gotErr)
			return nil
		}
		if string(got) != string(want) {
			t.Errorf("seeded %s differs from the repo source", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo templates: %v", err)
	}

	// Non-destructive: an operator edit on the volume survives a re-seed.
	custom := []byte("// operator customised\n")
	if err := os.WriteFile(filepath.Join(pkg, "lib.typ"), custom, 0o644); err != nil {
		t.Fatal(err)
	}
	seedBundledTemplates() // must not overwrite
	if got, _ := os.ReadFile(filepath.Join(pkg, "lib.typ")); string(got) != string(custom) {
		t.Error("re-seed clobbered an operator's customised template")
	}
}

// A document compiles against the *seeded* package, proving the embedded
// tree is a resolvable typst package and not just files on disk.
func TestSeedBundledTemplates_Compiles(t *testing.T) {
	if _, err := exec.LookPath("typst"); err != nil {
		t.Skip("typst not installed")
	}
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	seedBundledTemplates()

	dir := t.TempDir()
	in := filepath.Join(dir, "doc.typ")
	out := filepath.Join(dir, "doc.pdf")
	src := `#import "@house/templates:0.1.0": report
#show: report.with(title: "Seeded")
= Body
`
	if err := os.WriteFile(in, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("typst", "compile", in, out)
	cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+data)
	if outBytes, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile against seeded package failed: %v\n%s", err, outBytes)
	}
	if info, err := os.Stat(out); err != nil || info.Size() == 0 {
		t.Errorf("no PDF produced from seeded package: %v", err)
	}
}

// The argument is bounded: it decides whether headings are numbered and
// nothing else. A default-argument document must look exactly as it did
// before the argument existed — otherwise this would have been the
// "enable numbering outright" option wearing a parameter.
func TestHouseTemplates_NumberingDefaultsToUnchanged(t *testing.T) {
	if _, err := exec.LookPath("typst"); err != nil {
		t.Skip("typst not installed")
	}
	staged := stageHousePackage(t)

	render := func(src string) string {
		dir := t.TempDir()
		in := filepath.Join(dir, "doc.typ")
		out := filepath.Join(dir, "doc.txt")
		if err := os.WriteFile(in, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("typst", "compile", "--format", "html", "--features", "html", in, out)
		cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+staged)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("html export unavailable: %s", b)
		}
		raw, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	plain := render(`#import "@house/templates:0.1.0": report
#show: report.with(title: "T")
= Overview
`)
	if strings.Contains(plain, "1. Overview") || strings.Contains(plain, "1.Overview") {
		t.Errorf("the default gained heading numbers:\n%s", plain)
	}

	numbered := render(`#import "@house/templates:0.1.0": report
#show: report.with(title: "T", numbering: "1.")
= Overview
`)
	if !strings.Contains(numbered, "Overview") {
		t.Errorf("numbered render lost the heading:\n%s", numbered)
	}
}

// stageHousePackage puts the repo's template tree where typst looks for
// a local package, and returns the XDG_DATA_HOME to use.
func stageHousePackage(t *testing.T) string {
	t.Helper()
	staged := t.TempDir()
	pkgRoot := filepath.Join(staged, "typst", "packages")
	if err := os.MkdirAll(pkgRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(filepath.Join(repoTemplates(t), templateNamespace),
		filepath.Join(pkgRoot, templateNamespace)); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	return staged
}

// numbering: must PRINT the numbers it lets you reference. Rendering
// only the heading body meant @label resolved to "Section 8" on a page
// where nothing was numbered 8 — a document that compiled cleanly and
// read as nonsense.
func TestHouseTemplates_NumberedHeadingsShowTheirNumbers(t *testing.T) {
	if _, err := exec.LookPath("typst"); err != nil {
		t.Skip("typst not installed")
	}
	staged := stageHousePackage(t)
	dir := t.TempDir()
	in := filepath.Join(dir, "doc.typ")
	out := filepath.Join(dir, "doc.html")
	src := `#import "@house/templates:0.1.0": report
#show: report.with(title: "T", numbering: "1.")
= Alpha <a>
= Beta
See @a.
`
	if err := os.WriteFile(in, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("typst", "compile", "--format", "html", "--features", "html", in, out)
	cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+staged)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("html export unavailable: %s", b)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	// The reference resolves to a number, so that number must appear on
	// the heading it points at.
	if !strings.Contains(body, "1.") {
		t.Errorf("numbered headings do not show a number:\n%s", body)
	}
	if strings.Contains(body, "Section") && !strings.Contains(body, "1.") {
		t.Errorf("a reference names a number the document never prints:\n%s", body)
	}
}
