package hostadmit_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

const ufwBeforeMarker = "paisans host admit: before enabling ufw"

// sshdOn22 is what the probe before enabling ufw prints on Ubuntu 24.04
// with sshd listening on 22, systemd-run there and no fallback loaded.
const sshdOn22 = `sshd LISTEN 0 4096 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=812,fd=3))` + "\n" +
	`sshd LISTEN 0 4096 [::]:22 [::]:* users:(("sshd",pid=812,fd=4))` + "\n" +
	"systemd-run present\nfallback not-found not-found\nend\n"

// sshdOn22And2222 adds 2222, which a socket-activated ssh.socket listens
// on for sshd.
const sshdOn22And2222 = "socket [::]:2222 (Stream)\n" + sshdOn22

// noSSHD is the probe on a host where nothing listens for sshd.
const noSSHD = "systemd-run present\nfallback not-found not-found\nend\n"

// inactiveUFW is home() with ufw installed and not active, and the probe
// before enabling it answered with answer.
func inactiveUFW(answer string) func() *fakeHost {
	return func() *fakeHost { return home().with("ufw-active", "no").answer(ufwBeforeMarker, answer) }
}

var enableUFW = hostadmit.Flags{EnableUFW: true}

var ufwCases = addCases(
	goldenCase{name: "firewall-ufw-inactive-two-ports-refused", site: "vm", host: inactiveUFW(sshdOn22And2222)},
	goldenCase{name: "firewall-ufw-inactive-no-sshd-refused", site: "vm", host: inactiveUFW(noSSHD)},
	goldenCase{name: "firewall-enable-ufw", site: "vm", flags: enableUFW, host: inactiveUFW(sshdOn22And2222)},
	goldenCase{name: "firewall-enable-ufw-owner-allow", site: "vm", flags: enableUFW, host: func() *fakeHost {
		return inactiveUFW("rule allow 22/tcp\n"+sshdOn22)().answer("ufw show added", "ufw present\nstatus inactive\nrule allow 22/tcp\n")
	}},
)

func TestEnableUFWPlans(t *testing.T) { runGolden(t, ufwCases) }

// Without --enable-ufw an inactive ufw refuses, naming the flag first, then
// the commands that enable it by hand in their order, each allow before the
// enable, and what enabling without them does. With no port sshd listens
// on read, no enable command is given at all.
func TestAnInactiveUFWRefusesNamingTheFlagAndTheSafeOrder(t *testing.T) {
	for _, c := range []struct {
		name, answer string
		want         []string
	}{
		{"one port", sshdOn22, []string{"--enable-ufw", "  1. sudo ufw allow 22/tcp\n  2. sudo ufw enable\n", "without step 1 cuts off SSH"}},
		{"two ports", sshdOn22And2222, []string{"--enable-ufw", "  1. sudo ufw allow 22/tcp\n  2. sudo ufw allow 2222/tcp\n  3. sudo ufw enable\n", "without steps 1 and 2 cuts off SSH"}},
		{"none", noSSHD, []string{"found no port sshd listens on", "cuts off SSH"}},
		{"unread", "unread listeners (ss -Hltnp)\n" + noSSHD, []string{"could not read the listeners (ss -Hltnp)", "cuts off SSH"}},
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
		plan.Steps[arm].Command != "systemd-run --on-active=600 --unit=paisans-ufw-fallback ufw disable" ||
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
		"owner denies 22":    {"rule deny 22/tcp\n" + sshdOn22, "blocks 22/tcp"},
		"owner rejects 2222": {"rule reject 2222\n" + sshdOn22And2222, "blocks 2222/tcp"},
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
	if !slices.ContainsFunc(plan.Warnings, func(w hostprep.Warning) bool { return strings.Contains(w.Hint, "ufw stays enabled") }) {
		t.Errorf("evict does not say ufw stays enabled:\n%s", plan.Text())
	}
}
