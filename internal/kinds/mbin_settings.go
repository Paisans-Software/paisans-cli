package kinds

import (
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// Mbin's gate settings. Each is rendered into its .env and named in
// MBIN_LOCKED_SETTINGS, which the paisans fork reads from the environment on
// every read and never from its database (SettingsManager.php:104 at
// v1.15.1+paisans), so the container's environment is what Mbin enforces.
func init() {
	registerGateSettings(config.KindMbin, []GateSetting{
		{
			Name: "MBIN_PRIVATE_INSTANCE", Want: "true", When: gated,
			Why:       "the gate lets a request with a bearer token reach /api/*, and without private instance mode Mbin answers its API reads with no user at all",
			ConfigKey: "MBIN_PRIVATE_INSTANCE",
		},
		{
			Name: "MBIN_USE_FEDERATION_ALLOW_LIST", Want: "true", When: gated,
			Why:       "the gate leaves ActivityPub to Mbin, and without the allow list Mbin answers a signed read from any instance",
			ConfigKey: "MBIN_USE_FEDERATION_ALLOW_LIST",
		},
		{
			Name: "MBIN_AUTHORIZED_FETCH", Want: "true", When: gatedFederating,
			Why:       "the gate leaves ActivityPub to Mbin, and without authorized fetch Mbin answers an unsigned read",
			ConfigKey: "MBIN_AUTHORIZED_FETCH", SettingKey: "authorized_fetch",
		},
	}, mbinLive{})
}

// mbinLive reads Mbin's gate settings from the app container's environment.
// Mbin has no command that lists its settings, and a locked one is the
// environment's value by the fork's own rule, so the environment and the lock
// list are the whole answer. Only the named variables are printed: the same
// environment holds the app's secrets.
type mbinLive struct{}

func (mbinLive) Service() string { return "app" }

func (mbinLive) ReadCommand(names []string) []string {
	return []string{"sh", "-c", `for n in ` + strings.Join(append(append([]string(nil), names...), "MBIN_LOCKED_SETTINGS"), " ") + `; do printf '%s\t%s\n' "$n" "$(printenv "$n")"; done`}
}

func (mbinLive) Parse(out string, names []string) (map[string]Live, error) {
	values := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		name, value, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if ok {
			values[name] = value
		}
	}
	locked := map[string]bool{}
	for _, n := range strings.Split(values["MBIN_LOCKED_SETTINGS"], ",") {
		locked[strings.TrimSpace(n)] = true
	}
	live := map[string]Live{}
	for _, n := range names {
		if v, ok := values[n]; ok {
			live[n] = Live{Value: v, Locked: locked[n]}
		}
	}
	return live, nil
}
