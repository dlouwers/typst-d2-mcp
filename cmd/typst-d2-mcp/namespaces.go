package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dlouwers/typst-d2-mcp/internal/authdb"
	"github.com/dlouwers/typst-d2-mcp/internal/identity"
)

// Rung 4 of #63: a compile resolves only the package namespaces its
// caller can see.
//
// typst resolves local packages from
// $XDG_DATA_HOME/typst/packages/<namespace>/..., and the server spawns a
// typst child per compile — so the isolation boundary is the child's
// XDG_DATA_HOME. Each compile gets a directory containing a symlink per
// permitted namespace and nothing else. A namespace the caller may not
// see is not hidden behind a check that could be forgotten; it is simply
// absent from the tree typst searches, and the import fails with
// "package not found", the same as a typo.
//
// This is the discipline workspace.ScopedFS already applies to files:
// construct the reachable set, rather than filter an unreachable one.
//
// Only $XDG_DATA_HOME is redirected. typst's @preview cache lives under
// $XDG_CACHE_HOME, so the `@preview/based` import that every
// preprocessed document carries is unaffected.

// builtinNamespace is the name every caller can import. It is the house
// style — the default the templates exist to provide — so gating it on
// membership would both break every document that already imports it and
// leave a new user with no templates at all. Its name and its namespace
// id are the same string, so the tree the image seeds needs no special
// case.
const builtinNamespace = authdb.BuiltinNamespaceID

// allowedNamespaces returns the namespace NAMES a caller may import,
// each mapped to the namespace id that holds the packages.
//
// The indirection is the point. Names are pointers: a namespace keeps
// its id for life, so renaming one — or giving a personal namespace a
// proper name when it becomes shared — rebinds a pointer instead of
// moving content, and the old name goes on resolving so documents that
// already import it never break. Several names mapping to one id is an
// alias, and costs nothing to support.
//
// Without a store (stdio, or an unauthenticated deployment) there are no
// memberships to resolve and the built-in is the whole answer.
func allowedNamespaces(ctx context.Context, store *authdb.Store, id identity.Identity) (map[string]string, error) {
	allowed := map[string]string{builtinNamespace: authdb.BuiltinNamespaceID}

	if store != nil && !id.IsAnonymous() {
		owned, err := store.NamespacesForUser(ctx, id.UserID)
		if err != nil {
			// Fail closed. Falling back to "everything" on a database
			// error would turn a transient fault into a cross-tenant
			// read, which is the one outcome this function exists to
			// prevent.
			return nil, fmt.Errorf("resolve namespaces: %w", err)
		}
		for name, nsID := range owned {
			allowed[name] = nsID
		}
	}
	return allowed, nil
}

// sortedNames is the deterministic ordering callers report in.
func sortedNames(allowed map[string]string) []string {
	out := make([]string, 0, len(allowed))
	for name := range allowed {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// packageView builds a temporary XDG_DATA_HOME exposing only the given
// namespaces from the real package store, and returns it with a cleanup
// func. The view holds symlinks, so it costs no disk and nothing is
// copied.
//
// A namespace with no directory in the store is skipped rather than
// failing: an organisation exists in the schema before anyone publishes
// a template to it (rung 5), and that is not an error.
func packageView(dataDir string, allowed map[string]string, workspaceFonts string) (string, func(), error) {
	viewRoot, err := os.MkdirTemp("", "typst-d2-pkgview-*")
	if err != nil {
		return "", nil, fmt.Errorf("create package view: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(viewRoot) }

	pkgDir := filepath.Join(viewRoot, "typst", "packages")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("create package view: %w", err)
	}

	// The store is keyed by namespace id; the view is keyed by name.
	// That one hop is where a rename becomes free — and where two names
	// for the same namespace become two links to one directory.
	realPkgs := filepath.Join(dataDir, "typst", "packages")
	for _, name := range sortedNames(allowed) {
		src := filepath.Join(realPkgs, allowed[name])
		if _, statErr := os.Stat(src); statErr != nil {
			continue // namespace exists, nothing published to it yet
		}
		if linkErr := os.Symlink(src, filepath.Join(pkgDir, name)); linkErr != nil {
			cleanup()
			return "", nil, fmt.Errorf("link namespace %s: %w", name, linkErr)
		}
	}

	// The tenant's own fonts are linked in HERE rather than passed to
	// typst directly, and that is the whole fix for #107.
	//
	// typst splits --font-path on the OS path-list separator, which on
	// Unix is ":". A tenant workspace is rooted at <root>/gh:<id>, so
	// its font directory ALWAYS contains a colon, and the path was
	// always silently split into two nonexistent directories. typst
	// then substituted and exited 0 — a PDF that looks fine and is
	// wrong, which is the exact failure #92 was filed about.
	//
	// The view root is a mkdtemp path with no colon in it, so a link
	// from here is safe to name on a command line however the tenant
	// directory is spelled.
	if workspaceFonts != "" {
		if linkErr := os.Symlink(workspaceFonts, filepath.Join(viewRoot, FontsDir)); linkErr != nil {
			cleanup()
			return "", nil, fmt.Errorf("link workspace fonts: %w", linkErr)
		}
	}
	return viewRoot, cleanup, nil
}

// compileEnv returns the environment for a typst child, with
// XDG_DATA_HOME pointed at view. Everything else is inherited, so the
// @preview cache, PATH and locale are untouched.
func compileEnv(view string) []string {
	env := os.Environ()
	out := env[:0:0]
	for _, kv := range env {
		if len(kv) >= len("XDG_DATA_HOME=") && kv[:len("XDG_DATA_HOME=")] == "XDG_DATA_HOME=" {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "XDG_DATA_HOME="+view)
}

// packageFontPath returns the font search path for a package view.
//
// A template is designed in a typeface, and a template that cannot use
// its own is the same half-measure as one that cannot use its own logo:
// typst substitutes silently, so the document renders and is wrong.
// Fonts a template ships therefore travel with it.
//
// typst searches a --font-path recursively, so pointing at the packages
// root covers every namespace in the view — and only those, since the
// view is already the caller's permitted set. The font boundary is the
// template boundary, for free.
func packageFontPath(view string) string {
	if view == "" {
		return ""
	}
	// The view root, not the packages subdirectory: typst searches a
	// font path recursively, so one colon-free path covers both the
	// templates' own fonts and the tenant's linked fonts/ directory.
	return view
}

// safeFontPath reports whether p can survive being named on a typst
// command line. A path containing the OS list separator is split into
// pieces that do not exist, and typst says nothing about it — so this
// is checked rather than assumed. See #107.
func safeFontPath(p string) bool {
	return !strings.Contains(p, string(os.PathListSeparator))
}
