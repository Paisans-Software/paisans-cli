package main

import (
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

func prepareAll(t *testing.T, o hostPrepareOptions) (*ui.Recorder, error) {
	t.Helper()
	rec := &ui.Recorder{Verbose_: true}
	o.config, o.all = fixtureConfig(), true
	err := hostPrepare(prepareEnv{r: rec, found: func(string, error) {}, stdin: noTerminalStdin(), stdout: io.Discard}, o)
	return rec, err
}

// --all prepares every site the configuration declares, in its order, and
// shows the configuration's findings once rather than once a site.
func TestHostPrepareAllPreparesEverySiteInOrder(t *testing.T) {
	loggingSSH(t, "")
	fakeGrants(t, grantedAlready(t, fixtureConfig(), fixtureSecretsPath()))
	rec, err := prepareAll(t, hostPrepareOptions{})
	if err != nil {
		t.Fatalf("%v\n%s", err, rec.Lines())
	}
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	var sites []string
	findings := 0
	for _, e := range rec.Events {
		switch {
		case e.Kind == "section" && strings.HasSuffix(e.Text, ")"):
			sites = append(sites, strings.Fields(e.Text)[0])
		case e.Kind == "section" && e.Text == fixtureConfig():
			findings++
		}
	}
	if !slices.Equal(sites, cfg.SiteNames()) || findings != 1 {
		t.Errorf("sites %q, want %q; findings shown %d times\n%s", sites, cfg.SiteNames(), findings, rec.Lines())
	}
}

// --all stops at the first site that fails, naming it and how to resume,
// and reaches no site after it.
func TestHostPrepareAllStopsAtTheFirstFailure(t *testing.T) {
	loggingSSH(t, "awk -v fp=")
	fakeGrants(t, grantedAlready(t, fixtureConfig(), fixtureSecretsPath()))
	rec, err := prepareAll(t, hostPrepareOptions{execute: true})
	cfg, _ := config.Load(fixtureConfig())
	first := cfg.SiteNames()[0]
	if hint, explain := ui.Describe(err); hint != "host prepare --all stopped at "+first || !strings.Contains(explain, "paisans host prepare --all --execute") {
		t.Fatalf("got %q / %q\n%s", hint, explain, rec.Lines())
	}
	for _, name := range cfg.SiteNames()[1:] {
		if rec.Has("section", name) {
			t.Errorf("reached %s after %s failed\n%s", name, first, rec.Lines())
		}
	}
}

// --all is every site, so it takes no site, and no ssh destination, which
// would name one host for all of them.
func TestHostPrepareAllRefusesASiteOrAnSSHDestination(t *testing.T) {
	for _, o := range []hostPrepareOptions{{site: "home-a"}, {ssh: "admin@203.0.113.9"}} {
		_, err := prepareAll(t, o)
		var p *ui.Problem
		if !errors.As(err, &p) || !strings.Contains(p.Hint, "--all") {
			t.Errorf("%+v: got %v", o, err)
		}
	}
}

// Neither --site nor --all is refused naming both.
func TestHostPrepareNeedsASiteOrAll(t *testing.T) {
	err := hostPrepare(prepareEnv{r: ui.Discard, found: func(string, error) {}}, hostPrepareOptions{config: fixtureConfig()})
	if err == nil || !strings.Contains(err.Error(), "--site") || !strings.Contains(err.Error(), "--all") {
		t.Fatalf("got %v", err)
	}
}
