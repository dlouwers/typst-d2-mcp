package main

import (
	"os"
	"sort"
	"strings"
)

// This file is the one place that reads a template's surface: the
// exports it offers and how to call them.
//
// It exists because knowing a template's name was never enough. A
// caller importing something somebody else published could see that
// `report` exists and not what arguments it takes, so the only way to
// find out was to write a probe document, compile it, and read the
// error — a compile spent on something the listing already knew, and
// against a metered server that is a real cost. Worse, the trick only
// works when a missing argument is *required*: a template whose
// arguments all have defaults compiles silently, and the caller never
// learns what it could have passed.
//
// Everything reported here is parsed from the entrypoint typst itself
// resolves, so the description and the import cannot describe different
// code. It stays lexical rather than asking typst, which has no
// "describe a package" mode.

// paramEntry is one parameter of an export.
//
// Positional matters to a caller in a way the name alone does not: a
// parameter with a default is settable by name in `.with(...)`, and one
// without must be passed positionally. Getting that wrong is the error
// the probe compile was being spent to discover.
type paramEntry struct {
	Name       string `json:"name"`
	Default    string `json:"default,omitempty"`
	Positional bool   `json:"positional,omitempty"`
}

// exportEntry is one callable a template offers.
type exportEntry struct {
	Name string `json:"name"`
	// Signature is the parameter list as written, whitespace collapsed —
	// enough to copy a correct call from without reading Params.
	Signature string `json:"signature"`
	// Template reports that this export ends in a positional `body`, so
	// it is applied with `#show: name.with(...)` rather than called.
	Template bool         `json:"template,omitempty"`
	Params   []paramEntry `json:"params,omitempty"`
	// Doc is the `///` comment block above the declaration, if the
	// author wrote one. The convention predates this listing — the house
	// `adr` already explains there why its first section is `background:`
	// and not `context:` — and that explanation was being discarded.
	Doc string `json:"doc,omitempty"`
}

// parseExports describes every public export of a template entrypoint,
// name-sorted. A missing or unparseable file yields nothing rather than
// an error: a listing that omits a description is worth far more than a
// listing that fails.
func parseExports(libPath string) []exportEntry {
	content, err := os.ReadFile(libPath)
	if err != nil {
		return nil
	}
	var out []exportEntry
	for _, decl := range letDeclarations(string(content)) {
		params := parseParams(decl.params)
		out = append(out, exportEntry{
			Name:      decl.name,
			Signature: decl.name + "(" + signatureOf(params) + ")",
			Template:  isDocumentTemplate(decl.params),
			Params:    params,
			Doc:       decl.doc,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// parseParams splits a parameter list into its parts. A part with a
// top-level colon is named and carries the text after it as its
// default; anything else is positional.
func parseParams(params string) []paramEntry {
	var out []paramEntry
	for _, part := range splitTopLevel(params) {
		part = collapseSpace(part)
		if part == "" {
			continue
		}
		if name, def, ok := cutTopLevel(part, ':'); ok {
			out = append(out, paramEntry{
				Name:    strings.TrimSpace(name),
				Default: strings.TrimSpace(def),
			})
			continue
		}
		out = append(out, paramEntry{Name: part, Positional: true})
	}
	return out
}

// signatureOf renders parameters back as they would be written.
func signatureOf(params []paramEntry) string {
	parts := make([]string, 0, len(params))
	for _, p := range params {
		if p.Positional {
			parts = append(parts, p.Name)
			continue
		}
		parts = append(parts, p.Name+": "+p.Default)
	}
	return strings.Join(parts, ", ")
}

// cutTopLevel splits at the first sep that is not nested inside
// brackets or a string, so a default value containing one does not
// masquerade as the separator.
func cutTopLevel(s string, sep byte) (before, after string, found bool) {
	depth := 0
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
		case sep:
			if depth == 0 {
				return s[:i], s[i+1:], true
			}
		}
	}
	return s, "", false
}

// collapseSpace flattens a parameter written across several lines into
// one, so a signature reads as a call rather than as source.
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// docCommentAbove collects the contiguous `///` lines immediately above
// a declaration, stripped of their markers.
func docCommentAbove(src string, declStart int) string {
	lineStart := strings.LastIndexByte(src[:declStart], '\n') + 1
	var lines []string
	for lineStart > 0 {
		prevStart := strings.LastIndexByte(src[:lineStart-1], '\n') + 1
		line := strings.TrimSpace(src[prevStart : lineStart-1])
		rest, ok := strings.CutPrefix(line, "///")
		if !ok {
			break
		}
		lines = append([]string{strings.TrimSpace(rest)}, lines...)
		lineStart = prevStart
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

type letDecl struct {
	name   string
	params string
	doc    string
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
	var out []string
	for _, e := range parseExports(libPath) {
		if e.Template {
			out = append(out, e.Name)
		}
	}
	return out
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
		declStart := i + idx
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
		out = append(out, letDecl{
			name:   name,
			params: params,
			doc:    docCommentAbove(src, declStart),
		})
		i = end
	}
	return out
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

func isIdentByte(c byte) bool {
	return c == '-' || c == '_' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
