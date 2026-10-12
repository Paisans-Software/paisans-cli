package kinds

import (
	"fmt"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// GateSetting is one setting an app must enforce while it uses the gate: the
// gate leaves ActivityPub, and for some kinds a token holder's API calls, to
// the app, so the app's own setting is part of what keeps it closed. Every
// one is rendered into a file the app reads and named in that file's lock
// list, so the app reads it from disk on every read and nothing inside the
// app can change it. See docs/specs/2026-10-12-gate-settings.md.
type GateSetting struct {
	// Name is the app's own name for it, as its file and its settings
	// command spell it.
	Name string
	// Want is the value it must have.
	Want string
	// When is whether the app needs it, from its declaration.
	When func(config.App) bool
	// Why is what the gate leaves open without it.
	Why string
	// ConfigKey is the passthrough key that sets the same thing, in the
	// kind's config syntax: a config: key equal to Want only repeats what
	// the template writes, and any other value contradicts it.
	ConfigKey string
	// SettingKey is the settings: key that owns it, when one does.
	SettingKey string
}

// LiveSettings reads a kind's gate settings in a running app: the value it
// enforces and whether its file locks it. A kind with gate settings
// implements it in its own file, so doctor reads every kind the same way.
type LiveSettings interface {
	// Service is the compose service to run ReadCommand in.
	Service() string
	// ReadCommand is the argv run inside that service's container. It
	// prints the named settings and nothing else the app holds, so no
	// secret crosses the connection.
	ReadCommand(names []string) []string
	// Parse reads ReadCommand's output. A setting it did not print is
	// absent from the map.
	Parse(out string, names []string) (map[string]Live, error)
}

// Live is one setting as a running app holds it.
type Live struct {
	Value string
	// Locked is whether the file the toolkit renders locks it, which is
	// what makes the file's value the one the app enforces.
	Locked bool
}

var (
	gateSettings = map[config.Kind][]GateSetting{}
	liveSettings = map[config.Kind]LiveSettings{}
)

// registerGateSettings records a kind's gate settings and how to read them.
// Each kind with any calls it from its own file.
func registerGateSettings(kind config.Kind, settings []GateSetting, live LiveSettings) {
	gateSettings[kind] = settings
	liveSettings[kind] = live
}

// GateSettingsFor is every gate setting an app needs, given how it is
// declared: none for an app that does not use the gate.
func GateSettingsFor(app config.App) []GateSetting {
	if !app.UsesGate() {
		return nil
	}
	var out []GateSetting
	for _, s := range gateSettings[app.Kind] {
		if s.When(app) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// LiveSettingsFor is how to read a kind's gate settings in a running app,
// false for a kind with none.
func LiveSettingsFor(kind config.Kind) (LiveSettings, bool) {
	l, ok := liveSettings[kind]
	return l, ok
}

// gated is When for a setting every gated app needs.
func gated(app config.App) bool { return app.Gated }

// gatedFederating is When for a setting a gated app needs only while it
// federates.
func gatedFederating(app config.App) bool { return app.Gated && Federates(app) }

// RedundantKey is a passthrough key that only repeats the value a gate
// setting requires, which the template already writes.
type RedundantKey struct{ App, Key string }

// RedundantGateConfig is every config: key that repeats what gating makes the
// template write. Such a key says nothing the file does not already say, so
// `doctor --execute` removes it; a key with any other value contradicts the
// gate and is left for the operator.
func RedundantGateConfig(cfg *config.Config) []RedundantKey {
	var out []RedundantKey
	for _, name := range cfg.AppNames() {
		app := cfg.Apps[name]
		for _, s := range GateSettingsFor(app) {
			v, ok := app.Config[s.ConfigKey]
			if ok && strings.EqualFold(fmt.Sprint(v), s.Want) {
				out = append(out, RedundantKey{name, s.ConfigKey})
			}
		}
	}
	return out
}
