package blueprints

import (
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// The official core catalog is one repository, written the ways git accepts
// for it; anything else, a fork or a local copy included, is not it.
func TestOfficialCoreURL(t *testing.T) {
	for url, want := range map[string]bool{
		"https://github.com/Paisans-Software/paisans-blueprints":             true,
		"https://github.com/Paisans-Software/paisans-blueprints/":            true,
		"https://github.com/Paisans-Software/paisans-blueprints.git":         true,
		"https://github.com/Paisans-Software/paisans-blueprints.git/":        true,
		"https://GitHub.com/Paisans-Software/paisans-blueprints":             true,
		"https://github.com/Paisans-Software/paisans-blueprints-fork":        false,
		"https://github.com/Paisans-Software/paisans-blueprints/x":           false,
		"https://github.com/paisans-software/paisans-blueprints":             false,
		"https://github.com/example/paisans-blueprints":                      false,
		"https://github.com.example.org/Paisans-Software/paisans-blueprints": false,
		"https://github.com:443/Paisans-Software/paisans-blueprints":         false,
		"https://user@github.com/Paisans-Software/paisans-blueprints":        false,
		"https://github.com/Paisans-Software/paisans-blueprints?x=1":         false,
		"https://github.com/Paisans-Software/%70aisans-blueprints":           false,
		"http://github.com/Paisans-Software/paisans-blueprints":              false,
		"git@github.com:Paisans-Software/paisans-blueprints.git":             false,
		"file:///srv/mirror/paisans-blueprints":                              false,
		" https://github.com/Paisans-Software/paisans-blueprints":            false,
		"": false,
	} {
		if got := officialCoreURL(url); got != want {
			t.Errorf("%q: official %v, want %v", url, got, want)
		}
	}
}

// The toolkit's own blueprints are the reserved ones and the core catalog's,
// both written unqualified; a catalog's is written <catalog>/<name>.
func TestToolkitBlueprints(t *testing.T) {
	if err := LoadCore(""); err != nil {
		t.Fatal(err)
	}
	for blueprint, want := range map[config.Blueprint]bool{
		config.BlueprintPocketID:  true,
		"mbin":                    true,
		"example/journal":         false,
		"example/blueprints/mbin": false,
		"nonesuch":                false,
	} {
		if got := ToolkitBlueprint(blueprint); got != want {
			t.Errorf("%s: toolkit's own %v, want %v", blueprint, got, want)
		}
	}
}
