package apply

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// uidProbe asks the login user's UID, from which its rootless Docker
// daemon's socket is known.
const uidProbe = "id -u"

// RootlessDockerHost is DOCKER_HOST for the rootless daemon of the user with
// uid: its socket in the user's runtime directory, which systemd makes at
// /run/user/<uid> and keeps while the user lingers.
func RootlessDockerHost(uid string) string { return "unix:///run/user/" + uid + "/docker.sock" }

// Login is what a run learns the first time it logs in to one destination,
// shared by every copy of the transport to it, as SudoAuth is: the login
// user's DOCKER_HOST, from its UID. Every command runs as that user, against
// its own rootless daemon, so the UID is asked once a run and every command
// after it carries the answer.
type Login struct {
	// Connected, when set, is called once, after the first command run
	// through this login has succeeded: the user exists on the host and
	// accepted the operator's key.
	Connected func()

	mu         sync.Mutex
	dockerHost string
}

// NewLogin returns a Login that calls connected after its first successful
// command, or nothing for nil.
func NewLogin(connected func()) *Login { return &Login{Connected: connected} }

// resolve returns t's DOCKER_HOST, asking the host for the login user's UID
// the first time. A failure is not kept: it is either ssh's, which the next
// command meets again, or an answer that was not a UID, which a second
// command may find fixed.
func (l *Login) resolve(t SSHTransport) (string, error) {
	l.mu.Lock()
	host, first, err := l.resolveLocked(t)
	l.mu.Unlock()
	// Outside the lock, so whatever Connected does cannot wait on a
	// command through this login.
	if first && l.Connected != nil {
		l.Connected()
	}
	return host, err
}

func (l *Login) resolveLocked(t SSHTransport) (host string, first bool, err error) {
	if l.dockerHost != "" {
		return l.dockerHost, false, nil
	}
	bare := t
	bare.Login, bare.DockerHost = nil, ""
	out, err := bare.run(uidProbe, nil)
	if err != nil {
		var p *ui.Problem
		if errors.As(err, &p) {
			return "", false, err
		}
		return "", false, fmt.Errorf("%s: %s: %w\n%s", t.Describe(), uidProbe, err, strings.TrimRight(out, "\n"))
	}
	uid := strings.TrimSpace(out)
	if uid == "" || strings.Trim(uid, "0123456789") != "" {
		return "", false, fmt.Errorf("%s: %s printed %q, not a user ID, so the user's Docker daemon cannot be found. A shell startup file that prints on a command without a terminal does this", t.Describe(), uidProbe, uid)
	}
	l.dockerHost = RootlessDockerHost(uid)
	return l.dockerHost, true, nil
}
