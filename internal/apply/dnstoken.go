package apply

import (
	"context"
	"errors"
	"time"

	"github.com/paisans-software/paisans-cli/internal/dns"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// checkDNSToken is the gate before Caddy asks the DNS provider for anything:
// the check of the token in the caddy.env this apply has just written, run
// from this site, since the provider judges the address a call comes from
// and a check from the workstation says nothing about the server.
//
// It stops the site's apply on a refusal, because a token Caddy cannot use
// gets no certificate and Caddy's retries trip the provider's lockout, which
// outlasts the fix. It stops on a provider that does not answer as well,
// because then the gate cannot tell a token Caddy can use from one it
// cannot, and starting Caddy on an unchecked token is what the gate exists
// to prevent. Founder decision. Either way nothing of the gateway has
// changed yet, so it keeps serving the certificates it holds.
func checkDNSToken(plan *Plan, t Transport) error {
	done := plan.step("check DNS token")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r := plan.DNSToken.OnSite(ctx, t, dns.CaddyEnvFile(plan.Deployment))
	switch r.Status {
	case dns.TokenPassed:
		done.Detail("%s", r.Finding)
		done.Done("")
		return nil
	case dns.TokenNoCheck:
		// A provider with no check is not gated: Caddy's own log is what
		// says whether its token works.
		done.Detail("%s, so the gateway is not gated on it", r.Finding)
		done.Done("")
		return nil
	}
	done.Fail(errors.New(r.Finding))
	lead := "Caddy could get no certificate with it, and its retries would lock the token out."
	if r.Unanswered {
		lead = "So nothing could say whether Caddy can use the token, and Caddy is not started on a token nobody checked."
	}
	return &ui.Problem{
		Hint: plan.Site + ": " + r.Finding,
		Explain: lead + " " + r.Advice + " Then apply again. The gateway was not changed.\n" +
			"`paisans doctor` checks the token from every place that uses it.",
	}
}
