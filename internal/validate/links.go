package validate

import (
	"errors"
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// links checks each app's consumes of a role another app provides, for the
// two ways a blueprint's link can be left unresolvable: an optional link with
// several providers and no setting to choose one, and a shared secret whose
// provider is not in the deployment to be handed it. An app whose link cannot
// be resolved is reported once, as the first, and skipped by the second. A
// link LinkOf cannot resolve for another reason is reported with its error.
// A colocated consume that links to an app in the deployment holds the app to
// that provider's placement (colocated). README.md, "Links between apps".
func (c *checker) links() {
	for _, app := range c.cfg.AppNames() {
		for _, consume := range blueprints.Consumes(c.cfg.Apps[app].Blueprint) {
			setting, _ := blueprints.SettingsSeen(c.cfg.Apps[app])[consume.Setting].(string)
			link, err := blueprints.LinkOf(c.cfg, app, consume)
			if err != nil && !errors.Is(err, blueprints.ErrLinkAmbiguous) {
				c.refuse("link-unresolvable", fmt.Sprintf("apps.%s.settings.%s", app, consume.Setting),
					"this app's link to its provider cannot be resolved",
					"%s", err.Error())
				continue
			}
			if err != nil {
				hostnames := c.providerHostnames(consume.Role)
				c.refuse("link-provider-ambiguous", fmt.Sprintf("apps.%s.settings.%s", app, consume.Setting),
					"several apps provide the role this app links to",
					"leaves %s unset, and %d apps provide %s: %s. Set it to one of those hostnames.",
					consume.Setting, len(hostnames), consume.Role, strings.Join(hostnames, ", "))
				continue
			}
			if consume.Colocate && link.Provider != "" {
				c.colocated(app, link)
			}
			if len(consume.SharedSecrets) == 0 || !link.Linked || link.Provider != "" {
				continue
			}
			key := fmt.Sprintf("apps.%s", app)
			if setting != "" {
				key = fmt.Sprintf("apps.%s.settings.%s", app, consume.Setting)
			}
			hostnames := c.providerHostnames(consume.Role)
			var remedy string
			switch {
			case setting != "" && len(hostnames) > 0:
				remedy = fmt.Sprintf("Set it to one of %s.", strings.Join(hostnames, ", "))
			case setting != "":
				remedy = "Remove the setting."
			case len(hostnames) > 0:
				remedy = fmt.Sprintf("Set settings.%s to one of %s.", consume.Setting, strings.Join(hostnames, ", "))
			default:
				remedy = fmt.Sprintf("Set settings.%s to a hostname that provides %s, or add an app that provides it.", consume.Setting, consume.Role)
			}
			c.refuse("link-shared-secret-outside", key,
				"a shared secret needs its provider in this deployment",
				"links to %s for %s, which is not an app this deployment renders. The secret can be handed only to an app this deployment renders. %s",
				link.Hostname, consume.Role, remedy)
		}
	}
}

// colocated refuses an app placed other than as the provider its colocated
// consume links to: pinned to another site, or pinned beside a clustered
// provider, or clustered beside a pinned one. The blueprint reaches what its
// provider keeps on its own host, and is placed there so that what it holds
// to do so stays on the host that already holds the provider.
func (c *checker) colocated(app string, link blueprints.Link) {
	mine, theirs := c.cfg.Apps[app].Placement, c.cfg.Apps[link.Provider].Placement
	if mine.Mode == theirs.Mode && (mine.Mode != config.PlacementPinned || mine.Site == theirs.Site) {
		return
	}
	want := "placement: cluster"
	if theirs.Mode == config.PlacementPinned {
		want = fmt.Sprintf("placement: { pinned: %s }", theirs.Site)
	}
	where := "is clustered"
	if mine.Mode == config.PlacementPinned {
		where = fmt.Sprintf("is pinned to %s", mine.Site)
	}
	runs := "is clustered"
	if theirs.Mode == config.PlacementPinned {
		runs = fmt.Sprintf("is pinned to %s", theirs.Site)
	}
	c.refuse("colocated-app-elsewhere", fmt.Sprintf("apps.%s.placement", app),
		"this app runs beside its provider, which is placed elsewhere",
		"%s, and its blueprint runs beside its %s provider, %s, which %s. It reaches what %s keeps on its own host, and runs there so that what it holds to do so stays on the host that already holds %s. Set %s.",
		where, link.Role, link.Provider, runs, link.Provider, link.Provider, want)
}

// providerHostnames is the hostnames of the apps that provide role.
func (c *checker) providerHostnames(role string) []string {
	var out []string
	for _, p := range blueprints.ProvidersOf(c.cfg, role) {
		out = append(out, c.cfg.Apps[p].Hostname)
	}
	return out
}
