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
		{map[string]Catalog{"example": {URL: "u"}}, "example/journal", "catalogs.example.commit: required"},
		{map[string]Catalog{"example": {URL: "u", Commit: "main"}}, "example/journal", "is not a full 40 character"},
		{map[string]Catalog{"Bad_Name": {URL: "u", Commit: commit}}, "mbin", "is not a catalog name"},
		{map[string]Catalog{"core": {Signers: []string{"k"}}}, "mbin", "catalogs.core.signers"},
		{map[string]Catalog{"core": {Unsigned: true}}, "mbin", "core catalog cannot be unsigned"},
		{nil, "core/mbin", "written unqualified"},
		{nil, "a/b/c", "is not a blueprint name"},
		{nil, "gopher", "unknown blueprint"},
	}
	for _, c := range cases {
		cfg := &Config{Catalogs: c.catalogs, Apps: map[string]App{"notes": {Kind: c.kind}}}
		got := strings.Join(cfg.catalogProblems(), "\n")
		if !strings.Contains(got, c.want) {
			t.Errorf("%v %s: want %q, got %q", c.catalogs, c.kind, c.want, got)
		}
	}
	ok := &Config{Catalogs: map[string]Catalog{"example": {URL: "u", Commit: commit}}, Apps: map[string]App{"notes": {Kind: "example/journal"}, "talk": {Kind: "mbin"}}}
	if p := ok.catalogProblems(); len(p) > 0 {
		t.Errorf("a sound declaration was refused: %v", p)
	}
}
