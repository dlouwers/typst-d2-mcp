package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installTemplates puts an empty package tree where typst (and
// templateInstructions) look for one, and returns nothing: the point is
// the side effect on XDG_DATA_HOME.
func installTemplates(t *testing.T) {
	t.Helper()
	data := t.TempDir()
	pkg := filepath.Join(data, "typst", "packages",
		templateNamespace, templateName, templateVersion)
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("XDG_DATA_HOME", data)
}

// The templates are the server's main consistency mechanism, and they
// used to be appended after the longest string in the server — so a
// client with an instructions cap cut them entirely and a caller could
// author a whole document without learning they existed. They must now
// come first, and early enough to survive a modest cap.
func TestInstructions_TemplatesComeFirst(t *testing.T) {
	installTemplates(t)

	full := templateInstructions() + serverInstructions

	tmplAt := strings.Index(full, "HOUSE TEMPLATES")
	if tmplAt < 0 {
		t.Fatal("HOUSE TEMPLATES section missing from the instructions")
	}
	if tmplAt != 0 {
		t.Errorf("HOUSE TEMPLATES starts at byte %d, want the very front", tmplAt)
	}

	importAt := strings.Index(full, "@house/templates:0.1.0")
	if importAt < 0 {
		t.Fatal("the import line is missing")
	}
	// A client that forwards only the first 2000 bytes must still see
	// the import line and both template names. The number is a stand-in
	// for "any plausible cap", not a measured limit.
	const cap = 2000
	head := full
	if len(head) > cap {
		head = head[:cap]
	}
	for _, want := range []string{"@house/templates:0.1.0", "report", "adr"} {
		if !strings.Contains(head, want) {
			t.Errorf("%q does not survive a %d-byte instructions cap", want, cap)
		}
	}
}

// Layout guidance moved out of the instructions and onto the tool,
// which is never truncated and is read when it is relevant. Assert it
// arrived, and that it did not stay behind in both places.
func TestCompileToolDescription_CarriesLayoutGuidance(t *testing.T) {
	desc := compileToolDescription()

	for _, want := range []string{
		"direction: down",
		"STAR-TOPOLOGY",
		"A4",
		`theme "0"`,
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("tool description missing layout guidance %q", want)
		}
	}

	if strings.Contains(serverInstructions, "STAR-TOPOLOGY ANTI-PATTERN") {
		t.Error("star-topology guidance is still duplicated in the server instructions")
	}
}

// The nudge is the half of discoverability that does not depend on a
// client forwarding anything: it fires at the one moment the server
// knows both what was written and what was available.
func TestTemplateNudge(t *testing.T) {
	installTemplates(t)

	cases := []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "self-styled with no template",
			src:  "#set page(margin: 2cm)\n= Title\nBody.\n",
			want: true,
		},
		{
			name: "own heading rules",
			src:  "#show heading: it => text(weight: 700, it.body)\n= Title\n",
			want: true,
		},
		{
			name: "own fonts",
			src:  "#set text(font: \"Libertinus Serif\")\n= Title\n",
			want: true,
		},
		{
			name: "imports the template",
			src:  "#import \"@house/templates:0.1.0\": report\n#show: report.with(title: \"T\")\n= Title\n",
			want: false,
		},
		{
			// Importing a template AND setting page rules is the
			// caller overriding deliberately. Say nothing: they have
			// clearly seen the templates.
			name: "imports the template and still sets page rules",
			src:  "#import \"@house/templates:0.1.0\": report\n#set page(margin: 2cm)\n",
			want: false,
		},
		{
			name: "plain document with no styling at all",
			src:  "= Title\n\nJust prose and a diagram.\n",
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := templateNudge(tc.src) != ""
			if got != tc.want {
				t.Errorf("templateNudge fired=%v, want %v", got, tc.want)
			}
		})
	}
}

// Same honesty rule the instructions follow: never point at a package
// that is not installed, or the caller spends a compile finding out.
func TestTemplateNudge_SilentWhenTemplatesAbsent(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if got := templateNudge("#set page(margin: 2cm)\n= Title\n"); got != "" {
		t.Errorf("nudge advertised templates that are not installed: %s", got)
	}
}

// The nudge has to name the import line, or it is just nagging.
func TestTemplateNudge_IsActionable(t *testing.T) {
	installTemplates(t)
	got := templateNudge("#set page(margin: 2cm)\n= Title\n")
	for _, want := range []string{"@house/templates:0.1.0", "report", "adr"} {
		if !strings.Contains(got, want) {
			t.Errorf("nudge missing %q:\n%s", want, got)
		}
	}
}
