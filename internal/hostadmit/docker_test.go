package hostadmit_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

var dockerCases = addCases(
	goldenCase{name: "docker-blank", focus: []string{"docker apt", "install packages", "system Docker", "rootless Docker"}, host: newBlank, site: "vm"},
	goldenCase{name: "docker-blank-jammy", host: newJammy, site: "vm"},
	goldenCase{name: "docker-current-jammy", focus: []string{"install packages", "rootless Docker"}, site: "vm", host: func() *fakeHost {
		return newJammy().with("engine", "29.9.1").with("system-socket", "present").withPkg("docker-ce", "installed").
			answer("docker compose version", "compose present\npkg docker-ce install ok installed\npkg ufw install ok installed\npkg git install ok installed\npkg wireguard-tools install ok installed\npkg nftables install ok installed\ndockerversion 5:29.9.1-1~ubuntu.22.04~jammy\nextras 5:29.9.1-1~ubuntu.22.04~jammy\nkeyring present\narch amd64\n")
	}},
	goldenCase{name: "docker-old-refused", site: "vm", host: func() *fakeHost {
		return newBlank().with("engine", "27.5.1").withPkg("docker-ce", "installed").with("system-socket", "present")
	}},
	goldenCase{name: "docker-current", focus: []string{"install packages", "rootless Docker"}, site: "vm", host: func() *fakeHost {
		return newBlank().with("engine", "29.9.1").with("system-socket", "present").withPkg("docker-ce", "installed").
			answer("docker compose version", "compose present\npkg docker-ce install ok installed\npkg ufw install ok installed\npkg git install ok installed\npkg wireguard-tools install ok installed\npkg nftables install ok installed\ndockerversion 5:29.9.1-1~ubuntu.24.04~noble\nextras 5:29.9.1-1~ubuntu.24.04~noble\nkeyring present\narch amd64\n")
	}},
	goldenCase{name: "docker-snap-refused", site: "vm", host: func() *fakeHost {
		return newBlank().with("snap", "docker").with("engine", "29.9.1")
	}},
	goldenCase{name: "docker-distro-without-extras-refused", site: "vm", host: func() *fakeHost {
		return newBlank().with("engine", "29.6.0").withPkg("docker.io", "installed")
	}},
	goldenCase{name: "docker-blank-with-ubuntu-containerd-refused", site: "vm", host: func() *fakeHost {
		return newBlank().answer("docker compose version", "compose absent\npkg containerd install ok installed\npkg runc install ok installed\nkeyring absent\narch amd64\n")
	}},
	goldenCase{name: "apparmor-without-profile-refused", site: "vm", host: func() *fakeHost {
		return newBlank().with("rootlesskit-profile", "absent")
	}},
	goldenCase{name: "apparmor-not-shipped-refused", site: "vm", host: func() *fakeHost {
		return newJammy().with("userns-restrict", "1")
	}},
	goldenCase{name: "apparmor-unrestricted", focus: []string{"install packages", "rootless Docker"}, site: "vm", host: func() *fakeHost {
		return newBlank().with("userns-restrict", "0").with("rootlesskit-profile", "absent")
	}},
)

func TestDockerPlans(t *testing.T) { runGolden(t, dockerCases) }

// withPkg is h with one package's probe line set to state, the line it
// had replaced, as a real probe prints one line per package.
func (h *fakeHost) withPkg(name, state string) *fakeHost {
	h.dropKeyed("pkg", name)
	return h.add("pkg", name+" "+state)
}

// A dockerd whose version cannot be read is refused, never read as no
// Docker: admit would then install Docker's packages over the owner's.
func TestAnUnreadableEngineIsRefused(t *testing.T) {
	h := newBlank().with("engine", "")
	_, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	if err == nil || !strings.Contains(err.Error(), "dockerd --version") {
		t.Errorf("an unreadable engine: %v", err)
	}
}

// The AppArmor check guards each of its three cases: no restriction
// passes, a restriction with rootlesskit's profile loaded passes, and a
// restriction without it is refused before any package is installed.
// Where the host's apparmor package ships no rootlesskit profile, as
// Ubuntu 22.04's does not, the refusal says to lift the restriction
// instead of loading a profile that is not there.
func TestAppArmorGuardsEachCase(t *testing.T) {
	for _, c := range []struct {
		restrict, profile, want string
	}{
		{"0", "absent", "present: apparmor: unprivileged user namespaces are not restricted"},
		{"1", "loaded", "present: apparmor: rootlesskit's profile is loaded"},
		{"1", "absent", "refused: apparmor: AppArmor restricts unprivileged user namespaces here and no rootlesskit profile is loaded. The profile is /etc/apparmor.d/rootlesskit, from Ubuntu's apparmor package"},
	} {
		h := newBlank().with("userns-restrict", c.restrict).with("rootlesskit-profile", c.profile)
		got, _ := golden(t, goldenCase{name: "apparmor", host: func() *fakeHost { return h }, site: "vm"})
		if !strings.Contains(got, c.want) {
			t.Errorf("restrict %s, profile %s: want %q in\n%s", c.restrict, c.profile, c.want, got)
		}
		if strings.HasPrefix(c.want, "refused") && len(h.ran) > 0 {
			for _, r := range h.ran {
				if strings.Contains(r, "apt-get") && !strings.Contains(r, "read only") {
					t.Errorf("restrict %s, profile %s: ran %q", c.restrict, c.profile, r)
				}
			}
		}
	}
}

func TestAppArmorWithoutAShippedProfileSaysToLiftTheRestriction(t *testing.T) {
	h := newJammy().with("userns-restrict", "1")
	got, _ := golden(t, goldenCase{name: "apparmor", host: func() *fakeHost { return h }, site: "vm"})
	for _, want := range []string{
		// What lifting it costs, and the choice not to.
		"every user and program on this host may then create unprivileged user namespaces",
		"the kernel attack surface the restriction exists to reduce",
		"admit on another host instead",
		// Persistence a later file cannot undo, and a check that it held.
		"sysctl -w kernel.apparmor_restrict_unprivileged_userns=0",
		"kernel.apparmor_restrict_unprivileged_userns = 0 in /etc/sysctl.d/99-zz-paisans-userns.conf",
		"grep -rs apparmor_restrict_unprivileged_userns /etc/sysctl.conf /etc/sysctl.d /run/sysctl.d /usr/lib/sysctl.d",
		"after the next reboot",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("a host whose apparmor ships no rootlesskit profile is not told %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "apparmor_parser") || strings.Contains(got, "--reinstall") || strings.Contains(got, "that line") {
		t.Errorf("a host whose apparmor ships no rootlesskit profile is told:\n%s", got)
	}
}

// A host with no apparmor package is not told its package ships no
// profile: installing apparmor is the narrower fix, so it is told that,
// and the host-wide sysctl is not offered.
func TestAppArmorWithoutThePackageSaysToInstallIt(t *testing.T) {
	h := newBlank().with("userns-restrict", "1").with("rootlesskit-profile", "absent").with("rootlesskit-shipped", "no-package")
	got, _ := golden(t, goldenCase{name: "apparmor", host: func() *fakeHost { return h }, site: "vm"})
	for _, want := range []string{"the apparmor package is not installed", "apt-get install apparmor", "apparmor_parser -r /etc/apparmor.d/rootlesskit"} {
		if !strings.Contains(got, want) {
			t.Errorf("a host without apparmor is not told %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ships none") || strings.Contains(got, "sysctl -w") {
		t.Errorf("a host without apparmor is told:\n%s", got)
	}
}

// dockerCall is a docker command run anywhere in a shell line, its
// subcommand captured; docker -H, or docker as a word that is not a
// command (command -v docker, snap list docker), does not match.
var dockerCall = regexp.MustCompile("(?:^|[\\s;&|(`])docker\\s+([a-z][a-z-]*(?: [a-z]+)?)")

// Every system-Docker command admit runs, its probes' as well as its
// steps', names the system socket, so an owner's DOCKER_HOST or context
// never points it elsewhere. What runs as the deployment's user talks to
// the user's own daemon, and docker compose version reaches no daemon.
func TestSystemDockerIsNamedBySocket(t *testing.T) {
	if got := dockerCall.FindAllStringSubmatch("x=$(docker ps); if docker info; then docker -H unix:///var/run/docker.sock ps; fi; command -v docker >/dev/null", -1); len(got) != 2 || got[0][1] != "ps" || got[1][1] != "info" {
		t.Fatalf("dockerCall finds %q", got)
	}
	for _, c := range allCases {
		h := c.host()
		plan, _ := hostadmit.Build(h, input(t, c.site, c.flags))
		ran := h.ran
		if plan != nil {
			for _, s := range plan.Steps {
				ran = append(ran, s.Command)
			}
		}
		for _, command := range ran {
			if strings.Contains(command, "runuser -u ") {
				continue
			}
			for _, m := range dockerCall.FindAllStringSubmatch(command, -1) {
				if m[1] != "compose version" {
					t.Errorf("%s: runs docker %s without the system socket", c.name, m[1])
				}
			}
		}
	}
}

// titled is the index of plan's step titled title, -1 for none.
func titled(plan *hostadmit.Plan, title string) int {
	for i, s := range plan.Steps {
		if s.Title == title {
			return i
		}
	}
	return -1
}

// recordWritten is the admission record plan writes, decoded.
func recordWritten(t *testing.T, plan *hostadmit.Plan) admission.Record {
	t.Helper()
	for _, s := range plan.Steps {
		if s.File != nil && strings.HasPrefix(s.File.Path, "/etc/paisans/admitted.") {
			rec, err := admission.ParseRecord([]byte(s.File.Content), "f2a9")
			if err != nil {
				t.Fatal(err)
			}
			return rec
		}
	}
	t.Fatalf("no record written:\n%s", plan.Text())
	return admission.Record{}
}

// Admit installs Docker for the deployments' rootless daemons, and the
// system daemon its packages bring must never run: before it installs
// anything, admit records what it installs, so a run that stops later
// still knows the system Docker is its own; the install holds the daemon;
// and the next step disables Docker's units. The record says so.
func TestAdmitDisablesTheSystemDockerItInstalls(t *testing.T) {
	plan, err := hostadmit.Build(newBlank(), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	record, key := titled(plan, "record what admit installs"), titled(plan, "add docker apt key")
	install := slices.IndexFunc(plan.Steps, func(s hostadmit.Step) bool { return strings.HasPrefix(s.Title, "install packages: docker-ce") })
	disable := titled(plan, "disable the system Docker")
	if record < 0 || key < record || install < key || disable != install+1 {
		t.Fatalf("record %d, key %d, install %d, disable %d:\n%s", record, key, install, disable, plan.Text())
	}
	want := "installing docker-ce\ninstalling docker-ce-cli\ninstalling containerd.io\ninstalling docker-buildx-plugin\ninstalling docker-compose-plugin\ninstalling docker-ce-rootless-extras\ninstalling uidmap\ninstalling wireguard-tools\ninstalling git\nfile /etc/apt/keyrings/docker.asc\nfile /etc/apt/sources.list.d/docker.sources\ndir /etc/systemd/system/user@.service.d\n"
	if f := plan.Steps[record].File; f == nil || f.Path != "/etc/paisans/installed" || !strings.HasSuffix(f.Content, want) {
		t.Errorf("the installed file: %+v", plan.Steps[record].File)
	}
	if c := plan.Steps[install].Command; !strings.Contains(c, "/usr/sbin/policy-rc.d") {
		t.Errorf("the install does not hold the system daemon:\n%s", c)
	}
	if c := plan.Steps[disable].Command; !strings.HasPrefix(c, "systemctl disable --now docker.socket docker.service containerd.service && ") || !strings.Contains(c, "ip link show docker0") {
		t.Errorf("the disable step runs %q", c)
	}
	if !recordWritten(t, plan).SystemDockerDisabled {
		t.Error("the record does not say admit disabled the system Docker")
	}
}

// A Docker the owner had before admit is never touched: no hold, no
// disable, and the record says admit did not disable it.
func TestAdmitLeavesTheOwnersDocker(t *testing.T) {
	h := current().with("system-unit", "docker.service enabled active")
	plan, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Steps {
		if strings.Contains(s.Command, "policy-rc.d") || strings.Contains(s.Command, "systemctl disable") {
			t.Errorf("step %q touches the owner's Docker: %s", s.Title, s.Command)
		}
	}
	if recordWritten(t, plan).SystemDockerDisabled {
		t.Error("the record says admit disabled the owner's Docker")
	}
}

// installedByAdmit is h with the installed file saying admit installed
// Docker there.
func installedByAdmit(h *fakeHost) *fakeHost {
	content := "# Written by paisans site admit: what it installed on this host, which stays\n# when paisans site evict removes the last admitted deployment, and each\n# directory it created, which evict removes once empty.\npackage docker-ce\npackage docker-ce-rootless-extras\npackage uidmap\ndir /etc/systemd/system/user@.service.d\n"
	return h.file("/etc/paisans/installed", content).withFile("/etc/paisans/installed", content, "644", 0)
}

// A run that stopped between the install and the disable left the
// system Docker admit installed enabled and no record: the next run
// disables it. Once a record says admit disabled it, admit leaves it as it
// is, so an owner who enabled it since keeps it running.
func TestAdmitDisablesTheSystemDockerOnceOnly(t *testing.T) {
	stopped := installedByAdmit(current()).with("system-unit", "docker.service enabled active")
	plan, err := hostadmit.Build(stopped, input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	if titled(plan, "disable the system Docker") < 0 || titled(plan, "record what admit installs") >= 0 {
		t.Errorf("a run after one that stopped before the disable plans:\n%s", plan.Text())
	}
	if !recordWritten(t, plan).SystemDockerDisabled {
		t.Error("the record does not say admit disabled the system Docker")
	}

	in := input(t, "vm", hostadmit.Flags{})
	rec := admission.NewRecord(in.Request, in.Flags.Grants(), in.Digest, in.Now)
	rec.SystemDockerDisabled = true
	data, err := admission.EncodeRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	enabled := installedByAdmit(current()).with("system-unit", "docker.service enabled active").
		add("record", "/etc/paisans/admitted.f2a9.json").file("/etc/paisans/admitted.f2a9.json", string(data))
	plan, err = hostadmit.Build(enabled, in)
	if err != nil {
		t.Fatal(err)
	}
	if titled(plan, "disable the system Docker") >= 0 || !slices.Contains(plan.Present, "docker: the system daemon admit installed and disabled has been enabled since, and is left as it is") {
		t.Errorf("a re-run disables what the owner enabled:\n%s", plan.Text())
	}
}

// A run killed while it held the system Docker left admit's policy-rc.d
// in place; with nothing left to install, the next run puts the owner's
// back.
func TestAdmitPutsBackAPolicyALeftHoldLeft(t *testing.T) {
	plan, err := hostadmit.Build(installedByAdmit(current()).with("docker-hold", "policy"), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	i := titled(plan, "put back /usr/sbin/policy-rc.d")
	if i < 0 || !strings.Contains(plan.Steps[i].Command, "release_docker_hold") {
		t.Errorf("no step puts the policy back:\n%s", plan.Text())
	}
}

// A line in the installed file admit did not write is refused before
// anything changes: evict names its lines to the owner.
func TestAForeignInstalledLineIsRefused(t *testing.T) {
	h := current().file("/etc/paisans/installed", "package docker-ce\nrm -rf /\n").withFile("/etc/paisans/installed", "x", "644", 0)
	_, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	if err == nil || !strings.Contains(err.Error(), "/etc/paisans/installed holds rm -rf /") {
		t.Errorf("Build = %v", err)
	}
}

// containerd.io the host had is the owner's: admit installs Docker beside
// it, and holds and disables Docker's units alone, now and on a re-run
// that finds containerd running.
func TestAdmitLeavesAnOwnersContainerd(t *testing.T) {
	h := newBlank().with("system-unit", "containerd.service enabled active").
		answer("docker compose version", "compose absent\npkg containerd.io install ok installed\nkeyring absent\narch amd64\n")
	plan, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	i := titled(plan, "disable the system Docker")
	if i < 0 || !strings.HasPrefix(plan.Steps[i].Command, "systemctl disable --now docker.socket docker.service && ") {
		t.Fatalf("the disable step:\n%s", plan.Text())
	}
	for _, s := range plan.Steps {
		if s.File != nil && strings.Contains(s.File.Content, "package containerd.io") {
			t.Errorf("containerd.io is claimed as admit's:\n%s", s.File.Content)
		}
	}
	rerun := installedByAdmit(current()).with("system-unit", "containerd.service enabled active")
	plan, err = hostadmit.Build(rerun, input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	if titled(plan, "disable the system Docker") >= 0 {
		t.Errorf("a re-run disables the owner's containerd:\n%s", plan.Text())
	}
}

// /etc/paisans/installed decides whose the system Docker is, so it is
// read only when it is root's, mode 644, as admit writes it: one another
// user owns or can write is refused, by admit and by evict.
func TestAnInstalledListNotRootsIsRefused(t *testing.T) {
	h := installedByAdmit(current())
	content := h.files["/etc/paisans/installed"]
	for _, c := range []struct {
		mode string
		uid  int
	}{{"644", 1001}, {"664", 0}} {
		h := installedByAdmit(current()).withFile("/etc/paisans/installed", content, c.mode, c.uid)
		_, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
		if err == nil || !strings.Contains(err.Error(), "/etc/paisans/installed is not root's") {
			t.Errorf("mode %s, uid %d: Build = %v", c.mode, c.uid, err)
		}
		_, err = hostadmit.BuildEvict(h, hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
		if err == nil || !strings.Contains(err.Error(), "/etc/paisans/installed is not root's") {
			t.Errorf("mode %s, uid %d: BuildEvict = %v", c.mode, c.uid, err)
		}
	}
}

// A package is claimed as "installing" before the install and as admit's
// once the install has run. A claim whose install never ran does not
// outlive it: an "installing" line for a package the owner has since
// installed is dropped, unless the hold that install left says it was
// admit's own install that stopped; and a package line dpkg no longer
// shows installed is dropped.
func TestAStaleInstalledClaimIsDropped(t *testing.T) {
	header := "# Written by paisans site admit: what it installed on this host, which stays\n# when paisans site evict removes the last admitted deployment, and each\n# directory it created, which evict removes once empty.\n"
	claim := func(h *fakeHost, lines string) *fakeHost {
		return h.file("/etc/paisans/installed", header+lines).withFile("/etc/paisans/installed", header+lines, "644", 0)
	}
	// The owner installed Docker after an admit that stopped before its
	// install: the claim goes, and the owner's Docker is left.
	owner := claim(current().with("system-unit", "docker.service enabled active"), "installing docker-ce\npackage docker-ce-rootless-extras\npackage uidmap\npackage git\ndir /etc/systemd/system/user@.service.d\n").withPkg("git", "absent")
	plan, err := hostadmit.Build(owner, input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	i := titled(plan, "record what admit installs")
	if i < 0 || plan.Steps[i].File.Content != header+"package docker-ce-rootless-extras\npackage uidmap\ndir /etc/systemd/system/user@.service.d\n" {
		t.Errorf("the stale claims are kept:\n%s", plan.Text())
	}
	if titled(plan, "disable the system Docker") >= 0 || recordWritten(t, plan).SystemDockerDisabled {
		t.Errorf("the owner's Docker is taken for admit's:\n%s", plan.Text())
	}
	// An admit killed during its own install: the hold is left, so the
	// claim stands and the daemon is disabled.
	killed := claim(current().with("system-unit", "docker.service enabled active").with("docker-hold", "policy"), "package docker-ce-rootless-extras\npackage uidmap\ninstalling docker-ce\ndir /etc/systemd/system/user@.service.d\n")
	plan, err = hostadmit.Build(killed, input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	if titled(plan, "record what admit installs") >= 0 || titled(plan, "disable the system Docker") < 0 || titled(plan, "put back /usr/sbin/policy-rc.d") < 0 {
		t.Errorf("after an install that was killed:\n%s", plan.Text())
	}
}

// The install step turns its "installing" claims into admit's packages
// from its exit trap, before the hold goes, whatever apt did.
func TestTheInstallConfirmsItsClaims(t *testing.T) {
	plan, err := hostadmit.Build(newBlank(), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	record := titled(plan, "record what admit installs")
	if record < 0 || !strings.Contains(plan.Steps[record].File.Content, "\ninstalling docker-ce\n") || strings.Contains(plan.Steps[record].File.Content, "\npackage ") {
		t.Fatalf("the claim before the install:\n%s", plan.Steps[record].File.Content)
	}
	install := slices.IndexFunc(plan.Steps, func(s hostadmit.Step) bool { return strings.HasPrefix(s.Title, "install packages: docker-ce") })
	c := plan.Steps[install].Command
	if trap, apt := strings.Index(c, "confirm_claims || true; fi;"), strings.Index(c, "apt-get"); trap < 0 || apt < trap {
		t.Errorf("the install does not confirm its claims from its exit trap:\n%s", c)
	}
}

// Another deployment's record that cannot be read may say admit disabled
// the system Docker, so a running daemon the owner may have enabled is
// left, and the plan says why.
func TestAnUnreadableRecordKeepsTheSystemDockerAsItIs(t *testing.T) {
	h := withUnreadable(installedByAdmit(current()).with("system-unit", "docker.service enabled active"))
	plan, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	if titled(plan, "disable the system Docker") >= 0 || !slices.ContainsFunc(plan.Present, func(s string) bool { return strings.Contains(s, "/etc/paisans/admitted.0c0d.json cannot be read") }) {
		t.Errorf("beside an unreadable record:\n%s", plan.Text())
	}
}

// dockerDown is what the networks probe prints for a system Docker whose
// daemon does not answer: its socket is gone once its units are disabled.
const dockerDown = "paisans: dockerd does not answer: failed to connect to the docker API at unix:///var/run/docker.sock; check if the path is correct and if the daemon is running: dial unix /var/run/docker.sock: connect: no such file or directory\n"

// The system Docker admit installed and holds disabled has never run, so
// it has no networks, and the mesh's check reads none. A Docker of the
// owner's that does not answer still has its networks, which come back
// when it starts, so admit stops and says to start it, never a JSON
// error; so does one admit installed that the owner has enabled since,
// or started since, which left its network store behind.
func TestASystemDockerThatDoesNotAnswer(t *testing.T) {
	down := func(h *fakeHost) *fakeHost { return h.answer("unix:///var/run/docker.sock network ls", dockerDown) }
	plan, err := hostadmit.Build(down(installedByAdmit(current())), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatalf("admit's own held Docker: %v", err)
	}
	if !slices.Contains(plan.Present, "mesh: nothing on this host overlaps 10.44.0.0/24") {
		t.Errorf("the mesh's check:\n%s", plan.Text())
	}
	for name, h := range map[string]*fakeHost{
		"the owner's":            down(current()),
		"admit's, enabled since": down(installedByAdmit(current()).with("system-unit", "docker.service enabled inactive")),
		"admit's, run since":     down(installedByAdmit(current()).with("docker-networks", "present")),
	} {
		_, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
		if err == nil || !strings.Contains(err.Error(), "does not answer") || !strings.Contains(err.Error(), "Start it") || strings.Contains(err.Error(), "invalid character") {
			t.Errorf("%s: Build = %v", name, err)
		}
	}
}

// runInstall runs plan's install step for real, its paths moved into a
// directory of the test's, with an apt-get that makes dpkg show each
// package of fails installed and then fails, as a package that does not
// configure stops apt; dpkg-query answers from that. It returns
// /etc/paisans/installed as the step left it, which starts as claimed,
// and the moved /usr/sbin.
func runInstall(t *testing.T, plan *hostadmit.Plan, claimed string, fails ...string) (string, string) {
	t.Helper()
	install := slices.IndexFunc(plan.Steps, func(s hostadmit.Step) bool { return strings.HasPrefix(s.Title, "install packages: ") })
	if install < 0 {
		t.Fatalf("no install step:\n%s", plan.Text())
	}
	dir := t.TempDir()
	bin, sbin, list, dpkg := filepath.Join(dir, "bin"), filepath.Join(dir, "sbin"), filepath.Join(dir, "installed"), filepath.Join(dir, "dpkg")
	for _, d := range []string{bin, sbin} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	scripts := map[string]string{
		"apt-get": "#!/bin/sh\ncase \"$*\" in *' install '*) printf '%s\\n' " + strings.Join(fails, " ") + " >> " + dpkg + "; exit 100 ;; esac\nexit 0\n",
		// A package as name, or name:status for one dpkg left in another
		// state than installed.
		"dpkg-query": "#!/bin/sh\nfor p; do :; done\nline=$(grep -E \"^$p(:|\\$)\" " + dpkg + " 2>/dev/null | head -n 1)\n[ -n \"$line\" ] || exit 1\ncase $line in *:*) printf 'install ok %s' \"${line#*:}\" ;; *) printf 'install ok installed' ;; esac\n",
		"systemctl":  "#!/bin/sh\nexit 0\n",
		"flock":      "#!/bin/sh\nexit 0\n",
	}
	for name, s := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(list, []byte(claimed), 0o644); err != nil {
		t.Fatal(err)
	}
	command := strings.NewReplacer("/usr/sbin/", sbin+"/", "/etc/paisans/installed", list, "/run/paisans-docker-hold.lock", filepath.Join(dir, "lock")).Replace(plan.Steps[install].Command)
	sh := "sh"
	if p, err := exec.LookPath("dash"); err == nil {
		sh = p
	}
	cmd := exec.Command(sh, "-c", command)
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("the install did not fail: %s", out)
	}
	got, err := os.ReadFile(list)
	if err != nil {
		t.Fatal(err)
	}
	return string(got), sbin
}

// An install that fails after apt installed docker-ce still records it as
// admit's: the claims are confirmed from the exit trap, before the hold
// goes, for each package dpkg shows installed, so the next run does not
// take admit's Docker for the owner's. The same holds for an install with
// nothing to hold.
func TestAFailedInstallConfirmsWhatItInstalled(t *testing.T) {
	plan, err := hostadmit.Build(newBlank(), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	claimed := plan.Steps[titled(plan, "record what admit installs")].File.Content
	got, sbin := runInstall(t, plan, claimed, "docker-ce", "containerd.io")
	for _, want := range []string{"\npackage docker-ce\n", "\npackage containerd.io\n", "\ninstalling docker-ce-cli\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("after a failed install the list lacks %q:\n%s", want, got)
		}
	}
	if _, err := os.Lstat(filepath.Join(sbin, "policy-rc.d")); !os.IsNotExist(err) {
		t.Errorf("the hold is left: %v", err)
	}

	plan, err = hostadmit.Build(ownersDocker(), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	claimed = plan.Steps[titled(plan, "record what admit installs")].File.Content
	got, _ = runInstall(t, plan, claimed, "uidmap")
	if !strings.Contains(got, "\npackage uidmap\n") || !strings.Contains(got, "\ninstalling docker-ce-rootless-extras\n") {
		t.Errorf("after a failed install with nothing held:\n%s", got)
	}
}

// A hold that stopped before admit's policy was in place (a temporary
// file, or the owner's copy) means apt never ran under it: it is put back,
// but an "installing" claim does not stand on it, so a Docker the owner
// installed since is not taken for admit's, and no claim is confirmed.
func TestALeftoverBeforeThePolicyKeepsNoClaim(t *testing.T) {
	header := "# Written by paisans site admit: what it installed on this host, which stays\n# when paisans site evict removes the last admitted deployment, and each\n# directory it created, which evict removes once empty.\n"
	lines := "package docker-ce-rootless-extras\npackage uidmap\ninstalling docker-ce\ndir /etc/systemd/system/user@.service.d\n"
	h := current().with("system-unit", "docker.service enabled active").with("docker-hold", "leftover").
		file("/etc/paisans/installed", header+lines).withFile("/etc/paisans/installed", header+lines, "644", 0)
	plan, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	put := titled(plan, "put back /usr/sbin/policy-rc.d")
	if put < 0 || strings.Contains(plan.Steps[put].Command, "confirm_claims") {
		t.Errorf("the leftover is not put back, or its claims are confirmed:\n%s", plan.Text())
	}
	if titled(plan, "disable the system Docker") >= 0 || recordWritten(t, plan).SystemDockerDisabled {
		t.Errorf("the owner's Docker is taken for admit's:\n%s", plan.Text())
	}
}

// A package whose files dpkg has unpacked is admit's even when apt failed
// before it was configured: its script may have enabled its units, so the
// claim stands for the next run to disable them.
func TestAFailedInstallConfirmsAPackageLeftHalfConfigured(t *testing.T) {
	plan, err := hostadmit.Build(newBlank(), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	claimed := plan.Steps[titled(plan, "record what admit installs")].File.Content
	got, _ := runInstall(t, plan, claimed, "docker-ce:half-configured", "containerd.io:config-files")
	if !strings.Contains(got, "\npackage docker-ce\n") || !strings.Contains(got, "\ninstalling containerd.io\n") {
		t.Errorf("after an install left docker-ce half-configured:\n%s", got)
	}
}

// The install takes the hold's lock before it sets its exit trap, so a
// run refused at the lock writes nothing, and confirm_claims writes the
// list through a temporary file of its own; a signal ends the install
// through its exit trap, so its claims are confirmed then too.
func TestTheInstallLocksBeforeItConfirms(t *testing.T) {
	for name, h := range map[string]*fakeHost{"held": newBlank(), "nothing held": ownersDocker()} {
		plan, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
		if err != nil {
			t.Fatal(err)
		}
		install := slices.IndexFunc(plan.Steps, func(s hostadmit.Step) bool { return strings.HasPrefix(s.Title, "install packages: ") })
		c := plan.Steps[install].Command
		lock, trap, signals, apt := strings.Index(c, "flock -n 8"), strings.Index(c, "trap confirm_claims EXIT"), strings.Index(c, "trap 'exit 1' HUP INT TERM"), strings.Index(c, "apt-get")
		if lock < 0 || trap < lock || signals < 0 || apt < signals {
			t.Errorf("%s: lock %d, trap %d, signals %d, apt %d:\n%s", name, lock, trap, signals, apt, c)
		}
		if !strings.Contains(c, "mktemp \"$f.XXXXXX\"") || strings.Contains(c, "$f.paisans-tmp") {
			t.Errorf("%s: confirm_claims writes through a shared name:\n%s", name, c)
		}
	}
}

// Putting back a hold admit's own policy left confirms that install's
// claims before it releases the hold, so a run that dies between the two
// still finds the policy that says the claims stand.
func TestPuttingBackConfirmsBeforeItReleases(t *testing.T) {
	plan, err := hostadmit.Build(installedByAdmit(current()).with("docker-hold", "policy"), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	c := plan.Steps[titled(plan, "put back /usr/sbin/policy-rc.d")].Command
	lock, confirm, release := strings.Index(c, "flock -n 8"), strings.Index(c, "\nconfirm_claims\n"), strings.LastIndex(c, "\nrelease_docker_hold\n")
	if lock < 0 || confirm < lock || release < confirm {
		t.Errorf("lock %d, confirm %d, release %d:\n%s", lock, confirm, release, c)
	}
}

// ownersDocker is a blank host with the owner's Docker 29.9.1 and no
// rootless extras: admit installs them and uidmap, holding nothing.
func ownersDocker() *fakeHost {
	return newBlank().with("engine", "29.9.1").with("system-socket", "present").withPkg("docker-ce", "installed").
		answer("docker compose version", "compose present\npkg docker-ce install ok installed\npkg ufw install ok installed\npkg git install ok installed\npkg wireguard-tools install ok installed\npkg nftables install ok installed\ndockerversion 5:29.9.1-1~ubuntu.24.04~noble\nextras 5:29.9.1-1~ubuntu.24.04~noble\nkeyring present\narch amd64\n")
}

// A package of admit's that dpkg left unpacked but not configured is on
// disk, its units perhaps enabled: the list keeps it, and the system
// Docker is still admit's to disable.
func TestAnInstalledListKeepsAPackageOnDisk(t *testing.T) {
	h := installedByAdmit(current()).with("system-unit", "docker.service enabled active").
		withPkg("docker-ce", "absent").add("ondisk", "docker-ce present")
	plan, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	if titled(plan, "record what admit installs") >= 0 || titled(plan, "disable the system Docker") < 0 {
		t.Errorf("a docker-ce left half-configured is dropped:\n%s", plan.Text())
	}
}
