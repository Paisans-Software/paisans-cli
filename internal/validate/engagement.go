package validate

import (
	"fmt"

	"github.com/paisans-software/paisans-cli/internal/config"
)

func (c *checker) engagementApps() []string {
	var out []string
	for _, name := range c.cfg.AppNames() {
		if c.cfg.Apps[name].Blueprint == config.BlueprintEngagement {
			out = append(out, name)
		}
	}
	return out
}

// engagement checks the weekly digest app. Every rule is silent when the
// deployment has no engagement app. The digest reads one Mbin's database
// directly, so it needs exactly one Mbin to read; the service is its own
// cluster-wide singleton, like the account service.
func (c *checker) engagement() {
	apps := c.engagementApps()
	if len(apps) == 0 {
		return
	}
	if len(apps) > 1 {
		for _, name := range apps[1:] {
			c.refuse("engagement-one-per-deployment", fmt.Sprintf("apps.%s", name),
				"one engagement app per deployment",
				"is a second engagement app. The digest reads the one Mbin and mails each member once a week, so a second would mail every member twice. Keep one engagement app.")
		}
	}
	for _, name := range apps {
		if c.cfg.Apps[name].Placement.Mode != config.PlacementCluster {
			c.refuse("engagement-must-be-clustered", fmt.Sprintf("apps.%s.placement", name),
				"engagement must have cluster placement",
				"is not cluster. The digest keeps its state in the cluster's Postgres and must keep sending when a site is lost, so it runs on every apps site. Set placement: cluster.")
		}
		if !c.cfg.CanMail(name) {
			c.warn("engagement-without-smtp", fmt.Sprintf("apps.%s", name),
				"engagement needs mail",
				"resolves no SMTP host and from_address. The digest is an email, and the service cannot send without them. Declare an smtp: block with a host and from_address.")
		}
	}

	mbins := c.cfg.EngagementSources()
	switch {
	case len(mbins) == 0:
		c.refuse("engagement-without-source", fmt.Sprintf("apps.%s", apps[0]),
			"an engagement app needs an mbin app",
			"is an engagement app, and no app has blueprint: mbin. The digest summarises a week of Mbin activity and has nothing to read without it. Declare an mbin app.")
	case len(mbins) > 1:
		c.refuse("engagement-one-source", fmt.Sprintf("apps.%s", apps[0]),
			"an engagement app needs exactly one mbin app",
			"is an engagement app, and this configuration has %d mbin apps. The digest reads one Mbin's database and could not tell which community to summarise. Declare one mbin app.", len(mbins))
	default:
		// The limit, in full: the digest connects with a read only role that
		// the toolkit creates in the cluster's Postgres, so only an Mbin with
		// cluster placement has a database that role can exist in. A pinned
		// Mbin runs against a Postgres of its own on its site, where no such
		// role is created, so the digest has no way to read it.
		if c.cfg.Apps[mbins[0]].Placement.Mode != config.PlacementCluster {
			c.refuse("engagement-source-not-clustered", fmt.Sprintf("apps.%s.placement", mbins[0]),
				"the engagement digest reads only a clustered mbin",
				"is pinned. The engagement digest reads Mbin only when Mbin has cluster placement, because its read only role is created in the cluster's Postgres.")
		}
	}
}
