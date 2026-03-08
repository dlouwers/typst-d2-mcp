package preprocessor

import (
	"strings"
	"testing"

	"github.com/dlouwers/typst-d2-mcp/internal/d2"
)

func TestExtractD2Calls(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantLen  int
		wantCode []string
	}{
		{
			name:    "no d2 blocks",
			content: "# Hello World\n\nSome text.",
			wantLen: 0,
		},
		{
			name:     "single d2 block",
			content:  "#d2[\n  x -> y\n]",
			wantLen:  1,
			wantCode: []string{"\n  x -> y\n"},
		},
		{
			name:     "d2 block with options",
			content:  "#d2(layout: \"elk\", theme: \"200\")[\n  x -> y\n]",
			wantLen:  1,
			wantCode: []string{"\n  x -> y\n"},
		},
		{
			name: "multiple d2 blocks",
			content: `#d2[
  a -> b
]

Some text.

#d2(sketch: "true")[
  c -> d
]`,
			wantLen:  2,
			wantCode: []string{"\n  a -> b\n", "\n  c -> d\n"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks := extractD2Calls(tt.content)
			if len(blocks) != tt.wantLen {
				t.Errorf("extractD2Calls() got %d blocks, want %d", len(blocks), tt.wantLen)
			}
			for i, block := range blocks {
				if i < len(tt.wantCode) && block.Code != tt.wantCode[i] {
					t.Errorf("extractD2Calls() block[%d].Code = %q, want %q", i, block.Code, tt.wantCode[i])
				}
			}
		})
	}
}

func TestParseOptions(t *testing.T) {
	tests := []struct {
		name    string
		options string
		want    d2.Options
	}{
		{
			name:    "empty options",
			options: "",
			want:    d2.Options{},
		},
		{
			name:    "single option",
			options: `layout: "elk"`,
			want:    d2.Options{"layout": "elk"},
		},
		{
			name:    "multiple options",
			options: `layout: "elk", theme: "200", sketch: "true"`,
			want: d2.Options{
				"layout": "elk",
				"theme":  "200",
				"sketch": "true",
			},
		},
		{
			name:    "options with quotes",
			options: `theme: '100'`,
			want:    d2.Options{"theme": "100"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseOptions(tt.options)
			if len(got) != len(tt.want) {
				t.Errorf("parseOptions() got %d options, want %d", len(got), len(tt.want))
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("parseOptions()[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestSvgToTypstImage(t *testing.T) {
	tests := []struct {
		name    string
		svg     string
		options d2.Options
		want    string
	}{
		{
			name:    "simple svg",
			svg:     "<svg>test</svg>",
			options: d2.Options{},
			want:    `#image(decode64("PHN2Zz50ZXN0PC9zdmc+"), format: "svg")`,
		},
		{
			name:    "svg with padding",
			svg:     "<svg>test</svg>",
			options: d2.Options{"pad": "10pt"},
			want:    `#pad(10pt, #image(decode64("PHN2Zz50ZXN0PC9zdmc+"), format: "svg"))`,
		},
		{
			name:    "svg with none padding",
			svg:     "<svg>test</svg>",
			options: d2.Options{"pad": "none"},
			want:    `#image(decode64("PHN2Zz50ZXN0PC9zdmc+"), format: "svg")`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := svgToTypstImage(tt.svg, tt.options)
			if got != tt.want {
				t.Errorf("svgToTypstImage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAddBasedImport(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "no imports",
			content: "= Hello\n\nSome text.",
			want:    `#import "@preview/based:0.2.0": decode64` + "\n= Hello\n\nSome text.",
		},
		{
			name:    "existing import",
			content: "#import \"foo.typ\": bar\n\n= Hello",
			want:    "#import \"foo.typ\": bar\n" + `#import "@preview/based:0.2.0": decode64` + "\n\n= Hello",
		},
		{
			name:    "already has based import",
			content: `#import "@preview/based:0.2.0": decode64` + "\n\n= Hello",
			want:    `#import "@preview/based:0.2.0": decode64` + "\n\n= Hello",
		},
		{
			name:    "multiple imports",
			content: "#import \"a.typ\": x\n#import \"b.typ\": y\n\n= Hello",
			want:    "#import \"a.typ\": x\n#import \"b.typ\": y\n" + `#import "@preview/based:0.2.0": decode64` + "\n\n= Hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := addBasedImport(tt.content)
			if got != tt.want {
				t.Errorf("addBasedImport() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractD2CallsPreservesPositions(t *testing.T) {
	content := `Some prefix text
#d2[
  a -> b
]
Middle text
#d2[
  c -> d
]
Suffix text`

	blocks := extractD2Calls(content)
	if len(blocks) != 2 {
		t.Fatalf("Expected 2 blocks, got %d", len(blocks))
	}

	// Verify positions
	if content[blocks[0].Start:blocks[0].End] != "#d2[\n  a -> b\n]" {
		t.Errorf("Block 0 position incorrect: %q", content[blocks[0].Start:blocks[0].End])
	}

	if content[blocks[1].Start:blocks[1].End] != "#d2[\n  c -> d\n]" {
		t.Errorf("Block 1 position incorrect: %q", content[blocks[1].Start:blocks[1].End])
	}
}

func TestExtractD2CallsWithNestedBrackets(t *testing.T) {
	// D2 code can contain nested braces
	content := `#d2[
  user: User {
    shape: person
  }
]`

	blocks := extractD2Calls(content)
	if len(blocks) != 1 {
		t.Fatalf("Expected 1 block, got %d", len(blocks))
	}

	if !strings.Contains(blocks[0].Code, "shape: person") {
		t.Errorf("Code doesn't contain expected content: %q", blocks[0].Code)
	}
}
