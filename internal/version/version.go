// Package version holds the toolkit's version, the one place it is written,
// and the build identity every record the toolkit writes carries.
package version

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is the toolkit's version, a semantic version with no v:
// MAJOR.MINOR.PATCH for a release, MAJOR.MINOR.PATCH-<pre-release> for a
// pre-release such as 0.3.0-rc.1, and MAJOR.MINOR.PATCH-dev on develop
// between releases. `make bump` sets a release or a pre-release, and a merge
// into main carrying a Version with no matching tag is released as
// v<Version>. Once a release is cut, `make dev` moves develop to the next
// -dev, so a build of develop never reports a release it is not.
const Version = "0.3.0-dev"

// Compare orders two versions, MAJOR.MINOR.PATCH with an optional
// -<pre-release> and no v, as semantic versioning orders them: negative when
// a is older than b, zero when they are the same, positive when a is newer.
// A pre-release is older than its release, and pre-releases of one release
// order by their dot-separated identifiers, numeric ones numerically and
// below alphanumeric ones, a shorter list first when one is a prefix of the
// other: 0.2.0 < 0.3.0-dev < 0.3.0-rc.1 < 0.3.0-rc.2 < 0.3.0-rc.10 < 0.3.0.
// Anything else, build metadata included, is an error, since it cannot be
// ordered.
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
		if c := cmpInt(pa.release[i], pb.release[i]); c != 0 {
			return c, nil
		}
	}
	switch {
	case len(pa.pre) == 0 && len(pb.pre) == 0:
		return 0, nil
	case len(pa.pre) == 0:
		return 1, nil
	case len(pb.pre) == 0:
		return -1, nil
	}
	for i := 0; i < len(pa.pre) && i < len(pb.pre); i++ {
		if c := cmpIdentifier(pa.pre[i], pb.pre[i]); c != 0 {
			return c, nil
		}
	}
	return cmpInt(len(pa.pre), len(pb.pre)), nil
}

// Valid reports whether v is a version Compare can order.
func Valid(v string) bool {
	_, err := parse(v)
	return err == nil
}

// IsPreRelease reports whether v, a valid version, is a pre-release.
func IsPreRelease(v string) bool { return strings.Contains(v, "-") }

// Newer reports whether recorded, the release that last wrote a record, is
// newer than have, this binary's, as Compare orders them. A build of
// develop, X.Y.Z-dev, counts as every pre-release of X.Y.Z, as it meets a
// blueprint's requires_toolkit of one: develop holds every release
// candidate cut from it. A version Compare cannot order is an error.
func Newer(recorded, have string) (bool, error) {
	c, err := Compare(recorded, have)
	if err != nil || c <= 0 {
		return false, err
	}
	core, dev := strings.CutSuffix(have, "-dev")
	return !(dev && IsPreRelease(recorded) && strings.HasPrefix(recorded, core+"-")), nil
}

type parsed struct {
	release [3]int
	pre     []string
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpIdentifier(a, b string) int {
	na, aNum := numeric(a)
	nb, bNum := numeric(b)
	switch {
	case aNum && bNum:
		return cmpInt(na, nb)
	case aNum:
		return -1
	case bNum:
		return 1
	}
	return strings.Compare(a, b)
}

// numeric is the number a pre-release identifier of digits alone holds.
func numeric(id string) (int, bool) {
	for _, c := range id {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(id)
	return n, err == nil
}

func parse(v string) (parsed, error) {
	var out parsed
	bad := fmt.Errorf("%q is not a version, MAJOR.MINOR.PATCH with an optional -<pre-release>", v)
	core, pre, hasPre := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
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
	if !hasPre {
		return out, nil
	}
	for _, id := range strings.Split(pre, ".") {
		if id == "" {
			return out, bad
		}
		for _, c := range id {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-') {
				return out, bad
			}
		}
		if _, isNum := numeric(id); isNum && len(id) > 1 && id[0] == '0' {
			return out, bad
		}
		out.pre = append(out.pre, id)
	}
	return out, nil
}
