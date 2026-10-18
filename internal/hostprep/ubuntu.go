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
// profiles/ubuntu/, shared by every release registered here: each was
// checked against those templates, and whatever differs by release, such as
// the codename in Docker's apt source, is read from the host. A release whose
// shell differs needs a directory of its own.
type ubuntu struct {
	version string
}

func init() {
	register(ubuntu{version: "22.04"})
	register(ubuntu{version: "24.04"})
	register(ubuntu{version: "26.04"})
}

func (u ubuntu) ID() string      { return "ubuntu" }
func (u ubuntu) Version() string { return u.version }

func (u ubuntu) tmpl(name string) string { return "profiles/ubuntu/" + name }

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

// admitBasePackages are the base packages site admit installs: ufw is not
// among them, since admit works only on a host whose owner has installed
// and enabled it, and choosing a firewall is the owner's.
var admitBasePackages = []string{"wireguard-tools", "git"}

// rootlessPackages are what a deployment's own rootless daemon needs
// beside Docker: the setup tool and rootlesskit, newuidmap and newgidmap,
// and nft for the egress table. Docker's rootless page names the first two
// (https://docs.docker.com/engine/security/rootless/, read 2026-10-15).
var rootlessPackages = []string{"docker-ce-rootless-extras", "uidmap", "nftables"}

// withoutRecommends are installed with --no-install-recommends, in an apt
// call of their own. Ubuntu 22.04's wireguard-tools recommends
// wireguard-modules or wireguard-dkms, which on a host whose running kernel
// is not one of Ubuntu's (a provider's own kernel, a container) pulls in a
// whole Ubuntu kernel; the kernel's own WireGuard is what the mesh uses.
// Every other package keeps its recommendations, since Docker's recommend
// what Docker needs (iptables, pigz, the rootless extras).
var withoutRecommends = []string{"wireguard-tools"}

// aptInstall is the script that installs packages, those in
// withoutRecommends by a second apt call without their recommendations.
func (u ubuntu) aptInstall(packages []string, noUpgrade bool) (string, error) {
	var with, without []string
	for _, p := range packages {
		name, _, _ := strings.Cut(p, "=")
		if slices.Contains(withoutRecommends, name) {
			without = append(without, p)
		} else {
			with = append(with, p)
		}
	}
	return snippet(u.tmpl("apt-install.sh.tmpl"), map[string]any{"Packages": with, "WithoutRecommends": without, "NoUpgrade": noUpgrade})
}

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

// SnapDockerAdmit is site admit's refusal of a snap Docker. Admit's own
// probe refuses it first; AdmitPackages refuses it too, in the same words,
// for a caller that has not.
const SnapDockerAdmit = "Docker is installed as a snap, whose confinement keeps it from bind mounting under /srv and whose refreshes restart every container on snapd's schedule. Remove it yourself (snap remove docker), after checking nothing running depends on it, and admit again. Nothing was changed"

// HoldDockerMarker is the line that marks /usr/sbin/policy-rc.d as the one
// admit puts there while it installs Docker (docker-hold.sh.tmpl).
const HoldDockerMarker = "Written by paisans site admit while it installs Docker, and removed after."

// Installed is what an admit install adds that the host did not have:
// each package by name, and Docker's apt key and source by path.
type Installed struct {
	Packages, Files []string
}

// SystemUnits are the system units of the packages given that start a
// daemon: Docker's socket and service for docker-ce, containerd's service
// for containerd.io, in that order.
func SystemUnits(packages []string) []string {
	var units []string
	if slices.Contains(packages, "docker-ce") {
		units = append(units, "docker.socket", "docker.service")
	}
	if slices.Contains(packages, "containerd.io") {
		units = append(units, "containerd.service")
	}
	return units
}

// DockerHoldLock takes the hold's lock for the rest of the shell, or
// refuses while another run holds it; the hold then keeps the lock its
// shell took.
func (u ubuntu) DockerHoldLock() (string, error) {
	return snippet(u.tmpl("docker-hold-lock.sh.tmpl"), nil)
}

// ReleaseDockerHold is a script that puts /usr/sbin/policy-rc.d back as
// it was before admit held Docker's daemons, for a run that stopped while
// it held them, and fails, naming both, when the owner's copy differs from
// what is in place, which is the owner's to choose between. It takes the
// hold's lock first, and refuses while a running admit holds it; before
// runs then, ahead of the release.
func (u ubuntu) ReleaseDockerHold(before string) (string, error) {
	release, err := snippet(u.tmpl("docker-hold-release.sh.tmpl"), map[string]string{"Marker": HoldDockerMarker})
	if err != nil {
		return "", err
	}
	lock, err := snippet(u.tmpl("docker-hold-lock.sh.tmpl"), nil)
	if err != nil {
		return "", err
	}
	return "set -e\n" + lock + release + before + "release_docker_hold\n" +
		"if [ -e /usr/sbin/policy-rc.d.paisans-owner ] || [ -L /usr/sbin/policy-rc.d.paisans-owner ]; then echo '/usr/sbin/policy-rc.d.paisans-owner, a copy of your policy-rc.d admit made while it installed Docker, differs from the /usr/sbin/policy-rc.d there now. Keep the one you want as /usr/sbin/policy-rc.d, remove the other, and run again'; exit 1; fi\n", nil
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
		// Refused, not removed, so that apt never removes the owner's
		// containerd, runc or docker.io to make room.
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
	script, err := u.aptInstall(install, dockerPresent)
	if err != nil {
		return out, added, err
	}
	describe := "packages: install " + strings.Join(install, " ")
	if dockerPresent {
		describe += ", leaving your Docker at the version it is"
	}
	if !dockerPresent && facts.installed["containerd.io"] {
		// containerd.io is the owner's, and is neither held nor claimed,
		// but docker-ce depends on a version of it, which apt installs.
		describe += "; your containerd.io may be upgraded to the version docker-ce needs, which restarts it, though its containers run on"
	}
	if units := SystemUnits(added.Packages); len(units) > 0 {
		// docker-ce's and containerd.io's scripts would start their system
		// daemons, and dockerd changes forwarding and the firewall as it
		// starts: each this install adds is held for it instead, so it
		// never starts. A containerd.io the host had is the owner's, and
		// is not held.
		var names []string
		for _, unit := range units {
			names = append(names, unit)
			if base, ok := strings.CutSuffix(unit, ".service"); ok {
				names = append([]string{base}, names...)
			}
		}
		slices.Sort(names)
		release, err := snippet(u.tmpl("docker-hold-release.sh.tmpl"), map[string]string{"Marker": HoldDockerMarker})
		if err != nil {
			return out, added, err
		}
		lock, err := snippet(u.tmpl("docker-hold-lock.sh.tmpl"), nil)
		if err != nil {
			return out, added, err
		}
		hold, err := snippet(u.tmpl("docker-hold.sh.tmpl"), map[string]string{"Marker": HoldDockerMarker, "Release": release, "Lock": lock, "Units": strings.Join(names, "|"), "Disable": strings.Join(units, " ")})
		if err != nil {
			return out, added, err
		}
		script = hold + script
		describe += "; Docker's system daemon is kept from starting while its packages install, by a /usr/sbin/policy-rc.d of admit's that asks yours, if you have one, about every other service and is replaced by yours again after"
	}
	out.Steps = append(out.Steps, Step{Kinds: packageKinds(install), Title: "install packages: " + strings.Join(install, ", "), Describe: describe, Command: script})
	return out, added, nil
}

// unitState reads `systemctl is-enabled` and `is-active` for one unit of
// the manager scope names.
func (u ubuntu) unitState(t Transport, scope unitScope, unit string) (enabled, active bool, err error) {
	script, err := snippet(u.tmpl("services-probe.sh.tmpl"), map[string]string{"Unit": unit, "Systemctl": scope.systemctl})
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
			active = rest == "active" || (scope.activatingIsActive && rest == "activating")
		}
	}
	return enabled, active, nil
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
	path := systemScope.dir + "/" + unitName
	current, found, err := t.ReadFile(path)
	if err != nil {
		return out, err
	}
	enabled, _, err := u.unitState(t, systemScope, unitName)
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

// Firewall plans the deployment's ufw rules: every allow first, then the
// removals, so a rule is only taken away once everything it might have stood
// in for is in place. Which rules are added, removed or left alone is
// planRules, in ufw.go. The default policy and whether ufw is on are the
// owner's, and never planned here.
func (u ubuntu) Firewall(t Transport, d deployment.Deployment, rules []Rule) (Section, error) {
	var out Section
	script, err := snippet(u.tmpl("firewall-probe.sh.tmpl"), nil)
	if err != nil {
		return out, err
	}
	probe, err := t.Run(script)
	if err != nil {
		return out, err
	}
	var added []addedRule
	for _, line := range strings.Split(probe, "\n") {
		key, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
		if key != "rule" {
			continue
		}
		if a, ok := parseAdded(rest); ok {
			added = append(added, a)
		}
	}

	planned, removals, err := planRules(ownerTag(d), rules, added)
	if err != nil {
		return out, err
	}
	out.add(planned)
	out.Steps = append(out.Steps, removals...)
	return out, nil
}
