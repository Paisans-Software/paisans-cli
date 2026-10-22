package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"os/user"
	"regexp"
	"strconv"
	"strings"
)

// SSHConfig is how a destination is looked up in the operator's own ssh
// configuration: through `ssh -G`, which prints the options ssh would use for
// a host after reading every file it reads, so that Match, Include, wildcards
// and canonicalisation decide a destination exactly as they decide a plain
// ssh's. Tests give a stub.
type SSHConfig struct {
	// G runs ssh with -G and args and returns what it prints. args never
	// reach a shell; the real one execs ssh with them as its argv.
	G func(args []string) (string, error)
	// LocalUser is the login ssh falls back to when no User applies to a
	// host. `ssh -G` prints a user for every host, this one where nothing
	// configures one, so a destination whose user resolves to it is taken
	// as one that names no user (see ResolveDestination).
	LocalUser string
}

// OperatorSSHConfig is the SSHConfig of the ssh on this workstation's PATH,
// for the user running the toolkit.
func OperatorSSHConfig() SSHConfig {
	local := ""
	if u, err := user.Current(); err == nil {
		local = u.Username
	}
	return SSHConfig{G: runSSHG, LocalUser: local}
}

// runSSHG execs `ssh -G args...`, with no shell between.
func runSSHG(args []string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("ssh", append([]string{"-G"}, args...)...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("ssh -G: %s", msg)
		}
		return "", fmt.Errorf("ssh -G: %w", err)
	}
	return stdout.String(), nil
}

// sshHostArg is the host part of a destination as the operator types it: a
// host name, an address or a Host alias of their ssh config. It starts with
// a letter or digit, so it is never read as an ssh option, and it holds no
// pattern character (`*`, `?`, `!`), no `%` token and no space, so it names
// one host rather than matching several.
var sshHostArg = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// destinationForm is the refusal of s as a destination in any form.
func destinationForm(s string) error {
	return fmt.Errorf("%q is not user@host[:port] or an alias of your ssh config, [user@]alias[:port], Eg: admin@203.0.113.9, admin@203.0.113.9:2222, admin@[2001:db8::1]:22, home-a or admin@home-a:2222", s)
}

// ResolveDestination reads a destination in every form ssh takes one:
// user@host[:port] and user@[v6]:port as ParseDestination reads them, and a
// Host alias of the operator's ssh config, alone or with a user, a port or
// both (alias, user@alias, alias:port, user@alias:port). A user or port
// given overrides the config's, as it does for ssh.
//
// Each is looked up with `ssh -G` through c. A host ssh maps to another
// HostName is an alias: the result carries the alias, which is what ssh is
// given to reach it (see Destination), and the user, host name and port ssh
// would use. A host ssh maps to itself is a plain host, and keeps the user
// and port written, so user@host[:port] reads as it always has.
//
// The user is required, because it is whose authorized keys a command reads
// and the toolkit never guesses one. A destination with no user takes the
// User its ssh config gives it; one whose config gives none fails, rather
// than log in as the operator's local name, which `ssh -G` reports when
// nothing configures a user. A config whose User is that same local name is
// indistinguishable from none, and the error says to name the user.
//
// The user and host ssh resolves are held to the rules a declared ssh
// section is, since both go into command lines.
func ResolveDestination(s string, c SSHConfig) (Destination, error) {
	form := destinationForm(s)
	login, rest, hasUser := strings.Cut(s, "@")
	if !hasUser {
		login, rest = "", s
	} else if !unixUser.MatchString(login) {
		return Destination{}, form
	}
	host, port, bracketed, err := splitHostPort(s, rest)
	if err != nil {
		return Destination{}, err
	}
	ip := net.ParseIP(host)
	switch {
	case bracketed && (ip == nil || ip.To4() != nil):
		return Destination{}, fmt.Errorf("%q: only an IPv6 address goes in brackets", s)
	case !bracketed && !sshHostArg.MatchString(host):
		return Destination{}, form
	}
	if c.G == nil {
		return Destination{}, errors.New("no ssh to read the ssh config with")
	}
	args := []string{}
	if login != "" {
		args = append(args, "-l", login)
	}
	if port != 0 {
		args = append(args, "-p", strconv.Itoa(port))
	}
	// ssh -G takes the host after `--`, so not even a host that slipped
	// past sshHostArg could be read as an option.
	out, err := c.G(append(args, "--", host))
	if err != nil {
		return Destination{}, fmt.Errorf("%q: reading your ssh config: %w", s, err)
	}
	got := sshGOptions(out)
	resolved := got["hostname"]
	alias := !strings.EqualFold(resolved, host)
	if !alias {
		// A plain host: as written, with the defaults ParseDestination
		// gives, and the user the config gives when none is written.
		if !bracketed && ip == nil && !isHostname(host) {
			return Destination{}, form
		}
		if port == 0 {
			port = DefaultSSHPort
		}
		if login == "" {
			if login, err = configuredUser(s, s, got, c); err != nil {
				return Destination{}, err
			}
		}
		return Destination{User: login, Host: host, Port: port}, nil
	}
	shown := s + " -> " + resolved
	if net.ParseIP(resolved) == nil && !isHostname(resolved) {
		return Destination{}, fmt.Errorf("%q: your ssh config gives it HostName %q, which is not a host name or address", s, resolved)
	}
	if login == "" {
		if login, err = configuredUser(s, shown, got, c); err != nil {
			return Destination{}, err
		}
	}
	n, err := strconv.Atoi(got["port"])
	if err != nil || n < 1 || n > 65535 {
		return Destination{}, fmt.Errorf("%q: your ssh config gives it Port %q, not a number from 1 to 65535", s, got["port"])
	}
	return Destination{User: login, Host: resolved, Port: n, Alias: host}, nil
}

// splitHostPort is rest, the part of s after any user, as its host and port,
// with a port of 0 where none is written.
func splitHostPort(s, rest string) (host string, port int, bracketed bool, err error) {
	form := destinationForm(s)
	if rest == "" {
		return "", 0, false, form
	}
	bracketed = strings.HasPrefix(rest, "[")
	switch {
	case bracketed && strings.HasSuffix(rest, "]"):
		return rest[1 : len(rest)-1], 0, true, nil
	case bracketed || strings.Contains(rest, ":"):
		h, p, err := net.SplitHostPort(rest)
		if err != nil {
			return "", 0, false, form
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return "", 0, false, fmt.Errorf("%q: the port must be a number from 1 to 65535", s)
		}
		return h, n, bracketed, nil
	}
	return rest, 0, false, nil
}

// configuredUser is the User ssh config gives a destination written with
// none, or the refusal naming the user it lacks.
func configuredUser(s, shown string, got map[string]string, c SSHConfig) (string, error) {
	u := got["user"]
	if u == "" || u == c.LocalUser {
		return "", fmt.Errorf("%q names no user and your ssh config gives %s none, so ssh would log in as your local name. Name the user, Eg: admin@%s", s, shown, s)
	}
	if !unixUser.MatchString(u) {
		return "", fmt.Errorf("%q: your ssh config gives it User %q, which is not a login name the toolkit puts in a command line", s, u)
	}
	return u, nil
}

// sshGOptions is what `ssh -G` printed, keyword to value, the first value of
// each, as ssh uses the first.
func sshGOptions(out string) map[string]string {
	got := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		key, value, _ := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		key = strings.ToLower(key)
		if _, seen := got[key]; !seen && key != "" {
			got[key] = strings.TrimSpace(value)
		}
	}
	return got
}
