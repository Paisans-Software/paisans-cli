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
	b := "creation_rules:\n  - age: age1base\n"
	ours := "creation_rules:\n  - age: age1base,age1ours\n"
	theirs := "creation_rules:\n  - age: age1base\n  - path_regex: other\n    age: age1theirs\n"
	out, conflicts, err := SOPS([]byte(b), []byte(ours), []byte(theirs))
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("%v %v", err, conflicts)
	}
	if want := "creation_rules:\n  - age: age1base,age1ours\n  - path_regex: other\n    age: age1theirs\n"; string(out) != want {
		t.Fatalf("got\n%s", out)
	}
	theirs = "creation_rules:\n  - age: age1base,age1theirs\n"
	out, conflicts, err = SOPS([]byte(b), []byte(ours), []byte(theirs))
	if err != nil || !reflect.DeepEqual(conflicts, []string{"creation_rules.0"}) || string(out) != ours {
		t.Fatalf("%v %v\n%s", err, conflicts, out)
	}
}
