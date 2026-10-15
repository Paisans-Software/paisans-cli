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
	builtins, err := embeddedBlueprints()
	if err != nil {
		t.Fatal(err)
	}
	if len(builtins) == 0 {
		t.Fatal("no blueprints are built in")
	}
	for _, b := range builtins {
		var seen int
		err := fs.WalkDir(embeddedTemplates, b.Name, func(path string, d fs.DirEntry, err error) error {
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
		if _, err := fs.ReadFile(embeddedTemplates, b.Name+"/"+File); err == nil {
			t.Errorf("%s: its blueprint.yaml can be read as a template", b.Name)
		}
	}
}
