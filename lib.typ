// D2 diagram renderer for Typst
// Uses preprocessing to embed D2 diagrams without filesystem clutter

#let d2(
  code,
  layout: "elk",
  theme: none,
  sketch: false,
  pad: none,
  center: false,
  scale: auto,
) = {
  // This is a placeholder that gets replaced during preprocessing
  // The preprocessor will:
  // 1. Extract this D2 code block
  // 2. Render it with: d2 --layout=elk --theme=200 - -
  // 3. Capture the SVG output
  // 4. Replace this function call with: image.decode(...)
  
  // For now, show an error if used without preprocessor
  panic("D2 diagrams require the typst-d2 preprocessor. Run: typst-d2 compile " + repr(code))
}
