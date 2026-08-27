package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/dlouwers/typst-d2-mcp/internal/workspace"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// The rest of the workspace verbs. put_file was the only one, which
// meant a caller could write a document and then neither read it back,
// find it, nor remove it.
//
// The cost of that was not theoretical. An agent editing a 13KB document
// re-uploaded the whole thing for a two-line change, because the
// alternative was `find` on the server's filesystem — which worked only
// because the server happened to be local. Probe files it wrote while
// diagnosing were still sitting in the workspace at the end of the run,
// counting against the byte budget, with no way to reclaim them.
//
// Search rather than list, deliberately. A workspace accumulates
// documents, PDFs and now page previews; "show me everything" is the
// answer to a question nobody asks, and the same reasoning applies here
// as to fonts.

// maxGetFileBytes bounds what get_file will return inline. The cap is a
// backstop; the real division is artefact vs source — see artefactURI.
const maxGetFileBytes = 256 << 10

// artefactURI reports whether a path names something the server
// rendered rather than something a caller wrote, and where to read it.
//
// Rendered output belongs on the resource side whatever its size: the
// client streams it to a file, so it costs the caller no context at
// all. Refusing only when a file happens to be large would send a
// small PDF through the worse mechanism for no reason.
// Only output the SERVER produced counts. A .png the caller pushed is a
// logo or a figure — their file, and refusing it would be telling them
// their own asset is an artefact. Rendered pages are distinguished by
// living under the preview directory, not by extension.
func artefactURI(path string) (string, bool) {
	clean := filepath.ToSlash(path)
	if strings.EqualFold(filepath.Ext(clean), ".pdf") {
		return pdfURIPrefix + clean, true
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == previewDir {
			return pageURIPrefix + clean, true
		}
	}
	return "", false
}

func handleGetFile(factory workspace.Factory) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		path, err := request.RequireString("path")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		resolver, err := resolverFor(ctx, factory)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("workspace setup", err), nil
		}
		resolved, err := workspace.MustExist(resolver, path)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		info, err := os.Stat(resolved)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("read file", err), nil
		}
		// Artefacts go out as resources, not through here — and that is
		// a judgement about PURPOSE, not size. A client reading a
		// resource saves the bytes to a file and hands back a path, so
		// a PDF never enters the caller's context; base64 through a
		// tool result is strictly the worse mechanism for the same job.
		// Source you are about to edit is the opposite case: you want
		// the text in front of you, which is what this tool is for.
		if uri, isArtefact := artefactURI(path); isArtefact {
			msg := fmt.Sprintf(
				"%s is a rendered artefact, not source. Read it with resources/read on %s — "+
					"your client saves it to a file instead of filling your context.", path, uri)
			if strings.EqualFold(filepath.Ext(path), ".pdf") {
				msg += fmt.Sprintf(" To SEE a page rather than the file, read %s%s/<page number>.",
					pageURIPrefix, strings.TrimSuffix(path, filepath.Ext(path))+".typ")
			}
			return mcp.NewToolResultError(msg), nil
		}
		if info.Size() > maxGetFileBytes {
			return mcp.NewToolResultError(fmt.Sprintf(
				"%s is %s; get_file returns at most %s. Read it as a resource instead: %s%s",
				path, humanBytes(info.Size()), humanBytes(maxGetFileBytes),
				sourceURIPrefix, path)), nil
		}

		data, err := os.ReadFile(resolved)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("read file", err), nil
		}
		// Text comes back as text. Encoding binary as base64 by default
		// would hand the caller something it cannot read and cannot act
		// on, so that is opt-in.
		if utf8.Valid(data) && !strings.EqualFold(request.GetString("encoding", ""), "base64") {
			return mcp.NewToolResultText(string(data)), nil
		}
		if !strings.EqualFold(request.GetString("encoding", ""), "base64") {
			return mcp.NewToolResultError(fmt.Sprintf(
				"%s is not text. Pass encoding \"base64\" if you really want the bytes.", path)), nil
		}
		return mcp.NewToolResultText(base64.StdEncoding.EncodeToString(data)), nil
	}
}

func handleDeleteFile(factory workspace.Factory) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		path, err := request.RequireString("path")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		resolver, err := resolverFor(ctx, factory)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("workspace setup", err), nil
		}
		resolved, err := resolver.Resolve(path)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		info, err := os.Stat(resolved)
		if err != nil {
			if os.IsNotExist(err) {
				return mcp.NewToolResultError("no such file: " + path), nil
			}
			return mcp.NewToolResultErrorFromErr("delete", err), nil
		}
		// One file at a time. A recursive delete is a different tool
		// with a different risk profile, and nothing here needs it.
		if info.IsDir() {
			return mcp.NewToolResultError(path + " is a directory; delete_file removes one file"), nil
		}
		if err := os.Remove(resolved); err != nil {
			return mcp.NewToolResultErrorFromErr("delete", err), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf(
			"Deleted %s (%s reclaimed).", path, humanBytes(info.Size()))), nil
	}
}

// fileHit is one match.
type fileHit struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	Human string `json:"size"`
	Line  string `json:"line,omitempty"`
}

const maxSearchHits = 100

func handleSearchFile(factory workspace.Factory) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		resolver, err := resolverFor(ctx, factory)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("workspace setup", err), nil
		}
		bounded, ok := resolver.(workspace.Bounded)
		if !ok {
			return mcp.NewToolResultError(
				"search_file needs a bounded workspace; this server is running unscoped"), nil
		}
		root := bounded.WorkspaceRoot()
		namePat := strings.ToLower(request.GetString("name", ""))
		contains := request.GetString("contains", "")

		var hits []fileHit
		truncated := false
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return nil //nolint:nilerr // an unreadable entry is not a search failure
			}
			// Server scratch is not the caller's business.
			if strings.HasPrefix(d.Name(), workspace.StagePrefix) {
				return nil
			}
			rel, relErr := filepath.Rel(root, p)
			if relErr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if namePat != "" && !strings.Contains(strings.ToLower(rel), namePat) {
				return nil
			}
			info, infoErr := d.Info()
			if infoErr != nil || !info.Mode().IsRegular() {
				return nil
			}
			hit := fileHit{Path: rel, Bytes: info.Size(), Human: humanBytes(info.Size())}
			if contains != "" {
				line, found := firstMatchingLine(p, contains)
				if !found {
					return nil
				}
				hit.Line = line
			}
			if len(hits) >= maxSearchHits {
				truncated = true
				return filepath.SkipAll
			}
			hits = append(hits, hit)
			return nil
		})
		if err != nil {
			return mcp.NewToolResultErrorFromErr("search", err), nil
		}
		sort.Slice(hits, func(i, j int) bool { return hits[i].Path < hits[j].Path })

		out := map[string]any{"matches": hits, "count": len(hits)}
		if truncated {
			// Say so rather than silently returning a prefix — a
			// truncated answer that looks complete is worse than none.
			out["note"] = fmt.Sprintf(
				"stopped at %d matches; narrow the search with name or contains", maxSearchHits)
		}
		if len(hits) == 0 {
			out["note"] = "nothing matched. Omit both arguments to see everything in the workspace."
		}
		payload, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("encode results", err), nil
		}
		return mcp.NewToolResultText(string(payload)), nil
	}
}

// firstMatchingLine returns the first line of a text file containing
// needle. Binary files never match: searching them yields noise, and a
// byte sequence found inside a PDF tells the caller nothing.
func firstMatchingLine(path, needle string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil || info.Size() > maxGetFileBytes {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil || !utf8.Valid(data) {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, needle) {
			line = strings.TrimSpace(line)
			if len(line) > 160 {
				line = line[:160] + "…"
			}
			return line, true
		}
	}
	return "", false
}
