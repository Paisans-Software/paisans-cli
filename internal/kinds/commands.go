package kinds

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
)

// What the catalog and blueprint commands need beyond resolution.

// Newest is the newest tag the catalog at url has, by version order, and the
// commit it names, read with `git ls-remote` so nothing is cloned. An
// annotated tag's commit is the one it points at. A catalog with no tag has
// no release to pin, and is refused.
func Newest(url string) (tag, commit string, err error) {
	out, err := git("", "ls-remote", "--tags", "--sort=-v:refname", url)
	if err != nil {
		return "", "", fmt.Errorf("listing the tags of %s: %s", url, out)
	}
	peeled := map[string]string{}
	var order []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || !strings.HasPrefix(f[1], "refs/tags/") {
			continue
		}
		name := strings.TrimPrefix(f[1], "refs/tags/")
		if base, ok := strings.CutSuffix(name, "^{}"); ok {
			peeled[base] = f[0]
			continue
		}
		if _, seen := peeled[name]; !seen {
			peeled[name] = f[0]
		}
		order = append(order, name)
	}
	if len(order) == 0 {
		return "", "", fmt.Errorf("%s has no tags. A catalog is pinned to a release: tag one", url)
	}
	return order[0], peeled[order[0]], nil
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

// BuiltinNames is every built in blueprint's name, reserved ones marked.
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

// BuiltinSpec is a built in blueprint's blueprint.yaml.
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
	return nil, false
}

// Lint checks a blueprint directory as a catalog author writes it: the
// blueprint.yaml parses, every other file is a template, and the two
// templates every stack needs are there.
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
