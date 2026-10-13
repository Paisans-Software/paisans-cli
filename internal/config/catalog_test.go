package config

import (
	"strings"
	"testing"
)

// Each mistake in a catalog declaration, or in a blueprint name that leans
// on one, is reported by the key to change.
func TestCatalogDeclarationsAreChecked(t *testing.T) {
	commit := strings.Repeat("a", 40)
	cases := []struct {
		catalogs map[string]Catalog
		kind     Kind
		want     string
	}{
		{nil, "example/journal", "which catalogs does not declare"},
		{map[string]Catalog{"example": {Commit: commit}}, "example/journal", "catalogs.example.url: required"},
		{map[string]Catalog{"example": {URL: "https://example.org/x/paisans-c"}}, "example/journal", "catalogs.example.commit: required"},
		{map[string]Catalog{"example": {URL: "https://example.org/x/paisans-c", Commit: "main"}}, "example/journal", "is not a full 40 character"},
		{map[string]Catalog{"Bad_Name": {URL: "https://example.org/x/paisans-c", Commit: commit}}, "mbin", "is not a catalog name"},
		{nil, "core/mbin", "written unqualified"},
		{nil, "a/b/c/d", "is not a blueprint name"},
		{nil, "example/blueprints/journal", "paisans catalog add example/blueprints`, which reads https://github.com/example/paisans-blueprints"},
		{map[string]Catalog{"example": {URL: "https://example.org/x/blueprints", Commit: commit}}, "example/journal", "is not a catalog's repository"},
		{map[string]Catalog{"Example/Blueprints": {URL: "https://example.org/x/paisans-c", Commit: commit}}, "mbin", "is not a catalog name"},
	}
	for _, c := range cases {
		cfg := &Config{Catalogs: c.catalogs, Apps: map[string]App{"notes": {Kind: c.kind}}}
		got := strings.Join(cfg.catalogProblems(), "\n")
		if !strings.Contains(got, c.want) {
			t.Errorf("%v %s: want %q, got %q", c.catalogs, c.kind, c.want, got)
		}
	}
	ok := &Config{Catalogs: map[string]Catalog{
		"example":            {URL: "https://example.org/x/paisans-c", Commit: commit},
		"example/blueprints": {URL: "https://github.com/example/paisans-blueprints.git", Commit: commit},
	}, Apps: map[string]App{"notes": {Kind: "example/journal"}, "talk": {Kind: "mbin"}, "wiki": {Kind: "example/blueprints/journal"}}}
	if p := ok.catalogProblems(); len(p) > 0 {
		t.Errorf("a sound declaration was refused: %v", p)
	}
}

// A catalog's default name is its owner and its repository without the
// paisans- prefix, from any form of URL, and there is none for a repository
// not named with the prefix.
func TestDefaultCatalogName(t *testing.T) {
	for url, want := range map[string]string{
		"https://github.com/Example/paisans-blueprints":     "example/blueprints",
		"https://github.com/example/paisans-blueprints.git": "example/blueprints",
		"git@github.com:example/paisans-media.git":          "example/media",
		"ssh://git@git.example.org:2222/team/paisans-tools": "team/tools",
		"/srv/catalogs/local/paisans-test/":                 "local/test",
	} {
		if got, ok := DefaultCatalogName(url); !ok || got != want {
			t.Errorf("%s: got %q, %v, want %q", url, got, ok, want)
		}
	}
	for _, url := range []string{"https://github.com/example/blueprints", "https://github.com/example/paisans-", "paisans-x", "https://github.com/ex.ample/paisans-x"} {
		if got, ok := DefaultCatalogName(url); ok {
			t.Errorf("%s: got %q", url, got)
		}
	}
	if c, n := SplitBlueprint("example/blueprints/journal"); c != "example/blueprints" || n != "journal" {
		t.Errorf("split %s %s", c, n)
	}
}
