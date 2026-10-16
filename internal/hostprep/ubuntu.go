package hostprep

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// ubuntu is the profile for Ubuntu releases. Its shell lives in
// profiles/ubuntu-<version>/; a second release that differs only in its
// templates is a second directory and a second register call.
type ubuntu struct {
	version string
}

func init() {
	register(ubuntu{version: "24.04"})
}

func (u ubuntu) ID() string      { return "ubuntu" }
func (u ubuntu) Version() string { return u.version }

func (u ubuntu) tmpl(name string) string { return "profiles/ubuntu-" + u.version + "/" + name }

// dockerPackages are the ones Docker's install page names, step 2 of "Install
// using the apt repository": https://docs.docker.com/engine/install/ubuntu/
// (fetched 2026-10-06).
var dockerPackages = []string{"docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin"}

// dockerConflicts are the packages the same page says to remove first,
// containerd and runc included because Docker Engine ships its own.
var dockerConflicts = []string{"docker.io", "docker-compose", "docker-compose-v2", "docker-doc", "docker-buildx", "podman-docker", "containerd", "runc"}

// basePackages are needed whatever else is installed: wg and wg-quick for
// the mesh, the firewall this profile drives, and git for the configuration
// store.
var basePackages = []string{"wireguard-tools", "ufw", "git"}

// admitBasePackages are the base packages host admit installs: ufw is not
// among them, since admit works only on a host whose owner has installed
// and enabled it, and choosing a firewall is the owner's.
var admitBasePackages = []string{"wireguard-tools", "git"}

// rootlessPackages are what a deployment's own rootless daemon needs
// beside Docker: the setup tool and rootlesskit, newuidmap and newgidmap,
// and nft for the egress table. Docker's rootless page names the first two
// (https://docs.docker.com/engine/security/rootless/, read 2026-10-15).
var rootlessPackages = []string{"docker-ce-rootless-extras", "uidmap", "nftables"}

// packageKinds is the kind of change each package in an install makes, each
// once, in the order the packages are named: Docker's are docker, and each
// base package is what it is installed for.
func packageKinds(packages []string) []string {
	var out []string
	for _, p := range packages {
		kind := "packages"
		switch {
		case slices.Contains(dockerPackages, p), slices.Contains(rootlessPackages, p):
			kind = KindDocker
		case p == "wireguard-tools":
			kind = KindWireGuard
		case p == "ufw":
			kind = KindFirewall
		case p == "git":
			kind = KindConfigStore
		}
		if !slices.Contains(out, kind) {
			out = append(out, kind)
		}
	}
	return out
}

const (
	dockerKeyring = "/etc/apt/keyrings/docker.asc"
	dockerSources = "/etc/apt/sources.list.d/docker.sources"
)

type packageFacts struct {
	composeWorks bool
	installed    map[string]bool
	keyring      bool
	arch         string
	snapDocker   bool
	// dockerVersion is docker-ce's installed dpkg version, "" when it is not
	// installed; extrasVersions are the versions of docker-ce-rootless-extras
	// the configured repositories offer.
	dockerVersion  string
	extrasVersions []string
}

func (u ubuntu) probePackages(t Transport) (packageFacts, error) {
	all := append(append(append(append([]string{}, dockerPackages...), dockerConflicts...), rootlessPackages...), basePackages...)
	script, err := snippet(u.tmpl("packages-probe.sh.tmpl"), map[string]any{"Packages": all})
	if err != nil {
		return packageFacts{}, err
	}
	out, err := t.Run(script)
	if err != nil {
		return packageFacts{}, err
	}
	facts := packageFacts{installed: map[string]bool{}}
	for _, line := range strings.Split(out, "\n") {
		key, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch key {
		case "compose":
			facts.composeWorks = rest == "present"
		case "pkg":
			name, status, _ := strings.Cut(rest, " ")
			// dpkg's "install ok installed". "deinstall ok config-files" is a
			// removed package whose configuration was kept, which is absent.
			facts.installed[name] = strings.HasSuffix(strings.TrimSpace(status), "ok installed")
		case "keyring":
			facts.keyring = rest == "present"
		case "arch":
			facts.arch = strings.TrimSpace(rest)
		case "snap":
			facts.snapDocker = strings.TrimSpace(rest) == "docker"
		case "dockerversion":
			facts.dockerVersion = strings.TrimSpace(rest)
		case "extras":
			if v := strings.TrimSpace(rest); v != "" {
				facts.extrasVersions = append(facts.extrasVersions, v)
			}
		}
	}
	return facts, nil
}

// DockerKeyFingerprint is the fingerprint of Docker's release key, which
// signs its apt repository (Docker's install guide for Ubuntu gives it as
// 9DC8 5822 9FC7 DD38 854A E2D8 8D81 803C 0EBF CD88).
const DockerKeyFingerprint = "9DC858229FC7DD38854AE2D88D81803C0EBFCD88"

// dockerRepo plans Docker's apt key and repository, each only when it is
// not already as written.
func (u ubuntu) dockerRepo(t Transport, host OSRelease, facts packageFacts) (Section, error) {
	var out Section
	if facts.keyring {
		out.Present = append(out.Present, "docker: apt signing key at "+dockerKeyring)
	} else {
		script, err := snippet(u.tmpl("docker-keyring.sh.tmpl"), map[string]string{"Fingerprint": DockerKeyFingerprint})
		if err != nil {
			return out, err
		}
		out.Steps = append(out.Steps, Step{Kinds: []string{KindDocker}, Title: "add docker apt key", Describe: "docker: add Docker's apt signing key at " + dockerKeyring, Command: script})
	}

	codename := host.Fields["UBUNTU_CODENAME"]
	if codename == "" {
		codename = host.Fields["VERSION_CODENAME"]
	}
	if codename == "" || facts.arch == "" {
		return out, fmt.Errorf("docker: could not read the release codename (%q) or the dpkg architecture (%q), and Docker's apt source needs both", codename, facts.arch)
	}
	sources, err := snippet(u.tmpl("docker.sources.tmpl"), map[string]string{"Codename": codename, "Arch": facts.arch})
	if err != nil {
		return out, err
	}
	current, found, err := t.ReadFile(dockerSources)
	if err != nil {
		return out, err
	}
	if found && current == sources {
		out.Present = append(out.Present, "docker: apt repository at "+dockerSources)
	} else {
		out.Steps = append(out.Steps, Step{
			Kinds:    []string{KindDocker},
			Title:    "add docker apt repo",
			Describe: fmt.Sprintf("docker: add Docker's apt repository for %s/%s at %s", codename, facts.arch, dockerSources),
			File:     &File{Path: dockerSources, Content: sources, Mode: 0o644},
		})
	}
	return out, nil
}

// conflictMessage is why Docker's own packages cannot be installed beside
// what the host carries from Ubuntu's archive, or "" when nothing conflicts.
// It refuses, not removes: removing docker.io on a host that is already
// running containers from it takes those containers down, and that is a
// decision for whoever started them. again is the command the owner runs
// once they have decided. Admit asks only where no Docker is installed, so
// its reason is the removal, not compose.
func conflictMessage(facts packageFacts, again string) string {
	var conflicts []string
	for _, p := range dockerConflicts {
		if facts.installed[p] {
			conflicts = append(conflicts, p)
		}
	}
	if len(conflicts) == 0 {
		return ""
	}
	// Ubuntu's docker-compose-v2 is the compose plugin
	// (/usr/libexec/docker/cli-plugins/docker-compose in its file
	// list, packages.ubuntu.com, noble-updates, fetched 2026-10-11),
	// so a docker.io without it can be completed instead.
	remove := fmt.Sprintf("Remove them yourself (apt-get remove %s)", strings.Join(conflicts, " "))
	if facts.installed["docker.io"] && !facts.installed["docker-compose-v2"] {
		remove = "Install Ubuntu's compose plugin beside them (apt-get install docker-compose-v2), or r" + remove[1:]
	}
	why := "`docker compose` does not work with them"
	if again == "admit" {
		why = "installing Docker's packages would remove them"
	}
	return fmt.Sprintf("%s from Ubuntu's archive conflict with Docker's own packages, and %s. %s, after checking nothing running depends on them, and %s again",
		strings.Join(conflicts, ", "), why, remove, again)
}

// Packages leaves an existing Docker as it is when `docker compose` works
// with it, whatever its source: the host check has already refused a snap,
// and an engine or compose plugin older than the toolkit needs. Where Docker
// is absent it installs it from Docker's own repository, because an install
// needs one source and that one gives every host it prepares the same
// engine and compose plugin.
func (u ubuntu) Packages(t Transport, host OSRelease) (Section, error) {
	var out Section
	facts, err := u.probePackages(t)
	if err != nil {
		return out, err
	}

	// Refused, not removed, for the docker.io reason below, and whether or
	// not compose works with it. The host check refuses it first; this is
	// the same rule where the plan is built. A snap's confinement keeps
	// Docker from bind mounting under /srv, and snapd refreshes it, which
	// restarts the daemon, on its own schedule.
	if facts.snapDocker {
		return out, fmt.Errorf("docker: Docker is installed as a snap, whose confinement keeps it from bind mounting under /srv and whose refreshes restart every container on snapd's schedule. Remove it yourself (snap remove docker), after checking nothing running depends on it, and prepare again, which installs Docker from Docker's own apt repository")
	}

	var install []string
	if facts.composeWorks {
		out.Present = append(out.Present, "docker: `docker compose` already works")
	} else {
		// A Docker from Ubuntu's archive is accepted when compose works with
		// it. Where it does not, the install below would conflict with it,
		// so it is refused, not removed: removing docker.io on a host that is
		// already running containers from it takes those containers down, and
		// that is a decision for whoever started them.
		if msg := conflictMessage(facts, "prepare"); msg != "" {
			return out, fmt.Errorf("docker: %s", msg)
		}

		repo, err := u.dockerRepo(t, host, facts)
		if err != nil {
			return out, err
		}
		out.add(repo)
		for _, p := range dockerPackages {
			if !facts.installed[p] {
				install = append(install, p)
			}
		}
	}

	for _, p := range basePackages {
		if facts.installed[p] {
			out.Present = append(out.Present, "package: "+p+" installed")
		} else {
			install = append(install, p)
		}
	}
	if len(install) > 0 {
		script, err := snippet(u.tmpl("apt-install.sh.tmpl"), map[string]any{"Packages": install, "NoUpgrade": false})
		if err != nil {
			return out, err
		}
		out.Steps = append(out.Steps, Step{Kinds: packageKinds(install), Title: "install packages: " + strings.Join(install, ", "), Describe: "packages: install " + strings.Join(install, " "), Command: script})
	}
	return out, nil
}

// debVersion is what dpkg versions are made of; the pin is interpolated
// into a shell command, so nothing else is accepted.
var debVersion = regexp.MustCompile(`^[0-9A-Za-z.+:~-]+$`)

// pinExtras is the extras package pinned to the installed docker-ce's
// version, or the refusal when the repository offers no such version.
func pinExtras(facts packageFacts) (string, error) {
	// dpkg keeps the version of a docker-ce removed with its configuration
	// kept, and pinning to it would install docker-ce again.
	v := facts.dockerVersion
	if !facts.installed["docker-ce"] || v == "" || !debVersion.MatchString(v) {
		return "", fmt.Errorf("docker-ce is not installed as a Debian package here, so docker-ce-rootless-extras cannot be matched to your Docker's version. Install Docker from Docker's own apt repository yourself, or admit on another host. Nothing was changed")
	}
	if len(facts.extrasVersions) == 0 {
		return "", fmt.Errorf("docker-ce %s is installed and the apt repositories offer no docker-ce-rootless-extras, so Docker's apt repository is not configured here, or apt-get update has not run since it was. Add it as Docker's install guide for Ubuntu says (https://docs.docker.com/engine/install/ubuntu/), run apt-get update, and admit again. Nothing was changed", v)
	}
	if !slices.Contains(facts.extrasVersions, v) {
		return "", fmt.Errorf("docker-ce %s is installed and the apt repositories offer docker-ce-rootless-extras at %s. The extras must match your Docker exactly, or installing them upgrades it, which is not admit's decision. Run apt-get update and admit again, or upgrade Docker yourself first. Nothing was changed", v, strings.Join(facts.extrasVersions, ", "))
	}
	return "docker-ce-rootless-extras=" + v, nil
}

// SnapDockerAdmit is host admit's refusal of a snap Docker. Admit's own
// probe refuses it first; AdmitPackages refuses it too, in the same words,
// for a caller that has not.
const SnapDockerAdmit = "Docker is installed as a snap, whose confinement keeps it from bind mounting under /srv and whose refreshes restart every container on snapd's schedule. Remove it yourself (snap remove docker), after checking nothing running depends on it, and admit again. Nothing was changed"

// HoldDockerMarker is the line that marks /usr/sbin/policy-rc.d as the one
// admit puts there while it installs Docker (docker-hold.sh.tmpl).
const HoldDockerMarker = "Written by paisans host admit while it installs Docker, and removed after."

// Installed is what an admit install adds that the host did not have:
// each package by name, and Docker's apt key and source by path.
type Installed struct {
	Packages, Files []string
}

// ReleaseDockerHold is a script that puts /usr/sbin/policy-rc.d back as
// it was before admit held Docker's daemons, for a run that stopped while
// it held them.
func (u ubuntu) ReleaseDockerHold() (string, error) {
	release, err := snippet(u.tmpl("docker-hold-release.sh.tmpl"), map[string]string{"Marker": HoldDockerMarker})
	if err != nil {
		return "", err
	}
	return "set -e\n" + release + "release_docker_hold\n", nil
}

// AdmitPackages: see Profile.
func (u ubuntu) AdmitPackages(t Transport, host OSRelease, dockerPresent bool) (Section, Installed, error) {
	var out Section
	var added Installed
	facts, err := u.probePackages(t)
	if err != nil {
		return out, added, err
	}
	if facts.snapDocker {
		return out, added, errors.New("docker: " + SnapDockerAdmit)
	}
	var install []string
	if !dockerPresent {
		// Refused, as host prepare refuses, so that apt never removes the
		// owner's containerd, runc or docker.io to make room.
		if msg := conflictMessage(facts, "admit"); msg != "" {
			return out, added, fmt.Errorf("%s. Nothing was changed", msg)
		}
		repo, err := u.dockerRepo(t, host, facts)
		if err != nil {
			return out, added, err
		}
		out.add(repo)
		if !facts.keyring {
			added.Files = append(added.Files, dockerKeyring)
		}
		_, found, err := t.ReadFile(dockerSources)
		if err != nil {
			return out, added, err
		}
		if !found {
			added.Files = append(added.Files, dockerSources)
		}
		for _, p := range dockerPackages {
			if !facts.installed[p] {
				install = append(install, p)
			}
		}
	}
	for _, p := range append(append([]string{}, rootlessPackages...), admitBasePackages...) {
		switch {
		case facts.installed[p]:
			out.Present = append(out.Present, "package: "+p+" installed")
		case p == "docker-ce-rootless-extras" && dockerPresent:
			// apt resolves the extras' dependency on docker-ce to the
			// newest version, upgrading the owner's Docker, which
			// --no-upgrade does not prevent for a dependency. Pinning the
			// extras to the installed docker-ce version leaves nothing for
			// the resolver to upgrade.
			pinned, err := pinExtras(facts)
			if err != nil {
				return out, added, err
			}
			install = append(install, pinned)
		default:
			install = append(install, p)
		}
	}
	if len(install) == 0 {
		return out, added, nil
	}
	for _, p := range install {
		name, _, _ := strings.Cut(p, "=")
		added.Packages = append(added.Packages, name)
	}
	script, err := snippet(u.tmpl("apt-install.sh.tmpl"), map[string]any{"Packages": install, "NoUpgrade": dockerPresent})
	if err != nil {
		return out, added, err
	}
	describe := "packages: install " + strings.Join(install, " ")
	if dockerPresent {
		describe += ", leaving your Docker at the version it is"
	}
	if slices.Contains(install, "docker-ce") {
		// docker-ce's and containerd.io's scripts would start the system
		// daemon, which changes forwarding and the firewall as it starts:
		// it is held for the install instead, so it never starts.
		release, err := snippet(u.tmpl("docker-hold-release.sh.tmpl"), map[string]string{"Marker": HoldDockerMarker})
		if err != nil {
			return out, added, err
		}
		hold, err := snippet(u.tmpl("docker-hold.sh.tmpl"), map[string]string{"Marker": HoldDockerMarker, "Release": release})
		if err != nil {
			return out, added, err
		}
		script = hold + script
		describe += "; Docker's system daemon is kept from starting while its packages install, by a /usr/sbin/policy-rc.d of admit's that asks yours, if you have one, about every other service and is replaced by yours again after"
	}
	out.Steps = append(out.Steps, Step{Kinds: packageKinds(install), Title: "install packages: " + strings.Join(install, ", "), Describe: describe, Command: script})
	return out, added, nil
}

// unitState reads `systemctl is-enabled` and `is-active` for one unit.
func (u ubuntu) unitState(t Transport, unit string) (enabled, active bool, err error) {
	script, err := snippet(u.tmpl("services-probe.sh.tmpl"), map[string]string{"Unit": unit})
	if err != nil {
		return false, false, err
	}
	out, err := t.Run(script)
	if err != nil {
		return false, false, err
	}
	for _, line := range strings.Split(out, "\n") {
		key, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch key {
		case "enabled":
			enabled = rest == "enabled"
		case "active":
			active = rest == "active"
		}
	}
	return enabled, active, nil
}

// dockerAfterWireGuard is the drop-in that orders Docker after the
// deployment's mesh interface, named for the deployment that wrote it. Each
// deployment on a host writes its own, so Docker waits for every mesh whose
// address a container binds.
func dockerAfterWireGuard(d deployment.Deployment) string {
	return "/etc/systemd/system/docker.service.d/" + d.Prefix() + "-after-wireguard.conf"
}

// Services makes Docker start at boot and now, and after the deployment's
// mesh interface at boot. Docker's
// packages enable it on install, so on a freshly installed host enabling it is
// a no-op that is planned anyway: the probe runs before the install and cannot
// know that.
//
// The ordering is a drop-in rather than an edit to Docker's own unit, which a
// package upgrade replaces. Writing it needs only a daemon-reload: it takes
// effect at the next boot, which is the only moment it matters, so Docker and
// every container on the host keep running.
func (u ubuntu) Services(t Transport, d deployment.Deployment) (Section, error) {
	dropInPath := dockerAfterWireGuard(d)
	var out Section
	dropIn, err := snippet(u.tmpl("docker-after-wireguard.conf.tmpl"), map[string]string{"Interface": d.Interface(), "Unit": d.WireGuardUnit()})
	if err != nil {
		return out, err
	}
	current, found, err := t.ReadFile(dropInPath)
	if err != nil {
		return out, err
	}
	if found && current == dropIn {
		out.Present = append(out.Present, "service: docker starts after "+d.Interface()+" at boot")
	} else {
		out.Steps = append(out.Steps, Step{
			Kinds:    []string{KindDocker},
			Title:    "start docker after mesh",
			Describe: "service: start docker after " + d.Interface() + " at boot, so containers can bind the mesh address (" + dropInPath + ")",
			File:     &File{Path: dropInPath, Content: dropIn, Mode: 0o644},
			Command:  "systemctl daemon-reload",
		})
	}
	enabled, active, err := u.unitState(t, "docker")
	if err != nil {
		return out, err
	}
	if enabled && active {
		out.Present = append(out.Present, "service: docker enabled and running")
		return out, nil
	}
	out.Steps = append(out.Steps, Step{Kinds: []string{KindDocker}, Title: "start docker", Describe: "service: enable and start docker", Command: "systemctl enable --now docker"})
	return out, nil
}

// watchdogUnit is the unit that loads the watchdog module at boot, named for
// the deployment that wrote it.
func watchdogUnit(d deployment.Deployment) string { return d.Prefix() + "-watchdog.service" }

// WatchdogModule loads the module now with modprobe and at boot with a unit,
// not a modules-load.d entry. Ubuntu's kernel package ships a modprobe
// blacklist for the watchdog drivers, softdog among them, and
// systemd-modules-load honours it (Launchpad bug 1535840; systemd's
// module_load_and_warn probes with KMOD_PROBE_APPLY_BLACKLIST), so a
// modules-load.d file would be skipped at every boot while looking correct.
// modprobe named on a command line does not apply the blacklist.
func (u ubuntu) WatchdogModule(t Transport, d deployment.Deployment, module string, loaded, admit bool) (Section, error) {
	var out Section
	unitName := watchdogUnit(d)
	if !loaded {
		out.Steps = append(out.Steps, Step{Kinds: []string{KindWatchdog}, Title: "load " + module, Describe: "watchdog: load " + module + " now", Command: "modprobe " + module})
	}
	unit, err := snippet(u.tmpl("watchdog-module.service.tmpl"), map[string]any{"Module": module, "Admit": admit})
	if err != nil {
		return out, err
	}
	path := "/etc/systemd/system/" + unitName
	current, found, err := t.ReadFile(path)
	if err != nil {
		return out, err
	}
	enabled, _, err := u.unitState(t, unitName)
	if err != nil {
		return out, err
	}
	switch {
	case !found || current != unit:
		out.Steps = append(out.Steps, Step{
			Kinds:    []string{KindWatchdog},
			Title:    "load " + module + " at boot",
			Describe: fmt.Sprintf("watchdog: load %s at every boot (%s)", module, unitName),
			File:     &File{Path: path, Content: unit, Mode: 0o644},
			Command:  "systemctl daemon-reload && systemctl enable " + unitName,
		})
	case !enabled:
		out.Steps = append(out.Steps, Step{
			Kinds:    []string{KindWatchdog},
			Title:    "enable " + module + " at boot",
			Describe: fmt.Sprintf("watchdog: enable %s so %s loads at every boot", unitName, module),
			Command:  "systemctl enable " + unitName,
		})
	default:
		out.Present = append(out.Present, fmt.Sprintf("watchdog: %s loads %s at every boot", unitName, module))
	}
	return out, nil
}

// Firewall drives ufw. The order of the steps is the safety property: every
// allow and adoption, SSH first, then the default policy, then enabling, so
// there is no moment at which the firewall is on and SSH is not allowed; and
// removals last, so a rule is only taken away once everything it might have
// stood in for is in place. `--force` is what stops `ufw enable` asking
// whether to disrupt existing connections, a prompt that would hang a
// non-interactive run. Which rules are added, adopted, removed or left alone
// is planRules, in ufw.go. On a shared host (hostWide false) only the rules
// are planned: the default policy and enabling belong to the host, not to
// the deployment. by names the command planning, host prepare or host
// admit, in what the plan says.
func (u ubuntu) Firewall(t Transport, d deployment.Deployment, rules []Rule, hostWide bool, by string) (Section, error) {
	var out Section
	script, err := snippet(u.tmpl("firewall-probe.sh.tmpl"), nil)
	if err != nil {
		return out, err
	}
	probe, err := t.Run(script)
	if err != nil {
		return out, err
	}
	var present, active bool
	var added []addedRule
	for _, line := range strings.Split(probe, "\n") {
		key, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch key {
		case "ufw":
			present = rest == "present"
		case "status":
			active = rest == "active"
		case "rule":
			if a, ok := parseAdded(rest); ok {
				added = append(added, a)
			}
		}
	}

	planned, removals, err := planRules(ownerTag(d), rules, added, by)
	if err != nil {
		return out, err
	}
	out.add(planned)

	if !hostWide {
		out.Present = append(out.Present, "firewall: default policy and enabled state left alone, since the host is shared")
		out.Steps = append(out.Steps, removals...)
		return out, nil
	}

	input, output := "", ""
	if present {
		defaults, found, err := t.ReadFile("/etc/default/ufw")
		if err != nil {
			return out, err
		}
		if found {
			fields := ParseOSRelease(defaults).Fields // the same KEY="value" shape
			input, output = fields["DEFAULT_INPUT_POLICY"], fields["DEFAULT_OUTPUT_POLICY"]
		}
	}
	if input == "DROP" {
		out.Present = append(out.Present, "firewall: incoming denied by default")
	} else {
		out.Steps = append(out.Steps, Step{Kinds: []string{KindFirewall}, Title: "deny incoming", Describe: "firewall: deny incoming by default", Command: "ufw default deny incoming"})
	}
	if output == "ACCEPT" {
		out.Present = append(out.Present, "firewall: outgoing allowed by default")
	} else {
		out.Steps = append(out.Steps, Step{Kinds: []string{KindFirewall}, Title: "allow outgoing", Describe: "firewall: allow outgoing by default", Command: "ufw default allow outgoing"})
	}
	if active {
		out.Present = append(out.Present, "firewall: ufw active")
	} else {
		out.Steps = append(out.Steps, Step{Kinds: []string{KindFirewall}, Title: "enable ufw", Describe: "firewall: enable ufw", Command: "ufw --force enable"})
	}
	out.Steps = append(out.Steps, removals...)
	return out, nil
}
