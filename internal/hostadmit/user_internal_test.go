package hostadmit

import (
	"os"
	"slices"
	"strings"
	"testing"

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
	if got := meshDropInPath("1001"); got != "/etc/systemd/system/user@1001.service.d/paisans-mesh.conf" {
		t.Errorf("meshDropInPath: %s", got)
	}
}

// readSudo reads sudo -l as ubuntu:24.04 prints it off a terminal, its
// Defaults on one line, into each entry, each rule and each specific
// line; and as it prints them on a terminal, wrapped after a comma.
func TestReadSudoReadsUbuntusListing(t *testing.T) {
	b, err := os.ReadFile("testdata/sudo/setenv-helper.txt")
	if err != nil {
		t.Fatal(err)
	}
	var u UserFacts
	readSudo(&u, strings.Split(strings.TrimRight(string(b), "\n"), "\n"))
	if !u.SudoRead ||
		!slices.Equal(u.SudoDefaults, []string{"env_reset", "mail_badpass", `secure_path=/usr/local/sbin\:/usr/local/bin\:/usr/sbin\:/usr/bin\:/sbin\:/bin\:/snap/bin`, "use_pty"}) ||
		!slices.Equal(u.SudoRules, []string{"(root) NOPASSWD: /usr/local/libexec/paisans/wg-psns-f2a9"}) ||
		!slices.Equal(u.SudoSpecific, []string{"Defaults!/usr/local/libexec/paisans/wg-psns-f2a9 setenv"}) {
		t.Errorf("%+v", u)
	}
	u = UserFacts{}
	readSudo(&u, []string{"Matching Defaults entries for paisans-f2a9 on host:", "    env_reset, mail_badpass,", `    secure_path=/usr/bin\:/bin,`, "    use_pty", "",
		"User paisans-f2a9 may run the following commands on host:", "    (root) NOPASSWD: /usr/local/libexec/paisans/wg-psns-f2a9"})
	if !u.SudoRead || !slices.Equal(u.SudoDefaults, []string{"env_reset", "mail_badpass", `secure_path=/usr/bin\:/bin`, "use_pty"}) {
		t.Errorf("wrapped Defaults: %+v", u)
	}
	u = UserFacts{}
	readSudo(&u, []string{"Matching Defaults entries for paisans-f2a9 on host:", "    env_reset", "Something sudo never printed"})
	if u.SudoRead {
		t.Errorf("an unknown line is read: %+v", u)
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
