package main

import (
	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/gatewaymove"
)

// refuseSharedGatewayMove refuses to apply a site that paisans.yaml no
// longer gives the gateway role, that runs Caddy for a monitor, and whose
// host still holds the gateway's record or a move's marker. Applying it
// would rewrite its Caddy to the monitor's routes alone while DNS still sends
// every hostname there. apply without --site applies each site through here,
// so it is covered too.
func refuseSharedGatewayMove(cfg *config.Config, site string, t apply.Transport) error {
	s := cfg.Sites[site]
	if s.Has(config.RoleGateway) || !s.RunsCaddy() {
		return nil
	}
	held, err := gatewaymove.HoldsRecordOrMarker(t, cfg.Deployment())
	if err != nil {
		return err
	}
	if held {
		return gatewaymove.SharedRefusal(site)
	}
	return nil
}
