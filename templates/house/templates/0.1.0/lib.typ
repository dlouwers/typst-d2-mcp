// House document templates.
//
// Imported as `@house/templates:0.1.0`, where `house` is the namespace
// typst resolves from $XDG_DATA_HOME/typst/packages/<namespace>/. That
// namespace is the *owner* slug: today there is one, baked into the
// image; later it is per-organisation, materialised per request. The
// import path is what ends up inside people's documents, so it is
// deliberately already organisation-shaped.
//
// Two shapes on purpose:
//
//   report — owns the look. The author writes whatever content they
//            like and it comes out consistent.
//
//   adr    — owns the *structure* too. Sections are arguments, not
//            headings the author remembers to write, so every ADR has
//            the same ones in the same order.
//
// Styling only ever comes from here. A document that overrides fonts or
// colours has opted out of the house style, which defeats the point.
//
// One exception, and it is deliberate: `numbering:`. Cross-referencing a
// heading with `@label` is impossible in typst without it, so refusing
// the argument would not have kept documents consistent — it would have
// sent authors to `#set heading(...)` in their own documents, which is
// worse. The argument is bounded: it decides whether headings are
// numbered and nothing else.

#let _accent = rgb("#1f5673")
#let _muted = rgb("#5b6b73")

// Page geometry suits A4 portrait with diagrams: generous margins so a
// wide `#d2` block has somewhere to breathe rather than touching the
// edge of the page.
#let _page-setup(numbering: none, body) = {
  // Heading numbering is the ONE thing a document may ask this template
  // to change, and it is here because typst forces the choice: `@ref`
  // to a heading hard-errors without numbering, and the compiler's own
  // hint is `#set heading(numbering: "1.")` — precisely the restyling
  // these templates forbid. A caller following the compiler silently
  // opted out of the house style; one following the instructions lost
  // cross-references. Neither is a good outcome, so numbering became an
  // argument rather than a rule to break.
  //
  // It is not a precedent for styling knobs generally. Everything else
  // — fonts, colours, page geometry, the type scale — still comes from
  // here and only from here.
  set heading(numbering: numbering)
  set page(
    paper: "a4",
    margin: (top: 2.5cm, bottom: 2.5cm, x: 2.2cm),
    numbering: "1",
    number-align: center,
  )
  set text(size: 10.5pt, lang: "en")
  set par(justify: true, leading: 0.7em)

  // The number is printed, not just counted. Rendering `it.body` alone
  // meant `numbering:` enabled @label references that resolved to
  // "Section 8" on a page where nothing was numbered 8 — a document
  // that compiled cleanly and read as nonsense. An agent caught it by
  // looking at the rasterised page; the compiler was perfectly happy.
  let _numbered(it, size, weight, colour) = block(
    above: if it.level == 1 { 1.6em } else { 1.2em },
    below: if it.level == 1 { 0.8em } else { 0.6em },
    text(size: size, weight: weight, fill: colour)[
      #if it.numbering != none [#counter(heading).display(it.numbering) #h(0.35em)]
      #it.body
    ],
  )
  show heading.where(level: 1): it => _numbered(it, 15pt, 700, _accent)
  show heading.where(level: 2): it => _numbered(it, 12pt, 600, black)
  show link: it => text(fill: _accent, it)
  show raw.where(block: false): it => box(
    fill: luma(240), inset: (x: 3pt), outset: (y: 3pt), radius: 2pt, it,
  )

  // Diagrams are figures: centred, with the caption styled down so it
  // reads as a label rather than competing with body text.
  show figure.caption: it => text(size: 9pt, fill: _muted, it)

  body
}

#let _title-block(title, subtitle, author, date) = {
  // Titles are never justified or hyphenated: body justification is
  // fine, but a hyphenated title ("machine ac-cess") looks like a fault.
  set par(justify: false)
  set text(hyphenate: false)
  block(width: 100%, inset: (bottom: 1.2em))[
    #text(size: 24pt, weight: 800, fill: _accent, title)
    #if subtitle != none [
      #linebreak()
      #text(size: 13pt, fill: _muted, subtitle)
    ]
    #v(0.4em)
    #line(length: 100%, stroke: 0.8pt + _accent)
    #v(0.2em)
    #text(size: 9.5pt, fill: _muted)[
      #if author != none [#author #h(1em)]
      #date
    ]
  ]
}

/// A general technical document: title block, then whatever the author
/// writes. Use this when the content's shape is the author's choice.
#let report(
  title: "Untitled",
  subtitle: none,
  author: none,
  date: datetime.today().display("[year]-[month]-[day]"),
  numbering: none,
  body,
) = {
  show: _page-setup.with(numbering: numbering)
  _title-block(title, subtitle, author, date)
  body
}

/// An architecture decision record. The sections are arguments rather
/// than headings, so every ADR carries the same ones, in the same order,
/// whoever wrote it.
///
/// The first section is `background:` rather than `context:` because
/// `context` is a reserved word in Typst and cannot be a parameter name.
/// The heading still reads "Context", which is the convention people
/// expect to see.
///
/// Use it as `#show: adr.with(...)`, which hands the rest of the
/// document over as the trailing `body`; anything written there lands
/// after the fixed sections, as an appendix. `body` is positional
/// because that is how a show rule passes it — a named parameter would
/// reject the document.
#let adr(
  title: "Untitled decision",
  number: none,
  status: "Proposed",
  date: datetime.today().display("[year]-[month]-[day]"),
  background: [],
  decision: [],
  consequences: [],
  alternatives: none,
  numbering: none,
  body,
) = {
  show: _page-setup.with(numbering: numbering)

  let heading-text = if number != none [ADR #number: #title] else [#title]
  _title-block(heading-text, none, none, date)

  block(
    fill: luma(245), inset: 8pt, radius: 3pt, width: 100%,
    text(size: 9.5pt)[*Status:* #status],
  )

  heading(level: 1)[Context]
  background
  heading(level: 1)[Decision]
  decision
  heading(level: 1)[Consequences]
  consequences
  if alternatives != none {
    heading(level: 1)[Alternatives considered]
    alternatives
  }
  body
}
