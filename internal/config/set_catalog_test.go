package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const catalogDoc = `version: 1
# The apps.
apps:
  talk:
    blueprint: mbin
`

var pin = Catalog{URL: "https://example.org/c.git", Tag: "v1.0.0", Commit: strings.Repeat("a", 40), Signers: []string{"ssh-ed25519 AAAA k"}}

// A first catalog goes in its own section just before apps:, leaving the
// comment that introduces apps: where it was.
func TestSetCatalogAddsTheSection(t *testing.T) {
	out, err := setCatalog([]byte(catalogDoc), "example", pin)
	if err != nil {
		t.Fatal(err)
	}
	want := "version: 1\ncatalogs:\n  example:\n    url: https://example.org/c.git\n    tag: v1.0.0\n    commit: " + strings.Repeat("a", 40) + "\n    signers:\n      - ssh-ed25519 AAAA k\n\n# The apps.\napps:\n"
	if !strings.HasPrefix(string(out), want) {
		t.Errorf("got:\n%s", out)
	}
	cfg := mustParse(t, out)
	if cfg.Catalogs["example"].Commit != pin.Commit || cfg.Catalogs["example"].Signers[0] != pin.Signers[0] {
		t.Errorf("parsed %+v", cfg.Catalogs)
	}
}

// A second catalog is added after the first, and an existing one is
// replaced in place with the comment above it kept.
func TestSetCatalogAddsAndReplaces(t *testing.T) {
	doc := "catalogs:\n  # Ours.\n  example:\n    url: old\n    commit: " + strings.Repeat("b", 40) + "\n    unsigned: true\napps: {}\n"
	out, err := setCatalog([]byte(doc), "other", Catalog{URL: "o", Commit: strings.Repeat("c", 40), Unsigned: true})
	if err != nil {
		t.Fatal(err)
	}
	out, err = setCatalog(out, "example", pin)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "  # Ours.\n  example:\n    url: https://example.org/c.git\n") || !strings.Contains(s, "  other:\n    url: o\n") || strings.Contains(s, "url: old") {
		t.Errorf("got:\n%s", s)
	}
	cfg := mustParse(t, out)
	if len(cfg.Catalogs) != 2 {
		t.Errorf("parsed %+v", cfg.Catalogs)
	}
	// Removing the last of two leaves the section; removing the last of one
	// takes the section too.
	out, err = removeCatalog(out, "other")
	if err != nil {
		t.Fatal(err)
	}
	out, err = removeCatalog(out, "example")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "catalogs") || !strings.Contains(string(out), "apps: {}") {
		t.Errorf("got:\n%s", out)
	}
	if _, err := removeCatalog(out, "example"); err == nil {
		t.Error("removed a catalog that is not declared")
	}
}

func mustParse(t *testing.T, data []byte) *Config {
	t.Helper()
	var cfg struct {
		Catalogs map[string]Catalog `yaml:"catalogs"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	return &Config{Catalogs: cfg.Catalogs}
}
