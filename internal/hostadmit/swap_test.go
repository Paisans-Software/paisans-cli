package hostadmit_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

// admitted is a host where vm is already admitted, whose plan, with no
// steps, is its warnings.
func vmAdmitted() *fakeHost { return admittedHost(requestFor("vm"), hostadmit.Flags{}) }

// noSwap is h with no swap in any form.
func noSwap(h *fakeHost) *fakeHost {
	return h.with("swap-active", "no").with("swap-fstab", "no").with("swapfile", "absent")
}

var swapCases = addCases(
	goldenCase{name: "swap-none-offered", site: "vm", host: func() *fakeHost { return noSwap(vmAdmitted()) }},
	goldenCase{name: "swap-swapfile", focus: []string{"swapfile"}, site: "vm", flags: hostadmit.Flags{Swapfile: true}, host: func() *fakeHost { return noSwap(home()) }},
	goldenCase{name: "swap-swapfile-capped", focus: []string{"swapfile"}, site: "vm", flags: hostadmit.Flags{Swapfile: true}, host: func() *fakeHost {
		return noSwap(home()).with("mem-total", "34359738368")
	}},
	goldenCase{name: "swap-configured-not-on", site: "vm", flags: hostadmit.Flags{Swapfile: true}, host: func() *fakeHost {
		return noSwap(vmAdmitted()).with("swap-partition", "yes").with("swap-fstab", "yes")
	}},
	goldenCase{name: "swap-btrfs", site: "vm", host: func() *fakeHost { return noSwap(vmAdmitted()).with("root-fs", "btrfs") }},
	goldenCase{name: "swap-btrfs-refused", site: "vm", flags: hostadmit.Flags{Swapfile: true}, host: func() *fakeHost { return noSwap(home()).with("root-fs", "btrfs") }},
	goldenCase{name: "swap-short-of-space-refused", site: "vm", flags: hostadmit.Flags{Swapfile: true}, host: func() *fakeHost {
		return noSwap(home()).with("root-avail", "10000000000")
	}},
)

func TestSwapPlans(t *testing.T) { runGolden(t, swapCases) }

// SwapOffer is what --swapfile would make, for the command to ask about:
// set only on a host with no swap where admit can make one and was not
// already told to.
func TestSwapOffer(t *testing.T) {
	for _, c := range []struct {
		name  string
		host  *fakeHost
		flags hostadmit.Flags
		want  int64
		warns bool
	}{
		{"none", noSwap(home()), hostadmit.Flags{}, 8 << 30, true},
		{"on", home(), hostadmit.Flags{}, 0, false},
		{"asked already", noSwap(home()), hostadmit.Flags{Swapfile: true}, 0, false},
		{"zram configured", noSwap(home()).with("swap-zram", "yes"), hostadmit.Flags{}, 0, true},
		{"xfs", noSwap(home()).with("root-fs", "xfs"), hostadmit.Flags{}, 8 << 30, true},
		{"zfs", noSwap(home()).with("root-fs", "zfs"), hostadmit.Flags{}, 0, true},
		{"RAM unread", noSwap(home()).with("mem-total", ""), hostadmit.Flags{}, 0, true},
	} {
		plan, err := hostadmit.Build(c.host, input(t, "vm", c.flags))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if plan.SwapOffer != c.want {
			t.Errorf("%s: SwapOffer = %d, want %d", c.name, plan.SwapOffer, c.want)
		}
		warned := strings.Contains(plan.Text(), "warning: swap:")
		if warned != c.warns {
			t.Errorf("%s: swap warning shown = %v", c.name, warned)
		}
	}
}
