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
// is the check's error, Conflict the port that answered as something
// else, and Silent the port that gave no answer, with URL what it was
// asked for and Why how it was silent; each 0 when it does not apply. A
// 443 that answers in anything but TLS is a Conflict. Pending and Down are
// the run's classification of the site: a pending site is not asked, and a
// down one is asked with its absence in mind.
type SelfCheckProbe struct {
	Site, Host, Address, Unchecked string
	Conflict, Silent               int
	URL, Err                       string
	Why                            selfcheck.Silence
	Pending, Down                  bool
}

// selfCheckPending is why a site the classification found pending is not
// asked from outside: apply has never deployed its Caddy.
const selfCheckPending = "not asked from outside: the run's classification found it pending admission, so apply has not deployed its Caddy (see admission)"

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
		case p.Pending:
			out = append(out, Finding{Section: SectionSelfCheck, Level: Warn,
				Line: fmt.Sprintf("%s: %s", p.Site, selfCheckPending)})
		case p.Unchecked != "":
			out = append(out, Finding{Section: SectionSelfCheck, Level: Warn,
				Line: fmt.Sprintf("%s: not asked from outside: %s", p.Site, p.Unchecked),
				More: []string{fmt.Sprintf("doctor asks each site that runs the toolkit's Caddy for %s through its public_address, as apply asks the gateway; without it, another server holding 443 or 80 there goes unseen.", path)}})
		case p.Err == "":
			out = append(out, Finding{Section: SectionSelfCheck, Level: OK,
				Line: fmt.Sprintf("%s: answers as this deployment on 443 and 80, asked for %s through %s", p.Site, p.Host, p.Address)})
		case p.Down:
			asked := p.silence(path)
			if p.Conflict != 0 {
				asked = fmt.Sprintf("doctor asked %s for https://%s%s from this workstation, once, and another server answered on %d.", p.Address, p.Host, path, p.Conflict)
			}
			out = append(out, Finding{Section: SectionSelfCheck, Level: absentLevel(cfg, p.Site),
				Line: fmt.Sprintf("%s: %s", p.Site, firstLine(p.Err)),
				More: []string{asked,
					fmt.Sprintf("The run's classification found %s down (see reach): its absence is the cause to look at first. Once its ssh answers, `paisans doctor --site %s` again.", p.Site, p.Site),
				}})
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
				More: append([]string{p.silence(path)}, p.silenceAdvice()...)})
		}
	}
	return out
}

// silence says what doctor asked that gave no answer, and how: the URL of
// the port that was silent, the HTTPS one when the check did not say.
func (p SelfCheckProbe) silence(path string) string {
	url := p.URL
	if url == "" {
		url = "https://" + p.Host + path
	}
	asked := fmt.Sprintf("doctor asked %s for %s from this workstation, once, and ", p.Address, url)
	switch p.Why {
	case selfcheck.Refused:
		return asked + "the connection was refused."
	case selfcheck.TimedOut:
		return asked + "nothing answered before it timed out."
	}
	return asked + "nothing answered."
}

// hairpin is the advice for a site at home asked from its own network.
const hairpin = "When the site is at home behind NAT and doctor runs from the same network, the router has to send its own public address back inside (hairpin NAT, or NAT loopback); many do not, and then doctor is run from outside that network."

// silenceAdvice is where to look for a port that gave no answer. A refusal
// came from whatever holds the address, so it points first at the site's
// own listener; a timeout means something on the way dropped the
// connection or a listener accepted it and never answered, so it points at
// the router, the firewall and a hung Caddy.
func (p SelfCheckProbe) silenceAdvice() []string {
	port := p.Silent
	if port == 0 {
		port = 443
	}
	switch p.Why {
	case selfcheck.Refused:
		return []string{
			fmt.Sprintf("A refusal means nothing listens on %d at %s, or something on the way rejects the connection: check that %s's Caddy runs (containers %s), that the router or the provider's firewall forwards 80 and 443 to %s, and that public_address is the address they arrive on.", port, p.Address, p.Site, p.Site, p.Site),
			hairpin,
		}
	case selfcheck.TimedOut:
		return []string{
			fmt.Sprintf("A timeout means something on the way dropped the connection, or a listener accepted it and never answered: check that the router or the provider's firewall forwards 80 and 443 to %s, that public_address is the address they arrive on, and that %s's Caddy is not hung (containers %s).", p.Site, p.Site, p.Site),
			hairpin,
		}
	}
	return []string{
		hairpin,
		fmt.Sprintf("Otherwise check that the router or the provider's firewall forwards 80 and 443 to %s, that public_address is the address they arrive on, and that its Caddy runs (containers %s).", p.Site, p.Site),
	}
}
