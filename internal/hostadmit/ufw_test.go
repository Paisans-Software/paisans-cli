package hostadmit_test

import (
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

const ufwBeforeMarker = "paisans site admit: before enabling ufw"

// sshdOn22 is what the probe before enabling ufw prints on Ubuntu 24.04
// with sshd listening on 22, systemd-run there and no fallback loaded.
const sshdOn22 = `listen tcp LISTEN 0 4096 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=812,fd=3))` + "\n" +
	`listen tcp LISTEN 0 4096 [::]:22 [::]:* users:(("sshd",pid=812,fd=4))` + "\n" +
	"ipv6 yes\nsystemd-run present\nfallback not-found not-found\nend\n"

// sshdOn22And2222 adds 2222, which a socket-activated ssh.socket listens
// on for sshd.
const sshdOn22And2222 = "socket [::]:2222 (Stream)\n" + sshdOn22

// noSSHD is the probe on a host where nothing listens for sshd.
const noSSHD = "ipv6 yes\nsystemd-run present\nfallback not-found not-found\nend\n"

// inactiveUFW is home() with ufw installed and not active, and the probe
// before enabling it answered with answer.
func inactiveUFW(answer string) func() *fakeHost {
	return func() *fakeHost { return home().with("ufw-active", "no").answer(ufwBeforeMarker, answer) }
}

var enableUFW = hostadmit.Flags{EnableUFW: true}

var ufwCases = addCases(
	goldenCase{name: "firewall-ufw-inactive-two-ports-refused", site: "vm", host: inactiveUFW(sshdOn22And2222)},
	goldenCase{name: "firewall-ufw-inactive-no-sshd-refused", site: "vm", host: inactiveUFW(noSSHD)},
	goldenCase{name: "firewall-ufw-owner-deny-refused", site: "vm", host: inactiveUFW("rule deny in on eth0 to any port 22\nrule reject 22:23/tcp\n" + sshdOn22And2222)},
	goldenCase{name: "firewall-enable-ufw-ipv6-off-refused", site: "vm", flags: enableUFW, host: inactiveUFW(strings.Replace(sshdOn22, "ipv6 yes", "ipv6 no", 1))},
	goldenCase{name: "firewall-enable-ufw", site: "vm", flags: enableUFW, host: inactiveUFW(sshdOn22And2222)},
	goldenCase{name: "firewall-enable-ufw-owner-allow", focus: []string{"allow", "ufw"}, site: "vm", flags: enableUFW, host: func() *fakeHost {
		return inactiveUFW("rule allow 22/tcp\n"+sshdOn22)().answer("ufw show added", "ufw present\nstatus inactive\nrule allow 22/tcp\n")
	}},
)

func TestEnableUFWPlans(t *testing.T) { runGolden(t, ufwCases) }

// Without --enable-ufw an inactive ufw refuses, naming the flag first, then
// the commands that enable it by hand in their order, each allow before the
// enable, and what enabling without them does. With no port sshd listens
// on read, no enable command is given at all
// (TestARefusalTheFlagWouldMeetIsGivenAsItIs).
func TestAnInactiveUFWRefusesNamingTheFlagAndTheSafeOrder(t *testing.T) {
	for _, c := range []struct {
		name, answer string
		want         []string
	}{
		{"one port", sshdOn22, []string{"--enable-ufw", "  1. sudo ufw allow 22/tcp\n  2. sudo ufw enable\n", "without step 1 cuts off SSH"}},
		{"two ports", sshdOn22And2222, []string{"--enable-ufw", "  1. sudo ufw allow 22/tcp\n  2. sudo ufw allow 2222/tcp\n  3. sudo ufw enable\n", "without steps 1 and 2 cuts off SSH"}},
	} {
		_, err := hostadmit.Build(inactiveUFW(c.answer)(), input(t, "vm", hostadmit.Flags{}))
		var inactive *hostadmit.UFWInactiveError
		if !errors.As(err, &inactive) {
			t.Fatalf("%s: %v", c.name, err)
		}
		text := err.Error()
		for _, w := range c.want {
			if !strings.Contains(text, w) {
				t.Errorf("%s: the refusal does not say %q:\n%s", c.name, w, text)
			}
		}
		if strings.Index(text, "--enable-ufw") > strings.Index(text, "sudo ufw") {
			t.Errorf("%s: the refusal names --enable-ufw after the commands:\n%s", c.name, text)
		}
		if len(inactive.SSHPorts) == 0 && strings.Contains(text, "ufw enable") {
			t.Errorf("%s: an enable command with no SSH port to allow:\n%s", c.name, text)
		}
	}
}

// titled is the index of the step titled title, -1 for none.
func titledStep(plan *hostadmit.Plan, title string) int {
	return slices.IndexFunc(plan.Steps, func(s hostadmit.Step) bool { return s.Title == title })
}

// With --enable-ufw the plan ends with an allow for every port sshd listens
// on, after the deployment's own rules, then the fallback armed, then ufw
// enabled; the deployment's own allow of 22 is left to ours, which evict
// keeps, and the record says admit enabled ufw.
func TestEnableUFWPlansTheAllowsTheFallbackThenTheEnable(t *testing.T) {
	in := input(t, "vm", enableUFW)
	plan, err := hostadmit.Build(inactiveUFW(sshdOn22And2222)(), in)
	if err != nil {
		t.Fatal(err)
	}
	a22, a2222 := titledStep(plan, "allow SSH on 22/tcp"), titledStep(plan, "allow SSH on 2222/tcp")
	arm, enable := titledStep(plan, "arm the ufw fallback"), titledStep(plan, "enable ufw")
	if a22 < 0 || a2222 < 0 || !(a22 < arm && a2222 < arm && arm < enable) {
		t.Fatalf("the steps are out of order (%d %d %d %d):\n%s", a22, a2222, arm, enable, plan.Text())
	}
	for i, s := range plan.Steps {
		if strings.HasPrefix(s.Command, "ufw allow") && !strings.Contains(s.Command, "'paisans: ssh'") && i > a22 {
			t.Errorf("a deployment rule comes after the SSH allows: %s", s.Command)
		}
		if strings.HasPrefix(s.Command, "ufw allow 22/tcp comment 'paisans-f2a9:") {
			t.Errorf("the deployment's own allow of 22 is planned beside ours: %s", s.Command)
		}
	}
	if plan.Steps[a22].Command != "ufw allow 22/tcp comment 'paisans: ssh'" ||
		plan.Steps[arm].Command != "systemd-run --on-active=600 --timer-property=AccuracySec=1s --unit=paisans-ufw-fallback ufw disable" ||
		plan.Steps[enable].Command != "ufw --force enable" {
		t.Errorf("the commands:\n%s", plan.Text())
	}
	if !plan.EnablesUFW || !recordWritten(t, plan).UFWEnabled {
		t.Errorf("EnablesUFW %v, the record's ufw_enabled %v", plan.EnablesUFW, recordWritten(t, plan).UFWEnabled)
	}
}

// --enable-ufw refuses, and enables nothing, when it cannot tell which
// ports sshd listens on, when one of them is denied by the owner's rule,
// or when the fallback cannot be armed.
func TestEnableUFWRefusesWhatWouldEnableItBlind(t *testing.T) {
	for name, c := range map[string]struct{ answer, want string }{
		"no sshd port":       {noSSHD, "found no port sshd listens on"},
		"listeners unread":   {"unread listeners (ss -Hltnp)\n" + sshdOn22, "could not read the listeners"},
		"rules unread":       {"unread rules (ufw show added)\n" + sshdOn22, "could not read the rules"},
		"no systemd-run":     {strings.Replace(sshdOn22, "systemd-run present", "systemd-run absent", 1), "cannot arm the timer"},
		"fallback loaded":    {strings.Replace(sshdOn22, "fallback not-found not-found", "fallback loaded not-found", 1), "already loaded"},
		"fallback unread":    {strings.Replace(sshdOn22, "fallback not-found not-found", "fallback", 1), "already loaded"},
		"owner denies 22":    {"rule deny 22/tcp\n" + sshdOn22, "`ufw deny 22/tcp`"},
		"owner rejects 2222": {"rule reject 2222\n" + sshdOn22And2222, "`ufw reject 2222`"},
	} {
		_, err := hostadmit.Build(inactiveUFW(c.answer)(), input(t, "vm", enableUFW))
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "Nothing was changed") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// On a host whose ufw is active, --enable-ufw plans nothing more than admit
// without it, and says so; the record does not say admit enabled ufw.
func TestEnableUFWOnAnActiveUFWPlansNothingNew(t *testing.T) {
	without, err := hostadmit.Build(home(), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	with, err := hostadmit.Build(home(), input(t, "vm", enableUFW))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.EqualFunc(without.Steps, with.Steps, func(a, b hostadmit.Step) bool { return a.Title == b.Title && a.Command == b.Command }) {
		t.Errorf("--enable-ufw on an active ufw changed the plan:\n%s", with.Text())
	}
	if with.EnablesUFW || recordWritten(t, with).UFWEnabled || !slices.Contains(with.Present, "firewall: ufw is active already, so --enable-ufw changes nothing") {
		t.Errorf("EnablesUFW %v:\n%s", with.EnablesUFW, with.Text())
	}
}

// ufwEnabledHost is site admitted with --enable-ufw on a host whose ufw was
// inactive, ufw active since.
func ufwEnabledHost(site string) *fakeHost {
	return admittedOn(inactiveUFW(sshdOn22)(), requestFor(site), enableUFW).with("ufw-active", "yes")
}

// A re-run once admit has enabled ufw plans nothing, with --enable-ufw or
// without it, and keeps the record saying admit enabled it.
func TestARerunAfterEnablingUFWPlansNothing(t *testing.T) {
	for _, flags := range []hostadmit.Flags{enableUFW, {}} {
		h := ufwEnabledHost("vm")
		plan, err := hostadmit.Build(h, input(t, "vm", flags))
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Steps) != 0 {
			t.Errorf("%+v: a second run plans:\n%s", flags, plan.Text())
		}
		rec, err := hostadmit.RecordOf(h, input(t, "vm", flags))
		if err != nil || !rec.UFWEnabled {
			t.Errorf("%+v: the record forgets admit enabled ufw: %v", flags, err)
		}
	}
}

// Execute reads the host again at its first provisional step: when its own
// enable ran before that, ufw is active by then, and the plan it rebuilds
// still records that admit enabled it.
func TestARebuildAfterTheEnableRecordsIt(t *testing.T) {
	in := hostadmit.EnabledUFWByThisRun(input(t, "vm", enableUFW))
	rec, err := hostadmit.RecordOf(home(), in)
	if err != nil || !rec.UFWEnabled {
		t.Errorf("the rebuilt record: %+v, %v", rec, err)
	}
}

// Evict never disables ufw and never removes the paisans: ssh allow: its
// plan says ufw stays enabled because admit turned it on.
func TestEvictKeepsUFWAndTheSSHAllow(t *testing.T) {
	h := ufwEnabledHost("vm").with("mesh-unit", "enabled active").withFile("/etc/wireguard/psns-f2a9.conf", "[Interface]\n", "600", 0)
	plan, err := hostadmit.BuildEvict(h, hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Steps {
		if strings.Contains(s.Command, "paisans: ssh") || strings.Contains(s.Command, "ufw disable") || strings.Contains(s.Command, "ufw --force") {
			t.Errorf("evict touches ufw's state or the owner's SSH allow: %s", s.Command)
		}
	}
	if !slices.ContainsFunc(plan.Warnings, func(w hostprep.Warning) bool { return strings.Contains(w.Hint, "ufw stays as it is") }) {
		t.Errorf("evict does not say ufw stays as it is:\n%s", plan.Text())
	}
}

// Any deny or reject rule of the owner's, in whatever form, stops admit
// enabling ufw: ufw matches the first rule, and admit neither parses every
// form nor overrides the owner's rules. The refusal lists them, and the
// commands to enable ufw by hand once they are checked, each allow before
// the enable. With the flag or without, at a terminal or not, nothing asks
// to enable ufw then.
func TestAnOwnersDenyInAnyFormStopsTheEnable(t *testing.T) {
	for _, rule := range []string{"deny in on eth0 to any port 22", "reject 22:23/tcp", "deny 22,80/tcp", "deny OpenSSH", "deny from 203.0.113.0/24"} {
		for _, flags := range []hostadmit.Flags{enableUFW, {}} {
			_, err := hostadmit.Build(inactiveUFW("rule "+rule+"\n"+sshdOn22)(), input(t, "vm", flags))
			var inactive *hostadmit.UFWInactiveError
			if err == nil || errors.As(err, &inactive) {
				t.Fatalf("%q %+v: %v", rule, flags, err)
			}
			for _, want := range []string{"`ufw " + rule + "`", "decide what reaches SSH", "  1. sudo ufw allow 22/tcp\n  2. sudo ufw enable", "Nothing was changed"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%q %+v: the refusal does not say %q:\n%v", rule, flags, want, err)
				}
			}
			if strings.Contains(err.Error(), "safe way") {
				t.Errorf("%q: the refusal calls --enable-ufw the safe way:\n%v", rule, err)
			}
		}
	}
	// An allow, a limit and a route rule are no deny.
	for _, rule := range []string{"allow 80/tcp", "limit 22/tcp", "route deny in on eth1"} {
		if _, err := hostadmit.Build(inactiveUFW("rule "+rule+"\n"+sshdOn22)(), input(t, "vm", enableUFW)); err != nil {
			t.Errorf("%q: %v", rule, err)
		}
	}
}

// With IPV6 other than yes in /etc/default/ufw, enabling ufw drops all IPv6,
// SSH over IPv6 included: admit refuses when sshd listens on IPv6, and gives
// no command that enables ufw. With sshd on IPv4 alone it enables.
func TestIPv6OffStopsTheEnableWhenSSHDListensOnIPv6(t *testing.T) {
	v6 := strings.Replace(sshdOn22, "ipv6 yes", "ipv6 no", 1) // sshdOn22 listens on [::]:22
	for _, answer := range []string{v6, "socket [::]:22 (Stream)\nipv6 no\nsystemd-run present\nfallback not-found not-found\nend\n",
		`listen tcp LISTEN 0 4096 *:22 *:* users:(("sshd",pid=1,fd=3))` + "\n" + "ipv6\nsystemd-run present\nfallback not-found not-found\nend\n"} {
		for _, flags := range []hostadmit.Flags{enableUFW, {}} {
			_, err := hostadmit.Build(inactiveUFW(answer)(), input(t, "vm", flags))
			if err == nil || !strings.Contains(err.Error(), "IPV6=yes") || !strings.Contains(err.Error(), "/etc/default/ufw") || !strings.Contains(err.Error(), "SSH over IPv6") || strings.Contains(err.Error(), "sudo ufw enable") {
				t.Errorf("%q %+v: %v", answer, flags, err)
			}
		}
	}
	v4 := `listen tcp LISTEN 0 4096 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=812,fd=3))` + "\nipv6 no\nsystemd-run present\nfallback not-found not-found\nend\n"
	if _, err := hostadmit.Build(inactiveUFW(v4)(), input(t, "vm", enableUFW)); err != nil {
		t.Errorf("sshd on IPv4 alone with IPV6=no: %v", err)
	}
}

// Off a terminal, a refusal that --enable-ufw would meet too is given as
// it is, never as the question's refusal calling the flag the safe way.
func TestARefusalTheFlagWouldMeetIsGivenAsItIs(t *testing.T) {
	for _, answer := range []string{noSSHD, strings.Replace(sshdOn22, "systemd-run present", "systemd-run absent", 1),
		strings.Replace(sshdOn22, "fallback not-found not-found", "fallback loaded not-found", 1), "unread rules (ufw show added)\n" + sshdOn22} {
		_, err := hostadmit.Build(inactiveUFW(answer)(), input(t, "vm", hostadmit.Flags{}))
		var inactive *hostadmit.UFWInactiveError
		if err == nil || errors.As(err, &inactive) || strings.Contains(err.Error(), "safe way") {
			t.Errorf("%q: %v", answer, err)
		}
	}
}

// An owner's limit on an sshd port lets SSH in: no allow of admit's for it.
// An application profile's allow is not read, so admit adds its own, which
// is harmless.
func TestALimitOrAProfileAllowOnTheSSHPort(t *testing.T) {
	plan, err := hostadmit.Build(inactiveUFW("rule limit 22/tcp comment 'SSH (rate-limited)'\n"+sshdOn22)(), input(t, "vm", enableUFW))
	if err != nil {
		t.Fatal(err)
	}
	if titledStep(plan, "allow SSH on 22/tcp") >= 0 || !slices.Contains(plan.Present, "firewall: SSH on 22/tcp allowed by `ufw limit 22/tcp comment 'SSH (rate-limited)'`") {
		t.Errorf("a limit on 22:\n%s", plan.Text())
	}
	plan, err = hostadmit.Build(inactiveUFW("rule allow OpenSSH\n"+sshdOn22)(), input(t, "vm", enableUFW))
	if err != nil || titledStep(plan, "allow SSH on 22/tcp") < 0 {
		t.Errorf("allow OpenSSH: %v", err)
	}
}

// The fallback fires on time, and the plan says it does not survive a
// reboot.
func TestTheFallbackIsAccurateAndSaysItDoesNotSurviveAReboot(t *testing.T) {
	plan, err := hostadmit.Build(inactiveUFW(sshdOn22)(), input(t, "vm", enableUFW))
	if err != nil {
		t.Fatal(err)
	}
	arm := plan.Steps[titledStep(plan, "arm the ufw fallback")]
	if arm.Command != "systemd-run --on-active=600 --timer-property=AccuracySec=1s --unit=paisans-ufw-fallback ufw disable" || !strings.Contains(arm.Describe, "does not survive a reboot") {
		t.Errorf("the fallback: %+v", arm)
	}
}

// The plan and the question say what admit cannot see: drop rules written
// by hand in ufw's own rule files, and ways in that are not sshd.
func TestTheEnableSaysWhatAdmitCannotSee(t *testing.T) {
	plan, err := hostadmit.Build(inactiveUFW(sshdOn22)(), input(t, "vm", enableUFW))
	if err != nil {
		t.Fatal(err)
	}
	d := plan.Steps[titledStep(plan, "enable ufw")].Describe
	for _, want := range []string{"only the TCP ports sshd listens on", "/etc/ufw/before.rules", "before6.rules", "user.rules", "sslh", "dropbear", "mosh", "enable ufw by hand"} {
		if !strings.Contains(d, want) {
			t.Errorf("the enable step does not say %q:\n%s", want, d)
		}
	}
}

// Only root's sshd listeners off loopback are SSH ports to allow: a
// session's X11 or remote forward, held by a per-session sshd as the
// logged-in user or on loopback, and any process a user names sshd, are not.
// ss -e prints uid:N for every owner but root.
func TestOnlyRootsSSHDListenersOffLoopbackAreAllowed(t *testing.T) {
	answer := `listen tcp LISTEN 0 128 127.0.0.1:6010 0.0.0.0:* users:(("sshd",pid=4410,fd=9)) uid:1000 ino:91 sk:3` + "\n" +
		`listen tcp LISTEN 0 128 [::1]:6010 [::]:* users:(("sshd",pid=4410,fd=10)) uid:1000 ino:92 sk:4` + "\n" +
		`listen tcp LISTEN 0 128 127.0.0.1:6011 0.0.0.0:* users:(("sshd",pid=4411,fd=9)) ino:93 sk:5` + "\n" +
		`listen tcp LISTEN 0 128 0.0.0.0:2200 0.0.0.0:* users:(("sshd",pid=4500,fd=3)) uid:1001 ino:94 sk:6` + "\n" +
		sshdOn22
	h := inactiveUFW(answer)()
	_, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	var inactive *hostadmit.UFWInactiveError
	if !errors.As(err, &inactive) || !slices.Equal(inactive.SSHPorts, []int{22}) {
		t.Fatalf("ports %v: %v", inactive, err)
	}
	for _, c := range h.ran {
		if strings.Contains(c, ufwBeforeMarker) && !strings.Contains(c, "ss -Hltunpe") {
			t.Errorf("the probe does not read the listeners' owners: %s", c)
		}
	}
}

// ownerCaddy is the owner's Caddy on the host network, run as root,
// answering on 80 and 443 over TCP and on 443 over UDP for HTTP/3.
const ownerCaddy = `listen tcp LISTEN 0 4096 *:80 *:* users:(("caddy",pid=900,fd=7)) ino:11 sk:1` + "\n" +
	`listen tcp LISTEN 0 4096 *:443 *:* users:(("caddy",pid=900,fd=8)) ino:12 sk:2` + "\n" +
	`listen udp UNCONN 0 0 *:443 *:* users:(("caddy",pid=900,fd=9)) ino:13 sk:3` + "\n"

// blockListed is --enable-ufw with --enable-ufw-block-listed.
var blockListed = hostadmit.Flags{EnableUFW: true, EnableUFWBlockListed: true}

var ufwListenerCases = addCases(
	goldenCase{name: "firewall-enable-ufw-owner-caddy-refused", site: "vm", flags: enableUFW, host: inactiveUFW(ownerCaddy + sshdOn22)},
	goldenCase{name: "firewall-enable-ufw-owner-caddy-allowed", focus: []string{"enable ufw"}, site: "vm", flags: enableUFW,
		host: inactiveUFW(ownerRule("allow 'Nginx Full'", "allow tcp 80,443 0.0.0.0/0 any 0.0.0.0/0 Nginx%20Full - in") +
			ownerRule("allow 443/udp", "allow udp 443 0.0.0.0/0 any 0.0.0.0/0 in") + ownerCaddy + sshdOn22)},
	goldenCase{name: "firewall-enable-ufw-block-listed", focus: []string{"enable ufw"}, site: "vm", flags: blockListed, host: inactiveUFW(ownerCaddy + sshdOn22)},
	goldenCase{name: "firewall-ufw-inactive-owner-caddy-refused", site: "vm", host: inactiveUFW(ownerCaddy + sshdOn22)},
)

func TestEnableUFWListenerPlans(t *testing.T) { runGolden(t, ufwListenerCases) }

// blocks is the refusal of --enable-ufw on a host whose ufw is inactive
// and the probe before enabling it answered with answer, nil when admit
// plans the enable.
func blocks(t *testing.T, answer string) *hostadmit.UFWBlocksError {
	t.Helper()
	_, err := hostadmit.Build(inactiveUFW(answer)(), input(t, "vm", enableUFW))
	var blocked *hostadmit.UFWBlocksError
	if err != nil && !errors.As(err, &blocked) {
		t.Fatalf("not a refusal of what enabling ufw blocks: %v", err)
	}
	return blocked
}

// The owner's Caddy on 80 and 443, with no allow of the owner's, stops
// --enable-ufw before anything changes: the refusal names what answers on
// each port and gives one allow line per port, then says what to do with
// a service meant to stay unreachable.
func TestEnableUFWRefusesToBlockTheOwnersCaddy(t *testing.T) {
	b := blocks(t, ownerCaddy+sshdOn22)
	if b == nil {
		t.Fatal("admit plans to enable ufw in front of the owner's Caddy")
	}
	text := b.Error()
	for _, want := range []string{
		"80/tcp: caddy (pid 900, uid 0) listens on *:80",
		"443/tcp: caddy (pid 900, uid 0) listens on *:443",
		"443/udp: caddy (pid 900, uid 0) listens on *:443/udp",
		"  sudo ufw allow 80/tcp\n  sudo ufw allow 443/tcp\n  sudo ufw allow 443/udp\n",
		"then run admit again",
		"such as a database on a public address, needs nothing",
		"--enable-ufw-block-listed",
		"Nothing was changed",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "22/tcp:") || strings.Contains(text, "allow 22/tcp") {
		t.Errorf("the refusal lists sshd's port, which admit allows itself:\n%s", text)
	}
}

// ownerRule is one rule of the owner's as the probe reads it: show as
// `ufw show added` prints it, and tuple as ufw records it in user.rules
// and, with its addresses as IPv6's, in user6.rules.
func ownerRule(show, tuple string) string {
	return "rule " + show + "\ntuple4 " + tuple + "\ntuple6 " + strings.ReplaceAll(tuple, "0.0.0.0/0", "::/0") + "\n"
}

// The owner's own allows, added while ufw is inactive, keep the Caddy
// reachable, whatever form ufw records them in: admit plans the enable,
// and says under it what stays reachable and why.
func TestEnableUFWProceedsOverTheOwnersAllows(t *testing.T) {
	for _, rules := range []string{
		ownerRule("allow 80/tcp", "allow tcp 80 0.0.0.0/0 any 0.0.0.0/0 in") + ownerRule("allow 443", "allow any 443 0.0.0.0/0 any 0.0.0.0/0 in"),
		ownerRule("allow 'Nginx Full'", "allow tcp 80,443 0.0.0.0/0 any 0.0.0.0/0 Nginx%20Full - in") + ownerRule("allow 443/udp comment 'web'", "allow udp 443 0.0.0.0/0 any 0.0.0.0/0 in comment=776562"),
		ownerRule("allow in on eth0 to any port 80 proto tcp", "allow tcp 80 0.0.0.0/0 any 0.0.0.0/0 in_eth0") + ownerRule("limit log 443", "limit_log any 443 0.0.0.0/0 any 0.0.0.0/0 in"),
		ownerRule("allow 1:1000/tcp", "allow tcp 1:1000 0.0.0.0/0 any 0.0.0.0/0 in") + ownerRule("allow 1:1000/udp", "allow udp 1:1000 0.0.0.0/0 any 0.0.0.0/0 in"),
	} {
		plan, err := hostadmit.Build(inactiveUFW(rules+ownerCaddy+sshdOn22)(), input(t, "vm", enableUFW))
		if err != nil {
			t.Errorf("%q: %v", rules, err)
			continue
		}
		d := plan.Steps[titledStep(plan, "enable ufw")].Describe
		for _, want := range []string{"Stays reachable", "80/tcp (caddy (pid 900, uid 0) listens on *:80), allowed by your rule `ufw ", "22/tcp (sshd (pid 812, uid 0) listens on 0.0.0.0:22", "sshd's"} {
			if !strings.Contains(d, want) {
				t.Errorf("%q: the enable step does not say %q:\n%s", rules, want, d)
			}
		}
		if len(plan.UFWBlocks) != 0 {
			t.Errorf("%q: the plan blocks %q", rules, plan.UFWBlocks)
		}
	}
	// A rule narrowed to a source, of another protocol, outbound or
	// routed, the deployment's own interface rule, and a rule ufw holds
	// for IPv4 alone, which leaves the Caddy's IPv6 blocked, keep nothing
	// reachable.
	allow443 := ownerRule("allow 443", "allow any 443 0.0.0.0/0 any 0.0.0.0/0 in")
	for _, rules := range []string{
		ownerRule("allow from 203.0.113.0/24 to any port 80", "allow any 80 0.0.0.0/0 any 203.0.113.0/24 in"),
		ownerRule("allow 80/udp", "allow udp 80 0.0.0.0/0 any 0.0.0.0/0 in"),
		ownerRule("allow out 80/tcp", "allow tcp 80 0.0.0.0/0 any 0.0.0.0/0 out"),
		ownerRule("route allow 80/tcp", "route:allow tcp 80 0.0.0.0/0 any 0.0.0.0/0 in"),
		ownerRule("allow in on psns-f2a9", "allow any any 0.0.0.0/0 any 0.0.0.0/0 in_psns-f2a9"),
		"rule allow 80\ntuple4 allow any 80 0.0.0.0/0 any 0.0.0.0/0 in\n",
	} {
		b := blocks(t, rules+allow443+ownerCaddy+sshdOn22)
		if b == nil || !strings.Contains(b.Error(), "80/tcp: caddy") && !strings.Contains(b.Error(), "80/tcp over IPv6: caddy") {
			t.Errorf("%q: %v", rules, b)
		}
	}
	// A rule to the address a listener is bound to keeps it reachable.
	app := `listen tcp LISTEN 0 4096 203.0.113.5:8080 0.0.0.0:* users:(("app",pid=1,fd=3))` + "\n"
	if b := blocks(t, "rule allow to 203.0.113.5 port 8080\ntuple4 allow any 8080 203.0.113.5 any 0.0.0.0/0 in\n"+app+sshdOn22); b != nil {
		t.Errorf("a rule to the listener's address: %v", b)
	}
	if b := blocks(t, "rule allow to 203.0.113.6 port 8080\ntuple4 allow any 8080 203.0.113.6 any 0.0.0.0/0 in\n"+app+sshdOn22); b == nil {
		t.Error("a rule to another address keeps the listener reachable")
	}
}

// shopWeb is a container the system Docker publishes on 8443 with its
// userland proxy off: no listener, only its publish and a DNAT rule to
// the container's 443.
const shopWeb = "container shop-web-1\t0.0.0.0:8443->443/tcp, [::]:8443->443/tcp\tshop\n" +
	"dnat table ip nat chain DOCKER: iifname != \"docker0\" tcp dport 8443 counter packets 0 bytes 0 dnat to 172.17.0.2:443\n"

// What NAT receives is routed traffic, which ufw decides after
// translation: it is listed, with the `ufw route allow` line for the
// address and port it is translated to and no `ufw allow` line, and an
// owner's `ufw allow` of the published port keeps nothing reachable.
func TestEnableUFWListsAPortPublishedThroughNAT(t *testing.T) {
	for _, answer := range []string{shopWeb, "docker-backend \"firewall-backend\": \"nftables\"\ndocker-chains nft\n" + shopWeb,
		ownerRule("allow 8443/tcp", "allow tcp 8443 0.0.0.0/0 any 0.0.0.0/0 in") + shopWeb} {
		b := blocks(t, answer+sshdOn22)
		if b == nil {
			t.Fatalf("%q: admit plans to enable ufw in front of a published container", answer)
		}
		text := b.Error()
		for _, want := range []string{"8443/tcp: your Docker's container shop-web-1 publishes", "a NAT rule in table ip nat chain DOCKER",
			"  sudo ufw route allow proto tcp to 172.17.0.2 port 443\n", "routed", "Check each from off this host once ufw is on"} {
			if !strings.Contains(text, want) {
				t.Errorf("%q: the refusal does not say %q:\n%s", answer, want, text)
			}
		}
		if strings.Contains(text, "sudo ufw allow 8443") {
			t.Errorf("%q: an INPUT allow for a routed port:\n%s", answer, text)
		}
	}
	// The owner's route allow to where it is translated keeps it reachable.
	plan, err := hostadmit.Build(inactiveUFW(ownerRule("route allow proto tcp to 172.17.0.2 port 443", "route:allow tcp 443 172.17.0.2 any 0.0.0.0/0 in")+shopWeb+sshdOn22)(), input(t, "vm", enableUFW))
	if err != nil || !strings.Contains(plan.Steps[titledStep(plan, "enable ufw")].Describe, "8443/tcp (your Docker's container shop-web-1") {
		t.Errorf("a route allow: %v", err)
	}
	// A stopped container that restarts with the Docker takes its port
	// again, to an address admit cannot read: no line. A NAT rule whose
	// ports admit cannot read is listed with no line.
	b := blocks(t, `stopped /shop-db-1 always {"5432/tcp":[{"HostIp":"","HostPort":"15432"}]}`+"\n"+
		"dnat table ip nat chain PREROUTING: tcp dport @web dnat to 10.0.0.2\n"+sshdOn22)
	if b == nil || !strings.Contains(b.Error(), "15432/tcp: your Docker's stopped container shop-db-1") || !strings.Contains(b.Error(), "tcp dport @web") || strings.Contains(b.Error(), "sudo ufw") {
		t.Errorf("a stopped container and an unreadable rule: %v", b)
	}
	// A redirect reaches a port of the host's own, whose listener is read
	// as any other.
	if b := blocks(t, "dnat table ip nat chain PREROUTING: tcp dport 80 redirect to :8080\n"+sshdOn22); b != nil {
		t.Errorf("a redirect: %v", b)
	}
}

// With Docker's iptables backend, its own chains come ahead of ufw's in
// FORWARD, so what it publishes stays reachable: not listed, and named in
// the plan with why. Docker decides its backend from daemon.json first.
func TestDockersIptablesBackendKeepsItsPublishesReachable(t *testing.T) {
	proxy := `listen tcp LISTEN 0 4096 0.0.0.0:8443 0.0.0.0:* users:(("docker-proxy",pid=3001,fd=7))` + "\n"
	for _, chains := range []string{"docker-chains nft\n", "docker-chains iptables-save\n", "docker-backend \"firewall-backend\": \"iptables\"\n"} {
		plan, err := hostadmit.Build(inactiveUFW(chains+proxy+shopWeb+
			`stopped /shop-db-1 always {"5432/tcp":[{"HostIp":"","HostPort":"15432"}]}`+"\n"+sshdOn22)(), input(t, "vm", enableUFW))
		if err != nil {
			t.Errorf("%q: %v", chains, err)
			continue
		}
		d := plan.Steps[titledStep(plan, "enable ufw")].Describe
		for _, want := range []string{"8443/tcp (your Docker's container shop-web-1", "15432/tcp (your Docker's stopped container shop-db-1", "your Docker's own iptables rules accept it ahead of ufw's"} {
			if !strings.Contains(d, want) {
				t.Errorf("%q: the enable step does not say %q:\n%s", chains, want, d)
			}
		}
	}
	// A NAT rule outside Docker's chains is routed traffic ufw decides.
	if b := blocks(t, "docker-chains nft\ndnat table ip nat chain PREROUTING: tcp dport 2222 dnat to 192.168.122.5:22\n"+sshdOn22); b == nil || !strings.Contains(b.Error(), "sudo ufw route allow proto tcp to 192.168.122.5 port 22") {
		t.Errorf("a libvirt guest's forward: %v", b)
	}
}

// A listener on an IPv6 address alone is listed as any other.
func TestEnableUFWListsAnIPv6OnlyListener(t *testing.T) {
	b := blocks(t, `listen tcp LISTEN 0 4096 [2001:db8::5]:8080 [::]:* users:(("app",pid=1200,fd=3)) uid:1002 ino:1 sk:1`+"\n"+sshdOn22)
	if b == nil || !strings.Contains(b.Error(), "8080/tcp: app (pid 1200, uid 1002) listens on [2001:db8::5]:8080") || !strings.Contains(b.Error(), "sudo ufw allow 8080/tcp") {
		t.Errorf("an IPv6-only listener: %v", b)
	}
	// With ufw's IPv6 off, an allow does not keep it reachable: the
	// refusal says to set IPV6=yes, with or without an allow.
	b = blocks(t, ownerRule("allow 8080/tcp", "allow tcp 8080 0.0.0.0/0 any 0.0.0.0/0 in")+`listen tcp LISTEN 0 4096 [2001:db8::5]:8080 [::]:* users:(("app",pid=1200,fd=3)) uid:1002 ino:1 sk:1`+"\n"+
		`listen tcp LISTEN 0 4096 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=812,fd=3))`+"\nipv6 no\nsystemd-run present\nfallback not-found not-found\nend\n")
	if b == nil || !strings.Contains(b.Error(), "over IPv6") || !strings.Contains(b.Error(), "IPV6=yes") {
		t.Errorf("an IPv6 listener with ufw's IPv6 off: %v", b)
	}
}

// What only loopback reaches, which ufw never filters, what ufw's own
// before.rules let in (a DHCP client's port), the deployment's own
// WireGuard port and its own user's listeners are not listed.
func TestEnableUFWIgnoresWhatStaysReachableOrIsLocal(t *testing.T) {
	answer := `listen tcp LISTEN 0 4096 127.0.0.1:5432 0.0.0.0:* users:(("postgres",pid=700,fd=5)) uid:113 ino:1 sk:1` + "\n" +
		`listen tcp LISTEN 0 4096 [::1]:6379 [::]:* users:(("redis-server",pid=701,fd=6)) uid:114 ino:2 sk:2` + "\n" +
		`listen tcp LISTEN 0 4096 [::ffff:127.0.0.1]:9000 *:* users:(("app",pid=702,fd=6)) ino:3 sk:3` + "\n" +
		`listen udp UNCONN 0 0 127.0.0.53%lo:53 0.0.0.0:* users:(("systemd-resolve",pid=600,fd=13)) uid:991 ino:4 sk:4` + "\n" +
		`listen udp UNCONN 0 0 127.0.0.54:53 0.0.0.0:* users:(("systemd-resolve",pid=600,fd=14)) uid:991 ino:5 sk:5` + "\n" +
		`listen udp UNCONN 0 0 10.0.0.5%eth0:68 0.0.0.0:* users:(("systemd-network",pid=500,fd=20)) uid:998 ino:6 sk:6` + "\n" +
		`listen udp UNCONN 0 0 0.0.0.0:51820 0.0.0.0:* ino:7 sk:7` + "\n" +
		`listen tcp LISTEN 0 4096 0.0.0.0:2375 0.0.0.0:* users:(("rootlesskit",pid=703,fd=6)) uid:1001 ino:8 sk:8` + "\n" +
		"container shop-db-1\t127.0.0.1:15432->5432/tcp\tshop\n" +
		"dnat iptables-save -t nat: -A DOCKER -d 127.0.0.1/32 ! -i docker0 -p tcp -m tcp --dport 15432 -j DNAT --to-destination 172.17.0.2:5432\n" +
		sshdOn22
	if b := blocks(t, answer); b != nil {
		t.Errorf("admit refuses over what stays reachable or is local:\n%v", b)
	}
}

// With --enable-ufw-block-listed admit enables ufw over what it would
// block, and the plan names each port it blocks.
func TestTheBlockListedFlagEnablesAndNamesWhatItBlocks(t *testing.T) {
	plan, err := hostadmit.Build(inactiveUFW(ownerCaddy+sshdOn22)(), input(t, "vm", blockListed))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.EnablesUFW || !slices.Equal(plan.UFWBlocks, []string{"80/tcp", "443/tcp", "443/udp"}) {
		t.Errorf("EnablesUFW %v, blocks %q", plan.EnablesUFW, plan.UFWBlocks)
	}
	d := plan.Steps[titledStep(plan, "enable ufw")].Describe
	for _, want := range []string{"Blocks, as --enable-ufw-block-listed accepts", "80/tcp (caddy (pid 900, uid 0) listens on *:80)", "443/udp"} {
		if !strings.Contains(d, want) {
			t.Errorf("the enable step does not say %q:\n%s", want, d)
		}
	}
	// Without --enable-ufw the flag enables nothing.
	_, err = hostadmit.Build(inactiveUFW(ownerCaddy+sshdOn22)(), input(t, "vm", hostadmit.Flags{EnableUFWBlockListed: true}))
	var inactive *hostadmit.UFWInactiveError
	if !errors.As(err, &inactive) {
		t.Errorf("the flag alone: %v", err)
	}
}

// Without --enable-ufw the refusal of an inactive ufw names what enabling
// it would block too, with the allow lines.
func TestTheInactiveRefusalNamesWhatEnablingBlocks(t *testing.T) {
	_, err := hostadmit.Build(inactiveUFW(ownerCaddy+sshdOn22)(), input(t, "vm", hostadmit.Flags{}))
	var inactive *hostadmit.UFWInactiveError
	if !errors.As(err, &inactive) || len(inactive.Blocked) != 3 {
		t.Fatalf("%v", err)
	}
	for _, want := range []string{"80/tcp: caddy", "sudo ufw allow 443/udp", "--enable-ufw-block-listed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%s", want, err)
		}
	}
}

// What admit cannot read before enabling ufw stops it: it never enables
// ufw without knowing what it blocks.
func TestEnableUFWRefusesWhatItCannotRead(t *testing.T) {
	for _, unread := range []string{"unread containers (ps of the system Docker)", "unread ruleset (nft list ruleset)"} {
		_, err := hostadmit.Build(inactiveUFW(unread+"\n"+sshdOn22)(), input(t, "vm", blockListed))
		if err == nil || !strings.Contains(err.Error(), "could not read the "+strings.TrimPrefix(unread, "unread ")) || !strings.Contains(err.Error(), "Nothing was changed") {
			t.Errorf("%s: %v", unread, err)
		}
	}
}

// On a host whose ufw is active, admit reads none of this: it adds its own
// rules alone, whatever listens.
func TestAnActiveUFWReadsNoListeners(t *testing.T) {
	for _, flags := range []hostadmit.Flags{{}, enableUFW, blockListed} {
		h := home().answer(ufwBeforeMarker, ownerCaddy+sshdOn22)
		plan, err := hostadmit.Build(h, input(t, "vm", flags))
		if err != nil {
			t.Fatalf("%+v: %v", flags, err)
		}
		if plan.EnablesUFW || len(plan.UFWBlocks) != 0 || slices.ContainsFunc(h.ran, func(c string) bool { return strings.Contains(c, ufwBeforeMarker) }) {
			t.Errorf("%+v: an active ufw read what enabling it blocks:\n%s", flags, plan.Text())
		}
	}
}

// A listener that starts between the plan and its run is a port the
// owner never approved blocking: Execute, reading the host again once the
// user exists, stops before ufw is enabled and names it, although
// --enable-ufw-block-listed accepted what the plan listed.
func TestExecuteStopsOnAPortBlockedSinceThePlan(t *testing.T) {
	h := newBlank().with("ufw-active", "no").answer(ufwBeforeMarker, ownerCaddy+sshdOn22)
	in := input(t, "vm", blockListed)
	h.onRun = func(h *fakeHost, command string) {
		switch {
		case strings.Contains(command, registry.LockPath):
			h.file(registry.Path, registryOf(in.Request))
		case strings.HasPrefix(command, "install -d -m 755 -o root -g root /etc/paisans"):
			h.withDir("/etc/paisans", "drwxr-xr-x", 0, 0)
		case strings.Contains(command, "docker-ce-cli") && !strings.Contains(command, "docker compose version"):
			h.with("engine", "29.9.1").with("setuptool", "present").with("system-socket", "present").
				dropKeyed("pkg", "docker-ce")
			h.add("pkg", "docker-ce installed").add("pkg", "docker-ce-rootless-extras installed").add("pkg", "uidmap installed").
				answer("docker compose version", current().answers["docker compose version"])
		case strings.HasPrefix(command, "useradd "):
			h.with("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash").with("home", "dir 1001 /home/paisans-f2a9").
				with("groups", "paisans-f2a9").with("group", "paisans-f2a9").
				with("linger", "no").with("manager", "inactive").
				add("subuid", "paisans-f2a9:165536:65536").add("subgid", "paisans-f2a9:165536:65536")
			// The owner's database starts on a public address.
			h.answer(ufwBeforeMarker, `listen tcp LISTEN 0 4096 0.0.0.0:5432 0.0.0.0:* users:(("postgres",pid=990,fd=5)) uid:113`+"\n"+ownerCaddy+sshdOn22)
		}
	}
	plan, err := hostadmit.Build(h, in)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(plan.UFWBlocks, "80/tcp") || slices.Contains(plan.UFWBlocks, "5432/tcp") {
		t.Fatalf("the approved plan blocks %q", plan.UFWBlocks)
	}
	err = hostadmit.Execute(h, in, plan, ui.NewPlain(io.Discard, false))
	if err == nil || !strings.Contains(err.Error(), "5432/tcp") {
		t.Errorf("Execute = %v", err)
	}
	if slices.ContainsFunc(h.ran, func(c string) bool { return c == "ufw --force enable" }) {
		t.Error("ufw was enabled over a port the owner never approved blocking")
	}
}

// A UDP socket in the host's ephemeral port range is a client's, or a
// service's that picks its port as it starts: no allow can be added for
// it ahead, so it is listed under one key, with no line, and the refusal
// says the flag is the way through.
func TestEnableUFWListsEphemeralUDPWithoutALine(t *testing.T) {
	b := blocks(t, "portrange 32768\t60999\n"+
		`listen udp UNCONN 0 0 0.0.0.0:41234 0.0.0.0:* users:(("avahi-daemon",pid=612,fd=14)) uid:107`+"\n"+
		`listen udp UNCONN 0 0 0.0.0.0:51000 0.0.0.0:* users:(("rpc.statd",pid=700,fd=8))`+"\n"+sshdOn22)
	if b == nil {
		t.Fatal("admit plans to enable ufw over UDP sockets in the ephemeral range")
	}
	text := b.Error()
	for _, want := range []string{"UDP in the ephemeral range 32768:60999: avahi-daemon (pid 612, uid 107) listens on 0.0.0.0:41234/udp; rpc.statd", "cannot be allowed ahead", "fixed port", "--enable-ufw-block-listed"} {
		if !strings.Contains(text, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "sudo ufw allow") {
		t.Errorf("an allow line for an ephemeral port:\n%s", text)
	}
	// A UDP port below the range is listed with its line.
	if b := blocks(t, "portrange 32768\t60999\n"+`listen udp UNCONN 0 0 0.0.0.0:5355 0.0.0.0:* users:(("systemd-resolve",pid=600,fd=14)) uid:991`+"\n"+sshdOn22); b == nil || !strings.Contains(b.Error(), "sudo ufw allow 5355/udp") {
		t.Errorf("LLMNR: %v", b)
	}
}

// ufw's before.rules let mDNS and SSDP in to their multicast groups
// alone: a socket bound to the group is covered, one on every address
// takes unicast too, which ufw blocks, so it is listed. Its IPv6 is listed
// apart when ufw's IPv6 is off, as any listener's.
func TestOnlyMulticastDiscoveryIsCoveredByUFW(t *testing.T) {
	group := `listen udp UNCONN 0 0 224.0.0.251:5353 0.0.0.0:* users:(("avahi-daemon",pid=612,fd=12)) uid:107` + "\n" +
		`listen udp UNCONN 0 0 239.255.255.250:1900 0.0.0.0:* users:(("minissdpd",pid=613,fd=3))` + "\n"
	if b := blocks(t, group+sshdOn22); b != nil {
		t.Errorf("sockets bound to the multicast groups: %v", b)
	}
	if b := blocks(t, `listen udp UNCONN 0 0 0.0.0.0:5353 0.0.0.0:* users:(("avahi-daemon",pid=612,fd=12)) uid:107`+"\n"+sshdOn22); b == nil || !strings.Contains(b.Error(), "5353/udp: avahi-daemon") {
		t.Errorf("mDNS on every address: %v", b)
	}
	v6off := `listen tcp LISTEN 0 4096 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=812,fd=3))` + "\nipv6 no\nsystemd-run present\nfallback not-found not-found\nend\n"
	if b := blocks(t, `listen udp UNCONN 0 0 [fe80::5054:ff:fe12:3456]%eth0:546 [::]:* users:(("systemd-network",pid=500,fd=21)) uid:998`+"\n"+v6off); b == nil || !strings.Contains(b.Error(), "546/udp over IPv6") {
		t.Errorf("DHCPv6 with ufw's IPv6 off: %v", b)
	}
}

// With --enable-ufw-block-listed the plan warns what ufw blocks, so a
// script's log records it.
func TestTheBlockListedFlagWarnsWhatItBlocks(t *testing.T) {
	plan, err := hostadmit.Build(inactiveUFW(ownerCaddy+sshdOn22)(), input(t, "vm", blockListed))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(plan.Warnings, func(w hostprep.Warning) bool {
		return strings.Contains(w.Hint, "ufw blocks 80/tcp, 443/tcp, 443/udp") && strings.Contains(w.Detail, "caddy (pid 900")
	}) {
		t.Errorf("no warning names what ufw blocks:\n%s", plan.Text())
	}
}

// web80 is a container the system Docker publishes on 80, to the
// container's 80 at 172.17.0.2, on a host whose Docker is not on its
// iptables backend.
const web80 = "container web-1\t0.0.0.0:80->80/tcp\tweb\n" +
	"dnat table ip nat chain DOCKER: iifname != \"docker0\" tcp dport 80 counter packets 0 bytes 0 dnat to 172.17.0.2:80\n"

// The route allow as ufw records it on a real host, comment included,
// keeps the publish reachable.
func TestARouteAllowAsUFWRecordsItKeepsAPublishReachable(t *testing.T) {
	plan, err := hostadmit.Build(inactiveUFW("rule route allow proto tcp to 172.17.0.2 port 80 comment 'paisans-test'\n"+
		"tuple4 route:allow tcp 80 172.17.0.2 any 0.0.0.0/0 in comment=70616973616e732d74657374\n"+web80+sshdOn22)(), input(t, "vm", enableUFW))
	if err != nil {
		t.Fatal(err)
	}
	if d := plan.Steps[titledStep(plan, "enable ufw")].Describe; !strings.Contains(d, "80/tcp (your Docker's container web-1") || !strings.Contains(d, "`ufw route allow") {
		t.Errorf("the enable step:\n%s", d)
	}
}

// One listener on IPv6 alone, with ufw's IPv6 off, and one on IPv4 that
// the owner's rule allows: the IPv6 one is listed apart, and no allow line
// is printed for a rule that exists. A dual-stack listener allowed for one
// family gets the line for the other alone.
func TestAnAllowLineIsOnlyForAFamilyNoRuleAllows(t *testing.T) {
	v6off := `listen tcp LISTEN 0 4096 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=812,fd=3))` + "\nipv6 no\nsystemd-run present\nfallback not-found not-found\nend\n"
	b := blocks(t, "rule allow 8080/tcp\ntuple4 allow tcp 8080 0.0.0.0/0 any 0.0.0.0/0 in\n"+
		`listen tcp LISTEN 0 4096 [2001:db8::5]:8080 [::]:* users:(("app",pid=1,fd=3))`+"\n"+
		`listen tcp LISTEN 0 4096 0.0.0.0:8080 0.0.0.0:* users:(("app",pid=1,fd=4))`+"\n"+v6off)
	if b == nil || !strings.Contains(b.Error(), "8080/tcp over IPv6") || strings.Contains(b.Error(), "sudo ufw allow") {
		t.Errorf("IPv6 off: %v", b)
	}
	b = blocks(t, "rule allow to 0.0.0.0/0 port 8080 proto tcp\ntuple4 allow tcp 8080 0.0.0.0/0 any 0.0.0.0/0 in\n"+
		`listen tcp LISTEN 0 4096 *:8080 *:* users:(("app",pid=1,fd=3))`+"\n"+sshdOn22)
	if b == nil || !strings.Contains(b.Error(), "  sudo ufw allow proto tcp to ::/0 port 8080\n") || strings.Contains(b.Error(), "sudo ufw allow 8080/tcp") {
		t.Errorf("allowed over IPv4 alone: %v", b)
	}
}
