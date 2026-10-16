package hostadmit_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

var dataSite = hostadmit.Flags{Watchdog: true}

var watchdogCases = addCases(
	goldenCase{name: "watchdog-softdog", site: "home-b", flags: dataSite, host: homeB},
	goldenCase{name: "watchdog-hardware", site: "home-b", flags: dataSite, host: func() *fakeHost {
		return homeB().answer("RuntimeWatchdogUSec", "device present\nidentity watchdog0 iTCO_wdt\nsystemd 0\ndaemon absent\n")
	}},
	goldenCase{name: "watchdog-second-holder-refused", site: "home-b", flags: dataSite, host: func() *fakeHost {
		return homeB().add("watchdog-rule", "/etc/udev/rules.d/60-paisans-0c0d-watchdog.rules")
	}},
	goldenCase{name: "watchdog-systemd-holds-refused", site: "home-b", flags: dataSite, host: func() *fakeHost {
		return homeB().answer("RuntimeWatchdogUSec", "device present\nidentity watchdog0 iTCO_wdt\nsystemd 30s\ndaemon absent\n")
	}},
)

// homeB is a home host whose existing user is home-b's, community.
func homeB() *fakeHost {
	return home().with("passwd", "community:x:1002:1002::/home/community:/bin/bash").with("groups", "community").
		with("group", "community").add("subuid", "community:231072:65536").add("subgid", "community:231072:65536")
}

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
