package web

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// htmx is vendored by hand — deliberately. It interprets every server
// response, so bumping it should be a conscious act rather than a side effect
// of some other sync. That is why #80 left it alone when the design-system
// assets moved to a Go module.
//
// Deliberate and unrecorded are not the same thing, though, and until #85
// nothing here said which htmx this binary served. Establishing it meant
// hashing the file against candidate releases from the registry — not a
// question a codebase should make anyone answer by experiment.
//
// Dependabot cannot close this gap and never could: it reads manifests and
// lockfiles, and a vendored .js file appears in neither. Declaring htmx in a
// package.json would produce a PR bumping a version string while leaving the
// vendored file untouched — a green PR that changes nothing.
//
// So the manifest records package, version and the SHA-256 of the exact
// bytes. The hash is what stops the record drifting from the file: replacing
// the asset without updating the manifest fails
// TestVendorPins_MatchTheVendoredBytes.
//
// The design system solved the same problem by becoming a Go module (#80),
// where go.mod and go.sum do this. htmx is not a Go module, so it gets the
// smallest thing that answers the same question.

//go:embed vendorpins.json
var vendorPinsJSON []byte

// VendorPin is one hand-vendored front-end asset.
type VendorPin struct {
	Package string `json:"package"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	// URL is where these exact bytes came from, so a re-fetch needs no
	// guessing about which CDN path or filename a package uses.
	URL string `json:"url"`
}

// VendorPins returns the hand-vendored assets, keyed by their path under
// static/. It is the answer to "which htmx is this?".
func VendorPins() (map[string]VendorPin, error) {
	var pins map[string]VendorPin
	if err := json.Unmarshal(vendorPinsJSON, &pins); err != nil {
		return nil, fmt.Errorf("vendor pins: %w", err)
	}
	return pins, nil
}
