package secretsgen_test

import (
	"regexp"
	"slices"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
)

// Every site gets a beszel_token while the deployment declares a beszel app,
// 32 random bytes as hex, kept once set, and none without one: a token no
// hub lists is a secret nothing reads.
func TestFillBeszelToken(t *testing.T) {
	cfg := load(t)
	if cfg.Apps["stats"].Blueprint != config.BlueprintBeszel {
		t.Fatal("the fixture's stats app is not the beszel blueprint")
	}
	secrets := &config.Secrets{}
	filled, err := secretsgen.Fill(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	shape := regexp.MustCompile(`^[0-9a-f]{64}$`)
	before := map[string]string{}
	for _, site := range cfg.SiteNames() {
		token := secrets.Sites[site].BeszelToken
		if !shape.MatchString(token) {
			t.Errorf("sites.%s.beszel_token is %q, not 64 lowercase hex characters", site, token)
		}
		if !slices.Contains(filled.Generated, "sites."+site+".beszel_token") {
			t.Errorf("sites.%s.beszel_token is not reported as generated", site)
		}
		if !slices.Contains(secretsgen.Declare(cfg, secrets), secretsgen.Declared{Path: "sites." + site + ".beszel_token", Source: secretsgen.Generated}) {
			t.Errorf("sites.%s.beszel_token is not declared as generated", site)
		}
		before[site] = token
	}
	again, err := secretsgen.Fill(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed() {
		t.Fatalf("a second pass generated %v", again.Generated)
	}
	for _, site := range cfg.SiteNames() {
		if secrets.Sites[site].BeszelToken != before[site] {
			t.Errorf("sites.%s.beszel_token was replaced", site)
		}
	}

	next, err := secretsgen.ReplacementIn(cfg, "sites."+cfg.SiteNames()[0]+".beszel_token", secrets)
	if err != nil || !shape.MatchString(next) || next == before[cfg.SiteNames()[0]] {
		t.Errorf("ReplacementIn(beszel_token) = %q, %v; want a fresh 64 hex token", next, err)
	}

	delete(cfg.Apps, "stats")
	delete(cfg.Apps, "notify") // it serves the hub

	bare := &config.Secrets{}
	if _, err := secretsgen.Fill(cfg, bare); err != nil {
		t.Fatal(err)
	}
	for _, site := range cfg.SiteNames() {
		if bare.Sites[site].BeszelToken != "" {
			t.Errorf("sites.%s.beszel_token generated without a beszel app", site)
		}
	}
	for _, d := range secretsgen.Declare(cfg, bare) {
		if regexp.MustCompile(`\.beszel_token$`).MatchString(d.Path) {
			t.Errorf("%s declared without a beszel app", d.Path)
		}
	}
}
