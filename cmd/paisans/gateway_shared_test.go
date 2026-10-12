package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/deployrecord"
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

// apply --site on a monitor site still holding the gateway's record is
// refused; on a monitor site holding nothing, and on the gateway, it is not.
func TestApplyRefusesASiteThatKeepsTheMonitorAndTheGatewaysRecord(t *testing.T) {
	cfg := sharedFixture(t)
	record := recordHost{files: map[string]string{deployrecord.Path(cfg.Deployment()): "{}"}}
	err := refuseSharedGatewayMove(cfg, "watch", record)
	if err == nil || !strings.Contains(err.Error(), "keeps the monitor") {
		t.Fatalf("watch with the record: %v", err)
	}
	if err := refuseSharedGatewayMove(cfg, "watch", recordHost{files: map[string]string{}}); err != nil {
		t.Errorf("a plain monitor was refused: %v", err)
	}
	if err := refuseSharedGatewayMove(cfg, "vm", record); err != nil {
		t.Errorf("the gateway was refused: %v", err)
	}
	if err := refuseSharedGatewayMove(cfg, "home-a", record); err != nil {
		t.Errorf("a site running no Caddy was refused: %v", err)
	}
}

// secrets rotate and rotate-key apply a site's stacks through
// scopedApplyPlan, so a shared site holding the record is refused there too:
// a rotation of the DNS token would otherwise rewrite its Caddy mid-move.
func TestAScopedApplyRefusesASiteThatKeepsTheMonitorAndTheGatewaysRecord(t *testing.T) {
	cfg := sharedFixture(t)
	record := recordHost{files: map[string]string{deployrecord.Path(cfg.Deployment()): "{}"}}
	_, err := scopedApplyPlan(cfg, &config.Secrets{}, "watch", record, true, "infra")
	if err == nil || !strings.Contains(err.Error(), "keeps the monitor") {
		t.Fatalf("a scoped apply of watch with the record: %v", err)
	}
}
