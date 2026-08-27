package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// DirName maps an identity to the directory that holds its workspace.
//
// It exists because the obvious thing — using the user id directly —
// put a colon in every tenant path, and a colon is the OS path-list
// separator. Tools that take a list of directories split on it and
// silently read nothing: typst's --font-path did exactly that, so
// workspace fonts never loaded and typst substituted without a word
// (#107). There is no escape to apply, either; backslash, percent
// encoding and the environment variable all fail identically. The only
// fix is to never create the character.
//
// This is the single place that decision is made, matching how
// ScopedFS.Resolve is the single place traversal is decided. A rule
// enforced at each call site is a rule that comes back — #107 was
// patched twice at call sites and reappeared within a day.
//
// The encoding keeps the identity readable and adds a hash only when it
// has to. Sanitising alone is not injective — the standard example is
// that "file?" and "*file*" both reduce to "file" — so any name that
// was altered carries a short digest of the original, which restores
// uniqueness without making every directory unreadable. An operator
// looking at a volume still sees whose workspace is whose.
func DirName(userID string) string {
	// No special case for the empty id: giving it a friendly name would
	// collide with a real identity that happens to have that name, which
	// is the exact non-injectivity this function exists to avoid. It
	// falls through and gets a hashed form like anything else unsafe.
	safe := sanitiseSegment(userID)
	if safe == userID {
		return safe // already safe: leave it exactly as it is
	}
	sum := sha256.Sum256([]byte(userID))
	return safe + "-" + hex.EncodeToString(sum[:])[:8]
}

// safeSegmentRunes is the alphabet a path segment may use: unreserved
// on every filesystem this runs on, and free of every character that
// means something to a shell, a path list or a URL.
func sanitiseSegment(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	// A segment that is empty, or that a filesystem treats specially,
	// is not a name. Fall through to the hashed form by returning
	// something that cannot equal the input.
	if out == "" || out == "." || out == ".." {
		return "id"
	}
	return out
}
