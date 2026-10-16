package hostadmit_test

import (
	"strings"
	"testing"
)

var dockerCases = addCases(
	goldenCase{name: "docker-blank", host: newBlank, site: "vm"},
	goldenCase{name: "docker-old-refused", site: "vm", host: func() *fakeHost {
		return newBlank().with("engine", "27.5.1").with("pkg", "docker-ce installed").with("system-socket", "present")
	}},
	goldenCase{name: "docker-current", site: "vm", host: func() *fakeHost {
		return newBlank().with("engine", "29.9.1").with("system-socket", "present").add("pkg", "docker-ce installed").
			answer("docker compose version", "compose present\npkg docker-ce install ok installed\npkg ufw install ok installed\npkg git install ok installed\npkg wireguard-tools install ok installed\npkg nftables install ok installed\nkeyring present\narch amd64\n")
	}},
	goldenCase{name: "docker-snap-refused", site: "vm", host: func() *fakeHost {
		return newBlank().with("snap", "docker").with("engine", "29.9.1")
	}},
	goldenCase{name: "docker-distro-without-extras-refused", site: "vm", host: func() *fakeHost {
		return newBlank().with("engine", "29.6.0").with("pkg", "docker.io installed")
	}},
	goldenCase{name: "apparmor-extras-without-profile-refused", site: "vm", host: func() *fakeHost {
		return newBlank().with("engine", "29.9.1").with("setuptool", "present").
			with("pkg", "docker-ce-rootless-extras installed")
	}},
)

func TestDockerPlans(t *testing.T) { runGolden(t, dockerCases) }

// Every system-Docker command admit runs names the system socket, so an
// owner's DOCKER_HOST or context never points it elsewhere.
func TestSystemDockerIsNamedBySocket(t *testing.T) {
	for _, c := range allCases {
		_, plan := golden(t, c)
		if plan == nil {
			continue
		}
		for _, s := range plan.Steps {
			for _, line := range strings.Split(s.Command, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "docker ") && !strings.Contains(line, "-H unix:///var/run/docker.sock") {
					t.Errorf("%s: %s runs %q", c.name, s.Title, line)
				}
			}
		}
	}
}
