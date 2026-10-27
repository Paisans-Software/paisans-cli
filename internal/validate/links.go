package validate

import (
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
)

// links checks each app's consumes of a role another app provides, for the
// two ways a blueprint's link can be left unresolvable: an optional link with
// several providers and no setting to choose one, and a shared secret whose
// provider is not in the deployment to be handed it. An app whose link cannot
// be resolved is reported once, as the first, and skipped by the second.
func (c *checker) links() {
	for _, app := range c.cfg.AppNames() {
		for _, consume := range blueprints.Consumes(c.cfg.Apps[app].Blueprint) {
			setting, _ := blueprints.SettingsSeen(c.cfg.Apps[app])[consume.Setting].(string)
			link, err := blueprints.LinkOf(c.cfg, app, consume)
			if err != nil {
				hostnames := c.providerHostnames(consume.Role)
				c.refuse("link-provider-ambiguous", fmt.Sprintf("apps.%s.settings.%s", app, consume.Setting),
					"several apps provide the role this app links to",
					"leaves %s unset, and %d apps provide %s: %s. Set it to one of those hostnames.",
					consume.Setting, len(hostnames), consume.Role, strings.Join(hostnames, ", "))
				continue
			}
			if len(consume.SharedSecrets) == 0 || !link.Linked || link.Provider != "" {
				continue
			}
			key := fmt.Sprintf("apps.%s", app)
			if setting != "" {
				key = fmt.Sprintf("apps.%s.settings.%s", app, consume.Setting)
			}
			hostnames := c.providerHostnames(consume.Role)
			remedy := "Remove the setting."
			if len(hostnames) > 0 {
				remedy = fmt.Sprintf("Set it to one of %s.", strings.Join(hostnames, ", "))
			}
			c.refuse("link-shared-secret-outside", key,
				"a shared secret needs its provider in this deployment",
				"links to %s for %s, which is not an app this deployment renders. The secret can be handed only to an app this deployment renders. %s",
				link.Hostname, consume.Role, remedy)
		}
	}
}

// providerHostnames is the hostnames of the apps that provide role.
func (c *checker) providerHostnames(role string) []string {
	var out []string
	for _, p := range blueprints.ProvidersOf(c.cfg, role) {
		out = append(out, c.cfg.Apps[p].Hostname)
	}
	return out
}
