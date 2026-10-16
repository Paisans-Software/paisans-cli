package doctor

import (
	"fmt"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/selfcheck"
)

// SectionSelfCheck is each Caddy site's own answer, asked from outside,
// after everything read on the sites.
const SectionSelfCheck = "self-check"

// SelfCheckProbe is whether a site running the toolkit's Caddy answered
// /.well-known/paisans/<token> as this deployment, fetched from the
// workstation with the check apply runs at the end of the gateway's step.
// Host and Address are what was asked; Unchecked is why nothing was. Err
// is the check's error, and Conflict the port that answered as something
// else, 0 when nothing answered.
type SelfCheckProbe struct {
	Site, Host, Address, Unchecked string
	Conflict                       int
	Err                            string
}

func (p SelfCheckProbe) site() string { return p.Site }

// SelfChecks judges each site's answer. The deployment's user cannot read
// the host's ruleset or another user's sockets, so a port taken after
// admission, and above all one taken by a DNAT rule with no listener, is
// invisible from the host; asking from outside is the one way to see it.
func SelfChecks(cfg *config.Config, probes []SelfCheckProbe) []Finding {
	path := selfcheck.Path(cfg.Deployment())
	var out []Finding
	for _, p := range probes {
		switch {
		case p.Unchecked != "":
			out = append(out, Finding{Section: SectionSelfCheck, Level: Warn,
				Line: fmt.Sprintf("%s: not asked from outside: %s", p.Site, p.Unchecked),
				More: []string{fmt.Sprintf("doctor asks each site that runs the toolkit's Caddy for %s through its public_address, as apply asks the gateway; without it, another server holding 443 or 80 there goes unseen.", path)}})
		case p.Err == "":
			out = append(out, Finding{Section: SectionSelfCheck, Level: OK,
				Line: fmt.Sprintf("%s: answers as this deployment on 443 and 80, asked for %s through %s", p.Site, p.Host, p.Address)})
		case p.Conflict != 0:
			out = append(out, Finding{Section: SectionSelfCheck, Level: Fail,
				Line: fmt.Sprintf("%s: %s", p.Site, firstLine(p.Err)),
				More: []string{
					fmt.Sprintf("doctor asked %s for https://%s%s from this workstation, as apply does at the end of the gateway's step, and another server answered on %d: a listener of the host owner's, a container of their system Docker, or a DNAT rule, which only root on the host can see.", p.Address, p.Host, path, p.Conflict),
					"recover:",
					fmt.Sprintf("  1. The host's owner finds what holds %d on %s: `sudo ss -Hltnp`, `sudo docker ps` for the system daemon, and `sudo nft list ruleset` for a DNAT rule.", p.Conflict, p.Site),
					"  2. They free the port; or the site runs behind their web server (web_server.managed_by: external, on a monitor), or the gateway moves to another site with `paisans gateway move`.",
					fmt.Sprintf("  3. `paisans doctor --site %s` again.", p.Site),
				}})
		default:
			out = append(out, Finding{Section: SectionSelfCheck, Level: Fail,
				Line: fmt.Sprintf("%s: %s", p.Site, firstLine(p.Err)),
				More: []string{
					fmt.Sprintf("doctor asked %s for https://%s%s from this workstation, once, and nothing answered.", p.Address, p.Host, path),
					"When the site is at home behind NAT and doctor runs from the same network, the router has to send its own public address back inside (hairpin NAT, or NAT loopback); many do not, and then doctor is run from outside that network.",
					fmt.Sprintf("Otherwise check that the router or the provider's firewall forwards 80 and 443 to %s, that public_address is the address they arrive on, and that its Caddy runs (containers %s).", p.Site, p.Site),
				}})
		}
	}
	return out
}
