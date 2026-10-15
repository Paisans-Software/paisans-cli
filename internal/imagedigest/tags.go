package imagedigest

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
)

// versionTag is a tag shaped like a version: three numbers, an optional v
// before them and an optional suffix after a dash (Eg: 1.15.0-paisans, the
// form a fork's release `1.15.0+paisans` is published under, with the `+`
// written `-` because a tag cannot hold one).
var versionTag = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)(-[0-9A-Za-z.-]+)?$`)

// NewestVersion is the newest version-shaped tag of the repository ref is
// in, empty when it has none. It asks anonymously, as Digest does.
//
// It is for advice, not for planning: apply names it as an example of a
// published tag an operator could pin, so that the example is never a
// version this toolkit wrote down and the registry has since moved past.
// Only the first page the registry lists is read.
func (r Registry) NewestVersion(ref string) (string, error) {
	host, repo, _, err := split(ref)
	if err != nil {
		return "", err
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	f := &fetcher{client: client, base: fmt.Sprintf("https://%s/v2/%s/", host, repo), repo: repo}
	var list struct {
		Tags []string `json:"tags"`
	}
	if err := f.json("tags/list", "application/json", &list); err != nil {
		return "", fmt.Errorf("listing the tags of %s: %w", ref, err)
	}
	return Newest(list.Tags), nil
}

// Newest is the version-shaped tag of tags with the highest version, by its
// numbers, then by its suffix as text; empty when none is version-shaped.
func Newest(tags []string) string {
	best := ""
	var bestKey [3]int
	for _, tag := range tags {
		m := versionTag.FindStringSubmatch(tag)
		if m == nil {
			continue
		}
		var key [3]int
		for i := range key {
			key[i], _ = strconv.Atoi(m[i+1])
		}
		if best == "" || newer(key, bestKey) || (key == bestKey && tag > best) {
			best, bestKey = tag, key
		}
	}
	return best
}

func newer(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}
