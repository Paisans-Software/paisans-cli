package kinds

import "strings"

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
