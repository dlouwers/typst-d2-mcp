package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/dlouwers/typst-d2-mcp/internal/authdb"
	"github.com/dlouwers/typst-d2-mcp/internal/identity"
	"github.com/dlouwers/typst-d2-mcp/internal/workspace"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Publishing a template — rung 5 of #63.
//
// A published template styles other people's documents, so two rules do
// most of the work here.
//
// It must compile before it is accepted. A template that fails to
// compile does not fail for whoever published it; it fails for everyone
// who imports it afterwards, at a time and place unrelated to the cause.
// Checking at publish time is the difference between one person seeing a
// clear error and everybody seeing a confusing one.
//
// And a version, once published, is immutable. That is not a policy so
// much as the consequence of documents pinning what they import: if
// 1.0.0 could be replaced, a bad publish would retroactively break
// documents that were compiling yesterday. A new version cannot.
//
// Who may publish is settled by namespace ownership, which for a
// personal namespace is true by construction — so this needs no
// permission model beyond the role that already exists.

// maxPackageBytes bounds one published package. Templates are read on
// every compile that imports them and live on the server's volume, so
// this is deliberately far below what a workspace may hold.
const maxPackageBytes = 4 << 20

var (
	packageNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	// typst requires a three-part version and rejects anything else, so
	// reject it here rather than at every later import.
	versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
)

func handlePublishTemplate(factory workspace.Factory, store *authdb.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		source, err := request.RequireString("source")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		namespace, err := request.RequireString("namespace")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		version, err := request.RequireString("version")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		pkgName := request.GetString("name", "templates")

		if !packageNamePattern.MatchString(pkgName) {
			return mcp.NewToolResultError(
				"name must be lowercase letters, digits and hyphens (e.g. \"templates\")"), nil
		}
		if !versionPattern.MatchString(version) {
			return mcp.NewToolResultError(fmt.Sprintf(
				"version %q is not major.minor.patch (e.g. \"1.0.0\") — typst rejects anything else",
				version)), nil
		}

		id, _ := identity.FromContext(ctx)
		if store == nil || id.IsAnonymous() {
			return mcp.NewToolResultError(
				"publishing needs a signed-in caller and a server that tracks namespaces"), nil
		}

		// You may publish only to a namespace you own. Everyone owns
		// their own, so this is not a barrier to a first template — it
		// is a barrier to publishing into someone else's.
		nsID, err := namespaceForPublish(ctx, store, id, namespace)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		resolver, err := resolverFor(ctx, factory)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("workspace setup", err), nil
		}
		// Resolve rather than MustExist: a package is a directory, and
		// MustExist requires a regular file. Resolve still rejects
		// traversal, which is the part that matters.
		srcDir, err := resolver.Resolve(source)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if info, statErr := os.Stat(srcDir); statErr != nil || !info.IsDir() {
			return mcp.NewToolResultError(fmt.Sprintf(
				"source %q must be a directory holding typst.toml and the entrypoint", source)), nil
		}

		files, total, err := collectPackage(srcDir)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("read package", err), nil
		}
		if total > maxPackageBytes {
			return mcp.NewToolResultError(fmt.Sprintf(
				"package is %s; the limit is %s", humanBytes(total), humanBytes(maxPackageBytes))), nil
		}
		if !containsFile(files, "typst.toml") {
			return mcp.NewToolResultError(
				"no typst.toml in " + source + " — a template is a typst package and needs one"), nil
		}

		// Immutable versions. Refuse before doing any work, and say what
		// to do instead.
		dest := filepath.Join(typstDataDir(), "typst", "packages", nsID, pkgName, version)
		if _, statErr := os.Stat(dest); statErr == nil {
			return mcp.NewToolResultError(fmt.Sprintf(
				"@%s/%s:%s already exists. Published versions are immutable, because documents "+
					"pin what they import — publish a new version instead",
				namespace, pkgName, version)), nil
		}

		if err := compileCheck(ctx, store, id, resolver, srcDir, files,
			namespace, nsID, pkgName, version); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf(
				"the template did not compile, so it was not published:\n\n%s", err.Error())), nil
		}

		if err := installPackage(srcDir, files, dest); err != nil {
			return mcp.NewToolResultErrorFromErr("install package", err), nil
		}

		msg := fmt.Sprintf("Published @%s/%s:%s (%s, %d file(s)).\n\nImport it with:\n  #import \"@%s/%s:%s\": …\n",
			namespace, pkgName, version, humanBytes(total), len(files), namespace, pkgName, version)
		msg += "\nThis version is now immutable. Publish a new version to change it; " +
			"documents importing this one keep rendering the same way."
		return mcp.NewToolResultText(msg), nil
	}
}

// namespaceForPublish resolves a namespace name the caller may use and
// confirms they own it.
func namespaceForPublish(ctx context.Context, store *authdb.Store, id identity.Identity, name string) (string, error) {
	if name == builtinNamespace {
		return "", fmt.Errorf("@%s is the server's built-in namespace and cannot be published to", name)
	}
	allowed, err := allowedNamespaces(ctx, store, id)
	if err != nil {
		return "", fmt.Errorf("resolve namespaces: %w", err)
	}
	nsID, ok := allowed[name]
	if !ok {
		// Name what they CAN publish to. Pointing at list_templates was
		// worse than useless while it listed only packages: a caller
		// with an empty namespace saw nothing of theirs and concluded
		// they had none (#108).
		return "", fmt.Errorf("you cannot see a namespace called @%s.%s",
			name, ownedNamespacesHint(ctx, store, id))
	}
	role, err := store.RoleFor(ctx, nsID, id.UserID)
	if err != nil {
		return "", fmt.Errorf("check ownership: %w", err)
	}
	if role != authdb.RoleOwner {
		return "", fmt.Errorf("you are a member of @%s but not an owner, so you cannot publish to it.%s",
			name, ownedNamespacesHint(ctx, store, id))
	}
	return nsID, nil
}

// ownedNamespacesHint names the namespaces the caller may publish to,
// so a refusal carries its own remedy.
func ownedNamespacesHint(ctx context.Context, store *authdb.Store, id identity.Identity) string {
	allowed, err := allowedNamespaces(ctx, store, id)
	if err != nil {
		return ""
	}
	var owned []string
	for _, name := range sortedNames(allowed) {
		if name != builtinNamespace && ownsNamespace(ctx, store, id, allowed[name]) {
			owned = append(owned, "@"+name)
		}
	}
	if len(owned) == 0 {
		return " You own no namespace to publish to."
	}
	return " You can publish to: " + strings.Join(owned, ", ") + "."
}

// collectPackage lists the package's files, relative to its root.
func collectPackage(dir string) ([]string, int64, error) {
	var files []string
	var total int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Skip anything the server staged: a compile in the same
		// directory would otherwise get published along with it.
		if strings.HasPrefix(d.Name(), workspace.StagePrefix) {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() {
			return nil // symlinks and friends are not part of a package
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		files = append(files, filepath.ToSlash(rel))
		total += info.Size()
		return nil
	})
	sort.Strings(files)
	return files, total, err
}

func containsFile(files []string, want string) bool {
	for _, f := range files {
		if f == want {
			return true
		}
	}
	return false
}

// compileCheck stages the candidate into a throwaway package root and
// compiles a document against it. A failure here is returned as an
// error and the publish is refused; softer findings come back as
// warnings the caller can read but that do not block.
//
// The check runs in the same shape as a real compile — the package
// reached only through XDG_DATA_HOME — so a template that resolves here
// resolves for everyone importing it later.
func compileCheck(
	ctx context.Context,
	store *authdb.Store,
	id identity.Identity,
	resolver workspace.Resolver,
	srcDir string,
	files []string,
	nsName, nsID, pkgName, version string,
) error {
	stage, err := os.MkdirTemp("", "typst-d2-publish-*")
	if err != nil {
		return fmt.Errorf("stage package: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	// The check compiles against everything this caller could import,
	// not against the candidate alone.
	//
	// Staging the candidate by itself rejected templates that build on
	// another one — including the house style, which the instructions
	// tell people to prefer — with "package not found". A gate that
	// refuses correct work is worse than no gate: the caller is told
	// their template is broken and the obvious remedy does not help
	// (#115). Reusing the same view a real compile gets also removes a
	// discrepancy nobody had declared, between what the check sees and
	// what the document will see.
	allowed, err := allowedNamespaces(ctx, store, id)
	if err != nil {
		return fmt.Errorf("resolve namespaces for the check: %w", err)
	}
	view, cleanupView, err := packageView(typstDataDir(), allowed, workspaceFontPath(resolver))
	if err != nil {
		return fmt.Errorf("stage package: %w", err)
	}
	defer cleanupView()

	// The candidate has to be a real directory under its own namespace
	// name, replacing the symlink to the published store — otherwise
	// installing it would write into the live store before it has
	// passed.
	pkgsDir := filepath.Join(view, "typst", "packages")
	nsDir := filepath.Join(pkgsDir, nsName)
	published := filepath.Join(typstDataDir(), "typst", "packages", nsID)
	_ = os.Remove(nsDir) // drop the symlink, if the namespace has one
	if err := os.MkdirAll(nsDir, 0o755); err != nil {
		return fmt.Errorf("stage package: %w", err)
	}
	// Keep the namespace's already-published packages reachable, so a
	// new version can build on an older one.
	if entries, readErr := os.ReadDir(published); readErr == nil {
		for _, e := range entries {
			if e.Name() == pkgName {
				continue // the candidate supersedes it for this check
			}
			_ = os.Symlink(filepath.Join(published, e.Name()), filepath.Join(nsDir, e.Name()))
		}
	}
	if err := installPackage(srcDir, files, filepath.Join(nsDir, pkgName, version)); err != nil {
		return fmt.Errorf("stage package: %w", err)
	}

	work := filepath.Join(stage, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return fmt.Errorf("stage package: %w", err)
	}

	importPath := fmt.Sprintf("@%s/%s:%s", nsName, pkgName, version)

	// The gate: the package imports and a document using it compiles.
	// This is what catches the failures that actually reach users — a
	// syntax error, a missing asset, a typst.toml that does not match
	// the directory it sits in.
	doc := fmt.Sprintf("#import %q: *\n= Compile check\nBody text.\n", importPath)
	if out, err := runTypst(ctx, work, view, "check.typ", doc); err != nil {
		return errors.New(out)
	}

	// Importing is not enough on its own. typst evaluates lazily, so a
	// function that references a file the package does not ship — a
	// logo the author forgot to include — imports cleanly and only
	// fails when somebody calls it. Which is to say: it fails for the
	// people who import the template, not for the person publishing it.
	// That is precisely the failure this check exists to prevent, so
	// document templates are also APPLIED, the way a caller writes
	// `#show: report.with(...)` with the arguments left out.
	//
	// Only document templates, though — a package may reasonably export
	// helpers that are not meant to be applied to a document, and
	// refusing to publish those would block legitimate work. See
	// isDocumentTemplate for how the two are told apart.
	for _, name := range documentTemplateExports(filepath.Join(srcDir, "lib.typ")) {
		showDoc := fmt.Sprintf("#import %q: %s\n#show: %s.with()\n= Heading\nBody.\n",
			importPath, name, name)
		if out, err := runTypst(ctx, work, view, "show-"+name+".typ", showDoc); err != nil {
			return fmt.Errorf(
				"%s is a document template but fails when applied to a document with its "+
					"default arguments:\n\n%s", name, out)
		}
	}
	return nil
}

// documentTemplateExports returns the exports that are document
// templates — the ones a caller applies with `#show: name.with(...)`.
//
// The rule is structural: a document template's final parameter is
// positional and named `body`, because that is the argument `#show:`
// supplies. It is a convention, but a load-bearing one — the house
// templates follow it and so does every template in the typst
// ecosystem, and the alternative was reading typst's error text to
// guess whether a failure meant "broken" or "not that kind of thing".
// That guess is not decidable: `swatch(colour)` and `report(.., body)`
// have the same shape, and a helper misapplied as a show rule fails
// with a type error indistinguishable from a real defect.
//
// The cost of the rule is that a template naming its body something
// else is not applied, so it is checked less thoroughly. The benefit is
// that no legitimate package is ever refused for exporting a helper.
func documentTemplateExports(libPath string) []string {
	content, err := os.ReadFile(libPath)
	if err != nil {
		return nil
	}
	var out []string
	for _, decl := range letDeclarations(string(content)) {
		if isDocumentTemplate(decl.params) {
			out = append(out, decl.name)
		}
	}
	sort.Strings(out)
	return out
}

type letDecl struct {
	name   string
	params string
}

// letDeclarations finds top-level `#let name(params)` bindings. Parens
// are balanced across lines, because a template's parameter list
// usually spans several.
func letDeclarations(src string) []letDecl {
	var out []letDecl
	for i := 0; i < len(src); {
		idx := strings.Index(src[i:], "#let ")
		if idx < 0 {
			break
		}
		i += idx + len("#let ")
		j := i
		for j < len(src) && (isIdentByte(src[j])) {
			j++
		}
		name := src[i:j]
		if name == "" || strings.HasPrefix(name, "_") {
			i = j + 1
			continue
		}
		if j >= len(src) || src[j] != '(' {
			i = j + 1
			continue // a value binding, not a function
		}
		params, end, ok := balancedParens(src, j)
		if !ok {
			i = j + 1
			continue
		}
		out = append(out, letDecl{name: name, params: params})
		i = end
	}
	return out
}

func isIdentByte(c byte) bool {
	return c == '-' || c == '_' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// balancedParens returns the contents of the parenthesised group
// starting at open, and the index just past its close.
func balancedParens(src string, open int) (string, int, bool) {
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '"':
			for i++; i < len(src); i++ {
				if src[i] == '\\' {
					i++
					continue
				}
				if src[i] == '"' {
					break
				}
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return src[open+1 : i], i + 1, true
			}
		}
	}
	return "", 0, false
}

// isDocumentTemplate reports whether a parameter list ends in a
// positional `body`.
func isDocumentTemplate(params string) bool {
	parts := splitTopLevel(params)
	if len(parts) == 0 {
		return false
	}
	last := strings.TrimSpace(parts[len(parts)-1])
	return last == "body"
}

// splitTopLevel splits a parameter list on commas that are not nested
// inside parens, brackets or strings — a default value may contain any
// of those.
func splitTopLevel(s string) []string {
	var parts []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			for i++; i < len(s); i++ {
				if s[i] == '\\' {
					i++
					continue
				}
				if s[i] == '"' {
					break
				}
			}
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	if rest := strings.TrimSpace(s[start:]); rest != "" {
		parts = append(parts, s[start:])
	}
	return parts
}

// runTypst compiles one document against a staged package root.
func runTypst(ctx context.Context, workDir, dataHome, file, content string) (string, error) {
	in := filepath.Join(workDir, file)
	if err := os.WriteFile(in, []byte(content), 0o600); err != nil {
		return "", err
	}
	out := strings.TrimSuffix(in, ".typ") + ".pdf"
	// The check must see fonts the way a real compile does: a template
	// may ship the typeface it is designed in, and validating it
	// without that would reject it for a font it actually carries.
	// Same discrepancy as #115, one axis over.
	cmd := exec.CommandContext(ctx, "typst", "compile",
		"--font-path", dataHome, in, out)
	cmd.Env = compileEnv(dataHome)
	combined, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(combined)), err
}

// installPackage copies a package's files into dest.
func installPackage(srcDir string, files []string, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	for _, rel := range files {
		if err := copyFile(filepath.Join(srcDir, filepath.FromSlash(rel)),
			filepath.Join(dest, filepath.FromSlash(rel))); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
