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
