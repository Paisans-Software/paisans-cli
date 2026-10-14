// Package version holds the toolkit's version, the one place it is written.
package version

// Version is the toolkit's release version, MAJOR.MINOR.PATCH with no v.
// `make bump` sets it, and a merge into main carrying a Version with no
// matching tag is released as vMAJOR.MINOR.PATCH. Every build of a commit
// reports the Version that commit holds, so a build between releases
// reports the last release cut on its branch.
const Version = "0.0.0"
