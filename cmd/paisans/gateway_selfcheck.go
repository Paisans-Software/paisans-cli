package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/selfcheck"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// gatewayProbe is how apply reaches the gateway from outside. Tests replace
// it, so that none reaches the network.
var gatewayProbe = selfcheck.DefaultProbe

// checkGatewayAnswers ends a gateway's apply: its Caddy must answer its own
// /.well-known/paisans/<token> through the public address, on 443 and with
// the redirect on 80. Another server there fails the step, whatever the host
// check saw, since a port taken by a DNAT rule shows no listener to it.
func checkGatewayAnswers(r ui.Reporter, cfg *config.Config, site string) error {
	s, ok := cfg.Sites[site]
	if !ok || !s.Has(config.RoleGateway) {
		return nil
	}
	const title = "check the gateway answers as this deployment"
	t, ok := selfcheck.TargetFor(cfg, site)
	if !ok {
		why := "no hostname to ask for"
		if s.PublicAddress == "" {
			why = "no public_address to ask through"
		}
		r.Step(title).End(ui.OK, why)
		return nil
	}
	step := r.Step(title)
	step.Detail("https://%s%s through %s, and http on 80 for the redirect", t.Host, selfcheck.Path(t.Deployment), t.Address)
	if err := selfcheck.Check(context.Background(), gatewayProbe(), t); err != nil {
		step.Fail(err)
		var c *selfcheck.Conflict
		if errors.As(err, &c) {
			return &ui.Problem{
				Hint: fmt.Sprintf("%d on %s answers, but not as this deployment", c.Port, c.Site),
				Explain: "It answered with " + c.Detail + ". Another server answers on that port of the gateway's public address: a container of the host owner's, a DNAT rule, or another web server started after the host was admitted. " +
					"The host's owner frees the port, or runs the site behind their web server (web_server.managed_by: external on a monitor), and apply runs again.",
				Cause: err,
			}
		}
		return err
	}
	step.Done("")
	return nil
}
