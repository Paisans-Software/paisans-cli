package doctor

import (
	"fmt"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/topology"
)

// SectionAdmission is each site's admission record against its
// declaration, read right after reach: every other read of the site needs
// it.
const SectionAdmission = "admission"

// AdmissionProbe is the site's admission record, read as the login user:
// it is mode 644, so the deployment's user reads it without sudo.
type AdmissionProbe struct {
	Site string
	// Found is whether the host holds a record for this deployment.
	Found  bool
	Record admission.Record
	// Err is why a record could not be read or was refused, one line, and
	// More the rest of it.
	Err  string
	More []string
	// Class is the run's classification of the site, asked only when the
	// record does not cover it; Unclassified is why the classification
	// refused, when it did.
	Class        topology.Site
	Unclassified string
}

func (p AdmissionProbe) site() string { return p.Site }

// Covered reports whether the record was read and covers the site as cfg
// declares it: the same test every command that writes to a host makes
// (admission.ForSite).
func (p AdmissionProbe) Covered(cfg *config.Config) bool {
	return p.Err == "" && p.Found && len(p.Record.Differences(cfg, p.Site)) == 0
}

// Pending reports whether the run's classification left the site out as
// pending: nothing more is asked of it.
func (p AdmissionProbe) Pending() bool { return p.Class.Class == topology.Pending }

// grantWhy is what each grant gives, by the flag that gives it.
var grantWhy = map[string]string{
	"watchdog":     "--watchdog grants Patroni the machine's watchdog, which a data site needs to fail over safely, and which can reboot the machine.",
	"public-ports": "--public-ports lets the deployment bind ports 80 and 443, which the gateway and a monitor running Paisans' Caddy serve on; every local user can then bind ports below 1024.",
}

// grantLines is what each missing grant gives, for the grants grantWhy
// knows; one it does not know is named by Differences already.
func grantLines(missing []string) []string {
	var out []string
	for _, g := range missing {
		if why, ok := grantWhy[g]; ok {
			out = append(out, why)
		}
	}
	return out
}

// Admission checks each site's admission record against its declaration. A
// site the record does not cover (none, unreadable, or differing, a
// missing grant included) is pending when the run's classification says
// so: the deployment runs without it, so it is a warning naming what it is
// without and how its owner admits it (a failure on a gateway,
// absentLevel). Any other site the record does not cover is a failure,
// with the classification's refusal: the deployment has joined it, or
// cannot tell, and apply stops until its owner admits it again.
func Admission(cfg *config.Config, probes []AdmissionProbe) []Finding {
	var out []Finding
	for _, p := range probes {
		if p.Covered(cfg) {
			out = append(out, Finding{Section: SectionAdmission, Level: OK, Line: fmt.Sprintf("%s: admitted as %s, covering its roles", p.Site, p.Record.User)})
			continue
		}
		var problem string
		var more []string
		switch {
		case p.Err != "":
			problem = "its admission record cannot be read"
			more = append([]string{p.Err}, p.More...)
		case !p.Found:
			problem = "this deployment is not admitted on its host"
			more = []string{fmt.Sprintf("Its login as %s works, and the host holds no admission record for this deployment: its owner has not admitted it, or has evicted it.", cfg.Sites[p.Site].SSHUser())}
		default:
			problem = "its admission does not cover it"
			more = append(p.Record.Differences(cfg, p.Site), grantLines(admission.Needs(cfg.Sites[p.Site]).Missing(p.Record.Grants))...)
		}
		if p.Pending() {
			out = append(out, pending(cfg, SectionAdmission, p.Site, fmt.Sprintf("%s: pending: %s", p.Site, problem), more...))
			continue
		}
		if p.Unclassified != "" {
			more = append(more, "The run's classification of the sites refuses: "+p.Unclassified)
		}
		more = append(more, "recover:", requestAdvice(p.Site))
		out = append(out, Finding{Section: SectionAdmission, Level: Fail, Line: fmt.Sprintf("%s: %s", p.Site, problem), More: more})
	}
	return out
}

// pending is a site the deployment runs without because its host has not
// admitted it, or not as declared: its roles, why, what the deployment is
// without until it joins, and how its owner admits it.
func pending(cfg *config.Config, section, site, line string, why ...string) Finding {
	more := append([]string{roleLine(cfg, site)}, why...)
	if lost := Losses(cfg, site); lost != "" {
		more = append(more, "Until it joins, the deployment is without "+lost+".")
	}
	more = append(more, "recover:", requestAdvice(site))
	return Finding{Section: section, Level: absentLevel(cfg, site), Line: line, More: more}
}

// absentLevel is how bad a site's absence is: a warning, since the
// deployment runs on the sites it has, and a failure for a gateway, since
// apply refuses to run without one.
func absentLevel(cfg *config.Config, site string) Level {
	if cfg.Sites[site].Has(config.RoleGateway) {
		return Fail
	}
	return Warn
}

// requestAdvice is how a site's owner admits it, or admits it again, in
// the words apply's refusal uses.
func requestAdvice(site string) string {
	return fmt.Sprintf("  1. `paisans host request --site %[1]s > %[1]s.admit` writes the request; its owner runs `sudo paisans host admit --request %[1]s.admit` on the host, granting what it asks for, and the next `paisans apply` joins it.", site)
}
