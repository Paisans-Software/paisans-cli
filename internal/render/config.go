package render

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/configmerge"
)

// owningSetting is the setting that owns a kind's rendered key, if any: the
// `settings` key that is the sanctioned input for it
// (blueprints.Def.SettingKeys). When a passthrough key collides with one, the
// refusal names the setting rather than the template, because that is the way
// to change the value that somebody will review.
//
// Only keys a setting genuinely controls are recorded. A rendered key that
// merely depends on a setting among other things, such as Mbin's
// KBIN_STORAGE_URL, is not: naming a setting that only partly decides a value
// would send an operator to the wrong place. Element's base_url and
// server_name are fed by settings too, but they sit under "m.homeserver",
// which configmerge refuses as an ambiguous path before any collision is
// found, so an entry for them could never be reached.
//
// TestEverySettingThatOwnsAKeyReallyControlsIt renders each one with the
// setting changed and checks the key's line changes, so an entry that goes
// stale fails a test rather than misdirecting an operator.
func owningSetting(blueprint config.Blueprint, key string) (string, bool) {
	for _, o := range blueprints.SettingKeys(blueprint) {
		if o.Key == key {
			return o.Setting, true
		}
	}
	return "", false
}

// omittedOnPurpose is the key a kind's template leaves out of its config file
// on purpose that a passthrough key falls under, if any
// (blueprints.Def.OmittedKeys). Such a key is owned by the template exactly as
// a key it writes is, and a passthrough key that names it is refused as
// config-key-already-rendered. Only keys the template's own comments name as
// deliberately absent are recorded, so that the list records decisions rather
// than guesses.
func omittedOnPurpose(blueprint config.Blueprint, key string) (blueprints.OmittedKey, bool) {
	for _, o := range blueprints.OmittedKeys(blueprint) {
		if key == o.Key || strings.HasPrefix(key, o.Key+".") {
			return o, true
		}
	}
	return blueprints.OmittedKey{}, false
}

// mergers is the configmerge function for each format.
var mergers = map[blueprints.Format]func(string, map[string]any) (string, error){
	blueprints.ConfigEnv:  configmerge.Env,
	blueprints.ConfigINI:  configmerge.INI,
	blueprints.ConfigYAML: configmerge.YAML,
	blueprints.ConfigJSON: configmerge.JSON,
}

// mergeConfig places an app's passthrough keys into the one file its kind
// reads its configuration from, and into no other. files is the app's own
// rendered set and sources maps each of its paths to the template it came
// from, so a refusal can say which template owns the key.
//
// It is called only when the app declares keys, so an app without them
// renders exactly what its templates wrote.
func mergeConfig(app plannedApp, keys map[string]any, stack string, files []File, sources map[string]string) error {
	format := blueprints.ConfigFormat(app.Blueprint)
	merge, ok := mergers[format]
	if !ok {
		// Unreachable today: every kind renders a config file, so no rule in
		// validate refuses this first (that refusal was dropped for having
		// nothing to refuse). This is the guard for a future kind without one.
		return fmt.Errorf("apps.%s.config: kind %s renders no configuration file, so there is nowhere for a passthrough key to go", app.Name, app.Blueprint)
	}
	name := blueprints.ConfigFile(app.Blueprint)
	target := -1
	for i, f := range files {
		if f.Path == stack+name {
			target = i
		}
	}
	if target < 0 {
		return fmt.Errorf("apps.%s.config: kind %s reads its configuration from %s, but its template set rendered no such file", app.Name, app.Blueprint, name)
	}

	for _, key := range sortedKeys(keys) {
		if o, ok := omittedOnPurpose(app.Blueprint, key); ok {
			return collision(app, key, fmt.Sprintf("the template %s leaves %s out of %s/%s on purpose: %s. A key a template omits by decision is owned by the template as surely as one it writes, so a passthrough key may not put it back; that is a template change, and it is reviewable.",
				sources[files[target].Path], o.Key, app.Dir, name, o.Why))
		}
	}

	// Compose gives a service's `environment:` precedence over its env_file,
	// and interpolates ${VAR} from the .env beside it. An env key compose
	// already sets would be silently shadowed, and one it reads would change
	// the compose file rather than the application, so both are refused here
	// against the rendered compose.yaml.
	if format == blueprints.ConfigEnv {
		for _, f := range files {
			if f.Path != stack+"compose.yaml" {
				continue
			}
			for _, key := range sortedKeys(keys) {
				set, err := composeSets(f.Content, key)
				if err != nil {
					return fmt.Errorf("apps.%s.config: reading the rendered compose.yaml: %w", app.Name, err)
				}
				if set {
					return alreadyRendered(app, key, "compose.yaml", sources[f.Path],
						" Compose gives a service's environment: precedence over env_file and interpolates ${...} from .env, so the value would be shadowed or would change the compose file rather than the application.")
				}
			}
		}
	}

	out, err := merge(files[target].Content, keys)
	var collision *configmerge.Collision
	if errors.As(err, &collision) {
		return alreadyRendered(app, collision.Key, name, sources[files[target].Path], "")
	}
	if err != nil {
		return fmt.Errorf("apps.%s.config: %w", app.Name, err)
	}
	files[target].Content = out
	return nil
}

// alreadyRendered is the refusal for a passthrough key the stack already
// writes. Two sources of truth for one value is how a deployment ends up with
// a setting nobody can locate, and how the toolkit's own decisions get
// quietly undone, so it stops the build rather than overwriting.
func alreadyRendered(app plannedApp, key, file, template, why string) error {
	where := app.Dir + "/" + file
	if setting, ok := owningSetting(app.Blueprint, key); ok {
		return collision(app, key, fmt.Sprintf("%s already sets this key, and apps.%s.settings.%s is the sanctioned input for it. Set that instead and remove the key from config.%s",
			where, app.Name, setting, why))
	}
	return collision(app, key, fmt.Sprintf("%s already sets this key, written by the template %s. A passthrough key may only add what a template does not write: changing a template owned value is a settings key or a template change, and both are reviewable.%s",
		where, template, why))
}

// ConfigCollisionRule is the rule a passthrough key the stack already owns
// is refused under, by render and by validate alike.
const ConfigCollisionRule = "config-key-already-rendered"

// ConfigCollision is a passthrough key the stack already owns: one a template
// writes, one compose sets or reads, or one a template leaves out on purpose.
type ConfigCollision struct {
	App, Key string
	// Detail says who owns the key and what to do instead.
	Detail string
}

// ConfigKey is the key in paisans-stack.yaml: apps.<app>.config.<key>.
func (c *ConfigCollision) ConfigKey() string { return "apps." + c.App + ".config." + c.Key }

func (c *ConfigCollision) Error() string {
	return ConfigCollisionRule + ": " + c.ConfigKey() + ": " + c.Detail
}

func collision(app plannedApp, key, detail string) error {
	return &ConfigCollision{App: app.Name, Key: key, Detail: detail}
}

// ConfigCollisions is every passthrough key the stack already owns. Build
// stops at the first, so each key is rendered on its own here, with every
// other app's and every other key's passthrough left out: a key's collision
// does not depend on the others, and an operator fixing them one build at a
// time would meet them one at a time. Which keys a template writes depends on
// the configuration, not on secret values, so validate calls this with
// secrets it generated and throws away.
func ConfigCollisions(cfg *config.Config, secrets *config.Secrets) []*ConfigCollision {
	var out []*ConfigCollision
	for _, name := range cfg.AppNames() {
		for _, key := range sortedKeys(cfg.Apps[name].Config) {
			one := *cfg
			one.Apps = map[string]config.App{}
			for n, a := range cfg.Apps {
				a.Config = nil
				one.Apps[n] = a
			}
			app := one.Apps[name]
			app.Config = map[string]any{key: cfg.Apps[name].Config[key]}
			one.Apps[name] = app
			_, err := Build(&one, secrets)
			var c *ConfigCollision
			if errors.As(err, &c) {
				out = append(out, c)
			}
		}
	}
	return out
}

// interpolation matches compose's variable syntax: $$ is an escaped dollar,
// and ${NAME...} or $NAME reads NAME.
var interpolation = regexp.MustCompile(`\$(\$|\{([A-Za-z_][A-Za-z0-9_]*)|([A-Za-z_][A-Za-z0-9_]*))`)

// composeSets reports whether a rendered compose file sets key under any
// service's environment:, in map or list form, or reads it by interpolation
// anywhere. It reads the parsed document rather than the text, so a name that
// appears only in a comment does not count: compose never reads one.
func composeSets(compose, key string) (bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(compose), &doc); err != nil {
		return false, err
	}
	if len(doc.Content) == 0 {
		return false, nil
	}
	root := doc.Content[0]

	if services := mappingValue(root, "services"); services != nil && services.Kind == yaml.MappingNode {
		for i := 1; i < len(services.Content); i += 2 {
			env := mappingValue(services.Content[i], "environment")
			if env == nil {
				continue
			}
			switch env.Kind {
			case yaml.MappingNode:
				for j := 0; j < len(env.Content); j += 2 {
					if env.Content[j].Value == key {
						return true, nil
					}
				}
			case yaml.SequenceNode:
				for _, item := range env.Content {
					name, _, _ := strings.Cut(item.Value, "=")
					if name == key {
						return true, nil
					}
				}
			}
		}
	}

	return interpolates(root, key), nil
}

// mappingValue is the value under a key in a yaml mapping, or nil.
func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// interpolates reports whether any scalar under n reads key.
func interpolates(n *yaml.Node, key string) bool {
	if n.Kind == yaml.ScalarNode {
		for _, m := range interpolation.FindAllStringSubmatch(n.Value, -1) {
			if m[2] == key || m[3] == key {
				return true
			}
		}
		return false
	}
	for _, c := range n.Content {
		if interpolates(c, key) {
			return true
		}
	}
	return false
}
