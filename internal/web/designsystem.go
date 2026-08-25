package web

import (
	"bytes"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	stormlantern "github.com/dlouwers/stormlantern-design-system"
)

// The design-system assets used to be copied into internal/web/static/vendor
// by scripts/sync-vendor.sh and committed. That worked until it didn't: the
// copy step was manual, nothing recorded which release the bytes came from,
// and this admin UI served ten fixed WCAG failures for a day after they were
// released upstream — with the CI drift check working exactly as designed,
// because it can only fire once the design system's main moves AND something
// pushes here (#80).
//
// They now come from the Go module, so go.mod pins the version, go.sum
// checksums it, and stormlantern.Version can be logged. See
// dlouwers/stormlantern-design-system#21.
//
// The URLs are deliberately unchanged. Templates still link
// /admin/static/vendor/stormlantern-tokens.css and components.js still
// imports ./vendor/sl-*.js — only where the bytes come from moved.

// designSystemFS overlays the design-system module's assets onto this
// repo's own embedded static tree, under the vendor/ prefix the templates
// already use.
//
// Only the files the design system owns are intercepted. vendor/htmx.min.js
// stays local: it is pinned and vendored by hand on purpose, and bumping it
// should be a deliberate act rather than a side effect of a design-system
// release.
type designSystemFS struct{ local fs.FS }

func (d designSystemFS) Open(name string) (fs.File, error) {
	if base, ok := strings.CutPrefix(name, "vendor/"); ok {
		switch {
		case base == "stormlantern-tokens.css":
			return &bytesFile{name: base, data: bytes.NewReader(stormlantern.TokensCSS),
				size: int64(len(stormlantern.TokensCSS))}, nil
		case strings.HasPrefix(base, "sl-") && path.Ext(base) == ".js":
			return stormlantern.Components.Open(base)
		case strings.HasPrefix(base, "lantern-mark") || strings.HasPrefix(base, "lockup"):
			return stormlantern.Assets.Open(base)
		}
	}
	return d.local.Open(name)
}

// bytesFile adapts a byte slice to fs.File. stormlantern.TokensCSS is a
// []byte rather than an fs.FS entry, and http.FileServerFS needs a file it
// can Stat and Seek — Seek being what lets it answer range requests and set
// Content-Length.
type bytesFile struct {
	name string
	data *bytes.Reader
	size int64
}

func (f *bytesFile) Read(p []byte) (int, error)         { return f.data.Read(p) }
func (f *bytesFile) Seek(o int64, w int) (int64, error) { return f.data.Seek(o, w) }
func (f *bytesFile) Close() error                       { return nil }
func (f *bytesFile) Stat() (fs.FileInfo, error)         { return bytesFileInfo{f}, nil }

type bytesFileInfo struct{ f *bytesFile }

func (i bytesFileInfo) Name() string      { return i.f.name }
func (i bytesFileInfo) Size() int64       { return i.f.size }
func (i bytesFileInfo) Mode() fs.FileMode { return 0o444 }
func (i bytesFileInfo) IsDir() bool       { return false }
func (i bytesFileInfo) Sys() any          { return nil }

// ModTime is the zero time on purpose. http.ServeContent omits
// Last-Modified for a zero time rather than inventing one, which is the
// honest answer for bytes compiled into the binary: they have no
// modification time, and a fabricated one would drive conditional requests
// wrongly.
func (i bytesFileInfo) ModTime() time.Time { return time.Time{} }

var _ io.Seeker = (*bytesFile)(nil)
