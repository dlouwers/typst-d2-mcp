package preprocessor

// Typst-aware scanner for d2 call sites.
//
// The previous regex approach kept hitting the same class of bugs: a
// `#d2` substring could appear inside a comment, a string, or another
// raw block — places where Typst itself would never evaluate it — and
// any regex pattern that ignores those contexts will eventually be
// surprised by a new edge case. We instead walk the source one byte at
// a time tracking which lexical context we're in, and only consider
// `d2(...)` or `d2[...]` as a real call when we encounter it somewhere
// Typst would actually evaluate it.
//
// Two languages, two lexers
// ------------------------
// The important distinction — and the source of two production bugs —
// is that a `#d2` call's *argument* is D2 source, not Typst markup.
// D2 has no `//` line comments and no `/* */` block comments; it uses
// `#` for comments instead. Applying Typst's comment rules while
// walking D2 code meant a perfectly ordinary label containing a glob
//
//     a: vault/*.md
//
// opened a "block comment" that ran to EOF, swallowing the closing `]`
// and everything after it. What the user saw was `unclosed delimiter`
// from typst, pointing at a bracket that was in fact balanced. So:
// Typst contexts use Typst's rules (scanRegion, skipBalancedParens),
// and the D2 body uses D2's (skipD2Brackets).
//
// Markup mode and code mode
// -------------------------
// We also track Typst's two syntactic modes, because the natural way
// to caption a diagram puts the call in code mode, without the hash:
//
//     #figure(d2(...)[...], caption: [A diagram])
//
// Matching only the literal `#d2` left that block in the source for
// typst to choke on (`unknown variable: d2`, or stranger things once
// hex colours in the D2 body started being evaluated as numbers).
// scanRegion therefore recurses: `(` after a `#name` opens code mode,
// `[` inside code mode opens markup mode again, and `d2` is a call in
// either. Blocks found in code mode are flagged CodeContext so the
// replacement omits the leading `#`, which is not valid there.
//
// Lexical contexts we recognise (and ignore d2 inside):
//
//   - // line comments              (until newline)      [Typst only]
//   - /* block comments */          (no nesting)         [Typst only]
//   - "..." double-quoted strings   (with \ escapes)     [both]
//   - `...` short raw               (terminated by next `)
//   - ```...``` raw block           (triple backticks)
//   - # line comments               (until newline)      [D2 only]
//
// What we deliberately don't do:
//   - Parse Typst expressions or scopes beyond the mode switching
//     described above.
//   - Treat `'` as a string delimiter inside D2 code. D2 accepts
//     single-quoted strings, but an apostrophe in an ordinary label
//     ("don't") is far more common than one, and treating it as a
//     delimiter would resurrect exactly the swallowed-bracket bug
//     this scanner exists to prevent.
//   - Support 4+ backtick raw fences (Typst allows them; LLMs rarely
//     produce them, and adding the open-count tracking is easy to add
//     later if it ever matters).

import (
	"strings"

	"github.com/dlouwers/typst-d2-mcp/internal/d2"
)

// Typst's two syntactic modes. The mode decides what a bare `d2`
// means and which delimiters open a nested region.
const (
	modeMarkup = iota
	modeCode
)

// maxScanDepth bounds scanRegion's recursion. Real documents nest a
// handful of levels; anything past this is pathological input, and we
// would rather stop descending than grow the stack without limit.
const maxScanDepth = 256

// SkipSite records a position where the scanner saw something that
// unambiguously looked like a d2 call — the name immediately followed
// by `(` or `[` — but could not extract it.
//
// Without this the scanner had no way to say "I recognised this and
// failed": it just declined, left the block in the source, and the
// failure surfaced several layers later as a Typst syntax error
// naming the wrong construct. Callers turn a non-empty slice into an
// error that names the preprocessor.
type SkipSite struct {
	Offset int
	Reason string
}

type scanner struct {
	src     string
	i       int
	blocks  []D2Block
	skipped []SkipSite
}

// scanD2Calls returns every d2 call site in src as a D2Block, in
// source-order. Each block's Start / End span the full call (from the
// `#` or the `d`, through the final `)` or `]`); Code is the extracted
// D2 source. The second return value lists call sites that looked real
// but could not be extracted — see SkipSite.
func scanD2Calls(src string) ([]D2Block, []SkipSite) {
	s := &scanner{src: src}
	s.scanRegion(modeMarkup, 0, 0)
	return s.blocks, s.skipped
}

// scanRegion walks src from the current position collecting d2 call
// sites, until EOF or — when stop is non-zero — the matching close
// delimiter, which it consumes before returning.
func (s *scanner) scanRegion(mode int, stop byte, depth int) {
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

		case stop != 0 && s.cur() == stop:
			s.i++
			return

		// A `#` in markup opens code. `#d2(` / `#d2[` is the call we
		// want; any other `#name(` opens an argument list whose
		// contents are code, and a d2 call can be nested in there.
		case mode == modeMarkup && s.cur() == '#':
			s.markupHash(depth)

		// In code mode a bare `d2` is the call. Anything else that
		// starts an identifier is consumed whole so that a name
		// merely *containing* d2 — or a field access like `a.d2` —
		// is not mistaken for one.
		case mode == modeCode && s.atIdentStart():
			s.codeIdent()

		// `#d2` in code mode is a mistake — typst rejects a second
		// hash there ("the character `#` is not valid in code"). It is
		// an easy one to make, since every example writes the call
		// with a hash, so accept it and drop the hash on replacement
		// rather than passing a document through that cannot compile.
		case mode == modeCode && s.peek("#d2") && !isIdentChar(s.at(s.i+3)):
			s.tryD2(s.i, s.i+3, true)

		// Nested regions. `[` is always markup; `(` in code mode is
		// more code. (`(` in markup mode is ordinary text.)
		case s.cur() == '[' && depth < maxScanDepth:
			s.i++
			s.scanRegion(modeMarkup, ']', depth+1)
		case mode == modeCode && s.cur() == '(' && depth < maxScanDepth:
			s.i++
			s.scanRegion(modeCode, ')', depth+1)

		default:
			s.i++
		}
	}
}

// markupHash handles a `#` in markup mode. It always advances at
// least one byte, so the caller makes progress.
func (s *scanner) markupHash(depth int) {
	hash := s.i
	j := s.i + 1
	for j < len(s.src) && isIdentChar(s.src[j]) {
		j++
	}
	name := s.src[hash+1 : j]

	switch name {
	case "d2":
		s.tryD2(hash, j, false)
	case "":
		// `#(`, `#[`, an escaped `#`, or a stray one in prose.
		s.i++
	default:
		s.i = j
		if s.cur() == '(' && depth < maxScanDepth {
			s.i++
			s.scanRegion(modeCode, ')', depth+1)
		}
	}
}

// codeIdent handles an identifier in code mode, which is a d2 call
// only when the name is exactly `d2` and a `(` or `[` follows.
func (s *scanner) codeIdent() {
	start := s.i
	j := s.i
	for j < len(s.src) && isIdentChar(s.src[j]) {
		j++
	}
	if s.src[start:j] == "d2" {
		s.tryD2(start, j, true)
		return
	}
	s.i = j
}

// atIdentStart reports whether the current byte begins an identifier
// rather than continuing one. The `.` check keeps a field access —
// `shape.d2(...)` — from reading as a call to `d2`.
func (s *scanner) atIdentStart() bool {
	if !isIdentChar(s.cur()) {
		return false
	}
	if s.i == 0 {
		return true
	}
	prev := s.src[s.i-1]
	return !isIdentChar(prev) && prev != '.'
}

func isIdentChar(c byte) bool {
	return c == '_' || c == '-' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9')
}

func (s *scanner) cur() byte {
	return s.at(s.i)
}

func (s *scanner) at(i int) byte {
	if i < 0 || i >= len(s.src) {
		return 0
	}
	return s.src[i]
}

func (s *scanner) peek(prefix string) bool {
	return strings.HasPrefix(s.src[s.i:], prefix)
}

func (s *scanner) skipLineComment() {
	s.i += 2 // past //
	s.skipToEOL()
}

func (s *scanner) skipToEOL() {
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

// tryD2 attempts to match a d2 call whose name spans [start, afterName)
// — `#d2` in markup, or `d2` in code. On success it appends a block and
// leaves s.i past the end. On failure it leaves s.i at afterName so the
// caller makes progress, and records a SkipSite when the text looked
// like a call it should have handled.
func (s *scanner) tryD2(start, afterName int, codeContext bool) {
	s.i = afterName

	switch s.cur() {
	case '(':
		// Two sub-shapes inside parens:
		//   d2(```code```)              — raw-block-only args, no opts
		//   d2(opts)[code]              — opts then a content block
		argsOpen := s.i + 1
		if !s.skipBalancedParens() {
			s.skip(start, "unbalanced parentheses in the option list")
			return
		}
		args := s.src[argsOpen : s.i-1]

		if rawCode, ok := extractSingleRawBlock(args); ok {
			s.emit(D2Block{
				Start:       start,
				End:         s.i,
				Options:     d2.Options{},
				Code:        rawCode,
				CodeContext: codeContext,
			})
			return
		}

		// Treat the args as options; require a content block to follow.
		if s.cur() != '[' {
			s.skip(start, "options are not followed by a [ ... ] diagram block")
			return
		}
		codeOpen := s.i + 1
		if !s.skipD2Brackets() {
			s.skip(start, "unbalanced brackets in the diagram block")
			return
		}
		s.emit(D2Block{
			Start:       start,
			End:         s.i,
			Options:     parseOptions(args),
			Code:        s.src[codeOpen : s.i-1],
			CodeContext: codeContext,
		})

	case '[':
		// d2[code] — bracket-only form.
		codeOpen := s.i + 1
		if !s.skipD2Brackets() {
			s.skip(start, "unbalanced brackets in the diagram block")
			return
		}
		s.emit(D2Block{
			Start:       start,
			End:         s.i,
			Options:     d2.Options{},
			Code:        s.src[codeOpen : s.i-1],
			CodeContext: codeContext,
		})

	default:
		// Not a call at all: `#d2` alone in prose, or a variable that
		// happens to be named d2. Nothing to report.
	}
}

func (s *scanner) emit(b D2Block) {
	s.blocks = append(s.blocks, b)
}

func (s *scanner) skip(offset int, reason string) {
	s.skipped = append(s.skipped, SkipSite{Offset: offset, Reason: reason})
}

// skipBalancedParens consumes a `(...)` starting at the current `(`,
// counting nested parens and treating strings/raws/comments inside as
// opaque. Returns true on a clean close, false if EOF is hit first.
//
// This walks Typst code (a d2 option list), so Typst's comment rules
// apply here — unlike skipD2Brackets below.
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

// skipD2Brackets consumes a `[...]` whose contents are D2 source
// rather than Typst markup, counting nested brackets to find the
// close. Returns true on a clean close, false if EOF is hit first.
//
// D2's lexer is not Typst's, and the difference is not cosmetic:
//
//   - `/*` and `//` are ordinary characters. A glob in a label
//     (`a: vault/*.md`) or a bare URL (`a: https://example.com`) must
//     not open a comment — treating them as one swallowed the closing
//     bracket and produced a bogus "unclosed delimiter" from typst.
//   - `#` starts a line comment, but only as the first non-blank
//     character of a line. Mid-line it is ordinary text (`a: C#`), and
//     a hex colour lives inside a string ("#5e5e5e") which is skipped
//     as a string before the `#` is ever considered.
func (s *scanner) skipD2Brackets() bool {
	if s.cur() != '[' {
		return false
	}
	s.i++
	depth := 1
	lineBlank := true // nothing but whitespace seen on this line yet
	for s.i < len(s.src) {
		switch c := s.cur(); {
		case c == '"':
			s.skipString()
			lineBlank = false
		case c == '#' && lineBlank:
			s.skipToEOL()
		case c == '\n':
			s.i++
			lineBlank = true
		case c == ' ' || c == '\t' || c == '\r':
			s.i++
		case c == '[':
			depth++
			s.i++
			lineBlank = false
		case c == ']':
			depth--
			s.i++
			if depth == 0 {
				return true
			}
			lineBlank = false
		default:
			s.i++
			lineBlank = false
		}
	}
	return false
}

// extractSingleRawBlock decides whether args (the text between the
// parens of a d2(...) call) is, ignoring surrounding whitespace, a
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
