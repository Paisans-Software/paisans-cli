package hostprep_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

// With Docker absent, admit installs Docker from Docker's repository with
// the rootless extras; with it present, it installs the extras and the
// rest without upgrading anything already installed, so the owner's
// Docker is never upgraded by admit.
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
	for _, want := range []string{"add docker apt key", "add docker apt repo", "docker-ce ", "docker-ce-rootless-extras", "uidmap", "nftables", "wireguard-tools", "ufw", "git"} {
		if !strings.Contains(all, want) {
			t.Errorf("a blank host's admit packages lack %q:\n%s", want, all)
		}
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

// On a host without Docker, admit refuses what host prepare refuses, so
// installing Docker never removes Ubuntu's containerd or runc.
func TestAdmitPackagesRefusesConflicts(t *testing.T) {
	profile, err := hostprep.Select(hostprep.ParseOSRelease(ubuntu2404))
	if err != nil {
		t.Fatal(err)
	}
	h := freshHost()
	h.responses[probePackages] = "compose absent\npkg containerd install ok installed\npkg runc install ok installed\nkeyring absent\narch amd64\n"
	_, err = profile.AdmitPackages(h, hostprep.ParseOSRelease(ubuntu2404), false)
	if err == nil || !strings.Contains(err.Error(), "containerd, runc") || !strings.Contains(err.Error(), "Nothing was changed") {
		t.Errorf("want a refusal naming containerd and runc, got %v", err)
	}
}
