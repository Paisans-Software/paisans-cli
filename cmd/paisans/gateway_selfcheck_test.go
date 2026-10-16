package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/selfcheck"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// apply's gateway step ends on the check; a conflict fails it in the
// spec's words.
func TestApplyFailsAGatewayThatAnswersAsAnotherServer(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	gw := cfg.GatewaySites()[0]
	s := cfg.Sites[gw]
	s.PublicAddress = "203.0.113.30"
	cfg.Sites[gw] = s
	saved := gatewayProbe
	defer func() { gatewayProbe = saved }()
	gatewayProbe = func() selfcheck.Probe {
		return selfcheck.Probe{Fetch: func(context.Context, string, string, string, string) (selfcheck.Answer, error) {
			return selfcheck.Answer{Status: 200, Body: "hello"}, nil
		}, Now: time.Now, Sleep: func(time.Duration) {}, Wait: time.Second}
	}
	err = checkGatewayAnswers(&ui.Recorder{}, cfg, gw)
	if err == nil || !strings.Contains(err.Error(), "443 on "+gw+" answers, but not as this deployment") {
		t.Errorf("got %v", err)
	}
	if err := checkGatewayAnswers(&ui.Recorder{}, cfg, "home-a"); err != nil {
		t.Errorf("a site that is not a gateway was checked: %v", err)
	}
}

// A gateway with no public_address cannot be checked: its line warns that
// the check did not run, rather than reading as passed.
func TestAGatewayWithNoPublicAddressIsNotCheckedAndSaysSo(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	gw := cfg.GatewaySites()[0]
	s := cfg.Sites[gw]
	s.PublicAddress = ""
	cfg.Sites[gw] = s
	rec := &ui.Recorder{}
	if err := checkGatewayAnswers(rec, cfg, gw); err != nil {
		t.Fatal(err)
	}
	if !rec.Has("warned", "check the gateway answers as this deployment") || rec.Has("done", "check the gateway answers as this deployment") {
		t.Errorf("the line:\n%s", rec.Lines())
	}
}

// A public address that does not answer from here fails with what to look
// at, the commonest cause first: a home gateway behind NAT, checked from
// the same LAN through a router without hairpin NAT.
func TestAGatewayThatDoesNotAnswerSaysWhy(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	gw := cfg.GatewaySites()[0]
	s := cfg.Sites[gw]
	s.PublicAddress = "203.0.113.30"
	cfg.Sites[gw] = s
	saved := gatewayProbe
	defer func() { gatewayProbe = saved }()
	gatewayProbe = func() selfcheck.Probe {
		return selfcheck.Probe{Fetch: func(context.Context, string, string, string, string) (selfcheck.Answer, error) {
			return selfcheck.Answer{}, errors.New("no route to host")
		}, Now: time.Now, Sleep: func(time.Duration) {}, Wait: time.Second}
	}
	err = checkGatewayAnswers(&ui.Recorder{}, cfg, gw)
	hint, explain := ui.Describe(err)
	if err == nil || !strings.Contains(hint, "203.0.113.30") || !strings.Contains(explain, "hairpin") || !strings.Contains(explain, "no route to host") {
		t.Errorf("got %q / %q", hint, explain)
	}
}
