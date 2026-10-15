package apply

import (
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/diagnosis"
	"github.com/paisans-software/paisans-cli/internal/dns"
	"github.com/paisans-software/paisans-cli/internal/render"
)

// ServedHostnames is each hostname a Caddy site of cfg serves, to that site:
// every gateway hostname (dns.GatewayHostnames) to the gateway, and an app's
// on a monitor to that monitor when Paisans' Caddy runs there. A monitor
// whose web server is the operator's own serves no hostname here, since a
// TLS failure on it is not Caddy's.
func ServedHostnames(cfg *config.Config) diagnosis.Served {
	out := diagnosis.Served{}
	if gateways := cfg.GatewaySites(); len(gateways) > 0 {
		gateway := strings.Join(gateways, " and ")
		for _, h := range dns.GatewayHostnames(cfg) {
			out[strings.ToLower(h.Name)] = gateway
		}
	}
	for _, name := range cfg.AppNames() {
		site, ok := render.ServedBy(cfg, name)
		if !ok || !cfg.Sites[site].RunsCaddy() {
			continue
		}
		if h := cfg.Apps[name].Hostname; h != "" {
			out[strings.ToLower(h)] = site
		}
	}
	return out
}
