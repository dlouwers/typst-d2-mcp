package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dlouwers/typst-d2-mcp/internal/authdb"
	"github.com/dlouwers/typst-d2-mcp/internal/identity"
	"github.com/dlouwers/typst-d2-mcp/internal/workspace"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Fonts arrive from four places and none of them was discoverable
// together: typst's embedded faces, the collection this image ships, a
// tenant's own fonts/ directory, and the fonts inside a template they
// can import. A caller choosing a typeface had to reason across four
// mechanisms, three of them invisible — so two research agents went and
// fetched fonts from the internet rather than ask what was already
// there.
//
// Search rather than a list, for the same reason as files: nobody wants
// to read family names, they want to know whether a humanist sans is
// available or whether a particular family is here.
//
// The answer is derived from the SAME paths a compile resolves, and
// attributed by asking typst about each source separately. That is not
// tidiness: this area's recurring failure is the server saying one
// thing and typst doing another (#107), so agreement is built in rather
// than maintained.

// fontsDirName is where the image keeps the collection it ships.
const bundledFontsPath = "/usr/local/share/typst-d2/fonts"

// fontFace is one family, and where it comes from.
type fontFace struct {
	Family  string `json:"family"`
	Source  string `json:"source"`
	Origin  string `json:"origin,omitempty"`
	License string `json:"license,omitempty"`
	Note    string `json:"note,omitempty"`
}

// manifestEntry describes a bundled family. Recorded rather than
// inferred, so "may we redistribute this?" is answerable later without
// archaeology.
type manifestEntry struct {
	Family  string `json:"family"`
	License string `json:"license"`
	Origin  string `json:"origin"`
	Note    string `json:"note,omitempty"`
}

func loadFontManifest() map[string]manifestEntry {
	out := map[string]manifestEntry{}
	raw, err := os.ReadFile(filepath.Join(bundledFontsPath, "manifest.json"))
	if err != nil {
		return out
	}
	var entries []manifestEntry
	if json.Unmarshal(raw, &entries) != nil {
		return out
	}
	for _, e := range entries {
		out[strings.ToLower(e.Family)] = e
	}
	return out
}

func handleSearchFonts(factory workspace.Factory, store *authdb.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query := strings.ToLower(strings.TrimSpace(request.GetString("query", "")))
		resolver, err := resolverFor(ctx, factory)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("workspace setup", err), nil
		}
		id, _ := identity.FromContext(ctx)

		faces := collectFonts(ctx, store, id, resolver)

		var hits []fontFace
		for _, f := range faces {
			if query != "" && !strings.Contains(strings.ToLower(f.Family), query) {
				continue
			}
			hits = append(hits, f)
		}
		sort.Slice(hits, func(i, j int) bool { return hits[i].Family < hits[j].Family })

		out := map[string]any{"fonts": hits, "count": len(hits)}
		if len(hits) == 0 {
			out["note"] = "Nothing matched. Omit query to see everything available to you, " +
				"or push a .ttf/.otf to your workspace fonts/ directory to use your own."
		} else {
			out["note"] = "Every family listed here resolves in a compile by you. " +
				"A family NOT listed will be silently substituted by typst, producing a " +
				"document that looks fine and is wrong."
		}
		payload, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("encode fonts", err), nil
		}
		return mcp.NewToolResultText(string(payload)), nil
	}
}

// collectFonts asks typst what each source provides, so the answer
// cannot drift from what a compile resolves.
func collectFonts(ctx context.Context, store *authdb.Store, id identity.Identity, r workspace.Resolver) []fontFace {
	manifest := loadFontManifest()
	seen := map[string]bool{}
	var out []fontFace

	add := func(family, source, origin string) {
		key := strings.ToLower(family)
		if family == "" || seen[key] {
			return
		}
		seen[key] = true
		f := fontFace{Family: family, Source: source, Origin: origin}
		if m, ok := manifest[key]; ok {
			f.License, f.Origin, f.Note = m.License, m.Origin, m.Note
		}
		out = append(out, f)
	}

	// A tenant's own fonts win the attribution: if they shipped a family
	// that shadows a bundled one, theirs is the one in force.
	if ws := workspaceFontPath(r); ws != "" {
		for _, fam := range familiesUnder(ws) {
			add(fam, "workspace", "your fonts/ directory")
		}
	}
	// Then anything inside a template they can import.
	if allowed, err := allowedNamespaces(ctx, store, id); err == nil {
		for _, name := range sortedNames(allowed) {
			nsRoot := filepath.Join(typstDataDir(), "typst", "packages", allowed[name])
			for _, fam := range familiesUnder(nsRoot) {
				add(fam, "template", "@"+name)
			}
		}
	}
	for _, fam := range familiesUnder(bundledFontsPath) {
		add(fam, "bundled", "shipped with this server")
	}
	for _, fam := range embeddedFamilies() {
		add(fam, "built-in", "built into typst")
	}
	return out
}
