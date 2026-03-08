# Quick Start Guide

## Installation

```bash
# Option 1: Homebrew (recommended)
brew install dlouwers/tap/typst-d2-prep

# Option 2: Build from source
git clone https://github.com/dlouwers/typst-d2-mcp.git
cd typst-d2-mcp
go build -o typst-d2-prep ./cmd/typst-d2-prep

# Option 3: Download pre-built binary
# https://github.com/dlouwers/typst-d2-mcp/releases

# Verify D2 is installed
d2 --version
# If not: curl -fsSL https://d2lang.com/install.sh | sh -s --
```

## Usage

### Your Typst File (document.typ)

```typst
= Architecture Diagram

#d2[
  client -> server -> database
]

#d2(layout: "elk", theme: "0")[
  user: User {shape: person}
  app: Application
  user -> app: Uses
]
```

### Compile

```bash
typst-d2-prep compile document.typ
# ✅ Creates document.pdf with embedded diagrams
# ✅ No intermediate files
# ✅ No filesystem clutter
```

## That's It!

**What happens behind the scenes:**

1. Extracts D2 code blocks
2. Renders each via `d2 - -` (streaming)
3. Encodes SVG as base64
4. Adds `#import "@preview/based:0.2.0": decode64`
5. Replaces `#d2[...]` with `#image(decode64("..."), format: "svg")`
6. Compiles with `typst compile`
7. Cleans up temp files

**Your filesystem stays clean:**
```
Before:          After:
document.typ     document.typ
                 document.pdf  ← Contains embedded SVGs
```

**No leftover files. No manual cleanup.**
