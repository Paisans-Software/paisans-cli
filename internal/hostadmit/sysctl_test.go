package hostadmit_test

import (
	"io"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/hostadmit"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/version"
)

// other is the record of a second deployment on the host, 0c0d, with the
// grants and forwarding given. The fixture always encodes, so an error is
// a broken test and panics.
func other(public, forwarding bool) string {
	rec := admission.Record{Version: admission.RecordVersion, Deployment: "0c0dc4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01", User: "paisans-0c0d", Site: "other", Roles: []string{"witness"},
		Grants: admission.Grants{PublicPorts: public}, Forwarding: forwarding,
		Ports: admission.Ports{SSH: 22, WireGuard: 51821, Public: []int{}}, AdmittedAt: "2026-10-01T09:00:00Z", MeshSubnet: "10.45.0.0/24", Build: version.Current()}
	if public {
		rec.Ports.Public = []int{80, 443}
	}
	data, err := admission.EncodeRecord(rec)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// withOther is h with deployment 0c0d admitted too.
func withOther(h *fakeHost, public, forwarding bool) *fakeHost {
	return h.add("record", "/etc/paisans/admitted.0c0d.json").file("/etc/paisans/admitted.0c0d.json", other(public, forwarding))
}

const beforeFile = "net.ipv4.ip_forward = 0\nnet.ipv4.ip_unprivileged_port_start = 1024\n"

// steps reports whether any step of plan writes path.
func writes(plan *hostadmit.Plan, path string) bool {
	for _, s := range plan.Steps {
		if s.File != nil && s.File.Path == path {
			return true
		}
	}
	return false
}

// withUnreadable is h with a second deployment's record that admit cannot
// parse, as a newer paisans' would be.
func withUnreadable(h *fakeHost) *fakeHost {
	return h.add("record", "/etc/paisans/admitted.0c0d.json").file("/etc/paisans/admitted.0c0d.json", `{"version":99}`)
}

var sysctlCases = addCases(
	goldenCase{name: "sysctl-endpoint-and-low-ports", focus: []string{"sysctl"}, site: "home-a", flags: hostadmit.Flags{Watchdog: true, PublicPorts: true}, host: home},
	goldenCase{name: "sysctl-no-endpoint", focus: []string{"admission record"}, site: "vm", host: home},
	goldenCase{name: "sysctl-forwarding-on-already", focus: []string{"sysctl"}, site: "home-a", flags: hostadmit.Flags{Watchdog: true, PublicPorts: true}, host: func() *fakeHost { return home().with("ip-forward", "1") }},
	goldenCase{name: "sysctl-shared-with-another", focus: []string{"admission record"}, site: "vm", host: func() *fakeHost {
		return withOther(home(), true, true).with("port-start", "80").with("ip-forward", "1").
			withFile("/etc/sysctl.d/60-paisans.conf", hostadmit.SysctlFile(true, true), "644", 0).
			file("/etc/sysctl.d/60-paisans.conf", hostadmit.SysctlFile(true, true)).file("/etc/paisans/sysctl.before", beforeFile)
	}},
)

func TestSysctlPlans(t *testing.T) { runGolden(t, sysctlCases) }

// The low ports are announced as what they are: every local user may bind
// them; forwarding as routing between every interface.
func TestSysctlWarningsSayWhatTheyGrant(t *testing.T) {
	plan, err := hostadmit.Build(home(), input(t, "home-a", hostadmit.Flags{Watchdog: true, PublicPorts: true}))
	if err != nil {
		t.Fatal(err)
	}
	text := plan.Text()
	for _, want := range []string{"every local user", "ports 80 to 1023", "routing between every interface"} {
		if !strings.Contains(text, want) {
			t.Errorf("the plan does not say %q", want)
		}
	}
}

// A before file is read as root and its values reach a command line, so one
// that holds anything but digits under a known key is refused.
func TestSysctlBeforeFileMustHoldNumbers(t *testing.T) {
	h := home().file("/etc/paisans/sysctl.before", "net.ipv4.ip_forward = 0; reboot\n")
	_, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	if err == nil || !strings.Contains(err.Error(), "not a value admit wrote") {
		t.Fatalf("a before file with a command in it was accepted: %v", err)
	}
}

// A record admit cannot read turns nothing on: a request needing neither
// grant nor forwarding changes nothing, and the warning names the record.
func TestUnreadableRecordTurnsNothingOn(t *testing.T) {
	plan, err := hostadmit.Build(withUnreadable(home()), input(t, "store", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/etc/sysctl.d/60-paisans.conf", "/etc/paisans/sysctl.before"} {
		if writes(plan, p) {
			t.Errorf("an unreadable record made admit write %s", p)
		}
	}
	if !strings.Contains(plan.Text(), "admitted.0c0d.json cannot be read") {
		t.Errorf("the plan does not name the unreadable record:\n%s", plan.Text())
	}
}

// With the file holding both keys, an unreadable record keeps them.
func TestUnreadableRecordKeepsWhatTheFileHolds(t *testing.T) {
	h := withUnreadable(home()).with("port-start", "80").with("ip-forward", "1").
		withFile("/etc/sysctl.d/60-paisans.conf", hostadmit.SysctlFile(true, true), "644", 0).
		file("/etc/sysctl.d/60-paisans.conf", hostadmit.SysctlFile(true, true)).file("/etc/paisans/sysctl.before", beforeFile)
	plan, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Steps {
		if strings.Contains(s.Title, "sysctl") || strings.Contains(s.Title, "restore") || strings.Contains(s.Title, "60-paisans") {
			t.Errorf("an unreadable record changed the sysctls: %q", s.Title)
		}
	}
}

// The first admit to set a key records the host's value.
func TestFirstAdmitRecordsTheHostsValues(t *testing.T) {
	plan, err := hostadmit.Build(home(), input(t, "home-a", hostadmit.Flags{Watchdog: true, PublicPorts: true}))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Steps {
		if s.File != nil && s.File.Path == "/etc/paisans/sysctl.before" {
			if !strings.Contains(s.File.Content, "ip_unprivileged_port_start = 1024") || !strings.Contains(s.File.Content, "ip_forward = 0") {
				t.Errorf("the before file is %q", s.File.Content)
			}
			return
		}
	}
	t.Fatal("the first admit recorded nothing")
}

// A second admit holding a key the file already sets records nothing new,
// whether or not the before file has it, since the live value is paisans'.
func TestSecondAdmitRecordsNothing(t *testing.T) {
	for name, before := range map[string]string{"recorded": beforeFile, "not recorded": ""} {
		t.Run(name, func(t *testing.T) {
			h := withOther(home(), true, true).with("port-start", "80").with("ip-forward", "1").
				withFile("/etc/sysctl.d/60-paisans.conf", hostadmit.SysctlFile(true, true), "644", 0).
				file("/etc/sysctl.d/60-paisans.conf", hostadmit.SysctlFile(true, true))
			if before != "" {
				h.file("/etc/paisans/sysctl.before", before)
			}
			plan, err := hostadmit.Build(h, input(t, "home-a", hostadmit.Flags{Watchdog: true, PublicPorts: true}))
			if err != nil {
				t.Fatal(err)
			}
			if writes(plan, "/etc/paisans/sysctl.before") {
				t.Error("a key the file already sets was recorded again")
			}
		})
	}
}

// Installing Docker starts dockerd, which turns ip_forward on. On a fresh
// host the first --execute must still complete when it reads the host again
// once the user exists, and the value recorded for evict to restore is the
// one the host had before admit changed anything. A change from outside
// admit, after the plan was approved, still stops it.
func TestTheFirstExecuteRecordsForwardingAsItWasBeforeDocker(t *testing.T) {
	for _, outside := range []bool{false, true} {
		h := newBlank()
		in := input(t, "home-a", hostadmit.Flags{Watchdog: true, PublicPorts: true})
		h.onRun = func(h *fakeHost, command string) {
			switch {
			case strings.Contains(command, registry.LockPath):
				h.file(registry.Path, registryOf(in.Request))
			case strings.HasPrefix(command, "install -d -m 755 -o root -g root /etc/paisans"):
				h.withDir("/etc/paisans", "drwxr-xr-x", 0, 0)
			case strings.Contains(command, "docker-ce-cli") && !strings.Contains(command, "docker compose version"):
				// docker-ce's postinst starts dockerd, which turns on forwarding.
				h.with("engine", "29.9.1").with("setuptool", "present").with("system-socket", "present").
					dropKeyed("pkg", "docker-ce")
				h.add("pkg", "docker-ce installed").add("pkg", "docker-ce-rootless-extras installed").add("pkg", "uidmap installed").
					answer("docker compose version", current().answers["docker compose version"]).
					with("ip-forward", "1")
			case strings.HasPrefix(command, "useradd "):
				h.with("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash").with("home", "dir 1001 /home/paisans-f2a9").
					with("groups", "paisans-f2a9").with("group", "paisans-f2a9").
					with("linger", "no").with("manager", "inactive").
					add("subuid", "paisans-f2a9:165536:65536").add("subgid", "paisans-f2a9:165536:65536")
				if outside {
					// A second on-link prefix, which the egress table drops.
					h.add("lan6", "2001:db8:2::/64 eth0")
				}
			}
		}
		plan, err := hostadmit.Build(h, in)
		if err != nil {
			t.Fatal(err)
		}
		err = hostadmit.Execute(h, in, plan, ui.NewPlain(io.Discard, false))
		if outside {
			if err == nil || !strings.Contains(err.Error(), "the host changed while admit ran") {
				t.Errorf("an on-link prefix added from outside admit: Execute = %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if got := h.files["/etc/paisans/sysctl.before"]; !strings.Contains(got, "net.ipv4.ip_forward = 0\n") || !strings.Contains(got, "net.ipv4.ip_unprivileged_port_start = 1024\n") {
			t.Errorf("sysctl.before does not hold the host's values before admit:\n%s", got)
		}
	}
}

// A key no admitted deployment needs any more is restored before the
// before file forgets it, so a restore that fails leaves its value there
// for the next run; and the file is set with every key checked after
// sysctl --system, since a file read after 60-paisans.conf could set one
// back.
func TestRestoreRunsBeforeTheValueIsForgotten(t *testing.T) {
	h := admittedHost(requestFor("home-a"), hostadmit.Flags{Watchdog: true, PublicPorts: true}).file("/etc/paisans/sysctl.before", beforeFile)
	req := requestFor("home-a")
	req.Grants.PublicPorts = false
	plan, err := hostadmit.Build(h, hostadmit.Input{Request: req, Digest: admission.Digest(admission.Encode(req)), Flags: hostadmit.Flags{Watchdog: true}, Now: admitted})
	if err != nil {
		t.Fatal(err)
	}
	restore, forget := -1, -1
	for i, s := range plan.Steps {
		switch {
		case strings.HasPrefix(s.Title, "restore net.ipv4"):
			restore = i
		case s.File != nil && s.File.Path == "/etc/paisans/sysctl.before", s.Command == "rm -f /etc/paisans/sysctl.before":
			forget = i
		}
	}
	if restore < 0 || forget >= 0 && forget < restore {
		t.Errorf("restore is step %d and the before file is rewritten at %d:\n%s", restore, forget, plan.Text())
	}

	plan, err = hostadmit.Build(home(), input(t, "home-a", hostadmit.Flags{Watchdog: true, PublicPorts: true}))
	if err != nil {
		t.Fatal(err)
	}
	record, set := -1, -1
	for i, s := range plan.Steps {
		switch {
		case s.File != nil && s.File.Path == "/etc/paisans/sysctl.before":
			record = i
		case s.Title == "set the host's sysctls":
			set = i
			for _, want := range []string{`"$(sysctl -n net.ipv4.ip_unprivileged_port_start)" = 80`, `"$(sysctl -n net.ipv4.ip_forward)" = 1`} {
				if !strings.Contains(s.Command, want) {
					t.Errorf("the set step does not check %q:\n%s", want, s.Command)
				}
			}
		}
	}
	if record < 0 || set < record {
		t.Errorf("the values are recorded at %d and set at %d", record, set)
	}
}

// The values evict restores are recorded before anything else changes the
// host: installing Docker starts dockerd, which turns forwarding on, so a
// run that stops after it, and the run after that, must still find the
// host's own value. A value recorded already is kept, whatever the host
// reads now.
func TestTheBeforeValuesAreRecordedAheadOfDocker(t *testing.T) {
	plan, err := hostadmit.Build(newBlank(), input(t, "home-a", hostadmit.Flags{Watchdog: true, PublicPorts: true}))
	if err != nil {
		t.Fatal(err)
	}
	record, docker := -1, -1
	for i, s := range plan.Steps {
		switch {
		case s.File != nil && s.File.Path == "/etc/paisans/sysctl.before" && record < 0:
			record = i
		case strings.HasPrefix(s.Title, "install packages") && docker < 0:
			docker = i
		}
	}
	if record < 0 || docker < 0 || record > docker {
		t.Errorf("recorded at %d, Docker installed at %d:\n%s", record, docker, plan.Text())
	}
	// A run that stopped once Docker was in: forwarding is on now, and the
	// before file holds what the host had.
	h := current().withFile("/etc/paisans/sysctl.before", beforeFile, "644", 0).file("/etc/paisans/sysctl.before", beforeFile).
		withDir("/etc/paisans", "drwxr-xr-x", 0, 0).with("ip-forward", "1")
	plan, err = hostadmit.Build(h, input(t, "home-a", hostadmit.Flags{Watchdog: true, PublicPorts: true}))
	if err != nil {
		t.Fatal(err)
	}
	if writes(plan, "/etc/paisans/sysctl.before") {
		t.Errorf("a re-run records the before values again:\n%s", plan.Text())
	}
}
