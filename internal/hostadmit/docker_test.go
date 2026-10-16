package hostadmit_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

var dockerCases = addCases(
	goldenCase{name: "docker-blank", host: newBlank, site: "vm"},
	goldenCase{name: "docker-old-refused", site: "vm", host: func() *fakeHost {
		return newBlank().with("engine", "27.5.1").withPkg("docker-ce", "installed").with("system-socket", "present")
	}},
	goldenCase{name: "docker-current", site: "vm", host: func() *fakeHost {
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
	goldenCase{name: "apparmor-unrestricted", site: "vm", host: func() *fakeHost {
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
func TestAppArmorGuardsEachCase(t *testing.T) {
	for _, c := range []struct {
		restrict, profile, want string
	}{
		{"0", "absent", "present: apparmor: unprivileged user namespaces are not restricted"},
		{"1", "loaded", "present: apparmor: rootlesskit's profile is loaded"},
		{"1", "absent", "refused: apparmor: AppArmor restricts unprivileged user namespaces here and no rootlesskit profile is loaded"},
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
	content := "# Written by paisans host admit: what it installed on this host, which stays\n# when paisans host evict removes the last admitted deployment, and each\n# directory it created, which evict removes once empty.\npackage docker-ce\npackage docker-ce-rootless-extras\npackage uidmap\ndir /etc/systemd/system/user@.service.d\n"
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
	plan, err := hostadmit.Build(installedByAdmit(current()).with("docker-hold", "held"), input(t, "vm", hostadmit.Flags{}))
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
	header := "# Written by paisans host admit: what it installed on this host, which stays\n# when paisans host evict removes the last admitted deployment, and each\n# directory it created, which evict removes once empty.\n"
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
	killed := claim(current().with("system-unit", "docker.service enabled active").with("docker-hold", "held"), "package docker-ce-rootless-extras\npackage uidmap\ninstalling docker-ce\ndir /etc/systemd/system/user@.service.d\n")
	plan, err = hostadmit.Build(killed, input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	if titled(plan, "record what admit installs") >= 0 || titled(plan, "disable the system Docker") < 0 || titled(plan, "put back /usr/sbin/policy-rc.d") < 0 {
		t.Errorf("after an install that was killed:\n%s", plan.Text())
	}
}

// The install step turns its "installing" claims into admit's packages
// once apt has installed them, and only then.
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
	if apt, sed := strings.LastIndex(c, "apt-get"), strings.Index(c, "sed -i 's/^installing /package /' /etc/paisans/installed"); sed < apt {
		t.Errorf("the install does not confirm its claims after apt:\n%s", c)
	}
}
