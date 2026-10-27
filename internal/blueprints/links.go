package blueprints

import (
	"fmt"
	"slices"
	"strings"

	"cel.dev/cel-go/cel"
	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-cli/internal/config"
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

// Consume is one role a blueprint consumes. Role is the role. Setting is the
// app setting naming the provider's hostname, which is how the consumer says
// which provider it means. Optional says what an app that leaves the setting
// unset means: without it, a provider at the app's own hostname; with it, the
// deployment's one provider of the role, or no provider when there is none.
// SharedSecrets are the consumer's own self rotated secrets that its
// provider's templates read too, through .Consumers.
type Consume struct {
	Role          string   `yaml:"role"`
	Setting       string   `yaml:"setting"`
	Optional      bool     `yaml:"optional"`
	SharedSecrets []string `yaml:"shared_secrets"`
}

// Link is where one app's consume of a role leads.
type Link struct {
	Role     string
	Linked   bool   // false only for an optional consume with no provider
	Provider string // the providing app's name; "" for a provider outside the deployment
	Hostname string // the provider's hostname
}

// LinkOf is where the app called app in cfg is linked by its consume c. A
// setting naming a hostname links to the app there that provides the role,
// or to a provider outside the deployment at that hostname. An unset setting
// means the app's own hostname, the same way, unless c is optional: then it
// links to the deployment's one provider of the role, to none when there is
// none, and is refused when there are several.
func LinkOf(cfg *config.Config, app string, c Consume) (Link, error) {
	a := cfg.Apps[app]
	hostname, _ := SettingsSeen(a)[c.Setting].(string)
	if hostname == "" && c.Optional {
		switch providers := ProvidersOf(cfg, c.Role); len(providers) {
		case 0:
			return Link{Role: c.Role}, nil
		case 1:
			return Link{Role: c.Role, Linked: true, Provider: providers[0], Hostname: cfg.Apps[providers[0]].Hostname}, nil
		default:
			return Link{}, fmt.Errorf("apps.%s.settings.%s: unset, and %d apps provide %s (%s); name one by its hostname", app, c.Setting, len(providers), c.Role, strings.Join(providers, ", "))
		}
	}
	if hostname == "" {
		hostname = a.Hostname
	}
	link := Link{Role: c.Role, Linked: true, Hostname: hostname}
	for _, name := range ProvidersOf(cfg, c.Role) {
		if cfg.Apps[name].Hostname == hostname {
			link.Provider = name
			break
		}
	}
	return link, nil
}

// ProvidersOf is the apps in cfg whose blueprint provides role, in
// cfg.AppNames() order.
func ProvidersOf(cfg *config.Config, role string) []string {
	var out []string
	for _, name := range cfg.AppNames() {
		if slices.Contains(Provides(cfg.Apps[name].Blueprint), role) {
			out = append(out, name)
		}
	}
	return out
}

// ConsumersOf is the apps in cfg whose consume of role links to the app
// called provider, sorted. An app whose link cannot be resolved is left out,
// since validate refuses it.
func ConsumersOf(cfg *config.Config, provider, role string) []string {
	var out []string
	for _, name := range cfg.AppNames() {
		for _, c := range Consumes(cfg.Apps[name].Blueprint) {
			if c.Role != role {
				continue
			}
			if link, err := LinkOf(cfg, name, c); err == nil && link.Provider == provider {
				out = append(out, name)
			}
		}
	}
	return out
}

// SharedSecretProvider is the app in cfg that the app called app shares its
// secret key with: the provider in the deployment that the consume naming
// key links to. False when no consume shares key, or its link leads to no
// app in the deployment or cannot be resolved.
func SharedSecretProvider(cfg *config.Config, app, key string) (string, bool) {
	for _, c := range Consumes(cfg.Apps[app].Blueprint) {
		if !slices.Contains(c.SharedSecrets, key) {
			continue
		}
		if link, err := LinkOf(cfg, app, c); err == nil && link.Provider != "" {
			return link.Provider, true
		}
	}
	return "", false
}

// checkSharedSecrets refuses a shared secret that is not the consumer's own
// self rotated secret, or that is shared more than once.
func checkSharedSecrets(consumes []Consume, generate, selfRotated []string) error {
	shared := map[string]bool{}
	for i, c := range consumes {
		for j, name := range c.SharedSecrets {
			at := fmt.Sprintf("consumes[%d].shared_secrets[%d]", i, j)
			switch {
			case !slices.Contains(generate, name):
				return fmt.Errorf("%s: %s is not one of secrets.generate; a shared secret is the consumer's own", at, name)
			case !slices.Contains(selfRotated, name):
				return fmt.Errorf("%s: %s is not in secrets.self_rotated; rotating a shared secret re-renders the consumer and its provider, as a self rotated secret is", at, name)
			case shared[name]:
				return fmt.Errorf("%s: %s is shared twice", at, name)
			}
			shared[name] = true
		}
	}
	return nil
}

// Provides is the roles a blueprint provides.
func Provides(blueprint config.Blueprint) []string {
	var out []string
	for _, p := range of(blueprint).Provides {
		out = append(out, p.role)
	}
	return out
}

// Consumes is the roles a blueprint consumes.
func Consumes(blueprint config.Blueprint) []Consume {
	return append([]Consume(nil), of(blueprint).Consumes...)
}

// LinkValues is what the app called name in cfg offers as a provider of
// role: each value its blueprint declares, evaluated over the app.
func LinkValues(cfg *config.Config, name, role string) (map[string]string, error) {
	return linkValues(cfg.Apps[name].Blueprint, celAppIn(cfg, name), role)
}

// LinkValuesAt is what a provider of role outside the deployment, at
// hostname, offers, as any blueprint that provides role says: the values of
// an app at that hostname with nothing else declared. It is how a consumer
// pointed at a server the deployment does not run still gets its address.
func LinkValuesAt(role, hostname string) (map[string]string, error) {
	for _, blueprint := range known() {
		for _, p := range of(blueprint).Provides {
			if p.role == role {
				return linkValues(blueprint, celApp(config.App{Blueprint: blueprint, Hostname: hostname}), role)
			}
		}
	}
	return nil, fmt.Errorf("no blueprint provides the role %s", role)
}

func linkValues(blueprint config.Blueprint, vars map[string]any, role string) (map[string]string, error) {
	for _, p := range of(blueprint).Provides {
		if p.role != role {
			continue
		}
		out := map[string]string{}
		for value, prg := range p.values {
			v, _, err := prg.Eval(vars)
			if err != nil {
				return nil, fmt.Errorf("%s's provides %s.%s: %w", blueprint, role, value, err)
			}
			s, ok := v.Value().(string)
			if !ok {
				return nil, fmt.Errorf("%s's provides %s.%s is not a string", blueprint, role, value)
			}
			out[value] = s
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s does not provide the role %s", blueprint, role)
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
	consumed := map[string]bool{}
	for i, c := range consumes {
		if !config.BlueprintName(c.Role) {
			return nil, fmt.Errorf("consumes[%d].role: %q is not a role name: lowercase words joined by dashes", i, c.Role)
		}
		if consumed[c.Role] {
			return nil, fmt.Errorf("consumes[%d].role: %s is consumed twice; a template reads one link per role", i, c.Role)
		}
		consumed[c.Role] = true
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
	for _, l := range of(app.Blueprint).LockedSettings {
		if l.when != nil {
			if v, _, err := l.when.Eval(vars); err == nil && v.Value() == false {
				continue
			}
		}
		out = append(out, l.Name)
	}
	return out
}
