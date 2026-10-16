package hostprep_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

// Admit opens the SSH port, the mesh's port when one is admitted, the
// mesh interface, and 80 and 443 with the public ports; no br-+ rule, since
// a rootless container reaches the mesh address through the user's own
// process, not a host bridge.
func TestAdmitRules(t *testing.T) {
	d := deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}
	var got []string
	for _, r := range hostprep.AdmitRules(d, 2222, 51820, true) {
		got = append(got, r.String()+" ("+r.Why+")")
	}
	want := []string{"2222/tcp (ssh, the bootstrap route)", "51820/udp (WireGuard, the mesh)", "all inbound on psns-f2a9 (the mesh: etcd, Patroni, Garage, HAProxy)", "80/tcp (public ports, HTTP)", "443/tcp (public ports, HTTPS)"}
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
	s, err := profile.AdmitPackages(blank, hostprep.ParseOSRelease(ubuntu2404), false)
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
	s, err = profile.AdmitPackages(present, hostprep.ParseOSRelease(ubuntu2404), true)
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
	_, err = profile.AdmitPackages(h, hostprep.ParseOSRelease(ubuntu2404), true)
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
	s, err := profile.AdmitPackages(freshHost(), hostprep.ParseOSRelease(ubuntu2404), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range s.Steps {
		if st.Title != "add docker apt key" {
			continue
		}
		if !strings.Contains(st.Command, `$1 == "pub" { n++; take = 1; next }`) || !strings.Contains(st.Command, `END { if (n == 1) print fpr }`) {
			t.Errorf("the check does not require exactly one primary key:\n%s", st.Command)
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
	_, err = profile.AdmitPackages(h, hostprep.ParseOSRelease(ubuntu2404), false)
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
	_, err = profile.AdmitPackages(h, hostprep.ParseOSRelease(ubuntu2404), true)
	if err == nil || !strings.Contains(err.Error(), "docker-ce is not installed as a Debian package") {
		t.Errorf("a removed docker-ce: %v", err)
	}
	h.responses[probePackages] = "compose present\npkg docker-ce install ok installed\ndockerversion 5:29.9.0-1~ubuntu.24.04~noble\nkeyring absent\narch amd64\n"
	_, err = profile.AdmitPackages(h, hostprep.ParseOSRelease(ubuntu2404), true)
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
