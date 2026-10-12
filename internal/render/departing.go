package render

// WithDepartingGateway renders site, which runs Caddy for a monitor, as the
// gateway it still is on its host: paisans-stack.yaml has given the role to
// another site, but until gateway move hands it off, DNS still sends every
// hostname there. Its Caddy keeps every gateway route beside its monitor's,
// the gate's snippets and the host owner's sites. A site that runs no Caddy
// is left as it is.
func WithDepartingGateway(site string) Option {
	return func(p *planner) {
		if s, ok := p.sites[site]; ok && s.RunsCaddy {
			s.IsGateway = true
		}
	}
}
