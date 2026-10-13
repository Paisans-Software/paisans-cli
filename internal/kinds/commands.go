package kinds

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
)

// What the catalog and blueprint commands need beyond resolution.

// Newest is the catalog at url's newest release and the commit it names,
// read with `git ls-remote` so nothing is cloned. A release is a tag that is
// a semantic version and nothing more, vMAJOR.MINOR.PATCH or the same without
// the v, compared number by number. A pre-release (v2.0.0-rc1) or a build
// (v2.0.0+abc) is not one: it is pinned only by naming it with --tag, so a
// catalog's release candidate never reaches a deployment by default. Any
// other tag (latest, release-2026) is not a version and is ignored. The order
// is computed here rather than by git's version sort, which ranks
// v1.10.0-rc1 above v1.10.0 and follows each user's versionsort settings.
// An annotated tag's commit is the one it points at.
func Newest(url string) (tag, commit string, err error) {
	out, err := git("", "ls-remote", "--tags", url)
	if err != nil {
		return "", "", fmt.Errorf("listing the tags of %s: %s", url, out)
	}
	commits := map[string]string{}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || !strings.HasPrefix(f[1], "refs/tags/") {
			continue
		}
		name := strings.TrimPrefix(f[1], "refs/tags/")
		if base, ok := strings.CutSuffix(name, "^{}"); ok {
			commits[base] = f[0]
			continue
		}
		if _, seen := commits[name]; !seen {
			commits[name] = f[0]
			names = append(names, name)
		}
	}
	best, bestVersion := "", [3]int{}
	for _, name := range names {
		v, ok := releaseVersion(name)
		if !ok {
			continue
		}
		if best == "" || less(bestVersion, v) || (v == bestVersion && name < best) {
			best, bestVersion = name, v
		}
	}
	if best == "" {
		if len(names) == 0 {
			return "", "", fmt.Errorf("%s has no tags. A catalog is pinned to a release: tag one, as vMAJOR.MINOR.PATCH", url)
		}
		sort.Strings(names)
		if len(names) > 10 {
			names = append(names[:10], "...")
		}
		return "", "", fmt.Errorf("%s has no release tag, only %s. A release is tagged vMAJOR.MINOR.PATCH; a pre-release is pinned by naming it with --tag", url, strings.Join(names, ", "))
	}
	return best, commits[best], nil
}

// releaseTag is a tag that is a release: MAJOR.MINOR.PATCH with an optional
// leading v, no leading zeros, and no pre-release or build part.
var releaseTag = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// releaseVersion is a release tag's version, and false for any other tag.
func releaseVersion(tag string) ([3]int, bool) {
	m := releaseTag.FindStringSubmatch(tag)
	if m == nil {
		return [3]int{}, false
	}
	var v [3]int
	for i := range v {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}

// TagCommit is the commit a tag of the catalog at url names.
func TagCommit(url, tag string) (string, error) {
	out, err := git("", "ls-remote", "--tags", url, "refs/tags/"+tag, "refs/tags/"+tag+"^{}")
	if err != nil {
		return "", fmt.Errorf("listing the tags of %s: %s", url, out)
	}
	commit := ""
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		if strings.HasSuffix(f[1], "^{}") || commit == "" {
			commit = f[0]
		}
	}
	if commit == "" {
		return "", fmt.Errorf("%s has no tag %s", url, tag)
	}
	return commit, nil
}

// Changes is what moving one blueprint from old to new changes in what it
// grants and runs, one line each, for an operator deciding whether to take
// it: its images, its port, its hostnames, its sign in, the gate's openings,
// its storage, its secrets and its in-app actions. Nil when nothing they
// cover differs.
func Changes(old, new *Def) []string {
	var out []string
	images := func(d *Def) map[string]string {
		m := map[string]string{}
		for _, s := range d.Services {
			m[s.Name] = s.Image
		}
		return m
	}
	oi, ni := images(old), images(new)
	for _, name := range sortedUnion(oi, ni) {
		switch o, n := oi[name], ni[name]; {
		case o == n:
		case o == "" && !inMap(oi, name):
			out = append(out, fmt.Sprintf("adds the service %s, running %s", name, n))
		case !inMap(ni, name):
			out = append(out, fmt.Sprintf("drops the service %s", name))
		default:
			out = append(out, fmt.Sprintf("%s runs %s, was %s", name, n, o))
		}
	}
	if old.Port != new.Port {
		out = append(out, fmt.Sprintf("listens on %d, was %d", new.Port, old.Port))
	}
	if !reflect.DeepEqual(old.HostnameRoles, new.HostnameRoles) {
		out = append(out, fmt.Sprintf("hostname roles %v, were %v", new.HostnameRoles, old.HostnameRoles))
	}
	switch {
	case old.OIDC == nil && new.OIDC != nil:
		out = append(out, "signs members in through the identity provider, so apply creates its client")
	case old.OIDC != nil && new.OIDC == nil:
		out = append(out, "no longer signs members in through the identity provider")
	case old.OIDC != nil && new.OIDC.Callback("app.example.org", "app") != old.OIDC.Callback("app.example.org", "app"):
		out = append(out, "its sign-in callback moved, so its client changes")
	}
	og, ng := old.Gate, new.Gate
	if og.Gateable != ng.Gateable {
		out = append(out, fmt.Sprintf("gateable %v, was %v", ng.Gateable, og.Gateable))
	}
	for _, p := range []struct {
		what     string
		old, new []string
	}{{"opens past the gate", og.OpenPaths, ng.OpenPaths}, {"takes ActivityPub deliveries on", og.InboxPaths, ng.InboxPaths}, {"admits a bearer token on", og.TokenPaths, ng.TokenPaths}} {
		if added := minus(p.new, p.old); len(added) > 0 {
			out = append(out, p.what+" "+strings.Join(added, ", "))
		}
	}
	if old.ObjectStorage != new.ObjectStorage || old.PublicObjects != new.PublicObjects {
		out = append(out, fmt.Sprintf("object storage %v, public reads %v; was %v, %v", new.ObjectStorage, new.PublicObjects, old.ObjectStorage, old.PublicObjects))
	}
	if added := minus(new.AppSecrets, old.AppSecrets); len(added) > 0 {
		out = append(out, "generates the secrets "+strings.Join(added, ", "))
	}
	if (old.LiveSettings == nil) != (new.LiveSettings == nil) || !reflect.DeepEqual(old.LiveSettings, new.LiveSettings) {
		out = append(out, "reads its settings from a running app differently")
	}
	return out
}

func inMap(m map[string]string, k string) bool { _, ok := m[k]; return ok }

func sortedUnion(a, b map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range []map[string]string{a, b} {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

func minus(a, b []string) []string {
	have := map[string]bool{}
	for _, s := range b {
		have[s] = true
	}
	var out []string
	for _, s := range a {
		if !have[s] {
			out = append(out, s)
		}
	}
	return out
}

// BuiltinNames is every reserved and core blueprint's name, reserved ones
// apart.
func BuiltinNames() (reserved, core []string) {
	for _, kind := range known() {
		if _, fromCatalog := Source(kind); fromCatalog && strings.Contains(string(kind), "/") {
			continue
		}
		if of(kind).Reserved {
			reserved = append(reserved, string(kind))
		} else {
			core = append(core, string(kind))
		}
	}
	return reserved, core
}

// BuiltinSpec is a reserved or core blueprint's blueprint.yaml.
func BuiltinSpec(name string) ([]byte, bool) {
	builtins, err := blueprints.Builtins()
	if err != nil {
		return nil, false
	}
	for _, b := range builtins {
		if b.Name == name {
			return b.Spec, true
		}
	}
	if _, ok := For(config.Kind(name)); !ok || strings.Contains(name, "/") {
		return nil, false
	}
	dir, ok := Source(config.Kind(name))
	if !ok {
		return nil, false
	}
	spec, err := os.ReadFile(filepath.Join(dir, blueprints.File))
	return spec, err == nil
}

// Lint checks a blueprint directory as a catalog author writes it: the
// blueprint.yaml parses, every other file is a template, the two templates
// every stack needs are there, and no template's fallback for a setting
// disagrees with the default the blueprint declares for it.
func Lint(dir string) (*Def, []string, error) {
	name := filepath.Base(filepath.Clean(dir))
	raw, err := os.ReadFile(filepath.Join(dir, blueprints.File))
	if err != nil {
		return nil, nil, fmt.Errorf("%s has no %s", dir, blueprints.File)
	}
	hasTemplate := func(file string) bool {
		_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(file)))
		return err == nil
	}
	d, err := Parse(name, raw, hasTemplate)
	if err != nil {
		return nil, nil, err
	}
	var problems []string
	err = filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel != blueprints.File && !strings.HasSuffix(rel, ".tmpl") {
			problems = append(problems, fmt.Sprintf("%s is not a template; every file but %s is rendered", filepath.ToSlash(rel), blueprints.File))
		}
		if strings.HasSuffix(rel, ".tmpl") {
			content, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			problems = append(problems, fallbackProblems(filepath.ToSlash(rel), string(content), d.Settings)...)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	for _, need := range []string{"compose.yaml.tmpl", "caddy.snippet.tmpl"} {
		if _, err := os.Stat(filepath.Join(dir, need)); err != nil {
			problems = append(problems, need+" is missing; every stack has one")
		}
	}
	return d, problems, nil
}

// settingCall and settingBoolCall find a template's literal calls of the
// two setting helpers, with the key and the fallback each is given.
var (
	settingCall     = regexp.MustCompile(`\.Setting\s+("(?:[^"\\]|\\.)*")\s+("(?:[^"\\]|\\.)*")`)
	settingBoolCall = regexp.MustCompile(`\.SettingBool\s+("(?:[^"\\]|\\.)*")\s+(true|false)\b`)
)

// fallbackProblems is every literal `.Setting "key" "fallback"` and
// `.SettingBool "key" true|false` in a template whose fallback is not the
// default the blueprint declares for key. A catalog blueprint's default is
// filled in before the template runs (SettingsSeen), so the fallback is not
// what renders and a reader of the template would be misled about the value
// an app gets; the default is to be stated once.
//
// The calls are found by a regular expression over the template's text, so
// only calls written with a literal key and a literal fallback are seen. A
// key or a fallback that is a variable, a pipeline or a raw string is not
// checked.
func fallbackProblems(file, content string, settings map[string]SettingSpec) []string {
	var out []string
	check := func(rawKey, fallback, written, call string) {
		key, err := strconv.Unquote(rawKey)
		if err != nil {
			return
		}
		spec, ok := settings[key]
		if !ok || spec.Default == nil {
			return
		}
		if want := fmt.Sprint(spec.Default); want != fallback {
			out = append(out, fmt.Sprintf("%s: %s %q falls back to %s, but settings.%s.default is %v; state the default once, in %s", file, call, key, written, key, spec.Default, blueprints.File))
		}
	}
	for _, m := range settingCall.FindAllStringSubmatch(content, -1) {
		fallback, err := strconv.Unquote(m[2])
		if err != nil {
			continue
		}
		check(m[1], fallback, m[2], ".Setting")
	}
	for _, m := range settingBoolCall.FindAllStringSubmatch(content, -1) {
		check(m[1], m[2], m[2], ".SettingBool")
	}
	return out
}
