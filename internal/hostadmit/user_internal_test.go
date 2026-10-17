package hostadmit

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admitrules"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

// asUser runs a command as the user with nothing of root's environment:
// env -i, then the user's own home, runtime directory, bus and socket.
func TestAsUserClearsRootsEnvironment(t *testing.T) {
	p := &planner{in: testInput(t), f: Facts{User: UserFacts{Exists: true, UID: 1001, Home: "/home/paisans-f2a9"}}}
	got := asUser(p, "cat ~/.ssh/authorized_keys")
	want := "runuser -u paisans-f2a9 -- env -i HOME='/home/paisans-f2a9' PATH=/usr/bin:/bin:/usr/sbin:/sbin" +
		" XDG_RUNTIME_DIR=/run/user/1001 DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1001/bus" +
		" DOCKER_HOST=unix:///run/user/1001/docker.sock sh -c 'cat ~/.ssh/authorized_keys'"
	if got != want {
		t.Errorf("asUser:\n got %s\nwant %s", got, want)
	}
	p.f.User = UserFacts{}
	if got := asUser(p, "true"); !strings.Contains(got, "HOME='/home/paisans-f2a9' ") || !strings.Contains(got, "/run/user/<uid>/") {
		t.Errorf("asUser before the user exists: %s", got)
	}
}

// The probe reads the user's mesh drop-in, at the path the user's UID
// names, as it reads every other file admit writes as root.
func TestTheProbeReadsTheMeshDropIn(t *testing.T) {
	in := testInput(t)
	script, err := probeScript(deployment.Deployment{ID: in.Request.Deployment}, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, `userfiles="$userfiles /etc/systemd/system/user@$uid.service.d/paisans-mesh.conf"`) ||
		!strings.Contains(script, " $userfiles; do") {
		t.Errorf("the probe does not read the mesh drop-in:\n%s", script)
	}
	if got := admitrules.MeshDropInPath("1001"); got != "/etc/systemd/system/user@1001.service.d/paisans-mesh.conf" {
		t.Errorf("MeshDropInPath: %s", got)
	}
}

// Every Ubuntu release a host profile supports has its default delegation
// written down, so registering a release forces the question. A release
// without one is read as systemd's long-standing default, memory and
// pids, so no warning ever claims those are missing.
func TestEverySupportedReleaseHasItsDefaultDelegation(t *testing.T) {
	for _, s := range hostprep.Supported() {
		id, version, _ := strings.Cut(s, " ")
		if id != "ubuntu" {
			continue
		}
		if _, ok := delegatedByDefault[version]; !ok {
			t.Errorf("%s: no entry in delegatedByDefault", s)
		}
	}
	if got := undelegated(hostprep.OSRelease{ID: "ubuntu", VersionID: "99.04"}); got != "cpu, cpuset and io" {
		t.Errorf("an unlisted release lacks %q", got)
	}
}
