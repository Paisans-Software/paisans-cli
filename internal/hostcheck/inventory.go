package hostcheck

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/version"
)

// Transport is how the inventory reads a host. apply.SSHTransport and the
// other packages' transports satisfy it. It has no way to write, because the
// inventory never changes anything.
type Transport interface {
	// Run executes a command and returns its combined output. A non zero
	// exit is an error.
	Run(command string) (string, error)
	// ReadFile returns a file's contents, a missing file reported through
	// found.
	ReadFile(path string) (content string, found bool, err error)
	// Describe names the destination, for messages.
	Describe() string
}

// Inventory is what one host already runs.
type Inventory struct {
	Host string
	// Self is who the probes ran as: the deployment's user, whose
	// listeners and whose containers' are this deployment's.
	Self       Self
	Docker     Docker
	Containers []Container
	Volumes    []Volume
	Networks   []Network
	Sockets    []Socket
	Links      []string
	Routes     []Route
	// Manifest is whether this deployment's record of what it wrote exists
	// at ManifestPath, and ManifestWireGuard whether that record lists
	// its WireGuard config, which is how a mesh interface the deployment
	// wrote is told from one it did not.
	ManifestPath      string
	Manifest          bool
	ManifestWireGuard bool
	// ManifestFiles is every file that record lists, nil when there is
	// none or it is unreadable.
	ManifestFiles []render.ManifestFile
	// ManifestWebServer is every file that record lists in the include
	// directory of the operator's web server.
	ManifestWebServer []render.ManifestWebServer
	// ManifestPending is the sites that record says were pending when it
	// was written, nil when it names none.
	ManifestPending []string
	// ManifestBuild is the paisans that wrote that record.
	ManifestBuild version.Build
	// ManifestErr is the refusal of a record that does not say which build
	// of paisans wrote it, nil for every other. Its other fields are read
	// all the same, for apply, which writes it again.
	ManifestErr error
	// HostSites are the *.caddy files in the deployment's own render.HostSitesDir,
	// by full path and sorted: site blocks somebody added to the gateway's
	// Caddy, which no deployment owns.
	HostSites []string
}

// Self is the login user and the subordinate range its containers' users map
// into: everything a rootless Docker runs as is one of them.
type Self struct {
	UID                int
	SubStart, SubCount int
}

// Owns reports whether uid is the user's or one its containers run as.
func (s Self) Owns(uid int) bool {
	return uid == s.UID || (s.SubCount > 0 && uid >= s.SubStart && uid < s.SubStart+s.SubCount)
}

// ParseSelf reads SelfProbe's two lines. One without a uid, or naming uid 0,
// is an error, since as uid 0 every listener of root's would read as this
// deployment's. A subordinate range line that is present and unreadable is
// an error too, for the same reason: read as starting at 0 it would own
// root's and every system user's UIDs. An absent one is no range, which owns
// nothing.
func ParseSelf(out string) (Self, error) {
	var s Self
	found := false
	for _, line := range strings.Split(out, "\n") {
		key, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch key {
		case "uid":
			uid, err := strconv.Atoi(rest)
			if err != nil {
				return s, fmt.Errorf("unreadable uid %q", rest)
			}
			if uid == 0 {
				return s, errors.New("the probes ran as root, never the deployment's user")
			}
			s.UID, found = uid, true
		case "subuid":
			if rest == "" {
				continue
			}
			start, count, ok := parseSubRange(rest)
			if !ok {
				return s, fmt.Errorf("unreadable /etc/subuid line %q", rest)
			}
			s.SubStart, s.SubCount = start, count
		}
	}
	if !found {
		return s, fmt.Errorf("no uid in %q", firstLine(out))
	}
	return s, nil
}

// parseSubRange reads an /etc/subuid line, name:start:count. A start below 1
// or a count below 1 is not a range useradd or anyone else writes, and is
// refused rather than read.
func parseSubRange(line string) (start, count int, ok bool) {
	f := strings.Split(line, ":")
	if len(f) != 3 {
		return 0, 0, false
	}
	start, err := strconv.Atoi(f[1])
	if err != nil || start < 1 {
		return 0, 0, false
	}
	count, err = strconv.Atoi(f[2])
	if err != nil || count < 1 || start > math.MaxInt-count {
		return 0, 0, false
	}
	return start, count, true
}

// Docker is the engine, if there is one.
type Docker struct {
	Present bool
	// Version is the daemon's version and Client the docker CLI's, as
	// `docker version` reports them, distribution suffix and all.
	Version string
	Client  string
	// ComposeWorks is whether `docker compose version` answered, and
	// Compose the version it printed, without a leading "v".
	ComposeWorks bool
	Compose      string
	// ComposeMissing is whether it was asked and did not answer.
	ComposeMissing bool
	// Packages names where it came from: docker-ce, docker.io or snap. It
	// is shown, and only snap is refused.
	Packages []string
}

// The probes, one command per fact. Every one only reads, and every one runs
// as the deployment's user: none needs root.
//
// The three inspects take a list read a moment earlier, and a container,
// volume or network removed in between makes `docker inspect` complain and
// exit non zero while still printing the rest. That is not a failure to
// look: what is gone holds nothing, so the complaint is discarded and what
// answered is read.
const (
	// SelfProbe names the login user's UID and its subordinate range, which
	// a rootless container's users map into, so a listener can be told the
	// deployment's own by its owner. /etc/subuid is world readable.
	SelfProbe      = `echo "uid $(id -u)"; echo "subuid $(grep "^$(id -un):" /etc/subuid | head -n 1)"`
	dockerProbe    = `command -v docker >/dev/null 2>&1 || { echo absent; exit 0; }; docker version --format '{{.Server.Version}} {{.Client.Version}}' || exit 1; echo "compose $(docker compose version --short 2>/dev/null || echo absent)"`
	packageProbe   = `dpkg-query -W -f='${Package} ${Status}\n' docker-ce docker.io 2>/dev/null; snap list docker 2>/dev/null; true`
	containerProbe = `docker ps -aq --no-trunc | xargs -r docker inspect --format '{"id":{{json .Id}},"name":{{json .Name}},"image":{{json .Image}},"pid":{{.State.Pid}},"labels":{{json .Config.Labels}},"networks":{{json .NetworkSettings.Networks}},"ports":{{json .HostConfig.PortBindings}}}' 2>/dev/null || true`
	volumeProbe    = `docker volume ls -q | xargs -r docker volume inspect --format '{"name":{{json .Name}},"labels":{{json .Labels}}}' 2>/dev/null || true`
	networkProbe   = `docker network ls -q --no-trunc | xargs -r docker network inspect --format '{"id":{{json .Id}},"name":{{json .Name}},"labels":{{json .Labels}},"ipam":{{json .IPAM.Config}}}' 2>/dev/null || true`
	// socketProbe lists listeners with their owner's UID (-e) and without
	// their process (-p), which only root can name for another user's.
	socketProbe = "ss -Hltnue"
	linkProbe   = "ip -o link"
	routeProbe  = "ip -j route show table all"
	cLocale     = "export LC_ALL=C; "
)

// hostSitesProbe lists the *.caddy files in d's render.HostSitesDir. Each
// file is printed only when it is one, since an unmatched glob is left as the
// literal pattern. An unreadable directory fails rather than listing nothing,
// and a link whose target is gone is still a site file: either read as no
// sites would let a Caddy that serves the owner's sites be removed.
func hostSitesProbe(d deployment.Deployment) string {
	dir := quote(render.HostSitesDir(d))
	return `if [ -d ` + dir + ` ] && ! { [ -r ` + dir + ` ] && [ -x ` + dir + ` ]; }; then echo "cannot read ` + render.HostSitesDir(d) + `" >&2; exit 3; fi; for f in ` + dir + `/*.caddy; do if [ -e "$f" ] || [ -L "$f" ]; then echo "$f"; fi; done; true`
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ReadCommands is every fixed command Inspect sends, exactly as it sends it,
// for a caller's allow list of reads.
func ReadCommands(d deployment.Deployment) []string {
	var out []string
	for _, probe := range []string{SelfProbe, dockerProbe, packageProbe, containerProbe, volumeProbe, networkProbe, socketProbe, linkProbe, routeProbe, hostSitesProbe(d)} {
		out = append(out, cLocale+probe)
	}
	return out
}

// Inspect reads what a host already runs. It changes nothing. A probe that
// fails is an error rather than an empty answer: a host check that could not
// look has not found the host clean. d is the deployment whose manifest is
// read.
func Inspect(t Transport, d deployment.Deployment) (*Inventory, error) {
	inv := &Inventory{Host: t.Describe(), ManifestPath: d.Manifest()}
	fail := func(what string, err error) error {
		return fmt.Errorf("host check on %s: %s: %w", inv.Host, what, err)
	}
	// Every probe runs in the C locale, because dpkg and ss translate what
	// they print and the parsers read the English words.
	run := func(what, command string) (string, error) {
		out, err := t.Run(cLocale + command)
		if err != nil {
			return "", fail(what, fmt.Errorf("%v: %s", err, firstLine(out)))
		}
		return out, nil
	}

	out, err := run("asking who the probes run as", SelfProbe)
	if err != nil {
		return nil, err
	}
	if inv.Self, err = ParseSelf(out); err != nil {
		return nil, fail("asking who the probes run as", err)
	}

	out, err = run("asking Docker for its version", dockerProbe)
	if err != nil {
		return nil, fmt.Errorf("%w. Docker is installed and did not answer, so the containers on this host cannot be seen. Start it (Eg: systemctl start docker) and run again", err)
	}
	inv.Docker = parseDocker(out)
	if out, err = run("reading Docker's package", packageProbe); err != nil {
		return nil, err
	}
	inv.Docker.Packages = parsePackages(out)

	if inv.Docker.Present {
		if out, err = run("listing containers", containerProbe); err != nil {
			return nil, err
		}
		if inv.Containers, err = parseContainers(out); err != nil {
			return nil, fail("listing containers", err)
		}
		if out, err = run("listing volumes", volumeProbe); err != nil {
			return nil, err
		}
		if inv.Volumes, err = parseVolumes(out); err != nil {
			return nil, fail("listing volumes", err)
		}
		if out, err = run("listing networks", networkProbe); err != nil {
			return nil, err
		}
		if inv.Networks, err = parseNetworks(out); err != nil {
			return nil, fail("listing networks", err)
		}
	}

	if out, err = run("listing listeners", socketProbe); err != nil {
		return nil, err
	}
	if inv.Sockets, err = parseSockets(out); err != nil {
		return nil, fail("listing listeners", err)
	}

	if out, err = run("listing interfaces", linkProbe); err != nil {
		return nil, err
	}
	inv.Links = parseLinks(out)
	if out, err = run("listing routes", routeProbe); err != nil {
		return nil, err
	}
	if inv.Routes, err = parseRoutes(out); err != nil {
		return nil, fail("listing routes", err)
	}

	if out, err = run("listing "+render.HostSitesDir(d), hostSitesProbe(d)); err != nil {
		return nil, err
	}
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			inv.HostSites = append(inv.HostSites, line)
		}
	}
	sort.Strings(inv.HostSites)

	content, found, err := t.ReadFile(inv.ManifestPath)
	if err != nil {
		return nil, fail("reading "+inv.ManifestPath, err)
	}
	inv.Manifest = found
	if found {
		// An unreadable manifest records nothing, so a mesh interface beside
		// it is not known to be the toolkit's. apply refuses that manifest itself.
		m, err := render.ParseManifest([]byte(content))
		var noBuild *version.NoBuildError
		if errors.As(err, &noBuild) {
			inv.ManifestErr = fmt.Errorf("%s: %w. Apply the site again, which writes it anew", inv.ManifestPath, err)
			err = nil
		}
		// A newer paisans' manifest is read, its version with it, for the
		// commands that refuse it to say to upgrade.
		if version.IsNewer(err) {
			err = nil
		}
		if err == nil {
			inv.ManifestFiles = m.Files
			inv.ManifestWebServer = m.WebServer
			inv.ManifestPending = m.Pending
			inv.ManifestBuild = m.Build
			for _, f := range m.Files {
				if f.Path == d.WireGuardConf() {
					inv.ManifestWireGuard = true
				}
			}
		}
	}
	return inv, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
