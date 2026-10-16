package hostadmit_test

import (
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
