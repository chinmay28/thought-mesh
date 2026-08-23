// Package version carries the application version.
//
// The scheme is vMAJOR.MINOR.PATCH where the patch number is the repository's
// commit count — every commit is a patch release, so `v0.1.42` is the 42nd
// commit on the 0.1 line. Major and minor are declared here in source and
// bumped by hand; the patch number can only come from git, which a compiled
// binary has no access to, so it is stamped at link time instead:
//
//	go build -ldflags "-X github.com/chinmay28/thought-mesh/server/internal/version.Patch=$(git rev-list --count HEAD)"
//
// `npm run build` (and `build:server`) does this for you via scripts/version.mjs,
// which is also what the web client's build reads Major/Minor from — keep the
// two constants below in a form that file's regex can still find.
package version

import "strconv"

// Major and minor version. Bump these by hand.
const (
	Major = 0
	Minor = 1
)

// Patch is the repository's commit count, stamped at link time (see the
// package comment). A bare `go build` leaves it at "0": patch 0 means an
// unstamped development build, never a release.
var Patch = "0"

// String renders the full version, `v`-prefixed to match how the project tags
// releases (v0.1.0). This is the one rendering — it's what the CLI prints,
// what /api/health reports, and what the PWA shows.
func String() string {
	return "v" + strconv.Itoa(Major) + "." + strconv.Itoa(Minor) + "." + Patch
}
