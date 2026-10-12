package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/deployrecord"
	"github.com/paisans-software/paisans-stack/internal/render"
)

// recordHost is a host that answers file reads and nothing else.
type recordHost struct{ files map[string]string }

func (h recordHost) Describe() string { return "fake" }
func (h recordHost) Run(string) (string, error) {
	return "", errors.New("unexpected command")
}
func (h recordHost) RunInput(string, string) (string, error) {
	return "", errors.New("unexpected command")
}
func (h recordHost) ReadFile(p string) (string, bool, error) {
	c, ok := h.files[p]
	return c, ok, nil
}
func (h recordHost) WriteFile(string, string, uint32) error { return errors.New("unexpected write") }

func sharedFixture(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "..", "internal", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// caddyfileOf is site's Caddyfile in a render of cfg with opts.
func caddyfileOf(t *testing.T, cfg *config.Config, site string, opts []render.Option) string {
	t.Helper()
	secrets, err := config.LoadSecrets(filepath.Join("..", "..", "internal", "render", "testdata", "secrets.fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := render.Build(cfg, secrets, opts...)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range plan.Files {
		if strings.HasPrefix(f.Path, site+"/") && strings.HasSuffix(f.Path, "/infra/caddy/Caddyfile") {
			return f.Content
		}
	}
	t.Fatalf("no Caddyfile for %s", site)
	return ""
}

// A site that keeps the monitor and still holds the gateway's record renders
// as the gateway it still is, so applying it before the move keeps every
// route; a plain monitor renders as a monitor.
func TestASharedSiteHoldingTheRecordRendersDeparting(t *testing.T) {
	cfg := sharedFixture(t)
	record := recordHost{files: map[string]string{deployrecord.Path(cfg.Deployment()): "{}"}}
	opts, err := siteRenderOptions(cfg, "watch", record)
	if err != nil {
		t.Fatal(err)
	}
	if got := caddyfileOf(t, cfg, "watch", opts); !strings.Contains(got, "talk.example.org {") {
		t.Errorf("the departing site lost the gateway's routes:\n%s", got)
	}
	opts, err = siteRenderOptions(cfg, "watch", recordHost{files: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if got := caddyfileOf(t, cfg, "watch", opts); strings.Contains(got, "talk.example.org") {
		t.Errorf("a plain monitor routes the gateway's hostnames:\n%s", got)
	}
}

// apply without --site asks a monitor site too, so a gateway leaving a site
// that keeps the monitor gets its move step.
func TestConvergeMovesTheGatewayOffASiteThatKeepsTheMonitor(t *testing.T) {
	ran := fakeConverge(t, nil)
	holdsGateway(t, "watch", nil)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	want := append(commands(blankPlan()), "gateway move")
	sameLines(t, *ran, want)
}
