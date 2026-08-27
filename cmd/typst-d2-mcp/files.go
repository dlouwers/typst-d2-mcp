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

// maxGetFileBytes bounds what get_file will return inline.
//
// Reading a PDF back through a tool call is the wrong move — base64
// inflates it by a third and it lands in the caller's context as noise.
// PDFs have a resource URI for exactly this, so a large or binary file
// is refused with a pointer at the right mechanism rather than being
// silently truncated.
const maxGetFileBytes = 256 << 10

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
		if info.Size() > maxGetFileBytes {
			hint := ""
			if strings.EqualFold(filepath.Ext(path), ".pdf") {
				hint = fmt.Sprintf(" Fetch the PDF with resources/read on %s%s instead.",
					pdfURIPrefix, path)
			}
			return mcp.NewToolResultError(fmt.Sprintf(
				"%s is %s; get_file returns at most %s.%s",
				path, humanBytes(info.Size()), humanBytes(maxGetFileBytes), hint)), nil
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
