package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

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

// builtinNamespace is readable by every caller. It is the house style —
// the default the templates exist to provide — so gating it on
// membership would both break every document that already imports it and
// leave a new user with no templates at all.
const builtinNamespace = templateNamespace

// allowedNamespaces returns the package namespaces a caller may import,
// sorted and free of duplicates. Without a store (stdio, or an
// unauthenticated deployment) there are no organisations to resolve and
// the built-in is the whole answer.
func allowedNamespaces(ctx context.Context, store *authdb.Store, id identity.Identity) ([]string, error) {
	allowed := map[string]bool{builtinNamespace: true}

	if store != nil && !id.IsAnonymous() {
		orgs, err := store.OrgsForUser(ctx, id.UserID)
		if err != nil {
			// Fail closed. Falling back to "everything" on a database
			// error would turn a transient fault into a cross-tenant
			// read, which is the one outcome this function exists to
			// prevent.
			return nil, fmt.Errorf("resolve organisations: %w", err)
		}
		for _, slug := range orgs {
			allowed[slug] = true
		}
	}

	out := make([]string, 0, len(allowed))
	for slug := range allowed {
		out = append(out, slug)
	}
	sort.Strings(out)
	return out, nil
}

// packageView builds a temporary XDG_DATA_HOME exposing only the given
// namespaces from the real package store, and returns it with a cleanup
// func. The view holds symlinks, so it costs no disk and nothing is
// copied.
//
// A namespace with no directory in the store is skipped rather than
// failing: an organisation exists in the schema before anyone publishes
// a template to it (rung 5), and that is not an error.
func packageView(dataDir string, allowed []string) (string, func(), error) {
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

	realPkgs := filepath.Join(dataDir, "typst", "packages")
	for _, slug := range allowed {
		src := filepath.Join(realPkgs, slug)
		if _, statErr := os.Stat(src); statErr != nil {
			continue // namespace exists, nothing published to it yet
		}
		if linkErr := os.Symlink(src, filepath.Join(pkgDir, slug)); linkErr != nil {
			cleanup()
			return "", nil, fmt.Errorf("link namespace %s: %w", slug, linkErr)
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
	return filepath.Join(view, "typst", "packages")
}
