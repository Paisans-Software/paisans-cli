package hostadmit_test

import (
	"regexp"
	"strings"
	"testing"

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
