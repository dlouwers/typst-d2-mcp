// Package templates carries the house document templates, embedded into
// the binary so they travel with it rather than being COPY'd into the
// image at a fixed path. In hosted mode the server seeds this tree onto
// the data volume on startup (see seedBundledTemplates), which is what
// lets house style change on the volume without an image rebuild — step 2
// of #63.
//
// The embed path mirrors typst's local-package layout under
// <namespace>/<name>/<version>/, so seeding is a straight copy into
// $XDG_DATA_HOME/typst/packages/.
package templates

import "embed"

// FS holds the bundled package tree rooted at the owner namespace, e.g.
// house/templates/0.1.0/{typst.toml,lib.typ}.
//
//go:embed house
var FS embed.FS
