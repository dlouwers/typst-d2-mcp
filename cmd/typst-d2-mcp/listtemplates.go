package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dlouwers/typst-d2-mcp/internal/authdb"
	"github.com/dlouwers/typst-d2-mcp/internal/identity"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// list_templates exists because of rung 4 of #63.
//
// While there was one namespace, the server could name it in its
// instructions and be done. Now that a compile resolves only what its
// caller may see, the answer is per-caller and no fixed string can
// express it — narrated instructions would be wrong for everyone with an
// organisation and misleading for everyone without one. Worse, the
// instructions are truncated by clients (#90), so the one place the
// import line appeared was also the place most likely to be cut.
//
// So it is a tool: called exactly when a caller is choosing a template,
// never truncated, and reporting what THIS caller can actually resolve —
// which is the same set the compile will build its package view from,
// derived from the same function, so the two cannot drift.

// templateEntry is one importable package.
type templateEntry struct {
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
	Version   string   `json:"version"`
	Import    string   `json:"import"`
	Exports   []string `json:"exports,omitempty"`
	Builtin   bool     `json:"builtin,omitempty"`
}

type listTemplatesResult struct {
	Templates []templateEntry `json:"templates"`
	Note      string          `json:"note,omitempty"`
}

func handleListTemplates(store *authdb.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, _ := identity.FromContext(ctx)
		allowed, err := allowedNamespaces(ctx, store, id)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("resolve template namespaces", err), nil
		}

		out := listTemplatesResult{Templates: []templateEntry{}}
		for _, name := range sortedNames(allowed) {
			// Listed under the NAME, which is what goes in the import;
			// read from the id, which is where the packages live.
			out.Templates = append(out.Templates,
				templatesInNamespace(typstDataDir(), name, allowed[name])...)
		}
		if len(out.Templates) == 0 {
			out.Note = "No templates are installed for you. Style the document yourself, " +
				"or ask an administrator to publish one."
		}

		payload, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("encode templates", err), nil
		}
		return mcp.NewToolResultText(string(payload)), nil
	}
}

// templatesInNamespace lists the packages published under one owner.
// A namespace with nothing in it yields nothing rather than an error:
// an organisation exists in the schema before anyone publishes to it.
func templatesInNamespace(dataDir, name, namespaceID string) []templateEntry {
	nsRoot := filepath.Join(dataDir, "typst", "packages", namespaceID)
	pkgs, err := os.ReadDir(nsRoot)
	if err != nil {
		return nil
	}
	var out []templateEntry
	for _, pkg := range pkgs {
		if !pkg.IsDir() {
			continue
		}
		versions, err := os.ReadDir(filepath.Join(nsRoot, pkg.Name()))
		if err != nil {
			continue
		}
		for _, v := range versions {
			if !v.IsDir() {
				continue
			}
			entry := templateEntry{
				Namespace: name,
				Name:      pkg.Name(),
				Version:   v.Name(),
				Import:    fmt.Sprintf("@%s/%s:%s", name, pkg.Name(), v.Name()),
				Builtin:   name == builtinNamespace,
				Exports: exportedNames(filepath.Join(
					nsRoot, pkg.Name(), v.Name(), "lib.typ")),
			}
			out = append(out, entry)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Version < out[j].Version
	})
	return out
}

// exportedNames reports the top-level `#let name(` bindings a template
// offers, so a caller learns what to import rather than only where from.
//
// This reads the entrypoint rather than asking typst, which has no
// "describe a package" mode. It is deliberately shallow: a name starting
// with `_` is the package's own business (the house template uses that
// convention for its internals), and anything this misses costs a caller
// nothing — the import line, which is the thing they cannot guess, is
// always right.
func exportedNames(libPath string) []string {
	content, err := os.ReadFile(libPath)
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(string(content), "\n") {
		rest, ok := strings.CutPrefix(line, "#let ")
		if !ok {
			continue
		}
		end := strings.IndexAny(rest, "( =")
		if end <= 0 {
			continue
		}
		name := rest[:end]
		if strings.HasPrefix(name, "_") || name == "" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
