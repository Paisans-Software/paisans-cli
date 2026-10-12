package kinds

import "strings"

// writefreelyLive reads the blog's gate settings with its own settings
// command, which prints each setting's effective value and marks the ones
// config.ini locks (cmd/writefreely/settings.go:38-58 at v0.21.1+wisp). The
// command lists every setting; none of them is a secret, which config.ini
// keeps in sections the command does not read.
type writefreelyLive struct{}

func (writefreelyLive) Service() string { return "app" }

func (writefreelyLive) ReadCommand([]string) []string {
	return []string{"writefreely", "-c", "/data/config.ini", "settings", "list"}
}

// lockedMark is how the settings command marks a setting config.ini locks.
const lockedMark = "(locked in config.ini)"

func (writefreelyLive) Parse(out string, names []string) (map[string]Live, error) {
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
		live[fields[0]] = Live{Value: fields[1], Locked: strings.Contains(line, lockedMark)}
	}
	return live, nil
}
