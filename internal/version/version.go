// Package version holds the toolkit's version, the one place it is written.
package version

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is the toolkit's release version, MAJOR.MINOR.PATCH with no v.
// `make bump` sets it, and a merge into main carrying a Version with no
// matching tag is released as vMAJOR.MINOR.PATCH. Every build of a commit
// reports the Version that commit holds, so a build between releases
// reports the last release cut on its branch.
const Version = "0.2.0"

// Compare orders two releases, MAJOR.MINOR.PATCH with no v: negative when a
// is older than b, zero when they are the same release, positive when a is
// newer. Anything else is an error, since it cannot be ordered.
func Compare(a, b string) (int, error) {
	pa, err := parse(a)
	if err != nil {
		return 0, err
	}
	pb, err := parse(b)
	if err != nil {
		return 0, err
	}
	for i := range pa {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1, nil
			}
			return 1, nil
		}
	}
	return 0, nil
}

func parse(v string) ([3]int, error) {
	var out [3]int
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, fmt.Errorf("%q is not a release, MAJOR.MINOR.PATCH", v)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p != strconv.Itoa(n) {
			return out, fmt.Errorf("%q is not a release, MAJOR.MINOR.PATCH", v)
		}
		out[i] = n
	}
	return out, nil
}
