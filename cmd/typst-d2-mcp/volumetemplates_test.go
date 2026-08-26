package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The operator path documented in templates/README.md: because seeding
// never overwrites, an edit made on the data volume survives a restart
// and an image upgrade, and is what typst actually compiles against.
// The README is the only place this is written down, so pin it.
func TestVolumeTemplates_OperatorEditSurvivesSeeding(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	pkgDir := filepath.Join(data, "typst", "packages",
		templateNamespace, templateName, templateVersion)

	// First boot seeds the bundled tree.
	seedBundledTemplates()
	libPath := filepath.Join(pkgDir, "lib.typ")
	seeded, err := os.ReadFile(libPath)
	if err != nil {
		t.Fatalf("seed did not produce lib.typ: %v", err)
	}

	// An operator edits it in place, adding a document type.
	edited := string(seeded) + "\n#let memo(title: \"Untitled\", body) = {\n" +
		"  show: _page-setup\n  heading(title)\n  body\n}\n"
	if err := os.WriteFile(libPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	// A restart (or an image upgrade) re-runs the seed. The edit stands.
	seedBundledTemplates()
	after, err := os.ReadFile(libPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != edited {
		t.Fatal("re-seeding overwrote the operator's edit")
	}
	if !strings.Contains(string(after), "#let memo(") {
		t.Error("the added document type did not survive")
	}
}

// And the edited template is what typst resolves — an operator-added
// type is importable exactly like a bundled one.
func TestVolumeTemplates_OperatorAddedTypeCompiles(t *testing.T) {
	if _, err := exec.LookPath("typst"); err != nil {
		t.Skip("typst not installed")
	}

	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	seedBundledTemplates()

	libPath := filepath.Join(data, "typst", "packages",
		templateNamespace, templateName, templateVersion, "lib.typ")
	lib, err := os.ReadFile(libPath)
	if err != nil {
		t.Fatal(err)
	}
	added := string(lib) + "\n#let memo(title: \"Untitled\", body) = {\n" +
		"  show: _page-setup\n  heading(title)\n  body\n}\n"
	if err := os.WriteFile(libPath, []byte(added), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	in := filepath.Join(dir, "doc.typ")
	out := filepath.Join(dir, "doc.pdf")
	src := "#import \"@" + templateNamespace + "/" + templateName + ":" + templateVersion + "\": memo\n" +
		"#show: memo.with(title: \"A memo\")\nBody text.\n"
	if err := os.WriteFile(in, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("typst", "compile", in, out)
	cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+data)
	if outBytes, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("operator-added type did not compile: %v\n%s", err, outBytes)
	}
	if info, err := os.Stat(out); err != nil || info.Size() == 0 {
		t.Errorf("no PDF produced: %v", err)
	}
}
