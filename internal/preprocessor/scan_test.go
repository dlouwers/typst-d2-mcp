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
			blocks := scanD2Calls(tc.src)
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
				// substring at [Start:End] starts with #d2 and ends
				// with ) or ].
				span := tc.src[b.Start:b.End]
				if !strings.HasPrefix(span, "#d2") {
					t.Errorf("blocks[%d] span doesn't start with #d2: %q", i, span)
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
	blocks := scanD2Calls(src)
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
