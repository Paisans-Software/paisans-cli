package validate_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

func example(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "..", "examples", "paisans-stack.example.yaml"))
	if err != nil {
		t.Fatalf("loading the example: %v", err)
	}
	return cfg
}

func portCollisions(cfg *config.Config) []validate.Finding {
	var out []validate.Finding
	for _, f := range validate.Check(cfg).Findings {
		if f.Rule == "port-collision" {
			out = append(out, f)
		}
	}
	return out
}

func TestExampleHasNoPortCollision(t *testing.T) {
	if found := portCollisions(example(t)); len(found) != 0 {
		t.Fatalf("the example collides: %v", found)
	}
}

// Each case is the example with one change that puts two listeners on one
// port. The message must name both owners and the port, since that is all an
// operator has to go on.
func TestPortCollisionRefuses(t *testing.T) {
	cases := []struct {
		name   string
		change func(*config.Config)
		want   []string
	}{
		{
			// Mbin is clustered, so it is on every apps site.
			name: "a second clustered mbin",
			change: func(cfg *config.Config) {
				app := cfg.Apps["talk"]
				app.Hostname = "forum.example.org"
				cfg.Apps["forum"] = app
			},
			want: []string{"app forum (mbin)", "app talk (mbin)", "10.44.0.1:8080/tcp", "sites.home-a"},
		},
		{
			// Caddy binds 80 on every address of the gateway.
			name: "element pinned to the gateway",
			change: func(cfg *config.Config) {
				app := cfg.Apps["web"]
				app.Placement = config.Placement{Mode: config.PlacementPinned, Site: "vm", Literal: "vm"}
				cfg.Apps["web"] = app
			},
			want: []string{"Caddy binds *:80/tcp", "app web (element)", "10.44.0.3:80/tcp"},
		},
		{
			name: "a cluster port on Postgres's",
			change: func(cfg *config.Config) {
				cfg.Cluster.Port = 5432
			},
			want: []string{"Postgres", "HAProxy cluster port", ":5432/tcp"},
		},
		{
			// Every HAProxy site serves Pocket ID's API on 1413.
			name: "a cluster port on Pocket ID's API",
			change: func(cfg *config.Config) {
				cfg.Cluster.Port = 1413
			},
			want: []string{"HAProxy Pocket ID API", "HAProxy cluster port", ":1413/tcp"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := example(t)
			tc.change(cfg)
			found := portCollisions(cfg)
			if len(found) == 0 {
				t.Fatal("no port-collision refusal")
			}
			var all []string
			for _, f := range found {
				if f.Level != validate.Refuse {
					t.Errorf("%s is a warning, want a refusal", f)
				}
				all = append(all, f.Key+": "+f.Message)
			}
			joined := strings.Join(all, "\n")
			for _, w := range tc.want {
				if !strings.Contains(joined, w) {
					t.Errorf("no finding mentions %q:\n%s", w, joined)
				}
			}
		})
	}
}

// A service on the mesh address and on loopback that collides on both is one
// finding, not two.
// The homeserver publishes 18008 and MAS 18009, clear of Patroni's API on
// 8008 and bg_mon on 8009, so a homeserver may share a data site.
func TestAHomeserverOnADataSiteHasNoPortCollision(t *testing.T) {
	cfg := example(t)
	app := cfg.Apps["chat"]
	app.Placement = config.Placement{Mode: config.PlacementPinned, Site: "home-b", Literal: "home-b"}
	cfg.Apps["chat"] = app
	if found := portCollisions(cfg); len(found) != 0 {
		t.Errorf("collisions: %v", found)
	}
}

func TestPortCollisionReportsAPairOnce(t *testing.T) {
	cfg := example(t)
	cfg.Cluster.Port = 5432
	perSite := map[string]int{}
	for _, f := range portCollisions(cfg) {
		perSite[f.Key]++
	}
	if perSite["sites.home-a"] != 1 {
		t.Errorf("home-a has %d findings, want 1: %v", perSite["sites.home-a"], portCollisions(cfg))
	}
}
