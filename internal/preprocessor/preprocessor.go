// Package preprocessor handles D2 diagram preprocessing in Typst files.
package preprocessor

import (
	"encoding/base64"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/dlouwers/typst-d2-mcp/internal/d2"
)

// D2Block represents a parsed D2 diagram block from the Typst file.
type D2Block struct {
	Match   *regexp.Regexp // Not used, keeping for reference
	Start   int
	End     int
	Options d2.Options
	Code    string
}

// PreprocessFile reads a Typst file, processes all D2 blocks, and returns the modified content.
func PreprocessFile(inputPath string) (string, error) {
	// Read file content
	contentBytes, err := os.ReadFile(inputPath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	content := string(contentBytes)

	// Remove old lib.typ imports
	content = regexp.MustCompile(`#import\s+["'].*?lib\.typ["'].*?\n`).ReplaceAllString(content, "")

	// Find all D2 calls
	d2Blocks := extractD2Calls(content)

	if len(d2Blocks) == 0 {
		fmt.Fprintf(os.Stderr, "No D2 diagrams found in file.\n")
		return content, nil
	}

	fmt.Fprintf(os.Stderr, "Found %d D2 diagram(s), rendering...\n", len(d2Blocks))

	// Replace in reverse order to preserve positions
	for i := len(d2Blocks) - 1; i >= 0; i-- {
		block := d2Blocks[i]
		fmt.Fprintf(os.Stderr, "  [%d/%d] Rendering diagram...\n", len(d2Blocks)-i, len(d2Blocks))

		// Render D2 to SVG
		svg, err := d2.Render(block.Code, block.Options)
		if err != nil {
			return "", fmt.Errorf("failed to render diagram %d: %w", i+1, err)
		}

		// Convert to Typst image
		typstImg := svgToTypstImage(svg, block.Options)

		// Replace in content
		content = content[:block.Start] + typstImg + content[block.End:]
	}

	// Add based package import
	content = addBasedImport(content)

	fmt.Fprintf(os.Stderr, "✅ All diagrams rendered successfully\n")
	return content, nil
}

// extractD2Calls finds all #d2[...] or #d2(options)[...] blocks in the content.
func extractD2Calls(content string) []D2Block {
	// Pattern: #d2(key: value, ...)[code] or #d2[code]
	// (?s) makes . match newlines
	pattern := regexp.MustCompile(`(?s)#d2(?:\((.*?)\))?\[(.*?)\]`)
	matches := pattern.FindAllStringSubmatchIndex(content, -1)

	blocks := make([]D2Block, 0, len(matches))

	for _, match := range matches {
		// match[0], match[1] = full match start/end
		// match[2], match[3] = options group start/end
		// match[4], match[5] = code group start/end

		var optionsStr string
		if match[2] != -1 && match[3] != -1 {
			optionsStr = content[match[2]:match[3]]
		}

		code := content[match[4]:match[5]]

		// Parse options
		options := parseOptions(optionsStr)

		blocks = append(blocks, D2Block{
			Start:   match[0],
			End:     match[1],
			Options: options,
			Code:    code,
		})
	}

	return blocks
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

// svgToTypstImage converts SVG content to a Typst image() call using base64 encoding.
func svgToTypstImage(svgContent string, options d2.Options) string {
	// Encode SVG as base64
	b64 := base64.StdEncoding.EncodeToString([]byte(svgContent))

	// Create Typst image call
	typstCode := fmt.Sprintf(`#image(decode64("%s"), format: "svg")`, b64)

	// Add padding if specified
	if pad, ok := options["pad"]; ok && pad != "none" {
		typstCode = fmt.Sprintf(`#pad(%s, %s)`, pad, typstCode)
	}

	return typstCode
}

// addBasedImport adds the based package import at the top of the file.
func addBasedImport(content string) string {
	basedImport := `#import "@preview/based:0.2.0": decode64` + "\n"

	// Check if based import already exists
	if strings.Contains(content, "based") {
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
