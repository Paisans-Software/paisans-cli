package blueprints

import (
	"io/fs"
	"os"
	"path/filepath"
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

// The test copy of the core catalog is exactly the commit the pin names: a
// hand edit to a blueprint's copy, or a pin written without the copy, fails
// here rather than testing something the binary will not fetch.
func TestTheTestCopyMatchesThePin(t *testing.T) {
	pin, err := CorePin()
	if err != nil {
		t.Fatal(err)
	}
	if len(pin.Commit) != 40 || pin.URL == "" {
		t.Fatalf("pin %+v names no commit", pin)
	}
	copy := os.DirFS(filepath.Join("testdata", "core", "blueprints"))
	entries, err := fs.ReadDir(copy, ".")
	if err != nil {
		t.Fatal(err)
	}
	copied := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			copied[e.Name()] = true
		}
	}
	for name := range pin.Blueprints {
		if !copied[name] {
			t.Errorf("the pin names %s, which the test copy does not hold", name)
		}
	}
	for name := range copied {
		want, ok := pin.Blueprints[name]
		if !ok {
			t.Errorf("the test copy holds %s, which the pin does not name", name)
			continue
		}
		got, err := Digest(copy, name)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("the test copy of %s has SHA-256 %s and the pin says %s. Change a core blueprint in the catalog and run scripts/pin-core-catalog.sh, rather than editing the copy", name, got, want)
		}
	}
}
