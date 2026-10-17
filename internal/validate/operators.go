package validate

import (
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/render"
)

// operatorsFewerThanTwo holds a deployment's Pocket ID sites to the operators
// they admit. Operators run the deployment: they hold the keys and the
// secrets, and every one of them is a Pocket ID user and a community admin.
// With none, nobody can run apply, sign a configuration or reach the identity
// provider's hosts, so it is refused. With one, a lost workstation leaves
// nobody able to, which is a risk rather than an incoherence: a deployment
// being rolled out, or a small community, may have one operator for a while.
// So it is a warning, and the operator reconciler fails the operators
// heartbeat while only one is declared. An operator counts only with a key on
// every site that runs a pocket-id app, since running the deployment means
// reaching each of them; a deployment with no pocket-id app is not held to it.
func (c *checker) operatorsFewerThanTwo() {
	var sites []string
	runs := render.AppSites(c.cfg)
	for _, name := range c.pocketIDApps() {
		for _, s := range runs[name] {
			if !slices.Contains(sites, s) {
				sites = append(sites, s)
			}
		}
	}
	if len(sites) == 0 {
		return
	}
	slices.Sort(sites)
	var counted []string
	for _, id := range sortedKeys(c.cfg.Operators) {
		on := true
		for _, s := range sites {
			on = on && slices.Contains(c.cfg.Sites[s].SSH.Operators, id)
		}
		if on {
			counted = append(counted, id)
		}
	}
	if len(counted) >= 2 {
		return
	}
	if len(counted) == 0 {
		c.refuse("operators-none", "operators",
			"no operator can reach the identity provider",
			"has no operator with a key on every site that runs Pocket ID (%s), so nobody could run the deployment. Declare an operator, and name them in ssh.keys on each of those sites.",
			strings.Join(sites, ", "))
		return
	}
	// The reconciler counts the operators declared, not those on these
	// sites, so it fails the heartbeat only when one is declared.
	heartbeat := ""
	if len(c.cfg.Operators) < 2 {
		heartbeat = " The operators heartbeat fails until there is a second."
	}
	c.warn("operators-fewer-than-two", "operators",
		"only one operator can reach the identity provider",
		"has only %s with a key on every site that runs Pocket ID (%s). One lost workstation would leave nobody able to run the deployment.%s Declare a second operator, and name both in ssh.keys on each of those sites.",
		counted[0], strings.Join(sites, ", "), heartbeat)
}
