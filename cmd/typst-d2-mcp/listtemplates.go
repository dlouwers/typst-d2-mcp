package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

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
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Import    string `json:"import"`
	// Exports describe how to call what the package offers, not only
	// that it offers it — see exports.go for why the names alone were
	// not enough.
	Exports []exportEntry `json:"exports,omitempty"`
	Builtin bool          `json:"builtin,omitempty"`
}

// namespaceEntry is a namespace the caller can reach, whether or not
// anything has been published to it.
//
// Listing these is not a nicety. Templates alone answered "what can I
// import", and left "where can I publish" unanswerable: a caller's own
// namespace holds nothing until they publish, so it produced no
// template rows and was invisible. Agents guessed names, were refused,
// and concluded they had no namespace at all — while owning one (#108).
type namespaceEntry struct {
	Name      string `json:"name"`
	Writable  bool   `json:"writable"`
	Builtin   bool   `json:"builtin,omitempty"`
	Templates int    `json:"templates"`
}

type listTemplatesResult struct {
	Namespaces []namespaceEntry `json:"namespaces"`
	Templates  []templateEntry  `json:"templates"`
	Note       string           `json:"note,omitempty"`
}

// ownsNamespace reports whether the caller may publish to a namespace.
func ownsNamespace(ctx context.Context, store *authdb.Store, id identity.Identity, nsID string) bool {
	if store == nil || id.IsAnonymous() {
		return false
	}
	role, err := store.RoleFor(ctx, nsID, id.UserID)
	if err != nil {
		return false // fail closed: never advertise a write you cannot do
	}
	return role == authdb.RoleOwner
}

func handleListTemplates(store *authdb.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, _ := identity.FromContext(ctx)
		allowed, err := allowedNamespaces(ctx, store, id)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("resolve template namespaces", err), nil
		}

		out := listTemplatesResult{
			Namespaces: []namespaceEntry{},
			Templates:  []templateEntry{},
		}
		var writable []string
		for _, name := range sortedNames(allowed) {
			// Listed under the NAME, which is what goes in the import;
			// read from the id, which is where the packages live.
			found := templatesInNamespace(typstDataDir(), name, allowed[name])
			out.Templates = append(out.Templates, found...)

			canPublish := name != builtinNamespace &&
				ownsNamespace(ctx, store, id, allowed[name])
			if canPublish {
				writable = append(writable, name)
			}
			out.Namespaces = append(out.Namespaces, namespaceEntry{
				Name:      name,
				Writable:  canPublish,
				Builtin:   name == builtinNamespace,
				Templates: len(found),
			})
		}
		switch {
		case len(writable) > 0 && len(out.Templates) == 0:
			out.Note = fmt.Sprintf(
				"No templates published yet. You can publish one to @%s with publish_template.",
				writable[0])
		case len(out.Templates) == 0:
			out.Note = "No templates are available to you, and you own no namespace to " +
				"publish one to. Style the document yourself, or ask an administrator."
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
				Exports: parseExports(filepath.Join(
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
