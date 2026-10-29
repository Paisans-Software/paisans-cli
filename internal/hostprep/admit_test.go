package hostprep_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

// Admit opens the SSH port, the mesh's port when one is admitted, the
// mesh interface, and 80 and 443 with the public ports, 443 for UDP too,
// which HTTP/3 runs on; no br-+ rule, since
// a rootless container reaches the mesh address through the user's own
// process, not a host bridge.
func TestAdmitRules(t *testing.T) {
	d := deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}
	var got []string
	for _, r := range hostprep.AdmitRules(d, 2222, 51820, true, true) {
		got = append(got, r.String()+" ("+r.Why+")")
	}
	want := []string{"2222/tcp (ssh, the bootstrap route)", "51820/udp (WireGuard, the mesh)", "all inbound on psns-f2a9 (the mesh: etcd, Patroni, Garage, HAProxy)", "routed from psns-f2a9 back out psns-f2a9 (the mesh, relayed between sites that cannot dial each other)", "80/tcp (public ports, HTTP)", "443/tcp (public ports, HTTPS)", "443/udp (public ports, HTTP/3)"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("AdmitRules =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if rules := hostprep.AdmitRules(d, 22, 0, false, false); len(rules) != 2 || !rules[0].SSH {
		t.Errorf("no port, no public ports, no relay: %+v", rules)
	}
}

// With Docker absent, admit installs Docker from Docker's repository with
// the rootless extras; with it present, it installs the extras and the
// rest without upgrading anything already installed, so the owner's
// Docker is never upgraded by admit. ufw is not among them: admit works only
// where the owner has installed and enabled it.
func TestAdmitPackages(t *testing.T) {
	profile, err := hostprep.Select(hostprep.ParseOSRelease(ubuntu2404))
	if err != nil {
		t.Fatal(err)
	}
	blank := freshHost()
	s, _, err := profile.AdmitPackages(blank, hostprep.ParseOSRelease(ubuntu2404), false)
	if err != nil {
		t.Fatal(err)
	}
	all := ""
	for _, st := range s.Steps {
		all += st.Title + "\n" + st.Command + "\n"
	}
	for _, want := range []string{"add docker apt key", "add docker apt repo", "docker-ce ", "docker-ce-rootless-extras", "uidmap", "nftables", "wireguard-tools", "git"} {
		if !strings.Contains(all, want) {
			t.Errorf("a blank host's admit packages lack %q:\n%s", want, all)
		}
	}
	if strings.Contains(all, "ufw") {
		t.Errorf("admit installs ufw:\n%s", all)
	}
	if strings.Contains(all, "--no-upgrade") {
		t.Errorf("a blank host's install is --no-upgrade:\n%s", all)
	}

	present := freshHost()
	present.responses[probePackages] = "compose present\npkg docker-ce install ok installed\npkg wireguard-tools absent\npkg ufw install ok installed\npkg git install ok installed\ndockerversion 5:29.9.0-1~ubuntu.24.04~noble\nextras 5:29.9.1-1~ubuntu.24.04~noble\nextras 5:29.9.0-1~ubuntu.24.04~noble\nkeyring present\narch amd64\n"
	s, _, err = profile.AdmitPackages(present, hostprep.ParseOSRelease(ubuntu2404), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Steps) != 1 || !strings.Contains(s.Steps[0].Command, "--no-upgrade") || strings.Contains(s.Steps[0].Command, " docker-ce ") || !strings.Contains(s.Steps[0].Command, "docker-ce-rootless-extras") {
		t.Errorf("with Docker present, admit plans %+v", s.Steps)
	}
	if !strings.Contains(s.Steps[0].Command, " docker-ce-rootless-extras=5:29.9.0-1~ubuntu.24.04~noble") {
		t.Errorf("the extras are not pinned to the installed docker-ce: %s", s.Steps[0].Command)
	}
}

// With Docker present, the extras are installed only at the version of the
// installed docker-ce, since apt would otherwise upgrade the owner's Docker
// to match them; where the repository has no such version, admit refuses,
// naming both versions.
func TestAdmitPackagesRefusesExtrasThatWouldUpgradeDocker(t *testing.T) {
	profile, err := hostprep.Select(hostprep.ParseOSRelease(ubuntu2404))
	if err != nil {
		t.Fatal(err)
	}
	h := freshHost()
	h.responses[probePackages] = "compose present\npkg docker-ce install ok installed\ndockerversion 5:29.9.0-1~ubuntu.24.04~noble\nextras 5:29.9.1-1~ubuntu.24.04~noble\nkeyring present\narch amd64\n"
	_, _, err = profile.AdmitPackages(h, hostprep.ParseOSRelease(ubuntu2404), true)
	if err == nil || !strings.Contains(err.Error(), "5:29.9.0-1~ubuntu.24.04~noble") || !strings.Contains(err.Error(), "5:29.9.1-1~ubuntu.24.04~noble") {
		t.Errorf("want a refusal naming both versions, got %v", err)
	}
}

// Docker's apt key moves into place only once gpg has shown it to be
// Docker's release key, by fingerprint; anything else is removed.
func TestTheDockerKeyIsCheckedByFingerprint(t *testing.T) {
	profile, err := hostprep.Select(hostprep.ParseOSRelease(ubuntu2404))
	if err != nil {
		t.Fatal(err)
	}
	s, _, err := profile.AdmitPackages(freshHost(), hostprep.ParseOSRelease(ubuntu2404), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range s.Steps {
		if st.Title != "add docker apt key" {
			continue
		}
		// A secret key's primary record is sec, not pub: Docker's key with
		// another's secret key beside it holds two primary keys.
		if !strings.Contains(st.Command, `$1 == "pub" || $1 == "sec" { n++; take = 1; next }`) || !strings.Contains(st.Command, `END { if (n == 1) print fpr }`) {
			t.Errorf("the check does not require exactly one primary key, public or secret:\n%s", st.Command)
		}
		check := strings.Index(st.Command, `if [ "$fpr" != 9DC858229FC7DD38854AE2D88D81803C0EBFCD88 ]`)
		move := strings.Index(st.Command, "mv /etc/apt/keyrings/docker.asc.paisans-tmp /etc/apt/keyrings/docker.asc")
		if check < 0 || move < check || !strings.Contains(st.Command, "gpg --show-keys --with-colons") || !strings.Contains(st.Command, "install -y ca-certificates curl gnupg") {
			t.Errorf("the key is not checked before it moves into place:\n%s", st.Command)
		}
		return
	}
	t.Fatal("no key step on a fresh host")
}

// On a host without Docker, admit refuses what site prepare refuses, so
// installing Docker never removes Ubuntu's containerd or runc; with no
// Docker to run compose, the reason given is the removal.
func TestAdmitPackagesRefusesConflicts(t *testing.T) {
	profile, err := hostprep.Select(hostprep.ParseOSRelease(ubuntu2404))
	if err != nil {
		t.Fatal(err)
	}
	h := freshHost()
	h.responses[probePackages] = "compose absent\npkg containerd install ok installed\npkg runc install ok installed\nkeyring absent\narch amd64\n"
	_, _, err = profile.AdmitPackages(h, hostprep.ParseOSRelease(ubuntu2404), false)
	if err == nil || !strings.Contains(err.Error(), "containerd, runc") || !strings.Contains(err.Error(), "Nothing was changed") ||
		!strings.Contains(err.Error(), "installing Docker's packages would remove them") || strings.Contains(err.Error(), "docker compose") {
		t.Errorf("want a refusal naming containerd and runc, and why, got %v", err)
	}
}

// The extras are pinned only to a docker-ce that dpkg holds installed: a
// removed one whose configuration was kept still has a version, and
// pinning to it would install docker-ce over the owner's Docker. With no
// extras offered at all, Docker's repository is what is missing.
func TestAdmitPackagesPinsOnlyAnInstalledDockerCE(t *testing.T) {
	profile, err := hostprep.Select(hostprep.ParseOSRelease(ubuntu2404))
	if err != nil {
		t.Fatal(err)
	}
	h := freshHost()
	h.responses[probePackages] = "compose present\npkg docker-ce deinstall ok config-files\ndockerversion 5:29.9.0-1~ubuntu.24.04~noble\nextras 5:29.9.0-1~ubuntu.24.04~noble\nkeyring present\narch amd64\n"
	_, _, err = profile.AdmitPackages(h, hostprep.ParseOSRelease(ubuntu2404), true)
	if err == nil || !strings.Contains(err.Error(), "docker-ce is not installed as a Debian package") {
		t.Errorf("a removed docker-ce: %v", err)
	}
	h.responses[probePackages] = "compose present\npkg docker-ce install ok installed\ndockerversion 5:29.9.0-1~ubuntu.24.04~noble\nkeyring absent\narch amd64\n"
	_, _, err = profile.AdmitPackages(h, hostprep.ParseOSRelease(ubuntu2404), true)
	if err == nil || !strings.Contains(err.Error(), "offer no docker-ce-rootless-extras") || !strings.Contains(err.Error(), "Docker's apt repository") || strings.Contains(err.Error(), "at none") {
		t.Errorf("no extras offered: %v", err)
	}
}

// A sidecar as site prepare writes one reads back and writes out the same,
// so admit's sidecar and site prepare's are one format.
func TestOwnedKeysRoundTrip(t *testing.T) {
	sidecar := "# Keys site admit and site prepare added to this deployment's user's authorized_keys: <mark> <fingerprint> <name>. site prepare removes a line only when it is marked added here and ssh.keys no longer lists it.\n" +
		"added SHA256:4b2Q4Jx3ZQf1XU1Vb1k0f0sYz8m9n7bq3kq0iYkQWd0 alice\n" +
		"added SHA256:9xQ1m0bK3iYp2nWm7c8d5e4f3g2h1j0k9l8m7n6o5p4 bob\n"
	keys := hostprep.ParseOwnedKeys(sidecar)
	if len(keys) != 2 || keys[0] != (hostprep.OwnedKey{Mark: "added", Fingerprint: "SHA256:4b2Q4Jx3ZQf1XU1Vb1k0f0sYz8m9n7bq3kq0iYkQWd0", Name: "alice"}) {
		t.Fatalf("ParseOwnedKeys = %+v", keys)
	}
	if got := hostprep.OwnedKeysFile(keys); got != sidecar {
		t.Errorf("OwnedKeysFile =\n%s\nwant\n%s", got, sidecar)
	}
	if got := hostprep.OwnedKeysFile([]hostprep.OwnedKey{keys[1], keys[0]}); got != sidecar {
		t.Errorf("OwnedKeysFile does not sort by fingerprint:\n%s", got)
	}
}

// A sidecar line with any mark but added is no claim, so its key's line is
// never removed.
func TestOnlyAnAddedSidecarLineIsAClaim(t *testing.T) {
	keys := hostprep.ParseOwnedKeys("added SHA256:4b2Q4Jx3ZQf1XU1Vb1k0f0sYz8m9n7bq3kq0iYkQWd0 alice\n" +
		"shared SHA256:9xQ1m0bK3iYp2nWm7c8d5e4f3g2h1j0k9l8m7n6o5p4 bob\n")
	if len(keys) != 1 || keys[0].Name != "alice" {
		t.Errorf("ParseOwnedKeys = %+v", keys)
	}
}

// Installing Docker where it is absent holds its system daemon: the
// install's script starts by putting admit's policy-rc.d in place, and
// what the plan adds is named, the packages without their pins and
// Docker's key and source. With Docker present, nothing is held.
func TestAdmitPackagesHoldsTheSystemDocker(t *testing.T) {
	profile, err := hostprep.Select(hostprep.ParseOSRelease(ubuntu2404))
	if err != nil {
		t.Fatal(err)
	}
	s, added, err := profile.AdmitPackages(freshHost(), hostprep.ParseOSRelease(ubuntu2404), false)
	if err != nil {
		t.Fatal(err)
	}
	install := s.Steps[len(s.Steps)-1]
	hold, apt := strings.Index(install.Command, "mv -f /usr/sbin/.policy-rc.d.paisans-tmp /usr/sbin/policy-rc.d"), strings.Index(install.Command, "apt-get -o DPkg::Lock::Timeout=300 -o Dpkg::Options::=--force-confold install")
	if hold < 0 || apt < hold || !strings.Contains(install.Command, "release_docker_hold; exit \"$paisans_status\"' EXIT") {
		t.Errorf("the install does not hold Docker's daemons first:\n%s", install.Command)
	}
	if got := strings.Join(added.Packages, " "); got != "docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin docker-ce-rootless-extras uidmap nftables wireguard-tools git" {
		t.Errorf("added packages %q", got)
	}
	if got := strings.Join(added.Files, " "); got != "/etc/apt/keyrings/docker.asc /etc/apt/sources.list.d/docker.sources" {
		t.Errorf("added files %q", got)
	}

	present := freshHost()
	present.responses[probePackages] = "compose present\npkg docker-ce install ok installed\npkg wireguard-tools absent\npkg ufw install ok installed\npkg git install ok installed\ndockerversion 5:29.9.0-1~ubuntu.24.04~noble\nextras 5:29.9.0-1~ubuntu.24.04~noble\nkeyring present\narch amd64\n"
	s, added, err = profile.AdmitPackages(present, hostprep.ParseOSRelease(ubuntu2404), true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s.Steps[0].Command, "policy-rc.d") {
		t.Errorf("the owner's Docker is held:\n%s", s.Steps[0].Command)
	}
	if got := strings.Join(added.Packages, " "); got != "docker-ce-rootless-extras uidmap nftables wireguard-tools" || len(added.Files) != 0 {
		t.Errorf("added %+v", added)
	}
}

// The hold, run for real with /usr/sbin moved into a directory of the
// test's: while it holds, Docker's and containerd's units are forbidden
// (101) and every other is asked of the owner's own policy-rc.d; after,
// the owner's is back as it was, or there is none when the owner had none;
// and one an interrupted run left is put right before anything else.
func TestTheDockerHoldPutsBackTheOwnersPolicy(t *testing.T) {
	profile, err := hostprep.Select(hostprep.ParseOSRelease(ubuntu2404))
	if err != nil {
		t.Fatal(err)
	}
	s, _, err := profile.AdmitPackages(freshHost(), hostprep.ParseOSRelease(ubuntu2404), false)
	if err != nil {
		t.Fatal(err)
	}
	command := s.Steps[len(s.Steps)-1].Command
	hold := command[:strings.Index(command, "export DEBIAN_FRONTEND")]
	if !strings.Contains(hold, "\n  cp -pP /usr/sbin/policy-rc.d /usr/sbin/.policy-rc.d.paisans-owner-tmp\n  mv -f /usr/sbin/.policy-rc.d.paisans-owner-tmp /usr/sbin/policy-rc.d.paisans-owner\n") {
		t.Errorf("the owner's policy is not copied through a temporary name:\n%s", hold)
	}
	release, err := profile.ReleaseDockerHold("")
	if err != nil {
		t.Fatal(err)
	}
	const owner = "#!/bin/sh\necho owner \"$@\" >> \"$LOG\"\nexit 0\n"
	// asks is what the apt step stands in for: the policy asked for each
	// unit as invoke-rc.d and deb-systemd-invoke ask it.
	const asks = "for u in docker.service docker.socket containerd.service docker nginx.service; do r=0; /usr/sbin/policy-rc.d \"$u\" start || r=$?; echo \"$u $r\" >> \"$LOG\"; done\n"
	held := []string{"docker.service 101", "docker.socket 101", "containerd.service 101", "docker 101", "owner nginx.service start", "nginx.service 0"}
	for _, c := range []struct {
		name, before, leftover string
		// left is what a run that stopped early left in sbin, the
		// owner's policy-rc.d at before.
		left func(sbin string)
		want []string
	}{
		{name: "no policy", want: []string{"docker.service 101", "docker.socket 101", "containerd.service 101", "docker 101", "nginx.service 0"}},
		{name: "the owner's", before: owner, want: held},
		{name: "left by an interrupted run", before: owner, leftover: "hold", want: held},
		{name: "the owner's renamed, none in place", before: owner, want: held, left: func(sbin string) {
			if err := os.Rename(filepath.Join(sbin, "policy-rc.d"), filepath.Join(sbin, "policy-rc.d.paisans-owner")); err != nil {
				panic(err)
			}
		}},
		{name: "a copy cut short", before: owner, want: held, left: func(sbin string) {
			if err := os.WriteFile(filepath.Join(sbin, ".policy-rc.d.paisans-owner-tmp"), []byte("#!/bin/sh\nec"), 0o755); err != nil {
				panic(err)
			}
		}},
		{name: "a copy beside the owner's", before: owner, want: held, left: func(sbin string) {
			if err := os.WriteFile(filepath.Join(sbin, "policy-rc.d.paisans-owner"), []byte(owner), 0o755); err != nil {
				panic(err)
			}
			if err := os.WriteFile(filepath.Join(sbin, ".policy-rc.d.paisans-tmp"), []byte("half"), 0o755); err != nil {
				panic(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			sbin := filepath.Join(dir, "sbin")
			if err := os.Mkdir(sbin, 0o755); err != nil {
				t.Fatal(err)
			}
			policy := filepath.Join(sbin, "policy-rc.d")
			if c.before != "" {
				if err := os.WriteFile(policy, []byte(c.before), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			at := func(script string) string {
				return strings.NewReplacer("/usr/sbin/", sbin+"/", "/run/paisans-docker-hold.lock", filepath.Join(dir, "lock")).Replace(script)
			}
			run := func(script string) {
				t.Helper()
				cmd := exec.Command("sh", "-c", script)
				cmd.Env = append(os.Environ(), "LOG="+filepath.Join(dir, "log"), "PATH="+fakeFlock(t, 0)+":"+os.Getenv("PATH"))
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("%v: %s", err, out)
				}
			}
			if c.leftover != "" {
				// A run killed while it held: the owner's aside, admit's in place.
				run(at(hold) + "trap - EXIT\n")
			}
			if c.left != nil {
				c.left(sbin)
			}
			run(at(hold + asks))
			log, _ := os.ReadFile(filepath.Join(dir, "log"))
			if got := strings.Split(strings.TrimSpace(string(log)), "\n"); strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Errorf("while held:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(c.want, "\n"))
			}
			after, err := os.ReadFile(policy)
			switch {
			case c.before == "" && !os.IsNotExist(err):
				t.Errorf("admit's policy-rc.d is left: %q, %v", after, err)
			case c.before != "" && string(after) != c.before:
				t.Errorf("the owner's policy-rc.d is %q, %v", after, err)
			}
			for _, f := range []string{policy + ".paisans-owner", filepath.Join(sbin, ".policy-rc.d.paisans-tmp"), filepath.Join(sbin, ".policy-rc.d.paisans-owner-tmp")} {
				if _, err := os.Lstat(f); !os.IsNotExist(err) {
					t.Errorf("%s is left: %v", f, err)
				}
			}
			if c.leftover != "" {
				// ReleaseDockerHold alone puts it right too.
				run(at(hold) + "trap - EXIT\n")
				run(at(release))
				if after, err := os.ReadFile(policy); string(after) != c.before {
					t.Errorf("after ReleaseDockerHold the policy is %q, %v", after, err)
				}
			}
		})
	}
}

// A copy beside a policy-rc.d that differs from it is not admit's to
// choose between: the hold refuses, and leaves both as they are.
func TestTheDockerHoldRefusesACopyThatDiffers(t *testing.T) {
	profile, err := hostprep.Select(hostprep.ParseOSRelease(ubuntu2404))
	if err != nil {
		t.Fatal(err)
	}
	s, _, err := profile.AdmitPackages(freshHost(), hostprep.ParseOSRelease(ubuntu2404), false)
	if err != nil {
		t.Fatal(err)
	}
	command := s.Steps[len(s.Steps)-1].Command
	hold := command[:strings.Index(command, "export DEBIAN_FRONTEND")]
	sbin := t.TempDir()
	for name, content := range map[string]string{"policy-rc.d": "#!/bin/sh\nexit 0\n", "policy-rc.d.paisans-owner": "#!/bin/sh\nexit 101\n"} {
		if err := os.WriteFile(filepath.Join(sbin, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "-c", strings.NewReplacer("/usr/sbin/", sbin+"/", "/run/paisans-docker-hold.lock", filepath.Join(sbin, "lock")).Replace(hold))
	cmd.Env = append(os.Environ(), "PATH="+fakeFlock(t, 0)+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "differs from it") {
		t.Errorf("the hold went on: %v: %s", err, out)
	}
	for name, content := range map[string]string{"policy-rc.d": "#!/bin/sh\nexit 0\n", "policy-rc.d.paisans-owner": "#!/bin/sh\nexit 101\n"} {
		if got, _ := os.ReadFile(filepath.Join(sbin, name)); string(got) != content {
			t.Errorf("%s is now %q", name, got)
		}
	}
}

// containerd.io already on the host is the owner's: the hold forbids only
// Docker's units, and containerd.io is not among what the install adds.
// apt may still upgrade it to what docker-ce depends on, and the plan says
// so.
func TestTheDockerHoldLeavesAnOwnersContainerd(t *testing.T) {
	profile, err := hostprep.Select(hostprep.ParseOSRelease(ubuntu2404))
	if err != nil {
		t.Fatal(err)
	}
	h := freshHost()
	h.responses[probePackages] = "compose absent\npkg containerd.io install ok installed\nkeyring present\narch amd64\n"
	s, added, err := profile.AdmitPackages(h, hostprep.ParseOSRelease(ubuntu2404), false)
	if err != nil {
		t.Fatal(err)
	}
	command := s.Steps[len(s.Steps)-1].Command
	if slices.Contains(added.Packages, "containerd.io") {
		t.Errorf("the owner's containerd.io is claimed: %v", added.Packages)
	}
	if d := s.Steps[len(s.Steps)-1].Describe; !strings.Contains(d, "your containerd.io may be upgraded to the version docker-ce needs, which restarts it") {
		t.Errorf("the install does not name the upgrade of the owner's containerd.io: %s", d)
	}
	if !strings.Contains(command, "  case $a in docker|docker.service|docker.socket) exit 101 ;; esac\n") {
		t.Errorf("the hold does not forbid Docker's units alone:\n%s", command)
	}
	if units := hostprep.SystemUnits(added.Packages); strings.Join(units, " ") != "docker.socket docker.service" {
		t.Errorf("SystemUnits = %q", units)
	}
	if units := hostprep.SystemUnits([]string{"docker-ce", "containerd.io"}); strings.Join(units, " ") != "docker.socket docker.service containerd.service" {
		t.Errorf("SystemUnits = %q", units)
	}
}

// fakeFlock is a directory holding a flock that exits with code, standing
// in for util-linux's, which macOS lacks: 0 takes the lock, 1 finds it
// held.
func fakeFlock(t *testing.T, code int) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "flock"), []byte(fmt.Sprintf("#!/bin/sh\nexit %d\n", code)), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A hold is one run's alone: while another holds its lock, the hold and
// putting a hold back both refuse, naming the run, and change nothing, so
// no evict or second admit releases a live hold under a running install.
func TestTheDockerHoldIsLocked(t *testing.T) {
	profile, err := hostprep.Select(hostprep.ParseOSRelease(ubuntu2404))
	if err != nil {
		t.Fatal(err)
	}
	s, _, err := profile.AdmitPackages(freshHost(), hostprep.ParseOSRelease(ubuntu2404), false)
	if err != nil {
		t.Fatal(err)
	}
	command := s.Steps[len(s.Steps)-1].Command
	hold := command[:strings.Index(command, "export DEBIAN_FRONTEND")]
	release, err := profile.ReleaseDockerHold("")
	if err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string]string{"hold": hold, "release": release} {
		sbin := t.TempDir()
		const marked = "#!/bin/sh\n# Written by paisans site admit while it installs Docker, and removed after.\nexit 101\n"
		if err := os.WriteFile(filepath.Join(sbin, "policy-rc.d"), []byte(marked), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", "-c", strings.NewReplacer("/usr/sbin/", sbin+"/", "/run/paisans-docker-hold.lock", filepath.Join(sbin, "lock")).Replace(script))
		cmd.Env = append(os.Environ(), "PATH="+fakeFlock(t, 1)+":"+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "is installing Docker on this host now") {
			t.Errorf("%s with the lock held: %v: %s", name, err, out)
		}
		if got, _ := os.ReadFile(filepath.Join(sbin, "policy-rc.d")); string(got) != marked {
			t.Errorf("%s with the lock held changed the live hold: %q", name, got)
		}
		if !strings.Contains(script, "flock -n 8") {
			t.Errorf("%s takes no lock:\n%s", name, script)
		}
	}
}

// apt runs with the hold's lock closed, so nothing apt or a package's
// script starts can keep /run/paisans-docker-hold.lock held after the
// hold ends.
func TestAptRunsWithoutTheHoldsLock(t *testing.T) {
	profile, err := hostprep.Select(hostprep.ParseOSRelease(ubuntu2404))
	if err != nil {
		t.Fatal(err)
	}
	admit, _, err := profile.AdmitPackages(freshHost(), hostprep.ParseOSRelease(ubuntu2404), false)
	if err != nil {
		t.Fatal(err)
	}
	for name, command := range map[string]string{"admit": admit.Steps[len(admit.Steps)-1].Command} {
		dir := t.TempDir()
		bin, log := filepath.Join(dir, "bin"), filepath.Join(dir, "log")
		if err := os.Mkdir(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		apt := "#!/bin/sh\nif { true >&8; } 2>/dev/null; then echo \"fd8 open: $*\" >> " + log + "; else echo \"fd8 closed: $*\" >> " + log + "; fi\n"
		for f, s := range map[string]string{"apt-get": apt, "flock": "#!/bin/sh\nexit 0\n"} {
			if err := os.WriteFile(filepath.Join(bin, f), []byte(s), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		script := strings.NewReplacer("/usr/sbin/", dir+"/", "/run/paisans-docker-hold.lock", filepath.Join(dir, "lock")).Replace(command)
		cmd := exec.Command("sh", "-c", script)
		cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v: %s", name, err, out)
		}
		got, _ := os.ReadFile(log)
		if lines := strings.Split(strings.TrimSpace(string(got)), "\n"); len(lines) < 2 || strings.Contains(string(got), "fd8 open") {
			t.Errorf("%s: apt ran with the lock open, or not at all:\n%s", name, got)
		}
	}
}

// shell is dash where the host has it, as Ubuntu's /bin/sh is, else sh.
func shell() string {
	if p, err := exec.LookPath("dash"); err == nil {
		return p
	}
	return "sh"
}

// holdScript is the hold that AdmitPackages plans on a fresh host, before
// its apt lines, its paths moved into dir, and the release ReleaseDockerHold
// gives with before ahead of the release.
func holdScript(t *testing.T, dir, before string) (string, string) {
	t.Helper()
	profile, err := hostprep.Select(hostprep.ParseOSRelease(ubuntu2404))
	if err != nil {
		t.Fatal(err)
	}
	s, _, err := profile.AdmitPackages(freshHost(), hostprep.ParseOSRelease(ubuntu2404), false)
	if err != nil {
		t.Fatal(err)
	}
	command := s.Steps[len(s.Steps)-1].Command
	release, err := profile.ReleaseDockerHold(before)
	if err != nil {
		t.Fatal(err)
	}
	at := strings.NewReplacer("/usr/sbin/", dir+"/", "/run/paisans-docker-hold.lock", filepath.Join(dir, "lock")).Replace
	return at(command[:strings.Index(command, "export DEBIAN_FRONTEND")]), at(release)
}

// fakeTools is a directory of stand-ins on PATH: a flock that takes the
// lock, a systemctl that logs its arguments to log, and cp, when cpFails,
// one that leaves half a file and fails, as a full disk does.
func fakeTools(t *testing.T, log string, cpFails bool) string {
	t.Helper()
	dir := t.TempDir()
	tools := map[string]string{
		"flock":     "#!/bin/sh\nexit 0\n",
		"systemctl": "#!/bin/sh\necho \"systemctl $*\" >> " + log + "\n",
	}
	if cpFails {
		tools["cp"] = "#!/bin/sh\nfor last; do :; done\nprintf '#!/bin/sh\\nec' > \"$last\"\necho 'cp: No space left on device' >&2\nexit 1\n"
	}
	for name, s := range tools {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// When saving the owner's policy-rc.d fails, the hold fails closed: the
// step exits non-zero, admit's policy is never put in place, and the
// owner's is untouched.
func TestTheDockerHoldFailsClosedWhenTheCopyFails(t *testing.T) {
	sbin := t.TempDir()
	const owner = "#!/bin/sh\nexit 0\n"
	policy := filepath.Join(sbin, "policy-rc.d")
	if err := os.WriteFile(policy, []byte(owner), 0o755); err != nil {
		t.Fatal(err)
	}
	hold, _ := holdScript(t, sbin, "")
	cmd := exec.Command(shell(), "-c", hold+"echo the hold went on\n")
	cmd.Env = []string{"PATH=" + fakeTools(t, filepath.Join(sbin, "log"), true) + ":/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	if err == nil || strings.Contains(string(out), "the hold went on") {
		t.Errorf("the hold went on after the copy failed: %v: %s", err, out)
	}
	if got, err := os.ReadFile(policy); string(got) != owner {
		t.Errorf("the owner's policy-rc.d is now %q, %v", got, err)
	}
	if _, err := os.Lstat(policy + ".paisans-owner"); !os.IsNotExist(err) {
		t.Errorf("a copy is in place as the owner's: %v", err)
	}
}

// An install that fails leaves the units of what it adds disabled, from
// the hold's exit trap while the hold is still in place: their packages'
// scripts may have enabled them before the failure, and a reboot would
// otherwise start the system daemon. One that succeeds leaves that to the
// next step.
func TestAFailedHoldDisablesWhatItAdds(t *testing.T) {
	for _, c := range []struct {
		tail string
		want bool
	}{{"false\n", true}, {"true\n", false}} {
		sbin := t.TempDir()
		log := filepath.Join(sbin, "log")
		hold, _ := holdScript(t, sbin, "")
		cmd := exec.Command(shell(), "-c", hold+c.tail)
		cmd.Env = []string{"PATH=" + fakeTools(t, log, false) + ":/usr/bin:/bin"}
		out, err := cmd.CombinedOutput()
		if (err != nil) != c.want {
			t.Errorf("%q: the exit status is not the install's: %v: %s", c.tail, err, out)
		}
		got, _ := os.ReadFile(log)
		if disabled := strings.Contains(string(got), "systemctl disable docker.socket docker.service containerd.service"); disabled != c.want {
			t.Errorf("%q: disabled %v:\n%s", c.tail, disabled, got)
		}
	}
}

// ReleaseDockerHold runs what its caller gives after the lock is taken
// and before the hold is released, so a caller's record of the install
// is made while the hold still says the install was admit's.
func TestReleaseDockerHoldRunsBeforeAhead(t *testing.T) {
	_, release := holdScript(t, t.TempDir(), "echo before-the-release\n")
	lock, before, call := strings.Index(release, "flock -n 8"), strings.Index(release, "echo before-the-release"), strings.LastIndex(release, "\nrelease_docker_hold\n")
	if lock < 0 || before < lock || call < before {
		t.Errorf("lock %d, before %d, release %d:\n%s", lock, before, call, release)
	}
}

// fakePgrep is a directory whose pgrep answers that dpkg runs for its
// first busy calls, noting in log each answer and whether admit's policy
// is in place, then that it does not; busy below 0 answers it runs always.
func fakePgrep(t *testing.T, sbin, log string, busy int) string {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nn=$(cat %[1]s/calls 2>/dev/null || echo 0)\necho $((n+1)) > %[1]s/calls\nheld=no; grep -q 'paisans site admit' %[2]s/policy-rc.d 2>/dev/null && held=yes\nif [ %[3]d -lt 0 ] || [ \"$n\" -lt %[3]d ]; then echo \"dpkg running, hold $held\" >> %[4]s; exit 0; fi\necho \"dpkg gone, hold $held\" >> %[4]s\nexit 1\n", dir, sbin, busy, log)
	if err := os.WriteFile(filepath.Join(dir, "pgrep"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// apt-get runs dpkg in a session of its own, so a kill of apt-get can
// leave dpkg running docker-ce's postinst, which enables its units again.
// The exit trap waits for dpkg to finish, with the hold in place, before
// it confirms, disables and releases; a dpkg still running at the bound
// keeps the hold and fails the step, saying so.
func TestTheHoldWaitsForDpkg(t *testing.T) {
	sbin := t.TempDir()
	log := filepath.Join(sbin, "log")
	hold, _ := holdScript(t, sbin, "")
	cmd := exec.Command(shell(), "-c", hold+"false\n")
	cmd.Env = []string{"PATH=" + fakePgrep(t, sbin, log, 2) + ":" + fakeTools(t, log, false) + ":/usr/bin:/bin"}
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("a failed install succeeded: %s", out)
	}
	got, _ := os.ReadFile(log)
	want := "dpkg running, hold yes\ndpkg running, hold yes\ndpkg gone, hold yes\nsystemctl disable docker.socket docker.service containerd.service\n"
	if string(got) != want {
		t.Errorf("the trap did not wait for dpkg with the hold in place:\n%s\nwant\n%s", got, want)
	}

	sbin = t.TempDir()
	log = filepath.Join(sbin, "log")
	hold, _ = holdScript(t, sbin, "")
	hold = strings.Replace(hold, `[ "$n" -lt 600 ]`, `[ "$n" -lt 2 ]`, 1)
	cmd = exec.Command(shell(), "-c", hold+"false\n")
	cmd.Env = []string{"PATH=" + fakePgrep(t, sbin, log, -1) + ":" + fakeTools(t, log, false) + ":/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "dpkg is still running") {
		t.Errorf("a dpkg that does not finish: %v: %s", err, out)
	}
	got, _ = os.ReadFile(log)
	if strings.Contains(string(got), "systemctl") {
		t.Errorf("the units were touched while dpkg ran:\n%s", got)
	}
	if p, _ := os.ReadFile(filepath.Join(sbin, "policy-rc.d")); !strings.Contains(string(p), "paisans site admit") {
		t.Errorf("the hold was released while dpkg ran: %q", p)
	}
}

// wireguard-tools on Ubuntu 22.04 recommends a WireGuard kernel module
// package, which on a host whose running kernel is not one of Ubuntu's
// pulls in a whole kernel. It is installed without its recommendations, in
// an apt call of its own: Docker's packages recommend what Docker needs
// (iptables, pigz, the rootless extras), so the rest keep theirs. Both
// calls close the hold's lock, whether admit installs Docker or finds it,
// and with Docker present both keep --no-upgrade.
func TestWireGuardToolsIsInstalledWithoutRecommends(t *testing.T) {
	for _, ubuntu := range []string{ubuntu2204, ubuntu2404, ubuntu2604} {
		profile, err := hostprep.Select(hostprep.ParseOSRelease(ubuntu))
		if err != nil {
			t.Fatal(err)
		}
		host := freshHost()
		host.files["/etc/os-release"] = ubuntu
		absent, _, err := profile.AdmitPackages(host, hostprep.ParseOSRelease(ubuntu), false)
		if err != nil {
			t.Fatal(err)
		}
		present := freshHost()
		present.files["/etc/os-release"] = ubuntu
		present.responses[probePackages] = "compose present\npkg docker-ce install ok installed\npkg wireguard-tools absent\npkg git install ok installed\ndockerversion 5:29.9.0-1~ubuntu.24.04~noble\nextras 5:29.9.0-1~ubuntu.24.04~noble\nkeyring present\narch amd64\n"
		withDocker, _, err := profile.AdmitPackages(present, hostprep.ParseOSRelease(ubuntu), true)
		if err != nil {
			t.Fatal(err)
		}
		for name, s := range map[string]hostprep.Section{"Docker absent": absent, "Docker present": withDocker} {
			var command string
			for _, step := range s.Steps {
				if strings.Contains(step.Command, "wireguard-tools") {
					command = step.Command
				}
			}
			var lines []string
			for _, line := range strings.Split(command, "\n") {
				if strings.Contains(line, " install -y") {
					lines = append(lines, line)
				}
			}
			if len(lines) != 2 {
				t.Fatalf("%s: want two install lines, got:\n%s", name, command)
			}
			if strings.Contains(lines[0], "--no-install-recommends") || strings.Contains(lines[0], "wireguard-tools") {
				t.Errorf("%s: the main install is %q", name, lines[0])
			}
			if !strings.HasSuffix(lines[1], " --no-install-recommends wireguard-tools 8>&-") {
				t.Errorf("%s: wireguard-tools is installed by %q", name, lines[1])
			}
			if name == "Docker present" && (!strings.Contains(lines[0], "--no-upgrade") || !strings.Contains(lines[1], "--no-upgrade")) {
				t.Errorf("admit with Docker present dropped --no-upgrade:\n%s", command)
			}
		}
	}
}

// Admit reads the watchdog as root, where who the probe ran as is neither
// asked nor needed: only the deployment's user's read compares the owner.
func TestAdmitReadsTheWatchdogAsRoot(t *testing.T) {
	host := freshHost()
	host.responses[probeWatchdog] = "device present\nidentity watchdog0 iTCO_wdt\nsystemd 0\ndaemon absent\nowner 0\n"
	profile, err := hostprep.Select(hostprep.ParseOSRelease(ubuntu2404))
	if err != nil {
		t.Fatal(err)
	}
	d := deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}
	section, err := hostprep.PlanWatchdog(host, d, profile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(section.Present, "\n"), "iTCO_wdt") {
		t.Errorf("want the hardware watchdog present, got %+v", section)
	}
	for _, cmd := range host.ran {
		if strings.Contains(cmd, "/etc/subuid") {
			t.Errorf("admit asked who it ran as: %q", cmd)
		}
	}
}
