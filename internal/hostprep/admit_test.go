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
	present.responses[probePackages] = "compose present\npkg docker-ce install ok installed\npkg wireguard-tools absent\npkg ufw install ok installed\npkg git install ok installed\nkeyring present\narch amd64\n"
	s, err = profile.AdmitPackages(present, hostprep.ParseOSRelease(ubuntu2404), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Steps) != 1 || !strings.Contains(s.Steps[0].Command, "--no-upgrade") || strings.Contains(s.Steps[0].Command, " docker-ce ") || !strings.Contains(s.Steps[0].Command, "docker-ce-rootless-extras") {
		t.Errorf("with Docker present, admit plans %+v", s.Steps)
	}
}
