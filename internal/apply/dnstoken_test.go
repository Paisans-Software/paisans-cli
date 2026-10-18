package apply_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/render"
)

// cloudflareAnswers is a host whose Caddy holds a token, answering the DNS
// token check's command as curl would print Cloudflare's answer: zone is
// what the zone lookup answers, and verify what verify answers.
func cloudflareAnswers(zone string) func(string) (string, error) {
	return func(command string) (string, error) {
		switch {
		case strings.Contains(command, "/zones?"):
			return "user\n" + zone + "\n", nil
		case strings.Contains(command, "/tokens/verify"):
			return "user\n" + `{"success":true,"errors":[],"result":{"id":"t","status":"active"}}` + "\n200\n", nil
		}
		return "", fmt.Errorf("unexpected call: %s", command)
	}
}

const (
	zoneFound   = `{"success":true,"errors":[],"result":[{"id":"zone-1","name":"example.org","account":{"id":"acct-1"}}]}` + "\n200"
	zoneMissing = `{"success":true,"errors":[],"result":[]}` + "\n200"
	zone9109    = `{"success":false,"errors":[{"code":9109,"message":"Cannot use the access token from location: 203.0.113.7"}],"result":null}` + "\n403"
)

// withCloudflare is the plan with a DNS provider the toolkit has a token
// check for, in place of the fixture's desec.
func withCloudflare(p *render.Plan) *render.Plan {
	p.ACMEProvider = "cloudflare"
	return p
}

// A token the provider refuses from the server stops the site's apply
// before anything makes Caddy ask the provider: no validate, no reload, no
// start and no recreate, so the gateway keeps what it serves.
func TestTheDNSTokenGateStopsARefusal(t *testing.T) {
	for name, answer := range map[string]func(string) (string, error){
		"refused": cloudflareAnswers(zone9109),
		"no zone": func(command string) (string, error) {
			if strings.Contains(command, "/zones?") {
				return "user\n" + zoneMissing + "\n", nil
			}
			return "", fmt.Errorf("verify after a failed lookup: %s", command)
		},
		// An API that does not answer stops the apply as a refusal does,
		// because the gate cannot tell a usable token from one that is not.
		"no answer": func(string) (string, error) {
			return "user\ncurl: (28) Connection timed out after 20001 milliseconds\n", fmt.Errorf("exit status 28")
		},
	} {
		t.Run(name, func(t *testing.T) {
			host := applied(t, "vm")
			host.running = true
			host.dns = answer
			rec := &hostRecorder{host: host}
			p, err := apply.Build("vm", withCloudflare(planChangingOn(t, "vm", "srv/paisans/f2a9/infra/caddy/etc/Caddyfile", "srv/paisans/f2a9/infra/compose.yaml")), acmeModule(t), host, apply.Report(rec))
			if err != nil {
				t.Fatal(err)
			}
			err = apply.Execute(p, host)
			if err == nil {
				t.Fatal("the apply went on past a token the gate could not pass")
			}
			for _, gated := range []string{"caddy validate", "caddy reload", "up -d"} {
				if host.count(gated) > 0 {
					t.Errorf("%q ran after the gate stopped: %v", gated, host.commands)
				}
			}
			if !strings.Contains(err.Error(), "The gateway was not changed") || !strings.HasPrefix(err.Error(), "vm: ") {
				t.Errorf("error: %v", err)
			}
			if name == "refused" && !strings.Contains(err.Error(), "vm: cloudflare refuses the DNS token from this server (203.0.113.7)") {
				t.Errorf("the refusal names the address: %v", err)
			}
			if name == "no answer" && !strings.Contains(err.Error(), "cloudflare did not answer from this server") {
				t.Errorf("error: %v", err)
			}
			if !rec.Has("fail", "check DNS token") {
				t.Errorf("no failed step:\n%s", rec.Lines())
			}
		})
	}
}

// A token the provider accepts lets the apply go on, and the check runs
// after the module check and before validate.
func TestTheDNSTokenGateComesBeforeValidate(t *testing.T) {
	host := applied(t, "vm")
	host.running = true
	host.dns = cloudflareAnswers(zoneFound)
	rec := &hostRecorder{host: host}
	p, err := apply.Build("vm", withCloudflare(planChangingOn(t, "vm", "srv/paisans/f2a9/infra/caddy/etc/Caddyfile")), acmeModule(t), host, apply.Report(rec))
	if err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	modules, check, validate := host.indexOf("list-modules"), host.indexOf("ACME_DNS_TOKEN"), host.indexOf("caddy validate")
	if modules < 0 || check < modules || validate < check {
		t.Errorf("modules at %d, token check at %d, validate at %d: %v", modules, check, validate, host.commands)
	}
	if host.count("caddy reload") != 1 {
		t.Errorf("the apply did not go on to the reload: %v", host.commands)
	}
	if !rec.Has("done", "check DNS token") {
		t.Errorf("the step is not reported:\n%s", rec.Lines())
	}
}

// An apply that leaves Caddy alone makes no call: a site with no Caddy, and
// a gateway whose change is an app's alone.
func TestTheDNSTokenGateIsOnlyForCaddy(t *testing.T) {
	host := applied(t, "home-a")
	host.dns = cloudflareAnswers(zone9109)
	p, err := apply.Build("home-a", withCloudflare(planChangingOn(t, "home-a", "srv/paisans/f2a9/infra/compose.yaml")), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	if host.count("ACME_DNS_TOKEN") != 0 {
		t.Errorf("a site with no Caddy checked the token: %v", host.commands)
	}

	host = applied(t, "vm")
	host.dns = cloudflareAnswers(zone9109)
	p, err = apply.Build("vm", withCloudflare(plan(t)), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if p.DNSToken != nil {
		t.Errorf("nothing changed, yet the token is checked")
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	if host.count("ACME_DNS_TOKEN") != 0 {
		t.Errorf("an apply that leaves Caddy alone checked the token: %v", host.commands)
	}
}

// A provider the toolkit has no check for is not gated, and the step says
// so rather than reporting a pass.
func TestTheDNSTokenGateSaysWhenItHasNoCheck(t *testing.T) {
	host := applied(t, "vm")
	host.running = true
	rec := &hostRecorder{host: host}
	p, err := apply.Build("vm", planChangingOn(t, "vm", "srv/paisans/f2a9/infra/caddy/etc/Caddyfile"), acmeModule(t), host, apply.Report(rec))
	if err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	if host.count("ACME_DNS_TOKEN") != 0 {
		t.Errorf("a provider with no check was called: %v", host.commands)
	}
	if !strings.Contains(rec.Lines(), "no check for desec's tokens") {
		t.Errorf("the step does not say it has no check:\n%s", rec.Lines())
	}
}
