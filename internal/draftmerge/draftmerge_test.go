package draftmerge

import (
	"reflect"
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

func TestSOPSRuleIsOneValue(t *testing.T) {
	b := "creation_rules:\n  - age: age1base\n  - path_regex: other\n    age: age1other\n"
	ours := "creation_rules:\n  - age: age1base,age1ours\n  - path_regex: other\n    age: age1other\n"
	theirs := "creation_rules:\n  - age: age1base\n  - path_regex: other\n    age: age1other,age1theirs\n"
	out, conflicts, err := SOPS([]byte(b), []byte(ours), []byte(theirs))
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("%v %v", err, conflicts)
	}
	if want := "creation_rules:\n  - age: age1base,age1ours\n  - path_regex: other\n    age: age1other,age1theirs\n"; string(out) != want {
		t.Fatalf("got\n%s", out)
	}
	theirs = "creation_rules:\n  - age: age1base,age1theirs\n  - path_regex: other\n    age: age1other\n"
	out, conflicts, err = SOPS([]byte(b), []byte(ours), []byte(theirs))
	if err != nil || !reflect.DeepEqual(conflicts, []string{"creation_rules.0"}) || string(out) != ours {
		t.Fatalf("%v %v\n%s", err, conflicts, out)
	}
}

// The rules merge one by one only while the three lists are the same
// length; otherwise the list is one value, so a rule ours removed is never
// put back by its place shifting.
func TestSOPSRulesOfOtherLengthsAreOneValue(t *testing.T) {
	const a, b, c = "  - age: age1a\n", "  - age: age1b\n", "  - age: age1c\n"
	rules := func(r ...string) []byte { return []byte("creation_rules:\n" + strings.Join(r, "")) }
	for name, tc := range map[string]struct {
		base, ours, theirs []byte
		want               []byte
		conflicts          []string
	}{
		"ours removed one, theirs another": {rules(a, b, c), rules(a, b), rules(b, c), rules(a, b), []string{"creation_rules"}},
		"theirs inserted one":              {rules(a, b), rules(a, b), rules(c, a, b), rules(c, a, b), nil},
		"ours appended one":                {rules(a, b), rules(a, b, c), rules(a, b), rules(a, b, c), nil},
		"both appended differently":        {rules(a), rules(a, b), rules(a, c), rules(a, b), []string{"creation_rules"}},
	} {
		out, conflicts, err := SOPS(tc.base, tc.ours, tc.theirs)
		if err != nil || !reflect.DeepEqual(conflicts, tc.conflicts) || string(out) != string(tc.want) {
			t.Errorf("%s: %v %v\n%s", name, err, conflicts, out)
		}
	}
	// theirs inserted one while ours changed another: the list is one value.
	if _, conflicts, _ := SOPS(rules(a, b), rules(a, "  - age: age1b,age1d\n"), rules(c, a, b)); !reflect.DeepEqual(conflicts, []string{"creation_rules"}) {
		t.Errorf("an insert beside a change: %v", conflicts)
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
