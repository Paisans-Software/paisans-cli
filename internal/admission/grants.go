// Package admission is what passes between a deployment's operators and a
// host's owner: the request an operator sends (Build, Encode), the owner's
// strict reading of it (Parse) and the digest both compare (Digest), and the
// admission record host admit leaves on the host, which the deployment's own
// user reads without sudo (Read).
package admission

import "github.com/paisans-software/paisans-cli/internal/config"

// Grants are the owner's opt-in grants: Patroni's watchdog, which can
// reboot the machine, and ports below 1024, which every local user can then
// bind. In a request they are what the site's roles need; in a record they
// are what the owner gave.
type Grants struct {
	Watchdog    bool `yaml:"watchdog" json:"watchdog"`
	PublicPorts bool `yaml:"public_ports" json:"public_ports"`
}

// Needs is what a site's roles need granted: the watchdog for a data site,
// whatever its watchdog mode, since a Patroni member without one can keep
// acting as leader after losing its lock; low ports for a site that runs the
// toolkit's Caddy, a gateway or a monitor with managed_by: paisans, which
// binds 80 and 443.
func Needs(site config.Site) Grants {
	return Grants{Watchdog: site.Has(config.RoleData), PublicPorts: site.RunsCaddy()}
}

// Missing names each grant needed that granted lacks, in a fixed order,
// by the name the flag that gives it carries: watchdog, public-ports.
func (needed Grants) Missing(granted Grants) []string {
	var out []string
	if needed.Watchdog && !granted.Watchdog {
		out = append(out, "watchdog")
	}
	if needed.PublicPorts && !granted.PublicPorts {
		out = append(out, "public-ports")
	}
	return out
}
