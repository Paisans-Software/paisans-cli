package kinds

import (
	"fmt"
	"strings"
)

// The in-app actions a blueprint can name for reading its gate settings back
// from a running app (docs/specs/2026-10-12-blueprints-and-catalogs.md, "In-app
// actions"). Each is named for its mechanism rather than for the app that
// first needed it, so any blueprint may use it. Each runs a command inside one
// of the app's own containers and returns the named settings and nothing
// else the app holds, so no secret crosses the connection.

// liveSpec is a blueprint's `gate.live`.
type liveSpec struct {
	Action  string   `yaml:"action"`
	Service string   `yaml:"service"`
	Command []string `yaml:"command"`
	// LockVariable is env-lock-list's environment variable listing the
	// settings the app reads from its environment alone.
	LockVariable string `yaml:"lock_variable"`
	// LockedMark is command-table's marker for a locked setting.
	LockedMark string `yaml:"locked_mark"`
}

// liveAction builds the reader a blueprint's `gate.live` names.
func liveAction(s liveSpec) (LiveSettings, error) {
	if s.Service == "" {
		return nil, fmt.Errorf("gate.live.service: required, the container the read runs in")
	}
	switch s.Action {
	case "env-lock-list":
		if s.LockVariable == "" {
			return nil, fmt.Errorf("gate.live.lock_variable: required by env-lock-list")
		}
		return envLockList{service: s.Service, lockVariable: s.LockVariable}, nil
	case "command-table":
		if len(s.Command) == 0 || s.LockedMark == "" {
			return nil, fmt.Errorf("gate.live: command-table needs command and locked_mark")
		}
		return commandTable{service: s.Service, command: s.Command, lockedMark: s.LockedMark}, nil
	}
	return nil, fmt.Errorf("gate.live.action: %q is not an action this toolkit has; it has env-lock-list and command-table", s.Action)
}

// envLockList reads settings from the app container's environment, for an
// app whose locked settings are its environment's values by its own rule
// and which names them in one variable. Mbin is one: it has no command that
// lists its settings, and it reads each setting named in
// MBIN_LOCKED_SETTINGS from the environment. Only the named variables are
// printed: the same environment holds the app's secrets.
type envLockList struct {
	service      string
	lockVariable string
}

func (e envLockList) Service() string { return e.service }

func (e envLockList) ReadCommand(names []string) []string {
	return []string{"sh", "-c", `for n in ` + strings.Join(append(append([]string(nil), names...), e.lockVariable), " ") + `; do printf '%s\t%s\n' "$n" "$(printenv "$n")"; done`}
}

func (e envLockList) Parse(out string, names []string) (map[string]Live, error) {
	values := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		name, value, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if ok {
			values[name] = value
		}
	}
	locked := map[string]bool{}
	for _, n := range strings.Split(values[e.lockVariable], ",") {
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

// commandTable reads settings with the app's own command, which prints one
// setting per line, its name and its effective value first, and marks the
// ones its configuration file locks. WriteFreely's `settings list` is one
// (cmd/writefreely/settings.go:38-58 at v0.21.1+wisp): it lists every
// setting, and none of them is a secret, which config.ini keeps in sections
// the command does not read.
type commandTable struct {
	service    string
	command    []string
	lockedMark string
}

func (c commandTable) Service() string { return c.service }

func (c commandTable) ReadCommand([]string) []string {
	return append([]string(nil), c.command...)
}

func (c commandTable) Parse(out string, names []string) (map[string]Live, error) {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	live := map[string]Live{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !want[fields[0]] {
			continue
		}
		live[fields[0]] = Live{Value: fields[1], Locked: strings.Contains(line, c.lockedMark)}
	}
	return live, nil
}
