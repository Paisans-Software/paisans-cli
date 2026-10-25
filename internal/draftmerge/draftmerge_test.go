package draftmerge

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func merge(t *testing.T, base, ours, theirs string) (string, []string) {
	t.Helper()
	out, conflicts, err := Config([]byte(base), []byte(ours), []byte(theirs))
	if err != nil {
		t.Fatal(err)
	}
	return string(out), conflicts
}

// paths is the merged document by path, for comparing what it holds
// whatever its layout.
func paths(t *testing.T, doc string) map[string]string {
	t.Helper()
	p, err := Paths([]byte(doc))
	if err != nil {
		t.Fatalf("%v\n%s", err, doc)
	}
	return p
}

const base = `id: x
community:
  domain: example.org
  name: Example
sites:
  home-a:
    region: a
  home-b:
    region: b
`

func TestConfigMergesDisjointPaths(t *testing.T) {
	ours := strings.Replace(base, "name: Example", "name: Ours", 1)
	theirs := strings.Replace(base, "region: b", "region: bb", 1) + "apps:\n  talk:\n    site: home-a\n"
	out, conflicts := merge(t, base, ours, theirs)
	if len(conflicts) != 0 {
		t.Fatalf("conflicts %v", conflicts)
	}
	got := paths(t, out)
	want := map[string]string{"id": "x", "community.domain": "example.org", "community.name": "Ours", "sites.home-a.region": "a", "sites.home-b.region": "bb", "apps.talk.site": "home-a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\n%s", got, out)
	}
	// A path only theirs adds goes under its parent after ours' own keys.
	if !strings.HasSuffix(out, "apps:\n  talk:\n    site: home-a\n") {
		t.Errorf("theirs' new section is not appended:\n%s", out)
	}
}

func TestConfigConflictKeepsOurs(t *testing.T) {
	ours := strings.Replace(base, "region: a", "region: ours", 1)
	theirs := strings.Replace(strings.Replace(base, "region: a", "region: theirs", 1), "name: Example", "name: Theirs", 1)
	out, conflicts := merge(t, base, ours, theirs)
	if !reflect.DeepEqual(conflicts, []string{"sites.home-a.region"}) {
		t.Fatalf("conflicts %v", conflicts)
	}
	got := paths(t, out)
	if got["sites.home-a.region"] != "ours" || got["community.name"] != "Theirs" {
		t.Fatalf("got %v", got)
	}
}

func TestConfigBothSameChangeNoConflict(t *testing.T) {
	both := strings.Replace(base, "region: a", "region: same", 1)
	out, conflicts := merge(t, base, both, both+"# theirs\n")
	if len(conflicts) != 0 || paths(t, out)["sites.home-a.region"] != "same" {
		t.Fatalf("conflicts %v\n%s", conflicts, out)
	}
}

func TestConfigRemovedPath(t *testing.T) {
	removed := strings.Replace(base, "  home-b:\n    region: b\n", "", 1)
	// Ours removed it, theirs left it: removed.
	out, conflicts := merge(t, base, removed, strings.Replace(base, "name: Example", "name: Theirs", 1))
	got := paths(t, out)
	if _, ok := got["sites.home-b.region"]; ok || len(conflicts) != 0 || got["community.name"] != "Theirs" {
		t.Fatalf("ours removed: %v %v\n%s", got, conflicts, out)
	}
	// Theirs removed it, ours left it: removed.
	out, conflicts = merge(t, base, base, removed)
	if _, ok := paths(t, out)["sites.home-b.region"]; ok || len(conflicts) != 0 {
		t.Fatalf("theirs removed: %v\n%s", conflicts, out)
	}
	// Ours removed it, theirs changed it: a conflict, ours kept (removed).
	out, conflicts = merge(t, base, removed, strings.Replace(base, "region: b", "region: bb", 1))
	if _, ok := paths(t, out)["sites.home-b.region"]; ok || !reflect.DeepEqual(conflicts, []string{"sites.home-b.region"}) {
		t.Fatalf("removed and changed: %v\n%s", conflicts, out)
	}
	// Theirs removed it, ours changed it: a conflict, ours kept.
	out, conflicts = merge(t, base, strings.Replace(base, "region: b", "region: ours", 1), removed)
	if paths(t, out)["sites.home-b.region"] != "ours" || !reflect.DeepEqual(conflicts, []string{"sites.home-b.region"}) {
		t.Fatalf("changed and removed: %v\n%s", conflicts, out)
	}
}

func TestConfigSequenceIsOneValue(t *testing.T) {
	b := "list:\n  - a\n  - b\n"
	ours := "list:\n  - a\n  - b\n  - ours\n"
	theirs := "list:\n  - theirs\n  - a\n  - b\n"
	out, conflicts := merge(t, b, ours, theirs)
	if !reflect.DeepEqual(conflicts, []string{"list"}) || out != ours {
		t.Fatalf("conflicts %v\n%s", conflicts, out)
	}
	out, conflicts = merge(t, b, b, theirs)
	if len(conflicts) != 0 || paths(t, out)["list"] != paths(t, theirs)["list"] {
		t.Fatalf("theirs alone: %v\n%s", conflicts, out)
	}
	p := paths(t, "a:\n  b: [1, 2]\n  c: {}\n")
	if !reflect.DeepEqual(p, map[string]string{"a.b": "- 1\n- 2", "a.c": "{}"}) {
		t.Fatalf("Paths %q", p)
	}
}

func TestConfigKeepsOurComments(t *testing.T) {
	ours := "# the deployment\nid: x # its id\ncommunity:\n  # the name members see\n  name: Ours\n  domain: example.org\nsites:\n  home-a:\n    region: a\n  home-b:\n    region: b\n"
	theirs := strings.Replace(base, "region: a", "region: aa", 1)
	out, conflicts := merge(t, base, ours, theirs)
	if len(conflicts) != 0 {
		t.Fatal(conflicts)
	}
	for _, want := range []string{"# the deployment\n", "id: x # its id\n", "  # the name members see\n  name: Ours\n  domain: example.org\n", "region: aa\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	// Nothing to take from theirs: ours, byte for byte.
	if out, _ := merge(t, base, ours, base); out != ours {
		t.Errorf("ours rewritten:\n%s", out)
	}
}

// creation_rules is one value whatever its rules: a list only one side
// changed is taken whole, two different changes are a conflict keeping
// ours' whole list, and no rule is paired with another by its place. The
// keys beside it merge path by path.
func TestSOPSRulesAreOneValue(t *testing.T) {
	const a, b, c, d = "  - age: age1a\n", "  - age: age1b\n", "  - age: age1c\n", "  - age: age1d\n"
	const revoked, revokedNot = "  - path_regex: s\n    age: age1c,age1revoked\n", "  - path_regex: s\n    age: age1c\n"
	rules := func(r ...string) string { return "creation_rules:\n" + strings.Join(r, "") }
	for name, tc := range map[string]struct {
		base, ours, theirs string
		want               string
		conflicts          []string
	}{
		"ours revokes, theirs drops one and appends": {rules(a, b, revoked), rules(a, b, revokedNot), rules(b, revoked, d), rules(a, b, revokedNot), []string{"creation_rules"}},
		"both swap differently":                      {rules(a, b, c), rules(b, a, c), rules(a, c, b), rules(b, a, c), []string{"creation_rules"}},
		"ours removed one, theirs another":           {rules(a, b, c), rules(a, b), rules(b, c), rules(a, b), []string{"creation_rules"}},
		"both change one rule each":                  {rules(a, b), rules("  - age: age1a,age1x\n", b), rules(a, "  - age: age1b,age1y\n"), rules("  - age: age1a,age1x\n", b), []string{"creation_rules"}},
		"theirs inserted one":                        {rules(a, b), rules(a, b), rules(c, a, b), rules(c, a, b), nil},
		"theirs appended one":                        {rules(a, b), rules(a, b), rules(a, b, c), rules(a, b, c), nil},
		"theirs removed one":                         {rules(a, b, c), rules(a, b, c), rules(a, c), rules(a, c), nil},
		"theirs reordered":                           {rules(a, b, c), rules(a, b, c), rules(c, b, a), rules(c, b, a), nil},
		"ours appended one":                          {rules(a, b), rules(a, b, c), rules(a, b), rules(a, b, c), nil},
		"both made the same change":                  {rules(a), rules(a, b), rules(a, b), rules(a, b), nil},
	} {
		out, conflicts, err := SOPS([]byte(tc.base), []byte(tc.ours), []byte(tc.theirs))
		if err != nil || !reflect.DeepEqual(conflicts, tc.conflicts) || string(out) != tc.want {
			t.Errorf("%s: %v %v\n%s", name, err, conflicts, out)
		}
		if !strings.Contains(tc.ours, "age1revoked") && strings.Contains(string(out), "age1revoked") {
			t.Errorf("%s: a recipient ours removed is back:\n%s", name, out)
		}
	}
	// A key beside creation_rules merges by its path.
	base := rules(a) + "stores:\n  yaml:\n    indent: 2\n"
	out, conflicts, err := SOPS([]byte(base), []byte(rules(a, b)+"stores:\n  yaml:\n    indent: 2\n"), []byte(rules(a)+"stores:\n  yaml:\n    indent: 4\n"))
	if err != nil || len(conflicts) != 0 || !strings.Contains(string(out), "age1b") || !strings.Contains(string(out), "indent: 4") {
		t.Errorf("beside creation_rules: %v %v\n%s", err, conflicts, out)
	}
}

// A document with an anchor, an alias or a key given twice is refused: the
// toolkit writes none, and a merge cannot place them.
func TestConfigRefusesAnchorsAndDuplicateKeys(t *testing.T) {
	changed := strings.Replace(base, "name: Example", "name: Theirs", 1)
	for name, doc := range map[string]string{
		"anchor":    base + "x: &a 1\n",
		"alias":     "x: &a 1\ny: *a\n" + base,
		"duplicate": base + "id: y\n",
	} {
		_, _, err := Config([]byte(base), []byte(doc), []byte(changed))
		if err == nil || (name != "duplicate" && !strings.Contains(err.Error(), "by hand")) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A conflict at the root of a document that is not a mapping names the
// whole file.
func TestConfigRootConflictNamesTheWholeFile(t *testing.T) {
	_, conflicts := merge(t, "a\n", "b\n", "c\n")
	if !reflect.DeepEqual(conflicts, []string{"(whole file)"}) {
		t.Fatalf("conflicts %v", conflicts)
	}
}

// A key holding a dot is one segment, bracketed, so it never collides with
// the nested path its dotted spelling would name, in the paths and in a
// conflict's label.
func TestDottedKeysDoNotCollide(t *testing.T) {
	doc := []byte("a.b: 1\na:\n  b: 2\n")
	got, err := Paths(doc)
	if err != nil {
		t.Fatal(err)
	}
	if got[`["a.b"]`] != "1" || got["a.b"] != "2" || len(got) != 2 {
		t.Fatalf("paths %v", got)
	}
	_, conflicts, err := Config([]byte("x.y: 1\n"), []byte("x.y: 2\n"), []byte("x.y: 3\n"))
	if err != nil || len(conflicts) != 1 || conflicts[0] != `["x.y"]` {
		t.Fatalf("conflicts %q, %v", conflicts, err)
	}
	for _, keys := range [][]string{{"sites", "home.a", "host"}, {"a"}, {"", "b"}, {`q"[x]`, "y"}} {
		back, ok := SplitPath(JoinPath(keys))
		if !ok || !slices.Equal(back, keys) {
			t.Errorf("%q: %q round trips to %q, %v", keys, JoinPath(keys), back, ok)
		}
	}
	if JoinPath([]string{"sites", "home.a", "host"}) != `sites["home.a"].host` {
		t.Errorf("got %s", JoinPath([]string{"sites", "home.a", "host"}))
	}
}

// An inline merge key, which needs no anchor, is refused as an alias is:
// the merge cannot place what it merges in.
func TestInlineMergeKeyRefused(t *testing.T) {
	doc := []byte("a:\n  <<: {x: 1}\n  y: 2\n")
	if _, _, err := Config([]byte("a: {y: 1}\n"), doc, []byte("a: {y: 3}\n")); err == nil || !strings.Contains(err.Error(), "merge key") {
		t.Fatalf("got %v", err)
	}
	if _, err := Paths(doc); err == nil {
		t.Fatal("Paths took a merge key")
	}
	// A quoted "<<" is an ordinary key.
	if _, err := Paths([]byte("a:\n  \"<<\": 1\n")); err != nil {
		t.Fatalf("a quoted key: %v", err)
	}
}

// The blank lines between keys survive a merge: ours' where ours has a key,
// theirs' before a key only theirs adds, and none where neither has one.
func TestConfigKeepsBlankLines(t *testing.T) {
	base := `# header

version: 1
community:
  name: x

sites:
  solo:
    keys: [john]

apps:
  auth:
    app_name: Test
`
	ours := `# header

version: 1
community:
  name: x

sites:
  solo:
    keys: [john]

# john: a comment of ours.
apps:
  auth:
    app_name: Test
`
	theirs := `# header

version: 1
community:
  name: x

sites:
  solo:
    keys: [john]

apps:
  auth:
    app_name: Theirs

storage:
  garage: {}
`
	want := `# header

version: 1
community:
  name: x

sites:
  solo:
    keys: [john]

# john: a comment of ours.
apps:
  auth:
    app_name: Theirs

storage:
  garage: {}
`
	got, conflicts := merge(t, base, ours, theirs)
	if len(conflicts) != 0 {
		t.Fatalf("conflicts %v", conflicts)
	}
	if got != want {
		t.Errorf("merged:\n%s\nwant:\n%s", got, want)
	}
}

// A comment only theirs changed is theirs in the merge, as a value is; one
// both changed keeps ours.
func TestConfigTakesACommentOnlyTheirsChanged(t *testing.T) {
	base := "sites:\n  solo:\n    keys: [john]\n\n# the cluster\ncluster:\n  port: 5000\napps:\n  auth:\n    app_name: Test # the name\n"
	ours := strings.Replace(base, "app_name: Test", "app_name: Ours", 1)
	theirs := strings.Replace(strings.Replace(base, "# the cluster", "# john: the cluster, untouched", 1), "# the name", "# what members see", 1)
	want := "sites:\n  solo:\n    keys: [john]\n\n# john: the cluster, untouched\ncluster:\n  port: 5000\napps:\n  auth:\n    app_name: Ours # what members see\n"
	got, conflicts := merge(t, base, ours, theirs)
	if len(conflicts) != 0 {
		t.Fatalf("conflicts %v", conflicts)
	}
	if got != want {
		t.Errorf("merged:\n%s\nwant:\n%s", got, want)
	}

	ours = strings.Replace(ours, "# the cluster", "# ours: the cluster", 1)
	if got, _ := merge(t, base, ours, theirs); !strings.Contains(got, "# ours: the cluster\n") || strings.Contains(got, "untouched") {
		t.Errorf("a comment both changed is not ours:\n%s", got)
	}
}
