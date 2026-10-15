package imagedigest

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// A track is a reference whose tag is a pattern rather than a tag (Eg:
// ghcr.io/pocket-id/pocket-id:v2.*.*), which apply resolves to the newest tag
// the registry publishes that matches it. In a pattern each `*` matches one
// run of digits and everything else is literal, and a tag matches when the
// whole tag does, so `3.0.*-alpine` matches 3.0.11-alpine and neither
// 3.0.11-alpine3.20 nor 3.1.0-alpine: a pattern says exactly how far a track
// may move. docs/specs/2026-10-13-images-follow-tracks.md has the reasoning.

// IsTrack reports whether ref names a track: its tag holds a `*`.
func IsTrack(ref string) bool {
	_, pattern := trackParts(ref)
	return strings.Contains(pattern, "*")
}

// trackParts is ref's name and tag, both empty when ref names a digest.
func trackParts(ref string) (name, tag string) {
	ref = strings.TrimSpace(ref)
	if strings.Contains(ref, "@") {
		return "", ""
	}
	slash, colon := strings.LastIndex(ref, "/"), strings.LastIndex(ref, ":")
	if colon <= slash {
		return ref, ""
	}
	return ref[:colon], ref[colon+1:]
}

// Pattern is a track's tag pattern, compiled.
type Pattern struct {
	text string
	re   *regexp.Regexp
}

// Compile reads a tag pattern. It must hold at least one `*`, and every
// literal character must be one a tag may hold.
func Compile(pattern string) (Pattern, error) {
	if !strings.Contains(pattern, "*") {
		return Pattern{}, fmt.Errorf("%q has no *, so it names one tag rather than a track", pattern)
	}
	if strings.Contains(pattern, "**") {
		return Pattern{}, fmt.Errorf("%q has two * together, which would match one number two ways", pattern)
	}
	var b strings.Builder
	b.WriteString("^")
	for i, part := range strings.Split(pattern, "*") {
		if i > 0 {
			b.WriteString("([0-9]+)")
		}
		if !tagText.MatchString(part) {
			return Pattern{}, fmt.Errorf("%q holds a character no tag may", pattern)
		}
		b.WriteString(regexp.QuoteMeta(part))
	}
	b.WriteString("$")
	return Pattern{text: pattern, re: regexp.MustCompile(b.String())}, nil
}

// tagText is the characters a tag may hold, the distribution spec's
// [a-zA-Z0-9_.-], allowing an empty run between two `*`.
var tagText = regexp.MustCompile(`^[A-Za-z0-9_.-]*$`)

// String is the pattern as written.
func (p Pattern) String() string { return p.text }

// Prefix is the pattern's literal text before its first `*`, which is how far
// a track is fixed: `v2.*.*` cannot leave v2.
func (p Pattern) Prefix() string {
	prefix, _, _ := strings.Cut(p.text, "*")
	return prefix
}

// numbers is what each `*` matched in tag, nil when tag does not match.
func (p Pattern) numbers(tag string) []int {
	m := p.re.FindStringSubmatch(tag)
	if m == nil {
		return nil
	}
	out := make([]int, len(m)-1)
	for i, s := range m[1:] {
		n, err := strconv.Atoi(s)
		if err != nil {
			return nil
		}
		out[i] = n
	}
	return out
}

// Matches reports whether tag is on the track.
func (p Pattern) Matches(tag string) bool { return p.numbers(tag) != nil }

// Newest is the tag of tags on the track whose numbers, read left to right,
// are highest; empty when none is on it. Two tags with the same numbers
// differ only in leading zeros, and the first listed wins.
func (p Pattern) Newest(tags []string) string {
	best := ""
	var bestKey []int
	for _, tag := range tags {
		key := p.numbers(tag)
		if key == nil {
			continue
		}
		if best == "" || higher(key, bestKey) {
			best, bestKey = tag, key
		}
	}
	return best
}

func higher(a, b []int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// TrackPattern is ref's tag pattern, for a ref that IsTrack.
func TrackPattern(ref string) (Pattern, error) {
	_, tag := trackParts(ref)
	return Compile(tag)
}

// OnTrack reports whether resolved, a reference pinned or bare, is a build of
// track: the same name, and a tag the pattern matches. It is how apply tells
// a build it may keep from one the configuration no longer names.
func OnTrack(track, resolved string) bool {
	name, _ := trackParts(track)
	at := strings.Index(resolved, "@")
	if at >= 0 {
		resolved = resolved[:at]
	}
	gotName, tag := trackParts(resolved)
	if gotName != name || tag == "" {
		return false
	}
	p, err := TrackPattern(track)
	return err == nil && p.Matches(tag)
}

// Track is the newest tag on the track ref names, as a reference with that
// tag in place of the pattern (Eg: ghcr.io/pocket-id/pocket-id:v2.15.1). It
// lists every tag of the repository, anonymously, as Digest asks. A track the
// registry publishes no tag for is ErrNotFound, naming the newest
// version-shaped tag it does publish.
func (r Registry) Track(ref string) (string, error) {
	pattern, err := TrackPattern(ref)
	if err != nil {
		return "", err
	}
	tags, err := r.Tags(ref)
	if err != nil {
		return "", err
	}
	name, _ := trackParts(ref)
	tag := pattern.Newest(tags)
	if tag == "" {
		hint := ""
		if newest := Newest(tags); newest != "" {
			hint = fmt.Sprintf("; its newest version tag is %s", newest)
		}
		return "", fmt.Errorf("%s publishes no tag matching %s%s: %w", name, pattern, hint, ErrNotFound)
	}
	return name + ":" + tag, nil
}

// pageSize is how many tags one listing asks for. Docker Hub answers a
// listing without one with 100 at a time, and an image such as postgres has
// thousands.
const pageSize = 1000

// Tags is every tag of the repository ref is in, following the registry's
// pagination (the distribution spec's Link header) to the last page.
func (r Registry) Tags(ref string) ([]string, error) {
	name, _ := trackParts(ref)
	host, repo := locate(name)
	if repo == "" {
		return nil, fmt.Errorf("%s is not a reference this lookup can read", ref)
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	base := fmt.Sprintf("https://%s/v2/%s/", host, repo)
	f := &fetcher{client: client, base: base, repo: repo}
	var out []string
	next := fmt.Sprintf("tags/list?n=%d", pageSize)
	for pages := 0; next != ""; pages++ {
		if pages == 100 {
			return nil, fmt.Errorf("listing the tags of %s: more than %d pages", repo, pages)
		}
		var list struct {
			Tags []string `json:"tags"`
		}
		link, err := f.jsonLink(next, &list)
		if err != nil {
			if errors.Is(err, errNotFoundStatus) {
				return nil, fmt.Errorf("listing the tags of %s: %w", repo, ErrNotFound)
			}
			return nil, fmt.Errorf("listing the tags of %s: %w", repo, err)
		}
		out = append(out, list.Tags...)
		next, err = nextPage(base, link)
		if err != nil {
			return nil, fmt.Errorf("listing the tags of %s: %w", repo, err)
		}
	}
	return out, nil
}

// nextPage is the path, relative to base, of the page a Link header names as
// next, empty when it names none.
func nextPage(base, link string) (string, error) {
	for _, part := range strings.Split(link, ",") {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		start, end := strings.Index(part, "<"), strings.Index(part, ">")
		if start < 0 || end < start {
			return "", fmt.Errorf("a Link header the toolkit cannot read: %s", link)
		}
		target, err := url.Parse(part[start+1 : end])
		if err != nil {
			return "", err
		}
		b, _ := url.Parse(base)
		abs := b.ResolveReference(target).String()
		rel, ok := strings.CutPrefix(abs, base)
		if !ok {
			// A path from the root names the repository's own listing.
			rel, ok = strings.CutPrefix(abs, strings.TrimSuffix(base, "/")+"/")
		}
		if !ok {
			return "", fmt.Errorf("the next page %s is outside %s", abs, base)
		}
		return rel, nil
	}
	return "", nil
}

// digitRun is one run of digits in a tag.
var digitRun = regexp.MustCompile(`[0-9]+`)

// OlderTag reports whether tag a is an older release than tag b: their runs
// of digits, read left to right as numbers, first differ lower in a. Tags
// whose numbers agree as far as both go are not older, and neither is a tag
// holding no number. It compares the versions of two builds of one
// repository; two repositories' tags need not compare.
func OlderTag(a, b string) bool {
	x, y := digitRun.FindAllString(a, -1), digitRun.FindAllString(b, -1)
	for i := 0; i < len(x) && i < len(y); i++ {
		if c := compareNumbers(x[i], y[i]); c != 0 {
			return c < 0
		}
	}
	return false
}

// compareNumbers compares two runs of digits as numbers of any length.
func compareNumbers(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}
