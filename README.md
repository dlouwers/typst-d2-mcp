# Typst D2 Preprocessor

Render [D2 diagrams](https://d2lang.com) in [Typst](https://typst.app) documents **without any filesystem clutter** or intermediate files.

## Features

- ✅ **Zero filesystem clutter** - No intermediate `.svg` files created
- ✅ **Full D2 feature support** - All layouts (ELK, TALA, dagre), themes, sketch mode
- ✅ **Inline syntax** - D2 code embedded directly in `.typ` files
- ✅ **Simple workflow** - One command replaces `typst compile`

## Quick Start

### Installation

```bash
# 1. Clone the repository
git clone https://github.com/dlouwers/typst-d2-mcp.git
cd typst-d2-mcp

# 2. Make executable
chmod +x typst-d2

# 3. Verify D2 is installed
d2 --version
# If not: curl -fsSL https://d2lang.com/install.sh | sh -s --
```

### Usage

**Your Typst file (document.typ):**

```typst
= Architecture Diagram

#d2[
  client -> server -> database
]

#d2(layout: "elk", theme: "200")[
  user: User {shape: person}
  app: Application
  user -> app: Uses
]
```

**Compile:**

```bash
./typst-d2 compile document.typ
# ✅ Creates document.pdf with embedded diagrams
```

## How It Works

1. **Parse** - Scans your `.typ` file for `#d2[...]` blocks
2. **Extract** - Pulls out D2 code and options from each block
3. **Render** - For each diagram, calls `d2 - -` (stdin→stdout streaming)
4. **Encode** - Converts SVG to base64
5. **Import** - Adds `#import "@preview/based:0.2.0": decode64` at the top
6. **Replace** - Substitutes `#d2[...]` with `#image(decode64("..."), format: "svg")`
7. **Compile** - Runs `typst compile` on the processed document
8. **Cleanup** - Deletes temporary `.typ` file, keeps only your original + PDF

**Result:** Your PDF contains embedded SVGs, no leftover files, clean filesystem.

## Requirements

- **Python 3.6+** (for preprocessor script)
- **D2 CLI** installed and in PATH: https://d2lang.com/tour/install
- **Typst 0.14.2+**: https://github.com/typst/typst
- **Typst `based` package**: Automatically imported (no manual setup needed)

## Syntax Reference

### Basic Diagram

```typst
#d2[
  x -> y -> z
]
```

### With Options

```typst
#d2(layout: "elk", theme: "200", sketch: "true")[
  direction: right
  
  user: User {
    shape: person
  }
  
  app: Application {
    ui: Web Interface
    api: REST API
  }
  
  user -> app.ui: Browse
]
```

### Available Options

| Option | Values | Default | Description |
|--------|--------|---------|-------------|
| `layout` | `"elk"`, `"tala"`, `"dagre"` | `"elk"` | Layout engine |
| `theme` | `"0"`-`"200"` | default | Theme ID |
| `sketch` | `"true"`, `"false"` | `"false"` | Hand-drawn style |
| `center` | `"true"`, `"false"` | `"false"` | Center in viewbox |
| `scale` | number or `"auto"` | `"auto"` | Scale factor |
| `pad` | Typst length (e.g., `"10pt"`) | `none` | Padding around diagram |

## Examples

See `example.typ` for a complete demo with multiple diagrams, including:
- Simple connections
- Styled diagrams with ELK layout, themes, and sketch mode
- Complex architecture with multi-level containers

Compile it:
```bash
./typst-d2 compile example.typ
```

## Technical Details

### Base64 Encoding with `based` Package

The preprocessor uses the [`based`](https://typst.app/universe/package/based) package to decode base64-encoded SVG data:

```typst
#import "@preview/based:0.2.0": decode64

#image(decode64("PD94bWwgdmVyc2lvbj0iMS4wIj..."), format: "svg")
```

This approach:
- ✅ Avoids escaping issues with raw SVG strings
- ✅ Works reliably with all SVG content
- ✅ Uses an official Typst package (no custom code)
- ✅ Handles binary data correctly

See [IMPLEMENTATION.md](IMPLEMENTATION.md) for detailed technical documentation.

## Comparison to Alternatives

| Feature | typst-d2 (this) | Manual workflow | WASM plugin |
|---------|----------------|-----------------|-------------|
| **Setup** | Install script + D2 | Install D2 | N/A (impossible) |
| **Syntax** | `#d2[code]` | `#image("out.svg")` | `#d2[code]` |
| **Filesystem** | ✅ Clean | ❌ SVG files everywhere | ✅ Clean |
| **D2 Features** | ✅ 100% | ✅ 100% | ❌ 0% |
| **Build** | `typst-d2 compile` | `d2 ... && typst compile` | `typst compile` |

## Troubleshooting

### "d2 command not found"

Install D2:
```bash
curl -fsSL https://d2lang.com/install.sh | sh -s --
```

### "No D2 diagrams found"

Make sure you're using the `#d2[...]` syntax (not `#import "lib.typ"`).

### Python version issues

Requires Python 3.6+:
```bash
python3 --version
```

## Limitations

- **No watch mode yet** - Currently only supports single compilation
- **No incremental builds** - Every compile re-renders all diagrams
- **Python dependency** - Requires Python 3 installed

## Future Improvements

- [ ] Watch mode with smart caching
- [ ] Incremental rendering (only changed diagrams)
- [ ] Parallel diagram rendering
- [ ] Native binary (no Python dependency)
- [ ] Typst package integration

## Contributing

Contributions welcome! Please open an issue or PR.

## License

MIT License - see [LICENSE](LICENSE) for details.

## Credits

- **D2**: https://github.com/terrastruct/d2
- **Typst**: https://github.com/typst/typst
- **based package**: https://github.com/EpicEricEE/typst-based

## Related Documentation

- [QUICKSTART.md](QUICKSTART.md) - Quick start guide
- [IMPLEMENTATION.md](IMPLEMENTATION.md) - Technical implementation details
