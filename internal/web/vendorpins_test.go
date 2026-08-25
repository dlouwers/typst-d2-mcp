package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// The manifest is only worth having if it cannot drift from the file it
// describes. Swapping htmx.min.js for a different build without updating the
// manifest is exactly the change this must catch — it is how the repo ended
// up unable to name its own htmx version in the first place.
func TestVendorPins_MatchTheVendoredBytes(t *testing.T) {
	pins, err := VendorPins()
	if err != nil {
		t.Fatalf("load pins: %v", err)
	}
	if len(pins) == 0 {
		t.Fatal("no pins recorded — the test is not testing anything")
	}

	for name, pin := range pins {
		data, err := staticFS.ReadFile("static/" + name)
		if err != nil {
			t.Errorf("%s is pinned but not embedded: %v", name, err)
			continue
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != pin.SHA256 {
			t.Errorf("%s: sha256 %s, manifest says %s.\n"+
				"The file changed without the manifest. Update vendorpins.json — "+
				"including the version — or restore the pinned bytes from %s",
				name, got[:16], pin.SHA256[:16], pin.URL)
		}
		if pin.Version == "" || pin.Package == "" || pin.URL == "" {
			t.Errorf("%s: incomplete pin %+v", name, pin)
		}
	}
}

// htmx embeds its own version in the bundle, and where that exists it is the
// stronger check: it ties the manifest to what the library reports about
// itself rather than merely to bytes we hashed.
func TestVendorPins_HTMXSelfReportedVersionAgrees(t *testing.T) {
	pins, err := VendorPins()
	if err != nil {
		t.Fatalf("load pins: %v", err)
	}
	pin, ok := pins["vendor/htmx.min.js"]
	if !ok {
		t.Fatal("vendor/htmx.min.js is not pinned")
	}
	data, err := staticFS.ReadFile("static/vendor/htmx.min.js")
	if err != nil {
		t.Fatalf("read htmx: %v", err)
	}

	m := regexp.MustCompile(`version:"([0-9]+\.[0-9]+\.[0-9]+)"`).FindSubmatch(data)
	if m == nil {
		t.Fatal("htmx.min.js no longer carries a version: string — if upstream dropped it, " +
			"delete this test and rely on the manifest hash")
	}
	if got := string(m[1]); got != pin.Version {
		t.Errorf("htmx reports %s, manifest pins %s", got, pin.Version)
	}
}

// A vendored asset nobody records is one that will rot unnoticed — which is
// the whole history here.
func TestVendorPins_CoverEveryHandVendoredAsset(t *testing.T) {
	pins, err := VendorPins()
	if err != nil {
		t.Fatalf("load pins: %v", err)
	}
	err = fs.WalkDir(staticFS, "static/vendor", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := strings.TrimPrefix(p, "static/")
		if _, ok := pins[name]; !ok {
			t.Errorf("%s is vendored but not recorded in vendorpins.json", name)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk static/vendor: %v", err)
	}
}
