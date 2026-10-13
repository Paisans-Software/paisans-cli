package kinds

import (
	"fmt"

	"cel.dev/cel-go/cel"
	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// Links between apps. A blueprint may provide a named role, and another
// consume it: the consumer's templates then read what the toolkit works out
// about the provider with .Linked "<role>" "<value>". The roles are a closed
// set, because what each one offers is worked out in Go from the deployment,
// the same way whichever blueprint provides it. A role is named for what it
// is, so a second blueprint can provide the same one.

// LinkRoles are the roles a blueprint may provide or consume, with the values
// a consumer reads of each.
var LinkRoles = map[string][]string{
	// A Matrix homeserver: base_url is where its client API is, and
	// server_name the name inside every user identifier it mints.
	"matrix-homeserver": {"base_url", "server_name"},
}

// Consume is one role a blueprint consumes. Setting is the app setting
// naming the provider's hostname, which is how the consumer says which
// provider it means; an app that leaves it out means a provider at its own
// hostname.
type Consume struct {
	Role    string `yaml:"role"`
	Setting string `yaml:"setting"`
}

// Provides is the roles a kind provides.
func Provides(kind config.Kind) []string { return append([]string(nil), of(kind).Provides...) }

// Consumes is the roles a kind consumes.
func Consumes(kind config.Kind) []Consume { return append([]Consume(nil), of(kind).Consumes...) }

func checkLinks(provides []string, consumes []Consume) error {
	for _, role := range provides {
		if _, ok := LinkRoles[role]; !ok {
			return fmt.Errorf("provides: %q is not a role the toolkit links; it links matrix-homeserver", role)
		}
	}
	for i, c := range consumes {
		if _, ok := LinkRoles[c.Role]; !ok {
			return fmt.Errorf("consumes[%d].role: %q is not a role the toolkit links; it links matrix-homeserver", i, c.Role)
		}
		if c.Setting == "" {
			return fmt.Errorf("consumes[%d].setting: required, the setting naming the provider's hostname", i)
		}
	}
	return nil
}

// LockedSetting is one name a blueprint's `config.locked_settings` lists:
// a setting the configuration is the only source of, which the app refuses
// to change from its own admin pages. When, if set, is the condition under
// which it is locked.
type LockedSetting struct {
	Name string
	when cel.Program
}

// lockedSpec is a locked setting as a blueprint writes it: its name alone,
// or {name, when}.
type lockedSpec struct {
	Name string `yaml:"name"`
	When string `yaml:"when"`
}

func (l *lockedSpec) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&l.Name)
	}
	type plain lockedSpec
	return n.Decode((*plain)(l))
}

func lockedSettings(specs []lockedSpec) ([]LockedSetting, error) {
	var out []LockedSetting
	for i, s := range specs {
		if s.Name == "" {
			return nil, fmt.Errorf("config.locked_settings[%d]: needs a name", i)
		}
		l := LockedSetting{Name: s.Name}
		if s.When != "" {
			prg, err := compile(s.When)
			if err != nil {
				return nil, fmt.Errorf("config.locked_settings[%d].when: %w", i, err)
			}
			l.when = prg
		}
		out = append(out, l)
	}
	return out, nil
}

// LockedSettingsFor is the names an app's blueprint locks, in its order,
// the ones with a condition only when it holds. oidc is whether the app has
// a client at the identity provider, which is what app.oidc reads here. A
// condition that cannot be evaluated locks its setting, since a setting left
// unlocked is one an admin can change behind the configuration.
func LockedSettingsFor(app config.App, oidc bool) []string {
	var out []string
	vars := celApp(app)
	vars["app.oidc"] = oidc
	for _, l := range of(app.Kind).LockedSettings {
		if l.when != nil {
			if v, _, err := l.when.Eval(vars); err == nil && v.Value() == false {
				continue
			}
		}
		out = append(out, l.Name)
	}
	return out
}
