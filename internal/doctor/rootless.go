package doctor

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admitrules"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostcheck"
	"github.com/paisans-software/paisans-cli/internal/progname"
)

// The rootless checks ask whether a site is still what its owner's site
// admit made it: the admission record covers the site (admission.go), the
// deployment's user holds no group that reaches past it, its own rootless
// Docker daemon answers, is new enough and hands connections in without a
// wait (SourceMark), linger keeps that daemon running
// with nobody logged in and its manager starts after the mesh, and sudo
// lets the user run the WireGuard helper and nothing else (sudo.go). Each
// is read as the login user, with nothing that user may not read. See
// docs/specs/2026-10-15-rootless-and-host-admit.md, The deployment's user.

// Sections of the rootless checks, read right after admission.
const (
	SectionUser   = "user"
	SectionDocker = "docker"
	SectionLinger = "linger"
)

// UserCommand prints the login user's UID, name, group names and group IDs,
// a line each. The IDs are read as well as the names because a group of GID
// 0 can carry a name of its own.
const UserCommand = `printf 'uid %s\nuser %s\ngroups %s\ngids %s\n' "$(id -u)" "$(id -un)" "$(id -nG)" "$(id -G)"`

// DockerCommand prints the DOCKER_HOST the command ran with, then asks the
// daemon there for its Engine version and its security options, which name
// rootless for a daemon running as its user. The transport exports
// DOCKER_HOST as the login user's own socket ahead of every command. Last it
// prints the highest src_valid_mark in RootlessKit's network namespace, or
// unknown, the host's own, and whether the user's manager loaded apply's
// drop-in, reads that never fail the command (see SourceMark).
var DockerCommand = `printf 'host %s\n' "${DOCKER_HOST:-unset}" && docker version --format 'engine {{.Server.Version}}' && docker info --format 'security {{json .SecurityOptions}}' && ` +
	`printf 'source-mark %s\n' "$(` + apply.InRootlessNamespace(apply.SourceMarkRead) + ` 2>/dev/null || echo unknown)" && ` +
	`printf 'source-mark-host %s\n' "$(cat /proc/sys/net/ipv4/conf/all/src_valid_mark 2>/dev/null || echo unknown)" && ` +
	`printf 'source-mark-dropin %s\n' "$(` + apply.DropInLoaded + `)"`

// LingerCommand prints whether systemd keeps the login user's instance, and
// with it the user's Docker daemon, running with no session open, and
// whether site admit's drop-in starts that instance after the mesh at boot.
// The drop-in is root's, mode 644, so the user reads it.
var LingerCommand = `printf 'linger %s\n' "$(loginctl show-user "$(id -un)" --property=Linger --value)"; ` +
	`if [ -f "` + admitrules.MeshDropInPath("$(id -u)") + `" ]; then echo 'mesh-order yes'; else echo 'mesh-order no'; fi`

// UserProbe is UserCommand's answer on one site.
type UserProbe struct{ Site, Out, Err string }

// DockerProbe is DockerCommand's answer on one site. Out is kept when the
// command failed, since its first line still says which socket was asked.
type DockerProbe struct{ Site, Out, Err string }

// LingerProbe is LingerCommand's answer on one site.
type LingerProbe struct{ Site, Out, Err string }

func (p UserProbe) site() string   { return p.Site }
func (p DockerProbe) site() string { return p.Site }
func (p LingerProbe) site() string { return p.Site }

// keyed reads lines of the form "<key> <value>" into a map, the value being
// the rest of the line. A line with no space is left out.
func keyed(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimRight(line, "\r"), " "); ok {
			m[k] = strings.TrimSpace(v)
		}
	}
	return m
}

// User fails a login user that holds any group site admit refuses
// (admitrules.PrivilegedGroups), any group of GID 0, or uid 0. Admit checks
// the same list once; this checks it on every run, because a group can be
// added to the user after admission and nothing else would notice.
func User(probes []UserProbe) []Finding {
	var out []Finding
	for _, p := range probes {
		f := keyed(p.Out)
		name, uid := f["user"], f["uid"]
		if p.Err != "" || name == "" || uid == "" {
			why := p.Err
			if why == "" {
				why = "unreadable answer: " + firstLine(p.Out)
			}
			out = append(out, Finding{Section: SectionUser, Level: Warn, Line: fmt.Sprintf("%s: could not read the login user's groups", p.Site), More: []string{why}})
			continue
		}
		if uid == "0" {
			out = append(out, Finding{Section: SectionUser, Level: Fail,
				Line: fmt.Sprintf("%s: the login user %s has uid 0", p.Site, name),
				More: []string{
					"A deployment's user is never root: everything the operators run on the site would run as root.",
					"recover:",
					fmt.Sprintf("  1. Remove `ssh.user` from sites.%s, so the site logs in as the deployment's own user, and have the host's owner admit it.", p.Site),
				}})
			continue
		}
		var held []string
		for _, g := range strings.Fields(f["groups"]) {
			if slices.Contains(admitrules.PrivilegedGroups, g) && !slices.Contains(held, g) {
				held = append(held, g)
			}
		}
		if slices.Contains(strings.Fields(f["gids"]), "0") && !slices.Contains(held, "root") {
			held = append(held, "a group of GID 0, root's")
		}
		if len(held) == 0 {
			out = append(out, Finding{Section: SectionUser, Level: OK, Line: fmt.Sprintf("%s: %s is in no privileged group", p.Site, name)})
			continue
		}
		out = append(out, Finding{Section: SectionUser, Level: Fail,
			Line: fmt.Sprintf("%s: %s is in %s", p.Site, name, strings.Join(held, ", ")),
			More: []string{
				"The deployment's user is the boundary between this deployment and the rest of the host, and each of these groups reaches past it, to root, to every user's logs, to the owner's system Docker daemon, or to the host's disks and secrets.",
				"recover:",
				fmt.Sprintf("  1. The host's owner takes %s out of each, with `sudo gpasswd -d %s <group>` on %s.", name, name, p.Site),
				fmt.Sprintf("  2. `"+progname.Name()+" doctor --site %s` again.", p.Site),
			}})
	}
	return out
}

// rootlessOption is the security option docker info lists for a daemon
// that runs as its user.
const rootlessOption = "name=rootless"

func (p DockerProbe) parsed() (host, engine string, options []string, read bool) {
	f := keyed(p.Out)
	host, engine = f["host"], f["engine"]
	if s, ok := f["security"]; ok {
		read = json.Unmarshal([]byte(s), &options) == nil
	}
	return host, engine, options, read
}

// Answered reports whether the site's own rootless daemon answered, which
// every check that runs docker on the site needs.
func (p DockerProbe) Answered() bool {
	_, engine, options, read := p.parsed()
	return p.Err == "" && read && engine != "" && slices.Contains(options, rootlessOption)
}

// DockerSocket checks that the login user's own rootless daemon answers.
// Every container of the deployment runs in it, and a daemon running as root
// at that socket would be root on the host to anyone who can reach it.
func DockerSocket(probes []DockerProbe) []Finding {
	var out []Finding
	for _, p := range probes {
		host, engine, options, read := p.parsed()
		at := host
		if at == "" || at == "unset" {
			at = "the login user's socket"
		}
		switch {
		case p.Err != "" && engine == "":
			out = append(out, Finding{Section: SectionDocker, Level: Fail,
				Line: fmt.Sprintf("%s: its rootless Docker does not answer at %s", p.Site, at),
				More: []string{
					firstLine(p.Err),
					"Every container of this deployment on the site runs in the deployment's user's own daemon, so none of them runs while it does not answer.",
					"recover:",
					fmt.Sprintf("  1. `systemctl --user status docker`, as the deployment's user on %s, says why.", p.Site),
					"  2. `systemctl --user start docker` starts it.",
				}})
		case !read || engine == "":
			out = append(out, Finding{Section: SectionDocker, Level: Warn, Line: fmt.Sprintf("%s: could not read its Docker daemon's answer", p.Site), More: lastLines(p.Out+"\n"+p.Err, 3)})
		case !slices.Contains(options, rootlessOption):
			out = append(out, Finding{Section: SectionDocker, Level: Fail,
				Line: fmt.Sprintf("%s: the Docker daemon at %s is not rootless", p.Site, at),
				More: []string{
					"It runs as root, and a daemon running as root is root on the host to anyone who can reach its socket, which undoes what admitting the deployment to a user of its own draws.",
					"recover:",
					fmt.Sprintf("  1. The host's owner finds what runs a root daemon at that socket and stops it, then runs `"+progname.Name()+" site admit` for %s again, which sets up the user's own rootless daemon.", p.Site),
				}})
		default:
			out = append(out, Finding{Section: SectionDocker, Level: OK, Line: fmt.Sprintf("%s: its rootless Docker answers at %s", p.Site, at)})
		}
	}
	return out
}

// DockerEngine checks the rootless daemon's Engine against the floor site
// admit holds it to. A daemon that did not answer is DockerSocket's.
func DockerEngine(probes []DockerProbe) []Finding {
	var out []Finding
	for _, p := range probes {
		if !p.Answered() {
			continue
		}
		_, engine, _, _ := p.parsed()
		if hostcheck.AtLeast(engine, admitrules.MinRootlessEngine) {
			out = append(out, Finding{Section: SectionDocker, Level: OK, Line: fmt.Sprintf("%s: Docker Engine %s", p.Site, engine)})
			continue
		}
		out = append(out, Finding{Section: SectionDocker, Level: Fail,
			Line: fmt.Sprintf("%s: Docker Engine %s, and %s or later is needed", p.Site, engine, admitrules.MinRootlessEngine),
			More: []string{
				"From 29.5 a rootless container with host networking shares the host's network namespace. Every infrastructure container is network_mode: host and binds the mesh address, so on an older Engine they bind inside RootlessKit's own namespace, where no other site reaches them.",
				"recover:",
				"  1. The host's owner upgrades Docker; upgrading it is the owner's decision, which site admit leaves to them.",
				fmt.Sprintf("  2. `systemctl --user restart docker`, as the deployment's user on %s, which restarts every container in it.", p.Site),
			}})
	}
	return out
}

// SourceMark checks that src_valid_mark is 0 throughout the rootless
// daemon's RootlessKit namespace: all, default and every device. At 1, which the namespace copies from a host
// running Tailscale, RootlessKit's dial of each connection to a published
// port is dropped, and it dials again a second later without the client's
// address: every new connection to an app waits a second, and the app sees
// the bridge's gateway, outside the range it trusts a forwarded address from.
// Apply sets it to 0 and installs the drop-in that keeps it there. A daemon
// that did not answer is DockerSocket's; a value that could not be read is a
// warning, and so is a 0 on a host at 1 whose drop-in the user's manager has
// not loaded, since the daemon's next start copies the host's 1 again.
func SourceMark(probes []DockerProbe) []Finding {
	var out []Finding
	for _, p := range probes {
		if !p.Answered() {
			continue
		}
		f := keyed(p.Out)
		switch v := f["source-mark"]; v {
		case "0":
			if f["source-mark-host"] == "1" && f["source-mark-dropin"] == "absent" {
				out = append(out, Finding{Section: SectionDocker, Level: Warn,
					Line: fmt.Sprintf("%s: src_valid_mark is 0 in its RootlessKit namespace, and goes back to 1 at the daemon's next start", p.Site),
					More: []string{
						"The host's own value is 1, which a new namespace copies, and the user's manager has not loaded the drop-in apply installs to set it to 0 at every start of the daemon. At 1 every new connection to an app waits a second.",
						"recover:",
						fmt.Sprintf("  1. `"+progname.Name()+" deployment apply --execute`, which installs the drop-in on %s and has the user's manager reload it.", p.Site),
					}})
				break
			}
			out = append(out, Finding{Section: SectionDocker, Level: OK, Line: fmt.Sprintf("%s: src_valid_mark is 0 in its RootlessKit namespace", p.Site)})
		case "1":
			out = append(out, Finding{Section: SectionDocker, Level: Fail,
				Line: fmt.Sprintf("%s: src_valid_mark is 1 in its RootlessKit namespace, so every new connection to an app waits a second", p.Site),
				More: []string{
					"RootlessKit hands each connection to a published port in with the client's address, and at 1 the kernel drops the replies as coming from one of the namespace's own addresses. RootlessKit dials again a second later without the address, so the app also sees the Docker bridge's gateway, not the client. The namespace copies the value from its host, and Tailscale sets the host's to 1.",
					"recover:",
					fmt.Sprintf("  1. `"+progname.Name()+" deployment apply --execute`, which sets it to 0 in the namespace as the deployment's user on %s, and installs the drop-in that sets it again at every start of the daemon. The host's own value, which Tailscale needs, is left as it is.", p.Site),
				}})
		default:
			out = append(out, Finding{Section: SectionDocker, Level: Warn,
				Line: fmt.Sprintf("%s: src_valid_mark in its RootlessKit namespace could not be read", p.Site),
				More: []string{fmt.Sprintf("At 1, every new connection to an app waits a second. As the deployment's user on %s, `nsenter -U --preserve-credentials -t \"$(cat /run/user/$(id -u)/dockerd-rootless/child_pid)\" -m nsenter --net=/run/user/$(id -u)/dockerd-rootless/netns sh -c 'grep . /proc/sys/net/ipv4/conf/*/src_valid_mark'` reads it.", p.Site)}})
		}
	}
	return out
}

// Linger checks that systemd keeps the login user's instance running with
// nobody logged in, and that site admit's drop-in starts it after the mesh
// at boot. Without linger the user's Docker daemon, and every container in
// it, stops when the last login ends and does not start at boot; without
// the drop-in it may start the containers before the mesh address exists.
func Linger(d deployment.Deployment, probes []LingerProbe) []Finding {
	var out []Finding
	for _, p := range probes {
		f := keyed(p.Out)
		switch linger := f["linger"]; {
		case p.Err == "" && linger == "yes":
			out = append(out, Finding{Section: SectionLinger, Level: OK, Line: fmt.Sprintf("%s: linger is on: its Docker daemon runs with nobody logged in, and starts at boot", p.Site)})
		case p.Err == "" && linger == "no":
			out = append(out, Finding{Section: SectionLinger, Level: Fail,
				Line: fmt.Sprintf("%s: linger is off", p.Site),
				More: []string{
					"Without it the user's systemd instance, and with it its Docker daemon and every container, stops when the last login to the user ends, and nothing of the deployment starts at boot.",
					"recover:",
					fmt.Sprintf("  1. The host's owner runs `"+progname.Name()+" site admit` for %s again, which turns it on.", p.Site),
				}})
		default:
			why := p.Err
			if why == "" {
				why = "unreadable answer: " + firstLine(p.Out)
			}
			out = append(out, Finding{Section: SectionLinger, Level: Warn, Line: fmt.Sprintf("%s: could not read whether linger is on", p.Site), More: []string{why}})
			continue
		}
		switch f["mesh-order"] {
		case "yes":
			out = append(out, Finding{Section: SectionLinger, Level: OK, Line: fmt.Sprintf("%s: its manager starts after the mesh at boot", p.Site)})
		case "no":
			out = append(out, Finding{Section: SectionLinger, Level: Fail,
				Line: fmt.Sprintf("%s: its manager is not ordered after the mesh at boot", p.Site),
				More: []string{
					fmt.Sprintf("site admit's drop-in, which starts the user's manager after %s, is missing. At boot its Docker daemon can then start a container before %s is up, and one that publishes on the mesh address fails with cannot assign requested address, which Docker does not retry.", d.WireGuardUnit(), d.Interface()),
					"recover:",
					fmt.Sprintf("  1. The host's owner runs `"+progname.Name()+" site admit` for %s again, which writes it; it takes effect at the next boot.", p.Site),
				}})
		default:
			why := "the answer has no mesh-order line"
			if v, ok := f["mesh-order"]; ok {
				why = "unreadable line: mesh-order " + v
			}
			out = append(out, Finding{Section: SectionLinger, Level: Warn,
				Line: fmt.Sprintf("%s: could not read whether its manager starts after the mesh", p.Site),
				More: []string{why}})
		}
	}
	return out
}
