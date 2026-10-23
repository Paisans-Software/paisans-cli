package hostadmit_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

var memoryCases = addCases(
	// The witness on 458 MiB with no swap: under its 768 MiB, with the
	// swapfile admit offers named as a way out.
	goldenCase{name: "memory-short-no-swap", site: "vm", host: func() *fakeHost {
		return noSwap(vmAdmitted()).with("mem-total", "480247808").with("swap-total", "0")
	}},
)

func TestMemoryPlans(t *testing.T) { runGolden(t, memoryCases) }

// Admit's plan warns about a host under its roles' floor, and only then:
// swap that is on counts up to a quarter of the floor, and so does the
// swapfile the plan makes.
func TestMemoryWarning(t *testing.T) {
	const mib = 1 << 20
	n := func(v int64) string { return strconv.FormatInt(v, 10) }
	for _, c := range []struct {
		name  string
		site  string
		host  *fakeHost
		flags hostadmit.Flags
		warns bool
	}{
		{"witness, 1 GiB, no swap", "vm", noSwap(vmAdmitted()).with("mem-total", n(1024*mib)), hostadmit.Flags{}, false},
		{"witness, 512 MiB, no swap", "vm", noSwap(vmAdmitted()).with("mem-total", n(512*mib)), hostadmit.Flags{}, true},
		{"witness, 600 MiB and 1 GiB of swap on", "vm", vmAdmitted().with("mem-total", n(600*mib)).with("swap-total", n(1024*mib)), hostadmit.Flags{}, false},
		{"witness, 512 MiB and 4 GiB of swap on", "vm", vmAdmitted().with("mem-total", n(512*mib)).with("swap-total", n(4096*mib)), hostadmit.Flags{}, true},
		{"witness, 600 MiB, the swapfile this plan makes", "vm", noSwap(home()).with("mem-total", n(600*mib)), hostadmit.Flags{Swapfile: true}, false},
		{"witness, swap configured but not on", "vm", noSwap(vmAdmitted()).with("mem-total", n(600*mib)).with("swap-fstab", "yes").with("swap-total", "0"), hostadmit.Flags{}, true},
		{"data, apps and gateway, 4 GB", "home-a", home(), hostadmit.Flags{Watchdog: true, PublicPorts: true}, false},
		{"data, apps and gateway, 3 GiB", "home-a", home().with("mem-total", n(3072*mib)).with("swap-total", "0"), hostadmit.Flags{Watchdog: true, PublicPorts: true}, true},
		{"monitor behind its owner's web server, 768 MiB", "status", noSwap(home()).with("mem-total", n(768*mib)), hostadmit.Flags{}, false},
		{"monitor running Caddy, 768 MiB", "watch", noSwap(home()).with("mem-total", n(768*mib)), hostadmit.Flags{PublicPorts: true}, true},
		{"RAM unread", "vm", vmAdmitted().with("mem-total", ""), hostadmit.Flags{}, true},
	} {
		plan, err := hostadmit.Build(c.host, input(t, c.site, c.flags))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		warned := strings.Contains(plan.Text(), "warning: memory:")
		if warned != c.warns {
			t.Errorf("%s: memory warning shown = %v, want %v\n%s", c.name, warned, c.warns, plan.Text())
		}
	}
}

// The memory warning's advice names the way out the host has: turning on
// the swap it has but not on, or the swapfile admit offers.
func TestMemoryAdvice(t *testing.T) {
	for _, c := range []struct {
		name string
		host *fakeHost
		want string
	}{
		{"configured, not on", noSwap(vmAdmitted()).with("mem-total", "536870912").with("swap-fstab", "yes"), "turn on the swap it has (swapon -a"},
		{"none, offered", noSwap(vmAdmitted()).with("mem-total", "536870912"), "make a swapfile with --swapfile"},
		{"zram alone, offered", noSwap(vmAdmitted()).with("mem-total", "536870912").with("swap-active", "yes").with("swap-zram", "yes").with("swap-total", "1073741824").with("zram-on", "1073741824"), "make a swapfile with --swapfile"},
	} {
		plan, err := hostadmit.Build(c.host, input(t, "vm", hostadmit.Flags{}))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		text := plan.Text()
		i := strings.Index(text, "warning: memory:")
		if i < 0 || !strings.Contains(text[i:], c.want) {
			t.Errorf("%s: the memory warning lacks %q:\n%s", c.name, c.want, text)
		}
	}
}
