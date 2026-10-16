package hostprep

import (
	"fmt"
	"strconv"
	"strings"
)

// UserSystemctl is systemctl for the login user's own manager. A
// non-interactive ssh session does not always set XDG_RUNTIME_DIR, without
// which systemctl --user cannot find the manager admit's linger started.
const UserSystemctl = "XDG_RUNTIME_DIR=/run/user/$(id -u) systemctl --user"

// homeProbe prints the login user's passwd entry, whose sixth field is the
// home sshd and systemd use, whatever HOME the session was given, or a
// marker and the user's name when it has none, so that "no such user"
// cannot be confused with ssh failing.
const homeProbe = `u=$(id -un); getent passwd "$u" || echo "__PAISANS_ABSENT__ $u"`

// Home is the login user's home directory on the host t reaches. A login
// user the host has no passwd entry for is refused by name.
func Home(t Transport) (string, error) {
	home, _, err := HomeAndUID(t)
	return home, err
}

// HomeAndUID is Home with the login user's UID, from the same passwd entry,
// so a command can tell a root login from the deployment's user.
func HomeAndUID(t Transport) (home string, uid int, err error) {
	out, err := t.Run(homeProbe)
	if err != nil {
		return "", 0, fmt.Errorf("reading the login user's home on %s: %w", t.Describe(), err)
	}
	if user, absent := strings.CutPrefix(strings.TrimSpace(out), "__PAISANS_ABSENT__"); absent {
		return "", 0, fmt.Errorf("the login user %s does not exist on %s. host prepare does not create users: create it, with sudo, and prepare again", strings.TrimSpace(user), t.Describe())
	}
	fields := strings.Split(strings.TrimSpace(out), ":")
	if len(fields) < 7 {
		return "", 0, fmt.Errorf("the login user has no passwd entry on %s that paisans can read: %q", t.Describe(), strings.TrimSpace(out))
	}
	if uid, err = strconv.Atoi(fields[2]); err != nil || uid < 0 {
		return "", 0, fmt.Errorf("the login user's passwd entry on %s has no UID that paisans can read: %q", t.Describe(), strings.TrimSpace(out))
	}
	home = strings.TrimSuffix(fields[5], "/")
	if !strings.HasPrefix(home, "/") || strings.ContainsAny(home, " \t\n'\"") {
		return "", 0, fmt.Errorf("the login user's home on %s is %q, not a plain absolute path", t.Describe(), home)
	}
	return home, uid, nil
}

// UserUnitDir is where the login user's own systemd units live.
func UserUnitDir(home string) string { return home + "/.config/systemd/user" }

// unitScope is one systemd manager: the command that reaches it, with its
// environment, and the directory its own unit files go in.
type unitScope struct {
	systemctl string
	dir       string
	// activatingIsActive counts a unit that is activating as running: the
	// user's git daemon waits out RestartSec until the mesh is up, and is
	// running as it should while it does.
	activatingIsActive bool
}

// systemScope is the system's manager, which host admit and the root-side
// profile steps use.
var systemScope = unitScope{systemctl: "systemctl", dir: "/etc/systemd/system"}

// userScope is the login user's own manager, under the linger host admit
// enabled, whose home is given.
func userScope(home string) unitScope {
	return unitScope{systemctl: UserSystemctl, dir: UserUnitDir(home), activatingIsActive: true}
}
