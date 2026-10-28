package secretsrotate

import (
	"bytes"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// The rotations a blueprint declares (blueprints.SecretSpec): the cost it
// names, a group of secrets replaced as one unit, and a secret changed inside
// the running app by an in-app action.

// CostOf is what rotating an app's secret costs, as its blueprint says:
// nothing a member notices, for a secret it says nothing of. A restart of a
// secret shared with a linked provider is a restart of both stacks.
func CostOf(cfg *config.Config, app, key string) Cost {
	spec, _ := blueprints.SecretOf(cfg.Apps[app].Blueprint, key)
	if provider, ok := blueprints.SharedSecretProvider(cfg, app, key); ok && spec.RotationCost == blueprints.CostRestart {
		return Cost{Downtime: "the " + app + " and " + provider + " stacks, for one restart on each site in turn"}
	}
	return declaredCost(app, spec.RotationCost)
}

func declaredCost(app, cost string) Cost {
	switch cost {
	case blueprints.CostSignsOut:
		return Cost{SignsOut: true}
	case blueprints.CostRestart:
		return Cost{Downtime: "the " + app + " stack, for one restart on each site in turn"}
	}
	return Cost{}
}

// appGroup rotates the secrets of one app that its blueprint groups as a
// unit, as apps.<app>.<group>.
type appGroup struct {
	cfg        *config.Config
	app, group string
}

// AppGroup is the rotation of an app's secret unit: every secret its
// blueprint puts in group, replaced together, because a keypair whose
// passphrase changed alone is one the app cannot open. Its next value is a
// small YAML document holding every member, which Promote sets from. Every
// site running the app is handed the unit in turn, and all of them end on
// the same values.
func AppGroup(cfg *config.Config, app, group string) Rotator {
	return appGroup{cfg: cfg, app: app, group: group}
}

func (r appGroup) Path() string { return "apps." + r.app + "." + r.group }

func (r appGroup) key(k string) string { return "apps." + r.app + "." + k }

func (r appGroup) members() []blueprints.SecretSpec {
	return blueprints.SecretGroup(r.cfg.Apps[r.app].Blueprint, r.group)
}

// Cost is the dearest of the members': signing out, then a restart.
func (r appGroup) Cost() Cost {
	out := Cost{}
	for _, m := range r.members() {
		c := declaredCost(r.app, m.RotationCost)
		if c.SignsOut {
			return c
		}
		if c.Downtime != "" {
			out = c
		}
	}
	return out
}

func (r appGroup) Precheck(e Env) error {
	if _, ok := e.Cfg.Apps[r.app]; !ok {
		return fmt.Errorf("the configuration declares no app %q", r.app)
	}
	if len(r.members()) == 0 {
		return fmt.Errorf("apps.%s's blueprint groups no secrets as %s", r.app, r.group)
	}
	return runsSomewhere(e, r.app)
}

// Next is a new value for every member: a keypair encrypted under its
// passphrase's new value, and its public half beside it.
func (r appGroup) Next(e Env, s *config.Secrets) (string, error) {
	members := r.members()
	next := map[string]string{}
	for _, m := range members {
		if m.Format == blueprints.FormatRSAKeypair || m.Format == blueprints.FormatRSAPublic {
			continue
		}
		v, err := secretsgen.ReplacementIn(e.Cfg, r.key(m.Name), s)
		if err != nil {
			return "", err
		}
		ui.Secret(v)
		next[m.Name] = v
	}
	for _, m := range members {
		if m.Format != blueprints.FormatRSAKeypair {
			continue
		}
		priv, pub, err := secretsgen.ReplacementKeypair(next[m.Passphrase])
		if err != nil {
			return "", err
		}
		ui.Secret(priv)
		ui.Secret(pub)
		next[m.Name] = priv
		for _, half := range members {
			if half.Format == blueprints.FormatRSAPublic && half.Of == m.Name {
				next[half.Name] = pub
			}
		}
	}
	out, err := yaml.Marshal(next)
	if err != nil {
		return "", fmt.Errorf("writing the next %s unit", r.Path())
	}
	return string(out), nil
}

// parse reads a next value, registering each value as a secret so none is
// shown, and checks it holds every member and that each keypair opens with
// its passphrase and matches its public half. Its errors name no value.
func (r appGroup) parse(next string) (map[string]string, error) {
	var values map[string]string
	if err := yaml.NewDecoder(bytes.NewReader([]byte(next))).Decode(&values); err != nil {
		return nil, fmt.Errorf("the next value is not the %s unit", r.Path())
	}
	members := r.members()
	for _, m := range members {
		v := values[m.Name]
		if v == "" {
			return nil, fmt.Errorf("the next value has no %s", m.Name)
		}
		ui.Secret(v)
	}
	if len(values) != len(members) {
		return nil, fmt.Errorf("the next value holds secrets the %s unit does not", r.Path())
	}
	for _, m := range members {
		if m.Format != blueprints.FormatRSAKeypair {
			continue
		}
		key, err := secretsgen.OpenPrivateKey(values[m.Name], values[m.Passphrase])
		if err != nil {
			return nil, fmt.Errorf("the next %s does not open with the next %s, and the app could not sign with it", m.Name, m.Passphrase)
		}
		for _, half := range members {
			if half.Format != blueprints.FormatRSAPublic || half.Of != m.Name {
				continue
			}
			block, _ := pem.Decode([]byte(values[half.Name]))
			if block == nil {
				return nil, fmt.Errorf("the next %s is not PEM", half.Name)
			}
			public, err := x509.ParsePKIXPublicKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("the next %s does not parse: %w", half.Name, err)
			}
			if pub, ok := public.(*rsa.PublicKey); !ok || !pub.Equal(&key.PublicKey) {
				return nil, fmt.Errorf("the next %s is not the next %s's half", half.Name, m.Name)
			}
		}
	}
	return values, nil
}

// Checked has nothing to do: the app checks its own, with the unit stage 3
// hands it.
func (r appGroup) Checked(e Env, next string) error { return nil }

func (r appGroup) Accepts(e Env, next string) (bool, error) { return true, nil }

func (r appGroup) Users(e Env) []Use { return appUses(e, r.app) }

// Promote sets every member from the unit, once it is checked. Setting a
// value twice changes nothing, so it is safe to run twice.
func (r appGroup) Promote(s *config.Secrets, next string) error {
	values, err := r.parse(next)
	if err != nil {
		return err
	}
	for _, m := range r.members() {
		if err := s.Set(r.key(m.Name), values[m.Name]); err != nil {
			return fmt.Errorf("%s: setting it", r.key(m.Name))
		}
	}
	return nil
}

// inApp rotates a secret the running app keeps in its own data, changing it
// there with the commands its blueprint declares before the stack is handed
// the new value.
type inApp struct {
	Generated
	app, key string
	rot      blueprints.InAppRotation
	cost     Cost
}

// InApp is the rotation of apps.<app>.<key>, a secret its blueprint changes
// inside the running app: stage 2 runs the blueprint's change command in its
// service on every site the app runs on, with the new value on stdin; the
// check command then says whether each accepts it; stage 3 hands the stack
// the same value. A value handed to a command never begins with a dash,
// which a command would read as an option.
func InApp(cfg *config.Config, app, key string) Rotator {
	spec, _ := blueprints.SecretOf(cfg.Apps[app].Blueprint, key)
	r := inApp{Generated: Generated{SecretPath: "apps." + app + "." + key}, app: app, key: key, cost: declaredCost(app, spec.RotationCost)}
	if spec.Rotate != nil {
		r.rot = *spec.Rotate
	}
	return r
}

// inAppGenerate is where a new value comes from; a test replaces it.
var inAppGenerate = secretsgen.ReplacementIn

func (r inApp) Next(e Env, s *config.Secrets) (string, error) {
	for i := 0; i < 64; i++ {
		v, err := inAppGenerate(e.Cfg, r.SecretPath, s)
		if err != nil {
			return "", err
		}
		if !strings.HasPrefix(v, "-") {
			return v, nil
		}
	}
	return "", fmt.Errorf("%s: no value without a leading dash after 64 draws", r.SecretPath)
}

func (r inApp) Cost() Cost { return r.cost }

func (r inApp) Precheck(e Env) error {
	app, ok := e.Cfg.Apps[r.app]
	if !ok {
		return fmt.Errorf("the configuration declares no app %q", r.app)
	}
	if r.rot.Service == "" {
		return fmt.Errorf("%s: its blueprint declares no rotation inside the app", r.Path())
	}
	if !r.rot.Applies(app) {
		return &KeptError{Path: r.Path(), Reason: "apps." + r.app + ": " + r.rot.Kept}
	}
	return runsSomewhere(e, r.app)
}

// command is the blueprint's command, run in its service, as the shell on a
// site runs it. The value goes on stdin, never in the command line.
func (r inApp) command(e Env, argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = apply.ShellQuote(a)
	}
	return fmt.Sprintf("%s exec -T %s %s", e.Cfg.Deployment().ComposeCmd(r.app), apply.ShellQuote(r.rot.Service), strings.Join(quoted, " "))
}

func (r inApp) sendable(next string) error {
	if strings.HasPrefix(next, "-") {
		return fmt.Errorf(`%s: the value begins with a dash, which a command would read as an option, so it cannot be set or checked. Remove the pending value as README.md, "A rotation that cannot finish", says, and run again for a new one`, r.Path())
	}
	if next == "" || strings.ContainsAny(next, "\n\x00") {
		return fmt.Errorf("%s: the value is empty or holds a newline or NUL byte, which cannot be sent on one line", r.Path())
	}
	ui.Secret(next)
	return nil
}

// Checked runs the change command on every site that runs the app. It sets
// the same value again when run twice.
func (r inApp) Checked(e Env, next string) error {
	if err := r.sendable(next); err != nil {
		return err
	}
	for _, u := range appUses(e, r.app) {
		t, ok := e.Sites[u.Site]
		if !ok {
			return fmt.Errorf("%s: no way to reach it", u.Site)
		}
		if out, err := t.RunInput(r.command(e, r.rot.Change), next+"\n"); err != nil {
			return fmt.Errorf("%s: changing %s inside %s: %w: %s", u.Site, r.key, r.rot.Service, err, lastLine(out))
		}
	}
	return nil
}

// Accepts reports whether every site's app accepts next, by the check
// command. Output holding the blueprint's refusal text means not yet;
// anything else that stops the check is an error.
func (r inApp) Accepts(e Env, next string) (bool, error) {
	if err := r.sendable(next); err != nil {
		return false, err
	}
	all := true
	for _, u := range appUses(e, r.app) {
		t, ok := e.Sites[u.Site]
		if !ok {
			return false, fmt.Errorf("%s: no way to reach it", u.Site)
		}
		out, err := t.RunInput(r.command(e, r.rot.Check), next+"\n")
		if err == nil {
			continue
		}
		if r.rot.Refused != "" && strings.Contains(out, r.rot.Refused) {
			all = false
			continue
		}
		return false, fmt.Errorf("%s: checking %s inside %s: %w: %s", u.Site, r.key, r.rot.Service, err, lastLine(out))
	}
	return all, nil
}

func (r inApp) Users(e Env) []Use { return appUses(e, r.app) }
