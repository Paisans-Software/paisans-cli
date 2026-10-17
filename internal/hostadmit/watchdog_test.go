package hostadmit_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

var dataSite = hostadmit.Flags{Watchdog: true}

var watchdogCases = addCases(
	goldenCase{name: "watchdog-softdog", focus: []string{"watchdog", "softdog"}, site: "home-b", flags: dataSite, host: homeB},
	goldenCase{name: "watchdog-hardware", focus: []string{"watchdog"}, site: "home-b", flags: dataSite, host: func() *fakeHost {
		return homeB().answer("RuntimeWatchdogUSec", "device present\nidentity watchdog0 iTCO_wdt\nsystemd 0\ndaemon absent\n")
	}},
	goldenCase{name: "watchdog-second-holder-refused", site: "home-b", flags: dataSite, host: func() *fakeHost {
		return homeB().add("watchdog-rule", "/etc/udev/rules.d/60-paisans-0c0d-watchdog.rules")
	}},
	goldenCase{name: "watchdog-systemd-holds-refused", site: "home-b", flags: dataSite, host: func() *fakeHost {
		return homeB().answer("RuntimeWatchdogUSec", "device present\nidentity watchdog0 iTCO_wdt\nsystemd 30s\ndaemon absent\n")
	}},
)

// homeB is a home host whose existing user is home-b's, community, with
// its own home.
func homeB() *fakeHost { return communityHome() }

func TestWatchdogPlans(t *testing.T) { runGolden(t, watchdogCases) }

// Withdrawing the grant removes the rule and gives each device node back to
// root, since udev never resets an owner.
func TestWithdrawnWatchdogIsGivenBack(t *testing.T) {
	rule := "/etc/udev/rules.d/60-paisans-f2a9-watchdog.rules"
	h := home().withFile(rule, "x\n", "644", 0)
	plan, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Steps {
		if strings.Contains(s.Command, "rm -f "+rule) {
			for _, want := range []string{"/dev/watchdog[0-9]*", "chown root:root", "chmod 600"} {
				if !strings.Contains(s.Command, want) {
					t.Errorf("the take-back does not run %q: %s", want, s.Command)
				}
			}
			return
		}
	}
	t.Fatal("the rule was not removed")
}

// What admit says about the watchdog is admit's: its refusals and the
// softdog warning say to admit again, and the module's unit is written by
// site admit and loads the module before systemd-logind starts the
// lingering users' managers, whose Docker starts Patroni; no system
// docker.service is involved.
func TestTheWatchdogIsAdmitsOwn(t *testing.T) {
	soft, _ := golden(t, goldenCase{name: "watchdog", host: homeB, site: "home-b", flags: dataSite})
	held, _ := golden(t, goldenCase{name: "watchdog", site: "home-b", flags: dataSite, host: func() *fakeHost {
		return homeB().answer("RuntimeWatchdogUSec", "device present\nidentity watchdog0 iTCO_wdt\nsystemd 30s\ndaemon absent\n")
	}})
	for _, text := range []string{soft, held} {
		if strings.Contains(text, "prepare again") || !strings.Contains(text, "admit again") {
			t.Errorf("the watchdog's words are site prepare's:\n%s", text)
		}
	}
	for _, want := range []string{"# Written by paisans site admit. Loads softdog at boot", "Before=systemd-logind.service"} {
		if !strings.Contains(soft, want) {
			t.Errorf("the softdog unit lacks %q", want)
		}
	}
	if strings.Contains(soft, "docker.service") {
		t.Error("the softdog unit orders itself before the system's docker.service")
	}
}

// The rule matches the watchdog nodes by their subsystems as well as
// their names. A rule as written is present only while each node the probe
// read is the user's; one that is not is triggered again.
func TestTheWatchdogRuleAndItsDevices(t *testing.T) {
	plan, err := hostadmit.Build(homeB(), input(t, "home-b", dataSite))
	if err != nil {
		t.Fatal(err)
	}
	rule := "/etc/udev/rules.d/60-paisans-f2a9-watchdog.rules"
	var content string
	for _, s := range plan.Steps {
		if s.File != nil && s.File.Path == rule {
			content = s.File.Content
		}
	}
	if !strings.Contains(content, `SUBSYSTEM=="watchdog|misc", KERNEL=="watchdog*", OWNER="community"`) {
		t.Fatalf("the rule is %q", content)
	}
	written := func() *fakeHost { return homeB().withFile(rule, content, "644", 0) }
	for _, c := range []struct {
		name    string
		host    *fakeHost
		trigger bool
	}{
		{"no node yet", written(), false},
		{"the user's nodes", written().add("watchdog-dev", "1002 /dev/watchdog").add("watchdog-dev", "1002 /dev/watchdog0"), false},
		{"a node still root's", written().add("watchdog-dev", "1002 /dev/watchdog").add("watchdog-dev", "0 /dev/watchdog0"), true},
	} {
		plan, err := hostadmit.Build(c.host, input(t, "home-b", dataSite))
		if err != nil {
			t.Fatal(err)
		}
		var triggered, rewrote bool
		for _, s := range plan.Steps {
			triggered = triggered || strings.HasPrefix(s.Title, "give the watchdog to")
			rewrote = rewrote || s.File != nil && s.File.Path == rule
		}
		if triggered != c.trigger || rewrote {
			t.Errorf("%s: triggered %v, rewrote the rule %v", c.name, triggered, rewrote)
		}
	}
}
