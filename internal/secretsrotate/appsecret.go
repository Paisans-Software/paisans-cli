package secretsrotate

import (
	"fmt"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/render"
)

// A secret an app checks itself is checked by the stack that presents it: the
// app reads it from its own .env or configuration file when its containers
// start, and so does the part of the same stack that verifies it (valkey
// takes --requirepass from its command line at each recreate, and MAS and the
// homeserver are both in the synapse app's stack, which restarts together).
// So there is no checking side to change before the users: stage 2 has
// nothing to do, and stage 3's scoped apply is the whole of the rotation. It
// goes one site at a time, each healthy before the next, so with cluster
// placement the second site runs the old value while the first is checked.

// AppSelfKind is the kind whose secret key is, for each key AppSelf rotates,
// and false for any other key: what `secrets rotate` reads to find, in a
// deployment's apps, the ones whose secret it is. Each kind's record names
// its keys (blueprints.Def.SelfRotated).
func AppSelfKind(key string) (config.Kind, bool) { return blueprints.SelfRotatedKind(key) }

// appSelf rotates one secret an app checks itself.
type appSelf struct {
	Generated
	app, key string
	cost     Cost
}

// AppSelf is the rotation of apps.<app>.<key>, a secret checked by the same
// stack that uses it, one its blueprint lists under secrets.self_rotated.
// cost is what the plan says it costs, as the blueprint declares it
// (CostOf).
func AppSelf(app, key string, cost Cost) Rotator {
	return appSelf{Generated: Generated{SecretPath: "apps." + app + "." + key}, app: app, key: key, cost: cost}
}

func (r appSelf) Cost() Cost { return r.cost }

// Precheck refuses a key that is not one of the app's own, and an app that
// runs on no site, whose rotation would hand the value to nobody.
func (r appSelf) Precheck(e Env) error {
	app, ok := e.Cfg.Apps[r.app]
	if !ok {
		return fmt.Errorf("the configuration declares no app %q", r.app)
	}
	if spec, ok := blueprints.SecretOf(app.Kind, r.key); ok && spec.Group != "" {
		return fmt.Errorf("%s is rotated with the rest of its unit as apps.%s.%s", r.Path(), r.app, spec.Group)
	}
	kind, ok := AppSelfKind(r.key)
	if !ok {
		return fmt.Errorf("%s is not a secret its app checks itself", r.Path())
	}
	if app.Kind != kind {
		return fmt.Errorf("apps.%s is %s, and %s is a secret of %s", r.app, app.Kind, r.key, kind)
	}
	return runsSomewhere(e, r.app)
}

// Checked has nothing to do: the stack that checks the secret is the one
// stage 3 hands it to.
func (r appSelf) Checked(e Env, next string) error { return nil }

// Accepts is true: the app accepts whatever it is started with.
func (r appSelf) Accepts(e Env, next string) (bool, error) { return true, nil }

// Users is every site running the app, its own stack on each. Pocket ID's
// static_api_key is also presented by every invite app whose identity_app
// names it, from that app's .env, so each of those stacks is handed the next
// key too: on a site running both, in the same step with no order between the
// two stacks guaranteed; on a site running only the invite app, in a step of
// its own. The invite service calls Pocket ID only when it serves a request,
// so the window in which it holds a key Pocket ID does not yet accept is
// seconds. Without them the invite service would keep calling with a key
// Pocket ID no longer accepts.
func (r appSelf) Users(e Env) []Use {
	uses := appUses(e, r.app)
	if r.key != "static_api_key" {
		return uses
	}
	sites := render.AppSites(e.Cfg)
	bySite := map[string]int{}
	for i, u := range uses {
		bySite[u.Site] = i
	}
	for _, name := range e.Cfg.AppNames() {
		app := e.Cfg.Apps[name]
		if app.Kind != config.KindInvite {
			continue
		}
		if idp, _ := app.Settings["identity_app"].(string); idp != r.app {
			continue
		}
		for _, site := range sites[name] {
			if i, ok := bySite[site]; ok {
				uses[i].Stacks = append(uses[i].Stacks, name)
				continue
			}
			bySite[site] = len(uses)
			uses = append(uses, Use{Site: site, Stacks: []string{name}})
		}
	}
	return uses
}

// appUses is every site running app, as render places it (render.AppSites:
// its pinned site, or every apps site), its own stack on each, one at a time.
func appUses(e Env, app string) []Use {
	var out []Use
	for _, site := range render.AppSites(e.Cfg)[app] {
		out = append(out, Use{Site: site, Stacks: []string{app}})
	}
	return out
}

func runsSomewhere(e Env, app string) error {
	if len(render.AppSites(e.Cfg)[app]) == 0 {
		return fmt.Errorf("apps.%s runs on no site, so no stack would be handed the next value", app)
	}
	return nil
}
