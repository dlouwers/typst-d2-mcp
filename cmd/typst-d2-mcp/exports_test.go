package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The failure this closes: an agent given a namespace containing
// somebody else's template, and told to use it, wrote a probe document
// and compiled it purely to read the argument names out of the error.
// Everything it learned that way is now in the listing.

func exportByName(t *testing.T, exports []exportEntry, name string) exportEntry {
	t.Helper()
	for _, e := range exports {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("no export named %q in %v", name, exports)
	return exportEntry{}
}

func writeLib(t *testing.T, content string) string {
	t.Helper()
	lib := filepath.Join(t.TempDir(), "lib.typ")
	if err := os.WriteFile(lib, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return lib
}

func TestParseExports_NamedParametersAndDefaults(t *testing.T) {
	lib := writeLib(t, `#let memo(title: "Untitled", cc: none, body) = { body }`)
	memo := exportByName(t, parseExports(lib), "memo")

	want := []paramEntry{
		{Name: "title", Default: `"Untitled"`},
		{Name: "cc", Default: "none"},
		{Name: "body", Positional: true},
	}
	if len(memo.Params) != len(want) {
		t.Fatalf("params = %+v, want %+v", memo.Params, want)
	}
	for i, w := range want {
		if memo.Params[i] != w {
			t.Errorf("param %d = %+v, want %+v", i, memo.Params[i], w)
		}
	}
}

// A trailing positional body is what makes an export applicable with
// `#show:`. Reporting it is the difference between a caller writing
// `#show: memo.with(...)` and calling `memo(...)` — which fails.
func TestParseExports_ReportsWhetherAnExportTakesABody(t *testing.T) {
	lib := writeLib(t, `
#let memo(title: "Untitled", body) = { body }
#let swatch(colour) = { colour }
`)
	exports := parseExports(lib)
	if !exportByName(t, exports, "memo").Template {
		t.Error("memo takes a body but was not reported as a template")
	}
	if exportByName(t, exports, "swatch").Template {
		t.Error("swatch takes no body but was reported as a template")
	}
}

// A default containing a comma, a colon or a bracket must not be
// mistaken for the end of the parameter, or the reported signature is
// worse than none at all.
func TestParseExports_DefaultsWithSeparatorsSurvive(t *testing.T) {
	lib := writeLib(t, `
#let x(
  date: datetime.today().display("[year]-[month]-[day]"),
  pad: (x: 1pt, y: 2pt),
  label: "a, b",
  body,
) = { body }
`)
	x := exportByName(t, parseExports(lib), "x")
	got := map[string]string{}
	for _, p := range x.Params {
		got[p.Name] = p.Default
	}
	for name, want := range map[string]string{
		"date":  `datetime.today().display("[year]-[month]-[day]")`,
		"pad":   "(x: 1pt, y: 2pt)",
		"label": `"a, b"`,
	} {
		if got[name] != want {
			t.Errorf("%s default = %q, want %q", name, got[name], want)
		}
	}
	if !x.Template {
		t.Error("the trailing body was lost")
	}
}

// A parameter list written across several lines is reported as a call,
// not as source — a caller copies the signature, so it has to read like
// something they can paste.
func TestParseExports_SignatureIsOneLine(t *testing.T) {
	lib := writeLib(t, `
#let memo(
  title: "Untitled",
  body,
) = { body }
`)
	got := exportByName(t, parseExports(lib), "memo").Signature
	want := `memo(title: "Untitled", body)`
	if got != want {
		t.Errorf("signature = %q, want %q", got, want)
	}
}

// The `///` convention already existed in the house template before
// anything read it — the explanation of why ADR's first section is
// `background:` was written for a human reading the source. It is the
// answer to "what does this argument mean", which a signature alone
// cannot give.
func TestParseExports_DocCommentIsCarried(t *testing.T) {
	lib := writeLib(t, `
/// A memo.
///
/// Keep it short.
#let memo(body) = { body }
#let plain(body) = { body }
`)
	exports := parseExports(lib)
	if got, want := exportByName(t, exports, "memo").Doc, "A memo.\n\nKeep it short."; got != want {
		t.Errorf("doc = %q, want %q", got, want)
	}
	// A `///` block belongs to the declaration directly below it and
	// must not leak onto the next one.
	if doc := exportByName(t, exports, "plain").Doc; doc != "" {
		t.Errorf("plain picked up a doc comment: %q", doc)
	}
}

// An ordinary comment is not documentation, and must not be reported as
// though the author wrote one.
func TestParseExports_PlainCommentIsNotDoc(t *testing.T) {
	lib := writeLib(t, "// internal note, not for callers\n#let memo(body) = { body }\n")
	if doc := exportByName(t, parseExports(lib), "memo").Doc; doc != "" {
		t.Errorf("doc = %q, want empty", doc)
	}
}

// Degrading is the requirement: a listing that omits a description is
// worth far more than a listing that fails.
func TestParseExports_UnreadableOrEmptyDegrades(t *testing.T) {
	if got := parseExports(filepath.Join(t.TempDir(), "absent.typ")); got != nil {
		t.Errorf("missing entrypoint = %v, want nil", got)
	}
	if got := parseExports(writeLib(t, "// nothing here\n")); got != nil {
		t.Errorf("entrypoint with no exports = %v, want nil", got)
	}
	// An unterminated parameter list is not something to fail over.
	if got := parseExports(writeLib(t, "#let broken(title: \"x\"\n")); got != nil {
		t.Errorf("unparseable entrypoint = %v, want nil", got)
	}
}

// The house signatures are the ones every caller sees, and #123 added
// `numbering:` to both — a listing that did not report it would send
// callers back to compiling probes for the newest argument.
func TestParseExports_HouseTemplateSignatures(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	seedBundledTemplates()

	got := listTemplates(t, context.Background(), nil)
	if len(got.Templates) != 1 {
		t.Fatalf("got %d templates, want 1", len(got.Templates))
	}
	for _, name := range []string{"report", "adr"} {
		e := exportByName(t, got.Templates[0].Exports, name)
		if !e.Template {
			t.Errorf("%s should be applicable with #show:", name)
		}
		if !strings.HasPrefix(e.Signature, name+"(title: ") {
			t.Errorf("%s signature = %q, want it to start with its title argument", name, e.Signature)
		}
		if !strings.HasSuffix(e.Signature, ", body)") {
			t.Errorf("%s signature = %q, want it to end in the positional body", name, e.Signature)
		}
		var hasNumbering bool
		for _, p := range e.Params {
			if p.Name == "numbering" {
				hasNumbering = p.Default == "none"
			}
		}
		if !hasNumbering {
			t.Errorf("%s does not report numbering: none — params %+v", name, e.Params)
		}
		if e.Doc == "" {
			t.Errorf("%s has no doc comment; the house templates are the worked example", name)
		}
	}
}

// Two versions of a package describe themselves separately, from their
// own entrypoints. The listing reads the same file the compile
// resolves, so it cannot advertise a signature that belongs to a
// version the caller is not importing.
func TestParseExports_DescribeTheVersionThatIsImported(t *testing.T) {
	f := newPublishFixture(t)
	f.stage(t, "v1", map[string]string{
		"typst.toml": goodTOML,
		"lib.typ":    `#let report(title: "Untitled", body) = { body }`,
	})
	if res := f.publish(t, "v1", f.nsName, "1.0.0"); res.IsError {
		t.Fatalf("publish 1.0.0: %s", resultText(res))
	}
	f.stage(t, "v2", map[string]string{
		"typst.toml": strings.Replace(goodTOML, "1.0.0", "2.0.0", 1),
		"lib.typ":    `#let report(title: "Untitled", author: none, body) = { body }`,
	})
	if res := f.publish(t, "v2", f.nsName, "2.0.0"); res.IsError {
		t.Fatalf("publish 2.0.0: %s", resultText(res))
	}

	got := listTemplates(t, f.ctx, f.store)
	seen := map[string]string{}
	for _, tpl := range got.Templates {
		if tpl.Namespace != f.nsName {
			continue
		}
		seen[tpl.Version] = exportByName(t, tpl.Exports, "report").Signature
	}
	if want := `report(title: "Untitled", body)`; seen["1.0.0"] != want {
		t.Errorf("1.0.0 signature = %q, want %q", seen["1.0.0"], want)
	}
	if want := `report(title: "Untitled", author: none, body)`; seen["2.0.0"] != want {
		t.Errorf("2.0.0 signature = %q, want %q", seen["2.0.0"], want)
	}
}

// The claim this whole listing rests on is that a caller can write a
// correct call from it WITHOUT compiling first. That is not something to
// assert by hand — so this synthesises a call from nothing but the
// parsed signature and puts it through the real typst binary. If a
// parameter name is misread, or a positional one is reported as named
// (or the reverse), typst refuses it.
func TestParseExports_ASignatureIsEnoughToWriteACorrectCall(t *testing.T) {
	if _, err := exec.LookPath("typst"); err != nil {
		t.Skip("typst not installed")
	}
	lib := filepath.Join(repoTemplates(t), templateNamespace, "templates", "0.1.0", "lib.typ")
	exports := parseExports(lib)
	if len(exports) == 0 {
		t.Fatalf("no exports parsed from %s", lib)
	}

	var applied int
	for _, e := range exports {
		if !e.Template {
			continue
		}
		applied++
		t.Run(e.Name, func(t *testing.T) {
			// Built from the listing alone: every named parameter passed
			// by name with the default the listing reported, and the body
			// left to `#show:`, which is what "template": true promises.
			var args []string
			for _, p := range e.Params {
				if p.Positional {
					if p.Name != "body" {
						t.Fatalf("%s takes a positional %q the listing gives no way to supply",
							e.Name, p.Name)
					}
					continue
				}
				args = append(args, p.Name+": "+p.Default)
			}
			src := fmt.Sprintf("#import %q: %s\n#show: %s.with(%s)\n= Heading\nBody.\n",
				"@"+templateNamespace+"/templates:0.1.0", e.Name, e.Name,
				strings.Join(args, ", "))

			dir := t.TempDir()
			in := filepath.Join(dir, "doc.typ")
			if err := os.WriteFile(in, []byte(src), 0o600); err != nil {
				t.Fatal(err)
			}
			staged := t.TempDir()
			pkgRoot := filepath.Join(staged, "typst", "packages")
			if err := os.MkdirAll(pkgRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(repoTemplates(t), templateNamespace),
				filepath.Join(pkgRoot, templateNamespace)); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("typst", "compile", in, filepath.Join(dir, "doc.pdf"))
			cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+staged)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("a call written from the listing alone did not compile: %v\n%s\n\n%s",
					err, src, out)
			}
		})
	}
	if applied == 0 {
		t.Fatal("no export was reported as applicable with #show:")
	}
}
