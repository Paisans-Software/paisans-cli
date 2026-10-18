package doctor

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admitrules"
)

// rootlessDocker is what DockerCommand prints for a rootless daemon.
const rootlessDocker = "host unix:///run/user/1001/docker.sock\nengine 29.9.1\nsecurity [\"name=seccomp,profile=builtin\",\"name=rootless\",\"name=cgroupns\"]\nsource-mark 0\n"

// beforeSourceMark is rootlessDocker without its namespace line.
var beforeSourceMark = strings.TrimSuffix(rootlessDocker, "source-mark 0\n")

// A user in any group that reaches past it fails, naming each group; uid 0
// fails on its own; a user in none passes; an answer that cannot be read
// is a warning, never a pass.
func TestUser(t *testing.T) {
	findings := User([]UserProbe{
		{Site: "home-a", Out: "uid 1001\nuser paisans-f2a9\ngroups paisans-f2a9\n"},
		{Site: "home-b", Out: "uid 1002\nuser paisans-f2a9\ngroups paisans-f2a9 docker sudo docker\n"},
		{Site: "vm", Out: "uid 0\nuser root\ngroups root\n"},
		{Site: "watch", Out: "", Err: "id: cannot find name for group ID 1003"},
		{Site: "edge", Out: "uid 1003\nuser paisans-f2a9\ngroups paisans-f2a9 toor\ngids 1003 0\n"},
	})
	if f := find(t, findings, SectionUser, "edge: paisans-f2a9 is in a group of GID 0, root's"); f.Level != Fail {
		t.Errorf("edge: level %v", f.Level)
	}
	if f := find(t, findings, SectionUser, "home-a: paisans-f2a9 is in no privileged group"); f.Level != OK {
		t.Errorf("home-a: level %v", f.Level)
	}
	b := find(t, findings, SectionUser, "home-b: paisans-f2a9 is in docker, sudo")
	if b.Level != Fail || !strings.Contains(strings.Join(b.More, "\n"), "`sudo gpasswd -d paisans-f2a9 <group>` on home-b") {
		t.Errorf("home-b: %+v", b)
	}
	if f := find(t, findings, SectionUser, "vm: the login user root has uid 0"); f.Level != Fail {
		t.Errorf("vm: level %v", f.Level)
	}
	if f := find(t, findings, SectionUser, "watch: could not read the login user's groups"); f.Level != Warn {
		t.Errorf("watch: level %v", f.Level)
	}
}

// Every group site admit refuses fails here too: the two read one list.
func TestEveryPrivilegedGroupFails(t *testing.T) {
	for _, g := range admitrules.PrivilegedGroups {
		fs := User([]UserProbe{{Site: "home-a", Out: "uid 1001\nuser paisans-f2a9\ngroups paisans-f2a9 " + g + "\n"}})
		if len(fs) != 1 || fs[0].Level != Fail {
			t.Errorf("%s: %+v", g, fs)
		}
	}
}

// The daemon answers as rootless, does not answer, answers as root, or
// says something unreadable.
func TestDockerSocket(t *testing.T) {
	probes := []DockerProbe{
		{Site: "home-a", Out: rootlessDocker},
		{Site: "home-b", Out: "host unix:///run/user/1001/docker.sock\n", Err: "Cannot connect to the Docker daemon at unix:///run/user/1001/docker.sock. Is the docker daemon running?"},
		{Site: "vm", Out: "host unix:///run/user/1001/docker.sock\nengine 29.9.1\nsecurity [\"name=apparmor\",\"name=seccomp,profile=builtin\",\"name=cgroupns\"]\n"},
		{Site: "watch", Out: "host unix:///run/user/1001/docker.sock\nengine 29.9.1\nsecurity not json\n"},
	}
	findings := DockerSocket(probes)
	if f := find(t, findings, SectionDocker, "home-a: its rootless Docker answers at unix:///run/user/1001/docker.sock"); f.Level != OK {
		t.Errorf("home-a: level %v", f.Level)
	}
	b := find(t, findings, SectionDocker, "home-b: its rootless Docker does not answer at unix:///run/user/1001/docker.sock")
	if b.Level != Fail || !strings.Contains(strings.Join(b.More, "\n"), "`systemctl --user status docker`") {
		t.Errorf("home-b: %+v", b)
	}
	if f := find(t, findings, SectionDocker, "vm: the Docker daemon at unix:///run/user/1001/docker.sock is not rootless"); f.Level != Fail {
		t.Errorf("vm: level %v", f.Level)
	}
	if f := find(t, findings, SectionDocker, "watch: could not read its Docker daemon's answer"); f.Level != Warn {
		t.Errorf("watch: level %v", f.Level)
	}
	for i, want := range []bool{true, false, false, false} {
		if got := probes[i].Answered(); got != want {
			t.Errorf("%s: Answered %v, want %v", probes[i].Site, got, want)
		}
	}
}

// 29.5.0 is the floor, inclusive; a daemon that did not answer is judged
// by DockerSocket alone.
func TestDockerEngine(t *testing.T) {
	findings := DockerEngine([]DockerProbe{
		{Site: "home-a", Out: rootlessDocker},
		{Site: "home-b", Out: strings.Replace(rootlessDocker, "29.9.1", "29.4.2", 1)},
		{Site: "vm", Out: strings.Replace(rootlessDocker, "29.9.1", "29.5.0", 1)},
		{Site: "watch", Err: "Cannot connect to the Docker daemon"},
	})
	if len(findings) != 3 {
		t.Fatalf("want 3 findings, watch's left out:\n%s", dump(findings))
	}
	if f := find(t, findings, SectionDocker, "home-a: Docker Engine 29.9.1"); f.Level != OK {
		t.Errorf("home-a: level %v", f.Level)
	}
	b := find(t, findings, SectionDocker, "home-b: Docker Engine 29.4.2, and 29.5.0 or later is needed")
	if b.Level != Fail || !strings.Contains(strings.Join(b.More, "\n"), "host networking shares the host's network namespace") {
		t.Errorf("home-b: %+v", b)
	}
	if f := find(t, findings, SectionDocker, "vm: Docker Engine 29.5.0"); f.Level != OK {
		t.Errorf("vm: level %v", f.Level)
	}
}

// src_valid_mark 0 in the namespace passes, unless the host is at 1 and no
// drop-in is loaded to keep it 0, which warns; 1 fails, with apply as the
// recovery; a value the probe could not read, or none, is a warning; a
// daemon that did not answer says nothing.
func TestSourceMark(t *testing.T) {
	findings := SourceMark([]DockerProbe{
		{Site: "home-a", Out: rootlessDocker},
		{Site: "home-b", Out: beforeSourceMark + "source-mark 1\n"},
		{Site: "vm", Out: beforeSourceMark + "source-mark unknown\n"},
		{Site: "edge", Out: beforeSourceMark},
		{Site: "watch", Out: "source-mark 1\n", Err: "Cannot connect to the Docker daemon"},
		{Site: "lab", Out: rootlessDocker + "source-mark-host 1\nsource-mark-dropin absent\n"},
		{Site: "kept", Out: rootlessDocker + "source-mark-host 1\nsource-mark-dropin loaded\n"},
	})
	if len(findings) != 6 {
		t.Fatalf("want 6 findings, watch's left out:\n%s", dump(findings))
	}
	l := find(t, findings, SectionDocker, "lab: src_valid_mark is 0 in its RootlessKit namespace, and goes back to 1 at the daemon's next start")
	if l.Level != Warn || !strings.Contains(strings.Join(l.More, "\n"), "installs the drop-in") {
		t.Errorf("lab: %+v", l)
	}
	if f := find(t, findings, SectionDocker, "kept: src_valid_mark is 0 in its RootlessKit namespace"); f.Level != OK {
		t.Errorf("kept: level %v", f.Level)
	}
	if f := find(t, findings, SectionDocker, "home-a: src_valid_mark is 0 in its RootlessKit namespace"); f.Level != OK {
		t.Errorf("home-a: level %v", f.Level)
	}
	b := find(t, findings, SectionDocker, "home-b: src_valid_mark is 1 in its RootlessKit namespace, so every new connection to an app waits a second")
	if more := strings.Join(b.More, "\n"); b.Level != Fail || !strings.Contains(more, "Tailscale") || !strings.Contains(more, "`paisans deployment apply --execute`") {
		t.Errorf("home-b: %+v", b)
	}
	for _, site := range []string{"vm", "edge"} {
		if f := find(t, findings, SectionDocker, site+": src_valid_mark in its RootlessKit namespace could not be read"); f.Level != Warn {
			t.Errorf("%s: level %v", site, f.Level)
		}
	}
}

// DockerCommand's read of the namespace never fails it: a daemon that
// answers is still the daemon answering when the namespace cannot be read.
func TestDockerCommandsSourceMarkReadCannotFailIt(t *testing.T) {
	if !strings.Contains(DockerCommand, `2>/dev/null || echo unknown)"`) {
		t.Errorf("the read can fail the command: %s", DockerCommand)
	}
}

// Linger on and the manager ordered after the mesh pass; either missing
// fails; an answer that cannot be read is a warning.
func TestLinger(t *testing.T) {
	d := fixture(t).Deployment()
	findings := Linger(d, []LingerProbe{
		{Site: "home-a", Out: "linger yes\nmesh-order yes\n"},
		{Site: "home-b", Out: "linger no\nmesh-order no\n"},
		{Site: "vm", Out: "linger \nmesh-order yes\n"},
		{Site: "watch", Out: "linger yes\n"},
	})
	w := find(t, findings, SectionLinger, "watch: could not read whether its manager starts after the mesh")
	if w.Level != Warn || !strings.Contains(strings.Join(w.More, "\n"), "no mesh-order line") {
		t.Errorf("watch order: %+v", w)
	}
	if f := find(t, findings, SectionLinger, "home-a: linger is on"); f.Level != OK {
		t.Errorf("home-a: level %v", f.Level)
	}
	if f := find(t, findings, SectionLinger, "home-a: its manager starts after the mesh at boot"); f.Level != OK {
		t.Errorf("home-a order: level %v", f.Level)
	}
	b := find(t, findings, SectionLinger, "home-b: linger is off")
	if b.Level != Fail || !strings.Contains(strings.Join(b.More, "\n"), "`paisans site admit`") {
		t.Errorf("home-b: %+v", b)
	}
	o := find(t, findings, SectionLinger, "home-b: its manager is not ordered after the mesh at boot")
	if o.Level != Fail || !strings.Contains(strings.Join(o.More, "\n"), "wg-quick@psns-f2a9") {
		t.Errorf("home-b order: %+v", o)
	}
	if f := find(t, findings, SectionLinger, "vm: could not read whether linger is on"); f.Level != Warn {
		t.Errorf("vm: level %v", f.Level)
	}
}

// LingerCommand looks for the drop-in at site admit's own path.
func TestLingerCommandReadsAdmitsDropIn(t *testing.T) {
	if !strings.Contains(LingerCommand, `"`+admitrules.MeshDropInPath("$(id -u)")+`"`) {
		t.Errorf("LingerCommand: %s", LingerCommand)
	}
}
