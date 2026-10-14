package blueprints

import (
	"fmt"

	"cel.dev/cel-go/cel"
	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// Links between apps. A blueprint may provide a named role, saying what it
// offers as CEL expressions over the app (each a string: a provider of
// matrix-homeserver offers base_url and server_name), and another consume
// it: the consumer's templates then read those values with
// .Linked "<role>" "<value>". A role is named for what it is, so a second
// blueprint can provide the same one, and the toolkit knows no role itself.

// Provide is one role a blueprint provides, with the expression for each
// value it offers.
type Provide struct {
	Role   string            `yaml:"role"`
	Values map[string]string `yaml:"values"`
}

// provided is a Provide compiled.
type provided struct {
	role   string
	values map[string]cel.Program
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
func Provides(kind config.Kind) []string {
	var out []string
	for _, p := range of(kind).Provides {
		out = append(out, p.role)
	}
	return out
}

// Consumes is the roles a kind consumes.
func Consumes(kind config.Kind) []Consume { return append([]Consume(nil), of(kind).Consumes...) }

// LinkValues is what the app called name in cfg offers as a provider of
// role: each value its blueprint declares, evaluated over the app.
func LinkValues(cfg *config.Config, name, role string) (map[string]string, error) {
	return linkValues(cfg.Apps[name].Kind, celAppIn(cfg, name), role)
}

// LinkValuesAt is what a provider of role outside the deployment, at
// hostname, offers, as any blueprint that provides role says: the values of
// an app at that hostname with nothing else declared. It is how a consumer
// pointed at a server the deployment does not run still gets its address.
func LinkValuesAt(role, hostname string) (map[string]string, error) {
	for _, kind := range known() {
		for _, p := range of(kind).Provides {
			if p.role == role {
				return linkValues(kind, celApp(config.App{Kind: kind, Hostname: hostname}), role)
			}
		}
	}
	return nil, fmt.Errorf("no blueprint provides the role %s", role)
}

func linkValues(kind config.Kind, vars map[string]any, role string) (map[string]string, error) {
	for _, p := range of(kind).Provides {
		if p.role != role {
			continue
		}
		out := map[string]string{}
		for value, prg := range p.values {
			v, _, err := prg.Eval(vars)
			if err != nil {
				return nil, fmt.Errorf("%s's provides %s.%s: %w", kind, role, value, err)
			}
			s, ok := v.Value().(string)
			if !ok {
				return nil, fmt.Errorf("%s's provides %s.%s is not a string", kind, role, value)
			}
			out[value] = s
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s does not provide the role %s", kind, role)
}

func checkLinks(provides []Provide, consumes []Consume) ([]provided, error) {
	var out []provided
	seen := map[string]bool{}
	for i, p := range provides {
		if !config.BlueprintName(p.Role) {
			return nil, fmt.Errorf("provides[%d].role: %q is not a role name: lowercase words joined by dashes", i, p.Role)
		}
		if seen[p.Role] {
			return nil, fmt.Errorf("provides[%d].role: %s is provided twice", i, p.Role)
		}
		seen[p.Role] = true
		if len(p.Values) == 0 {
			return nil, fmt.Errorf("provides[%d].values: required, what a consumer of %s reads", i, p.Role)
		}
		compiled := provided{role: p.Role, values: map[string]cel.Program{}}
		for _, value := range sortedKeys(p.Values) {
			prg, err := compileString(p.Values[value])
			if err != nil {
				return nil, fmt.Errorf("provides[%d].values.%s: %w", i, value, err)
			}
			compiled.values[value] = prg
		}
		out = append(out, compiled)
	}
	for i, c := range consumes {
		if !config.BlueprintName(c.Role) {
			return nil, fmt.Errorf("consumes[%d].role: %q is not a role name: lowercase words joined by dashes", i, c.Role)
		}
		if c.Setting == "" {
			return nil, fmt.Errorf("consumes[%d].setting: required, the setting naming the provider's hostname", i)
		}
	}
	return out, nil
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
