// Package version holds the toolkit's version, the one place it is written,
// and the build identity every record the toolkit writes carries.
package version

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is the toolkit's version, MAJOR.MINOR.PATCH with no v, or
// MAJOR.MINOR.PATCH-dev on develop between releases. `make bump` sets a
// release, and a merge into main carrying a Version with no matching tag is
// released as vMAJOR.MINOR.PATCH. Once a release is cut, `make dev` moves
// develop to the next minor's -dev, so a build of develop never reports a
// release it is not.
const Version = "0.3.0-dev"

// Compare orders two versions, MAJOR.MINOR.PATCH or MAJOR.MINOR.PATCH-dev
// with no v: negative when a is older than b, zero when they are the same,
// positive when a is newer. A -dev version is the one before its release,
// and after every release before that, as semantic versioning orders a
// pre-release: 0.2.0 < 0.3.0-dev < 0.3.0. Anything else is an error, since
// it cannot be ordered.
func Compare(a, b string) (int, error) {
	pa, err := parse(a)
	if err != nil {
		return 0, err
	}
	pb, err := parse(b)
	if err != nil {
		return 0, err
	}
	for i := range pa.release {
		if pa.release[i] != pb.release[i] {
			if pa.release[i] < pb.release[i] {
				return -1, nil
			}
			return 1, nil
		}
	}
	switch {
	case pa.dev == pb.dev:
		return 0, nil
	case pa.dev:
		return -1, nil
	}
	return 1, nil
}

// Valid reports whether v is a version Compare can order.
func Valid(v string) bool {
	_, err := parse(v)
	return err == nil
}

type parsed struct {
	release [3]int
	dev     bool
}

func parse(v string) (parsed, error) {
	var out parsed
	bad := fmt.Errorf("%q is not a version, MAJOR.MINOR.PATCH or MAJOR.MINOR.PATCH-dev", v)
	rest, dev := strings.CutSuffix(v, "-dev")
	out.dev = dev
	parts := strings.Split(rest, ".")
	if len(parts) != 3 {
		return out, bad
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p != strconv.Itoa(n) {
			return out, bad
		}
		out.release[i] = n
	}
	return out, nil
}
