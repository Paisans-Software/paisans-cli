package hostprep

import (
	"fmt"
	"strings"
)

// UserSystemctl is systemctl for the login user's own manager. A
// non-interactive ssh session does not always set XDG_RUNTIME_DIR, without
// which systemctl --user cannot find the manager admit's linger started.
const UserSystemctl = "XDG_RUNTIME_DIR=/run/user/$(id -u) systemctl --user"

// homeProbe prints the login user's passwd entry, whose sixth field is the
// home sshd and systemd use, whatever HOME the session was given.
const homeProbe = `getent passwd "$(id -un)"`

// Home is the login user's home directory on the host t reaches.
func Home(t Transport) (string, error) {
	out, err := t.Run(homeProbe)
	if err != nil {
		return "", fmt.Errorf("reading the login user's home on %s: %w", t.Describe(), err)
	}
	fields := strings.Split(strings.TrimSpace(out), ":")
	if len(fields) < 7 {
		return "", fmt.Errorf("the login user has no passwd entry on %s that paisans can read: %q", t.Describe(), strings.TrimSpace(out))
	}
	home := strings.TrimSuffix(fields[5], "/")
	if !strings.HasPrefix(home, "/") || strings.ContainsAny(home, " \t\n'\"") {
		return "", fmt.Errorf("the login user's home on %s is %q, not a plain absolute path", t.Describe(), home)
	}
	return home, nil
}

// UserUnitDir is where the login user's own systemd units live.
func UserUnitDir(home string) string { return home + "/.config/systemd/user" }
