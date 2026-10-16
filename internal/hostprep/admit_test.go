package hostprep_test

import (
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
	for _, r := range hostprep.AdmitRules(d, 2222, 51820, true) {
		got = append(got, r.String()+" ("+r.Why+")")
	}
	want := []string{"2222/tcp (ssh, the bootstrap route)", "51820/udp (WireGuard, the mesh)", "all inbound on psns-f2a9 (the mesh: etcd, Patroni, Garage, HAProxy)", "80/tcp (public ports, HTTP)", "443/tcp (public ports, HTTPS)", "443/udp (public ports, HTTP/3)"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("AdmitRules =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if rules := hostprep.AdmitRules(d, 22, 0, false); len(rules) != 2 || !rules[0].SSH {
		t.Errorf("no port, no public ports: %+v", rules)
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

// On a host without Docker, admit refuses what host prepare refuses, so
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

// A sidecar as host prepare writes one reads back and writes out the same,
// so admit's sidecar and host prepare's are one format.
func TestOwnedKeysRoundTrip(t *testing.T) {
	sidecar := "# Keys this deployment claims in ubuntu's authorized_keys: <mark> <fingerprint> <name>. host prepare removes a line only when it is marked added here and no other deployment's file lists it.\n" +
		"added SHA256:4b2Q4Jx3ZQf1XU1Vb1k0f0sYz8m9n7bq3kq0iYkQWd0 alice\n" +
		"shared SHA256:9xQ1m0bK3iYp2nWm7c8d5e4f3g2h1j0k9l8m7n6o5p4 bob\n"
	keys := hostprep.ParseOwnedKeys(sidecar)
	if len(keys) != 2 || keys[0] != (hostprep.OwnedKey{Mark: "added", Fingerprint: "SHA256:4b2Q4Jx3ZQf1XU1Vb1k0f0sYz8m9n7bq3kq0iYkQWd0", Name: "alice"}) {
		t.Fatalf("ParseOwnedKeys = %+v", keys)
	}
	if got := hostprep.OwnedKeysFile("ubuntu", keys); got != sidecar {
		t.Errorf("OwnedKeysFile =\n%s\nwant\n%s", got, sidecar)
	}
	if got := hostprep.OwnedKeysFile("ubuntu", []hostprep.OwnedKey{keys[1], keys[0]}); got != sidecar {
		t.Errorf("OwnedKeysFile does not sort by fingerprint:\n%s", got)
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
	if hold < 0 || apt < hold || !strings.Contains(install.Command, "trap release_docker_hold EXIT") {
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
	release, err := profile.ReleaseDockerHold()
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
			at := func(script string) string { return strings.ReplaceAll(script, "/usr/sbin/", sbin+"/") }
			run := func(script string) {
				t.Helper()
				cmd := exec.Command("sh", "-c", script)
				cmd.Env = append(os.Environ(), "LOG="+filepath.Join(dir, "log"))
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
			for _, f := range []string{policy + ".paisans-owner", filepath.Join(sbin, ".policy-rc.d.paisans-tmp")} {
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
	out, err := exec.Command("sh", "-c", strings.ReplaceAll(hold, "/usr/sbin/", sbin+"/")).CombinedOutput()
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
