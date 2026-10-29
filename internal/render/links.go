package render

import (
	"fmt"
	"slices"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
)

// Consumer is one app linked to this one by a role its blueprint provides:
// the app's name, each of its shared_secrets for that role that is set,
// keyed by the secret's name, and the values it shows its providers
// (consumerValues).
type Consumer struct {
	Name    string
	Secrets map[string]string
	Values  map[string]string
}

// linkValues fills v's links, linkedTo and consumers: where each role the
// app's blueprint consumes leads (blueprints.LinkOf) and what its provider
// offers there, and which apps consume each role it provides.
func (p *planner) linkValues(v *appValues) error {
	for _, c := range blueprints.Consumes(v.App.Blueprint) {
		link, err := blueprints.LinkOf(p.cfg, v.App.Name, c)
		if err != nil {
			return err
		}
		if !link.Linked {
			continue
		}
		values, err := p.linked(link)
		if err != nil {
			return err
		}
		if v.links == nil {
			v.links, v.linkedTo = map[string]map[string]string{}, map[string]blueprints.Link{}
		}
		v.links[c.Role] = values
		v.linkedTo[c.Role] = link
	}
	for _, role := range blueprints.Provides(v.App.Blueprint) {
		if v.consumers == nil {
			v.consumers = map[string][]Consumer{}
		}
		consumers := []Consumer{}
		for _, name := range blueprints.ConsumersOf(p.cfg, v.App.Name, role) {
			consumers = append(consumers, Consumer{Name: name, Secrets: p.sharedSecrets(name, role), Values: p.consumerValues(name)})
		}
		v.consumers[role] = consumers
	}
	return nil
}

// sharedSecrets is each of the shared_secrets the app called app's consume of
// role names whose value is a non-empty string.
func (p *planner) sharedSecrets(app, role string) map[string]string {
	out := map[string]string{}
	for _, c := range blueprints.Consumes(p.cfg.Apps[app].Blueprint) {
		if c.Role != role {
			continue
		}
		for _, key := range c.SharedSecrets {
			if s, ok := p.secrets.Apps[app][key].(string); ok && s != "" {
				out[key] = s
			}
		}
	}
	return out
}

// linked is what the provider link leads to offers: the app's own values in
// the deployment, or, outside it, what any blueprint providing the role says
// of the hostname, so a consumer pointed at a server the deployment does not
// run still gets its address.
func (p *planner) linked(link blueprints.Link) (map[string]string, error) {
	if link.Provider != "" {
		return blueprints.LinkValues(p.cfg, link.Provider, link.Role)
	}
	return blueprints.LinkValuesAt(link.Role, link.Hostname)
}

// consumes is the app's blueprint's consume of role, false when it has none.
func (v appValues) consumes(role string) (blueprints.Consume, bool) {
	for _, c := range blueprints.Consumes(v.App.Blueprint) {
		if c.Role == role {
			return c, true
		}
	}
	return blueprints.Consume{}, false
}

// HasLinked is whether role is consumed by the app's blueprint and linked to
// a provider. Only an optional consume with no provider leaves a consumed
// role unlinked, and a template reading it guards .Linked with this.
func (v appValues) HasLinked(role string) bool {
	_, ok := v.links[role]
	return ok
}

// Linked is a value the provider of a role the app's blueprint consumes
// offers, as the provider's blueprint declares it. A value the provider does
// not declare, a role the blueprint does not consume, or one left unlinked
// fails the render, naming it, rather than rendering an empty string.
func (v appValues) Linked(role, value string) (string, error) {
	values, ok := v.links[role]
	if !ok {
		if _, consumed := v.consumes(role); consumed {
			return "", fmt.Errorf("the role %s is not linked: guard .Linked with .HasLinked", role)
		}
		return "", fmt.Errorf("the blueprint reads .Linked %q, a role it does not consume", role)
	}
	got, ok := values[value]
	if !ok {
		return "", fmt.Errorf("a provider of %s offers no value %q", role, value)
	}
	return got, nil
}

// LinkedOffers is whether role is linked and its provider offers value, so a
// template can read a value only some providers declare.
func (v appValues) LinkedOffers(role, value string) bool {
	_, ok := v.links[role][value]
	return ok
}

// LinkedApp is the name of the app in the deployment that role is linked
// to. An unlinked role, or a provider outside the deployment, fails the
// render.
func (v appValues) LinkedApp(role string) (string, error) {
	link, ok := v.linkedTo[role]
	if !ok {
		return "", fmt.Errorf("the role %s is not linked", role)
	}
	if link.Provider == "" {
		return "", fmt.Errorf("the provider of %s at %s is outside the deployment, so it has no app name", role, link.Hostname)
	}
	return link.Provider, nil
}

// Consumers is the apps linked to this one by role, sorted by name, each
// with its shared secrets for role. A role the app's blueprint does not
// provide fails the render.
func (v appValues) Consumers(role string) ([]Consumer, error) {
	consumers, ok := v.consumers[role]
	if !ok {
		return nil, fmt.Errorf("the blueprint reads .Consumers %q, a role it does not provide", role)
	}
	return slices.Clone(consumers), nil
}
