package registry

import (
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// A site is one machine. Everything a deployment owns on a host is named for
// the deployment and nothing narrower: its root /srv/paisans/<token>, its
// manifest, its compose projects paisans-<token>-<stack>, its WireGuard
// interface psns-<token> and its mesh file /srv/paisans/<token>/psns-<token>.conf, and its one
// entry in this registry. Two sites of one deployment on one machine would
// write the same files, replace each other's containers and fight over one
// interface with two addresses. Roles combine on one site, and a second
// environment is a second deployment, which the registry keeps apart from the
// first; so nothing needs two sites of one deployment on one machine, and a
// claim that would put a second one there is refused.
//
// Which site of this deployment a host already runs is read from two places.
// This deployment's registry entry names its site, and every claim writes it,
// so it is the answer whenever there is an entry: it names the site rather
// than an address, which keeps it right when that site's address changes in
// paisans-stack.yaml. A host with no entry for this deployment but its WireGuard
// config still in place (a registry restored or edited by hand) is read by
// that file's Address, which is mapped to the site paisans-stack.yaml gives it.

// machineAdvice is what every refusal of a second site says.
const machineAdvice = "A site is a machine: its files, containers and WireGuard interface are named for the deployment, so a second site of it on this machine would overwrite the first. To combine roles, give one site several roles; a second environment is a second deployment."

// sameMachine refuses claiming the host t reaches for site when it already
// runs another site of cfg's deployment. r is the host's registry, nil when it
// could not be read, which leaves the WireGuard config to answer.
func sameMachine(t Runner, r *Registry, cfg *config.Config, site string) error {
	if r != nil {
		if e, ok := r.Deployments[cfg.ID]; ok && e.Site != "" {
			if e.Site == site {
				return nil
			}
			return otherSite(t, cfg, site, e.Site, e.Address, e.Kept)
		}
	}
	conf := "/" + cfg.Deployment().WireGuardConf()
	content, found, err := t.ReadFile(conf)
	if err != nil {
		return fmt.Errorf("%s: reading %s: %w", t.Describe(), conf, err)
	}
	if !found {
		return nil
	}
	address := ConfAddress(content)
	if address == "" || address == cfg.Sites[site].Address {
		return nil
	}
	for _, name := range cfg.SiteNames() {
		if cfg.Sites[name].Address == address {
			return otherSite(t, cfg, site, name, address, "")
		}
	}
	return fmt.Errorf("%s: this machine holds %s with this deployment's WireGuard address %s, and no site in "+config.FileName+" has that address, so %s cannot be deployed on it, and nothing was changed. It is left by a site of this deployment that no longer has the address: one taken out of "+config.FileName+", or one whose address changed with no registry entry to say which it was. %s If this machine is meant to be %s, clean what is left with `paisans site remove --force --ssh <this machine> --id %s` and run again; otherwise point sites.%s.ssh at another machine", t.Describe(), conf, address, site, machineAdvice, site, cfg.Deployment().Token(), site)
}

// otherSite is the refusal for a host that runs owner, another site of this
// deployment, at address, with kept naming what its site remove left there.
func otherSite(t Runner, cfg *config.Config, site, owner, address, kept string) error {
	what := "site " + owner
	if address != "" {
		what += " (" + address + ")"
	}
	what += " of this deployment"
	if kept != "" {
		what += ", or what its removal kept (" + kept + ")"
	}
	if _, declared := cfg.Sites[owner]; !declared {
		return fmt.Errorf("%s: this machine already runs %s, which "+config.FileName+" no longer declares, so %s cannot be deployed on it, and nothing was changed. %s If this machine is meant to be %s, clean %s off it with `paisans site remove %s --force --ssh <this machine>` and run again; otherwise point sites.%s.ssh at another machine", t.Describe(), what, site, machineAdvice, site, owner, owner, site)
	}
	return fmt.Errorf("%s: this machine already runs %s, so %s cannot be deployed on it, and nothing was changed. %s Point sites.%s.ssh at a machine of its own", t.Describe(), what, site, machineAdvice, site)
}

// ConfAddress is the address a wg-quick file's Address line gives the
// interface, without its prefix length, "" when it has none. The toolkit
// renders exactly one.
func ConfAddress(conf string) string {
	for _, line := range strings.Split(conf, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "Address") {
			continue
		}
		value = strings.TrimSpace(value)
		if first, _, ok := strings.Cut(value, ","); ok {
			value = strings.TrimSpace(first)
		}
		address, _, _ := strings.Cut(value, "/")
		return address
	}
	return ""
}
