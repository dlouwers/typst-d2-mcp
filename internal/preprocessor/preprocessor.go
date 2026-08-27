// Package preprocessor handles D2 diagram preprocessing in Typst files.
package preprocessor

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"

	"github.com/dlouwers/typst-d2-mcp/internal/d2"
	"github.com/dlouwers/typst-d2-mcp/internal/workspace"
)

// D2Block represents a parsed D2 diagram block from the Typst file.
type D2Block struct {
	Match   *regexp.Regexp // Not used, keeping for reference
	Start   int
	End     int
	Options d2.Options
	Code    string

	// CodeContext is true when the call was found in Typst code mode
	// rather than markup — `#figure(d2(...)[...], caption: [...])`.
	// The replacement must then omit the leading `#`, which typst
	// rejects there ("the character `#` is not valid in code").
	CodeContext bool
}

// PreprocessFile reads a Typst file from the local filesystem, processes all
// D2 blocks, and returns the modified content. It is a back-compat wrapper
// around Preprocess that uses workspace.LocalFS as the resolver and a
// background context, preserving the original behavior used by the
// typst-d2-prep CLI.
func PreprocessFile(inputPath string) (string, error) {
	return Preprocess(context.Background(), workspace.LocalFS{}, inputPath)
}

// Preprocess resolves inputPath through the supplied workspace.Resolver,
// reads the resulting file, processes all D2 blocks, and returns the
// modified Typst content. Callers in HTTP mode pass a tenant-scoped
// resolver; the stdio path passes workspace.LocalFS. The context bounds
// each d2.Render invocation — pass a context.WithTimeout from the tool
// handler to enforce a per-compile budget.
func Preprocess(ctx context.Context, r workspace.Resolver, inputPath string) (string, error) {
	resolved, err := r.Resolve(inputPath)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	contentBytes, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	content := string(contentBytes)

	// Remove old lib.typ imports
	content = regexp.MustCompile(`#import\s+["'].*?lib\.typ["'].*?\n`).ReplaceAllString(content, "")

	// Find all D2 calls
	d2Blocks, skipped := extractD2Calls(content)

	// A call site the scanner recognised but could not extract used to
	// be left in the source for typst to trip over, several layers
	// later, with an error naming the wrong construct — usually
	// "unclosed delimiter" at a bracket that was in fact balanced.
	// Failing here instead costs nothing and names the actual cause.
	if len(skipped) > 0 {
		site := skipped[0]
		line, col := lineCol(content, site.Offset)
		return "", fmt.Errorf(
			"preprocessor: %s:%d:%d: found a d2 diagram block but could not extract it (%s). "+
				"It was left unprocessed, so typst would fail on the D2 source with a misleading error",
			inputPath, line, col, site.Reason)
	}

	if len(d2Blocks) == 0 {
		slog.DebugContext(ctx, "no d2 blocks in input")
		return content, nil
	}

	slog.DebugContext(ctx, "rendering d2 blocks", "count", len(d2Blocks))

	// Render forward, replace backward.
	//
	// Replacement must run in reverse so earlier offsets stay valid,
	// but rendering in that order meant the first failure REACHED was
	// the last diagram in the document — so a broken first diagram was
	// reported as "failed to render diagram 3". An agent went hunting
	// through its third diagram for a fault that was not there (#110).
	// Rendering first, in document order, makes the diagram named the
	// diagram to look at.
	rendered := make([]string, len(d2Blocks))
	for i, block := range d2Blocks {
		svg, err := d2.Render(ctx, block.Code, block.Options)
		if err != nil {
			return "", fmt.Errorf("failed to render diagram %d of %d: %w",
				i+1, len(d2Blocks), err)
		}
		rendered[i] = svgToTypstImage(svg, block.Options, block.CodeContext)
	}
	for i := len(d2Blocks) - 1; i >= 0; i-- {
		block := d2Blocks[i]
		content = content[:block.Start] + rendered[i] + content[block.End:]
	}

	// Add based package import
	content = addBasedImport(content)

	return content, nil
}

// extractD2Calls finds every #d2(...) and #d2[...] call site in the
// content. Delegates to the Typst-aware scanner in scan.go; see the
// commentary there for what we recognise and what we deliberately
// don't.
func extractD2Calls(content string) ([]D2Block, []SkipSite) {
	return scanD2Calls(content)
}

// lineCol converts a byte offset into 1-based line and column numbers,
// so preprocessor errors point at the same coordinates typst would.
func lineCol(content string, offset int) (int, int) {
	if offset > len(content) {
		offset = len(content)
	}
	line := 1 + strings.Count(content[:offset], "\n")
	col := offset - (strings.LastIndex(content[:offset], "\n") + 1) + 1
	return line, col
}

// parseOptions extracts key-value pairs from the options string.
func parseOptions(optionsStr string) d2.Options {
	options := make(d2.Options)

	if optionsStr == "" {
		return options
	}

	// Pattern: key: value (simple parser)
	optPattern := regexp.MustCompile(`(\w+):\s*([^,\)]+)`)
	matches := optPattern.FindAllStringSubmatch(optionsStr, -1)

	for _, match := range matches {
		if len(match) >= 3 {
			key := strings.TrimSpace(match[1])
			value := strings.TrimSpace(match[2])
			// Remove quotes if present
			value = strings.Trim(value, `"'`)
			options[key] = value
		}
	}

	return options
}

// svgToTypstImage converts SVG content to a Typst image() call using base64
// encoding. The image is rendered at width: 100% by default so a wide D2
// diagram scales to fit the page instead of overflowing horizontally —
// without this cap, Typst emits placement warnings, drops subsequent
// content, and produces a silently-truncated PDF (exit code 0 + warning
// on stderr).
//
// An explicit "width" key in options overrides the default; "none" or
// the literal "intrinsic" disables the constraint entirely so the SVG
// renders at its natural size (rarely what you want, but supported for
// callers who know).
//
// The expression is built without a hash and gets one only at the very
// end, and only in markup. Typst allows `#` exactly once, to cross from
// markup into code: inside an argument list we are already in code, and
// a second one is a hard error. That applies both to a call found in
// code context (codeContext, e.g. inside #figure(...)) and to the pad
// wrapper, which used to nest one unconditionally — `#pad(4pt,
// #image(...))` never compiled.
func svgToTypstImage(svgContent string, options d2.Options, codeContext bool) string {
	b64 := base64.StdEncoding.EncodeToString([]byte(svgContent))

	width, ok := options["width"]
	if !ok {
		width = "100%"
	}
	var typstCode string
	if width == "none" || width == "intrinsic" {
		typstCode = fmt.Sprintf(`image(decode64("%s"), format: "svg")`, b64)
	} else {
		typstCode = fmt.Sprintf(`image(decode64("%s"), format: "svg", width: %s)`, b64, width)
	}

	if pad, ok := options["pad"]; ok && pad != "none" {
		typstCode = fmt.Sprintf(`pad(%s, %s)`, pad, typstCode)
	}

	if codeContext {
		return typstCode
	}
	return "#" + typstCode
}

// addBasedImport adds the based package import at the top of the file.
func addBasedImport(content string) string {
	basedImport := `#import "@preview/based:0.2.0": decode64` + "\n"

	// Check if based import already exists (any version)
	basedImportPattern := regexp.MustCompile(`#import\s+"@preview/based:[^"]+"\s*:\s*decode64`)
	if basedImportPattern.MatchString(content) {
		return content
	}

	// Find the position to insert (after last import or at start)
	importPattern := regexp.MustCompile(`(?m)^#import.*?$`)
	matches := importPattern.FindAllStringIndex(content, -1)

	if len(matches) > 0 {
		// Insert after last import
		lastMatch := matches[len(matches)-1]
		insertPos := lastMatch[1] + 1 // +1 for newline
		return content[:insertPos] + basedImport + content[insertPos:]
	}

	// No imports found, insert at start
	return basedImport + content
}
