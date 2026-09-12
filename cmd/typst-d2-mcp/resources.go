package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dlouwers/typst-d2-mcp/internal/authdb"
	"github.com/dlouwers/typst-d2-mcp/internal/identity"
	"github.com/dlouwers/typst-d2-mcp/internal/preprocessor"
	"github.com/dlouwers/typst-d2-mcp/internal/workspace"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/yosida95/uritemplate/v3"
)

// Resources are how artefacts leave this server; tools are how source
// does.
//
// The division is by purpose, not by size. A client reading a resource
// saves the bytes to a file and hands back a path, so a PDF or a page
// image never enters the caller's context at all — which is strictly
// better than a tool returning base64. But re-reading your own .typ to
// change two lines is not fetching an artefact; you want the text in
// front of you, which is what get_file is for.
//
// The catch found in testing: only PDFs were exposed, as a resource
// TEMPLATE, and templates are not what resources/list returns. An agent
// asking what it could read was told "No resources found" while holding
// a working typst-d2:// URI. Hence the index below — one concrete
// resource whose job is to answer that question honestly.

const (
	sourceURIPrefix = "typst-d2://source/"
	pageURIPrefix   = "typst-d2://page/"
	indexURI        = "typst-d2://index"
	previewDir      = ".previews"
)

func registerResources(s *server.MCPServer, factory workspace.Factory, store *authdb.Store) {
	s.AddResourceTemplate(mcp.ResourceTemplate{
		URITemplate: templateFor(pdfURIPrefix + "{+path}"),
		Name:        "pdf",
		Description: "Compiled Typst PDF produced by compile_typst_with_d2.",
		MIMEType:    "application/pdf",
	}, handleReadPDF(factory))

	s.AddResourceTemplate(mcp.ResourceTemplate{
		URITemplate: templateFor(sourceURIPrefix + "{+path}"),
		Name:        "source",
		Description: "A text file in the workspace, read as text.",
		MIMEType:    "text/plain",
	}, handleReadSource(factory))

	s.AddResourceTemplate(mcp.ResourceTemplate{
		URITemplate: templateFor(pageURIPrefix + "{+path}"),
		Name:        "page",
		Description: "One rendered page of a document, as PNG. " +
			"Address as typst-d2://page/<document.typ>/<page number>. " +
			"Pages are rendered on first read, not at compile time.",
		MIMEType: "image/png",
	}, handleReadPage(factory, store))

	// A concrete resource, so resources/list has something to return.
	// Without it a caller asking what is readable is told nothing is,
	// which is the same discoverability failure as a namespace that
	// exists but is never named.
	s.AddResource(mcp.Resource{
		URI:         indexURI,
		Name:        "workspace",
		Description: "What this caller can read: every document, its pages, and its compiled PDF.",
		MIMEType:    "application/json",
	}, handleReadIndex(factory))
}

// handleReadSource returns a workspace text file as text.
func handleReadSource(factory workspace.Factory) server.ResourceTemplateHandlerFunc {
	return func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		rel, err := uriPath(req.Params.URI, sourceURIPrefix)
		if err != nil {
			return nil, err
		}
		resolver, err := resolverFor(ctx, factory)
		if err != nil {
			return nil, fmt.Errorf("workspace setup: %w", err)
		}
		resolved, err := workspace.MustExist(resolver, rel)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", rel, err)
		}
		return []mcp.ResourceContents{mcp.TextResourceContents{
			URI: req.Params.URI, MIMEType: "text/plain", Text: string(data),
		}}, nil
	}
}

// handleReadPage renders one page of a document and returns it as PNG.
//
// Rendering is lazy: nothing is produced at compile time, because
// agents compile repeatedly while iterating and mostly never look. The
// first read of any page renders the whole document once and caches the
// pages in the workspace, so the second read is free. Cached pages
// older than the source are re-rendered, so a stale preview cannot be
// served as if it were current.
func handleReadPage(factory workspace.Factory, store *authdb.Store) server.ResourceTemplateHandlerFunc {
	return func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		spec, err := uriPath(req.Params.URI, pageURIPrefix)
		if err != nil {
			return nil, err
		}
		slash := strings.LastIndex(spec, "/")
		if slash < 0 {
			return nil, fmt.Errorf("address a page as %s<document.typ>/<page number>", pageURIPrefix)
		}
		docRel, pageStr := spec[:slash], spec[slash+1:]
		page, convErr := strconv.Atoi(pageStr)
		if convErr != nil || page < 1 {
			return nil, fmt.Errorf("page number must be 1 or greater, got %q", pageStr)
		}
		resolver, err := resolverFor(ctx, factory)
		if err != nil {
			return nil, err
		}
		srcPath, err := workspace.MustExist(resolver, docRel)
		if err != nil {
			return nil, err
		}

		id, _ := identity.FromContext(ctx)
		pageFile, err := renderPages(ctx, store, id, resolver, docRel, srcPath, page)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(pageFile)
		if err != nil {
			// renderPages has already established the page exists, so
			// anything here is a real read failure and says so rather
			// than blaming the document's length.
			return nil, fmt.Errorf("read page %d of %s: %w", page, docRel, err)
		}
		return []mcp.ResourceContents{mcp.BlobResourceContents{
			URI: req.Params.URI, MIMEType: "image/png", Blob: base64.StdEncoding.EncodeToString(data),
		}}, nil
	}
}

// renderPages rasterises a document into the workspace preview
// directory if the cache is missing or stale, and returns the path of
// the requested page.
//
// The previews live in the workspace and count against the tenant's
// byte budget, which is deliberate: the caller asked for them, and
// hiding storage a caller caused is how quotas stop meaning anything.
// delete_file reclaims them.
func renderPages(ctx context.Context, store *authdb.Store, id identity.Identity,
	r workspace.Resolver, docRel, srcPath string, page int) (string, error) {
	outDir, err := r.Resolve(previewDirFor(docRel))
	if err != nil {
		return "", err
	}
	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return "", err
	}
	if pages := renderedPages(outDir); pagesAreFresh(pages, srcInfo.ModTime()) {
		return pageFrom(pages, page, docRel)
	}
	// Clear before rendering. A document that has SHRUNK would otherwise
	// keep a preview for a page it no longer has — charged to the
	// tenant's budget, and reachable by a reader who would then be
	// looking at content the document does not contain.
	if err := os.RemoveAll(outDir); err != nil {
		return "", fmt.Errorf("prepare previews: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("prepare previews: %w", err)
	}

	processed, err := preprocessor.Preprocess(ctx, r, docRel)
	if err != nil {
		return "", fmt.Errorf("preprocess %s: %w", docRel, err)
	}
	staged, err := os.CreateTemp(filepath.Dir(srcPath), workspace.StagePrefix+"*.typ")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(staged.Name()) }()
	if _, err := staged.WriteString(processed); err != nil {
		_ = staged.Close()
		return "", err
	}
	if err := staged.Close(); err != nil {
		return "", err
	}

	// Render through the caller's package view, exactly as a compile
	// does. Without it the child inherited the server's store, which is
	// keyed by namespace ID rather than name — so a document importing
	// @acme/templates failed with "package not found", and previews
	// worked only for @house, where the id happens to equal the name.
	// The documents most worth previewing were the ones that could not
	// be. Found by an agent whose org template would not render.
	allowed, err := allowedNamespaces(ctx, store, id)
	if err != nil {
		return "", fmt.Errorf("resolve namespaces: %w", err)
	}
	view, cleanupView, err := packageView(typstDataDir(), allowed, workspaceFontPath(r))
	if err != nil {
		return "", fmt.Errorf("prepare packages: %w", err)
	}
	defer cleanupView()

	// "{p}", not "{n}". Both spell the page number, but {n} is
	// undocumented and zero-pads it to the width of the page count, so
	// a document of ten pages or more wrote page-01.png while every
	// reader here looked for page-1.png. That was #145: a 20-page deck
	// rendered perfectly and no page of it could be read, while the
	// five-page documents everyone tested with worked. typst documents
	// {p}, {0p} and {t}; {p} is the unpadded one.
	args := typstArgs(r, staged.Name(), filepath.Join(outDir, "page-{p}.png"),
		packageFontPath(view))
	args = append([]string{args[0], "--format", "png", "--ppi", "96"}, args[1:]...)
	cmd := exec.CommandContext(ctx, "typst", args...)
	cmd.Env = compileEnv(view)
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		return "", fmt.Errorf("render pages of %s: %s", docRel, strings.TrimSpace(string(out)))
	}
	return pageFrom(renderedPages(outDir), page, docRel)
}

// renderedPages maps page number to file for the previews already in
// outDir.
//
// Read back rather than reconstructed. The name typst writes depends on
// the document's page count, and #145 was this server predicting that
// name and being wrong — so the prediction is gone. A padded preview
// left by an older build is still found, which is what makes the fix
// safe on a workspace that already has one.
func renderedPages(outDir string) map[int]string {
	entries, err := os.ReadDir(outDir)
	if err != nil {
		return nil
	}
	pages := map[int]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		digits := strings.TrimSuffix(strings.TrimPrefix(name, "page-"), ".png")
		if digits == name || digits == "" {
			continue
		}
		n, convErr := strconv.Atoi(digits)
		if convErr != nil || n < 1 {
			continue
		}
		pages[n] = filepath.Join(outDir, name)
	}
	return pages
}

// pagesAreFresh reports whether every preview postdates the source. A
// document is rendered in one typst run, so a single stale page means
// the whole set belongs to an older draft.
func pagesAreFresh(pages map[int]string, src time.Time) bool {
	if len(pages) == 0 {
		return false
	}
	for _, p := range pages {
		info, err := os.Stat(p)
		if err != nil || !info.ModTime().After(src) {
			return false
		}
	}
	return true
}

// pageFrom picks the requested page out of what was actually rendered,
// and otherwise says what the document has.
//
// "was not produced — does it have that many pages?" was a guess, and
// in #145 it was the wrong guess twice over: every page existed, and
// the question it asked the caller to consider was not the problem. A
// caller who cannot see the filesystem can do nothing with a guess.
func pageFrom(pages map[int]string, page int, docRel string) (string, error) {
	if p, ok := pages[page]; ok {
		return p, nil
	}
	if len(pages) == 0 {
		return "", fmt.Errorf("rendering %s produced no pages", docRel)
	}
	return "", fmt.Errorf("%s renders to %d page(s), so there is no page %d",
		docRel, len(pages), page)
}

// handleReadIndex answers "what can I read here" for this caller.
func handleReadIndex(factory workspace.Factory) server.ResourceHandlerFunc {
	return func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		resolver, err := resolverFor(ctx, factory)
		if err != nil {
			return nil, err
		}
		bounded, ok := resolver.(workspace.Bounded)
		if !ok {
			return nil, fmt.Errorf("no bounded workspace on this server")
		}
		root := bounded.WorkspaceRoot()

		type entry struct {
			Document string `json:"document"`
			Source   string `json:"source"`
			PDF      string `json:"pdf,omitempty"`
			Pages    string `json:"pages,omitempty"`
		}
		var out []entry
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(d.Name()), ".typ") {
				return nil //nolint:nilerr // an unreadable entry is not a listing failure
			}
			if strings.HasPrefix(d.Name(), workspace.StagePrefix) {
				return nil
			}
			rel, relErr := filepath.Rel(root, p)
			if relErr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			e := entry{
				Document: rel,
				Source:   sourceURIPrefix + rel,
				Pages:    pageURIPrefix + rel + "/1",
			}
			if pdf := strings.TrimSuffix(rel, filepath.Ext(rel)) + ".pdf"; fileExists(filepath.Join(root, pdf)) {
				e.PDF = pdfURIPrefix + pdf
			}
			out = append(out, e)
			return nil
		})
		sort.Slice(out, func(i, j int) bool { return out[i].Document < out[j].Document })

		payload, err := json.MarshalIndent(map[string]any{
			"documents": out,
			"note": "Read a PDF or a page rather than pulling it through a tool — the client " +
				"saves it to a file instead of filling your context. Pages render on first read.",
		}, "", "  ")
		if err != nil {
			return nil, err
		}
		return []mcp.ResourceContents{mcp.TextResourceContents{
			URI: indexURI, MIMEType: "application/json", Text: string(payload),
		}}, nil
	}
}

// previewDirFor is where a document's rendered pages live, relative to
// the workspace. One definition, because two would drift and the
// deletion path has to find exactly what the render path wrote.
func previewDirFor(docRel string) string {
	return filepath.Join(filepath.Dir(docRel), previewDir,
		strings.TrimSuffix(filepath.Base(docRel), filepath.Ext(docRel)))
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

// templateFor builds a URI template, panicking on a malformed one —
// these are constants, so a bad one is a programming error caught at
// startup rather than a runtime failure per request.
func templateFor(t string) *mcp.URITemplate {
	return &mcp.URITemplate{Template: uritemplate.MustNew(t)}
}

// uriPath extracts and unescapes the path portion of a typst-d2 URI.
func uriPath(uri, prefix string) (string, error) {
	if !strings.HasPrefix(uri, prefix) {
		return "", fmt.Errorf("not a %s URI: %s", strings.TrimSuffix(prefix, "/"), uri)
	}
	p, err := url.PathUnescape(strings.TrimPrefix(uri, prefix))
	if err != nil {
		return "", fmt.Errorf("decode URI path: %w", err)
	}
	return p, nil
}
