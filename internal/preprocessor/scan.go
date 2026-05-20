package preprocessor

// Typst-aware scanner for #d2 call sites.
//
// The previous regex approach kept hitting the same class of bugs: a
// `#d2` substring could appear inside a comment, a string, or another
// raw block — places where Typst itself would never evaluate it — and
// any regex pattern that ignores those contexts will eventually be
// surprised by a new edge case. We instead walk the source one byte at
// a time tracking which lexical context we're in, and only consider
// `#d2(...)` or `#d2[...]` as a real call when we encounter it in
// normal markup.
//
// Lexical contexts we recognise (and ignore #d2 inside):
//
//   - // line comments              (until newline)
//   - /* block comments */          (no nesting; matches Typst)
//   - "..." double-quoted strings   (with \ escapes)
//   - `...` short raw               (terminated by next ` )
//   - ```...``` raw block           (triple backticks)
//
// Inside the argument of a recognised #d2 call we apply the same
// context-tracking to count balanced ( ) or [ ] — so D2 code that
// happens to contain a closing bracket inside a quoted label, or a
// nested function call, is captured correctly instead of cutting the
// match short or running on past it.
//
// What we deliberately don't do:
//   - Parse Typst expressions, scopes, or `code-mode` semantics.
//   - Distinguish markup mode from code mode (no need; markup-mode
//     `#d2` is the only call shape LLMs emit in practice).
//   - Support 4+ backtick raw fences (Typst allows them; LLMs rarely
//     produce them, and adding the open-count tracking is easy to add
//     later if it ever matters).

import (
	"strings"

	"github.com/dlouwers/typst-d2-mcp/internal/d2"
)

type scanner struct {
	src string
	i   int
}

// scanD2Calls returns every #d2 call site in src as a D2Block, in
// source-order. Each block's Start / End span the full call (from `#`
// through the final `)` or `]`); Code is the extracted D2 source.
func scanD2Calls(src string) []D2Block {
	s := &scanner{src: src}
	var out []D2Block
	for s.i < len(s.src) {
		switch {
		case s.peek("//"):
			s.skipLineComment()
		case s.peek("/*"):
			s.skipBlockComment()
		case s.cur() == '"':
			s.skipString()
		case s.peek("```"):
			s.skipRawBlock()
		case s.cur() == '`':
			s.skipShortRaw()
		case s.peek("#d2"):
			if block, ok := s.tryD2(); ok {
				out = append(out, block)
			}
			// tryD2 always advances at least past the `#d2` substring
			// (whether it matched a real call or not), so we make
			// monotonic progress.
		default:
			s.i++
		}
	}
	return out
}

func (s *scanner) cur() byte {
	if s.i >= len(s.src) {
		return 0
	}
	return s.src[s.i]
}

func (s *scanner) peek(prefix string) bool {
	return strings.HasPrefix(s.src[s.i:], prefix)
}

func (s *scanner) skipLineComment() {
	s.i += 2 // past //
	for s.i < len(s.src) && s.src[s.i] != '\n' {
		s.i++
	}
}

func (s *scanner) skipBlockComment() {
	s.i += 2 // past /*
	for s.i < len(s.src) {
		if s.peek("*/") {
			s.i += 2
			return
		}
		s.i++
	}
}

func (s *scanner) skipString() {
	s.i++ // past opening "
	for s.i < len(s.src) {
		switch s.src[s.i] {
		case '\\':
			// Skip the escape and its target. Stops one short of EOF
			// being valid, which is fine for malformed input.
			s.i += 2
		case '"':
			s.i++
			return
		default:
			s.i++
		}
	}
}

func (s *scanner) skipShortRaw() {
	s.i++ // past opening `
	for s.i < len(s.src) {
		if s.src[s.i] == '`' {
			s.i++
			return
		}
		s.i++
	}
}

func (s *scanner) skipRawBlock() {
	s.i += 3 // past opening ```
	for s.i < len(s.src) {
		if s.peek("```") {
			s.i += 3
			return
		}
		s.i++
	}
}

// tryD2 attempts to match a #d2 call starting at the current `#`
// position. On success returns the parsed block and leaves s.i past
// the end. On failure returns false and leaves s.i three past the `#`
// (i.e. past the `#d2` substring) so the outer loop makes progress.
func (s *scanner) tryD2() (D2Block, bool) {
	start := s.i
	s.i += 3 // past #d2

	switch s.cur() {
	case '(':
		// Two sub-shapes inside parens:
		//   #d2(```code```)              — raw-block-only args, no opts
		//   #d2(opts)[code]              — opts then a content block
		argsOpen := s.i + 1
		if !s.skipBalancedParens() {
			return D2Block{}, false
		}
		args := s.src[argsOpen : s.i-1]

		if rawCode, ok := extractSingleRawBlock(args); ok {
			return D2Block{
				Start:   start,
				End:     s.i,
				Options: d2.Options{},
				Code:    rawCode,
			}, true
		}

		// Treat the args as options; require a content block to follow.
		if s.cur() != '[' {
			return D2Block{}, false
		}
		codeOpen := s.i + 1
		if !s.skipBalancedBrackets() {
			return D2Block{}, false
		}
		return D2Block{
			Start:   start,
			End:     s.i,
			Options: parseOptions(args),
			Code:    s.src[codeOpen : s.i-1],
		}, true

	case '[':
		// #d2[code] — bracket-only form.
		codeOpen := s.i + 1
		if !s.skipBalancedBrackets() {
			return D2Block{}, false
		}
		return D2Block{
			Start:   start,
			End:     s.i,
			Options: d2.Options{},
			Code:    s.src[codeOpen : s.i-1],
		}, true

	default:
		// Not a #d2 call (could be #d2foo, or #d2 alone in prose).
		return D2Block{}, false
	}
}

// skipBalancedParens consumes a `(...)` starting at the current `(`,
// counting nested parens and treating strings/raws/comments inside as
// opaque. Returns true on a clean close, false if EOF is hit first.
func (s *scanner) skipBalancedParens() bool {
	if s.cur() != '(' {
		return false
	}
	s.i++
	depth := 1
	for s.i < len(s.src) {
		switch {
		case s.peek("//"):
			s.skipLineComment()
		case s.peek("/*"):
			s.skipBlockComment()
		case s.cur() == '"':
			s.skipString()
		case s.peek("```"):
			s.skipRawBlock()
		case s.cur() == '`':
			s.skipShortRaw()
		case s.cur() == '(':
			depth++
			s.i++
		case s.cur() == ')':
			depth--
			s.i++
			if depth == 0 {
				return true
			}
		default:
			s.i++
		}
	}
	return false
}

// skipBalancedBrackets is skipBalancedParens for `[...]`. Same shape,
// different delimiters — kept as separate methods rather than param-
// eterised so each one's hot path is a tight set of byte comparisons.
func (s *scanner) skipBalancedBrackets() bool {
	if s.cur() != '[' {
		return false
	}
	s.i++
	depth := 1
	for s.i < len(s.src) {
		switch {
		case s.peek("//"):
			s.skipLineComment()
		case s.peek("/*"):
			s.skipBlockComment()
		case s.cur() == '"':
			s.skipString()
		case s.peek("```"):
			s.skipRawBlock()
		case s.cur() == '`':
			s.skipShortRaw()
		case s.cur() == '[':
			depth++
			s.i++
		case s.cur() == ']':
			depth--
			s.i++
			if depth == 0 {
				return true
			}
		default:
			s.i++
		}
	}
	return false
}

// extractSingleRawBlock decides whether args (the text between the
// parens of a #d2(...) call) is, ignoring surrounding whitespace, a
// single ```...``` raw block. If so it returns the block's body with
// the optional language tag (e.g. ```d2) and one leading/trailing
// newline stripped. Otherwise it returns false, signalling "treat
// args as key:value options instead".
func extractSingleRawBlock(args string) (string, bool) {
	trimmed := strings.TrimSpace(args)
	if !strings.HasPrefix(trimmed, "```") || !strings.HasSuffix(trimmed, "```") || len(trimmed) < 6 {
		return "", false
	}
	inner := trimmed[3 : len(trimmed)-3]
	// Reject if the body itself contains a triple-backtick — that
	// means args isn't ONE raw block; it's something more complex
	// (e.g. two adjacent raws), which we don't classify here.
	if strings.Contains(inner, "```") {
		return "", false
	}
	// Strip a leading "first-line" segment in two cases that look
	// the same to the user — a bare newline immediately after the
	// opening ``` (no language tag), or a language tag followed by
	// a newline. Anything else on that first line is real code.
	if nl := strings.IndexByte(inner, '\n'); nl >= 0 {
		first := strings.TrimSpace(inner[:nl])
		if first == "" || isLanguageTag(first) {
			inner = inner[nl+1:]
		}
	}
	inner = strings.TrimSuffix(inner, "\n")
	return inner, true
}

func isLanguageTag(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}
