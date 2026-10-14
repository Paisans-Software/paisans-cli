package validate

import (
	"slices"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/render"
)

// operatorsFewerThanTwo refuses a deployment whose Pocket ID sites admit
// fewer than two operators. Operators run the deployment: they hold the keys
// and the secrets, and every one of them is a Pocket ID user and a community
// admin. With one, a lost workstation leaves nobody able to run apply, sign
// a configuration or reach the identity provider's hosts. An operator counts
// only with a key on every site that runs a pocket-id app, since running the
// deployment means reaching each of them; a deployment with no pocket-id app
// is not held to it.
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
	who := "none"
	if len(counted) == 1 {
		who = "only " + counted[0]
	}
	c.refuse("operators-fewer-than-two", "operators",
		"fewer than two operators can reach the identity provider",
		"has %s with a key on every site that runs Pocket ID (%s). One lost workstation must not leave nobody able to run the deployment. Declare at least two operators, and name both in ssh.keys on each of those sites.",
		who, strings.Join(sites, ", "))
}
