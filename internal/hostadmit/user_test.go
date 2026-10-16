package hostadmit_test

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// current is a host with Docker 29.9.1 and its extras installed and the
// profile loaded, so the user's tests see only the user's steps change.
func current() *fakeHost {
	return newBlank().with("engine", "29.9.1").with("setuptool", "present").with("rootlesskit-profile", "loaded").
		with("pkg", "docker-ce installed").add("pkg", "docker-ce-rootless-extras installed").add("pkg", "uidmap installed").add("pkg", "nftables installed").
		with("system-socket", "present").
		answer("docker compose version", "compose present\npkg docker-ce install ok installed\npkg docker-ce-rootless-extras install ok installed\npkg uidmap install ok installed\npkg nftables install ok installed\npkg wireguard-tools install ok installed\npkg ufw install ok installed\npkg git install ok installed\nkeyring present\narch amd64\n")
}

// The host-wide delegation drop-in and paisans-f2a9's mesh drop-in, as
// admit writes them.
const (
	delegatePath = "/etc/systemd/system/user@.service.d/delegate.conf"
	delegateConf = "# Written by paisans host admit. Shared by every admitted deployment;\n" +
		"# paisans host evict removes it with the last of them.\n" +
		"[Service]\nDelegate=cpu cpuset io memory pids\n"
	meshPath = "/etc/systemd/system/user@1001.service.d/paisans-mesh.conf"
	meshConf = "# Written by paisans host admit for deployment f2a9; paisans host evict\n" +
		"# removes it. The user's manager, its Docker daemon and its containers\n" +
		"# start after the mesh at boot.\n" +
		"[Unit]\nWants=wg-quick@psns-f2a9.service\nAfter=wg-quick@psns-f2a9.service\n"
)

// admittedUser is a host where admit has made paisans-f2a9 and written both
// drop-ins, its manager running under linger: a re-run changes nothing.
func admittedUser() *fakeHost {
	return helperInstalled(current().withUser(), "vm").withDir("/srv/paisans", "drwxr-xr-x", 0, 0).withDir("/srv/paisans/f2a9", "drwxr-x---", 1001, 1001).
		withFile(delegatePath, delegateConf, "644", 0).withFile(meshPath, meshConf, "644", 0)
}

var userCases = addCases(
	goldenCase{name: "user-fresh", host: current, site: "vm"},
	goldenCase{name: "user-existing-running", site: "vm", host: func() *fakeHost { return current().withUser() }},
	goldenCase{name: "user-admitted-unchanged", site: "vm", host: admittedUser},
	goldenCase{name: "user-not-paisans", site: "home-b", flags: hostadmit.Flags{Watchdog: true}, host: func() *fakeHost {
		return current().with("passwd", "community:x:1002:1002::/home/community:/bin/bash").with("home", "dir 1002 /home/community").with("groups", "community").
			with("group", "community").with("linger", "no").with("manager", "inactive").
			add("subuid", "community:231072:65536").add("subgid", "community:231072:65536")
	}},
	goldenCase{name: "user-no-subids-refused", site: "vm", host: func() *fakeHost {
		return current().with("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash").with("home", "dir 1001 /home/paisans-f2a9").with("groups", "paisans-f2a9").with("group", "paisans-f2a9")
	}},
	goldenCase{name: "user-home-exists-refused", site: "vm", host: func() *fakeHost {
		return current().with("home", "dir 1234 /home/paisans-f2a9")
	}},
	goldenCase{name: "user-low-subids-refused", site: "vm", host: func() *fakeHost {
		h := current().withUser()
		var kept [][2]string
		for _, kv := range h.facts {
			if kv != [2]string{"subuid", "paisans-f2a9:165536:65536"} {
				kept = append(kept, kv)
			}
		}
		h.facts = kept
		return h.add("subuid", "paisans-f2a9:0:65536")
	}},
	goldenCase{name: "user-sudo-rule-refused", site: "vm", host: func() *fakeHost {
		return withSudo(current().withUser(), sudoFixture("stock")+"    (ALL : ALL) NOPASSWD: ALL\n")
	}},
	goldenCase{name: "user-sudo-setenv-refused", site: "vm", host: func() *fakeHost {
		return withSudo(current().withUser(), sudoFixture("setenv-helper"))
	}},
	goldenCase{name: "user-overlapping-subids-refused", site: "vm", host: func() *fakeHost {
		return current().withUser().add("subuid", "someone:170000:65536")
	}},
)

func TestUserPlans(t *testing.T) { runGolden(t, userCases) }

// A user that is root, or in any group that reaches past it, is refused
// whatever else it is: docker is the owner's system daemon, sudo and the
// rest reach root or every user's logs.
func TestAPrivilegedUserIsRefused(t *testing.T) {
	for _, group := range hostadmit.PrivilegedGroups {
		h := current().withUser().with("groups", "paisans-f2a9 "+group)
		_, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
		if err == nil || !strings.Contains(err.Error(), group) {
			t.Errorf("a user in %s: %v", group, err)
		}
	}
	h := current().with("passwd", "paisans-f2a9:x:0:0::/root:/bin/bash").with("groups", "root").with("group", "root")
	if _, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{})); err == nil || !strings.Contains(err.Error(), "uid 0") {
		t.Errorf("a user with uid 0: %v", err)
	}
}

// The manager of a user that is already running is never restarted: not
// for the delegation drop-in, not for the mesh drop-in, not for linger.
func TestARunningManagerIsNeverRestarted(t *testing.T) {
	plan, err := hostadmit.Build(current().withUser(), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Steps {
		if strings.Contains(s.Command, "restart user@") || strings.Contains(s.Command, "try-restart user@") {
			t.Errorf("%s restarts the user's manager: %s", s.Title, s.Command)
		}
	}
	found := false
	for _, w := range plan.Warnings {
		found = found || strings.Contains(w.Hint, "next start")
	}
	if !found {
		t.Errorf("no warning that delegation reaches the running manager at its next start: %+v", plan.Warnings)
	}
}

// The drop-in is written before linger is enabled, since enabling linger
// starts the user's manager at once.
func TestDelegationComesBeforeLinger(t *testing.T) {
	plan, err := hostadmit.Build(current(), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	delegate, linger := -1, -1
	for i, s := range plan.Steps {
		if s.File != nil && strings.HasSuffix(s.File.Path, "delegate.conf") {
			delegate = i
		}
		if strings.Contains(s.Command, "enable-linger") {
			linger = i
		}
	}
	if delegate < 0 || linger < 0 || delegate > linger {
		t.Errorf("delegate.conf at step %d, linger at step %d", delegate, linger)
	}
}

// The mesh drop-in orders the user's manager after the deployment's
// wg-quick unit, root's and mode 644, followed by a daemon-reload and
// never a restart. A drop-in that differs from admit's, in content, mode
// or owner, is written again; a link there is refused.
func TestTheMeshDropIn(t *testing.T) {
	find := func(plan *hostadmit.Plan) *hostadmit.Step {
		for i, s := range plan.Steps {
			if s.File != nil && strings.HasSuffix(s.File.Path, "paisans-mesh.conf") {
				return &plan.Steps[i]
			}
		}
		return nil
	}
	plan, err := hostadmit.Build(current().withUser(), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	s := find(plan)
	if s == nil {
		t.Fatalf("no mesh drop-in planned for a user without one")
	}
	if s.File.Path != meshPath || s.File.Content != meshConf || s.File.Mode != 0o644 || s.Command != "systemctl daemon-reload" || s.Provisional {
		t.Errorf("mesh drop-in: %+v %+v", *s, *s.File)
	}

	for name, h := range map[string]*fakeHost{
		"content": current().withUser().withFile(meshPath, "[Unit]\n", "644", 0),
		"mode":    current().withUser().withFile(meshPath, meshConf, "666", 0),
		"owner":   current().withUser().withFile(meshPath, meshConf, "644", 1001),
	} {
		plan, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if find(plan) == nil {
			t.Errorf("a drop-in with another %s is not written again", name)
		}
	}

	plan, err = hostadmit.Build(admittedUser(), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 0 {
		t.Errorf("a re-run plans %d steps", len(plan.Steps))
	}

	h := current().withUser()
	h.dropKeyed("file", meshPath)
	h.add("file", meshPath+" link")
	if _, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{})); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("a linked mesh drop-in: %v", err)
	}
}

// On a blank host the mesh drop-in names <uid>, so it waits for the user.
func TestTheMeshDropInWaitsForTheUser(t *testing.T) {
	plan, err := hostadmit.Build(current(), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Steps {
		if s.File != nil && s.File.Path == "/etc/systemd/system/user@<uid>.service.d/paisans-mesh.conf" {
			if !s.Provisional {
				t.Errorf("the mesh drop-in is not provisional on a blank host")
			}
			return
		}
	}
	t.Errorf("no mesh drop-in planned on a blank host")
}

// On a blank host, --execute creates the user, reads the host again, and
// runs the user's steps with the UID useradd gave it, as approved.
func TestExecuteRunsTheUsersStepsWithItsUID(t *testing.T) {
	h := current()
	h.onRun = func(h *fakeHost, command string) {
		if strings.HasPrefix(command, "useradd ") {
			h.with("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash").with("home", "dir 1001 /home/paisans-f2a9").
				with("groups", "paisans-f2a9").with("group", "paisans-f2a9").
				with("linger", "no").with("manager", "inactive").
				add("subuid", "paisans-f2a9:165536:65536").add("subgid", "paisans-f2a9:165536:65536")
		}
	}
	in := input(t, "vm", hostadmit.Flags{})
	plan, err := hostadmit.Build(h, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := hostadmit.Execute(h, in, plan, ui.NewPlain(io.Discard, false)); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.writes[meshPath]; !ok || h.files[meshPath] != meshConf {
		t.Errorf("the mesh drop-in was not written at %s: %v", meshPath, h.writes)
	}
	for _, c := range []string{"useradd --create-home --shell /bin/bash paisans-f2a9", "loginctl enable-linger paisans-f2a9", "systemctl start user@1001.service"} {
		if !slices.Contains(h.ran, c) {
			t.Errorf("%q did not run: %q", c, h.ran)
		}
	}
}

// The user's own subordinate ranges must sit above every real ID: the
// egress table trusts the whole range as the user's, and newuidmap maps
// the containers' IDs onto it, so a range holding 0, 65534 or the user's
// own UID would hand a container root, nobody or the user itself.
func TestAMisplacedSubordinateRangeIsRefused(t *testing.T) {
	for _, kind := range []string{"subuid", "subgid"} {
		for _, c := range []struct{ name, line, want string }{
			{"below 100000", "paisans-f2a9:99999:65536", "starts below 100000"},
			{"holding 0", "paisans-f2a9:0:65536", "holds 0"},
			{"holding 65534", "paisans-f2a9:65000:65536", "holds 65534"},
			{"holding its own UID", "paisans-f2a9:165536:65536", "holds paisans-f2a9's own ID 170000"},
		} {
			h := current().withUser()
			own := c.name == "holding its own UID"
			if own {
				h.with("passwd", "paisans-f2a9:x:170000:170000::/home/paisans-f2a9:/bin/bash").with("home", "dir 170000 /home/paisans-f2a9")
			}
			var kept [][2]string
			for _, kv := range h.facts {
				ranged := kv[0] == kind || own && (kv[0] == "subuid" || kv[0] == "subgid")
				if !ranged || !strings.HasPrefix(kv[1], "paisans-f2a9:") {
					kept = append(kept, kv)
				}
			}
			h.facts = kept
			h.add(kind, c.line)
			if other := map[string]string{"subuid": "subgid", "subgid": "subuid"}[kind]; own {
				h.add(other, "paisans-f2a9:300000:65536")
			}
			_, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "/etc/"+kind) {
				t.Errorf("%s %s: %v", kind, c.name, err)
			}
		}
	}
}

// A home that exists before useradd makes the user is refused, whoever
// owns it: useradd takes it over with only a warning, and with it whatever
// keys and data an earlier user left there. A link there would make the
// new user's home wherever it points.
func TestAnExistingHomeIsRefused(t *testing.T) {
	for _, c := range []struct{ name, home, want string }{
		{"another's", "dir 1234 /home/paisans-f2a9", "/home/paisans-f2a9 already exists"},
		{"root's", "dir 0 /home/paisans-f2a9", "/home/paisans-f2a9 already exists"},
		{"a file", "other 0 /home/paisans-f2a9", "/home/paisans-f2a9 already exists"},
		{"a link", "link 0 /home/paisans-f2a9", "/home/paisans-f2a9 already exists"},
	} {
		_, err := hostadmit.Build(current().with("home", c.home), input(t, "vm", hostadmit.Flags{}))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("a home that is %s, before the user: %v", c.name, err)
		}
	}
	for _, c := range []struct{ name, home, want string }{
		{"another's", "dir 1234 /home/paisans-f2a9", "is not paisans-f2a9's"},
		{"a link", "link 0 /home/paisans-f2a9", "is a link"},
		{"absent", "absent 0 /home/paisans-f2a9", "is not a directory"},
		{"unread", "", "could not be read"},
	} {
		h := current().withUser().with("home", c.home)
		if c.home == "" {
			var kept [][2]string
			for _, kv := range h.facts {
				if kv[0] != "home" {
					kept = append(kept, kv)
				}
			}
			h.facts = kept
		}
		_, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("an existing user's home that is %s: %v", c.name, err)
		}
	}
}

// sudoFixture is what sudo -l -U paisans-f2a9 printed, under LC_ALL=C,
// in ubuntu:24.04 with admit's helper line installed (stock) and with one
// more sudoers entry for each other fixture; no-rules has no line at all.
func sudoFixture(name string) string {
	b, err := os.ReadFile(filepath.Join("testdata", "sudo", name+".txt"))
	if err != nil {
		panic(err)
	}
	return string(b)
}

// withSudo is h with sudo installed and listing out for the user, as the
// probe prints it: one sudo-l line per line sudo printed.
func withSudo(h *fakeHost, out string) *fakeHost {
	h.with("sudo", "listed")
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		h.add("sudo-l", l)
	}
	return h
}

// A user sudo grants anything to, by name or by a group admit does not
// list, is refused: admit's own line for the WireGuard helper, which a
// re-run finds, is to be its only rule.
func TestAUserWithSudoRulesIsRefused(t *testing.T) {
	for _, c := range []struct {
		name string
		host *fakeHost
		want string
	}{
		{"a rule", withSudo(current().withUser(), sudoFixture("stock")+"    (ALL : ALL) ALL\n"), "sudo grants paisans-f2a9 a rule"},
		{"a rule beside admit's", withSudo(current().withUser(), sudoFixture("stock")+"    (root) /usr/bin/apt\n"), "(root) /usr/bin/apt"},
		{"output it cannot read", withSudo(current().withUser(), "sudo: unable to resolve host\n"), "sudo's rules for paisans-f2a9 could not be read"},
		{"no sudo line", func() *fakeHost {
			h := current().withUser()
			var kept [][2]string
			for _, kv := range h.facts {
				if kv[0] != "sudo" {
					kept = append(kept, kv)
				}
			}
			h.facts = kept
			return h
		}(), "sudo's rules for paisans-f2a9 could not be read"},
	} {
		_, err := hostadmit.Build(c.host, input(t, "vm", hostadmit.Flags{}))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// A sudoers default that lets the user hand the helper an environment, or
// that is set for the user, its runas or the helper alone, is refused:
// with setenv, sudo -l lists only admit's line while the user passes
// LD_PRELOAD to the helper, as root. Stock Ubuntu's defaults, and ones
// that change nothing the helper sees, pass.
func TestASudoDefaultThatReachesTheHelperIsRefused(t *testing.T) {
	for name, want := range map[string]string{
		"setenv-user":      "setenv",
		"setenv-helper":    "Defaults!/usr/local/libexec/paisans/wg-psns-f2a9 setenv",
		"runas-root":       "Defaults>root env_reset",
		"not-env-reset":    "!env_reset",
		"env-keep":         "env_keep+=LD_PRELOAD",
		"env-check":        "env_check+=FOO",
		"env-file":         "env_file=/etc/x",
		"not-secure-path":  "!secure_path",
		"secure-path-home": `secure_path=/home/paisans-f2a9/bin\:/usr/bin`,
		"runchroot":        "runchroot=*",
	} {
		_, err := hostadmit.Build(withSudo(current().withUser(), sudoFixture(name)), input(t, "vm", hostadmit.Flags{}))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"stock", "no-rules", "harmless-user"} {
		if _, err := hostadmit.Build(withSudo(current().withUser(), sudoFixture(name)), input(t, "vm", hostadmit.Flags{})); err != nil {
			t.Errorf("%s is refused: %v", name, err)
		}
	}
}

// The groups added for Ubuntu 24.04 are refused like the rest.
func TestTheRootEquivalentGroupsAreListed(t *testing.T) {
	for _, g := range []string{"disk", "shadow", "kmem", "libvirt", "incus-admin", "lxd"} {
		if !slices.Contains(hostadmit.PrivilegedGroups, g) {
			t.Errorf("%s is not in PrivilegedGroups", g)
		}
	}
	_, err := hostadmit.Build(current().withUser().with("groups", "paisans-f2a9 disk"), input(t, "vm", hostadmit.Flags{}))
	if err == nil || !strings.Contains(err.Error(), "disk") {
		t.Errorf("a user in disk: %v", err)
	}
}
