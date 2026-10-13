package blueprints

import (
	"io/fs"
	"strings"
	"testing"
)

// The template filesystem shows each blueprint's templates under its name
// and never its blueprint.yaml, so a renderer walking a blueprint renders
// every file it finds.
func TestTemplatesHideTheBlueprintFile(t *testing.T) {
	builtins, err := Builtins()
	if err != nil {
		t.Fatal(err)
	}
	if len(builtins) == 0 {
		t.Fatal("no blueprints are built in")
	}
	for _, b := range builtins {
		var seen int
		err := fs.WalkDir(Templates, b.Name, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Name() == File {
				t.Errorf("%s: %s is visible as a template", b.Name, path)
			}
			if !d.IsDir() {
				seen++
				if !strings.HasSuffix(path, ".tmpl") {
					t.Errorf("%s is in a blueprint but is not a template", path)
				}
			}
			return nil
		})
		if err != nil {
			t.Errorf("%s: %v", b.Name, err)
		}
		if seen == 0 {
			t.Errorf("%s ships no templates", b.Name)
		}
		if _, err := fs.ReadFile(Templates, b.Name+"/"+File); err == nil {
			t.Errorf("%s: its blueprint.yaml can be read as a template", b.Name)
		}
	}
}

// A blueprint is in core or reserved, never both, so a name has one answer.
func TestABlueprintHasOneOrigin(t *testing.T) {
	builtins, err := Builtins()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]Origin{}
	for _, b := range builtins {
		if o, ok := seen[b.Name]; ok {
			t.Errorf("%s is in both %s and %s", b.Name, o, b.Origin)
		}
		seen[b.Name] = b.Origin
	}
}

// The copy of the core catalog in the binary is exactly the commit its pin
// names: a hand edit to a blueprint's copy, or a sync that did not write the
// pin, fails here rather than shipping as if it were the catalog's.
func TestTheCoreCopyMatchesItsPin(t *testing.T) {
	pin, err := CorePin()
	if err != nil {
		t.Fatal(err)
	}
	if len(pin.Commit) != 40 || pin.URL == "" {
		t.Fatalf("pin %+v names no commit", pin)
	}
	copied := map[string]bool{}
	for _, b := range mustBuiltins(t) {
		if b.Origin == Core {
			copied[b.Name] = true
		}
	}
	for name := range pin.Blueprints {
		if !copied[name] {
			t.Errorf("the pin names %s, which the core copy does not hold", name)
		}
	}
	for name := range copied {
		want, ok := pin.Blueprints[name]
		if !ok {
			t.Errorf("the core copy holds %s, which the pin does not name", name)
			continue
		}
		got, err := CoreDigest(name)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("the core copy of %s has SHA-256 %s and its pin says %s. Change a core blueprint in the catalog and run scripts/pin-core-catalog.sh, rather than editing the copy", name, got, want)
		}
	}
}

func mustBuiltins(t *testing.T) []Builtin {
	t.Helper()
	builtins, err := Builtins()
	if err != nil {
		t.Fatal(err)
	}
	return builtins
}
