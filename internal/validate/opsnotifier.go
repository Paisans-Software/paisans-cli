package validate

import (
	"fmt"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// opsNotifier checks that an ops-notifier app has what it serves and runs
// on: a homeserver in the deployment whose MAS admin API it can reach, and a
// Beszel hub in the deployment, and that there is one of it. README.md,
// "Server and container stats", and docs/specs/2026-10-29-ops-notifier.md.
//
// Both links are optional consumes, so an app with no provider would render
// with no Matrix and no hub, and the service, refusing to start without
// either, would restart forever. A provider outside the deployment is no
// better: MAS's admin listener and the hub's superuser API are reached on a
// mesh address, which only an app in the deployment has.
//
// One app, because each operator gets one bot room and one alert URL, and a
// second app would give them a second of each from another bot.
func (c *checker) opsNotifier() {
	var notifiers []string
	for _, name := range c.cfg.AppNames() {
		if c.cfg.Apps[name].Blueprint != config.BlueprintOpsNotifier {
			continue
		}
		notifiers = append(notifiers, name)
		key := fmt.Sprintf("apps.%s", name)
		for _, cons := range blueprints.Consumes(config.BlueprintOpsNotifier) {
			link, err := blueprints.LinkOf(c.cfg, name, cons)
			if err != nil {
				continue // links reports an ambiguous one
			}
			switch cons.Role {
			case "matrix-homeserver":
				offers := false
				if link.Provider != "" {
					values, err := blueprints.LinkValues(c.cfg, link.Provider, cons.Role)
					offers = err == nil && values["mas_admin_url"] != ""
				}
				if !offers {
					c.refuse("ops-notifier-without-homeserver", key,
						"ops-notifier app with no homeserver in the deployment",
						"needs a Matrix homeserver app in this deployment whose MAS admin API it can reach, to make and keep its bot, and there is none. Declare one (blueprint synapse), or remove the app.")
				}
			case "beszel-hub":
				if link.Provider == "" {
					c.refuse("ops-notifier-without-hub", key,
						"ops-notifier app with no Beszel hub in the deployment",
						"serves the operators of a Beszel hub in this deployment, and there is none. Declare a beszel app, or remove this one.")
				}
			}
		}
	}
	if len(notifiers) > 1 {
		for _, name := range notifiers[1:] {
			c.refuse("one-ops-notifier", fmt.Sprintf("apps.%s", name),
				"one ops-notifier app per deployment",
				"is a second ops-notifier app, beside %s. Each operator gets one bot room and one alert URL, and a second app would give them a second of each. Keep one ops-notifier app.", notifiers[0])
		}
	}
}
