package preprocessor

import (
	"strings"
	"testing"
)

// Each case is "an arbitrary chunk of Typst that contains zero or more
// #d2 call sites we expect to extract". The wantCount/wantCodes pair
// asserts both how many matched AND what each match's code body was.
// The body assertion is what catches "matched the wrong span" bugs,
// which is what the previous regex approach kept doing.
func TestScanD2Calls(t *testing.T) {
	cases := []struct {
		name      string
		src       string
		wantCount int
		wantCodes []string
	}{
		{
			name:      "empty",
			src:       "",
			wantCount: 0,
		},
		{
			name:      "no d2 calls",
			src:       "= Heading\n\nSome prose with no diagrams.",
			wantCount: 0,
		},

		// --- bracket form ---

		{
			name:      "single bracket-form",
			src:       "#d2[a -> b]",
			wantCount: 1,
			wantCodes: []string{"a -> b"},
		},
		{
			name:      "bracket-form with opts",
			src:       `#d2(layout: "elk", theme: "0")[a -> b]`,
			wantCount: 1,
			wantCodes: []string{"a -> b"},
		},
		{
			name:      "bracket-form with nested brackets in code",
			src:       "#d2[user: User {\n  shape: person\n}]",
			wantCount: 1,
			wantCodes: []string{"user: User {\n  shape: person\n}"},
		},
		{
			name:      "two bracket-form blocks",
			src:       "#d2[a -> b]\n\nText.\n\n#d2[c -> d]",
			wantCount: 2,
			wantCodes: []string{"a -> b", "c -> d"},
		},

		// --- raw-string form ---

		{
			name:      "raw-string form",
			src:       "#d2(```\ndirection: down\na -> b\n```)",
			wantCount: 1,
			wantCodes: []string{"direction: down\na -> b"},
		},
		{
			name:      "raw-string with language tag",
			src:       "#d2(```d2\nx -> y\n```)",
			wantCount: 1,
			wantCodes: []string{"x -> y"},
		},
		{
			name:      "raw-string without trailing newline",
			src:       "#d2(```a -> b```)",
			wantCount: 1,
			wantCodes: []string{"a -> b"},
		},

		// --- mixed forms ---

		{
			name:      "bracket then raw-string",
			src:       "#d2[a -> b]\n\nbetween\n\n#d2(```\nc -> d\n```)",
			wantCount: 2,
			wantCodes: []string{"a -> b", "c -> d"},
		},

		// --- the gobble regression ---
		// The previous regex matched ONE span from the first #d2(```
		// to the document's last ] — eating the second block and the
		// markup between. The scanner must keep them independent.
		{
			name: "two raw-string blocks with markup brackets between",
			src: `Lead-in.

#d2(` + "```" + `
direction: down
ukraine: "Russia–Ukraine War (year 5)"
` + "```" + `)

Middle markup with #text[a label] and [more bracket content].

#d2(` + "```" + `
direction: down
iran: "Iran"
` + "```" + `)

Tail #text[footnote].`,
			wantCount: 2,
			wantCodes: []string{
				"direction: down\nukraine: \"Russia–Ukraine War (year 5)\"",
				"direction: down\niran: \"Iran\"",
			},
		},

		// --- contexts where #d2 must be IGNORED ---

		{
			name:      "ignore inside line comment",
			src:       "// #d2[ignored]\n#d2[real]",
			wantCount: 1,
			wantCodes: []string{"real"},
		},
		{
			name:      "ignore inside block comment",
			src:       "/* keep #d2[ignored] here */ #d2[real]",
			wantCount: 1,
			wantCodes: []string{"real"},
		},
		{
			name:      "ignore inside string literal",
			src:       `let x = "before #d2[ignored] after"; #d2[real]`,
			wantCount: 1,
			wantCodes: []string{"real"},
		},
		{
			name:      "ignore inside short raw",
			src:       "see `#d2[ignored]` for example; #d2[real]",
			wantCount: 1,
			wantCodes: []string{"real"},
		},
		{
			name:      "ignore inside raw block",
			src:       "```\n#d2[ignored]\n```\n#d2[real]",
			wantCount: 1,
			wantCodes: []string{"real"},
		},
		{
			name:      "string escape doesn't end the string",
			src:       `let x = "with \"#d2[ignored]\" inside"; #d2[real]`,
			wantCount: 1,
			wantCodes: []string{"real"},
		},

		// --- D2 code is not Typst: no // or /* comments ---
		// Each of these used to open a "comment" that ran past the
		// closing bracket, so the block was never extracted and typst
		// reported `unclosed delimiter` on a balanced bracket.

		{
			name:      "glob in a label is not a block comment",
			src:       "#d2(layout: \"elk\")[\n  a: vault/*.md\n  a -> b\n]\n\nAfter.",
			wantCount: 1,
			wantCodes: []string{"\n  a: vault/*.md\n  a -> b\n"},
		},
		{
			name:      "two globs do not pair into a comment",
			src:       "#d2[\n  a: src/*.go\n  b: logs/*.json\n  a -> b\n]",
			wantCount: 1,
			wantCodes: []string{"\n  a: src/*.go\n  b: logs/*.json\n  a -> b\n"},
		},
		{
			// Same line as the closing bracket, which is where the
			// `//` bug actually bites: skipping to end-of-line ate
			// the `]` too.
			name:      "bare URL on the same line as the close bracket",
			src:       "#d2[a: https://example.com]",
			wantCount: 1,
			wantCodes: []string{"a: https://example.com"},
		},
		{
			name:      "bare URL in a label is not a line comment",
			src:       "#d2[\n  a: https://example.com\n  a -> b\n]",
			wantCount: 1,
			wantCodes: []string{"\n  a: https://example.com\n  a -> b\n"},
		},
		{
			name:      "hex colours in D2 code are opaque",
			src:       "#d2[\n  x.style.stroke: \"#5e5e5e\"\n  x.style.fill: \"#006566\"\n]",
			wantCount: 1,
			wantCodes: []string{"\n  x.style.stroke: \"#5e5e5e\"\n  x.style.fill: \"#006566\"\n"},
		},
		{
			// D2's own comment syntax. A `]` inside one must not close
			// the block early.
			name:      "D2 line comment does not affect bracket depth",
			src:       "#d2[\n  # a stray ] in a comment\n  a -> b\n]",
			wantCount: 1,
			wantCodes: []string{"\n  # a stray ] in a comment\n  a -> b\n"},
		},
		{
			// Only at the start of a line. Mid-line `#` is ordinary
			// text in a label and must not eat the rest of the line.
			name:      "mid-line hash is not a D2 comment",
			src:       "#d2[\n  lang: C#\n  lang -> b\n]",
			wantCount: 1,
			wantCodes: []string{"\n  lang: C#\n  lang -> b\n"},
		},

		// --- code context: a diagram inside another call ---

		{
			name:      "figure with a bare d2 call",
			src:       "#figure(\n  d2(layout: \"elk\")[\n    a -> b\n  ],\n  caption: [A diagram.],\n)",
			wantCount: 1,
			wantCodes: []string{"\n    a -> b\n  "},
		},
		{
			name:      "figure with the wrapped markup form still works",
			src:       "#figure([#d2(layout: \"elk\")[a -> b]], caption: [A diagram.])",
			wantCount: 1,
			wantCodes: []string{"a -> b"},
		},
		{
			name:      "d2 in a figure caption",
			src:       "#figure(rect(), caption: [see #d2[a -> b] here])",
			wantCount: 1,
			wantCodes: []string{"a -> b"},
		},
		{
			name:      "identifier merely containing d2 is not a call",
			src:       "#figure(myd2(x), caption: [none])\n#d2[real]",
			wantCount: 1,
			wantCodes: []string{"real"},
		},
		{
			name:      "field access .d2 is not a call",
			src:       "#figure(shape.d2(x), caption: [none])\n#d2[real]",
			wantCount: 1,
			wantCodes: []string{"real"},
		},
		{
			// Bare d2( in markup is literal text, not a call — only
			// code context promotes it.
			name:      "bare d2( in prose is not a call",
			src:       "The d2(...) helper is described below.\n\n#d2[real]",
			wantCount: 1,
			wantCodes: []string{"real"},
		},

		// --- non-#d2 things starting with #d2 ---

		{
			name:      "#d2foo is not a #d2 call",
			src:       "#d2foo[ignored]\n#d2[real]",
			wantCount: 1,
			wantCodes: []string{"real"},
		},
		{
			name:      "#d2 with no following ( or [",
			src:       "the value is #d2 itself; #d2[real]",
			wantCount: 1,
			wantCodes: []string{"real"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blocks, skipped := scanD2Calls(tc.src)
			// None of these are malformed, so none may be reported as
			// a call the scanner recognised and failed to extract.
			// Guards against a "fix" that trades a silent skip for a
			// spurious hard error.
			if len(skipped) != 0 {
				t.Errorf("unexpected skipped call sites: %+v", skipped)
			}
			if len(blocks) != tc.wantCount {
				t.Fatalf("count=%d, want=%d (got %+v)", len(blocks), tc.wantCount, blocks)
			}
			for i, b := range blocks {
				if i >= len(tc.wantCodes) {
					break
				}
				if b.Code != tc.wantCodes[i] {
					t.Errorf("blocks[%d].Code = %q, want %q", i, b.Code, tc.wantCodes[i])
				}
				// And the captured span MUST round-trip — i.e. the
				// substring at [Start:End] starts with the call name
				// and ends with ) or ]. A call found in code context
				// has no hash: #figure(d2(...)[...]) spans from the d.
				span := tc.src[b.Start:b.End]
				wantPrefix := "#d2"
				if b.CodeContext {
					wantPrefix = "d2"
				}
				if !strings.HasPrefix(span, wantPrefix) {
					t.Errorf("blocks[%d] span doesn't start with %s: %q", i, wantPrefix, span)
				}
				last := span[len(span)-1]
				if last != ')' && last != ']' {
					t.Errorf("blocks[%d] span doesn't end with ) or ]: %q", i, span)
				}
			}
		})
	}
}

// Replacing #d2 calls in reverse order must leave the document with
// the right structure: untouched prose between blocks, no overlap,
// and the original number of blocks. This is the property that broke
// in production — verify it directly.
func TestScanD2Calls_ReplacementRoundTrip(t *testing.T) {
	src := "intro #d2[A -> B] mid1 #d2(```\nC -> D\n```) mid2 #d2(layout: \"elk\")[E -> F] tail"
	blocks, _ := scanD2Calls(src)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}

	// Replace each with a placeholder, in reverse, the way Preprocess
	// does. The non-d2 text MUST survive verbatim.
	out := src
	for i := len(blocks) - 1; i >= 0; i-- {
		b := blocks[i]
		out = out[:b.Start] + "<X>" + out[b.End:]
	}
	want := "intro <X> mid1 <X> mid2 <X> tail"
	if out != want {
		t.Errorf("reverse replace produced %q, want %q", out, want)
	}
}

// The scanner must flag a call site it recognised but could not
// extract. Before this existed the block was left in the source and
// typst reported the failure several layers later, naming the wrong
// construct — usually "unclosed delimiter" on a balanced bracket.
func TestScanD2Calls_SkippedSites(t *testing.T) {
	cases := []struct {
		name      string
		src       string
		wantSkips int
	}{
		{
			name:      "unterminated diagram block",
			src:       "#d2[\n  a -> b\n",
			wantSkips: 1,
		},
		{
			name:      "unterminated option list",
			src:       "#d2(layout: \"elk\"\n",
			wantSkips: 1,
		},
		{
			name:      "options with no diagram block",
			src:       "#d2(layout: \"elk\")\n\nProse.",
			wantSkips: 1,
		},
		{
			name:      "code-context call that cannot be extracted",
			src:       "#figure(d2(layout: \"elk\")[\n  a -> b\n, caption: [x])",
			wantSkips: 1,
		},
		{
			// Not a call, so not a failure to extract — mentioning
			// #d2 in prose must stay silent.
			name:      "bare #d2 in prose is not a skipped call",
			src:       "Write your diagram with #d2 and compile.",
			wantSkips: 0,
		},
		{
			// A malformed call inside a comment is still a comment.
			name:      "malformed call inside a Typst comment",
			src:       "// #d2[ never closed\n\nProse.",
			wantSkips: 0,
		},
		{
			name:      "malformed call inside a raw block",
			src:       "```\n#d2[ never closed\n```\n\nProse.",
			wantSkips: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, skipped := scanD2Calls(tc.src)
			if len(skipped) != tc.wantSkips {
				t.Fatalf("skipped=%d, want %d (got %+v)", len(skipped), tc.wantSkips, skipped)
			}
			for _, sk := range skipped {
				if sk.Reason == "" {
					t.Errorf("skip site at %d has no reason", sk.Offset)
				}
				if sk.Offset < 0 || sk.Offset >= len(tc.src) {
					t.Errorf("skip site offset %d out of range for %d-byte source", sk.Offset, len(tc.src))
				}
			}
		})
	}
}

// A call written in code context (inside another call's argument list)
// must be flagged, because its replacement may not carry a leading `#`.
func TestScanD2Calls_CodeContextFlag(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want bool
	}{
		{"markup form", "#d2[a -> b]", false},
		{"markup form inside a content block", "#figure([#d2[a -> b]], caption: [c])", false},
		{"code form inside figure", "#figure(d2[a -> b], caption: [c])", true},
		{"code form with options", "#figure(d2(layout: \"elk\")[a -> b], caption: [c])", true},
		{"code form with a raw body", "#figure(d2(```\na -> b\n```), caption: [c])", true},
		{"markup form in a caption is still markup", "#figure(rect(), caption: [#d2[a -> b]])", false},
		// A stray hash inside an argument list. Typst would reject it;
		// we accept it and drop the hash rather than emit a document
		// that cannot compile.
		{"stray hash in code context", "#figure(#d2[a -> b], caption: [c])", true},
		{"stray hash with options", "#figure(#d2(layout: \"elk\")[a -> b], caption: [c])", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blocks, skipped := scanD2Calls(tc.src)
			if len(skipped) != 0 {
				t.Fatalf("unexpected skips: %+v", skipped)
			}
			if len(blocks) != 1 {
				t.Fatalf("blocks=%d, want 1", len(blocks))
			}
			if blocks[0].CodeContext != tc.want {
				t.Errorf("CodeContext=%v, want %v", blocks[0].CodeContext, tc.want)
			}
		})
	}
}
